// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package health

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
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

// Health state strings (the persisted/derived current state). They mirror the
// core HealthState enum (core/model/enums.go) so the mirror into HealthStatus is
// a direct map.
const (
	stateHealthy  = "healthy"
	stateDegraded = "degraded"
	stateDown     = "down"
	stateUnknown  = "unknown"
)

// stateObserved is a dependency-map node annotation ONLY: the subject was OBSERVED
// alive by an edge on this page (an MCP server just touched, or an agent that
// acted) but has NO declared health.check, so its health is not measured. It is the
// honest intermediate between "healthy" (a declared check signaled) and "unknown"
// (no signal at all) — without fabricating a measured-healthy state. It is NEVER
// written to colLastState and NEVER returned by /status or /checks (those rows are
// backed by a declared check by definition).
const stateObserved = "observed"

// Transition causes (what produced a state change) — recorded on the event row.
const (
	causeEdge   = "edge"   // liveness derived from edge.observed
	causeReport = "report" // an explicit probe result posted to /report
	causeSweep  = "sweep"  // the staleness sweep
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

// listAll drains every page of a module-entity query (bounded by listCap per
// page) so a scan over an entity's owned rows is complete.
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
// caller's transaction, so the append-only ledger records WHO did what to which
// entity (docs/SECURITY-HARDENING.md) — never the system actor and never a secret/payload (Meta
// carries ids and counts only).
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

// hashHex returns the hex SHA-256 of s — a one-way fingerprint of an
// already-redaction-safe detail, used for the on-bus FindingReport.DetailHash and
// the event/incident detail_hash. Content is never stored raw (docs/SECURITY-HARDENING.md).
func hashHex(s string) string {
	if s == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// idParam reads and validates a path id parameter.
func idParam(s string) (model.ID, bool) {
	id := model.ID(strings.TrimSpace(s))
	return id, !id.IsZero()
}

// parseIDOrZero returns ref as a model.ID when it is a canonical UUID, else zero.
// A subject is usually a natural ref (an MCP name, an agent external id), not an
// engine id; when it IS a core id the module mirrors the subject's state into the
// core HealthStatus entity (state.go), otherwise the mirror is skipped.
func parseIDOrZero(ref string) model.ID {
	if id, err := model.ParseID(ref); err == nil {
		return id
	}
	return ""
}

// clamp truncates s to n runes (never bytes, so a multibyte rune is never split),
// appending an ellipsis marker if it was cut. Used to bound operator prose.
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

// nonZeroTime returns t, or the clock's now when t is the zero instant.
func nonZeroTime(t time.Time, clock model.Clock) time.Time {
	if t.IsZero() {
		return clock.Now().Time()
	}
	return t
}

// advanceLast moves the col timestamp forward to at (canonical RFC3339 timestamps
// sort lexically, so a string compare is a valid chronological advance — it never
// rewinds last_seen on an out-of-order redelivery).
func advanceLast(rec model.Record, col string, at time.Time) {
	atTS := model.NewTimestamp(at).String()
	if cur := rec.String(col); cur == "" || cur < atTS {
		rec[col] = atTS
	}
}

// coreHealthState maps the module's current-state string to the core HealthState
// enum used by the mirrored HealthStatus entity. An unknown value fails closed to
// HealthUnknown rather than asserting health.
func coreHealthState(s string) model.HealthState {
	switch s {
	case stateHealthy:
		return model.HealthHealthy
	case stateDegraded:
		return model.HealthDegraded
	case stateDown:
		return model.HealthDown
	default:
		return model.HealthUnknown
	}
}
