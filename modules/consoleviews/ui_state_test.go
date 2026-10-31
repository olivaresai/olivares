// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package consoleviews_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/consoleviews"
)

func TestSessionNotificationChoicesBelongToTheUser(t *testing.T) {
	h := newHarness(t)
	root := h.rootLogin()
	tenant := h.createOrg(root, "notification-choices")
	owner := model.NewID().String()
	ctx := context.Background()
	settings, _ := json.Marshal(map[string]any{"state": map[string]any{"notificationEvents": map[string]bool{"needsYou": true, "finished": false, "failed": true}}})
	params, _ := json.Marshal(map[string]any{"preferences": map[string]string{"olivares.settings": string(settings)}})
	if err := h.st.Mutate(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(consoleviews.SavedViewKind)
		if err != nil {
			return err
		}
		_, err = repo.Create(ctx, model.Record{"owner": "user:" + owner, "feature_id": "ui-state", "name": "ui-state", "params": string(params), "shared": false})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		user, kind string
		want       bool
	}{
		{owner, "approval", true}, {owner, "finished", false}, {owner, "failed", true}, {model.NewID().String(), "finished", true},
	} {
		err := h.st.View(ctx, tenant, func(sc store.Scope) error {
			allowed, err := consoleviews.SessionNotificationAllowed(ctx, sc, tc.user, tc.kind)
			if err == nil && allowed != tc.want {
				t.Errorf("%s: allowed=%v want=%v", tc.kind, allowed, tc.want)
			}
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}

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

func TestUIStateEditionBadge(t *testing.T) {
	h := newHarness(t)
	root := h.rootLogin()
	ten := h.createOrg(root, "badge")
	alice := h.roleToken(root, ten, "alice@badge.io", "viewer")
	bob := h.roleToken(root, ten, "bob@badge.io", "viewer")
	path := "/v1/m/consoleviews/ui-state"
	if r := h.do("PUT", path, alice, map[string]any{"sidebar": "rail"}, ten); r.code != http.StatusOK {
		t.Fatal(r.raw)
	}
	if r := h.do("PUT", path, alice, map[string]any{"show_edition_badge": false}, ten); r.code != http.StatusOK {
		t.Fatalf("hide badge: %d %s", r.code, r.raw)
	}
	if r := h.do("GET", path, alice, nil, ten); r.body["show_edition_badge"] != false || r.body["sidebar"] != "rail" {
		t.Fatalf("saved preference: %s", r.raw)
	}
	if r := h.do("PUT", path, alice, map[string]any{"sidebar": "full"}, ten); r.body["show_edition_badge"] != false {
		t.Fatalf("old sidebar client erased badge choice: %s", r.raw)
	}
	if r := h.do("GET", path, bob, nil, ten); r.body["show_edition_badge"] != true {
		t.Fatalf("other user's default: %s", r.raw)
	}
	if r := h.do("PUT", path, alice, map[string]any{"show_edition_badge": true}, ten); r.body["show_edition_badge"] != true || r.body["sidebar"] != "full" {
		t.Fatalf("restore badge: %s", r.raw)
	}
	if r := h.do("PUT", path, root, map[string]any{"show_edition_badge": false}, ten); r.code != http.StatusOK {
		t.Fatalf("root preference: %s", r.raw)
	}
	other := h.createOrg(root, "other-badge")
	if r := h.do("GET", path, root, nil, other); r.body["show_edition_badge"] != true {
		t.Fatalf("other tenant's default: %s", r.raw)
	}
	if r := h.do("PUT", path, alice, map[string]any{"show_edition_badge": "false"}, ten); r.code != http.StatusBadRequest {
		t.Fatalf("bad boolean: %d", r.code)
	}
}

// TestUIStatePaneWidths: a person's panel widths are their own interface state on the server,
// so they follow them to another browser. A PUT merges the panes it names, a width of 0 puts a
// pane back to its default, and a client that does not know widths never erases them.
func TestUIStatePaneWidths(t *testing.T) {
	h := newHarness(t)
	root := h.rootLogin()
	ten := h.createOrg(root, "panes")
	alice := h.roleToken(root, ten, "alice@panes.io", "viewer")
	bob := h.roleToken(root, ten, "bob@panes.io", "viewer")
	path := "/v1/m/consoleviews/ui-state"
	widths := func(r resp) map[string]any {
		m, _ := r.body["pane_widths"].(map[string]any)
		return m
	}

	if r := h.do("GET", path, alice, nil, ten); widths(r) != nil {
		t.Fatalf("a new user has widths: %s", r.raw)
	}
	if r := h.do("PUT", path, alice, map[string]any{"pane_widths": map[string]any{"sessions-list": 420}}, ten); r.code != http.StatusOK || widths(r)["sessions-list"] != float64(420) {
		t.Fatalf("save one pane: %d %s", r.code, r.raw)
	}
	// A second pane joins the first; the first is untouched.
	if r := h.do("PUT", path, alice, map[string]any{"pane_widths": map[string]any{"sessions-context": 360}}, ten); r.code != http.StatusOK {
		t.Fatalf("save second pane: %d %s", r.code, r.raw)
	}
	r := h.do("GET", path, alice, nil, ten)
	if w := widths(r); w["sessions-list"] != float64(420) || w["sessions-context"] != float64(360) || r.body["stored"] != true {
		t.Fatalf("read back: %s", r.raw)
	}
	// Another user has their own.
	if r := h.do("GET", path, bob, nil, ten); widths(r) != nil {
		t.Fatalf("bob sees alice's widths: %s", r.raw)
	}
	// A client that sends only the sidebar or the badge keeps the widths.
	if r := h.do("PUT", path, alice, map[string]any{"sidebar": "rail"}, ten); widths(r)["sessions-list"] != float64(420) {
		t.Fatalf("sidebar-only client erased widths: %s", r.raw)
	}
	// A width of 0 is "back to the default": that pane is dropped, the other stays.
	if r := h.do("PUT", path, alice, map[string]any{"pane_widths": map[string]any{"sessions-list": 0}}, ten); r.code != http.StatusOK {
		t.Fatalf("reset: %d %s", r.code, r.raw)
	}
	r = h.do("GET", path, alice, nil, ten)
	if w := widths(r); w["sessions-list"] != nil || w["sessions-context"] != float64(360) || r.body["sidebar"] != "rail" {
		t.Fatalf("after reset: %s", r.raw)
	}
}

func TestUIStatePaneWidthsValidation(t *testing.T) {
	h := newHarness(t)
	root := h.rootLogin()
	ten := h.createOrg(root, "panebad")
	alice := h.roleToken(root, ten, "alice@panebad.io", "viewer")
	path := "/v1/m/consoleviews/ui-state"
	many := map[string]any{}
	for i := 0; i < 17; i++ {
		many["pane-"+string(rune('a'+i))] = 300
	}
	for name, body := range map[string]map[string]any{
		"no pane named":        {"pane_widths": map[string]any{}},
		"uppercase pane id":    {"pane_widths": map[string]any{"Sessions": 300}},
		"path-like pane id":    {"pane_widths": map[string]any{"../x": 300}},
		"overlong pane id":     {"pane_widths": map[string]any{"a123456789012345678901234567890123": 300}},
		"negative width":       {"pane_widths": map[string]any{"sessions-list": -1}},
		"absurd width":         {"pane_widths": map[string]any{"sessions-list": 100000}},
		"fractional width":     {"pane_widths": map[string]any{"sessions-list": 300.5}},
		"text width":           {"pane_widths": map[string]any{"sessions-list": "wide"}},
		"too many panes":       {"pane_widths": many},
		"widths not an object": {"pane_widths": []any{300}},
	} {
		if r := h.do("PUT", path, alice, body, ten); r.code != http.StatusBadRequest {
			t.Errorf("%s: %d %s, want 400", name, r.code, r.raw)
		}
	}
	// Nothing bad was stored.
	if r := h.do("GET", path, alice, nil, ten); r.body["stored"] != false {
		t.Fatalf("a refused save stored something: %s", r.raw)
	}
}

// The cap of 16 panes holds for what is stored, not only for one request: a 17th pane is
// refused and nothing changes, while a request that drops one and adds one stays at 16.
func TestUIStatePaneWidthsCapOnMerge(t *testing.T) {
	h := newHarness(t)
	root := h.rootLogin()
	ten := h.createOrg(root, "panecap")
	alice := h.roleToken(root, ten, "alice@panecap.io", "viewer")
	path := "/v1/m/consoleviews/ui-state"
	full := map[string]any{}
	for i := 0; i < 16; i++ {
		full["pane-"+string(rune('a'+i))] = 300
	}
	if r := h.do("PUT", path, alice, map[string]any{"pane_widths": full}, ten); r.code != http.StatusOK {
		t.Fatalf("16 panes: %d %s", r.code, r.raw)
	}
	if r := h.do("PUT", path, alice, map[string]any{"pane_widths": map[string]any{"one-more": 300}}, ten); r.code != http.StatusBadRequest {
		t.Fatalf("17th pane: %d %s, want 400", r.code, r.raw)
	}
	if r := h.do("GET", path, alice, nil, ten); len(r.body["pane_widths"].(map[string]any)) != 16 || r.body["pane_widths"].(map[string]any)["one-more"] != nil {
		t.Fatalf("a refused save changed the widths: %s", r.raw)
	}
	swap := map[string]any{"pane-a": 0, "one-more": 300}
	if r := h.do("PUT", path, alice, map[string]any{"pane_widths": swap}, ten); r.code != http.StatusOK {
		t.Fatalf("drop one, add one: %d %s", r.code, r.raw)
	}
}

// Resetting the last pane leaves a valid row that still reads as stored, with the other
// settings at their values, and a width saved with the sidebar keeps both.
func TestUIStatePaneWidthsLastResetAndMixedSave(t *testing.T) {
	h := newHarness(t)
	root := h.rootLogin()
	ten := h.createOrg(root, "panelast")
	alice := h.roleToken(root, ten, "alice@panelast.io", "viewer")
	path := "/v1/m/consoleviews/ui-state"
	h.do("PUT", path, alice, map[string]any{"pane_widths": map[string]any{"sessions-list": 420}}, ten)
	if r := h.do("PUT", path, alice, map[string]any{"pane_widths": map[string]any{"sessions-list": 0}}, ten); r.code != http.StatusOK {
		t.Fatalf("reset last: %d %s", r.code, r.raw)
	}
	r := h.do("GET", path, alice, nil, ten)
	if r.body["stored"] != true || r.body["pane_widths"] != nil || r.body["sidebar"] != "full" || r.body["show_edition_badge"] != true {
		t.Fatalf("after the last reset: %s", r.raw)
	}
	if r := h.do("PUT", path, alice, map[string]any{"sidebar": "rail", "pane_widths": map[string]any{"side-panel": 500}}, ten); r.code != http.StatusOK {
		t.Fatalf("mixed save: %d %s", r.code, r.raw)
	}
	r = h.do("GET", path, alice, nil, ten)
	if w, _ := r.body["pane_widths"].(map[string]any); r.body["sidebar"] != "rail" || w["side-panel"] != float64(500) {
		t.Fatalf("mixed save read back: %s", r.raw)
	}
}

func TestUIStatePreferences(t *testing.T) {
	h := newHarness(t)
	root := h.rootLogin()
	ten := h.createOrg(root, "prefs")
	alice := h.roleToken(root, ten, "alice@prefs.io", "viewer")
	bob := h.roleToken(root, ten, "bob@prefs.io", "viewer")
	path := "/v1/m/consoleviews/ui-state"
	put := func(body map[string]any) {
		t.Helper()
		r := h.do("PUT", path, alice, body, ten)
		if r.code != http.StatusOK {
			t.Fatalf("save = %d %s", r.code, r.raw)
		}
	}
	put(map[string]any{"preferences": map[string]any{"olivares.lang": "es", "olivares.settings": "clock"}})
	put(map[string]any{"sidebar": "rail"})
	put(map[string]any{"preferences": map[string]any{"olivares.lang": nil}})
	r := h.do("GET", path, alice, nil, ten)
	prefs, ok := r.body["preferences"].(map[string]any)
	if !ok || prefs["olivares.settings"] != "clock" || prefs["olivares.lang"] != nil {
		t.Fatalf("preferences = %s", r.raw)
	}
	if other := h.do("GET", path, bob, nil, ten); other.body["preferences"] != nil {
		t.Fatalf("another user sees %s", other.raw)
	}
	for _, name := range []string{"olivares.session", "arbitrary"} {
		r := h.do("PUT", path, alice, map[string]any{"preferences": map[string]any{name: "secret"}}, ten)
		if r.code != http.StatusBadRequest {
			t.Fatalf("unknown preference %s = %d", name, r.code)
		}
	}
}
