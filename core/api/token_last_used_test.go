// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api_test

import (
	"net/http"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
)

// TestTokenListShowsLastUseAfterAuthentication pins GET
// /v1/tokens, the listing `olivares tokens ls` and the console API keys tab
// render, carries last_used_at once the token has authenticated, and recording
// that use leaves the credential working.
func TestTokenListShowsLastUseAfterAuthentication(t *testing.T) {
	h := newHarness(t)
	root := h.adminLogin()
	tenant := h.createOrg(root, "token-last-used")
	issued := h.do("POST", "/v1/tokens", root, map[string]any{
		"name": "audit-ci", "tenant": tenant.String(), "role": auth.RoleViewer,
	}, nil)
	if issued.code != http.StatusCreated {
		t.Fatalf("issue token = %d %s", issued.code, issued.raw)
	}
	id, token := issued.body["id"].(string), issued.body["token"].(string)

	lastUsed := func() (string, bool) {
		t.Helper()
		r := h.do("GET", "/v1/tokens", root, nil, nil)
		if r.code != http.StatusOK {
			t.Fatalf("list tokens = %d %s", r.code, r.raw)
		}
		for _, raw := range r.body["items"].([]any) {
			item := raw.(map[string]any)
			if item["id"] == id {
				at, ok := item["last_used_at"].(string)
				return at, ok
			}
		}
		t.Fatalf("issued token %s is not listed", id)
		return "", false
	}

	if at, ok := lastUsed(); ok {
		t.Fatalf("a token never presented already lists last_used_at %q", at)
	}
	if r := h.do("GET", "/v1/auth/whoami", token, nil, nil); r.code != http.StatusOK || r.body["kind"] != "token" {
		t.Fatalf("whoami with the token = %d %v", r.code, r.body)
	}
	at, ok := lastUsed()
	if !ok {
		t.Fatal("GET /v1/tokens omits last_used_at after the token authenticated")
	}
	if _, err := model.ParseTimestamp(at); err != nil {
		t.Fatalf("last_used_at %q is not a timestamp: %v", at, err)
	}
	if r := h.do("GET", "/v1/auth/whoami", token, nil, nil); r.code != http.StatusOK {
		t.Fatalf("the token stopped authenticating after its use was recorded: %d %s", r.code, r.raw)
	}
}
