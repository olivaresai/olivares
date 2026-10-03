// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A BODY IS ONE JSON DOCUMENT — the NHI rotate/offboard/finalize actions
// (2026-10-01). Their body is OPTIONAL (a bare POST is valid), and the module's
// decodeOptionalJSON returned success after the first document with no
// trailing-data rejection: `{...}{...}` and the stray-bracket tails were applied
// as the first value on three lifecycle-mutating routes. The shared helper
// (api.DecodeRequestBody) owns the property now; this test anchors it at the
// routes that had the defect.
//
// The bodies are RAW on purpose: a marshaled fixture cannot express a trailing
// value. The controls use a well-formed ref with no lifecycle row, so a valid
// body passes the decode and reaches the governed logic (the gate, or the
// finalize precondition) — while every malformed tail must answer 400 BEFORE
// the governed gate opens an approval or any lifecycle row changes.
func TestNHIActionsRefuseABodyThatIsNotOneJSONDocument(t *testing.T) {
	h := newHarness(t)
	tenant, tok := h.nhiTenant()

	// controlWant is the post-decode status of each route: the valid body must
	// pass the decode and reach the governed logic (the gate opens an approval
	// and this harness wires none → 503 no_gate; finalize evaluates its
	// soft-delete precondition → 409). A 400 on the control means the fixture,
	// not the decode, is wrong.
	routes := []struct {
		name, path  string
		controlWant int
	}{
		{"rotate", "/v1/m/governance/nhi/vault:approle:ghost/rotate", http.StatusServiceUnavailable},
		{"offboard", "/v1/m/governance/nhi/vault:approle:ghost/offboard", http.StatusServiceUnavailable},
		{"offboard finalize", "/v1/m/governance/nhi/vault:approle:ghost/offboard/finalize", http.StatusConflict},
	}
	const one = `{"reason":"maintenance window"}`

	raw := func(path, body string) (int, string) {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		req.RemoteAddr = "10.0.0.1:1234"
		req.Header.Set("Authorization", "Bearer "+tok)
		req.Header.Set("X-Olivares-Tenant", tenant.String())
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		h.srv.Handler().ServeHTTP(rec, req)
		return rec.Code, rec.Body.String()
	}

	for _, route := range routes {
		t.Run(route.name, func(t *testing.T) {
			// The control FIRST: a valid single document passes the decode and
			// reaches the governed logic.
			if code, body := raw(route.path, one); code != route.controlWant {
				t.Fatalf("control = %d %s, want %d — the fixture is not reaching the post-decode logic", code, body, route.controlWant)
			}
			for _, tc := range []struct{ name, tail string }{
				{"stray closing brace", `}`},
				{"stray closing bracket", `]`},
				{"newline bracket then object", "\n]{}"},
				{"newline brace then null", "\n}null"},
				{"a second value", `{"reason":"ghost"}`},
			} {
				code, body := raw(route.path, one+tc.tail)
				if code != http.StatusBadRequest {
					t.Errorf("%s = %d %s, want 400: a body that is not ONE document must be refused before the governed gate", tc.name, code, body)
				}
			}
		})
	}
}
