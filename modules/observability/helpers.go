// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package observability

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/olivaresai/olivares/core/api"
)

// listResponse is the paginated envelope every list endpoint returns: the ONE
// engine-wide shape (items + opaque cursor + has_more), aliased rather than
// re-declared so an empty page can never serialize as `{"items":null}` here
// while it serializes as `{"items":[]}` next door (core/api/listresponse.go).
type listResponse[T any] = api.ListResponse[T]

// writeJSON writes v as a JSON response.
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

// rfc3339 formats t for the wire: RFC3339 UTC, with sub-second precision kept
// when present (RFC3339Nano trims trailing zeros, so whole seconds stay
// compact). The zero instant formats to "" so an omitempty field stays absent
// rather than claiming the epoch.
func rfc3339(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}

// isLowerHex reports whether s is exactly n lowercase hex characters (the W3C
// trace-id/span-id forms; an uppercase or odd-length id is invalid, not
// normalized — the ledger stamps canonical lowercase).
func isLowerHex(s string, n int) bool {
	if len(s) != n {
		return false
	}
	return isLowerHexLoose(s)
}

// isLowerHexLoose reports whether s is non-empty lowercase hex of any length
// (path-input validation; length is bounded by the caller).
func isLowerHexLoose(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// clampStr truncates s to AT MOST n runes including the ellipsis marker (never
// bytes, so a multibyte rune is never split) — the documented caps are real
// ceilings, not cap-plus-one.
func clampStr(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	if n < 1 {
		return ""
	}
	return string(r[:n-1]) + "…"
}

// warnf logs at warn level if a logger is set (handlers can run in unit tests
// before Init wires one).
func (m *Module) warnf(msg string, args ...any) {
	if m.log != nil {
		m.log.Warn(msg, args...)
	}
}
