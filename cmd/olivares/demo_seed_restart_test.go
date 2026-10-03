// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// bootDemo serves dir with --seed-demo, as `serve` does (module profile applied).
func bootDemo(t *testing.T, dir string) (*engine, error) {
	t.Helper()
	return boot(context.Background(), bootConfig{
		DataDir: dir, Engine: "sqlite", Version: version, Logger: quietLog(),
		DemoSeed: true, ApplyModuleProfile: true,
	})
}

// demoEstateFacts is what the seed wrote: the demo tenant and its workspace and
// agent counts, read through the store.
func demoEstateFacts(t *testing.T, eng *engine) (model.TenantID, int, int) {
	t.Helper()
	ctx := context.Background()
	var workspaces, agents int
	err := eng.store.View(ctx, eng.demoTenant, func(sc store.Scope) error {
		ws, _, err := sc.Workspaces().List(ctx, model.Query{Limit: 1000})
		if err != nil {
			return err
		}
		ag, _, err := sc.Agents().List(ctx, model.Query{Limit: 1000})
		workspaces, agents = len(ws), len(ag)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return eng.demoTenant, workspaces, agents
}

// --seed-demo on a fresh directory, then the self-restart the deployment settings
// ask for (the seeded modules hold data, so the selection changes): the restarted
// engine, started with the same arguments, reaches readiness on the same estate.
func TestSeedDemoThenSelfRestartReachesReadiness(t *testing.T) {
	dir := t.TempDir()
	first, err := bootDemo(t, dir)
	if err != nil {
		t.Fatalf("first start with --seed-demo: %v", err)
	}
	tenant, ws, ag := demoEstateFacts(t, first)
	mods := &moduleReconcile{booted: first.moduleProfile, used: func(ctx context.Context) ([]string, error) {
		return usedModules(ctx, first.store, first.census)
	}}
	var restart *selfRestartError
	if err := reconcileSettings(context.Background(), newProductSettings(first.store, first.dataDir), mods, quietLog()); !errors.As(err, &restart) {
		t.Fatalf("first start reconcile = %v, want a self-restart (the demo data uses modules outside the standard selection)", err)
	}
	_ = first.Close()

	second, err := bootDemo(t, dir)
	if err != nil {
		t.Fatalf("restart with --seed-demo: %v", err)
	}
	defer func() { _ = second.Close() }()
	mods2 := &moduleReconcile{booted: second.moduleProfile, used: func(ctx context.Context) ([]string, error) {
		return usedModules(ctx, second.store, second.census)
	}}
	if err := reconcileSettings(context.Background(), newProductSettings(second.store, second.dataDir), mods2, quietLog()); err != nil {
		t.Fatalf("restarted engine reconcile = %v, want serve", err)
	}
	t2, ws2, ag2 := demoEstateFacts(t, second)
	if t2 != tenant || ws2 != ws || ag2 != ag {
		t.Fatalf("estate after restart: tenant %s ws %d agents %d, want %s %d %d", t2, ws2, ag2, tenant, ws, ag)
	}
}

// A second ordinary start (a service manager restart) with --seed-demo starts on
// the same estate and writes nothing new.
func TestSeedDemoSecondStartKeepsTheSameEstate(t *testing.T) {
	dir := t.TempDir()
	first, err := bootDemo(t, dir)
	if err != nil {
		t.Fatalf("first start: %v", err)
	}
	tenant, ws, ag := demoEstateFacts(t, first)
	_ = first.Close()
	for i := 0; i < 2; i++ {
		again, err := bootDemo(t, dir)
		if err != nil {
			t.Fatalf("start %d with --seed-demo: %v", i+2, err)
		}
		t2, ws2, ag2 := demoEstateFacts(t, again)
		_ = again.Close()
		if t2 != tenant || ws2 != ws || ag2 != ag {
			t.Fatalf("start %d: tenant %s ws %d agents %d, want %s %d %d", i+2, t2, ws2, ag2, tenant, ws, ag)
		}
	}
}

// An organization named like the demo that the seed did not create is never taken
// over: --seed-demo refuses with one sentence and writes nothing.
func TestSeedDemoRefusesAnOrganizationItDidNotCreate(t *testing.T) {
	dir := t.TempDir()
	eng, err := boot(context.Background(), bootConfig{DataDir: dir, Engine: "sqlite", Version: version, Logger: quietLog()})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := eng.store.System(ctx, func(sys store.SystemScope) error {
		_, err := sys.CreateOrg(ctx, model.Org{Name: "Operations", Slug: demoOrgSlug, Status: model.StatusActive})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	_ = eng.Close()

	_, err = bootDemo(t, dir)
	if err == nil {
		t.Fatal("--seed-demo took over an organization it did not create")
	}
	if msg := err.Error(); !strings.Contains(msg, "--seed-demo did not create") || strings.Contains(msg, "UNIQUE") {
		t.Fatalf("refusal = %q, want one sentence naming the organization the seed did not create", msg)
	}
}
