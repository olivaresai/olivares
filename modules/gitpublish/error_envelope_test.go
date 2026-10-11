// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package gitpublish

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/api"
)

func TestGitPublishErrorEnvelopeHTTP(t *testing.T) {
	h := newHarness(t)
	push := handlerFor(t, h.m, http.MethodPost, "/targets/{id}/pushes")
	c := h.user()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		push(w, r, api.ModuleContext{Principal: c.Principal, Tenant: c.Tenant})
	}))
	defer server.Close()
	response, err := server.Client().Post(server.URL, "application/json", strings.NewReader("{"))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var body struct {
		Error struct{ Code, Message string }
	}
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusBadRequest || body.Error.Code != "invalid_request" || body.Error.Message != "invalid_request" {
		t.Fatalf("response = %d %+v", response.StatusCode, body)
	}
	if response.Header.Get("Cache-Control") != "no-store" || response.Header.Get("Content-Type") != "application/json" {
		t.Fatalf("headers = %v", response.Header)
	}
}
