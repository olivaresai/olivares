// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package evals

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
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

// listCap bounds an internal List page; it matches the store's own maximum.
const listCap = 1000

// Bounds on caller-supplied strings (docs/SECURITY-HARDENING.md — minimal data). Every value is
// clamped before it reaches Create so a fixture, label or ref cannot grow unbounded.
const (
	maxNameLen    = 200
	maxRefLen     = 1024
	maxFixtureLen = 8192
	maxLabelLen   = 200
	maxReqBytes   = 1 << 20 // 1 MiB cap on a request body
)

// listResponse is the paginated envelope every list endpoint returns: the ONE
// engine-wide shape (items + opaque cursor + has_more), aliased rather than
// re-declared so an empty page can never serialize as `{"items":null}` here
// while it serializes as `{"items":[]}` next door (core/api/listresponse.go).
type listResponse[T any] = api.ListResponse[T]

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if v != nil {
		_ = json.NewEncoder(w).Encode(v)
	}
}

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

func isNotFound(err error) bool { return errors.Is(err, store.ErrNotFound) }

func isConflict(err error) bool { return errors.Is(err, store.ErrConflict) }

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

func eq(col string, val any) model.Filter {
	return model.Filter{Column: col, Op: model.OpEq, Value: val}
}

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

// collectAll pages a typed Repository fully (the typed twin of listAll, which only
// works on a GenericRepo). It loops the keyset cursor so a count/sum aggregation over
// a core entity is never silently truncated at the store's max page — pass a bound
// repo method, e.g. collectAll(ctx, sc.Findings().List, eq(...)).
func collectAll[T any](ctx context.Context, list func(context.Context, model.Query) ([]T, model.Page, error), filters ...model.Filter) ([]T, error) {
	var out []T
	q := model.Query{Filters: filters, Limit: listCap}
	for {
		recs, page, err := list(ctx, q)
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

// auditEvent appends an evals self-audit attributed to the REAL principal in the
// caller's transaction. Launching a run/A-B/monitor, pinning a baseline and opening
// a stream are privileged and auditable (docs/SECURITY-HARDENING.md).
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

func hashHex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func hashBytes(s string) []byte {
	sum := sha256.Sum256([]byte(s))
	return sum[:]
}

func idParam(s string) (model.ID, bool) {
	id, err := model.ParseID(strings.TrimSpace(s))
	return id, err == nil && !id.IsZero()
}

// formatFloat renders a metric for a hash preimage / audit detail (fixed 4 decimals,
// deterministic).
func formatFloat(f float64) string {
	return strconv.FormatFloat(f, 'f', 4, 64)
}

func clamp(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// encodeJSONMap encodes a small metadata map to a canonical JSON string for a
// KindJSON column (the generic repo stores the value verbatim), or nil when empty so
// the column is NULL.
func encodeJSONMap(m map[string]any) any {
	if len(m) == 0 {
		return nil
	}
	b, err := json.Marshal(m)
	if err != nil {
		return nil
	}
	return string(b)
}

// decodeJSONMap decodes a KindJSON column's text back into a map (nil on empty/bad
// input). A KindJSON column scans back as the stored JSON text.
func decodeJSONMap(s string) map[string]any {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		return nil
	}
	return m
}

// parseIDOrZero returns ref as a model.ID when it is a canonical UUID, else zero
// (the subject ref is often an external name, kept in Metadata["subject_ref"]).
func parseIDOrZero(ref string) model.ID {
	if id, err := model.ParseID(ref); err == nil {
		return id
	}
	return ""
}

// sevToCore maps the 5-value wire severity onto the 4-value persisted severity
// (info → low); an unknown value collapses to low (fail-safe).
func sevToCore(s sdkmodel.Severity) model.Severity {
	switch s {
	case sdkmodel.SeverityCritical:
		return model.SeverityCritical
	case sdkmodel.SeverityHigh:
		return model.SeverityHigh
	case sdkmodel.SeverityMedium:
		return model.SeverityMedium
	default:
		return model.SeverityLow
	}
}
