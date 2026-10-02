// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package orchestration

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A BODY IS ONE JSON DOCUMENT — the governed fire and run paths (2026-10-01).
// Their body is OPTIONAL, and the module's decodeOptionalJSON decoded the first
// document and returned success with NO trailing-data check: `{...}{...}` and the
// stray-bracket tails were applied as the first value — on the two routes that
// actuate production. The shared helper (api.DecodeRequestBody) now owns the
// property; this test is the behavioral anchor at the routes that had the defect.
//
// The bodies are RAW on purpose: a marshaled fixture cannot express a trailing
// value, so it cannot detect the defect. The controls go to ids that do not
// exist, so a valid body answers 404 from the store lookup — anything that is
// not 400 proves the decode passed — while every malformed tail must answer 400
// BEFORE any approval is opened or anything is actuated.
func TestGovernedFireAndRunRefuseABodyThatIsNotOneJSONDocument(t *testing.T) {
	h, _ := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	tok := h.roleToken(admin, tenant, "op@acme.io", "admin")

	const missing = "11111111-1111-1111-1111-111111111111"
	routes := []struct{ name, path string }{
		{"schedule fire", "/v1/m/orchestration/schedules/" + missing + "/fire"},
		{"workflow run", "/v1/m/orchestration/workflows/" + missing + "/run"},
	}
	const one = `{"approval_ref":"appr-1"}`

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
			// reaches the store (404 on the unknown id). A 400 here means the
			// fixture, not the decode, is wrong.
			if code, body := raw(route.path, one); code != http.StatusNotFound {
				t.Fatalf("control = %d %s, want 404 — the fixture is not reaching the store lookup", code, body)
			}
			for _, tc := range []struct{ name, tail string }{
				{"stray closing brace", `}`},
				{"stray closing bracket", `]`},
				{"newline bracket then object", "\n]{}"},
				{"newline brace then null", "\n}null"},
				{"a second value", `{"approval_ref":"appr-2"}`},
			} {
				code, body := raw(route.path, one+tc.tail)
				if code != http.StatusBadRequest {
					t.Errorf("%s = %d %s, want 400: a body that is not ONE document must be refused before any approval or actuation", tc.name, code, body)
				}
			}
		})
	}
}
