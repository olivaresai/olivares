// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package ratelimit_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/api/ratelimit"
	"github.com/olivaresai/olivares/core/metrics"
)

// fakeStore scripts the shared-store seam: a programmable outcome plus a call
// counter, so the breaker's skip-the-store behavior is directly observable.
type fakeStore struct {
	calls   int
	err     error
	allowed bool
	tokens  []float64
}

func (f *fakeStore) Take(_ context.Context, reqs []ratelimit.StoreRequest) (bool, []ratelimit.StoreState, error) {
	f.calls++
	if f.err != nil {
		return false, nil, f.err
	}
	states := make([]ratelimit.StoreState, len(reqs))
	for i := range states {
		states[i] = ratelimit.StoreState{Tokens: f.tokens[i]}
	}
	return f.allowed, states, nil
}

func storeTestConfig(fs ratelimit.Store, now func() time.Time) ratelimit.Config {
	return ratelimit.Config{
		Tiers: map[string]ratelimit.TierLimits{
			"default": {
				PerClass: map[ratelimit.EndpointClass]ratelimit.Limit{
					ratelimit.ClassRead:  {Rate: 1, Burst: 4},
					ratelimit.ClassWrite: {Rate: 1, Burst: 4},
				},
				Total: ratelimit.Limit{Rate: 1, Burst: 8},
			},
		},
		DefaultTier: "default",
		Store:       fs,
		Now:         now,
	}
}

// TestStoreDecisionMath: the store path must advertise the SAME binding
// decision the in-proc path would (shared bindingFrom): the bucket with the
// fewest tokens binds Limit/Remaining/Retry-After.
func TestStoreDecisionMath(t *testing.T) {
	fs := &fakeStore{allowed: false, tokens: []float64{2.5, 0.25}}
	clk := newClock()
	l := ratelimit.New(storeTestConfig(fs, clk.now), nil)

	d := l.Allow(context.Background(), "tn:x", "default", ratelimit.ClassRead)
	if d.OK || !d.Limited {
		t.Fatalf("scripted denial must deny: %+v", d)
	}
	// Binding bucket = aggregate (0.25 tokens, burst 8, rate 1):
	// Remaining floor(0.25)=0; RetryAfter ceil(0.75/1)=1; Reset ceil(7.75/1)=8.
	if d.Limit != 8 || d.Remaining != 0 || d.RetryAfter != 1 || d.Reset != 8 {
		t.Fatalf("binding math diverged from in-proc: %+v", d)
	}
	if fs.calls != 1 {
		t.Fatalf("store should be called once, got %d", fs.calls)
	}
}

// TestStoreFallbackAndBreaker: store failures fall back to the LOCAL shards
// (bounded enforcement, never unlimited, never a 429 storm), count the
// fallback metric, and after 3 consecutive failures the breaker opens —
// takes skip the store for the cooldown, then probe half-open.
func TestStoreFallbackAndBreaker(t *testing.T) {
	fs := &fakeStore{err: errors.New("pg down")}
	clk := newClock()
	reg := metrics.New("test", clk.now())
	l := ratelimit.New(storeTestConfig(fs, clk.now), reg)

	// 3 failing takes: each falls back locally (admitted: local burst holds).
	for i := 0; i < 3; i++ {
		if d := l.Allow(context.Background(), "tn:x", "default", ratelimit.ClassRead); !d.OK {
			t.Fatalf("fallback take %d must be served by the local shards", i)
		}
	}
	if fs.calls != 3 {
		t.Fatalf("store should see the 3 failing takes, got %d", fs.calls)
	}
	if got := seriesValue(t, reg, "olivares_http_ratelimit_store_fallback_total"); got != 3 {
		t.Fatalf("fallback counter: want 3, got %v", got)
	}

	// Breaker is now open: the next takes never touch the store.
	for i := 0; i < 5; i++ {
		l.Allow(context.Background(), "tn:x", "default", ratelimit.ClassRead)
	}
	if fs.calls != 3 {
		t.Fatalf("breaker open: store must be skipped, calls %d", fs.calls)
	}
	if got := seriesValue(t, reg, "olivares_http_ratelimit_store_up"); got != 0 {
		t.Fatalf("store_up gauge must be 0 while open, got %v", got)
	}

	// After the cooldown the next take probes half-open; the store recovered.
	fs.err = nil
	fs.allowed = true
	fs.tokens = []float64{3, 7}
	clk.advance(6 * time.Second)
	if d := l.Allow(context.Background(), "tn:x", "default", ratelimit.ClassRead); !d.OK {
		t.Fatal("recovered store should admit")
	}
	if fs.calls != 4 {
		t.Fatalf("half-open probe should reach the store, calls %d", fs.calls)
	}
	if got := seriesValue(t, reg, "olivares_http_ratelimit_store_up"); got != 1 {
		t.Fatalf("store_up gauge must be 1 after recovery, got %v", got)
	}
}

// TestStoreParentCancelNotCounted: a dying request (client disconnect) is not
// a store failure — no fallback count, no breaker trip.
func TestStoreParentCancelNotCounted(t *testing.T) {
	fs := &fakeStore{err: context.Canceled}
	clk := newClock()
	reg := metrics.New("test", clk.now())
	l := ratelimit.New(storeTestConfig(fs, clk.now), reg)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for i := 0; i < 5; i++ {
		l.Allow(ctx, "tn:x", "default", ratelimit.ClassRead)
	}
	if got := seriesValue(t, reg, "olivares_http_ratelimit_store_fallback_total"); got != 0 {
		t.Fatalf("client disconnects must not count as store fallbacks, got %v", got)
	}
	if fs.calls != 5 {
		t.Fatalf("breaker must not trip on client disconnects, store calls %d", fs.calls)
	}
}

// TestStoreReportOnly: mode logic is independent of the bucket backend — a
// store denial in report-only admits and counts decision=report_only.
func TestStoreReportOnly(t *testing.T) {
	fs := &fakeStore{allowed: false, tokens: []float64{0, 0}}
	clk := newClock()
	reg := metrics.New("test", clk.now())
	cfg := storeTestConfig(fs, clk.now)
	cfg.Mode = ratelimit.ModeReportOnly
	l := ratelimit.New(cfg, reg)

	d := l.Allow(context.Background(), "tn:x", "default", ratelimit.ClassRead)
	if !d.OK || !d.Limited {
		t.Fatalf("report-only must admit but flag Limited: %+v", d)
	}
	if got := seriesValue(t, reg, `olivares_http_ratelimit_decisions_total{class="read",decision="report_only"}`); got != 1 {
		t.Fatalf("report_only decision not counted: %v", got)
	}
}

// TestStoreClientDeadlineNotCounted: a client-supplied deadline expiring is a
// dying request, not store-health evidence — otherwise a tenant sending tiny
// gRPC deadlines could open the breaker at will and degrade every node to
// per-node quotas.
func TestStoreClientDeadlineNotCounted(t *testing.T) {
	fs := &fakeStore{err: context.DeadlineExceeded}
	clk := newClock()
	reg := metrics.New("test", clk.now())
	l := ratelimit.New(storeTestConfig(fs, clk.now), reg)

	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	for i := 0; i < 4; i++ { // 4 = the local read burst; all must be served locally
		if d := l.Allow(ctx, "tn:x", "default", ratelimit.ClassRead); !d.OK {
			t.Fatalf("take %d must fall back to local shards", i)
		}
	}
	if got := seriesValue(t, reg, "olivares_http_ratelimit_store_fallback_total"); got != 0 {
		t.Fatalf("expired client deadlines must not count as store fallbacks, got %v", got)
	}
	if fs.calls != 4 {
		t.Fatalf("breaker must not trip on client deadlines, store calls %d", fs.calls)
	}
}

// TestStoreHalfOpenSingleProbe: after the cooldown exactly ONE take probes the
// still-down store (re-extending the open window); the rest stay local — a
// hard-down store costs one stalled request per cooldown, never a stampede.
func TestStoreHalfOpenSingleProbe(t *testing.T) {
	fs := &fakeStore{err: errors.New("pg down")}
	clk := newClock()
	l := ratelimit.New(storeTestConfig(fs, clk.now), nil)

	for i := 0; i < 3; i++ { // open the breaker
		l.Allow(context.Background(), "tn:x", "default", ratelimit.ClassRead)
	}
	if fs.calls != 3 {
		t.Fatalf("setup: want 3 store calls, got %d", fs.calls)
	}
	clk.advance(6 * time.Second) // cooldown expired; store still down
	for i := 0; i < 10; i++ {
		l.Allow(context.Background(), "tn:x", "default", ratelimit.ClassRead)
	}
	if fs.calls != 4 {
		t.Fatalf("exactly one half-open probe may hit the store, got %d extra", fs.calls-3)
	}
	// The failed probe left the breaker open: within the re-extended window no
	// further takes reach the store.
	clk.advance(2 * time.Second)
	l.Allow(context.Background(), "tn:x", "default", ratelimit.ClassRead)
	if fs.calls != 4 {
		t.Fatalf("failed probe must leave the breaker open, calls %d", fs.calls)
	}
	// After another full cooldown the next probe wins — and the store recovered.
	fs.err = nil
	fs.allowed = true
	fs.tokens = []float64{3, 7}
	clk.advance(6 * time.Second)
	if d := l.Allow(context.Background(), "tn:x", "default", ratelimit.ClassRead); !d.OK {
		t.Fatal("recovered probe should admit")
	}
	if fs.calls != 5 {
		t.Fatalf("recovery probe should be call 5, got %d", fs.calls)
	}
	// Closed again: takes flow to the store.
	l.Allow(context.Background(), "tn:x", "default", ratelimit.ClassRead)
	if fs.calls != 6 {
		t.Fatalf("breaker should be closed after a successful probe, calls %d", fs.calls)
	}
}
