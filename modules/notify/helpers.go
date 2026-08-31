// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package notify

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

	"github.com/go-chi/chi/v5"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

// listCap bounds an internal List page; it matches the store's own maximum.
const listCap = 1000

// Bounds on operator/caller-supplied strings so a single field cannot balloon the
// store or the audit Meta (docs/SECURITY-HARDENING.md).
const (
	maxNameLen  = 200
	maxRefLen   = 1024
	maxReqBytes = 1 << 20 // 1 MiB cap on a request body (mirrors the core API)
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

// writeStoreError maps a store error to an HTTP status. Everything except this
// module's own standby wording is api.StoreErrorStatus (core/api/moduleerrors.go),
// the ONE mapping the whole product shares — see the note there for what the
// thirty-six hand-written copies had drifted into.
func writeStoreError(w http.ResponseWriter, err error) {
	switch {
	case err == nil:
		writeJSON(w, http.StatusOK, nil)
	case errors.Is(err, store.ErrNotLeader):
		// KEPT LOCAL: an HA standby declining a write (e.g. the route-test claim)
		// is retryable against the leader, not an internal fault. The shared mapping
		// agrees on the 503 and says "not leader"; this module's sentence tells the
		// caller what to DO, so it stays until someone decides that wording for all
		// thirty-six at once.
		writeJSON(w, http.StatusServiceUnavailable, errorBody("not the active writer; retry against the leader"))
	default:
		status, msg, _ := api.StoreErrorStatus(err)
		writeJSON(w, status, errorBody(msg))
	}
}

// decodeJSON reads a JSON body into v, bounding the read and rejecting unknown
// fields so a client cannot smuggle a value into an undeclared field.
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

// gte is a shorthand for a greater-than-or-equal filter (used for time windows;
// canonical RFC3339 timestamps compare lexically).
func gte(col string, val any) model.Filter {
	return model.Filter{Column: col, Op: model.OpGte, Value: val}
}

// lte is a shorthand for a less-than-or-equal filter (the outbox due-scan: a row is
// due when next_attempt_at <= now; canonical timestamps compare lexically).
func lte(col string, val any) model.Filter {
	return model.Filter{Column: col, Op: model.OpLte, Value: val}
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

// listAll drains every page of a module-entity query (bounded by listCap per
// page).
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

// auditEvent appends a self-audit event attributed to the REAL principal, in the
// caller's transaction, so the ledger records WHO changed which route (docs/SECURITY-HARDENING.md)
// — never the system actor and never a secret (Meta carries ids and counts only).
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

// hashHex returns the hex SHA-256 of s — a one-way correlation fingerprint (the
// dedup key). Content is never stored raw (docs/SECURITY-HARDENING.md).
func hashHex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// idParam reads and validates a path id parameter.
func idParam(s string) (model.ID, bool) {
	id := model.ID(strings.TrimSpace(s))
	return id, !id.IsZero()
}

// chiID reads the {id} path param as a validated model.ID.
func chiID(r *http.Request) (model.ID, bool) {
	return idParam(chi.URLParam(r, "id"))
}

// clamp truncates s to n runes (never bytes), appending an ellipsis marker if it
// was cut. Used to bound operator prose and refs.
func clamp(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
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

// csvJoin renders a value set as a comma-separated, trimmed, de-duplicated string
// for the match_* columns. Empty entries are dropped.
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

// globMatchAny reports whether any glob in the csv set matches s (or the set is
// empty, meaning "match all"). A glob is either an exact string or a "prefix*"
// (trailing-star) pattern — enough to express "health_*" without a regex engine.
func globMatchAny(csv, s string) bool {
	if csv == "" {
		return true
	}
	for _, pat := range csvSplit(csv) {
		if globMatch(pat, s) {
			return true
		}
	}
	return false
}

// globMatch matches a single exact-or-trailing-star pattern.
func globMatch(pat, s string) bool {
	if strings.HasSuffix(pat, "*") {
		return strings.HasPrefix(s, strings.TrimSuffix(pat, "*"))
	}
	return pat == s
}

// parseSeverity parses a stored min-severity string to the shared scale; an empty
// or unknown value floors at info (so an unset threshold matches every finding).
func parseSeverity(s string) sdkmodel.Severity {
	switch sdkmodel.Severity(s) {
	case sdkmodel.SeverityCritical:
		return sdkmodel.SeverityCritical
	case sdkmodel.SeverityHigh:
		return sdkmodel.SeverityHigh
	case sdkmodel.SeverityMedium:
		return sdkmodel.SeverityMedium
	case sdkmodel.SeverityLow:
		return sdkmodel.SeverityLow
	default:
		return sdkmodel.SeverityInfo
	}
}

// validSeverity reports whether s is empty or one of the shared scale values.
func validSeverity(s string) bool {
	switch sdkmodel.Severity(s) {
	case "", sdkmodel.SeverityInfo, sdkmodel.SeverityLow, sdkmodel.SeverityMedium, sdkmodel.SeverityHigh, sdkmodel.SeverityCritical:
		return true
	default:
		return false
	}
}
