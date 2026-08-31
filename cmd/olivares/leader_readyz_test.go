// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	coreengine "github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/store"
)

// standbyLeader is a non-leader elector: the store is healthy but this node is a
// hot standby in an active-passive cluster.
type standbyLeader struct{}

func (standbyLeader) IsLeader() bool                        { return false }
func (standbyLeader) Active() bool                          { return false }
func (standbyLeader) Run(context.Context) error             { return nil }
func (standbyLeader) Resign(context.Context) error          { return nil }
func (standbyLeader) Epoch() uint64                         { return 0 }
func (standbyLeader) OnPromote(func(context.Context) error) {}

// standbyStore wraps a real, reachable store but reports this node as a standby,
// so /readyz takes its leadership-drain branch while the store ping still succeeds.
type standbyStore struct {
	store.Store
}

func (standbyStore) Leader() store.LeaderElector { return standbyLeader{} }

// TestReadyzStandbyDrains is the failover contract at the probe surface: a
// STANDBY node (store reachable, not the leader) answers /readyz with 503 so
// Kubernetes removes it from the Service endpoints — but /livez stays 200 so it is
// NOT restarted. Restarting a healthy hot standby would defeat the whole point: it
// must stay up, ready to take over the instant the leader dies.
func TestReadyzStandbyDrains(t *testing.T) {
	ctx := context.Background()
	base, err := coreengine.Open(ctx, store.Config{Engine: store.EngineSQLite, DSN: ":memory:"}, nil)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = base.Close() })

	h := newProbeHandler(t, standbyStore{Store: base})

	rr := do(h, http.MethodGet, "/readyz")
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("/readyz on a standby = %d, want 503 (drain from the Service)", rr.Code)
	}
	var ready struct {
		Status string `json:"status"`
		Store  string `json:"store"`
		Leader bool   `json:"leader"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &ready); err != nil {
		t.Fatalf("decode /readyz body %q: %v", rr.Body.String(), err)
	}
	if ready.Status != "standby" || ready.Store != "up" || ready.Leader {
		t.Errorf("/readyz body = %q, want status=standby store=up leader=false", rr.Body.String())
	}

	// /livez must stay 200 — a standby is ALIVE, just not ready; restarting it (the
	// livenessProbe action) would be wrong.
	rr = do(h, http.MethodGet, "/livez")
	if rr.Code != http.StatusOK {
		t.Fatalf("/livez on a standby = %d, want 200 (a standby must NOT be restarted)", rr.Code)
	}
}

// TestPodReadyzReachesEngineThroughSPA is the stage-2 half of the same
// contract, and the C1 regression guard for the NEW probe: /pod-readyz must
// reach the engine handler through the SPA wrapper (a 200 from the SPA shell would
// make every pod look healthy forever) and must answer 200 on a STANDBY — that is
// what lets the kubelet mark a hot standby Ready so a rolling update progresses.
func TestPodReadyzReachesEngineThroughSPA(t *testing.T) {
	ctx := context.Background()
	base, err := coreengine.Open(ctx, store.Config{Engine: store.EngineSQLite, DSN: ":memory:"}, nil)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = base.Close() })

	h := newProbeHandler(t, standbyStore{Store: base})
	rr := do(h, http.MethodGet, "/pod-readyz")
	if rr.Code != http.StatusOK {
		t.Fatalf("/pod-readyz on a standby = %d, want 200 (pod health is leader-agnostic)", rr.Code)
	}
	if ct := rr.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("/pod-readyz content-type = %q, want application/json (engine handler, not the SPA shell)", ct)
	}
	var pod struct {
		Status string `json:"status"`
		Store  string `json:"store"`
		Leader bool   `json:"leader"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &pod); err != nil {
		t.Fatalf("decode /pod-readyz body %q: %v", rr.Body.String(), err)
	}
	if pod.Status != "ok" || pod.Store != "up" || pod.Leader {
		t.Errorf("/pod-readyz body = %q, want status=ok store=up leader=false", rr.Body.String())
	}

	// A wedged store still fails it: pod health includes the store dependency, so
	// the pod leaves the Service endpoints (readiness) without being restarted.
	down := newProbeHandler(t, pingFailStore{Store: base, err: errors.New("store wedged")})
	if rr := do(down, http.MethodGet, "/pod-readyz"); rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("/pod-readyz with the store down = %d, want 503", rr.Code)
	}
}
