// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package voice

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"time"

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
	maxReqBytes = 1 << 22 // 4 MiB cap on a request body
)

// Recency windows for deriving a session's liveness at READ time, never stored, so
// the state is always accurate to the moment and the module never fabricates a
// stored lifecycle (a session ending is honest silence).
const (
	defaultActiveWindow = 2 * time.Minute
	defaultIdleWindow   = 30 * time.Minute
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

// decodeJSON reads a JSON body into v, bounding the read so a malformed or huge body
// cannot exhaust memory, and rejecting unknown fields so a client cannot smuggle a
// value into a field the typed DTO does not declare.
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

// setIf stores v under col only when non-empty (leaving a nullable column NULL).
func setIf(rec model.Record, col, v string) {
	if v != "" {
		rec[col] = v
	}
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

// listAll drains every page of a module-entity query (bounded by listCap per page).
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
// caller's transaction (docs/SECURITY-HARDENING.md,§5) — never the system actor and never a
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

// hashHex returns the hex SHA-256 of s — a one-way fingerprint of a redaction-safe
// ref (a transcript LOCATOR, the plan_hash, the on-bus DetailHash). Content is never
// stored raw (docs/SECURITY-HARDENING.md).
func hashHex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// hashBytes returns the raw 32-byte SHA-256 of s, for the []byte DetailHash column
// of a core Finding.
func hashBytes(s string) []byte {
	sum := sha256.Sum256([]byte(s))
	return sum[:]
}

// parseIDOrZero returns ref as a model.ID when it is a canonical UUID, else zero.
func parseIDOrZero(ref string) model.ID {
	if id, err := model.ParseID(ref); err == nil {
		return id
	}
	return ""
}

// clamp truncates s to n runes (never bytes), appending an ellipsis marker if cut.
func clamp(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// sevToCore maps the 5-value wire severity onto the 4-value persisted severity
// (info/unknown collapse to low).
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

// tenantOf resolves an event's string tenant reference to a usable business
// TenantID, or false to skip (placeholder label or the system tenant).
func tenantOf(ref string) (model.TenantID, bool) {
	t, err := model.ParseTenantID(ref)
	if err != nil || t.IsZero() || t.IsSystem() {
		return "", false
	}
	return t, true
}

// nonZeroTime returns t, or the clock's now when t is the zero instant.
func nonZeroTime(t time.Time, clock model.Clock) time.Time {
	if t.IsZero() {
		return clock.Now().Time()
	}
	return t
}

// advanceLast moves the col timestamp forward to at (canonical RFC3339 timestamps
// sort lexically, so this never rewinds on an out-of-order redelivery).
func advanceLast(rec model.Record, col string, at time.Time) {
	atTS := model.NewTimestamp(at).String()
	if cur := rec.String(col); cur == "" || cur < atTS {
		rec[col] = atTS
	}
}

// Derived session states (never stored; computed from last-activity recency).
const (
	stateLive  = "live"
	stateIdle  = "idle"
	stateEnded = "ended"
)

// deriveState maps a session's last-event recency to live/idle/ended at read time.
func deriveState(lastTS string, clock model.Clock, active, idle time.Duration) string {
	t, err := model.ParseTimestamp(lastTS)
	if err != nil {
		return stateEnded
	}
	switch d := clock.Now().Time().Sub(t.Time()); {
	case d <= active:
		return stateLive
	case d <= idle:
		return stateIdle
	default:
		return stateEnded
	}
}
