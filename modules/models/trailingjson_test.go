// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package models_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/olivaresai/olivares/modules/models"
)

// A REQUEST BODY IS ONE JSON DOCUMENT (2026-08-06).
//
// `json.Decoder.Decode` reads the FIRST value and stops. Every module's decodeJSON decoded
// once and returned success, so a body of two concatenated objects decoded the first,
// silently discarded the rest, and performed a durable mutation — measured against a live
// engine on this very route, which answered 201 Created and left a row a separate GET could
// read back. The same bytes sent to a core route answered 400, which is what made it drift
// rather than a property of encoding/json: core/api/render.go has called dec.More() since it
// was written, and 21 of the 22 streaming copies had lost it.
//
// The textual gate (scripts/check-json-decoders.sh) asserts the property across all 22 at
// once, because 21 packages cannot each grow a test like this one. THIS test is the
// behavioral anchor underneath it: a gate that only reads source can be satisfied by code
// that looks right, and one live route proving the refusal is what makes the other 21
// believable.
//
// It is written with a RAW body on purpose. The harness marshals from a Go value, which can
// only ever produce one document — a fixture that cannot express the defect cannot detect it.
func TestModuleRouteRefusesABodyThatIsTwoJSONDocuments(t *testing.T) {
	h := newHarness(t, models.New())
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")

	raw := func(body string) (int, string) {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "/v1/m/models/routing-policies", bytes.NewReader([]byte(body)))
		req.RemoteAddr = "10.0.0.1:1234"
		req.Header.Set("Authorization", "Bearer "+admin)
		req.Header.Set("X-Olivares-Tenant", tenant.String())
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		h.srv.Handler().ServeHTTP(rec, req)
		return rec.Code, rec.Body.String()
	}

	const one = `{"name":"single","enabled":false,"strategy":"cost"}`

	// The control FIRST: if the single-document form does not succeed, the fixture is wrong
	// and every refusal below would be a green measuring the wrong thing.
	if code, body := raw(one); code != http.StatusCreated {
		t.Fatalf("the control (one valid document) = %d %s, want 201 — this fixture is not exercising the route", code, body)
	}

	for _, tc := range []struct{ name, body string }{
		{"a second object after the first", one + `{"name":"ghost","enabled":true,"strategy":"cost"}`},
		{"a second object after a newline", one + "\n" + `{"name":"ghost2","strategy":"cost"}`},
		{"trailing garbage that is not JSON at all", one + ` not-json`},
		{"a second scalar", one + ` 42`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, body := raw(tc.body)
			if code != http.StatusBadRequest {
				t.Fatalf("= %d %s, want 400: a body that is not ONE document must be refused", code, body)
			}
		})
	}

	// AND THE EFFECT, not just the status. A 400 that still wrote a row would be the same
	// defect with a better error message, and the original report was believed only because
	// the created row was read back — so this reads back too.
	req := httptest.NewRequest(http.MethodGet, "/v1/m/models/routing-policies", nil)
	req.Header.Set("Authorization", "Bearer "+admin)
	req.Header.Set("X-Olivares-Tenant", tenant.String())
	rec := httptest.NewRecorder()
	h.srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("list = %d %s", rec.Code, rec.Body.String())
	}
	var list struct {
		Items []struct {
			Name string `json:"name"`
		} `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatalf("list body: %v (%s)", err, rec.Body.String())
	}
	for _, it := range list.Items {
		if it.Name != "single" {
			t.Errorf("a refused body still created %q; the rejection must precede the write", it.Name)
		}
	}
	if len(list.Items) != 1 {
		t.Errorf("routing policies = %d, want exactly the 1 the control created", len(list.Items))
	}
}
