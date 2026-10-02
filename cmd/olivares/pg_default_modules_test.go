// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"net/http"
	"slices"
	"testing"

	"github.com/olivaresai/olivares/core/engine/enginetest"
)

// On the default PostgreSQL install (application and owner roles, no BYPASSRLS
// admin pool) the engine reads which modules hold data, and its first start
// records the installation's module selection instead of failing to reconcile.
func TestDefaultPostgresReadsWhichModulesHoldData(t *testing.T) {
	pg := enginetest.IsolatedPostgresSplitOwner(t)
	ctx := context.Background()
	eng, err := boot(ctx, bootConfig{DataDir: t.TempDir(), Engine: "postgres", DSN: pg.App, OwnerDSN: pg.Owner,
		Version: version, Logger: discardLogger(), ApplyModuleProfile: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = eng.Close() })
	h := eng.api.Handler()
	tok, _, err := eng.setupTok.Ensure()
	if err != nil {
		t.Fatal(err)
	}
	code, setup, raw := doDemoViewJSON(t, h, http.MethodPost, "/v1/setup", "", "", map[string]any{"token": tok, "email": "root@x.io", "password": "supersecret1"})
	if code != http.StatusCreated {
		t.Fatalf("setup = %d: %s", code, raw)
	}
	org, _ := setup["organization"].(map[string]any)
	tenant, _ := org["tenant_id"].(string)
	code, login, raw := doDemoViewJSON(t, h, http.MethodPost, "/v1/auth/login", "", "", map[string]any{"email": "root@x.io", "password": "supersecret1"})
	if code != http.StatusOK {
		t.Fatalf("login = %d: %s", code, raw)
	}
	admin, _ := login["token"].(string)
	if code, _, raw := doDemoViewJSON(t, h, http.MethodPost, "/v1/m/consoleviews/views", admin, tenant, map[string]any{
		"feature_id": "audit", "name": "mine", "params": map[string]any{},
	}); code != http.StatusCreated && code != http.StatusOK {
		t.Fatalf("create a saved view = %d %s", code, raw)
	}

	used, err := usedModules(ctx, eng.store, eng.census)
	if err != nil || !slices.Equal(used, []string{"consoleviews"}) {
		t.Fatalf("used = %v (err %v), want [consoleviews]", used, err)
	}
	p := newProductSettings(eng.store, eng.dataDir)
	mods := &moduleReconcile{booted: eng.moduleProfile, used: func(ctx context.Context) ([]string, error) { return usedModules(ctx, eng.store, eng.census) }}
	if _, err := p.reconcileModules(ctx, *mods, discardLogger()); err != nil {
		t.Fatalf("module reconcile on the default PostgreSQL install = %v, want the selection recorded", err)
	}
	if got := recordedSelection(t, p); !slices.Contains(got, "consoleviews") {
		t.Fatalf("recorded selection = %v, want it to keep consoleviews", got)
	}
}
