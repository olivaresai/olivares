// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api_test

import (
	"context"
	"errors"
	"net"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/api/genpb/apiv1"
	"github.com/olivaresai/olivares/core/store"
)

// standbyElector is an armed, non-leading elector: this node is a hot standby in
// an active-passive cluster — the store is reachable, but another node
// holds the write lock.
type standbyElector struct{}

func (standbyElector) IsLeader() bool                        { return false }
func (standbyElector) Active() bool                          { return false }
func (standbyElector) Run(context.Context) error             { return nil }
func (standbyElector) Resign(context.Context) error          { return nil }
func (standbyElector) Epoch() uint64                         { return 0 }
func (standbyElector) OnPromote(func(context.Context) error) {}

// standbyStore wraps a healthy store but reports this node as a standby.
type standbyStore struct{ store.Store }

func (standbyStore) Leader() store.LeaderElector { return standbyElector{} }

// promotingElector is an adversarial legacy elector: it claims Active before
// IsLeader. The real pgElector no longer does this during OnPromote, but keeping
// the inconsistent fake proves /readyz is explicitly bound to established
// leadership rather than accidentally reverting to Active.
type promotingElector struct{ established atomic.Bool }

func (e *promotingElector) IsLeader() bool                      { return e.established.Load() }
func (*promotingElector) Active() bool                          { return true }
func (*promotingElector) Run(context.Context) error             { return nil }
func (*promotingElector) Resign(context.Context) error          { return nil }
func (*promotingElector) Epoch() uint64                         { return 0 }
func (*promotingElector) OnPromote(func(context.Context) error) {}

type promotingStore struct {
	store.Store
	elector *promotingElector
}

func (s promotingStore) Leader() store.LeaderElector { return s.elector }

// downStore wraps a store whose backend is unreachable (Ping fails) while the
// process itself is alive — the store-outage branch of both readiness probes.
type downStore struct{ store.Store }

func (downStore) Ping(context.Context) error { return errors.New("store down") }

// TestPodReadyzIsLeaderAgnostic is the Patroni split at the probe surface
// (stage-2, design §B.1): /pod-readyz answers POD health — store reachable,
// engine serving — with NO leadership check, so an HA standby is Ready to the
// kubelet and a rolling update can progress past it. /readyz keeps its
// leader-drain meaning, so the leader-selecting Service still routes only to the
// active writer.
func TestPodReadyzIsLeaderAgnostic(t *testing.T) {
	h := newHarnessOpts(t, func(o *api.Options) { o.Store = standbyStore{Store: o.Store} })

	r := h.do("GET", "/pod-readyz", "", nil, nil)
	if r.code != http.StatusOK {
		t.Fatalf("/pod-readyz on a standby = %d %s, want 200 (pod health is leader-agnostic)", r.code, r.raw)
	}
	if r.body["store"] != "up" || r.body["leader"] != false {
		t.Errorf("/pod-readyz body = %s, want store=up leader=false", r.raw)
	}

	// The contract is untouched: /readyz still drains a standby.
	if r := h.do("GET", "/readyz", "", nil, nil); r.code != http.StatusServiceUnavailable {
		t.Fatalf("/readyz on a standby = %d, want 503 (leader-only drain preserved)", r.code)
	}
}

// TestReadyzDrainsUntilPromotionIsEstablished prevents a promotion bootstrap
// from publishing readiness while durable Cedar/runtime reactivation is still
// running. The store can intentionally permit bootstrap writes in that window;
// it must not become a serving endpoint until IsLeader is published.
func TestReadyzDrainsUntilPromotionIsEstablished(t *testing.T) {
	elector := &promotingElector{}
	h := newHarnessOpts(t, func(o *api.Options) {
		o.Store = promotingStore{Store: o.Store, elector: elector}
	})

	if r := h.do("GET", "/readyz", "", nil, nil); r.code != http.StatusServiceUnavailable {
		t.Fatalf("/readyz while promotion is in progress = %d %s, want 503", r.code, r.raw)
	}

	elector.established.Store(true)
	if r := h.do("GET", "/readyz", "", nil, nil); r.code != http.StatusOK {
		t.Fatalf("/readyz after promotion establishes leadership = %d %s, want 200", r.code, r.raw)
	}
}

// TestPodReadyzFailsOnStoreDown pins the dependency check: /pod-readyz is pod
// HEALTH, not "the process answers" — a pod whose store is unreachable must leave
// the Service endpoints (that is /livez's job to NOT do).
func TestPodReadyzFailsOnStoreDown(t *testing.T) {
	h := newHarnessOpts(t, func(o *api.Options) { o.Store = downStore{Store: o.Store} })

	if r := h.do("GET", "/pod-readyz", "", nil, nil); r.code != http.StatusServiceUnavailable {
		t.Fatalf("/pod-readyz with the store down = %d, want 503", r.code)
	}
	if r := h.do("GET", "/livez", "", nil, nil); r.code != http.StatusOK {
		t.Fatalf("/livez with the store down = %d, want 200 (a dependency outage must not restart the pod)", r.code)
	}
}

// TestLeaderRouteGateBlocksApplicationRoutesOnStandby is the split-brain backstop
// of the leader-routing split (design §B.1 "preserving leader-only routing"): once
// standbys are Ready they are reachable, so a stale leader label — or a direct dial
// — could land an application request on a standby. The gate answers the SAME
// retryable 503 not_leader the store's write fence produces, while every
// operational probe stays reachable so the kubelet and Prometheus still work.
func TestLeaderRouteGateBlocksApplicationRoutesOnStandby(t *testing.T) {
	h := newHarnessOpts(t, func(o *api.Options) {
		o.Store = standbyStore{Store: o.Store}
		o.LeaderRouteGate = true
	})

	r := h.do("GET", "/v1/server-info", "", nil, nil)
	if r.code != http.StatusServiceUnavailable {
		t.Fatalf("/v1/server-info on a gated standby = %d %s, want 503", r.code, r.raw)
	}
	if errObj, _ := r.body["error"].(map[string]any); errObj == nil || errObj["code"] != "not_leader" {
		t.Errorf("gated response = %s, want error.code=not_leader", r.raw)
	}

	// Operational surface stays reachable on a standby: the kubelet probes it, the
	// scraper scrapes it, and an operator can still read its status.
	for _, p := range []string{"/livez", "/pod-readyz", "/healthz", "/metrics", "/status"} {
		if r := h.do("GET", p, "", nil, nil); r.code == http.StatusServiceUnavailable {
			t.Errorf("%s on a gated standby = 503; operational endpoints must stay reachable", p)
		}
	}
	// /readyz keeps answering (with its own 503 standby verdict), not the gate's.
	if r := h.do("GET", "/readyz", "", nil, nil); r.body["status"] != "standby" {
		t.Errorf("/readyz on a gated standby = %s, want the leader-drain standby verdict, not the route gate", r.raw)
	}
}

// TestLeaderRouteGateOffByDefault pins the blast radius: the gate is opt-in
// (the operator enables it only for pods in the leader-routing layout), so an
// existing HA deployment keeps serving reads from a standby exactly as before.
func TestLeaderRouteGateOffByDefault(t *testing.T) {
	h := newHarnessOpts(t, func(o *api.Options) { o.Store = standbyStore{Store: o.Store} })
	if r := h.do("GET", "/v1/server-info", "", nil, nil); r.code != http.StatusOK {
		t.Fatalf("/v1/server-info on an ungated standby = %d %s, want 200 (unchanged behavior)", r.code, r.raw)
	}
}

// TestLeaderRouteGateAllowsLeader proves the gate is inert on the active writer:
// the default (always-leader) elector serves every route normally.
func TestLeaderRouteGateAllowsLeader(t *testing.T) {
	h := newHarnessOpts(t, func(o *api.Options) { o.LeaderRouteGate = true })
	if r := h.do("GET", "/v1/server-info", "", nil, nil); r.code != http.StatusOK {
		t.Fatalf("/v1/server-info on a gated LEADER = %d %s, want 200", r.code, r.raw)
	}
}

// flippableElector is an elector whose leadership can be revoked mid-test — the
// demotion a long-lived stream must not survive.
type flippableElector struct{ leader atomic.Bool }

func (f *flippableElector) IsLeader() bool                      { return f.leader.Load() }
func (f *flippableElector) Active() bool                        { return f.leader.Load() }
func (*flippableElector) Run(context.Context) error             { return nil }
func (*flippableElector) Resign(context.Context) error          { return nil }
func (*flippableElector) Epoch() uint64                         { return 1 }
func (*flippableElector) OnPromote(func(context.Context) error) {}

type flippableStore struct {
	store.Store
	elector *flippableElector
}

func (f flippableStore) Leader() store.LeaderElector { return f.elector }

// TestGRPCLeaderGateEndsStreamOnDemotion pins the hole a create-time-only check
// would leave: the collector ingest RPC is a LONG-LIVED stream. If leadership is
// verified only when the stream opens, a collector that connected to the leader
// keeps pushing observations through that same stream after the pod is demoted —
// two nodes accepting application traffic at once, on a path the store's write
// fence does not cover. Every received message must therefore re-check leadership.
func TestGRPCLeaderGateEndsStreamOnDemotion(t *testing.T) {
	elector := &flippableElector{}
	elector.leader.Store(true)
	h := newHarnessOpts(t, func(o *api.Options) {
		o.Store = flippableStore{Store: o.Store, elector: elector}
		o.LeaderRouteGate = true
	})

	recvd := 0
	gated := h.srv.LeaderGateStreamInterceptorForTest()
	stream := &fakeServerStream{ctx: context.Background()}
	err := gated(nil, stream, &grpc.StreamServerInfo{FullMethod: "/olivares.v1.IngestService/Push"},
		func(_ any, ss grpc.ServerStream) error {
			// The collector's loop: receive until the server refuses.
			for {
				if err := ss.RecvMsg(nil); err != nil {
					return err
				}
				recvd++
				if recvd == 2 {
					elector.leader.Store(false) // this pod is demoted mid-stream
				}
				if recvd > 10 {
					return errors.New("stream never ended after demotion")
				}
			}
		})
	if status.Code(err) != codes.Unavailable {
		t.Fatalf("stream ended with %v, want codes.Unavailable after demotion", err)
	}
	if recvd != 2 {
		t.Errorf("accepted %d messages, want exactly the 2 received while still leader", recvd)
	}
}

// fakeServerStream is a minimal grpc.ServerStream whose RecvMsg always succeeds,
// so the test observes only the gate's decisions.
type fakeServerStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (f *fakeServerStream) Context() context.Context { return f.ctx }
func (f *fakeServerStream) RecvMsg(any) error        { return nil }

// TestGRPCLeaderRouteGate is the gRPC half of the same backstop: an application
// RPC on a gated standby returns the retryable codes.Unavailable, while the
// standard health service — the gRPC analog of the kubelet probes — still
// answers on every pod.
func TestGRPCLeaderRouteGate(t *testing.T) {
	h := newHarnessOpts(t, func(o *api.Options) {
		o.Store = standbyStore{Store: o.Store}
		o.LeaderRouteGate = true
	})

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	gs := h.srv.NewGRPCServer()
	go func() { _ = gs.Serve(lis) }()
	defer gs.Stop()

	conn, err := grpc.NewClient(lis.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if _, err := apiv1.NewControlPlaneClient(conn).GetServerInfo(ctx, &apiv1.Empty{}); status.Code(err) != codes.Unavailable {
		t.Fatalf("GetServerInfo on a gated standby = %v, want codes.Unavailable", err)
	}
	if _, err := healthpb.NewHealthClient(conn).Check(ctx, &healthpb.HealthCheckRequest{}); err != nil {
		t.Fatalf("health Check on a gated standby = %v, want served (probes must reach every pod)", err)
	}
}
