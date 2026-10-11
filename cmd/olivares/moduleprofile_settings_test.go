// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/olivaresai/olivares/core/api"
)

func mustProfile(t *testing.T, sel []string) moduleProfile {
	t.Helper()
	p, err := resolveModuleProfile(sel)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func usedReturns(names ...string) func(context.Context) ([]string, error) {
	return func(context.Context) ([]string, error) { return names, nil }
}

func recordedSelection(t *testing.T, p *productSettings) []string {
	t.Helper()
	doc, found, err := p.load(context.Background())
	if err != nil || !found || doc.Modules == nil {
		t.Fatalf("no module selection recorded (found=%v err=%v)", found, err)
	}
	return doc.Modules.Selected
}

// A profile-free existing installation keeps the published default, including
// modules with no data, without depending on the data census or restarting.
func TestReconcileModulesKeepsEveryModuleAnExistingInstallationUses(t *testing.T) {
	_, p := bootForSettings(t)
	// This installation ran 26.10.0, before skills joined the catalog.
	want := []string{
		"accessmap", "adoption", "capabilities", "catalog", "claude-agents",
		"claude-policy", "compliance", "consoleviews", "deploy", "evals", "eventing",
		"finops", "gitpublish", "governance", "health", "identity", "inferenceproxy",
		"inventory", "knowledge", "liveingest", "models", "notify", "observability",
		"orchestration", "posture", "recording", "redteam", "reporting", "sandbox",
		"security", "sessions", "siemforward", "sourcescope", "voice",
	}
	booted := mustProfile(t, want)
	booted.existingInstallation = true
	mods := &moduleReconcile{booted: booted, used: func(context.Context) ([]string, error) {
		t.Fatal("an existing installation's default must not depend on module rows")
		return nil, nil
	}}
	if err := reconcileSettings(context.Background(), p, mods, slog.Default()); err != nil {
		t.Fatalf("first start of an existing installation = %v, want serve", err)
	}
	if got := recordedSelection(t, p); !slices.Equal(got, want) {
		t.Fatalf("recorded selection = %v, want %v", got, want)
	}
	if node, found, _ := loadNodeModuleSelection(p.dataDir); !found || !slices.Equal(node, want) {
		t.Fatalf("node copy = %v (found %v), want %v", node, found, want)
	}
	if _, err := os.Stat(filepath.Join(p.dataDir, settingsRestartMarker)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("preserving the published default requested a restart: %v", err)
	}
}

func TestReconcileModulesImportsExplicitNodeSelectionExactly(t *testing.T) {
	for _, selected := range [][]string{{}, {"eventing"}} {
		t.Run(fmt.Sprint(selected), func(t *testing.T) {
			_, p := bootForSettings(t)
			if err := saveNodeModuleSelection(p.dataDir, selected, productSettingsT); err != nil {
				t.Fatal(err)
			}
			booted := mustProfile(t, selected)
			booted.existingInstallation = true
			mods := &moduleReconcile{booted: booted, used: func(context.Context) ([]string, error) {
				t.Fatal("an explicit selection must not depend on module rows")
				return nil, nil
			}}
			if err := reconcileSettings(context.Background(), p, mods, slog.Default()); err != nil {
				t.Fatalf("explicit selection import: %v", err)
			}
			if got := recordedSelection(t, p); !slices.Equal(got, selected) {
				t.Fatalf("recorded selection = %v, want exactly %v", got, selected)
			}
		})
	}
}

// A new installation already runs the standard selection: it is recorded and
// nothing restarts.
func TestReconcileModulesRecordsANewInstallationWithoutRestart(t *testing.T) {
	_, p := bootForSettings(t)
	mods := &moduleReconcile{booted: mustProfile(t, standardModuleSelection()), used: usedReturns()}
	if err := reconcileSettings(context.Background(), p, mods, slog.Default()); err != nil {
		t.Fatalf("new installation = %v, want serve", err)
	}
	if got := recordedSelection(t, p); !slices.Equal(got, standardModuleSelection()) {
		t.Fatalf("recorded selection = %v", got)
	}
}

// The record wins: a node built from another selection takes the record's and
// restarts once; after that one restart it serves.
func TestReconcileModulesFollowsTheRecordAndRestartsOnce(t *testing.T) {
	_, p := bootForSettings(t)
	if err := p.writeModules(context.Background(), operator(t), "deployment.settings.modules", []string{"eventing"}); err != nil {
		t.Fatal(err)
	}
	booted := mustProfile(t, standardModuleSelection())
	mods := &moduleReconcile{booted: booted, used: usedReturns()}
	var restart *selfRestartError
	if err := reconcileSettings(context.Background(), p, mods, slog.Default()); !errors.As(err, &restart) {
		t.Fatalf("node differing from the record = %v, want one restart", err)
	}
	if node, _, _ := loadNodeModuleSelection(p.dataDir); !slices.Equal(node, []string{"eventing"}) {
		t.Fatalf("node copy = %v, want [eventing]", node)
	}
	// Still differing after that restart (the marker is present): serve.
	if err := reconcileSettings(context.Background(), p, mods, slog.Default()); err != nil {
		t.Fatalf("after one restart = %v, want serve", err)
	}
	// Built from the record: in step, no restart.
	mods.booted = mustProfile(t, []string{"eventing"})
	if err := reconcileSettings(context.Background(), p, mods, slog.Default()); err != nil {
		t.Fatalf("in step = %v, want serve", err)
	}
}

// The activation and the module selection share one record; writing one keeps
// the other.
func TestDeploymentSettingsWritesKeepEachOther(t *testing.T) {
	_, p := bootForSettings(t)
	ctx := context.Background()
	if err := p.SaveActivation(ctx, operator(t), activationWith(reportingActive), nil); err != nil {
		t.Fatal(err)
	}
	if err := p.writeModules(ctx, operator(t), "deployment.settings.modules", []string{"finops"}); err != nil {
		t.Fatal(err)
	}
	if err := p.SaveActivation(ctx, operator(t), activationWith(rtbfActive), nil); err != nil {
		t.Fatal(err)
	}
	doc, _, err := p.load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if doc.Modules == nil || !slices.Equal(doc.Modules.Selected, []string{"finops"}) {
		t.Fatalf("module selection lost by an activation write: %+v", doc.Modules)
	}
	if doc.Activation == nil || len(doc.Activation.Entries) != 1 || doc.Activation.Entries[0].Addon != "rtbf-depth" {
		t.Fatalf("activation = %+v", doc.Activation)
	}
}

// The console's selection is recorded and copied to the node; the engine restarts
// only when the modules it runs change, and a restart it cannot do is a failure.
func TestModuleSelectionRestartsOnlyWhenTheRunningModulesChange(t *testing.T) {
	_, p := bootForSettings(t)
	ctx := context.Background()
	restarts := 0
	svc := moduleSelectionService{settings: p, running: mustProfile(t, standardModuleSelection()), log: slog.Default(),
		restart: func(string) error { restarts++; return nil }}

	dto, err := svc.SelectModules(ctx, operator(t), standardModuleSelection())
	if err != nil || dto.Restarting || restarts != 0 {
		t.Fatalf("unchanged selection: restarting=%v restarts=%d err=%v", dto.Restarting, restarts, err)
	}
	svc.running = mustProfile(t, []string{"identity", "consoleviews", "claude-policy", "capabilities", "liveingest"})
	dto, err = svc.SelectModules(ctx, operator(t), []string{"eventing", "consoleviews"})
	if err != nil || !dto.Restarting || restarts != 1 {
		t.Fatalf("added module: restarting=%v restarts=%d err=%v", dto.Restarting, restarts, err)
	}
	if got := recordedSelection(t, p); !slices.Equal(got, []string{"consoleviews", "eventing"}) {
		t.Fatalf("recorded = %v", got)
	}
	if node, _, _ := loadNodeModuleSelection(p.dataDir); !slices.Equal(node, []string{"consoleviews", "eventing"}) {
		t.Fatalf("node copy = %v", node)
	}
	states := map[string]api.ModuleStateDTO{}
	for _, m := range dto.Modules {
		states[m.Name] = m
	}
	if e := states["eventing"]; !e.Selected || e.Running {
		t.Fatalf("eventing = %+v, want selected and not yet running", e)
	}
	if g := states["governance"]; !g.AlwaysOn || !g.Running {
		t.Fatalf("governance = %+v, want always on", g)
	}
	if l := states["liveingest"]; len(l.RequiredBy) != 0 {
		t.Fatalf("liveingest required by %v, want none", l.RequiredBy)
	}

	if _, err := svc.SelectModules(ctx, operator(t), []string{"teleport"}); !errors.Is(err, api.ErrUnknownModule) {
		t.Fatalf("unknown module = %v, want ErrUnknownModule", err)
	}
	if got := recordedSelection(t, p); !slices.Equal(got, []string{"consoleviews", "eventing"}) {
		t.Fatalf("an unknown module changed the record: %v", got)
	}
	svc.restart = func(string) error { return errors.New("not serving") }
	if _, err := svc.SelectModules(ctx, operator(t), []string{"finops"}); !errors.Is(err, api.ErrModulesRestartUnavailable) {
		t.Fatalf("restart failure = %v, want ErrModulesRestartUnavailable", err)
	}
}

// The restart that applies a selection stops every running session, so the
// selection says how many run now, before Apply and in the reply (#507).
func TestModuleSelectionSaysHowManySessionsTheRestartStops(t *testing.T) {
	_, p := bootForSettings(t)
	ctx := context.Background()
	svc := moduleSelectionService{settings: p, running: mustProfile(t, standardModuleSelection()), log: slog.Default(),
		restart: func(string) error { return nil }, sessions: func() int { return 2 }}
	dto, err := svc.ModuleSelection(ctx)
	if err != nil || dto.RunningSessions != 2 {
		t.Fatalf("before Apply: running_sessions = %d (%v), want 2", dto.RunningSessions, err)
	}
	dto, err = svc.SelectModules(ctx, operator(t), []string{"eventing"})
	if err != nil || !dto.Restarting || dto.RunningSessions != 2 {
		t.Fatalf("the restarting reply: restarting=%v running_sessions=%d (%v), want true 2", dto.Restarting, dto.RunningSessions, err)
	}
	// A change that keeps what runs restarts nothing, and its reply still counts them.
	dto, err = svc.SelectModules(ctx, operator(t), standardModuleSelection())
	if err != nil || dto.Restarting || dto.RunningSessions != 2 {
		t.Fatalf("the reply without a restart: restarting=%v running_sessions=%d (%v), want false 2", dto.Restarting, dto.RunningSessions, err)
	}
	svc.sessions = nil
	if dto, err := svc.ModuleSelection(ctx); err != nil || dto.RunningSessions != 0 {
		t.Fatalf("no sessions module: running_sessions = %d (%v), want 0", dto.RunningSessions, err)
	}
}

// A selection that cannot be reconciled never stops the engine: it serves with the
// modules this start built, and the next start tries again.
func TestReconcileModulesFailureServesWithTheModulesThisStartBuilt(t *testing.T) {
	_, p := bootForSettings(t)
	mods := &moduleReconcile{booted: mustProfile(t, standardModuleSelection()),
		used: func(context.Context) ([]string, error) { return nil, errors.New("tenant directory unavailable") }}
	if err := reconcileSettings(context.Background(), p, mods, slog.Default()); err != nil {
		t.Fatalf("an unreadable selection stopped the engine: %v", err)
	}
	if doc, _, _ := p.load(context.Background()); doc.Modules != nil {
		t.Fatalf("a selection was recorded although usage could not be read: %+v", doc.Modules)
	}
}

// A module whose tables hold rows says so, running or not: turned off, its data stays
// and nothing acts on it (finops: budgets are no longer enforced).
func TestModuleSelectionMarksModulesThatHoldData(t *testing.T) {
	_, p := bootForSettings(t)
	svc := moduleSelectionService{settings: p, running: mustProfile(t, standardModuleSelection()), log: slog.Default(),
		used: func(context.Context) ([]string, error) { return []string{"finops"}, nil }}
	dto, err := svc.ModuleSelection(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range dto.Modules {
		if want := m.Name == "finops"; m.HoldsData != want {
			t.Errorf("%s holds_data = %v, want %v", m.Name, m.HoldsData, want)
		}
	}
	svc.used = func(context.Context) ([]string, error) { return nil, errors.New("tenant directory unavailable") }
	if _, err := svc.ModuleSelection(context.Background()); err == nil {
		t.Fatal("an unreadable usage was reported as no data")
	}
}
