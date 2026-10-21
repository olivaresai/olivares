// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

func TestDRHarnessBorrowedStoreSkipsAcquisitionAndKeepsIdentity(t *testing.T) {
	dir := t.TempDir()
	st := openDRStore(t, dir)
	openCalls := 0
	observeOpen := func(t *testing.T) store.Store {
		t.Helper()
		openCalls++
		return defaultHarnessStore(t)
	}

	t.Run("borrowers", func(t *testing.T) {
		h1 := newDRHarness(t, dir, st, "", observeOpen)
		clk := &movableClock{now: time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)}
		h2 := newDRHarnessAt(t, dir, st, clk, observeOpen)
		if openCalls != 0 {
			t.Fatalf("borrowed DR constructors opened %d discarded stores", openCalls)
		}
		if h1.st != st || h2.st != st {
			t.Fatal("DR harness did not retain the borrowed store identity")
		}
		if h1.authr == h2.authr || h1.signer == h2.signer || h1.setupTok == h2.setupTok {
			t.Fatal("separate DR harnesses reused per-harness credentials")
		}

		admin := h1.adminLogin()
		if _, err := h2.authr.Authenticate(context.Background(), admin); err != nil {
			t.Fatalf("second harness authenticator does not use borrowed store: %v", err)
		}
		r := h2.do("GET", "/v1/console/dr/schedule", admin, nil, nil)
		if r.code != http.StatusOK {
			t.Fatalf("second harness server does not use borrowed store/authenticator: %d %s", r.code, r.raw)
		}
	})

	if err := st.Ping(context.Background()); err != nil {
		t.Fatalf("borrower cleanup closed the owner store: %v", err)
	}
}

func TestDRHarnessAcquisitionControlAndEstateIsolation(t *testing.T) {
	dir := t.TempDir()
	st := openDRStore(t, dir)
	openCalls := 0
	var configuredAuth *auth.Authenticator
	legacy := newHarnessOptsFromStoreSource(t, harnessStoreSource{
		open: func(t *testing.T) store.Store {
			t.Helper()
			openCalls++
			return defaultHarnessStore(t)
		},
	}, func(o *api.Options) {
		o.Store = st
		configuredAuth = auth.NewAuthenticator(st, nil)
		o.Authenticator = configuredAuth
		o.Version = "26.900"
		o.DR = &api.DRConfig{DataDir: dir, EngineKind: "sqlite"}
	})
	if openCalls != 1 {
		t.Fatalf("prior constructor shape opened %d stores, want 1", openCalls)
	}
	if legacy.st == st || legacy.authr == configuredAuth {
		t.Fatal("prior constructor shape did not expose its discarded store/authenticator")
	}
	admin := legacy.adminLogin()
	if _, err := legacy.authr.Authenticate(context.Background(), admin); err == nil {
		t.Fatal("discarded authenticator unexpectedly observed the configured store session")
	}

	ownerDir := t.TempDir()
	ownerStore := openDRStore(t, ownerDir)
	ownerHarness := newDRHarness(t, ownerDir, ownerStore, "")
	var closedEstate store.Store
	t.Run("independent estate", func(t *testing.T) {
		otherDir := t.TempDir()
		closedEstate = openDRStore(t, otherDir)
		other := newDRHarnessAt(t, otherDir, closedEstate, model.SystemClock{})
		otherAdmin := other.adminLogin()
		if _, err := ownerHarness.authr.Authenticate(context.Background(), otherAdmin); err == nil {
			t.Fatal("credentials crossed independent harness stores")
		}
	})
	if err := closedEstate.Ping(context.Background()); err == nil {
		t.Fatal("independent estate owner did not close its store")
	}
	if err := ownerStore.Ping(context.Background()); err != nil {
		t.Fatalf("closing an independent estate closed the owner store: %v", err)
	}
	_ = ownerHarness.adminLogin()
}

// TestDefaultHarnessStoresShareOneInitializationNotOneDatabase guards the race timeout of this package.
// Every newHarness used to migrate a fresh store from nothing, which is most of the time of
// the package (measured under -race: 5.2 s for a fresh store, 1.0 s to reopen an initialized
// copy), so core/api no longer fit its race binary's timeout. The first initialization runs
// once per test binary; each harness still receives its own database.
func TestDefaultHarnessStoresShareOneInitializationNotOneDatabase(t *testing.T) {
	ctx := context.Background()
	a, b := defaultHarnessStore(t), defaultHarnessStore(t)
	if n := harnessStoreInitializations.Load(); n != 1 {
		t.Errorf("default harness stores ran %d first initializations in this test binary, want 1", n)
	}

	if err := a.System(ctx, func(sys store.SystemScope) error {
		_, err := sys.CreateOrg(ctx, model.Org{Name: "only-a", Slug: "only-a", Status: model.StatusActive})
		return err
	}); err != nil {
		t.Fatalf("create an organization in the first store: %v", err)
	}
	systemOrg := map[string]model.Org{}
	for name, st := range map[string]store.Store{"first": a, "second": b} {
		var orgs []model.Org
		var chain store.VerifyReport
		if err := st.System(ctx, func(sys store.SystemScope) error {
			var err error
			if orgs, err = sys.ListOrgs(ctx); err != nil {
				return err
			}
			chain, err = sys.Verify(ctx, model.SystemTenantID, 0)
			return err
		}); err != nil {
			t.Fatalf("%s store: list organizations and verify the system chain: %v", name, err)
		}
		if !chain.OK || chain.BreakAt != 0 {
			t.Errorf("%s store: system audit chain broken at %d (%s)", name, chain.BreakAt, chain.Reason)
		}
		slugs := map[string]bool{}
		for _, o := range orgs {
			slugs[o.Slug] = true
			if o.TenantID == model.SystemTenantID {
				systemOrg[name] = o
			}
		}
		if _, ok := systemOrg[name]; !ok {
			t.Errorf("%s store holds no system tenant", name)
		}
		if want := name == "first"; slugs["only-a"] != want {
			t.Errorf("%s store sees the first store's organization = %v, want %v", name, slugs["only-a"], want)
		}
	}
	// The counter sees only the template. A harness that opened and initialized its own store
	// again would still create the system tenant, at its own time: both stores must carry the
	// row of the one initialization they were copied from.
	if fa, fb := systemOrg["first"].CreatedAt, systemOrg["second"].CreatedAt; fa.IsZero() || fa.String() != fb.String() {
		t.Errorf("system tenant created at %v and %v, want the one initialization both stores copy", fa, fb)
	}
}
