// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package consoleviews_test

import (
	"net/http"
	"testing"
)

// TestUIStateFollowsTheUser: the console's interface state (today the sidebar width) is the
// caller's own, kept beside their favorites; every signed-in role keeps its own, another user
// never sees it, and it is neither a saved view nor a favorites list.
func TestUIStateFollowsTheUser(t *testing.T) {
	h := newHarness(t)
	root := h.rootLogin()
	ten := h.createOrg(root, "acme")
	alice := h.roleToken(root, ten, "alice@acme.io", "editor")
	carol := h.roleToken(root, ten, "carol@acme.io", "viewer")

	r := h.do("GET", "/v1/m/consoleviews/ui-state", carol, nil, ten)
	if r.code != http.StatusOK || r.body["stored"] != false {
		t.Fatalf("first read = %d %s, want 200 with nothing stored", r.code, r.raw)
	}
	if r := h.do("PUT", "/v1/m/consoleviews/ui-state", carol, map[string]any{"sidebar": "rail"}, ten); r.code != http.StatusOK {
		t.Fatalf("viewer save = %d %s, want 200", r.code, r.raw)
	}
	r = h.do("GET", "/v1/m/consoleviews/ui-state", carol, nil, ten)
	if r.body["stored"] != true || r.body["sidebar"] != "rail" {
		t.Fatalf("read back = %s, want the rail", r.raw)
	}
	// Replacing keeps one row.
	if r := h.do("PUT", "/v1/m/consoleviews/ui-state", carol, map[string]any{"sidebar": "full"}, ten); r.code != http.StatusOK {
		t.Fatalf("replace = %d %s", r.code, r.raw)
	}
	if r := h.do("GET", "/v1/m/consoleviews/ui-state", carol, nil, ten); r.body["sidebar"] != "full" {
		t.Fatalf("after replace = %s, want full", r.raw)
	}
	// Another user has their own (nothing stored).
	if r := h.do("GET", "/v1/m/consoleviews/ui-state", alice, nil, ten); r.body["stored"] != false {
		t.Fatalf("alice sees %s, want nothing stored", r.raw)
	}
	// Not a saved view, not favorites, and the slug cannot be used for a view.
	if lr := h.do("GET", "/v1/m/consoleviews/views", carol, nil, ten); len(items(lr)) != 0 {
		t.Fatalf("views list = %s, want no ui-state row", lr.raw)
	}
	if got := favoriteIDs(h.do("GET", "/v1/m/consoleviews/favorites", carol, nil, ten)); len(got) != 0 {
		t.Fatalf("favorites = %v, want none", got)
	}
	if r := h.do("POST", "/v1/m/consoleviews/views", alice, view("ui-state", "mine", false), ten); r.code != http.StatusBadRequest {
		t.Fatalf("saved view on the reserved slug = %d, want 400", r.code)
	}
}

func TestUIStateValidation(t *testing.T) {
	h := newHarness(t)
	root := h.rootLogin()
	ten := h.createOrg(root, "acme")
	alice := h.roleToken(root, ten, "alice@acme.io", "editor")
	for name, body := range map[string]map[string]any{
		"unknown width": {"sidebar": "hidden"},
		"missing width": {},
		"unknown field": {"sidebar": "rail", "theme": "dark"},
	} {
		if r := h.do("PUT", "/v1/m/consoleviews/ui-state", alice, body, ten); r.code != http.StatusBadRequest {
			t.Errorf("%s: %d %s, want 400", name, r.code, r.raw)
		}
	}
}
