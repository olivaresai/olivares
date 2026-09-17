// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package inventory

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/sdk/event"
)

// --- the seam's test doubles ------------------------------------------------

// fixedSweepScope is the SweepScopeSource under test control. It records the
// deadline of the context it was called with, because the enumeration's own
// budget is part of the contract: a tenant turn must NOT inherit it.
//
// It deliberately returns rows ALONGSIDE its error when one is set. That is the
// exact shape SystemScope.ListOrgs has (core/internal/store/sqlstore/system.go:527-536),
// and a sweep that consumed those rows would be enumerating from a set the store
// had just refused to certify.
type fixedSweepScope struct {
	mu          sync.Mutex
	tenants     []model.TenantID
	err         error
	calls       int
	deadline    time.Time
	hasDeadline bool
}

func (s *fixedSweepScope) ListSweepTenants(ctx context.Context) ([]model.TenantID, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	s.deadline, s.hasDeadline = ctx.Deadline()
	return append([]model.TenantID(nil), s.tenants...), s.err
}

func (s *fixedSweepScope) enumerationDeadline(t *testing.T) time.Time {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.hasDeadline {
		t.Fatal("the scope source was called with no deadline: the enumeration has no budget of its own")
	}
	return s.deadline
}

func scopeOf(tenants ...model.TenantID) *fixedSweepScope {
	return &fixedSweepScope{tenants: tenants}
}

// turnRecorder observes every Mutate the sweep opens: which tenant, in what
// order, and the deadline of the context that turn was given.
type turnRecorder struct {
	api.ModuleData
	mu        sync.Mutex
	tenants   []model.TenantID
	deadlines []time.Time
	// before runs inside the turn, ahead of the sweep's own callback. Its error
	// replaces the turn.
	before func(ctx context.Context, tenant model.TenantID) error
	// afterCommit runs once the inner Mutate has returned; what it returns is
	// what the caller sees. This is the lost-acknowledgement seam.
	afterCommit func(tenant model.TenantID, committed error) error
}

func (r *turnRecorder) Mutate(ctx context.Context, tenant model.TenantID, fn func(store.Scope) error) error {
	r.mu.Lock()
	r.tenants = append(r.tenants, tenant)
	deadline, _ := ctx.Deadline()
	r.deadlines = append(r.deadlines, deadline)
	before, afterCommit := r.before, r.afterCommit
	r.mu.Unlock()
	err := r.ModuleData.Mutate(ctx, tenant, func(sc store.Scope) error {
		if before != nil {
			if e := before(ctx, tenant); e != nil {
				return e
			}
		}
		return fn(sc)
	})
	if afterCommit != nil {
		return afterCommit(tenant, err)
	}
	return err
}

func (r *turnRecorder) visited() []model.TenantID {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]model.TenantID(nil), r.tenants...)
}

func (r *turnRecorder) turnDeadlines() []time.Time {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]time.Time(nil), r.deadlines...)
}

// failingCatalogData fails the Nth catalog Update of a turn, AFTER the earlier
// updates of that same page have really run against the store. That is the only
// shape that can prove a partial page does not escape: a failure before the
// first write would leave nothing to roll back.
type failingCatalogData struct {
	api.ModuleData
	mu       sync.Mutex
	armed    bool
	failAt   int // 1-based ordinal of the Update that fails
	err      error
	occ      bool // corrupt the OCC version for a REAL store conflict instead
	attempts int
}

type failingCatalogScope struct {
	store.Scope
	owner *failingCatalogData
}

type failingCatalogRepo struct {
	store.GenericRepo
	owner *failingCatalogData
}

func (d *failingCatalogData) Mutate(ctx context.Context, tenant model.TenantID, fn func(store.Scope) error) error {
	return d.ModuleData.Mutate(ctx, tenant, func(sc store.Scope) error {
		return fn(failingCatalogScope{Scope: sc, owner: d})
	})
}

func (s failingCatalogScope) Ext(kind model.Kind) (store.GenericRepo, error) {
	repo, err := s.Scope.Ext(kind)
	if err == nil && kind == catalogEntryKind {
		return failingCatalogRepo{GenericRepo: repo, owner: s.owner}, nil
	}
	return repo, err
}

func (r failingCatalogRepo) Update(ctx context.Context, rec model.Record) (model.Record, error) {
	r.owner.mu.Lock()
	r.owner.attempts++
	n, armed, failAt, occ, injected := r.owner.attempts, r.owner.armed, r.owner.failAt, r.owner.occ, r.owner.err
	r.owner.mu.Unlock()
	if !armed || failAt == 0 || n != failAt {
		return r.GenericRepo.Update(ctx, rec)
	}
	if occ {
		// A REAL conflict from the store: hand it a version nobody holds, so the
		// UPDATE ... WHERE version = ? matches no row. Not a synthesized error.
		stale := model.Record{}
		for k, v := range rec {
			stale[k] = v
		}
		stale[model.ColVersion] = rec.Int(model.ColVersion) + 7
		return r.GenericRepo.Update(ctx, stale)
	}
	return nil, injected
}

func (d *failingCatalogData) arm(failAt int, err error, occ bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.armed, d.failAt, d.err, d.occ, d.attempts = true, failAt, err, occ, 0
}

func (d *failingCatalogData) disarm() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.armed = false
}

// cursorlessPageData returns HasMore with an empty cursor for the catalog
// listing — the one page shape that carries no usable frontier to resume from.
type cursorlessPageData struct{ api.ModuleData }

type cursorlessPageScope struct{ store.Scope }

type cursorlessPageRepo struct{ store.GenericRepo }

func (d cursorlessPageData) Mutate(ctx context.Context, tenant model.TenantID, fn func(store.Scope) error) error {
	return d.ModuleData.Mutate(ctx, tenant, func(sc store.Scope) error {
		return fn(cursorlessPageScope{Scope: sc})
	})
}

func (s cursorlessPageScope) Ext(kind model.Kind) (store.GenericRepo, error) {
	repo, err := s.Scope.Ext(kind)
	if err == nil && kind == catalogEntryKind {
		return cursorlessPageRepo{GenericRepo: repo}, nil
	}
	return repo, err
}

func (r cursorlessPageRepo) List(ctx context.Context, q model.Query) ([]model.Record, model.Page, error) {
	rows, page, err := r.GenericRepo.List(ctx, q)
	if err != nil {
		return rows, page, err
	}
	return rows, model.Page{HasMore: true}, nil
}

// duplicatedStateData shows the sweep two state rows where the unique index
// allows one. The index makes this unreachable in the real store; the module
// must still refuse rather than pick one and call it progress.
type duplicatedStateData struct{ api.ModuleData }

type duplicatedStateScope struct{ store.Scope }

type duplicatedStateRepo struct{ store.GenericRepo }

func (d duplicatedStateData) Mutate(ctx context.Context, tenant model.TenantID, fn func(store.Scope) error) error {
	return d.ModuleData.Mutate(ctx, tenant, func(sc store.Scope) error {
		return fn(duplicatedStateScope{Scope: sc})
	})
}

func (s duplicatedStateScope) Ext(kind model.Kind) (store.GenericRepo, error) {
	repo, err := s.Scope.Ext(kind)
	if err == nil && kind == freshnessSweepKind {
		return duplicatedStateRepo{GenericRepo: repo}, nil
	}
	return repo, err
}

func (r duplicatedStateRepo) List(ctx context.Context, q model.Query) ([]model.Record, model.Page, error) {
	rows, page, err := r.GenericRepo.List(ctx, q)
	if err != nil || len(rows) != 1 {
		return rows, page, err
	}
	return []model.Record{rows[0], rows[0]}, page, nil
}

// --- fixtures ---------------------------------------------------------------

// openDurableEstate opens a real dual-engine SQLite store on a PATH (not
// :memory:), so a Close/Open pair is a genuine restart of the same estate.
func openDurableEstate(t *testing.T, path string, at time.Time, opts ...Option) (*Module, store.Store) {
	t.Helper()
	m := New(append([]Option{WithClock(pinnedClock{at: at})}, opts...)...)
	st, err := engine.Open(context.Background(), store.Config{Engine: store.EngineSQLite, DSN: path}, m.RegisterSchema)
	if err != nil {
		t.Fatalf("open durable estate %s: %v", path, err)
	}
	t.Cleanup(func() { _ = st.Close() })
	m.UseData(api.NewModuleData(st))
	return m, st
}

// seedCatalog writes n active catalog entries observed at lastSeen, directly
// through the store, and returns their ids sorted the way the keyset cursor
// walks them — so a test can assert the EXACT union a sweep covered.
func seedCatalog(t *testing.T, st store.Store, tenant model.TenantID, kind string, n int, lastSeen time.Time) []string {
	t.Helper()
	ids := make([]string, 0, n)
	if err := st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(catalogEntryKind)
		if err != nil {
			return err
		}
		stamp := model.NewTimestamp(lastSeen).String()
		for i := 0; i < n; i++ {
			rec, err := repo.Create(context.Background(), model.Record{
				colEntityKind: kind, colEntityID: model.NewID().String(),
				colName: fmt.Sprintf("%s-%04d", kind, i), colRef: "",
				colStatus: statusActive, colSignalSources: "[]", colHosts: "[]",
				colFirstSeen: stamp, colLastSeen: stamp, colOccurrence: int64(1),
			})
			if err != nil {
				return err
			}
			ids = append(ids, rec.String(model.ColID))
		}
		return nil
	}); err != nil {
		t.Fatalf("seed %d %s entries: %v", n, kind, err)
	}
	sort.Strings(ids)
	return ids
}

// catalogStatuses returns id→status for every catalog row of the tenant.
func catalogStatuses(t *testing.T, st store.Store, tenant model.TenantID) map[string]string {
	t.Helper()
	out := map[string]string{}
	if err := st.View(context.Background(), tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(catalogEntryKind)
		if err != nil {
			return err
		}
		cursor := ""
		for {
			rows, page, err := repo.List(context.Background(), model.Query{Limit: listCap, Cursor: cursor})
			if err != nil {
				return err
			}
			for _, rec := range rows {
				out[rec.String(model.ColID)] = rec.String(colStatus)
			}
			if !page.HasMore {
				return nil
			}
			cursor = page.Cursor
		}
	}); err != nil {
		t.Fatalf("read catalog statuses: %v", err)
	}
	return out
}

func idsWithStatus(statuses map[string]string, want string) []string {
	out := make([]string, 0, len(statuses))
	for id, got := range statuses {
		if got == want {
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out
}

// sweepState reads the tenant's durable progress row. It fails the test if more
// than one exists: "at most one row per tenant" is an assertion, not a hope.
func sweepState(t *testing.T, st store.Store, tenant model.TenantID) (model.Record, bool) {
	t.Helper()
	var rows []model.Record
	if err := st.View(context.Background(), tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(freshnessSweepKind)
		if err != nil {
			return err
		}
		rows, _, err = repo.List(context.Background(), model.Query{Limit: 4})
		return err
	}); err != nil {
		t.Fatalf("read durable sweep state: %v", err)
	}
	if len(rows) > 1 {
		t.Fatalf("durable sweep state has %d rows for one tenant, want at most 1", len(rows))
	}
	if len(rows) == 0 {
		return nil, false
	}
	return rows[0], true
}

func cutoffFor(at time.Time) string {
	return model.NewTimestamp(at.Add(-defaultStaleAfter)).String()
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func firstDivergence(got, want []string) string {
	for i := range want {
		if i >= len(got) {
			return fmt.Sprintf("missing %s", want[i])
		}
		if got[i] != want[i] {
			return fmt.Sprintf("index %d got %s want %s", i, got[i], want[i])
		}
	}
	if len(got) > len(want) {
		return fmt.Sprintf("extra %s", got[len(want)])
	}
	return "none"
}

// --- group 1: durability, reopen and multi-page continuation ----------------

// TestDurableFreshnessContinuesAcrossReopenWithoutNewEvents is what C2a is for:
// a catalog larger than one page is swept to completion across a restart,
// driven by the durable directory rather than by whatever the process happened
// to have observed before it was killed.
func TestDurableFreshnessContinuesAcrossReopenWithoutNewEvents(t *testing.T) {
	const old = listCap + 217
	const fresh = 40
	at := baseTime.Add(2 * time.Hour)

	path := filepath.Join(t.TempDir(), "estate.db")
	m, st := openDurableEstate(t, path, at)
	tenant := c1Tenant(t, st, "durable-reopen")
	m.UseSweepScopeSource(scopeOf(tenant))

	oldIDs := seedCatalog(t, st, tenant, kindSession, old, baseTime)
	freshIDs := seedCatalog(t, st, tenant, kindAgent, fresh, at)

	n, err := m.Sweep(context.Background(), at)
	if err != nil {
		t.Fatalf("first pass: %v", err)
	}
	if n != listCap {
		t.Fatalf("first pass marked %d, want exactly one page of %d", n, listCap)
	}
	state, ok := sweepState(t, st, tenant)
	if !ok {
		t.Fatal("no durable sweep state after the first page: nothing would survive the restart")
	}
	if state.String(colCatalogCursor) == "" {
		t.Fatal("an unfinished cycle persisted no cursor: the restart would rescan from the start")
	}
	if got := state.String(colCycleCutoffAt); got != cutoffFor(at) {
		t.Fatalf("cycle cutoff = %q, want the fixed %q", got, cutoffFor(at))
	}
	if got := state.String(colLastCompletedCutoffAt); got != "" {
		t.Fatalf("an unfinished cycle already advanced the completed cutoff to %q", got)
	}

	// The restart: close the store and open a NEW module over the SAME file. No
	// event is replayed and no observation arrives — only the durable directory.
	if err := st.Close(); err != nil {
		t.Fatalf("close estate: %v", err)
	}
	resumed, st2 := openDurableEstate(t, path, at)
	resumed.UseSweepScopeSource(scopeOf(tenant))

	total := n
	passes := 0
	for {
		got, err := resumed.Sweep(context.Background(), at)
		if err != nil {
			t.Fatalf("resumed pass %d: %v", passes, err)
		}
		total += got
		passes++
		state, ok := sweepState(t, st2, tenant)
		if !ok {
			t.Fatal("the durable state row vanished across the reopen")
		}
		if state.String(colCycleCutoffAt) == "" {
			break
		}
		if passes > 8 {
			t.Fatal("the resumed cycle never finished")
		}
	}
	if total != old {
		t.Fatalf("swept %d entries across the restart, want the exact %d stale ones", total, old)
	}

	statuses := catalogStatuses(t, st2, tenant)
	if got := idsWithStatus(statuses, statusStale); !equalStrings(got, oldIDs) {
		t.Fatalf("stale set has %d ids, want the exact %d seeded old ones (first divergence: %s)",
			len(got), len(oldIDs), firstDivergence(got, oldIDs))
	}
	if got := idsWithStatus(statuses, statusActive); !equalStrings(got, freshIDs) {
		t.Fatalf("active set has %d ids, want the exact %d fresh control ones (first divergence: %s)",
			len(got), len(freshIDs), firstDivergence(got, freshIDs))
	}

	final, _ := sweepState(t, st2, tenant)
	if got := final.String(colLastCompletedCutoffAt); got != cutoffFor(at) {
		t.Fatalf("completed cutoff = %q, want %q ONLY once the cycle finished", got, cutoffFor(at))
	}
	if final.String(colCycleCutoffAt) != "" || final.String(colCatalogCursor) != "" {
		t.Fatalf("a finished cycle left cutoff=%q cursor=%q behind",
			final.String(colCycleCutoffAt), final.String(colCatalogCursor))
	}
}

// TestDurableFreshnessOnLegacySchemaKeepsC1 opens the OLD descriptor set (the
// catalog alone: no C1 projections, no sweep state), then reopens with today's
// and sweeps. The fixture is a DESCRIPTOR from before this work, not an old
// binary, and the claim is bounded to that: the reconciler creates what is
// missing, the state row appears lazily on the first turn, and C1 replay and
// conflict still behave.
func TestDurableFreshnessOnLegacySchemaKeepsC1(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	_, legacySt := c1Open(t, path, true)
	tenant := c1Tenant(t, legacySt, "legacy-freshness")
	oldIDs := seedCatalog(t, legacySt, tenant, kindSession, 12, baseTime)
	if err := legacySt.Close(); err != nil {
		t.Fatalf("close legacy estate: %v", err)
	}

	at := baseTime.Add(2 * time.Hour)
	m, st := openDurableEstate(t, path, at)
	m.UseSweepScopeSource(scopeOf(tenant))

	// C1 still works on the upgraded database: a receipt, its replay, and a
	// conflicting redelivery. This is the compatibility control, not a rerun of C1.
	e := c1Event(tenant, "legacy-upgrade-event", "source-legacy")
	if err := m.onEvent(context.Background(), e); err != nil {
		t.Fatalf("C1 ingestion after the upgrade: %v", err)
	}
	if err := m.onEvent(context.Background(), e); err != nil {
		t.Fatalf("C1 replay after the upgrade: %v", err)
	}
	edge, ok := event.EdgeOf(e)
	if !ok {
		t.Fatal("fixture event carries no edge")
	}
	edge.OriginRef = "conflicting-agent"
	conflicting := e
	conflicting.Payload = edge
	if err := m.onEvent(context.Background(), conflicting); !errors.Is(err, ErrObservationReceiptConflict) {
		t.Fatalf("conflicting redelivery = %v, want ErrObservationReceiptConflict", err)
	}
	if got := len(c1Rows(t, st, tenant, observationReceiptKind)); got != 1 {
		t.Fatalf("receipts after the upgrade = %d, want 1", got)
	}
	if got := len(c1Rows(t, st, tenant, observationConflictKind)); got != 1 {
		t.Fatalf("retained conflicts after the upgrade = %d, want 1", got)
	}

	if _, ok := sweepState(t, st, tenant); ok {
		t.Fatal("the sweep state row exists before any turn: it must be lazy, not a backfill")
	}
	marked, err := m.Sweep(context.Background(), at)
	if err != nil {
		t.Fatalf("sweep on the upgraded estate: %v", err)
	}
	if marked != len(oldIDs) {
		t.Fatalf("marked %d, want the %d legacy entries", marked, len(oldIDs))
	}
	if _, ok := sweepState(t, st, tenant); !ok {
		t.Fatal("no sweep state row was created lazily on the first turn")
	}
	if got := idsWithStatus(catalogStatuses(t, st, tenant), statusStale); !equalStrings(got, oldIDs) {
		t.Fatalf("stale set after the upgrade = %d ids, want the exact %d legacy ones (first divergence: %s)",
			len(got), len(oldIDs), firstDivergence(got, oldIDs))
	}
}

// --- group 2 (module half): authority and configuration ---------------------

// TestSweepWithoutScopeSourceRefusesAndMutatesNothing: an absent seam is an
// explicit error, never an empty success, and never a fallback to whatever the
// module happened to have observed.
func TestSweepWithoutScopeSourceRefusesAndMutatesNothing(t *testing.T) {
	at := baseTime.Add(2 * time.Hour)
	path := filepath.Join(t.TempDir(), "estate.db")
	m, st := openDurableEstate(t, path, at)
	tenant := c1Tenant(t, st, "no-scope-source")
	seedCatalog(t, st, tenant, kindSession, 6, baseTime)

	// Ingestion HAS observed this tenant. Under the retired seenTenants authority
	// that alone would have made it sweepable; it must not.
	if err := m.onEvent(context.Background(), c1Event(tenant, "observed-event", "source-a")); err != nil {
		t.Fatalf("C1 ingestion: %v", err)
	}
	recorder := &turnRecorder{ModuleData: api.NewModuleData(st)}
	m.UseData(recorder)

	n, err := m.Sweep(context.Background(), at)
	if !errors.Is(err, ErrSweepScopeUnavailable) {
		t.Fatalf("sweep without a scope source = (%d, %v), want ErrSweepScopeUnavailable", n, err)
	}
	if n != 0 {
		t.Fatalf("a refused sweep counted %d entries", n)
	}
	if got := recorder.visited(); len(got) != 0 {
		t.Fatalf("a refused sweep opened %d mutations: %v", len(got), got)
	}
	for id, status := range catalogStatuses(t, st, tenant) {
		if status != statusActive {
			t.Fatalf("entry %s became %q with no authoritative scope", id, status)
		}
	}
}

// TestSweepDiscardsRowsThatCameWithAnEnumerationError makes the review's
// enumeration limit executable: ListOrgs returns rows ALONGSIDE
// ErrEnumerationNotAuthoritative, and consuming them would sweep a set the store
// had just refused to certify.
func TestSweepDiscardsRowsThatCameWithAnEnumerationError(t *testing.T) {
	at := baseTime.Add(2 * time.Hour)
	path := filepath.Join(t.TempDir(), "estate.db")
	m, st := openDurableEstate(t, path, at)
	tenant := c1Tenant(t, st, "rows-with-error")
	seedCatalog(t, st, tenant, kindSession, 5, baseTime)
	recorder := &turnRecorder{ModuleData: api.NewModuleData(st)}
	m.UseData(recorder)

	source := scopeOf(tenant)
	source.err = fmt.Errorf("%w: no admin pool", store.ErrEnumerationNotAuthoritative)
	m.UseSweepScopeSource(source)

	n, err := m.Sweep(context.Background(), at)
	if !errors.Is(err, store.ErrEnumerationNotAuthoritative) {
		t.Fatalf("sweep = (%d, %v), want the enumeration error propagated", n, err)
	}
	if n != 0 {
		t.Fatalf("a non-authoritative enumeration counted %d entries", n)
	}
	if got := recorder.visited(); len(got) != 0 {
		t.Fatalf("rows that arrived with an enumeration error were still swept: %v", got)
	}
	for id, status := range catalogStatuses(t, st, tenant) {
		if status != statusActive {
			t.Fatalf("entry %s became %q from a non-authoritative enumeration", id, status)
		}
	}
}

// TestStartKeepsIngestionWhenTheSweepCannotRun: making Start fail would
// unsubscribe C1 as well (core/runtime/lifecycle.go:124-126). An estate that
// cannot sweep must still ingest, and must still say it cannot sweep.
func TestStartKeepsIngestionWhenTheSweepCannotRun(t *testing.T) {
	at := baseTime.Add(2 * time.Hour)
	path := filepath.Join(t.TempDir(), "estate.db")
	m, st := openDurableEstate(t, path, at)
	tenant := c1Tenant(t, st, "start-without-scope")

	if err := m.Start(context.Background()); err != nil {
		t.Fatalf("Start with an operational ModuleData and no scope source = %v, want nil", err)
	}
	t.Cleanup(func() { _ = m.Stop(context.Background()) })

	if err := m.onEvent(context.Background(), c1Event(tenant, "post-start-event", "source-a")); err != nil {
		t.Fatalf("C1 ingestion after Start: %v", err)
	}
	if got := len(c1Rows(t, st, tenant, observationReceiptKind)); got != 1 {
		t.Fatalf("receipts after Start = %d, want the ingestion to persist while the sweep is unavailable", got)
	}
	if _, err := m.Sweep(context.Background(), at); !errors.Is(err, ErrSweepScopeUnavailable) {
		t.Fatalf("the sweep must still say it cannot run, got %v", err)
	}
}

// --- group 3: atomic page and honest return ---------------------------------

// TestFocalFailureAfterRealUpdatesLeavesNoPartialPage arms a failure on the
// third Update of the page — after two rows were really written — and then
// removes it. Nothing of the failed page, status or cursor, may survive, and the
// explicit next call must process that same page exactly once.
func TestFocalFailureAfterRealUpdatesLeavesNoPartialPage(t *testing.T) {
	for _, tc := range []struct {
		name string
		occ  bool
	}{
		{"injected repository failure", false},
		{"real store OCC conflict", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			at := baseTime.Add(2 * time.Hour)
			path := filepath.Join(t.TempDir(), "estate.db")
			m, st := openDurableEstate(t, path, at)
			tenant := c1Tenant(t, st, "focal-failure")
			oldIDs := seedCatalog(t, st, tenant, kindSession, 9, baseTime)
			failing := &failingCatalogData{ModuleData: api.NewModuleData(st)}
			m.UseData(failing)
			m.UseSweepScopeSource(scopeOf(tenant))

			boom := errors.New("focal repository failure")
			failing.arm(3, boom, tc.occ)
			n, err := m.Sweep(context.Background(), at)
			if err == nil {
				t.Fatal("the sweep reported success while its page failed")
			}
			if tc.occ {
				if !errors.Is(err, store.ErrConflict) {
					t.Fatalf("OCC failure = %v, want store.ErrConflict from the store itself", err)
				}
			} else if !errors.Is(err, boom) {
				t.Fatalf("failure = %v, want the injected %v", err, boom)
			}
			if n != 0 {
				t.Fatalf("a failed turn counted %d entries", n)
			}
			for id, status := range catalogStatuses(t, st, tenant) {
				if status != statusActive {
					t.Fatalf("entry %s escaped the rollback as %q", id, status)
				}
			}
			if state, ok := sweepState(t, st, tenant); ok {
				if state.String(colCatalogCursor) != "" || state.String(colCycleCutoffAt) != "" {
					t.Fatalf("a failed turn persisted progress: cutoff=%q cursor=%q",
						state.String(colCycleCutoffAt), state.String(colCatalogCursor))
				}
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
				t.Fatalf("after the retry the stale set is %d ids, want the exact %d (first divergence: %s)",
					len(got), len(oldIDs), firstDivergence(got, oldIDs))
			}
		})
	}
}

// TestErrorAfterAConfirmedCommitIsUncertainNotRolledBack is the correction root
// adjudicated: a commit error does not prove a rollback.
//
// This models a LOST ACKNOWLEDGEMENT — the transaction really committed and the
// caller was told it failed. It is a SIMULATION of that outcome, not a
// demonstrated PostgreSQL network fault. The contract under test is that the
// turn adds zero to the count and that the next explicit call rereads the
// DURABLE state, which here is the ADVANCED one.
func TestErrorAfterAConfirmedCommitIsUncertainNotRolledBack(t *testing.T) {
	at := baseTime.Add(2 * time.Hour)
	path := filepath.Join(t.TempDir(), "estate.db")
	m, st := openDurableEstate(t, path, at)
	tenant := c1Tenant(t, st, "lost-ack")
	oldIDs := seedCatalog(t, st, tenant, kindSession, listCap+30, baseTime)

	lostAck := errors.New("simulated lost acknowledgement of a confirmed commit")
	var swallowed atomic.Bool
	recorder := &turnRecorder{ModuleData: api.NewModuleData(st)}
	recorder.afterCommit = func(_ model.TenantID, committed error) error {
		if committed != nil {
			return committed
		}
		if swallowed.CompareAndSwap(false, true) {
			return lostAck
		}
		return nil
	}
	m.UseData(recorder)
	m.UseSweepScopeSource(scopeOf(tenant))

	n, err := m.Sweep(context.Background(), at)
	if !errors.Is(err, lostAck) {
		t.Fatalf("sweep = (%d, %v), want the uncertain outcome propagated", n, err)
	}
	if n != 0 {
		t.Fatalf("a turn whose confirmation was lost counted %d entries; only a confirmed return may count", n)
	}
	// The page DID commit. The contract does not undo it; it rereads it.
	state, ok := sweepState(t, st, tenant)
	if !ok {
		t.Fatal("no durable state after the confirmed-but-unacknowledged commit")
	}
	if state.String(colCatalogCursor) == "" {
		t.Fatal("the durable state kept no cursor, so this is not the advanced-state case at all")
	}
	if got := len(idsWithStatus(catalogStatuses(t, st, tenant), statusStale)); got != listCap {
		t.Fatalf("durable stale rows = %d, want the %d that really committed", got, listCap)
	}

	next, err := m.Sweep(context.Background(), at)
	if err != nil {
		t.Fatalf("the next explicit call failed: %v", err)
	}
	if want := len(oldIDs) - listCap; next != want {
		t.Fatalf("the next call marked %d, want the %d remaining from the ADVANCED durable state", next, want)
	}
	if got := idsWithStatus(catalogStatuses(t, st, tenant), statusStale); !equalStrings(got, oldIDs) {
		t.Fatalf("final stale set is %d ids, want the exact %d (first divergence: %s)",
			len(got), len(oldIDs), firstDivergence(got, oldIDs))
	}
}

// --- group 4: fair progress and closure -------------------------------------

// TestOneExhaustedTenantDoesNotStarveTheOthers covers the non-starvation claim:
// a turn that reports ITS OWN context exhausted leaves the later tenants fresh,
// usable contexts, and the error names that tenant without carrying its business
// data.
//
// The one-page bound is deliberately not part of this test. It is already held
// by TestDurableFreshnessContinuesAcrossReopenWithoutNewEvents with listCap+217
// real rows. Requiring a 1000-update SQLite transaction to finish inside the SAME
// 300 ms used to expire T1 made this context test a machine-speed benchmark under
// -race: a slow T2 could roll back honestly while T3 still proved non-starvation.
func TestOneExhaustedTenantDoesNotStarveTheOthers(t *testing.T) {
	at := baseTime.Add(2 * time.Hour)
	path := filepath.Join(t.TempDir(), "estate.db")
	m, st := openDurableEstate(t, path, at)
	t1 := c1Tenant(t, st, "starve-t1")
	t2 := c1Tenant(t, st, "starve-t2")
	t3 := c1Tenant(t, st, "starve-t3")
	t1IDs := seedCatalog(t, st, t1, kindSession, 4, baseTime)
	t2IDs := seedCatalog(t, st, t2, kindSession, 4, baseTime)
	t3IDs := seedCatalog(t, st, t3, kindSession, 3, baseTime)

	recorder := &turnRecorder{ModuleData: api.NewModuleData(st)}
	// Inject the exact outcome an exhausted context returns for T1, without making
	// the test wait on a wall-clock race. Every turn must first enter with a live,
	// deadline-bearing context. Inheriting the canceled enumeration context,
	// reusing a spent turn context, or aborting the pass at T1 all make this fail.
	recorder.before = func(ctx context.Context, tenant model.TenantID) error {
		if _, ok := ctx.Deadline(); !ok {
			return fmt.Errorf("tenant %s received a turn context without a deadline", tenant)
		}
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("tenant %s received an already-spent turn context: %w", tenant, err)
		}
		if tenant == t1 {
			return context.DeadlineExceeded
		}
		return nil
	}
	m.UseData(recorder)
	source := scopeOf(t1, t2, t3)
	m.UseSweepScopeSource(source)

	n, err := m.Sweep(context.Background(), at)
	if err == nil {
		t.Fatal("the sweep hid T1's exhausted turn")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("T1's error = %v, want its injected context.DeadlineExceeded outcome", err)
	}
	if !strings.Contains(err.Error(), t1.String()) {
		t.Fatalf("the error does not identify the affected tenant: %v", err)
	}
	if strings.Contains(err.Error(), "starve-t1") {
		t.Fatalf("the error carries the org slug rather than only the tenant id: %v", err)
	}
	for _, healthy := range []model.TenantID{t2, t3} {
		if strings.Contains(err.Error(), healthy.String()) {
			t.Fatalf("the error names healthy tenant %s: %v", healthy, err)
		}
	}
	if want := len(t2IDs) + len(t3IDs); n != want {
		t.Fatalf("sweep counted %d, want %d: all small rows for T2 and T3, and nothing for T1", n, want)
	}
	visited, wantVisited := recorder.visited(), []model.TenantID{t1, t2, t3}
	if len(visited) != len(wantVisited) {
		t.Fatalf("turns opened = %v, want T1, T2 and T3 exactly once in snapshot order", visited)
	}
	for i := range wantVisited {
		if visited[i] != wantVisited[i] {
			t.Fatalf("turns opened = %v, want T1, T2 and T3 exactly once in snapshot order", visited)
		}
	}
	if got := idsWithStatus(catalogStatuses(t, st, t1), statusStale); len(got) != 0 {
		t.Fatalf("T1 committed %d stale rows after its exhausted turn: %v", len(got), got)
	}
	if got := idsWithStatus(catalogStatuses(t, st, t1), statusActive); !equalStrings(got, t1IDs) {
		t.Fatalf("T1 active ids = %v after its exhausted turn, want the unchanged %v", got, t1IDs)
	}
	for _, healthy := range []struct {
		tenant model.TenantID
		want   []string
	}{{t2, t2IDs}, {t3, t3IDs}} {
		if got := idsWithStatus(catalogStatuses(t, st, healthy.tenant), statusStale); !equalStrings(got, healthy.want) {
			t.Fatalf("healthy tenant %s stale ids = %v, want %v", healthy.tenant, got, healthy.want)
		}
	}

	// Each turn was given a context of its own, derived from the CALLER. Every
	// deadline is later than the enumeration's, and each successor is later than
	// its predecessor's: neither an already-canceled enumeration context nor one
	// turn context can have been reused.
	enum := source.enumerationDeadline(t)
	deadlines := recorder.turnDeadlines()
	if len(deadlines) != 3 {
		t.Fatalf("turn deadlines = %v, want one for each tenant", deadlines)
	}
	for i, d := range deadlines {
		if d.IsZero() {
			t.Fatalf("turn %d ran with no deadline of its own", i)
		}
		if !d.After(enum) {
			t.Fatalf("turn %d deadline %s is not after the enumeration deadline %s: the turn inherited the enumeration budget",
				i, d, enum)
		}
		if i > 0 && !d.After(deadlines[i-1]) {
			t.Fatalf("turn %d deadline %s did not advance beyond prior turn deadline %s: a turn context was reused",
				i, d, deadlines[i-1])
		}
	}
}

// TestParentCancellationStopsTheFollowingTurns: one turn's timeout is local, but
// the caller's cancellation — which is how Stop reaches the sweep — ends the pass.
func TestParentCancellationStopsTheFollowingTurns(t *testing.T) {
	at := baseTime.Add(2 * time.Hour)
	path := filepath.Join(t.TempDir(), "estate.db")
	m, st := openDurableEstate(t, path, at)
	t1 := c1Tenant(t, st, "cancel-t1")
	t2 := c1Tenant(t, st, "cancel-t2")
	t3 := c1Tenant(t, st, "cancel-t3")
	for _, tenant := range []model.TenantID{t1, t2, t3} {
		seedCatalog(t, st, tenant, kindSession, 3, baseTime)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	recorder := &turnRecorder{ModuleData: api.NewModuleData(st)}
	recorder.afterCommit = func(tenant model.TenantID, committed error) error {
		if tenant == t1 {
			cancel()
		}
		return committed
	}
	m.UseData(recorder)
	m.UseSweepScopeSource(scopeOf(t1, t2, t3))

	n, err := m.Sweep(ctx, at)
	if err == nil {
		t.Fatal("a cancelled pass reported success")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled pass error = %v, want context.Canceled", err)
	}
	if n != 3 {
		t.Fatalf("counted %d, want the 3 entries T1 really committed before the cancellation", n)
	}
	if got := recorder.visited(); len(got) != 1 || got[0] != t1 {
		t.Fatalf("turns after the cancellation = %v, want only T1", got)
	}
	for _, tenant := range []model.TenantID{t2, t3} {
		for id, status := range catalogStatuses(t, st, tenant) {
			if status != statusActive {
				t.Fatalf("tenant %s entry %s was swept after the cancellation", tenant, id)
			}
		}
	}
}

// --- group 6: the clock ------------------------------------------------------

// TestClockGoingBackwardsDuringACycleClearsItWithoutClassifying: a cutoff in the
// future of the new candidate must not be used to judge an observation received
// under the retarded clock.
func TestClockGoingBackwardsDuringACycleClearsItWithoutClassifying(t *testing.T) {
	at := baseTime.Add(4 * time.Hour)
	path := filepath.Join(t.TempDir(), "estate.db")
	m, st := openDurableEstate(t, path, at)
	tenant := c1Tenant(t, st, "clock-backwards")
	seedCatalog(t, st, tenant, kindSession, listCap+5, baseTime)
	m.UseSweepScopeSource(scopeOf(tenant))

	if n, err := m.Sweep(context.Background(), at); err != nil || n != listCap {
		t.Fatalf("first page = (%d, %v), want (%d, nil)", n, err, listCap)
	}
	before := catalogStatuses(t, st, tenant)

	// The clock goes back two hours WHILE the cycle is open.
	n, err := m.Sweep(context.Background(), at.Add(-2*time.Hour))
	if err != nil {
		t.Fatalf("retarded-clock turn: %v", err)
	}
	if n != 0 {
		t.Fatalf("the retarded-clock turn classified %d rows; it must clear the cycle and stop", n)
	}
	state, ok := sweepState(t, st, tenant)
	if !ok {
		t.Fatal("the state row disappeared")
	}
	if state.String(colCycleCutoffAt) != "" || state.String(colCatalogCursor) != "" {
		t.Fatalf("the future cycle survived the clock going back: cutoff=%q cursor=%q",
			state.String(colCycleCutoffAt), state.String(colCatalogCursor))
	}
	if got := state.String(colLastCompletedCutoffAt); got != "" {
		t.Fatalf("a cleared cycle fabricated a completed cutoff %q", got)
	}
	after := catalogStatuses(t, st, tenant)
	if len(before) != len(after) {
		t.Fatalf("the retarded-clock turn changed the row count %d → %d", len(before), len(after))
	}
	for id, was := range before {
		if after[id] != was {
			t.Fatalf("entry %s went %q → %q under the retarded clock", id, was, after[id])
		}
	}
}

// TestClockGoingBackwardsBetweenCyclesNeitherRegressesNorFabricates: with no
// cycle open and a candidate at or below the last completed cutoff there is no
// work — and, above all, no decrement and no zero timestamp.
func TestClockGoingBackwardsBetweenCyclesNeitherRegressesNorFabricates(t *testing.T) {
	at := baseTime.Add(4 * time.Hour)
	path := filepath.Join(t.TempDir(), "estate.db")
	m, st := openDurableEstate(t, path, at)
	tenant := c1Tenant(t, st, "clock-between")
	seedCatalog(t, st, tenant, kindSession, 6, baseTime)
	m.UseSweepScopeSource(scopeOf(tenant))

	if _, err := m.Sweep(context.Background(), at); err != nil {
		t.Fatalf("first complete cycle: %v", err)
	}
	completed, _ := sweepState(t, st, tenant)
	want := cutoffFor(at)
	if got := completed.String(colLastCompletedCutoffAt); got != want {
		t.Fatalf("completed cutoff = %q, want %q", got, want)
	}

	fresh := seedCatalog(t, st, tenant, kindAgent, 4, at)
	n, err := m.Sweep(context.Background(), at.Add(-90*time.Minute))
	if err != nil {
		t.Fatalf("retarded-clock pass between cycles: %v", err)
	}
	if n != 0 {
		t.Fatalf("a candidate at or below the completed cutoff did %d rows of work", n)
	}
	state, _ := sweepState(t, st, tenant)
	if got := state.String(colLastCompletedCutoffAt); got != want {
		t.Fatalf("completed cutoff moved to %q under a retarded clock, want %q unchanged", got, want)
	}
	if got := state.String(colCycleCutoffAt); got != "" {
		t.Fatalf("a no-work pass opened a cycle at %q", got)
	}

	// Recovery: the clock returns, the cutoff advances, and the entries that have
	// since gone quiet are classified. Nothing already confirmed is reverted.
	recovered := at.Add(2 * time.Hour)
	if _, err := m.Sweep(context.Background(), recovered); err != nil {
		t.Fatalf("recovered pass: %v", err)
	}
	state, _ = sweepState(t, st, tenant)
	if got := state.String(colLastCompletedCutoffAt); got != cutoffFor(recovered) {
		t.Fatalf("completed cutoff after recovery = %q, want %q", got, cutoffFor(recovered))
	}
	statuses := catalogStatuses(t, st, tenant)
	for _, id := range fresh {
		if statuses[id] != statusStale {
			t.Fatalf("entry %s observed at %s is %q after the recovered cutoff, want stale", id, at, statuses[id])
		}
	}
}

// --- local refusals: state that cannot be read is not an empty state ---------

// TestUnusableProgressIsRefusedRatherThanTreatedAsFinished covers the three
// local refusals: more than one state row, an unreadable timestamp, and HasMore
// with no usable frontier. None of them may look like a finished cycle.
func TestUnusableProgressIsRefusedRatherThanTreatedAsFinished(t *testing.T) {
	at := baseTime.Add(2 * time.Hour)

	t.Run("the unique index refuses a second state row", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "estate.db")
		m, st := openDurableEstate(t, path, at)
		tenant := c1Tenant(t, st, "duplicate-state")
		seedCatalog(t, st, tenant, kindSession, 3, baseTime)
		m.UseSweepScopeSource(scopeOf(tenant))
		if _, err := m.Sweep(context.Background(), at); err != nil {
			t.Fatalf("first pass: %v", err)
		}
		err := st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
			repo, e := sc.Ext(freshnessSweepKind)
			if e != nil {
				return e
			}
			_, e = repo.Create(context.Background(), model.Record{})
			return e
		})
		if !errors.Is(err, store.ErrConflict) {
			t.Fatalf("a second state row = %v, want store.ErrConflict from the unique index", err)
		}
	})

	t.Run("more than one state row is refused", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "estate.db")
		m, st := openDurableEstate(t, path, at)
		tenant := c1Tenant(t, st, "two-state-rows")
		seedCatalog(t, st, tenant, kindSession, 3, baseTime)
		m.UseSweepScopeSource(scopeOf(tenant))
		if _, err := m.Sweep(context.Background(), at); err != nil {
			t.Fatalf("first pass: %v", err)
		}
		m.UseData(duplicatedStateData{ModuleData: api.NewModuleData(st)})
		n, err := m.Sweep(context.Background(), at.Add(time.Hour))
		if err == nil {
			t.Fatalf("two state rows were accepted (%d marked)", n)
		}
		if n != 0 {
			t.Fatalf("two state rows still marked %d entries", n)
		}
	})

	t.Run("an unreadable timestamp is refused", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "estate.db")
		m, st := openDurableEstate(t, path, at)
		tenant := c1Tenant(t, st, "unreadable-state")
		seedCatalog(t, st, tenant, kindSession, 3, baseTime)
		m.UseSweepScopeSource(scopeOf(tenant))
		if _, err := m.Sweep(context.Background(), at); err != nil {
			t.Fatalf("first pass: %v", err)
		}
		if err := st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
			repo, e := sc.Ext(freshnessSweepKind)
			if e != nil {
				return e
			}
			rows, _, e := repo.List(context.Background(), model.Query{Limit: 2})
			if e != nil {
				return e
			}
			rows[0][colLastCompletedCutoffAt] = "not-a-timestamp"
			_, e = repo.Update(context.Background(), rows[0])
			return e
		}); err != nil {
			t.Fatalf("corrupt the durable state: %v", err)
		}
		n, err := m.Sweep(context.Background(), at.Add(time.Hour))
		if err == nil {
			t.Fatalf("an unreadable durable state was accepted (%d marked)", n)
		}
		if n != 0 {
			t.Fatalf("an unreadable durable state still marked %d entries", n)
		}
	})

	t.Run("HasMore with no usable frontier is refused", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "estate.db")
		m, st := openDurableEstate(t, path, at)
		tenant := c1Tenant(t, st, "no-frontier")
		seedCatalog(t, st, tenant, kindSession, 5, baseTime)
		m.UseData(cursorlessPageData{ModuleData: api.NewModuleData(st)})
		m.UseSweepScopeSource(scopeOf(tenant))

		n, err := m.Sweep(context.Background(), at)
		if err == nil {
			t.Fatalf("HasMore with no cursor was accepted as a finished cycle (%d marked)", n)
		}
		if n != 0 {
			t.Fatalf("an unusable page still counted %d entries", n)
		}
		if state, ok := sweepState(t, st, tenant); ok && state.String(colLastCompletedCutoffAt) != "" {
			t.Fatalf("an unusable page finished a cycle at %q", state.String(colLastCompletedCutoffAt))
		}
		for id, status := range catalogStatuses(t, st, tenant) {
			if status != statusActive {
				t.Fatalf("entry %s was classified from an unusable page", id)
			}
		}
	})
}

// --- the seam's declared configuration ---------------------------------------

// TestSweepBudgetDefaultIsThirtySeconds pins the documented default, so the
// option cannot silently become the only source of the value.
func TestSweepBudgetDefaultIsThirtySeconds(t *testing.T) {
	if got := New().sweepBudget; got != defaultSweepBudget {
		t.Fatalf("default sweep budget = %s, want %s", got, defaultSweepBudget)
	}
	if defaultSweepBudget != 30*time.Second {
		t.Fatalf("the declared default budget is %s, want 30s", defaultSweepBudget)
	}
	if got := New(WithSweepBudget(2 * time.Second)).sweepBudget; got != 2*time.Second {
		t.Fatalf("WithSweepBudget did not reach the module: %s", got)
	}
	if got := New(WithSweepBudget(0)).sweepBudget; got != defaultSweepBudget {
		t.Fatalf("a non-positive budget must keep the default, got %s", got)
	}
}
