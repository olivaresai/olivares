// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
)

// driftStub simulates the deploy module endpoints the drift loop drives in-process,
// counting verify/apply calls so a test can prove the alert-only vs auto-heal policy.
type driftStub struct {
	mu          sync.Mutex
	verifyCalls int
	applyCalls  int
	inSync      bool // verify reports in_sync
}

func (s *driftStub) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "" || r.Header.Get("X-Olivares-Tenant") == "" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/m/deploy/definitions":
			// d1 active+applied (verified); d2 retired (skipped); d3 never applied (skipped).
			_, _ = w.Write([]byte(`{"items":[
				{"id":"d1","desired_status":"active","applied_version":1},
				{"id":"d2","desired_status":"retired","applied_version":1},
				{"id":"d3","desired_status":"active","applied_version":0}
			],"has_more":false}`))
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/verify"):
			s.verifyCalls++
			if s.inSync {
				_, _ = w.Write([]byte(`{"in_sync":true,"drift":[]}`))
			} else {
				_, _ = w.Write([]byte(`{"in_sync":false,"drift":[{"kind":"update"}]}`))
			}
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/apply"):
			s.applyCalls++
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"op":"apply","status":"requested","requires_approval":true}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
}

func driftLoopFor(t *testing.T, stub *driftStub, autoHeal bool) *deployDriftLoop {
	t.Helper()
	d := &deployDriftLoop{
		tenants:  []driftTenant{{id: model.TenantID("t-1"), token: "svc-token", autoHeal: autoHeal}},
		interval: time.Minute,
		log:      discardLog(),
	}
	d.useHandler(stub.handler())
	return d
}

func TestDriftLoopAlertOnlyDoesNotHeal(t *testing.T) {
	stub := &driftStub{inSync: false}
	d := driftLoopFor(t, stub, false) // alert-only
	if err := d.runOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	stub.mu.Lock()
	defer stub.mu.Unlock()
	if stub.verifyCalls != 1 {
		t.Fatalf("exactly the one active+applied deployment must be verified, got %d", stub.verifyCalls)
	}
	if stub.applyCalls != 0 {
		t.Fatalf("alert-only policy must NEVER auto-apply, got %d apply calls", stub.applyCalls)
	}
}

func TestDriftLoopAutoHealOpensGovernedApply(t *testing.T) {
	stub := &driftStub{inSync: false}
	d := driftLoopFor(t, stub, true) // auto-heal
	if err := d.runOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	stub.mu.Lock()
	defer stub.mu.Unlock()
	if stub.verifyCalls != 1 || stub.applyCalls != 1 {
		t.Fatalf("auto-heal must verify (1) then open a governed apply phase-1 (1); got verify=%d apply=%d", stub.verifyCalls, stub.applyCalls)
	}
}

func TestDriftLoopInSyncDoesNotHeal(t *testing.T) {
	stub := &driftStub{inSync: true}
	d := driftLoopFor(t, stub, true) // auto-heal, but nothing drifted
	if err := d.runOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	stub.mu.Lock()
	defer stub.mu.Unlock()
	if stub.applyCalls != 0 {
		t.Fatalf("an in-sync deployment must not trigger a heal, got %d apply calls", stub.applyCalls)
	}
}

func TestDriftLoopNilHandlerIsNoop(t *testing.T) {
	d := &deployDriftLoop{tenants: []driftTenant{{id: model.TenantID("t-1"), token: "x"}}, interval: time.Minute, log: discardLog()}
	// no useHandler => currentHandler() nil => runOnce returns nil without panicking
	if err := d.runOnce(context.Background()); err != nil {
		t.Fatalf("a drift run before the handler is bound must be a clean noop, got %v", err)
	}
}

func TestNewDeployDriftLoopNilWhenEmpty(t *testing.T) {
	if d := newDeployDriftLoop(nil, time.Minute, discardLog()); d != nil {
		t.Fatalf("an unconfigured drift loop must be nil, got %v", d)
	}
}
