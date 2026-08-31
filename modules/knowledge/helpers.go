// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package knowledge

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// listCap bounds an internal List page; it matches the store's own maximum.
const listCap = 1000

// Bounds on operator/source-supplied strings. Content is chunked and bounded so a
// single field cannot balloon the store or the audit Meta (docs/SECURITY-HARDENING.md).
const (
	maxNameLen     = 200
	maxRefLen      = 1024
	maxQueryLen    = 8192
	maxBodyBytes   = 1 << 20 // 1 MiB cap on a single inline document body
	maxReqBytes    = 1 << 22 // 4 MiB cap on a request body (inline ingest of several docs)
	maxInlineDocs  = 200     // hard cap on documents accepted in one inline ingest
	maxChunksPerKB = 100000  // honest scale ceiling for the store-backed exact index
	maxTemplateLen = 1 << 16 // 64 KiB cap on a prompt template
	maxContentLen  = 1 << 16 // 64 KiB cap on a memory entry
	maxTopK        = 100
	maxMemPerAgent = 10000 // quota: max memory entries per (agent) — a write-access DoS guard
	// maxScopeRefLen bounds the memory namespace refs (user_ref/session_ref).
	// Tighter than maxRefLen because knowledge_memory_scoped_uniq spans FIVE text
	// columns and a Postgres btree index tuple caps at ~2704 bytes: worst case
	// tenant(36) + agent(1024) + key(1024) + 2×200 + per-attr overhead stays
	// under the cap, where 2×1024 would not — validation must never accept a
	// write the engine then fails with an opaque 500.
	maxScopeRefLen = 200
)

// listResponse is the paginated envelope every list endpoint returns: the ONE
// engine-wide shape (items + opaque cursor + has_more), aliased rather than
// re-declared so an empty page can never serialize as `{"items":null}` here
// while it serializes as `{"items":[]}` next door (core/api/listresponse.go).
type listResponse[T any] = api.ListResponse[T]

// writeJSON writes v as a JSON response. Modules cannot reach the core API's
// unexported render helper, so each module owns a tiny equivalent.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if v != nil {
		_ = json.NewEncoder(w).Encode(v)
	}
}

// errorBody is the small error envelope module endpoints return.
func errorBody(msg string) map[string]any {
	return map[string]any{"error": map[string]string{"message": msg}}
}

// writeStoreError maps a store error to an HTTP status. THE MAPPING ITSELF IS NOT
// HERE: it is api.StoreErrorStatus (core/api/moduleerrors.go), which derives the
// status from the same statusFor that answers core/api's own routes. This module
// therefore cannot answer a sentinel differently from core, or from the other
// thirty-five copies of this function, and a sentinel added to statusFor tomorrow
// reaches this module without anyone editing it.
//
// That is not hypothetical: on 2026-08-12 four sentinels core/api had long mapped —
// tenant_suspended, tenant_not_in_service, not_leader and residency_violation —
// were absent from all but two of the thirty-six copies, so the same refusal was
// answered 423/503/403 by a core route and 500 "internal error" by every module
// route. The per-arm reasoning (ADR-0024 Q2 for the audit spool/B-03 for
// workspace confinement for the standby) now lives beside statusFor, once.
func writeStoreError(w http.ResponseWriter, err error) {
	if err == nil {
		writeJSON(w, http.StatusOK, nil)
		return
	}
	status, msg, _ := api.StoreErrorStatus(err)
	writeJSON(w, status, errorBody(msg))
}

// isNotFound reports the store's not-found sentinel.
func isNotFound(err error) bool { return errors.Is(err, store.ErrNotFound) }

// decodeJSON reads a JSON body into v, bounding the read so a malformed or huge
// body cannot exhaust memory, and rejecting unknown fields so a client cannot
// smuggle a value into a field the typed DTO does not declare.
func decodeJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(io.LimitReader(r.Body, maxReqBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid JSON body"))
		return false
	}
	// A BODY IS ONE JSON DOCUMENT (2026-08-06). Decode reads the FIRST value and stops,
	// so `{...}{...}` used to decode the first, silently discard the rest and perform a
	// durable mutation returning 201. Measured against a live engine on the models route,
	// with the created row read back by a separate GET; core/api/render.go has rejected
	// this since it was written, and 21 of the 22 copies of this helper had drifted from
	// it. A concatenation error becomes an apparently correct action, and two layers can
	// disagree about which document the request meant.
	if dec.More() {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid JSON body"))
		return false
	}
	return true
}

// listQuery builds a List query from ?limit and ?cursor.
func listQuery(r *http.Request) model.Query {
	q := model.Query{}
	if c := r.URL.Query().Get("cursor"); c != "" {
		q.Cursor = c
	}
	if l := r.URL.Query().Get("limit"); l != "" {
		if n, err := strconv.Atoi(l); err == nil {
			q.Limit = n
		}
	}
	return q
}

// eq is a shorthand for an equality filter.
func eq(col string, val any) model.Filter {
	return model.Filter{Column: col, Op: model.OpEq, Value: val}
}

// findOne returns the first row matching the AND of filters, or ok=false.
func findOne(ctx context.Context, repo store.GenericRepo, filters ...model.Filter) (model.Record, bool, error) {
	list, _, err := repo.List(ctx, model.Query{Filters: filters, Limit: 1})
	if err != nil {
		return nil, false, err
	}
	if len(list) == 0 {
		return nil, false, nil
	}
	return list[0], true, nil
}

// listAll drains every page of a module-entity query (bounded by listCap per page)
// so a scan over an entity's owned rows is complete.
func listAll(ctx context.Context, repo store.GenericRepo, filters ...model.Filter) ([]model.Record, error) {
	var out []model.Record
	q := model.Query{Filters: filters, Limit: listCap}
	for {
		recs, page, err := repo.List(ctx, q)
		if err != nil {
			return nil, err
		}
		out = append(out, recs...)
		if !page.HasMore || page.Cursor == "" {
			return out, nil
		}
		q.Cursor = page.Cursor
	}
}

// auditEvent appends a knowledge self-audit event attributed to the REAL principal,
// in the caller's transaction, so the append-only ledger records WHO did what to
// which knowledge entity (docs/SECURITY-HARDENING.md,§5) — never the system actor and never a
// secret/payload (Meta carries ids and counts only).
func auditEvent(ctx context.Context, sc store.Scope, mc api.ModuleContext, action string, kind model.Kind, id model.ID, meta map[string]any) error {
	_, err := sc.Audit().Append(ctx, model.AuditDraft{
		Actor:      mc.Principal.Actor(),
		ActorKind:  mc.Principal.ActorKind(),
		Action:     action,
		TargetKind: kind,
		TargetID:   id,
		Meta:       meta,
	})
	return err
}

// hashHex returns the hex SHA-256 of s. It is a content fingerprint (dedup/change
// detection / lineage reference), never a place a secret leaks: content is
// redacted before it is ever hashed for storage.
func hashHex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// marshalStrings serializes a []string to a JSON string for a KindJSON column.
func marshalStrings(ss []string) string {
	if len(ss) == 0 {
		return "[]"
	}
	b, err := json.Marshal(ss)
	if err != nil {
		return "[]"
	}
	return string(b)
}

// unmarshalStrings parses a KindJSON []string column value, tolerating null/empty.
func unmarshalStrings(rec model.Record, col string) []string {
	s := rec.String(col)
	if strings.TrimSpace(s) == "" {
		return nil
	}
	var out []string
	if err := json.Unmarshal([]byte(s), &out); err != nil {
		return nil
	}
	return out
}

// marshalJSON serializes v to a JSON string for a KindJSON column ("null" on error
// is never produced — an error yields "{}"/"[]"-shaped empty per the caller).
func marshalJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "null"
	}
	return string(b)
}

// unmarshalInto parses a JSON string into v.
func unmarshalInto(s string, v any) error { return json.Unmarshal([]byte(s), v) }

// idParam reads and validates a path id parameter.
func idParam(s string) (model.ID, bool) {
	id := model.ID(strings.TrimSpace(s))
	return id, !id.IsZero()
}

// itoa formats an int64 as a decimal string.
func itoa(n int64) string { return strconv.FormatInt(n, 10) }
