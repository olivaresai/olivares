// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package ratelimit_test

import (
	"bytes"
	"context"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/api/ratelimit"
	"github.com/olivaresai/olivares/core/metrics"
	"github.com/olivaresai/olivares/core/model"
)

// seriesValue scrapes reg and returns the numeric value of the first exposition
// line beginning with prefix (the fully-labeled series name).
func seriesValue(t *testing.T, reg *metrics.Registry, prefix string) float64 {
	t.Helper()
	var b bytes.Buffer
	reg.WritePrometheus(&b)
	for _, line := range strings.Split(b.String(), "\n") {
		if strings.HasPrefix(line, prefix+" ") {
			f := strings.Fields(line)
			v, err := strconv.ParseFloat(f[len(f)-1], 64)
			if err != nil {
				t.Fatalf("parse %q: %v", line, err)
			}
			return v
		}
	}
	t.Fatalf("series %q not found in scrape:\n%s", prefix, b.String())
	return 0
}

// fakeClock returns wall-clock-only time.Time values (no monotonic reading),
// exactly like the production model.Clock (Timestamp normalizes to UTC, stripping
// monotonic). Seeding from time.Date and advancing with Add keeps it monotonic-free,
// so the tests exercise the same arithmetic prod does — including backward steps.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newClock() *fakeClock {
	return &fakeClock{t: time.Date(2026, 6, 9, 12, 0, 0, 0, time.UTC)}
}
func (c *fakeClock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}
func (c *fakeClock) set(t time.Time) { c.mu.Lock(); c.t = t; c.mu.Unlock() }

const tenant = model.TenantID("11111111-1111-1111-1111-111111111111")

func newLimiter(t *testing.T, cfg ratelimit.Config) *ratelimit.Limiter {
	t.Helper()
	return ratelimit.New(cfg, nil)
}

// drain fires n read requests and returns how many were admitted.
func drain(l *ratelimit.Limiter, id string, tier string, class ratelimit.EndpointClass, n int) (allowed int) {
	for i := 0; i < n; i++ {
		if l.Allow(context.Background(), id, tier, class).OK {
			allowed++
		}
	}
	return allowed
}

func TestBurstThenDenyThenRefill(t *testing.T) {
	clk := newClock()
	l := newLimiter(t, ratelimit.Config{Now: clk.now}) // built-in default tiers

	// default tier read: burst 100, rate 50/s. The first 100 admit (burst), then deny.
	if got := drain(l, "tn:"+tenant.String(), ratelimit.TierDefault, ratelimit.ClassRead, 100); got != 100 {
		t.Fatalf("burst: admitted %d, want 100", got)
	}
	d := l.Allow(context.Background(), "tn:"+tenant.String(), ratelimit.TierDefault, ratelimit.ClassRead)
	if d.OK {
		t.Fatal("101st request must be denied")
	}
	if d.RetryAfter < 1 {
		t.Fatalf("denied decision must carry Retry-After >= 1, got %d", d.RetryAfter)
	}
	if d.Remaining != 0 {
		t.Fatalf("denied Remaining = %d, want 0", d.Remaining)
	}

	// Obeying Retry-After then retrying must succeed (the honesty property).
	clk.advance(time.Duration(d.RetryAfter) * time.Second)
	if !l.Allow(context.Background(), "tn:"+tenant.String(), ratelimit.TierDefault, ratelimit.ClassRead).OK {
		t.Fatal("after waiting Retry-After, the request must be admitted")
	}
}

// A wall-clock step backwards (NTP) must never drive tokens negative or 429 a
// blameless tenant — the monotonic-strip safety clamp.
func TestClockRegressionDoesNotPenalize(t *testing.T) {
	clk := newClock()
	l := newLimiter(t, ratelimit.Config{Now: clk.now})
	id := "tn:" + tenant.String()

	// Spend ~half the read burst.
	drain(l, id, ratelimit.TierDefault, ratelimit.ClassRead, 50)
	start := clk.now()

	// Step the clock 2s backwards.
	clk.set(start.Add(-2 * time.Second))
	d := l.Allow(context.Background(), id, ratelimit.TierDefault, ratelimit.ClassRead)
	if !d.OK {
		t.Fatalf("a backward clock step must not deny a tenant with tokens left (got !OK, retry=%d)", d.RetryAfter)
	}
	if d.Remaining < 0 || d.Remaining > 100 {
		t.Fatalf("Remaining out of range after clock regression: %d", d.Remaining)
	}
	// Forward again: still healthy, no absurd Retry-After lurking.
	clk.set(start.Add(time.Second))
	if d := l.Allow(context.Background(), id, ratelimit.TierDefault, ratelimit.ClassRead); !d.OK {
		t.Fatalf("forward step after regression must admit, got retry=%d", d.RetryAfter)
	}
}

// A degenerate operator tier (Rate<=0 / Burst<1) must clamp to the hard floor:
// neither "unlimited" nor a permanent-deny brick.
func TestDegenerateLimitClampsToFloor(t *testing.T) {
	clk := newClock()
	cfg := ratelimit.Config{
		Now:         clk.now,
		DefaultTier: "broken",
		Tiers: map[string]ratelimit.TierLimits{
			"broken": {
				PerClass: map[ratelimit.EndpointClass]ratelimit.Limit{
					ratelimit.ClassRead:  {Rate: 0, Burst: 0.5},  // both degenerate
					ratelimit.ClassWrite: {Rate: -5, Burst: 100}, // negative rate
				},
				Total: ratelimit.Limit{Rate: 0, Burst: 0}, // degenerate aggregate
			},
		},
	}
	l := newLimiter(t, cfg)
	id := "tn:" + tenant.String()
	// Hard floor is burst 20: the first request must admit (not a permanent-deny brick),
	// and an unbounded flood must eventually deny (not "unlimited").
	if !l.Allow(context.Background(), id, "broken", ratelimit.ClassRead).OK {
		t.Fatal("degenerate limit must clamp to a usable floor, not permanently deny")
	}
	allowed := 1 + drain(l, id, "broken", ratelimit.ClassRead, 1000)
	if allowed >= 1000 {
		t.Fatalf("degenerate limit must not clamp to unlimited; admitted %d/1001", allowed)
	}
	if allowed > 25 { // floor burst 20 + a few refills within the same instant (none, clock frozen)
		t.Fatalf("expected ~floor(20) admits, got %d", allowed)
	}
}

// Exhausting the read class must not deny writes (per-class isolation) — until the
// shared aggregate is itself drained.
func TestPerClassIsolation(t *testing.T) {
	clk := newClock()
	l := newLimiter(t, ratelimit.Config{Now: clk.now})
	id := "tn:" + tenant.String()

	// Drain read burst (100). Default Total burst is 120, so 20 aggregate tokens remain.
	drain(l, id, ratelimit.TierDefault, ratelimit.ClassRead, 100)
	if l.Allow(context.Background(), id, ratelimit.TierDefault, ratelimit.ClassRead).OK {
		t.Fatal("read class should be exhausted")
	}
	// A write still admits: its own class bucket is full and the aggregate still has tokens.
	if !l.Allow(context.Background(), id, ratelimit.TierDefault, ratelimit.ClassWrite).OK {
		t.Fatal("write must remain admitted while only the read class is exhausted")
	}
}

// The aggregate Total bucket caps a tenant's whole footprint even when a per-class
// bucket still has tokens.
func TestAggregateCeiling(t *testing.T) {
	clk := newClock()
	l := newLimiter(t, ratelimit.Config{Now: clk.now})
	id := "tn:" + tenant.String()

	// Default Total burst is 120. Spend 100 reads + 20 writes = 120 aggregate tokens.
	drain(l, id, ratelimit.TierDefault, ratelimit.ClassRead, 100)
	drain(l, id, ratelimit.TierDefault, ratelimit.ClassWrite, 20)
	// The write class bucket still has 20 tokens, but the aggregate is empty: deny.
	d := l.Allow(context.Background(), id, ratelimit.TierDefault, ratelimit.ClassWrite)
	if d.OK {
		t.Fatal("aggregate Total must deny once exhausted even though the class bucket has tokens")
	}
	if d.RetryAfter < 1 {
		t.Fatalf("aggregate denial must carry Retry-After, got %d", d.RetryAfter)
	}
}

// The per-class bucket binds independently of the aggregate: draining the write class
// denies further writes while the aggregate still has room (a read still admits). The
// dual of TestAggregateCeiling — together they prove BOTH buckets gate.
func TestPerClassWriteQuotaBinds(t *testing.T) {
	clk := newClock()
	cfg := ratelimit.Config{
		Now: clk.now, DefaultTier: "t",
		Tiers: map[string]ratelimit.TierLimits{
			"t": {
				PerClass: map[ratelimit.EndpointClass]ratelimit.Limit{
					ratelimit.ClassRead:  {Rate: 1000, Burst: 100},
					ratelimit.ClassWrite: {Rate: 1000, Burst: 3}, // tight write class
				},
				Total: ratelimit.Limit{Rate: 1000, Burst: 50}, // generous aggregate
			},
		},
	}
	l := newLimiter(t, cfg)
	id := "tn:" + tenant.String()
	if got := drain(l, id, "t", ratelimit.ClassWrite, 3); got != 3 {
		t.Fatalf("write burst: admitted %d, want 3", got)
	}
	if l.Allow(context.Background(), id, "t", ratelimit.ClassWrite).OK {
		t.Fatal("4th write must be denied by the per-class write bucket (aggregate still has room)")
	}
	// Proof the WRITE CLASS bound, not the aggregate: a read still admits.
	if !l.Allow(context.Background(), id, "t", ratelimit.ClassRead).OK {
		t.Fatal("a read must still admit — the aggregate was not the binding constraint")
	}
}

// A DENIED request decrements NO bucket: writes denied by an empty write-class bucket
// must not drain the still-full aggregate (else a refill would leave the tenant
// spuriously throttled on the aggregate it never actually spent).
func TestDeniedRequestConsumesNoBucket(t *testing.T) {
	clk := newClock()
	cfg := ratelimit.Config{
		Now: clk.now, DefaultTier: "t",
		Tiers: map[string]ratelimit.TierLimits{
			"t": {
				PerClass: map[ratelimit.EndpointClass]ratelimit.Limit{
					ratelimit.ClassRead:  {Rate: 1000, Burst: 100},
					ratelimit.ClassWrite: {Rate: 1000, Burst: 5},
				},
				Total: ratelimit.Limit{Rate: 1000, Burst: 100},
			},
		},
	}
	l := newLimiter(t, cfg)
	id := "tn:" + tenant.String()
	drain(l, id, "t", ratelimit.ClassWrite, 5) // write class -> 0, aggregate -> 95
	for i := 0; i < 20; i++ {
		if l.Allow(context.Background(), id, "t", ratelimit.ClassWrite).OK {
			t.Fatalf("write %d should be denied (class empty)", i)
		}
	}
	// If the 20 denied writes had drained the aggregate, fewer reads would admit. The
	// aggregate should still hold 95 (only the 5 genuinely-admitted writes spent it).
	if got := drain(l, id, "t", ratelimit.ClassRead, 100); got != 95 {
		t.Fatalf("denied writes must not consume the aggregate: reads admitted %d, want 95", got)
	}
}

func TestReportOnlyAllowsButFlagsAndCounts(t *testing.T) {
	clk := newClock()
	reg := metrics.New("test", clk.now())
	l := ratelimit.New(ratelimit.Config{Now: clk.now, Mode: ratelimit.ModeReportOnly}, reg)
	id := "tn:" + tenant.String()

	// Exhaust the read burst; in report-only every call still admits.
	for i := 0; i < 130; i++ {
		if !l.Allow(context.Background(), id, ratelimit.TierDefault, ratelimit.ClassRead).OK {
			t.Fatalf("report-only must always admit (i=%d)", i)
		}
	}
	d := l.Allow(context.Background(), id, ratelimit.TierDefault, ratelimit.ClassRead)
	if !d.OK || !d.Limited {
		t.Fatalf("report-only over-limit must be OK=true, Limited=true; got OK=%v Limited=%v", d.OK, d.Limited)
	}
	// Assert the COUNTER VALUE, not the label's presence: the series is pre-created at 0,
	// so a substring check would pass even if would-be denials were never counted.
	if v := seriesValue(t, reg, `olivares_http_ratelimit_decisions_total{class="read",decision="report_only"}`); v < 1 {
		t.Fatalf("report-only over-limit reads must be counted; report_only=%v, want >=1", v)
	}
}

func TestModeOffNeverMeters(t *testing.T) {
	clk := newClock()
	l := newLimiter(t, ratelimit.Config{Now: clk.now, Mode: ratelimit.ModeOff})
	id := "tn:" + tenant.String()
	for i := 0; i < 1000; i++ {
		if !l.Allow(context.Background(), id, ratelimit.TierDefault, ratelimit.ClassWrite).OK {
			t.Fatalf("ModeOff must admit everything (i=%d)", i)
		}
	}
	if l.ActiveBuckets() != 0 {
		t.Fatalf("ModeOff must not allocate buckets, got %d", l.ActiveBuckets())
	}
}

// Idle buckets are evicted (memory is bounded). Single shard so two identities
// share one map and one idle one can be observed to drop out of the live count.
func TestSweepEvictsIdleBuckets(t *testing.T) {
	clk := newClock()
	l := newLimiter(t, ratelimit.Config{Now: clk.now, Shards: 1})
	a := "tn:" + tenant.String()
	b := "tn:22222222-2222-2222-2222-222222222222"

	l.Allow(context.Background(), a, ratelimit.TierDefault, ratelimit.ClassRead) // A: 2 buckets (class+total)
	l.Allow(context.Background(), b, ratelimit.TierDefault, ratelimit.ClassRead) // B: 2 buckets
	if l.ActiveBuckets() != 4 {
		t.Fatalf("expected 4 live buckets, got %d", l.ActiveBuckets())
	}
	// Both go idle well past the TTL, then a take on A triggers the shard sweep: B is
	// idle and reaped; A is re-created by the same take.
	clk.advance(48 * time.Hour)
	l.Allow(context.Background(), a, ratelimit.TierDefault, ratelimit.ClassRead)
	if got := l.ActiveBuckets(); got != 2 {
		t.Fatalf("idle bucket B must be evicted (A re-created); live = %d, want 2", got)
	}
}

// A bucket under continuous DENIED load must NOT be reset mid-attack: because last
// advances on every take (allowed or denied), the sweep never sees it as idle, so
// it cannot be evicted and handed back as a fresh full burst.
func TestActivelyDeniedBucketIsNotReset(t *testing.T) {
	clk := newClock()
	l := newLimiter(t, ratelimit.Config{Now: clk.now})
	id := "tn:" + tenant.String()

	// Read class: burst 100, refill 50/s. Each 1s step refills 50; firing 60 keeps the
	// bucket pinned at ~0 (net -10/step) so it is in sustained denial. 70 steps (70s)
	// comfortably exceeds the (>=60s) idle TTL — if last were frozen on denials, the
	// sweep would have evicted and reset this bucket somewhere in the span.
	drain(l, id, ratelimit.TierDefault, ratelimit.ClassRead, 200) // exhaust
	for step := 0; step < 70; step++ {
		clk.advance(time.Second)
		drain(l, id, ratelimit.TierDefault, ratelimit.ClassRead, 60)
	}
	// One more second of refill: the bucket should yield ~one step's worth (~50), never
	// a full fresh burst (~100) — proof it was never swept and reset.
	clk.advance(time.Second)
	if got := drain(l, id, ratelimit.TierDefault, ratelimit.ClassRead, 100); got > 65 {
		t.Fatalf("actively-denied bucket appears to have been reset to full; admitted %d (want ~50)", got)
	}
}

func TestTierForResolverAndFallback(t *testing.T) {
	clk := newClock()
	other := model.TenantID("33333333-3333-3333-3333-333333333333")
	cfg := ratelimit.Config{
		Now:      clk.now,
		Resolver: ratelimit.StaticTierResolver{tenant: ratelimit.TierEnterprise, other: "nonexistent-tier"},
	}
	l := newLimiter(t, cfg)
	if got := l.TierFor(tenant); got != ratelimit.TierEnterprise {
		t.Fatalf("mapped tenant tier = %q, want enterprise", got)
	}
	// A tenant mapped to an undefined tier falls back to the default tier (not the floor).
	if got := l.TierFor(other); got != ratelimit.TierDefault {
		t.Fatalf("tenant mapped to unknown tier = %q, want default fallback", got)
	}
	// An unmapped tenant gets the default tier.
	if got := l.TierFor(model.TenantID("44444444-4444-4444-4444-444444444444")); got != ratelimit.TierDefault {
		t.Fatalf("unmapped tenant tier = %q, want default", got)
	}
}

func TestMetricsSeriesPreCreated(t *testing.T) {
	clk := newClock()
	reg := metrics.New("test", clk.now())
	_ = ratelimit.New(ratelimit.Config{Now: clk.now}, reg)
	var buf bytes.Buffer
	reg.WritePrometheus(&buf)
	out := buf.String()
	// Every class×decision series exists at zero before any request (real SLO baseline).
	for _, want := range []string{
		`olivares_http_ratelimit_decisions_total{class="read",decision="allowed"} 0`,
		`olivares_http_ratelimit_decisions_total{class="write",decision="limited"} 0`,
		`olivares_http_ratelimit_decisions_total{class="read",decision="report_only"} 0`,
		"olivares_http_ratelimit_active_buckets 0",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("metrics missing %q\n%s", want, out)
		}
	}
}

// Concurrency: many goroutines hammer one tenant; -race must stay clean and the
// aggregate can never admit beyond its burst within a frozen instant.
func TestConcurrentTakesRaceClean(t *testing.T) {
	clk := newClock()
	l := newLimiter(t, ratelimit.Config{Now: clk.now})
	id := "tn:" + tenant.String()
	var wg sync.WaitGroup
	var admitted int64
	var mu sync.Mutex
	for g := 0; g < 16; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			local := 0
			for i := 0; i < 100; i++ {
				if l.Allow(context.Background(), id, ratelimit.TierDefault, ratelimit.ClassRead).OK {
					local++
				}
			}
			mu.Lock()
			admitted += int64(local)
			mu.Unlock()
		}()
	}
	wg.Wait()
	// Clock frozen: no refill, so EXACTLY the read burst (100) admits — the read class
	// binds before the aggregate (120). Equality catches both over-admission (a broken
	// lock) and under-admission (a regression that wrongly denies).
	if admitted != 100 {
		t.Fatalf("admitted %d within a frozen instant, want exactly 100 (read burst)", admitted)
	}
}
