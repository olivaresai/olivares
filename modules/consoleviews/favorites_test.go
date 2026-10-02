// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package consoleviews_test

import (
	"fmt"
	"net/http"
	"testing"
)

func favoriteIDs(r resp) []string {
	raw, _ := r.body["favorites"].([]any)
	out := []string{}
	for _, it := range raw {
		if m, ok := it.(map[string]any); ok {
			out = append(out, m["id"].(string))
		}
	}
	return out
}

// TestFavoritesFollowTheUser: every signed-in role (a viewer too) keeps its own favorites on
// the server; another user never sees them; they are not a saved view.
func TestFavoritesFollowTheUser(t *testing.T) {
	h := newHarness(t)
	root := h.rootLogin()
	ten := h.createOrg(root, "acme")
	alice := h.roleToken(root, ten, "alice@acme.io", "editor")
	carol := h.roleToken(root, ten, "carol@acme.io", "viewer")

	r := h.do("GET", "/v1/m/consoleviews/favorites", carol, nil, ten)
	if r.code != http.StatusOK || r.body["stored"] != false || len(favoriteIDs(r)) != 0 {
		t.Fatalf("first read = %d %s, want 200 with no stored favorites", r.code, r.raw)
	}
	put := map[string]any{"favorites": []map[string]any{{"kind": "feature", "id": "audit"}, {"kind": "utility", "id": "settings"}}}
	if r := h.do("PUT", "/v1/m/consoleviews/favorites", carol, put, ten); r.code != http.StatusOK {
		t.Fatalf("viewer save = %d %s, want 200", r.code, r.raw)
	}
	r = h.do("GET", "/v1/m/consoleviews/favorites", carol, nil, ten)
	if got := favoriteIDs(r); r.body["stored"] != true || len(got) != 2 || got[0] != "audit" || got[1] != "settings" {
		t.Fatalf("read back = %s, want audit then settings", r.raw)
	}
	// Replacing keeps one row, in the new order.
	put = map[string]any{"favorites": []map[string]any{{"kind": "feature", "id": "finops"}}}
	if r := h.do("PUT", "/v1/m/consoleviews/favorites", carol, put, ten); r.code != http.StatusOK {
		t.Fatalf("replace = %d %s", r.code, r.raw)
	}
	if got := favoriteIDs(h.do("GET", "/v1/m/consoleviews/favorites", carol, nil, ten)); len(got) != 1 || got[0] != "finops" {
		t.Fatalf("after replace = %v, want [finops]", got)
	}
	// Another user has their own (empty) list.
	if got := favoriteIDs(h.do("GET", "/v1/m/consoleviews/favorites", alice, nil, ten)); len(got) != 0 {
		t.Fatalf("alice sees %v, want none", got)
	}
	// Favorites are not a saved view: not listed, and the slug cannot be used for one.
	if lr := h.do("GET", "/v1/m/consoleviews/views", carol, nil, ten); len(items(lr)) != 0 {
		t.Fatalf("views list = %s, want no favorites row", lr.raw)
	}
	if r := h.do("POST", "/v1/m/consoleviews/views", alice, view("favorites", "mine", false), ten); r.code != http.StatusBadRequest {
		t.Fatalf("saved view on the reserved slug = %d, want 400", r.code)
	}
}

func TestFavoritesValidation(t *testing.T) {
	h := newHarness(t)
	root := h.rootLogin()
	ten := h.createOrg(root, "acme")
	alice := h.roleToken(root, ten, "alice@acme.io", "editor")
	// Every page the console offers fits; the list stays bounded.
	many := make([]map[string]any, 257)
	for i := range many {
		many[i] = map[string]any{"kind": "feature", "id": fmt.Sprintf("page%d", i)}
	}
	if r := h.do("PUT", "/v1/m/consoleviews/favorites", alice, map[string]any{"favorites": many[:256]}, ten); r.code != http.StatusOK {
		t.Errorf("256 favorites = %d %s, want 200", r.code, r.raw)
	}
	if r := h.do("PUT", "/v1/m/consoleviews/favorites", alice, map[string]any{"favorites": many}, ten); r.code != http.StatusBadRequest {
		t.Errorf("257 favorites = %d, want 400", r.code)
	}
	for name, body := range map[string]map[string]any{
		"bad kind":  {"favorites": []map[string]any{{"kind": "url", "id": "audit"}}},
		"bad id":    {"favorites": []map[string]any{{"kind": "feature", "id": "../x"}}},
		"duplicate": {"favorites": []map[string]any{{"kind": "feature", "id": "audit"}, {"kind": "feature", "id": "audit"}}},
	} {
		if r := h.do("PUT", "/v1/m/consoleviews/favorites", alice, body, ten); r.code != http.StatusBadRequest {
			t.Errorf("%s: %d %s, want 400", name, r.code, r.raw)
		}
	}
}
