// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package inventory

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

// dfPostgresOpen opens one module over an already-provisioned PostgreSQL
// fixture with a pinned clock, on top of the SAME scoped helpers the accepted C1
// PostgreSQL suite uses (c1PostgresFixture / c1PostgresNamedDSN /
// c1PostgresWaitForBlockedWriter). It differs from c1PostgresOpen only where it
// has to: the clock is chosen per caller, because last_seen is stamped from it
// (provenance.go:127), and that stamp is the subject of a freshness test.
func dfPostgresOpen(t *testing.T, cfg store.Config, at time.Time) (*Module, store.Store) {
	t.Helper()
	m := New(WithClock(pinnedClock{at: at}), WithSweepBudget(45*time.Second))
	st, err := engine.Open(context.Background(), cfg, m.RegisterSchema)
	if err != nil {
		t.Fatalf("open PostgreSQL inventory store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	m.UseData(api.NewModuleData(st))
	return m, st
}

// dfSessionEntry returns the catalog row of the fixture's session entity.
func dfSessionEntry(t *testing.T, st store.Store, tenant model.TenantID) model.Record {
	t.Helper()
	for _, row := range c1Rows(t, st, tenant, catalogEntryKind) {
		if row.String(colEntityKind) == kindSession {
			return row
		}
	}
	t.Fatal("no session catalog entry in the fixture")
	return nil
}

// sweepMutateHold keeps the sweep's transaction OPEN after its page is written,
// so a second, independent PostgreSQL connection can be observed meeting it.
type sweepMutateHold struct {
	api.ModuleData
	once    sync.Once
	entered chan struct{}
	release chan struct{}
}

func (h *sweepMutateHold) Mutate(ctx context.Context, tenant model.TenantID, fn func(store.Scope) error) error {
	return h.ModuleData.Mutate(ctx, tenant, func(sc store.Scope) error {
		if err := fn(sc); err != nil {
			return err
		}
		h.once.Do(func() { close(h.entered) })
		select {
		case <-h.release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})
}

// TestDurableFreshnessPostgresReappearanceMeetsTheSweepAtTheTenantGate is causal
// group 5 on a real PostgreSQL 16 server.
//
// The claim is narrow and it is the one the design makes: the sweep and an
// ingestion refresh OF THE SAME TENANT are already serialized BEFORE either
// callback reads anything, by the lineage writer's per-tenant advisory lock
// (core/internal/store/sqlstore/lineage_writer.go:52-63), so no extra row lock
// is needed and no barrier waiting for two simultaneous callbacks could ever be
// satisfied. The waiting is therefore observed FROM THE SERVER — the same shape
// as the accepted C1 antecedent — and not inferred from a barrier in Go.
//
// What this does NOT claim: it is not a unique-index race, not two concurrent
// callbacks, not an automatic retry and not any high-availability guarantee.
func TestDurableFreshnessPostgresReappearanceMeetsTheSweepAtTheTenantGate(t *testing.T) {
	cfg, pg := c1PostgresFixture(t)
	const sweeperName = "inventory-df-sweeper"
	const writerName = "inventory-df-writer"
	sweepCfg, writeCfg := cfg, cfg
	sweepCfg.DSN = c1PostgresNamedDSN(t, cfg.DSN, sweeperName)
	writeCfg.DSN = c1PostgresNamedDSN(t, cfg.DSN, writerName)

	old := baseTime
	fresh := baseTime.Add(4 * time.Hour)
	sweepAt := baseTime.Add(2 * time.Hour)

	sweeper, sweepStore := dfPostgresOpen(t, sweepCfg, sweepAt)
	tenant := c1Tenant(t, sweepStore, "df-pg-reappearance")

	// The entity is created by real ingestion at the OLD instant, on the writer's
	// own connection, so last_seen is a genuine observation stamp.
	seeder, writeStore := dfPostgresOpen(t, writeCfg, old)
	edge := mkEdge("session", "sess-reappear", "file", "/data/x",
		sdkmodel.ModeRead, sdkmodel.SignalOTEL, "Read", old)
	if err := seeder.onEdge(context.Background(), tenant.String(), edge); err != nil {
		t.Fatalf("seed the observed entity: %v", err)
	}
	if got := dfSessionEntry(t, writeStore, tenant).String(colStatus); got != statusActive {
		t.Fatalf("seeded entry status = %q, want active", got)
	}

	// The refreshing writer is built BEFORE the barrier, over the store that is
	// ALREADY open on the writer's named connection. Measured on this fixture:
	// calling engine.Open while the sweep held its transaction blocked for ~41 s
	// (attempt-05-pg-diagnostic), because opening a store runs the schema
	// reconcile and the append-only guard rollout, and those meet the open
	// transaction. The antecedent C1 barrier opens both stores up front for the
	// same reason; a barrier that also measures a store Open is measuring boot.
	refresher := New(WithClock(pinnedClock{at: fresh}))
	refresher.UseData(api.NewModuleData(writeStore))

	hold := &sweepMutateHold{
		ModuleData: api.NewModuleData(sweepStore),
		entered:    make(chan struct{}), release: make(chan struct{}),
	}
	sweeper.UseData(hold)
	sweeper.UseSweepScopeSource(scopeOf(tenant))

	observer, err := sql.Open("pgx", pg.Superuser)
	if err != nil {
		t.Fatalf("open PostgreSQL lock observer: %v", err)
	}
	t.Cleanup(func() { _ = observer.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	sweepDone := make(chan error, 1)
	sweepMarked := make(chan int, 1)
	go func() {
		n, err := sweeper.Sweep(ctx, sweepAt)
		sweepMarked <- n
		sweepDone <- err
	}()
	select {
	case <-hold.entered:
	case err := <-sweepDone:
		t.Fatalf("the sweep finished before holding its transaction open: %v", err)
	case <-ctx.Done():
		t.Fatalf("the sweep never reached its transaction: %v", ctx.Err())
	}

	// A genuinely independent connection now tries to refresh the same entity.
	refreshDone := make(chan error, 1)
	go func() {
		refreshDone <- refresher.onEdge(ctx, tenant.String(), edge)
	}()

	waiterPID, holderPID := c1PostgresWaitForBlockedWriter(t, ctx, observer, pg.Database, writerName, sweeperName)
	t.Logf("PostgreSQL reports the refreshing writer pid=%d blocked on the sweep's advisory lock held by pid=%d",
		waiterPID, holderPID)
	select {
	case err := <-refreshDone:
		t.Fatalf("the refresh completed while the sweep still held the tenant gate: %v", err)
	default:
	}

	close(hold.release)
	if err := <-sweepDone; err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if n := <-sweepMarked; n == 0 {
		t.Fatal("the sweep marked nothing, so there was no classification for the refresh to undo")
	}
	if err := <-refreshDone; err != nil {
		t.Fatalf("the released refresh failed: %v", err)
	}

	entry := dfSessionEntry(t, writeStore, tenant)
	if got := entry.String(colStatus); got != statusActive {
		t.Fatalf("after the reappearance the entry is %q, want active: being seen again is what reactivates it", got)
	}
	if got, want := entry.String(colLastSeen), model.NewTimestamp(fresh).String(); got != want {
		t.Fatalf("last_seen = %q, want the fresh observation %q", got, want)
	}
}

// TestDurableFreshnessPostgresConfirmedRefreshBeforeTheSweepStaysFresh is the
// inverse ordering, and it needs no barrier at all: a refresh that COMMITTED
// before the sweep started simply leaves the row outside the sweep's cutoff.
func TestDurableFreshnessPostgresConfirmedRefreshBeforeTheSweepStaysFresh(t *testing.T) {
	cfg := c1PostgresConfig(t)
	old := baseTime
	fresh := baseTime.Add(4 * time.Hour)
	sweepAt := baseTime.Add(2 * time.Hour)

	seeder, st := dfPostgresOpen(t, cfg, old)
	tenant := c1Tenant(t, st, "df-pg-fresh-first")
	edge := mkEdge("session", "sess-fresh-first", "file", "/data/y",
		sdkmodel.ModeRead, sdkmodel.SignalOTEL, "Read", old)
	if err := seeder.onEdge(context.Background(), tenant.String(), edge); err != nil {
		t.Fatalf("seed: %v", err)
	}
	refresher, _ := dfPostgresOpen(t, cfg, fresh)
	if err := refresher.onEdge(context.Background(), tenant.String(), edge); err != nil {
		t.Fatalf("refresh: %v", err)
	}

	sweeper, _ := dfPostgresOpen(t, cfg, sweepAt)
	sweeper.UseSweepScopeSource(scopeOf(tenant))
	n, err := sweeper.Sweep(context.Background(), sweepAt)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	entry := dfSessionEntry(t, st, tenant)
	if got := entry.String(colStatus); got != statusActive {
		t.Fatalf("an entry refreshed before the sweep is %q, want active (marked %d)", got, n)
	}
	if got, want := entry.String(colLastSeen), model.NewTimestamp(fresh).String(); got != want {
		t.Fatalf("last_seen = %q, want the confirmed refresh %q", got, want)
	}
}

// TestDurableFreshnessPostgresContinuesAcrossReopen is causal group 1 on the
// real engine: more than one page, a restart with no event replayed, and an
// exact union at the end.
func TestDurableFreshnessPostgresContinuesAcrossReopen(t *testing.T) {
	cfg := c1PostgresConfig(t)
	const old = listCap + 50
	const fresh = 15
	at := baseTime.Add(2 * time.Hour)

	first, st := dfPostgresOpen(t, cfg, at)
	tenant := c1Tenant(t, st, "df-pg-reopen")
	oldIDs := seedCatalog(t, st, tenant, kindSession, old, baseTime)
	freshIDs := seedCatalog(t, st, tenant, kindAgent, fresh, at)
	first.UseSweepScopeSource(scopeOf(tenant))

	n, err := first.Sweep(context.Background(), at)
	if err != nil {
		t.Fatalf("first pass: %v", err)
	}
	if n != listCap {
		t.Fatalf("first pass marked %d, want exactly one page of %d", n, listCap)
	}
	state, ok := sweepState(t, st, tenant)
	if !ok || state.String(colCatalogCursor) == "" {
		t.Fatal("the unfinished cycle persisted no cursor on PostgreSQL")
	}
	if got := state.String(colLastCompletedCutoffAt); got != "" {
		t.Fatalf("an unfinished cycle advanced the completed cutoff to %q", got)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	resumed, st2 := dfPostgresOpen(t, cfg, at)
	resumed.UseSweepScopeSource(scopeOf(tenant))
	total := n
	for passes := 0; ; passes++ {
		got, err := resumed.Sweep(context.Background(), at)
		if err != nil {
			t.Fatalf("resumed pass %d: %v", passes, err)
		}
		total += got
		state, ok := sweepState(t, st2, tenant)
		if !ok {
			t.Fatal("the durable state row vanished across the PostgreSQL reopen")
		}
		if state.String(colCycleCutoffAt) == "" {
			break
		}
		if passes > 6 {
			t.Fatal("the resumed cycle never finished")
		}
	}
	if total != old {
		t.Fatalf("swept %d across the restart, want the exact %d stale ones", total, old)
	}
	statuses := catalogStatuses(t, st2, tenant)
	if got := idsWithStatus(statuses, statusStale); !equalStrings(got, oldIDs) {
		t.Fatalf("stale set = %d ids, want the exact %d (first divergence: %s)",
			len(got), len(oldIDs), firstDivergence(got, oldIDs))
	}
	if got := idsWithStatus(statuses, statusActive); !equalStrings(got, freshIDs) {
		t.Fatalf("active set = %d ids, want the exact %d fresh controls", len(got), len(freshIDs))
	}
	final, _ := sweepState(t, st2, tenant)
	if got := final.String(colLastCompletedCutoffAt); got != cutoffFor(at) {
		t.Fatalf("completed cutoff = %q, want %q only at the end", got, cutoffFor(at))
	}
}

// TestDurableFreshnessPostgresRollsBackThePartialPage is causal group 3 on the
// real engine: the atomicity of page and cursor is PostgreSQL's transaction,
// not a property of the embedded engine.
func TestDurableFreshnessPostgresRollsBackThePartialPage(t *testing.T) {
	cfg := c1PostgresConfig(t)
	at := baseTime.Add(2 * time.Hour)
	m, st := dfPostgresOpen(t, cfg, at)
	tenant := c1Tenant(t, st, "df-pg-rollback")
	oldIDs := seedCatalog(t, st, tenant, kindSession, 9, baseTime)

	failing := &failingCatalogData{ModuleData: api.NewModuleData(st)}
	m.UseData(failing)
	m.UseSweepScopeSource(scopeOf(tenant))

	failing.arm(3, nil, true) // a REAL OCC conflict from PostgreSQL itself
	n, err := m.Sweep(context.Background(), at)
	if !errors.Is(err, store.ErrConflict) {
		t.Fatalf("sweep = (%d, %v), want store.ErrConflict from PostgreSQL", n, err)
	}
	if n != 0 {
		t.Fatalf("a rolled-back turn counted %d entries", n)
	}
	for id, status := range catalogStatuses(t, st, tenant) {
		if status != statusActive {
			t.Fatalf("entry %s escaped the PostgreSQL rollback as %q", id, status)
		}
	}
	if state, ok := sweepState(t, st, tenant); ok &&
		(state.String(colCatalogCursor) != "" || state.String(colCycleCutoffAt) != "") {
		t.Fatalf("a rolled-back turn persisted progress: cutoff=%q cursor=%q",
			state.String(colCycleCutoffAt), state.String(colCatalogCursor))
	}

	failing.disarm()
	again, err := m.Sweep(context.Background(), at)
	if err != nil {
		t.Fatalf("the explicit retry failed: %v", err)
	}
	if again != len(oldIDs) {
		t.Fatalf("the retry marked %d, want the whole %d-entry page exactly once", again, len(oldIDs))
	}
	if got := idsWithStatus(catalogStatuses(t, st, tenant), statusStale); !equalStrings(got, oldIDs) {
		t.Fatalf("after the retry the stale set is %d ids, want the exact %d", len(got), len(oldIDs))
	}
}
