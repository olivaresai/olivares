// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package finops

import (
	"context"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/engine/enginetest"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// fakeClock is a deterministic, mutable clock so the expiry test can advance time
// without sleeping. Safe for the module's single-threaded reserve calls in that test.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) Now() model.Timestamp {
	c.mu.Lock()
	defer c.mu.Unlock()
	return model.NewTimestamp(c.t)
}
func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// oneUSD is 1 USD expressed in micro-USD, the module's money unit.
const oneUSD = int64(1_000_000)

// -----------------------------------------------------------------------------
// The RACE. TestBudgetTOCTOU_NaiveCheckOverAdmits is the RED characterization: it
// proves the pre-fix behavior (a read-only CheckBudget with no reservation) lets
// EVERY concurrent request through, so N requests under the limit collectively
// exceed it. TestBudgetReserveLedger_ConcurrentExactlyMminus1 is the GREEN fix:
// the atomic reserve ledger admits EXACTLY M-1.
// -----------------------------------------------------------------------------

// TestBudgetTOCTOU_NaiveCheckOverAdmits documents the bug fixes. With a
// budget that affords only M-1 requests, M concurrent pre-flight CheckBudget calls
// ALL pass, because the check reads a spend read-model that no in-flight request
// has written yet — the classic check→act window. This is deterministic (no spend
// is recorded during the check phase), so it is a stable RED baseline, not a flake.
func TestBudgetTOCTOU_NaiveCheckOverAdmits(t *testing.T) {
	m, st, tenant, _ := newFin(t)
	const M = 8
	limit := int64(M-1) * oneUSD
	createBudget(t, st, tenant, "global-block", budgetSpec{
		Dimension: "global", Period: "monthly", LimitMicroUSD: limit, Action: "block",
	})

	var wg sync.WaitGroup
	start := make(chan struct{})
	var allowed int64
	wg.Add(M)
	for i := 0; i < M; i++ {
		go func() {
			defer wg.Done()
			<-start
			chk, err := m.CheckBudget(context.Background(), tenant, SpendDims{})
			if err == nil && chk.Allowed {
				atomic.AddInt64(&allowed, 1)
			}
		}()
	}
	close(start)
	wg.Wait()

	// The naive check admits ALL M — more than the M-1 the budget can pay for. If
	// each admitted request then spends 1 USD, the budget of (M-1) USD is blown by
	// one whole request. THIS is the TOCTOU the reserve ledger closes.
	if allowed != M {
		t.Fatalf("naive CheckBudget admitted %d of %d; expected all M to pass, documenting the TOCTOU (budget only affords M-1=%d)", allowed, M, M-1)
	}
}

// TestBudgetReserveLedger_ConcurrentExactlyMminus1 is the fix: M goroutines race
// to ReserveBudget against a budget that affords M-1. The atomic reserve ledger
// must admit EXACTLY M-1 and deny exactly one — no over-admit, no under-admit —
// under -race. A barrier releases all goroutines at once to maximize contention on
// the per-(policy, period) seq.
func TestBudgetReserveLedger_ConcurrentExactlyMminus1(t *testing.T) {
	m, st, tenant, _ := newFin(t)
	runConcurrentBudgetReserve(t, m, st, tenant, 24)
}

// TestBudgetReserveLedger_CrossBackend runs the exact-M-1 proof against SQLite
// (always) and Postgres (on its own isolated database, when one is configured). Postgres is
// where the serialization is load-bearing: with MaxConns>1 the reservers run on
// separate connections under READ COMMITTED, so only the monotonic-seq UNIQUE
// index + OCC retry keeps the admit count at M-1. SQLite's single writer serializes
// on its own, so it is the belt-and-suspenders backstop.
func TestBudgetReserveLedger_CrossBackend(t *testing.T) {
	// each config is built INSIDE its subtest so the Postgres leg provisions
	// (and drops) its own database rather than sharing the workspace-wide one.
	configs := map[string]func(t *testing.T) store.Config{
		"sqlite": func(*testing.T) store.Config {
			return store.Config{Engine: store.EngineSQLite, DSN: ":memory:", Debug: true}
		},
	}
	if enginetest.PostgresAvailable(t) {
		configs["postgres"] = func(t *testing.T) store.Config {
			return store.Config{Engine: store.EnginePostgres, DSN: enginetest.IsolatedPostgres(t).App, MaxConns: 8}
		}
	} else {
		t.Logf("%s unset: skipping the Postgres cross-backend leg (the leg that exercises real READ COMMITTED concurrency)", enginetest.EnvSuperuserDSN)
	}
	for name, cfg := range configs {
		cfg := cfg
		t.Run(name, func(t *testing.T) {
			m, st, tenant, _ := openFinCfg(t, cfg(t))
			runConcurrentBudgetReserve(t, m, st, tenant, 32)
		})
	}
}

// runConcurrentBudgetReserve fires M concurrent ReserveBudget calls against a
// global block budget that affords M-1, and asserts EXACTLY M-1 are admitted.
func runConcurrentBudgetReserve(t *testing.T, m *Module, st store.Store, tenant model.TenantID, M int) {
	t.Helper()
	limit := int64(M-1) * oneUSD
	createBudget(t, st, tenant, "global-block", budgetSpec{
		Dimension: "global", Period: "monthly", LimitMicroUSD: limit, Action: "block",
	})

	var wg sync.WaitGroup
	start := make(chan struct{})
	var allowed, denied int64
	wg.Add(M)
	for i := 0; i < M; i++ {
		go func() {
			defer wg.Done()
			<-start
			res, err := m.ReserveBudget(context.Background(), tenant, SpendDims{}, oneUSD)
			if err != nil {
				t.Errorf("ReserveBudget: %v", err)
				return
			}
			if res.Allowed {
				atomic.AddInt64(&allowed, 1)
			} else {
				atomic.AddInt64(&denied, 1)
				if res.Action != "block" {
					t.Errorf("denied reservation action = %q, want block", res.Action)
				}
			}
		}()
	}
	close(start)
	wg.Wait()

	if allowed != int64(M-1) {
		t.Fatalf("reserve ledger admitted %d, want EXACTLY M-1=%d (over-admit => race not closed)", allowed, M-1)
	}
	if denied != int64(M)-int64(M-1) {
		t.Fatalf("reserve ledger denied %d, want exactly 1", denied)
	}
}

// TestSpendLimitReserveLedger_ConcurrentExactlyMminus1 repeats the proof for the
// per-seat apps-gateway spend limit (CheckSpendLimit's identical TOCTOU).
func TestSpendLimitReserveLedger_ConcurrentExactlyMminus1(t *testing.T) {
	m, st, tenant, _ := newFin(t)
	const M = 16
	const actor = "user:alice"
	capCents := strconv.FormatInt(int64(M-1)*100, 10) // (M-1) USD, in integer cents
	if _, _, err := m.SpendLimitUpsert(context.Background(), tenant, SpendLimitSpec{
		Scope:  SpendLimitScope{Type: "user", UserID: actor},
		Amount: &capCents,
		Period: "daily",
	}, "admin"); err != nil {
		t.Fatalf("SpendLimitUpsert: %v", err)
	}
	_ = st

	var wg sync.WaitGroup
	start := make(chan struct{})
	var allowed int64
	wg.Add(M)
	for i := 0; i < M; i++ {
		go func() {
			defer wg.Done()
			<-start
			res, err := m.ReserveSpendLimit(context.Background(), tenant, actor, nil, oneUSD)
			if err != nil {
				t.Errorf("ReserveSpendLimit: %v", err)
				return
			}
			if res.Allowed {
				atomic.AddInt64(&allowed, 1)
			}
		}()
	}
	close(start)
	wg.Wait()

	if allowed != int64(M-1) {
		t.Fatalf("per-seat reserve admitted %d, want EXACTLY M-1=%d", allowed, M-1)
	}
}

// TestReservationLifecycle exercises reserve → deny-when-full → release-frees →
// commit, and asserts CheckBudget/budgetStatus reflect the live reservation.
func TestReservationLifecycle(t *testing.T) {
	m, st, tenant, _ := newFin(t)
	ctx := context.Background()
	limit := int64(10) * oneUSD
	bid := createBudget(t, st, tenant, "global-block", budgetSpec{
		Dimension: "global", Period: "monthly", LimitMicroUSD: limit, Action: "block",
	})

	// Reserve 3 USD; CheckBudget must now count it (effective 3 < 10 => still allowed).
	r1, err := m.ReserveBudget(ctx, tenant, SpendDims{}, 3*oneUSD)
	if err != nil || !r1.Allowed {
		t.Fatalf("reserve 3 USD: allowed=%v err=%v", r1.Allowed, err)
	}
	chk, err := m.CheckBudget(ctx, tenant, SpendDims{})
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if !chk.Allowed {
		t.Fatalf("check after reserving 3 of 10 should still allow")
	}

	// A reservation for 8 more (3+8=11 > 10) must be denied — the ceiling now counts
	// the in-flight 3 USD reservation.
	r2, err := m.ReserveBudget(ctx, tenant, SpendDims{}, 8*oneUSD)
	if err != nil {
		t.Fatalf("reserve 8 USD: %v", err)
	}
	if r2.Allowed {
		t.Fatalf("reserving 8 USD on top of a 3 USD reservation (limit 10) must be denied")
	}

	// Release the 3 USD reservation; the 8 USD reservation now fits.
	if err := m.ReleaseReservation(ctx, tenant, r1.Handle); err != nil {
		t.Fatalf("release: %v", err)
	}
	r3, err := m.ReserveBudget(ctx, tenant, SpendDims{}, 8*oneUSD)
	if err != nil || !r3.Allowed {
		t.Fatalf("reserve 8 USD after release: allowed=%v err=%v", r3.Allowed, err)
	}

	// Commit r3 (actuation done). Committed reservations leave the active sum, so the
	// headroom frees again (the ACTUAL spend is a separate cost-sample, not recorded
	// here). Releasing/committing is idempotent.
	if err := m.CommitReservation(ctx, tenant, r3.Handle, 8*oneUSD); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if err := m.CommitReservation(ctx, tenant, r3.Handle, 8*oneUSD); err != nil {
		t.Fatalf("commit idempotent: %v", err)
	}
	r4, err := m.ReserveBudget(ctx, tenant, SpendDims{}, 8*oneUSD)
	if err != nil || !r4.Allowed {
		t.Fatalf("reserve 8 USD after commit: allowed=%v err=%v", r4.Allowed, err)
	}

	// budgetStatus reflects the live 8 USD reservation from r4 in its consumption.
	var over bool
	var consumed int
	if err := st.View(ctx, tenant, func(sc store.Scope) error {
		p, e := sc.Policies().Get(ctx, bid)
		if e != nil {
			return e
		}
		dto, e := budgetStatus(ctx, sc, p, m.clock.Now().Time())
		over, consumed = dto.Over, dto.ConsumedPct
		return e
	}); err != nil {
		t.Fatalf("status view: %v", err)
	}
	if consumed < 80 {
		t.Fatalf("budgetStatus consumed=%d%%, want >=80%% (8 USD reserved of 10)", consumed)
	}
	_ = over
}

// TestReservationExpiryNoDoubleCount proves an EXPIRED reservation stops holding
// headroom the instant it lapses (the sum excludes expires_at <= now), so it is
// never double-counted, and SweepExpiredReservations only records the terminal
// state.
func TestReservationExpiryNoDoubleCount(t *testing.T) {
	m, st, tenant, _ := newFin(t)
	ctx := context.Background()
	fc := &fakeClock{t: time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC)}
	m.clock = fc

	// Short TTL so the reservation expires when we advance the clock.
	defer func(prev time.Duration) { reservationTTL = prev }(reservationTTL)
	reservationTTL = 30 * time.Second

	limit := int64(10) * oneUSD
	createBudget(t, st, tenant, "global-block", budgetSpec{
		Dimension: "global", Period: "monthly", LimitMicroUSD: limit, Action: "block",
	})

	// Reserve 9 USD, then a 9 USD reservation must be denied (9+9 > 10).
	r1, err := m.ReserveBudget(ctx, tenant, SpendDims{}, 9*oneUSD)
	if err != nil || !r1.Allowed {
		t.Fatalf("reserve 9 USD: allowed=%v err=%v", r1.Allowed, err)
	}
	if r2, _ := m.ReserveBudget(ctx, tenant, SpendDims{}, 9*oneUSD); r2.Allowed {
		t.Fatalf("second 9 USD reservation must be denied while the first is active")
	}

	// Advance past the TTL: the first reservation lapses and no longer holds headroom,
	// WITHOUT any decrement — so a fresh 9 USD reservation now fits.
	fc.advance(time.Minute)
	r3, err := m.ReserveBudget(ctx, tenant, SpendDims{}, 9*oneUSD)
	if err != nil || !r3.Allowed {
		t.Fatalf("reserve 9 USD after expiry: allowed=%v err=%v", r3.Allowed, err)
	}

	// The sweep records the terminal state; it must NOT change accounting (r3 is still
	// the only live reservation, so a further 9 USD stays denied — no double free).
	swept, err := m.SweepExpiredReservations(ctx, tenant)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if swept != 1 {
		t.Fatalf("sweep marked %d expired, want 1", swept)
	}
	if r4, _ := m.ReserveBudget(ctx, tenant, SpendDims{}, 9*oneUSD); r4.Allowed {
		t.Fatalf("after sweep, a 9 USD reservation must still be denied (r3 holds the headroom)")
	}
}

// BenchmarkReserveBudget measures the reserve→release hot path (the seam runs the
// reserve on every governed request). It must not materially degrade the check.
func BenchmarkReserveBudget(b *testing.B) {
	m, st, tenant, _ := newFin(b)
	createBudget(b, st, tenant, "global-block", budgetSpec{
		Dimension: "global", Period: "monthly", LimitMicroUSD: 1 << 62, Action: "block",
	})
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		res, err := m.ReserveBudget(ctx, tenant, SpendDims{}, 1000)
		if err != nil || !res.Allowed {
			b.Fatalf("reserve: allowed=%v err=%v", res.Allowed, err)
		}
		if err := m.ReleaseReservation(ctx, tenant, res.Handle); err != nil {
			b.Fatalf("release: %v", err)
		}
	}
}

// BenchmarkCheckBudget measures the read-only check for comparison (it now folds
// the reservation sum into effective).
func BenchmarkCheckBudget(b *testing.B) {
	m, st, tenant, _ := newFin(b)
	createBudget(b, st, tenant, "global-block", budgetSpec{
		Dimension: "global", Period: "monthly", LimitMicroUSD: 1 << 62, Action: "block",
	})
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := m.CheckBudget(ctx, tenant, SpendDims{}); err != nil {
			b.Fatalf("check: %v", err)
		}
	}
}
