// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// rawReq is rawGet with the method as a parameter: this test's whole subject is
// what a NON-GET on a real path answers.
func rawReq(h *harness, method, path, token string, hdr map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, nil)
	req.RemoteAddr = "10.0.0.1:1234"
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.srv.Handler().ServeHTTP(rec, req)
	return rec
}

// THE ROUTER'S OWN TWO ANSWERS CARRY THE API'S ERROR SHAPE (2026-08-05).
//
// Measured against a live engine before this existed:
//
//	POST /v1/console/setup-status  -> 405, ZERO bytes, no Content-Type at all
//	GET  /v1/no-such-route         -> 404, text/plain "404 page not found"
//
// Neither is the {"error":{"code","message"}} every handler emits, and the 405 gave
// a client literally nothing to act on. It matters most exactly where a generated
// client goes wrong: three published operations pointed at URLs the router never
// matched, and what those calls got back was this empty 405.
func TestRouterErrorsUseTheAPIErrorShape(t *testing.T) {
	h := newHarness(t)
	// The engine must be past first-boot setup or the setup gate answers 409 before
	// the router is ever consulted — which would test the gate, not the router.
	admin := h.adminLogin()

	cases := []struct {
		name, method, path, wantCode string
		wantStatus                   int
	}{
		{"method not allowed on a real path", http.MethodPost, "/v1/console/setup-status", "method_not_allowed", http.StatusMethodNotAllowed},
		{"no route at that path", http.MethodGet, "/v1/no-such-route-exists", "not_found", http.StatusNotFound},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := rawReq(h, c.method, c.path, admin, nil)
			if rec.Code != c.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, c.wantStatus)
			}
			if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "application/json") {
				t.Errorf("Content-Type = %q, want JSON — an empty typeless body is nothing a client can parse", ct)
			}
			if rec.Body.Len() == 0 {
				t.Fatal("empty body: the client is told the request failed and nothing else")
			}
			var body struct {
				Error struct {
					Code    string `json:"code"`
					Message string `json:"message"`
				} `json:"error"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("body is not the API error shape (%v): %s", err, rec.Body.String())
			}
			if body.Error.Code != c.wantCode {
				t.Errorf("code = %q, want %q", body.Error.Code, c.wantCode)
			}
			if strings.TrimSpace(body.Error.Message) == "" {
				t.Error("a code with no message is half an answer")
			}
		})
	}
}
