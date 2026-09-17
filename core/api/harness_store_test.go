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
		o.Version = "26.9.0"
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
