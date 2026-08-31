// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

func TestPreparePDPReloadTenantsRejectsEnumerationFailure(t *testing.T) {
	injected := errors.New("authoritative inventory unavailable")
	_, err := preparePDPReloadTenants(context.Background(), func(context.Context) ([]model.Org, error) {
		return nil, injected
	})
	if !errors.Is(err, injected) {
		t.Fatalf("pdp tenant enumeration error = %v, want wrapped %v", err, injected)
	}
}

func TestPreparePDPReloadTenantsRejectsInvalidTenantInventory(t *testing.T) {
	for _, org := range []model.Org{
		{},
		{BaseFields: model.BaseFields{ID: model.NewID(), TenantID: model.TenantID(model.NewID())}},
	} {
		_, err := preparePDPReloadTenants(context.Background(), func(context.Context) ([]model.Org, error) {
			return []model.Org{org}, nil
		})
		if err == nil {
			t.Fatalf("invalid tenant inventory %#v = err:%v, want error", org.BaseFields, err)
		}
	}
}

func TestBootServeAbortsAndClosesWhenPDPInventoryFails(t *testing.T) {
	dir := t.TempDir()
	injected := errors.New("authoritative inventory unavailable")
	var seen store.Store
	eng, err := boot(context.Background(), bootConfig{
		DataDir: dir, Engine: "sqlite", Version: "test", ServeMode: true,
		pdpListOrgs: func(_ context.Context, st store.Store) ([]model.Org, error) {
			seen = st
			return nil, injected
		},
	})
	if eng != nil || !errors.Is(err, injected) {
		t.Fatalf("serve boot after failed PDP inventory = engine:%v err:%v, want nil/wrapped injected", eng, err)
	}
	if seen == nil {
		t.Fatal("failed inventory did not receive the boot store handle")
	}
	if err := seen.System(context.Background(), func(store.SystemScope) error { return nil }); err == nil {
		t.Fatal("initial Leader.Run inventory failure left its exact boot store handle usable")
	}
	// A normal ServeMode boot over the same SQLite file proves the error path
	// closed the earlier store rather than returning a usable engine over it.
	reopened, reopenErr := boot(context.Background(), bootConfig{DataDir: dir, Engine: "sqlite", Version: "test", ServeMode: true})
	if reopenErr != nil {
		t.Fatalf("normal reopen after inventory-failed boot: %v", reopenErr)
	}
	if err := reopened.Close(); err != nil {
		t.Fatalf("close reopened engine: %v", err)
	}
}

func TestBootNonServeSkipsPDPInventory(t *testing.T) {
	called := false
	eng, err := boot(context.Background(), bootConfig{
		DataDir: t.TempDir(), Engine: "sqlite", Version: "test", ServeMode: false,
		pdpListOrgs: func(context.Context, store.Store) ([]model.Org, error) {
			called = true
			return nil, errors.New("must not list tenants outside serve mode")
		},
	})
	if err != nil || eng == nil || called {
		t.Fatalf("non-serve boot = engine:%v err:%v listed:%t, want engine/nil/false", eng, err, called)
	}
	if err := eng.Close(); err != nil {
		t.Fatalf("close non-serve engine: %v", err)
	}
}

func TestReloadPDPForPromotionRetriesAuthoritativeInventory(t *testing.T) {
	tenant := model.TenantID(model.NewID())
	org := model.Org{BaseFields: model.BaseFields{ID: model.ID(tenant), TenantID: tenant}}
	injected := errors.New("inventory temporarily unavailable")
	listCalls := 0
	reloads := 0
	list := func(context.Context) ([]model.Org, error) {
		listCalls++
		if listCalls == 1 {
			return nil, injected
		}
		return []model.Org{org}, nil
	}
	reload := func(_ context.Context, got model.TenantID) error {
		reloads++
		if got != tenant {
			t.Fatalf("reload tenant = %s, want %s", got, tenant)
		}
		return nil
	}
	if err := reloadPDPForPromotion(context.Background(), list, reload, nil); !errors.Is(err, injected) {
		t.Fatalf("first promotion inventory = %v, want wrapped %v", err, injected)
	}
	if listCalls != 1 || reloads != 0 {
		t.Fatalf("failed promotion calls list/reload=%d/%d, want 1/0", listCalls, reloads)
	}
	if err := reloadPDPForPromotion(context.Background(), list, reload, nil); err != nil {
		t.Fatalf("retry promotion after inventory recovery: %v", err)
	}
	if listCalls != 2 || reloads != 1 {
		t.Fatalf("retry calls list/reload=%d/%d, want 2/1 (no cached failed inventory)", listCalls, reloads)
	}
}

func TestReloadPDPForPromotionCompletesInventoryBeforeReloadAndContinuesKnownTenants(t *testing.T) {
	tenantA := model.TenantID(model.NewID())
	tenantB := model.TenantID(model.NewID())
	orgs := []model.Org{
		{BaseFields: model.BaseFields{ID: model.ID(tenantA), TenantID: tenantA}},
		{BaseFields: model.BaseFields{ID: model.ID(tenantB), TenantID: tenantB}},
	}
	var trace []string
	injected := errors.New("tenant A runtime unavailable")
	err := reloadPDPForPromotion(context.Background(), func(context.Context) ([]model.Org, error) {
		trace = append(trace, "inventory")
		return orgs, nil
	}, func(_ context.Context, tenant model.TenantID) error {
		if len(trace) == 0 || trace[0] != "inventory" {
			t.Fatal("tenant reload began before the complete inventory returned")
		}
		trace = append(trace, "reload:"+tenant.String())
		if tenant == tenantA {
			return injected
		}
		return nil
	}, nil)
	if err != nil {
		t.Fatalf("known tenant reload failure aborted promotion: %v", err)
	}
	want := []string{"inventory", "reload:" + tenantA.String(), "reload:" + tenantB.String()}
	if strings.Join(trace, ",") != strings.Join(want, ",") {
		t.Fatalf("promotion inventory/reload trace = %v, want %v", trace, want)
	}
}

func TestReloadPDPForPromotionReplaysDurableGenerationBeforeLeaderBecomesVisible(t *testing.T) {
	tenant := model.TenantID(model.NewID())
	org := model.Org{BaseFields: model.BaseFields{ID: model.ID(tenant), TenantID: tenant}}
	visible := false
	durableGeneration := int64(1)
	runtimeGeneration := int64(0)
	listCalls, reloadCalls := 0, 0
	list := func(context.Context) ([]model.Org, error) {
		listCalls++
		return []model.Org{org}, nil
	}
	reload := func(_ context.Context, got model.TenantID) error {
		reloadCalls++
		if got != tenant {
			t.Fatalf("reload tenant = %s, want %s", got, tenant)
		}
		if visible {
			t.Fatal("Cedar reactivation ran after leader visibility")
		}
		runtimeGeneration = durableGeneration
		return nil
	}
	if err := reloadPDPForPromotion(context.Background(), list, reload, nil); err != nil {
		t.Fatalf("initial promotion reload barrier: %v", err)
	}
	if runtimeGeneration != 1 || listCalls != 1 || reloadCalls != 1 {
		t.Fatalf("initial promotion runtime/list/reload = %d/%d/%d, want G/1/1", runtimeGeneration, listCalls, reloadCalls)
	}
	// While this node follows, another leader advances durable authority to G+1.
	// A later OnPromote invokes the same barrier again before visibility, replacing
	// the stale local G rather than inheriting it across failover.
	visible = true
	durableGeneration = 2
	visible = false
	if err := reloadPDPForPromotion(context.Background(), list, reload, nil); err != nil {
		t.Fatalf("failover promotion reload barrier: %v", err)
	}
	if runtimeGeneration != 2 || listCalls != 2 || reloadCalls != 2 {
		t.Fatalf("failover promotion runtime/list/reload = %d/%d/%d, want G+1/2/2", runtimeGeneration, listCalls, reloadCalls)
	}
	visible = true
}

func TestBootServeEnumeratesPDPInventoryExactlyOnce(t *testing.T) {
	calls := 0
	eng, err := boot(context.Background(), bootConfig{
		DataDir: t.TempDir(), Engine: "sqlite", Version: "test", ServeMode: true,
		pdpListOrgs: func(context.Context, store.Store) ([]model.Org, error) {
			calls++
			return nil, nil
		},
	})
	if err != nil || eng == nil {
		t.Fatalf("serve boot = engine:%v err:%v", eng, err)
	}
	defer func() { _ = eng.Close() }()
	if calls != 1 {
		t.Fatalf("serve boot PDP inventory calls = %d, want exactly one OnPromote barrier (not a post-Run duplicate)", calls)
	}
}

func TestBootRegisteredPromotionCallbackRetriesPDPWithoutClosingFollower(t *testing.T) {
	ctx := context.Background()
	tenant := model.TenantID(model.NewID())
	org := model.Org{BaseFields: model.BaseFields{ID: model.ID(tenant), TenantID: tenant}}
	inventoryErr := errors.New("promotion inventory unavailable")
	phase := "initial"
	leaderVisible := false
	durableGeneration := int64(1)
	// Model this process as a standby that already cached G before an active peer
	// advances durable authority. The retry below must replace that G with G+1.
	runtimeGeneration := int64(1)
	var captured func(context.Context) error
	var seen store.Store
	var trace []string
	eng, err := boot(ctx, bootConfig{
		DataDir: t.TempDir(), Engine: "sqlite", Version: "test", ServeMode: true,
		pdpPromotionRegistered: func(fn func(context.Context) error) {
			captured = fn
		},
		pdpListOrgs: func(_ context.Context, st store.Store) ([]model.Org, error) {
			seen = st
			trace = append(trace, "inventory:"+phase)
			if leaderVisible {
				t.Fatal("PDP inventory ran after the test leader became visible")
			}
			switch phase {
			case "initial":
				return nil, nil
			case "failure":
				return nil, inventoryErr
			case "retry":
				return []model.Org{org}, nil
			default:
				t.Fatalf("unexpected promotion phase %q", phase)
				return nil, nil
			}
		},
		pdpReload: func(_ context.Context, got model.TenantID) error {
			trace = append(trace, "reload:"+got.String())
			if leaderVisible {
				t.Fatal("PDP reload ran after the test leader became visible")
			}
			if got != tenant {
				t.Fatalf("PDP reload tenant = %s, want %s", got, tenant)
			}
			runtimeGeneration = durableGeneration
			return nil
		},
	})
	if err != nil || eng == nil {
		t.Fatalf("initial serve boot = engine:%v err:%v", eng, err)
	}
	defer func() { _ = eng.Close() }()
	if captured == nil || seen == nil || strings.Join(trace, ",") != "inventory:initial" {
		t.Fatalf("boot did not register/use the exact promotion callback: captured:%t seen:%t trace:%v", captured != nil, seen != nil, trace)
	}

	// A failed later promotion is retryable: invoking the SAME callback must not
	// close the follower store, because the elector will call it again.
	phase = "failure"
	leaderVisible = false
	if err := captured(ctx); !errors.Is(err, inventoryErr) {
		t.Fatalf("registered promotion inventory failure = %v, want wrapped %v", err, inventoryErr)
	}
	if err := seen.System(ctx, func(store.SystemScope) error { return nil }); err != nil {
		t.Fatalf("promotion inventory failure closed the follower store: %v", err)
	}

	// Simulate an active peer committing G+1 while this process follows. A second
	// call to the exact registered callback must run the injected Cedar reload and
	// install that generation before the callback returns/visibility flips.
	phase = "retry"
	durableGeneration = 2
	if err := captured(ctx); err != nil {
		t.Fatalf("registered promotion retry: %v", err)
	}
	if runtimeGeneration != 2 || leaderVisible {
		t.Fatalf("retry promotion runtime/visibility = G%d/%t, want G2/false before visibility", runtimeGeneration, leaderVisible)
	}
	want := []string{"inventory:initial", "inventory:failure", "inventory:retry", "reload:" + tenant.String()}
	if strings.Join(trace, ",") != strings.Join(want, ",") {
		t.Fatalf("registered promotion trace = %v, want %v", trace, want)
	}
	leaderVisible = true
}

// TestBootReopensEstateWithTenant is the regression for the second-boot deadlock
// a time-to-value walkthrough surfaced: a stock SQLite serve re-opening a data dir that
// already holds a tenant must NOT hang.
//
// Root cause: the default SQLite store is single-connection (SetMaxOpenConns(1)), and
// the boot-time Cedar-PDP reload opens its OWN per-tenant View transaction. When
// that reload ran inside the System transaction that enumerates the tenants, the View
// waited forever for the one connection the System tx still held — so the SECOND boot of
// any estate with a tenant deadlocked (the first boot is fine: no tenant exists yet).
// boot() must enumerate the tenants first, then reload each outside System. This is the
// path every real operator hits the moment they create an org and restart, and the path
// the quickstart's "wire a real connector" flow (create org → restart with
// OLIVARES_SOURCES_CONFIG) depends on.
func TestBootReopensEstateWithTenant(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()

	// Boot once and persist a tenant — the state every install reaches the moment an
	// operator creates their first org — then close so the file store is reused.
	eng, err := boot(ctx, bootConfig{DataDir: dir, Engine: "sqlite", Version: "test", ServeMode: true})
	if err != nil {
		t.Fatalf("first boot: %v", err)
	}
	if err := eng.store.System(ctx, func(sys store.SystemScope) error {
		_, e := sys.CreateOrg(ctx, model.Org{Name: "Acme", Slug: "acme", Status: model.StatusActive})
		return e
	}); err != nil {
		t.Fatalf("create org: %v", err)
	}
	if err := eng.Close(); err != nil {
		t.Fatalf("close first boot: %v", err)
	}

	// Re-open the same data dir. Pre-fix this deadlocks; the Go runtime's deadlock
	// detector cannot fire under the test harness (its goroutines are live), so guard
	// with a timeout instead of hanging the whole suite.
	done := make(chan error, 1)
	var eng2 *engine
	go func() {
		var e error
		eng2, e = boot(ctx, bootConfig{DataDir: dir, Engine: "sqlite", Version: "test", ServeMode: true})
		done <- e
	}()
	select {
	case e := <-done:
		if e != nil {
			t.Fatalf("second boot: %v", e)
		}
		if err := eng2.Close(); err != nil {
			t.Errorf("close second boot: %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("second boot deadlocked re-opening an estate that already has a tenant " +
			"(PDP reload nested inside the System transaction; SQLite is single-connection)")
	}
}
