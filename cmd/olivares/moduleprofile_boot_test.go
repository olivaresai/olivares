// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/runtime"
)

// bootWithModuleProfile serves an engine whose node selection is selected, signs
// the first administrator in and returns the handler, token and tenant.
func bootWithModuleProfile(t *testing.T, selected []string) (*engine, http.Handler, string, string) {
	t.Helper()
	dir := t.TempDir()
	if err := saveNodeModuleSelection(dir, selected, time.Now()); err != nil {
		t.Fatal(err)
	}
	eng, err := boot(context.Background(), bootConfig{
		DataDir: dir, Engine: "sqlite", Version: version, ApplyModuleProfile: true,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("boot: %v", err)
	}
	t.Cleanup(func() { _ = eng.Close() })
	tok, _, err := eng.setupTok.Ensure()
	if err != nil {
		t.Fatal(err)
	}
	h := eng.api.Handler()
	code, setup, raw := doDemoViewJSON(t, h, http.MethodPost, "/v1/setup", "", "", map[string]any{
		"token": tok, "email": "root@x.io", "password": "supersecret1",
	})
	if code != http.StatusCreated {
		t.Fatalf("setup = %d: %s", code, raw)
	}
	org, _ := setup["organization"].(map[string]any)
	tenant, _ := org["tenant_id"].(string)
	code, login, raw := doDemoViewJSON(t, h, http.MethodPost, "/v1/auth/login", "", "", map[string]any{
		"email": "root@x.io", "password": "supersecret1",
	})
	if code != http.StatusOK {
		t.Fatalf("login = %d: %s", code, raw)
	}
	admin, _ := login["token"].(string)
	return eng, h, admin, tenant
}

func moduleStatus(eng *engine, name string) runtime.Status {
	for _, cs := range eng.rt.Status() {
		if cs.Name == name {
			return cs.Status
		}
	}
	return ""
}

// A module outside the node's profile keeps its tables but does not run: its
// routes answer module_not_enabled, server-info names it for the console, and
// the runtime holds it dormant. Selecting it serves it again.
func TestModuleProfileServesOnlyTheModulesItRuns(t *testing.T) {
	eng, h, admin, tenant := bootWithModuleProfile(t, []string{"consoleviews"})

	code, body, raw := doDemoViewJSON(t, h, http.MethodGet, "/v1/m/eventing/subscriptions", admin, tenant, nil)
	errBody, _ := body["error"].(map[string]any)
	if code != http.StatusNotFound || errBody["code"] != "module_not_enabled" {
		t.Fatalf("eventing route = %d %s, want 404 module_not_enabled", code, raw)
	}
	if code, _, raw := doDemoViewJSON(t, h, http.MethodGet, "/v1/m/sessions/runs", admin, tenant, nil); code != http.StatusOK {
		t.Fatalf("sessions route = %d %s, want 200", code, raw)
	}
	_, info, raw := doDemoViewJSON(t, h, http.MethodGet, "/v1/server-info", "", "", nil)
	var notEnabled []string
	for _, v := range info["modules_not_enabled"].([]any) {
		notEnabled = append(notEnabled, v.(string))
	}
	if !slices.Contains(notEnabled, "eventing") || slices.Contains(notEnabled, "sessions") || slices.Contains(notEnabled, "consoleviews") {
		t.Fatalf("server-info modules_not_enabled = %v (%s)", notEnabled, raw)
	}
	if got := moduleStatus(eng, "olivares.eventing"); got != runtime.StatusDormant {
		t.Fatalf("eventing runtime status = %q, want dormant", got)
	}
	if got := moduleStatus(eng, "olivares.sessions"); got != runtime.StatusRunning {
		t.Fatalf("sessions runtime status = %q, want running", got)
	}

	_, h2, admin2, tenant2 := bootWithModuleProfile(t, []string{"eventing"})
	if code, _, raw := doDemoViewJSON(t, h2, http.MethodGet, "/v1/m/eventing/subscriptions", admin2, tenant2, nil); code != http.StatusOK {
		t.Fatalf("selected eventing route = %d %s, want 200", code, raw)
	}
	_, info2, _ := doDemoViewJSON(t, h2, http.MethodGet, "/v1/server-info", "", "", nil)
	if list, _ := info2["modules_not_enabled"].([]any); slices.ContainsFunc(list, func(v any) bool { return v == "eventing" }) {
		t.Fatalf("selected eventing is listed as not enabled: %v", list)
	}
}

// A first serving start of a new installation builds the standard selection.
func TestBootModuleProfileOfANewInstallationIsTheStandardSelection(t *testing.T) {
	p := bootModuleProfile(t.TempDir(), false, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if got := p.Selected(); !slices.Equal(got, standardModuleSelection()) {
		t.Fatalf("new installation selection = %v, want %v", got, standardModuleSelection())
	}
	if p.Active("eventing") {
		t.Fatal("a new installation runs eventing without selecting it")
	}
	// An existing store without a node copy runs everything until it is reconciled.
	full := bootModuleProfile(t.TempDir(), true, slog.New(slog.NewTextHandler(io.Discard, nil)))
	for name := range moduleCatalog {
		if !full.Active(name) {
			t.Fatalf("an existing store without a node copy turned %s off", name)
		}
	}
}

// The serving engine finds the modules an installation uses through the store it
// really serves with (a wrapper) and its registry: a fresh installation uses none,
// and a saved view makes consoleviews used.
func TestUsedModulesReadsTheEngineStoreAndRegistry(t *testing.T) {
	eng, h, admin, tenant := bootWithModuleProfile(t, []string{"consoleviews"})
	ctx := context.Background()
	used, err := usedModules(ctx, eng.store, eng.census)
	if err != nil {
		t.Fatalf("usedModules on the serving engine: %v", err)
	}
	if len(used) != 0 {
		t.Fatalf("a fresh installation uses %v", used)
	}
	if code, _, raw := doDemoViewJSON(t, h, http.MethodPost, "/v1/m/consoleviews/views", admin, tenant, map[string]any{
		"feature_id": "audit", "name": "mine", "params": map[string]any{},
	}); code != http.StatusCreated && code != http.StatusOK {
		t.Fatalf("create a saved view = %d %s", code, raw)
	}
	used, err = usedModules(ctx, eng.store, eng.census)
	if err != nil || !slices.Equal(used, []string{"consoleviews"}) {
		t.Fatalf("used = %v (err %v), want [consoleviews]", used, err)
	}
}

// The serving engine's module selection reads usage from its real store: a saved
// view makes consoleviews hold data, and a module with no rows does not.
func TestConsoleModulesReportsWhichModulesHoldData(t *testing.T) {
	_, h, admin, tenant := bootWithModuleProfile(t, standardModuleSelection())
	if code, _, raw := doDemoViewJSON(t, h, http.MethodPost, "/v1/m/consoleviews/views", admin, tenant, map[string]any{
		"feature_id": "audit", "name": "kept", "params": map[string]any{},
	}); code != http.StatusCreated && code != http.StatusOK {
		t.Fatalf("create a saved view = %d %s", code, raw)
	}
	code, body, raw := doDemoViewJSON(t, h, http.MethodGet, "/v1/console/modules", admin, "", nil)
	if code != http.StatusOK {
		t.Fatalf("GET /v1/console/modules = %d %s", code, raw)
	}
	holds := map[string]bool{}
	for _, m := range body["modules"].([]any) {
		row := m.(map[string]any)
		holds[row["name"].(string)], _ = row["holds_data"].(bool)
	}
	if !holds["consoleviews"] || holds["finops"] {
		t.Fatalf("holds_data consoleviews=%v finops=%v, want true/false: %s", holds["consoleviews"], holds["finops"], raw)
	}
}
