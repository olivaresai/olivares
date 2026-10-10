// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/engine/enginetest"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// A launch parked after this node starts has no local approval worker. Calling
// the exact registered promotion callback must recover it without another Start.
func TestBootPromotionRecoversWaitingLaunchCreatedAfterStart(t *testing.T) {
	var promote func(context.Context) error
	eng, err := boot(t.Context(), bootConfig{
		DataDir: t.TempDir(), Engine: "sqlite", Version: "test", ServeMode: true,
		pdpPromotionRegistered: func(fn func(context.Context) error) { promote = fn },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = eng.Close() })
	tenant, ref := parkPromotionWaitingLaunch(t, eng.store)
	if promote == nil {
		t.Fatal("boot did not register the promotion callback")
	}
	if err := promote(t.Context()); err != nil {
		t.Fatal(err)
	}
	waitPromotionLaunchState(t, eng.store, tenant, ref, "expired")
}

func TestBootPromotionRetriesWaitingLaunchInventoryWithoutAborting(t *testing.T) {
	var promote func(context.Context) error
	eng, err := boot(t.Context(), bootConfig{
		DataDir: t.TempDir(), Engine: "sqlite", Version: "test", ServeMode: true,
		pdpPromotionRegistered: func(fn func(context.Context) error) { promote = fn },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = eng.Close() })
	tenant, ref := parkPromotionWaitingLaunch(t, eng.store)
	list := eng.sessionsMod.ApprovalRecoveryTenants
	defer func() { eng.sessionsMod.ApprovalRecoveryTenants = list }()
	calls := 0
	eng.sessionsMod.ApprovalRecoveryTenants = func(context.Context) ([]model.TenantID, error) {
		calls++
		if calls == 1 {
			return nil, errors.New("tenant inventory temporarily unavailable")
		}
		return []model.TenantID{tenant}, nil
	}
	if err := promote(t.Context()); err != nil {
		t.Fatalf("best-effort waiting recovery aborted promotion: %v", err)
	}
	if calls != 1 {
		t.Fatalf("promotion inventory calls = %d, want 1", calls)
	}
	if err := promote(t.Context()); err != nil {
		t.Fatal(err)
	}
	waitPromotionLaunchState(t, eng.store, tenant, ref, "expired")
	if calls != 2 {
		t.Fatalf("retry promotion inventory calls = %d, want 2", calls)
	}
}

func TestBootPromotionOutsideServeDoesNotRecoverWaitingLaunches(t *testing.T) {
	var promote func(context.Context) error
	eng, err := boot(t.Context(), bootConfig{
		DataDir: t.TempDir(), Engine: "sqlite", Version: "test",
		pdpPromotionRegistered: func(fn func(context.Context) error) { promote = fn },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = eng.Close() })
	tenant, _ := parkPromotionWaitingLaunch(t, eng.store)
	// An enumerable module is still not a serve owner. This isolates the
	// promotion's ServeMode guard from the separate start-time port binding.
	called := false
	eng.sessionsMod.ApprovalRecoveryTenants = func(context.Context) ([]model.TenantID, error) {
		called = true
		return []model.TenantID{tenant}, nil
	}
	if err := promote(t.Context()); err != nil {
		t.Fatal(err)
	}
	if called {
		t.Fatal("offline promotion attempted serve-owned waiting-launch recovery")
	}
}

// Both nodes run the real serve boot over the same PostgreSQL database. The
// former leader parks a launch after the follower's Start, then resigns. Only
// the elector's actual HA promotion can arm the follower's missing worker.
func TestPostgresHAPromotionRecoversWaitingLaunchCreatedAfterStandbyStart(t *testing.T) {
	pg := enginetest.IsolatedPostgresSplitOwner(t)
	leader := startWaitingPromotionNode(t, pg)
	standby := startWaitingPromotionNode(t, pg)
	if !leader.store.Leader().IsLeader() || standby.store.Leader().IsLeader() {
		t.Fatal("expected an active leader and an already-started standby")
	}
	tenant, ref := parkPromotionWaitingLaunch(t, leader.store)
	if err := leader.store.Leader().Resign(t.Context()); err != nil {
		t.Fatal(err)
	}
	waitPromotionLaunchState(t, standby.store, tenant, ref, "expired")
	if !standby.store.Leader().IsLeader() {
		t.Fatal("the waiting launch changed without established standby leadership")
	}
}

// Drive setup, a privileged launch, and its human rejection through the real
// APIs. The former leader creates the wait after its peer finishes starting;
// the promoted peer must observe the decision without either serve restarting.
func TestPostgresHAPromotionConsumesWaitingLaunchHumanRejection(t *testing.T) {
	pg := enginetest.IsolatedPostgresSplitOwner(t)
	home := t.TempDir()
	program := filepath.Join(home, "claude")
	if err := os.WriteFile(program, []byte("#!/bin/sh\nexit 99\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv(envSessionClaudeBin, program)
	leader := startWaitingPromotionNode(t, pg)
	standby := startWaitingPromotionNode(t, pg)
	var admin, tenant string
	do := func(eng *engine, method, path string, body any, want int) map[string]any {
		t.Helper()
		code, response, raw := doDemoViewJSON(t, eng.api.Handler(), method, path, admin, tenant, body)
		if code != want {
			t.Fatalf("%s %s = %d, want %d: %s", method, path, code, want, raw)
		}
		return response
	}
	setup, _, err := leader.setupTok.Ensure()
	if err != nil {
		t.Fatal(err)
	}
	installed := do(leader, "POST", "/v1/setup", map[string]any{"token": setup, "email": "ha@example.test", "password": "fixture-password-2026!"}, http.StatusCreated)
	login := do(leader, "POST", "/v1/auth/login", map[string]any{"email": "ha@example.test", "password": "fixture-password-2026!"}, http.StatusOK)
	admin = login["token"].(string)
	tenant = installed["organization"].(map[string]any)["tenant_id"].(string)
	profile := do(leader, "POST", "/v1/m/sessions/provider-profiles", map[string]any{
		"driver": "claude", "config_home": home, "user_home": home, "auth_source": "provider_account_home", "display_name": "HA fixture",
	}, http.StatusCreated)
	run := do(leader, "POST", "/v1/m/sessions/runs", map[string]any{
		"provider_profile_ref": profile["profile_ref"], "name": "HA human review", "transport": "stream-json", "permission_mode": "dontAsk", "isolation": "native",
	}, http.StatusAccepted)
	if run["state"] != "waiting_approval" {
		t.Fatalf("privileged launch state = %v, want waiting_approval", run["state"])
	}
	if err := leader.store.Leader().Resign(t.Context()); err != nil {
		t.Fatal(err)
	}
	deadline := time.NewTimer(15 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	for !standby.store.Leader().IsLeader() {
		select {
		case <-deadline.C:
			t.Fatal("standby did not become leader")
		case <-tick.C:
		}
	}
	do(standby, "POST", "/v1/m/governance/approvals/"+run["approval_ref"].(string)+"/decisions", map[string]any{"decision": "reject"}, http.StatusOK)
	waitPromotionLaunchState(t, standby.store, model.TenantID(tenant), run["run_ref"].(string), "declined")
}

func startWaitingPromotionNode(t *testing.T, pg enginetest.DSNs) *engine {
	t.Helper()
	eng, err := boot(t.Context(), bootConfig{
		DataDir: t.TempDir(), Engine: "postgres", DSN: pg.App, OwnerDSN: pg.Owner, AdminDSN: pg.Admin,
		Version: "test", ServeMode: true, Logger: discardLogger(),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = eng.Close() })
	return eng
}

func parkPromotionWaitingLaunch(t *testing.T, st store.Store) (model.TenantID, string) {
	t.Helper()
	ctx := t.Context()
	var tenant model.TenantID
	if err := st.System(ctx, func(sys store.SystemScope) error {
		org, err := sys.CreateOrg(ctx, model.Org{Name: "Promotion", Slug: "promotion", Status: model.StatusActive})
		tenant = org.TenantID
		return err
	}); err != nil {
		t.Fatal(err)
	}
	ref := model.NewID().String()
	// A missing approval is expired, so its first recovered watcher tick must
	// terminalize the run. No launch gate or recovery callback is mocked here.
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext("sessions.run")
		if err != nil {
			return err
		}
		_, err = repo.Create(ctx, model.Record{
			"run_ref": ref, "transport": "stream-json", "permission_mode": "default",
			"isolation": "native", "state": "waiting_approval", "last_event_seq": int64(0),
			"agent_ref": "agent:waiting-launch", "approval_ref": model.NewID().String(), "queued_intent": "{}",
		})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return tenant, ref
}

func waitPromotionLaunchState(t *testing.T, st store.Store, tenant model.TenantID, ref, want string) {
	t.Helper()
	timer := time.NewTimer(15 * time.Second)
	defer timer.Stop()
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	var state string
	for {
		if err := st.View(t.Context(), tenant, func(sc store.Scope) error {
			repo, err := sc.Ext("sessions.run")
			if err != nil {
				return err
			}
			rows, _, err := repo.List(t.Context(), model.Query{Limit: 1, Filters: []model.Filter{{Column: "run_ref", Op: model.OpEq, Value: ref}}})
			if len(rows) == 1 {
				state = rows[0].String("state")
			}
			return err
		}); err != nil {
			t.Fatal(err)
		}
		if state == want {
			return
		}
		select {
		case <-timer.C:
			t.Fatalf("waiting launch after promotion = %q, want %q without a serve restart", state, want)
		case <-tick.C:
		}
	}
}
