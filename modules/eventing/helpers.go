// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package eventing

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// listCap bounds an internal List page; it matches the store's own maximum.
const listCap = 1000

// Bounds on caller-supplied strings so a single field cannot balloon the store
// or the audit Meta (docs/SECURITY-HARDENING.md).
const (
	maxNameLen     = 200
	maxDescLen     = 1024
	maxEndpointLen = 2048
	maxTypeCount   = 32
	maxSourceLen   = 200
	maxReqBytes    = 1 << 20 // 1 MiB cap on a request body (mirrors the core API)
)

// listResponse is the paginated envelope every list endpoint returns: the ONE
// engine-wide shape (items + opaque cursor + has_more), aliased rather than
// re-declared so an empty page can never serialize as `{"items":null}` here
// while it serializes as `{"items":[]}` next door (core/api/listresponse.go).
type listResponse[T any] = api.ListResponse[T]

var writeJSON = api.WriteJSON

// errorBody is the small error envelope module endpoints return.
var errorBody = api.ModuleErrorBody

// errorBodyCode is the error envelope with a stable machine code.
func errorBodyCode(code, msg string) map[string]any {
	return map[string]any{"error": map[string]string{"code": code, "message": msg}}
}

// writeStoreError maps a store error to an HTTP status. Everything except this
// module's own two refusals is api.StoreErrorStatus (core/api/moduleerrors.go),
// the ONE mapping the whole product shares — see the note there for what the
// thirty-six hand-written copies had drifted into.
func writeStoreError(w http.ResponseWriter, err error) {
	switch {
	case err == nil:
		writeJSON(w, http.StatusOK, nil)
	case errors.Is(err, errNoSealer):
		// An operator wiring gap, not a caller mistake: visible and actionable.
		writeJSON(w, http.StatusServiceUnavailable, errorBody("secret storage is not configured on this deployment"))
	case isEgressWriterFenceRefusal(err):
		// Unit H. The fence refuses a write whose proof does not match the CURRENT disposition,
		// and on this path there is exactly one way that happens to a binary which carries the gate:
		// an arming moved the generation between reading it and writing. That is retryable, and it
		// is the caller's next attempt that resolves it.
		//
		// Before this, the refusal fell through to a generic 500 "internal error" while the design
		// and the code comments described a writer that "is refused and retries" — a claim the
		// implementation contrast correctly called inaccurate. A 503 with the reason is what makes
		// the sentence true.
		writeJSON(w, http.StatusServiceUnavailable, errorBody("the egress writer fence's disposition changed while this write was in flight; retry"))
	default:
		status, body, _ := api.StoreErrorBody(err)
		writeJSON(w, status, body)
	}
}

// decodeJSON reads a JSON body into v, bounding the read and rejecting unknown
// fields so a client cannot smuggle a value into an undeclared field.
func decodeJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := api.DecodeRequestBody(w, r, v, api.RequestBodySpec{MaxBytes: maxReqBytes}); err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody(api.RequestBodyErrorMessage(err, "invalid JSON body")))
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

// eq, gte and lte are shorthands for List filters (canonical RFC3339 timestamps
// compare lexically, so they work for time windows too).
func eq(col string, val any) model.Filter {
	return model.Filter{Column: col, Op: model.OpEq, Value: val}
}

func gte(col string, val any) model.Filter {
	return model.Filter{Column: col, Op: model.OpGte, Value: val}
}

func lte(col string, val any) model.Filter {
	return model.Filter{Column: col, Op: model.OpLte, Value: val}
}

func lt(col string, val any) model.Filter {
	return model.Filter{Column: col, Op: model.OpLt, Value: val}
}

// auditEvent appends a self-audit event attributed to the REAL principal, in
// the caller's transaction, so the ledger records WHO changed which
// subscription (docs/SECURITY-HARDENING.md) — never the system actor and never a secret (Meta
// carries ids, names and counts only).
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

// hashHex returns the hex SHA-256 of b — used for the non-secret secret
// fingerprint (docs/SECURITY-HARDENING.md: content is never stored raw).
func hashHex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// chiID reads the {id} path param as a validated model.ID.
func chiID(r *http.Request) (model.ID, bool) {
	id := model.ID(strings.TrimSpace(chi.URLParam(r, "id")))
	return id, !id.IsZero()
}

// tenantOf resolves an event's string tenant reference to a usable business
// TenantID, or false to skip (placeholder label or the system tenant).
func tenantOf(ref string) (model.TenantID, bool) {
	t, err := model.ParseTenantID(ref)
	if err != nil || t.IsZero() || t.IsSystem() {
		return "", false
	}
	return t, true
}

// csvJoin renders a value set as a comma-separated, trimmed, de-duplicated
// string. Empty entries are dropped.
func csvJoin(vals []string) string {
	seen := map[string]bool{}
	out := make([]string, 0, len(vals))
	for _, v := range vals {
		v = strings.TrimSpace(v)
		if v == "" || seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	return strings.Join(out, ",")
}

// csvSplit parses a comma-separated value set, dropping empties.
func csvSplit(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// csvContains reports whether the csv value set contains v (or is empty, which
// means "match all").
func csvContains(csv, v string) bool {
	if csv == "" {
		return true
	}
	for _, p := range csvSplit(csv) {
		if p == v {
			return true
		}
	}
	return false
}

// validationError marks a caller mistake distinguishable from a store failure.
type validationError string

func (e validationError) Error() string { return string(e) }

// asValidation unwraps a validationError, if err is one.
func asValidation(err error) (string, bool) {
	var v validationError
	if errors.As(err, &v) {
		return string(v), true
	}
	return "", false
}
