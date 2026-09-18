// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package finops

// The attempt lifecycle, first internal cut — the durable activation frontier and the census.
//
// Two tenants throughout: T1 crosses the frontier, T2 never does. T2 is not
// decoration — the guarantee under test is that a boundary is PER TENANT, so every
// case that refuses something in T1 checks that the same call still works in T2.

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// -----------------------------------------------------------------------------
// Order and confinement fixtures
// -----------------------------------------------------------------------------

// orderProbe records, per transaction, whether the FinOps writer lock was taken
// before the frontier row was read. It forwards the real capability: a fixture
// that pretended to lock would make every case here vacuous.
type orderProbe struct {
	mu     sync.Mutex
	events []string
}

func (p *orderProbe) mark(what string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.events = append(p.events, what)
}

func (p *orderProbe) seen() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]string, len(p.events))
	copy(out, p.events)
	return out
}

type orderProbeData struct {
	api.ModuleData
	p *orderProbe
}

func (d orderProbeData) View(ctx context.Context, tenant model.TenantID, fn func(store.Scope) error) error {
	return d.ModuleData.View(ctx, tenant, func(sc store.Scope) error {
		return fn(orderProbeScope{Scope: sc, p: d.p})
	})
}

func (d orderProbeData) Mutate(ctx context.Context, tenant model.TenantID, fn func(store.Scope) error) error {
	return d.ModuleData.Mutate(ctx, tenant, func(sc store.Scope) error {
		return fn(orderProbeScope{Scope: sc, p: d.p})
	})
}

type orderProbeScope struct {
	store.Scope
	p *orderProbe
}

func (s orderProbeScope) LockTransaction(ctx context.Context, key string) error {
	locker, ok := s.Scope.(store.TransactionLocker)
	if !ok {
		return errors.New("finops-test: wrapped scope provides no transaction lock")
	}
	if err := locker.LockTransaction(ctx, key); err != nil {
		return err
	}
	s.p.mark("lock:" + key)
	return nil
}

func (s orderProbeScope) Ext(kind model.Kind) (store.GenericRepo, error) {
	repo, err := s.Scope.Ext(kind)
	if err != nil || kind != lifecycleScopeKind {
		return repo, err
	}
	return orderProbeRepo{GenericRepo: repo, p: s.p}, nil
}

type orderProbeRepo struct {
	store.GenericRepo
	p *orderProbe
}

func (r orderProbeRepo) List(ctx context.Context, q model.Query) ([]model.Record, model.Page, error) {
	r.p.mark("frontier-read")
	return r.GenericRepo.List(ctx, q)
}

// brokenScopeData makes the FRONTIER LOOKUP fail while everything else keeps
// working: the "unknown activation state" condition, staged honestly rather than by
// breaking the whole store.
type brokenScopeData struct {
	api.ModuleData
}

func (d brokenScopeData) View(ctx context.Context, tenant model.TenantID, fn func(store.Scope) error) error {
	return d.ModuleData.View(ctx, tenant, func(sc store.Scope) error {
		return fn(brokenScope{Scope: sc})
	})
}

func (d brokenScopeData) Mutate(ctx context.Context, tenant model.TenantID, fn func(store.Scope) error) error {
	return d.ModuleData.Mutate(ctx, tenant, func(sc store.Scope) error {
		return fn(brokenScope{Scope: sc})
	})
}

type brokenScope struct{ store.Scope }

func (s brokenScope) LockTransaction(ctx context.Context, key string) error {
	locker, ok := s.Scope.(store.TransactionLocker)
	if !ok {
		return errors.New("finops-test: wrapped scope provides no transaction lock")
	}
	return locker.LockTransaction(ctx, key)
}

func (s brokenScope) Ext(kind model.Kind) (store.GenericRepo, error) {
	repo, err := s.Scope.Ext(kind)
	if err != nil || kind != lifecycleScopeKind {
		return repo, err
	}
	return brokenRepo{GenericRepo: repo}, nil
}

type brokenRepo struct{ store.GenericRepo }

func (brokenRepo) List(context.Context, model.Query) ([]model.Record, model.Page, error) {
	return nil, model.Page{}, errors.New("finops-test: the lifecycle scope could not be read")
}

// -----------------------------------------------------------------------------
// The two-tenant fixture
// -----------------------------------------------------------------------------

type frontierFixture struct {
	m        *Module
	st       store.Store
	t1, t2   model.TenantID
	v        *labVerifier
	budget1  model.ID
	budget2  model.ID
	pending  model.ID // T1's pending handle (one active + one expired child)
	terminal model.ID // T1's already-terminal handle
	rows     []model.Record
}

// newFrontierFixture builds T1 with a pending group (active + expired), a prior
// TERMINAL group, and a 10 USD monthly block budget; and T2 with the same budget
// and one ordinary legacy hold, which must keep working throughout.
func newFrontierFixture(t testing.TB) *frontierFixture {
	t.Helper()
	m, st, t1, v := newLifecycleFin(t)
	t2 := provisionSecondTenant(t, st)
	v.grant(t2)

	f := &frontierFixture{m: m, st: st, t1: t1, t2: t2, v: v}
	f.budget1 = createBudget(t, st, t1, "b1", budgetSpec{
		Dimension: "global", Period: "monthly", LimitMicroUSD: 10 * oneUSD, Action: "block",
	})
	f.budget2 = createBudget(t, st, t2, "b2", budgetSpec{
		Dimension: "global", Period: "monthly", LimitMicroUSD: 10 * oneUSD, Action: "block",
	})
	f.pending = model.NewID()
	f.terminal = model.NewID()
	last := baseTime.AddDate(0, -1, 0)
	f.rows = seedGroup(t, st, t1,
		legacyChild(f.budget1, f.pending, "", "monthly", 1, 5*oneUSD, baseTime, baseTime.Add(time.Hour), resvStateActive),
		legacyChild(f.budget1, f.pending, "seat-a", "monthly", 2, 2*oneUSD, last, last.Add(time.Hour), resvStateExpired),
	)
	seedGroup(t, st, t1,
		legacyChild(f.budget1, f.terminal, "", "monthly", 3, 9*oneUSD, last, last.Add(time.Hour), resvStateCommitted),
	)
	seedGroup(t, st, t2,
		legacyChild(f.budget2, model.NewID(), "", "monthly", 1, 1*oneUSD, baseTime, baseTime.Add(time.Hour), resvStateActive),
	)
	return f
}

func (f *frontierFixture) begin(t testing.TB) LifecycleScopeView {
	t.Helper()
	view, err := f.m.BeginLifecycleActivation(context.Background(), f.t1, LifecycleActivationRequest{
		Evidence: []EvidenceRef{labEvidence("quiescence"), labEvidence("caller-readiness")},
	})
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	return view
}

// -----------------------------------------------------------------------------
// Group 2 — the frontier and every legacy route
// -----------------------------------------------------------------------------

// TestBeginFreezesThePendingCensusAndTheTerminalBaseline: the pending set is the
// group with an active and an EXPIRED child (an expired obligation was never
// settled, so it is pending, not history); the already-committed group is the
// frozen baseline and is not pending.
func TestBeginFreezesThePendingCensusAndTheTerminalBaseline(t *testing.T) {
	f := newFrontierFixture(t)
	view := f.begin(t)

	if view.PendingGroupCount != 1 {
		t.Fatalf("pending group count = %d, want exactly the one unsettled group", view.PendingGroupCount)
	}
	doc := frontierDocOf(t, f.st, f.t1)
	if len(doc.PendingGroups) != 1 || doc.PendingGroups[0].Handle != f.pending.String() {
		t.Fatalf("pending groups = %+v, want only %s", doc.PendingGroups, f.pending)
	}
	if doc.PendingGroups[0].ChildCount != 2 {
		t.Fatalf("the pending group covers %d children, want both", doc.PendingGroups[0].ChildCount)
	}
	if doc.HistoricalTerminalCount != 1 {
		t.Fatalf("terminal baseline count = %d, want the one committed group", doc.HistoricalTerminalCount)
	}
	if doc.PendingGroupDigest == doc.HistoricalTerminalDigest {
		t.Fatalf("the two halves of the census share a digest")
	}
	// T2 is untouched: a frontier is per tenant.
	if n := len(scopeRows(t, f.st, f.t2)); n != 0 {
		t.Fatalf("T2 acquired %d frontier rows from T1's Begin", n)
	}
}

func frontierDocOf(t testing.TB, st store.Store, tenant model.TenantID) jsonFrontier {
	t.Helper()
	rows := scopeRows(t, st, tenant)
	if len(rows) != 1 {
		t.Fatalf("%d frontier rows for %s, want 1", len(rows), tenant)
	}
	doc, err := frontierDocumentOf(rows[0])
	if err != nil {
		t.Fatalf("frontier document: %v", err)
	}
	return doc
}

// TestEveryCoveredWrapperIsRefusedAfterTheFrontierAndT2IsNot is the compatibility
// causal in one case: all five wrappers, INCLUDING a reserve with no enforcing
// target, refuse in T1 with lifecycle_api_required and change nothing — while the
// identical calls in T2 keep their adjudicated legacy behavior.
func TestEveryCoveredWrapperIsRefusedAfterTheFrontierAndT2IsNot(t *testing.T) {
	f := newFrontierFixture(t)
	// A handle T2 can settle, and one T1 could have settled before the frontier.
	t1Handle := f.rows[0].String(colResvHandle)
	f.begin(t)

	before := countReservations(t, f.st, f.t1)
	auditBefore := len(auditActions(t, f.st, f.t1))

	t.Run("ReserveBudget", func(t *testing.T) {
		res, err := f.m.ReserveBudget(context.Background(), f.t1, SpendDims{}, 1*oneUSD)
		requireFrontierRefusal(t, res, err)
	})
	t.Run("ReserveBudget with no enforcing target", func(t *testing.T) {
		// A dimension no budget scopes: the evaluation has nothing to bind, and the
		// The pre-lifecycle fast path answered Allowed:true before opening a transaction.
		m2, st2, tenant2, v2 := newLifecycleFin(t)
		_ = v2
		handle := model.NewID()
		bid := createBudget(t, st2, tenant2, "provider-only", budgetSpec{
			Dimension: "provider", Key: "anthropic", Period: "monthly",
			LimitMicroUSD: 10 * oneUSD, Action: "block",
		})
		seedGroup(t, st2, tenant2, legacyChild(bid, handle, "anthropic", "monthly", 1, 1*oneUSD, baseTime, baseTime.Add(time.Hour), resvStateActive))
		// Before the frontier the no-target call admits, as it always has: the request
		// names a provider no enforcing budget scopes.
		if res, err := m2.ReserveBudget(context.Background(), tenant2, SpendDims{ProviderRef: "openai"}, 1*oneUSD); err != nil || !res.Allowed {
			t.Fatalf("the no-target path before the frontier: %+v err=%v", res, err)
		}
		if _, err := m2.BeginLifecycleActivation(context.Background(), tenant2, LifecycleActivationRequest{
			Evidence: []EvidenceRef{labEvidence("quiescence")},
		}); err != nil {
			t.Fatalf("Begin: %v", err)
		}
		res, err := m2.ReserveBudget(context.Background(), tenant2, SpendDims{ProviderRef: "openai"}, 1*oneUSD)
		requireFrontierRefusal(t, res, err)
	})
	t.Run("ReserveSpendLimit", func(t *testing.T) {
		res, err := f.m.ReserveSpendLimit(context.Background(), f.t1, "actor-a", nil, 1*oneUSD)
		requireFrontierRefusal(t, res, err)
	})
	t.Run("CommitReservation", func(t *testing.T) {
		err := f.m.CommitReservation(context.Background(), f.t1, t1Handle, 1*oneUSD)
		if attemptCode(err) != errCodeLifecycleAPIRequired {
			t.Fatalf("code = %q, want lifecycle_api_required (err=%v)", attemptCode(err), err)
		}
	})
	t.Run("ReleaseReservation", func(t *testing.T) {
		err := f.m.ReleaseReservation(context.Background(), f.t1, t1Handle)
		if attemptCode(err) != errCodeLifecycleAPIRequired {
			t.Fatalf("code = %q, want lifecycle_api_required (err=%v)", attemptCode(err), err)
		}
	})
	t.Run("SweepExpiredReservations", func(t *testing.T) {
		n, err := f.m.SweepExpiredReservations(context.Background(), f.t1)
		if attemptCode(err) != errCodeLifecycleAPIRequired {
			t.Fatalf("code = %q, want lifecycle_api_required (err=%v)", attemptCode(err), err)
		}
		if n != 0 {
			t.Fatalf("the refused sweep reported %d rows swept", n)
		}
	})

	// NOTHING moved in T1: no new row, no state transition, no audit event.
	after := countReservations(t, f.st, f.t1)
	if len(after) != len(before) {
		t.Fatalf("reservation rows: %d -> %d across the refusals", len(before), len(after))
	}
	beforeStates := map[string]string{}
	for _, r := range before {
		beforeStates[r.String(model.ColID)] = r.String(colResvState)
	}
	for _, r := range after {
		if got, want := r.String(colResvState), beforeStates[r.String(model.ColID)]; got != want {
			t.Fatalf("row %s moved %q -> %q behind the frontier", r.String(model.ColID), want, got)
		}
	}
	if got := len(auditActions(t, f.st, f.t1)); got != auditBefore {
		t.Fatalf("the refusals wrote %d audit events", got-auditBefore)
	}

	// T2 keeps every one of those behaviors.
	res, err := f.m.ReserveBudget(context.Background(), f.t2, SpendDims{}, 1*oneUSD)
	if err != nil || !res.Allowed || res.Handle == "" {
		t.Fatalf("T2 reserve: %+v err=%v", res, err)
	}
	if err := f.m.CommitReservation(context.Background(), f.t2, res.Handle, 1*oneUSD); err != nil {
		t.Fatalf("T2 commit: %v", err)
	}
	if _, err := f.m.SweepExpiredReservations(context.Background(), f.t2); err != nil {
		t.Fatalf("T2 sweep: %v", err)
	}
}

func requireFrontierRefusal(t testing.TB, res BudgetReservation, err error) {
	t.Helper()
	if attemptCode(err) != errCodeLifecycleAPIRequired {
		t.Fatalf("code = %q, want lifecycle_api_required (err=%v)", attemptCode(err), err)
	}
	if res.Allowed {
		t.Fatalf("the refusal was reported as an admission: %+v", res)
	}
	if res.Handle != "" {
		t.Fatalf("a refused reservation returned a handle: %+v", res)
	}
}

// TestTheGuardIsReadInsideTheTransactionAfterTheLock: the frontier is consulted in
// the SAME transaction as the mutation and AFTER the writer lock, not before the
// call and not in a separate read.
//
// It is asserted on ORDER within one transaction, deliberately, and NOT by holding
// two callbacks open at once: the current store serializes a tenant's Mutate, so a
// barrier expecting simultaneous callbacks would deadlock rather than measure
// anything, and no exclusion is claimed here.
func TestTheGuardIsReadInsideTheTransactionAfterTheLock(t *testing.T) {
	f := newFrontierFixture(t)
	f.begin(t)
	p := &orderProbe{}
	f.m.UseData(orderProbeData{ModuleData: f.m.data, p: p})

	res, err := f.m.ReserveBudget(context.Background(), f.t1, SpendDims{}, 1*oneUSD)
	requireFrontierRefusal(t, res, err)

	events := p.seen()
	lockAt, readAt := -1, -1
	for i, e := range events {
		if lockAt < 0 && len(e) > 5 && e[:5] == "lock:" {
			lockAt = i
		}
		if readAt < 0 && e == "frontier-read" {
			readAt = i
		}
	}
	if lockAt < 0 || readAt < 0 {
		t.Fatalf("events = %v, want both a lock and a frontier read", events)
	}
	if lockAt > readAt {
		t.Fatalf("the frontier was read BEFORE the writer lock: %v", events)
	}
	// And it is the one physical key the other FinOps writers take.
	if want := "lock:" + alertWriterLockKeyPrefix + f.t1.String(); events[lockAt] != want {
		t.Fatalf("locked %q, want the existing FinOps writer key %q", events[lockAt], want)
	}
}

// TestAnUnknownFrontierNeverAdmits stages the two ways the state cannot be
// established — no data handle, and a lookup that fails — and requires that
// neither produces an admission. This is the behavior change the cut introduces
// and the reason covered callers must stop treating an error as permission.
func TestAnUnknownFrontierNeverAdmits(t *testing.T) {
	t.Run("no data handle", func(t *testing.T) {
		m := New()
		m.clock = &fakeClock{t: baseTime}
		res, err := m.ReserveBudget(context.Background(), model.TenantID(model.NewID().String()), SpendDims{}, 1*oneUSD)
		if res.Allowed {
			t.Fatalf("a module with no store admitted: %+v", res)
		}
		if attemptCode(err) != errCodeCapabilityUnavailable {
			t.Fatalf("code = %q, want capability_unavailable", attemptCode(err))
		}
		if _, err := m.SweepExpiredReservations(context.Background(), model.TenantID(model.NewID().String())); attemptCode(err) != errCodeCapabilityUnavailable {
			t.Fatalf("sweep code = %q, want capability_unavailable", attemptCode(err))
		}
		if err := m.CommitReservation(context.Background(), model.TenantID(model.NewID().String()), model.NewID().String(), 1); attemptCode(err) != errCodeCapabilityUnavailable {
			t.Fatalf("commit code = %q, want capability_unavailable", attemptCode(err))
		}
	})

	t.Run("a failed lookup", func(t *testing.T) {
		f := newFrontierFixture(t)
		f.m.UseData(brokenScopeData{ModuleData: f.m.data})
		res, err := f.m.ReserveBudget(context.Background(), f.t1, SpendDims{}, 1*oneUSD)
		if res.Allowed {
			t.Fatalf("an unreadable frontier admitted: %+v", res)
		}
		if attemptCode(err) != errCodeStoreUnavailable {
			t.Fatalf("code = %q, want store_unavailable — and never an inferred inactive", attemptCode(err))
		}
		if n := len(countReservations(t, f.st, f.t1)); n != 3 {
			t.Fatalf("%d reservation rows, want the 3 seeded ones and nothing new", n)
		}
	})
}

// TestAnImportBeforeTheFrontierIsValidatedApartAndNotReimported: a v1 group that
// already existed when Begin ran is NOT in the pending census (it is not a legacy
// obligation any more), and Begin still succeeds.
func TestAnImportBeforeTheFrontierIsValidatedApartAndNotReimported(t *testing.T) {
	f := newFrontierFixture(t)
	early := model.NewID()
	rows := seedGroup(t, f.st, f.t1,
		legacyChild(f.budget1, early, "", "monthly", 7, 3*oneUSD, baseTime, baseTime.Add(time.Hour), resvStateActive))
	imported, err := f.m.ImportLegacyHold(context.Background(), f.t1, importFor(t, early, rows))
	if err != nil {
		t.Fatalf("import before Begin: %v", err)
	}
	if imported.LegacyImport.FrontierRef != nil {
		t.Fatalf("an import taken before any frontier captured a frontier reference")
	}

	view := f.begin(t)
	if view.PendingGroupCount != 1 {
		t.Fatalf("pending group count = %d, want only the legacy group", view.PendingGroupCount)
	}
	doc := frontierDocOf(t, f.st, f.t1)
	for _, g := range doc.PendingGroups {
		if g.Handle == early.String() {
			t.Fatalf("an already-imported group was re-censused as pending legacy")
		}
	}
	// Re-importing it is a replay against the SAME parent, not a second obligation,
	// and the boundary does not change that.
	replay, err := f.m.ImportLegacyHold(context.Background(), f.t1, importFor(t, early, rows))
	if err != nil {
		t.Fatalf("replay of the pre-frontier import: %v", err)
	}
	if replay.AttemptRef != imported.AttemptRef {
		t.Fatalf("the replay produced another identity: %q vs %q", replay.AttemptRef, imported.AttemptRef)
	}
	if replay.LegacyImport.FrontierRef != nil {
		t.Fatalf("a replay acquired a frontier reference the original import did not have")
	}
	if n := len(attemptRows(t, f.st, f.t1)); n != 1 {
		t.Fatalf("%d attempt rows after the replay, want 1", n)
	}
}

// TestBeginBlocksOnAGroupItCannotClassify walks the three shapes that must stop the
// boundary rather than shrink the census: a group mixing pending and terminal rows,
// a group mixing legacy and v1 children, and a v1 group whose parent is gone.
func TestBeginBlocksOnAGroupItCannotClassify(t *testing.T) {
	t.Run("a group mixing pending and terminal rows", func(t *testing.T) {
		m, st, tenant, _ := newLifecycleFin(t)
		bid := createBudget(t, st, tenant, "b", budgetSpec{Dimension: "global", Period: "monthly", LimitMicroUSD: 10 * oneUSD, Action: "block"})
		handle := model.NewID()
		seedGroup(t, st, tenant,
			legacyChild(bid, handle, "", "monthly", 1, 5*oneUSD, baseTime, baseTime.Add(time.Hour), resvStateActive),
			legacyChild(bid, handle, "seat-a", "monthly", 2, 2*oneUSD, baseTime, baseTime.Add(time.Hour), resvStateReleased))
		_, err := m.BeginLifecycleActivation(context.Background(), tenant, LifecycleActivationRequest{
			Evidence: []EvidenceRef{labEvidence("quiescence")},
		})
		if attemptCode(err) != errCodeLegacyUnresolved {
			t.Fatalf("code = %q, want legacy_unresolved", attemptCode(err))
		}
		if n := len(scopeRows(t, st, tenant)); n != 0 {
			t.Fatalf("a blocked Begin still wrote %d frontier rows", n)
		}
	})

	t.Run("a group mixing legacy and v1 children", func(t *testing.T) {
		m, st, tenant, _ := newLifecycleFin(t)
		bid := createBudget(t, st, tenant, "b", budgetSpec{Dimension: "global", Period: "monthly", LimitMicroUSD: 10 * oneUSD, Action: "block"})
		handle := model.NewID()
		linked := legacyChild(bid, handle, "seat-a", "monthly", 2, 2*oneUSD, baseTime, baseTime.Add(time.Hour), resvStateActive)
		linked[colResvAttemptRef] = string(legacyImportRef(tenant, handle))
		linked[colResvLifecycleVersion] = lifecycleLinkageVersion
		seedGroup(t, st, tenant,
			legacyChild(bid, handle, "", "monthly", 1, 5*oneUSD, baseTime, baseTime.Add(time.Hour), resvStateActive),
			linked)
		_, err := m.BeginLifecycleActivation(context.Background(), tenant, LifecycleActivationRequest{
			Evidence: []EvidenceRef{labEvidence("quiescence")},
		})
		if attemptCode(err) != errCodeLegacyUnresolved {
			t.Fatalf("code = %q, want legacy_unresolved", attemptCode(err))
		}
	})

	t.Run("a v1 group with no parent", func(t *testing.T) {
		m, st, tenant, _ := newLifecycleFin(t)
		bid := createBudget(t, st, tenant, "b", budgetSpec{Dimension: "global", Period: "monthly", LimitMicroUSD: 10 * oneUSD, Action: "block"})
		handle := model.NewID()
		orphan := legacyChild(bid, handle, "", "monthly", 1, 5*oneUSD, baseTime, baseTime.Add(time.Hour), resvStateActive)
		orphan[colResvAttemptRef] = string(legacyImportRef(tenant, handle))
		orphan[colResvLifecycleVersion] = lifecycleLinkageVersion
		seedGroup(t, st, tenant, orphan)
		_, err := m.BeginLifecycleActivation(context.Background(), tenant, LifecycleActivationRequest{
			Evidence: []EvidenceRef{labEvidence("quiescence")},
		})
		if attemptCode(err) != errCodeLegacyUnresolved {
			t.Fatalf("code = %q, want legacy_unresolved", attemptCode(err))
		}
	})
}

// TestBeginRefusesWithoutEvidenceAndReplaysExactly: quiescence and caller
// readiness are evidenced, an exact repeat of the SAME request returns the current
// view without a second census or a second audit, and a DIFFERENT request against
// an existing boundary conflicts.
func TestBeginRefusesWithoutEvidenceAndReplaysExactly(t *testing.T) {
	f := newFrontierFixture(t)
	if _, err := f.m.BeginLifecycleActivation(context.Background(), f.t1, LifecycleActivationRequest{}); attemptCode(err) != errCodeLifecycleActivation {
		t.Fatalf("Begin with no evidence: code = %q, want lifecycle_activation_required", attemptCode(err))
	}
	if n := len(scopeRows(t, f.st, f.t1)); n != 0 {
		t.Fatalf("an unevidenced Begin wrote %d rows", n)
	}

	first := f.begin(t)
	auditAfter := len(auditActions(t, f.st, f.t1))

	replay := f.begin(t)
	if replay.ID != first.ID || replay.Version != first.Version || replay.FrontierDigest != first.FrontierDigest {
		t.Fatalf("the replay produced a different boundary: %+v vs %+v", replay, first)
	}
	if got := len(auditActions(t, f.st, f.t1)); got != auditAfter {
		t.Fatalf("the replay wrote %d audit events", got-auditAfter)
	}
	if n := len(scopeRows(t, f.st, f.t1)); n != 1 {
		t.Fatalf("%d frontier rows after a replay", n)
	}

	_, err := f.m.BeginLifecycleActivation(context.Background(), f.t1, LifecycleActivationRequest{
		Evidence: []EvidenceRef{labEvidence("a-different-claim")},
	})
	if attemptCode(err) != errCodeAttemptIdentityConflict {
		t.Fatalf("a different Begin request: code = %q, want attempt_identity_conflict", attemptCode(err))
	}
	// A stale ExpectedVersion on a tenant with no row is not a replay either.
	m2, st2, tenant2, _ := newLifecycleFin(t)
	_ = st2
	if _, err := m2.BeginLifecycleActivation(context.Background(), tenant2, LifecycleActivationRequest{
		ExpectedVersion: 3, Evidence: []EvidenceRef{labEvidence("quiescence")},
	}); attemptCode(err) != errCodeStaleAttempt {
		t.Fatalf("a nonzero expected version with no row: code = %q, want stale_attempt", attemptCode(err))
	}
}

// -----------------------------------------------------------------------------
// Group 4 — an incomplete or altered census
// -----------------------------------------------------------------------------

// TestTheCensusIsTheExactUnionOfEveryPage: a two-page enumeration must produce the
// union, and every way the paging can end early must REFUSE — a prefix is not a
// census, and a boundary built on one would exempt whatever it did not read.
func TestTheCensusIsTheExactUnionOfEveryPage(t *testing.T) {
	pageRows := func(t testing.TB, tenant model.TenantID, policy model.ID, handles []model.ID) [][]model.Record {
		var out [][]model.Record
		for i, h := range handles {
			r := legacyChild(policy, h, "", "monthly", int64(i+1), oneUSD, baseTime, baseTime.Add(time.Hour), resvStateActive)
			r[model.ColTenantID] = tenant.String()
			r[model.ColID] = model.NewID().String()
			r[model.ColVersion] = int64(1)
			out = append(out, []model.Record{r})
		}
		return out
	}

	t.Run("two pages become one census", func(t *testing.T) {
		m, st, tenant, _ := newLifecycleFin(t)
		bid := createBudget(t, st, tenant, "b", budgetSpec{Dimension: "global", Period: "monthly", LimitMicroUSD: 10 * oneUSD, Action: "block"})
		h1, h2 := model.NewID(), model.NewID()
		pages := pageRows(t, tenant, bid, []model.ID{h1, h2})
		forcePager(m, func(call int, _ model.Query) ([]model.Record, model.Page) {
			if call == 0 {
				return pages[0], model.Page{Cursor: "page-2", HasMore: true}
			}
			return pages[1], model.Page{HasMore: false}
		})
		view, err := m.BeginLifecycleActivation(context.Background(), tenant, LifecycleActivationRequest{
			Evidence: []EvidenceRef{labEvidence("quiescence")},
		})
		if err != nil {
			t.Fatalf("Begin over two pages: %v", err)
		}
		if view.PendingGroupCount != 2 {
			t.Fatalf("pending groups = %d, want the union of both pages", view.PendingGroupCount)
		}
	})

	for _, tc := range []struct {
		name  string
		pager func(int, model.Query) ([]model.Record, model.Page)
	}{
		{"a cursor that never arrives", noCursorPager(nil)},
		{"a cursor that does not advance", stuckCursorPager(nil)},
		{"the page cap with rows still to come", alwaysMorePager(nil)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, st, tenant, _ := newLifecycleFin(t)
			forcePager(m, tc.pager)
			_, err := m.BeginLifecycleActivation(context.Background(), tenant, LifecycleActivationRequest{
				Evidence: []EvidenceRef{labEvidence("quiescence")},
			})
			if attemptCode(err) != errCodeLedgerIncomplete {
				t.Fatalf("code = %q, want ledger_incomplete", attemptCode(err))
			}
			if n := len(scopeRows(t, st, tenant)); n != 0 {
				t.Fatalf("a boundary was written from an incomplete census (%d rows)", n)
			}
		})
	}
}

// TestAlteringAPendingGroupAfterTheFrontierBreaksItsImport is the anti-tampering
// direction: the census recorded a digest over the group's complete original rows,
// so deleting a child or re-labeling the group terminal afterwards cannot make it
// importable, and cannot make it historical either.
func TestAlteringAPendingGroupAfterTheFrontierBreaksItsImport(t *testing.T) {
	t.Run("a deleted child", func(t *testing.T) {
		f := newFrontierFixture(t)
		f.begin(t)
		deleteReservation(t, f.st, f.t1, mustID(t, f.rows[1].String(model.ColID)))
		_, err := f.m.ImportLegacyHold(context.Background(), f.t1, importFor(t, f.pending, f.rows[:1]))
		if attemptCode(err) != errCodeLegacyUnresolved {
			t.Fatalf("code = %q, want legacy_unresolved: the frozen group digest no longer matches", attemptCode(err))
		}
	})

	t.Run("a group re-labeled terminal", func(t *testing.T) {
		f := newFrontierFixture(t)
		f.begin(t)
		for _, r := range f.rows {
			mutateReservation(t, f.st, f.t1, mustID(t, r.String(model.ColID)), func(rec model.Record) {
				rec[colResvState] = resvStateReleased
			})
		}
		rows := currentGroupRows(t, f.st, f.t1, f.pending)
		_, err := f.m.ImportLegacyHold(context.Background(), f.t1, importFor(t, f.pending, rows))
		if attemptCode(err) != errCodeLegacyUnresolved {
			t.Fatalf("code = %q, want legacy_unresolved: a pending group cannot be retired into history", attemptCode(err))
		}
		// And the census still names it as pending, so it has not vanished either.
		doc := frontierDocOf(t, f.st, f.t1)
		if len(doc.PendingGroups) != 1 || doc.PendingGroups[0].Handle != f.pending.String() {
			t.Fatalf("the census lost the group it froze: %+v", doc.PendingGroups)
		}
	})

	t.Run("a handle that appeared after the boundary", func(t *testing.T) {
		f := newFrontierFixture(t)
		f.begin(t)
		late := model.NewID()
		rows := seedGroup(t, f.st, f.t1,
			legacyChild(f.budget1, late, "", "monthly", 9, oneUSD, baseTime, baseTime.Add(time.Hour), resvStateActive))
		_, err := f.m.ImportLegacyHold(context.Background(), f.t1, importFor(t, late, rows))
		if attemptCode(err) != errCodeLegacyUnresolved {
			t.Fatalf("code = %q, want legacy_unresolved: the boundary never covered this group", attemptCode(err))
		}
	})
}

// TestACensusedGroupStillImportsUnderItsFrontier is the positive control the three
// refusals above need: an UNCHANGED pending group imports, captures the frontier
// reference the boundary supplies, and binds it into the import digest.
func TestACensusedGroupStillImportsUnderItsFrontier(t *testing.T) {
	f := newFrontierFixture(t)
	view := f.begin(t)
	imported, err := f.m.ImportLegacyHold(context.Background(), f.t1, importFor(t, f.pending, f.rows))
	if err != nil {
		t.Fatalf("import under the frontier: %v", err)
	}
	if imported.LegacyImport.FrontierRef == nil {
		t.Fatalf("the import captured no frontier reference under a committed boundary")
	}
	if got := imported.LegacyImport.FrontierRef.Digest; got != view.FrontierDigest {
		t.Fatalf("frontier reference digest = %q, want the boundary's %q", got, view.FrontierDigest)
	}
	// The import digest BINDS that reference: an import taken under a boundary and
	// one taken without it are not the same import.
	without := importDigest(f.t1, f.pending, imported.LegacyImport.RequestDigest, imported.LegacyImport.GroupDigest, nil)
	if without == imported.LegacyImport.ImportDigest {
		t.Fatalf("the import digest does not depend on the frontier reference")
	}
}

// currentGroupRows reads a handle's rows as they stand now.
func currentGroupRows(t testing.TB, st store.Store, tenant model.TenantID, handle model.ID) []model.Record {
	t.Helper()
	var out []model.Record
	for _, r := range countReservations(t, st, tenant) {
		if r.String(colResvHandle) == handle.String() {
			out = append(out, r)
		}
	}
	if len(out) == 0 {
		t.Fatalf("handle %s has no rows", handle)
	}
	return out
}

// TestAV1HoldIsNeverTerminalizedByTheLegacyRouteEvenBeforeAFrontier is the rule
// that does NOT wait for a boundary: a v1 child requires the lifecycle to be
// terminalized, so the legacy settlement refuses its group outright and the sweep
// leaves it alone — while the legacy rows beside it keep behaving exactly as they
// did.
//
// The sweep is the sharper half. An imported child KEEPS its original expires_at
// (that is history, and the import does not rewrite it), so a hold whose legacy
// TTL lapsed long ago is precisely what the very next sweep would have expired —
// retiring money with no evidence, no audit and no trace.
func TestAV1HoldIsNeverTerminalizedByTheLegacyRouteEvenBeforeAFrontier(t *testing.T) {
	m, st, tenant, _ := newLifecycleFin(t)
	if n := len(scopeRows(t, st, tenant)); n != 0 {
		t.Fatalf("the fixture already has a frontier; the case is about the state BEFORE one")
	}
	bid := createBudget(t, st, tenant, "b", budgetSpec{Dimension: "global", Period: "monthly", LimitMicroUSD: 10 * oneUSD, Action: "block"})
	long := baseTime.AddDate(0, -1, 0)

	v1Handle := model.NewID()
	v1Rows := seedGroup(t, st, tenant,
		legacyChild(bid, v1Handle, "", "monthly", 1, 7*oneUSD, long, long.Add(time.Hour), resvStateActive))
	imported, err := m.ImportLegacyHold(context.Background(), tenant, importFor(t, v1Handle, v1Rows))
	if err != nil {
		t.Fatalf("import: %v", err)
	}

	// A legacy group beside it, equally expired, which the sweep must still tidy.
	legacyHandle := model.NewID()
	seedGroup(t, st, tenant,
		legacyChild(bid, legacyHandle, "seat-a", "monthly", 2, 1*oneUSD, long, long.Add(time.Hour), resvStateActive))

	if err := m.CommitReservation(context.Background(), tenant, v1Handle.String(), 1*oneUSD); attemptCode(err) != errCodeLifecycleAPIRequired {
		t.Fatalf("legacy commit of a v1 group: code = %q, want lifecycle_api_required (err=%v)", attemptCode(err), err)
	}
	if err := m.ReleaseReservation(context.Background(), tenant, v1Handle.String()); attemptCode(err) != errCodeLifecycleAPIRequired {
		t.Fatalf("legacy release of a v1 group: code = %q, want lifecycle_api_required (err=%v)", attemptCode(err), err)
	}

	swept, err := m.SweepExpiredReservations(context.Background(), tenant)
	if err != nil {
		t.Fatalf("sweep on an inactive tenant: %v", err)
	}
	if swept != 1 {
		t.Fatalf("swept %d rows, want exactly the one legacy row", swept)
	}
	for _, r := range countReservations(t, st, tenant) {
		link, _ := linkageOf(r)
		state := r.String(colResvState)
		switch {
		case link == linkageV1 && state != resvStateActive:
			t.Fatalf("the v1 hold was moved to %q by the legacy sweep", state)
		case link == linkageLegacy && r.String(colResvHandle) == legacyHandle.String() && state != resvStateExpired:
			t.Fatalf("the legacy row beside it was NOT swept: state = %q", state)
		}
	}
	// And the money is still held.
	if got := reservedFor(t, m, st, tenant, bid, ""); !got.established() || got.MicroUSD != 7*oneUSD {
		t.Fatalf("held after the sweep = %+v, want the 7 USD intact", got)
	}
	if got, gerr := m.GetAttempt(context.Background(), tenant, imported.AttemptRef); gerr != nil || got.Phase != phaseOutcomeUnknown {
		t.Fatalf("the parent moved phase: %+v err=%v", got, gerr)
	}
}

// =============================================================================
// CORRECTION R1 — no admission before inactive is CONFIRMED in this attempt
// =============================================================================
//
// The independent return found the hole these controls close: the frontier guard
// runs inside the reservation transaction, but SEVERAL routes return before that
// transaction is ever opened, or fail while opening it, and every one of them used
// to answer Allowed:true. A committed frontier may already exist; none of those
// routes has looked. The rule is now stated once: NO route may admit until this
// attempt has confirmed, under the physical writer lock, that the tenant has no
// frontier — and a previous request's confirmation is not this one's.

// failingViewData fails the Nth and later View calls of the module.
//
// The count is the point: the reservation admission path issues its pre-transaction
// reads through separate Views in a fixed order — the budget census, then the firm
// identity when an enforcing identity budget exists, then the agent groups when an
// enforcing agent_group budget does. Counting therefore aims a failure at ONE named
// seam rather than at "some read".
type failingViewData struct {
	api.ModuleData
	mu         sync.Mutex
	allow      int // this many Views succeed; the rest fail
	seen       int
	err        error
	failMutate bool
}

func (d *failingViewData) View(ctx context.Context, tenant model.TenantID, fn func(store.Scope) error) error {
	d.mu.Lock()
	d.seen++
	n := d.seen
	d.mu.Unlock()
	if n > d.allow {
		return d.err
	}
	return d.ModuleData.View(ctx, tenant, fn)
}

func (d *failingViewData) Mutate(ctx context.Context, tenant model.TenantID, fn func(store.Scope) error) error {
	if d.failMutate {
		// The transaction never opens, so the callback — and the guard inside it —
		// never runs.
		return d.err
	}
	return d.ModuleData.Mutate(ctx, tenant, fn)
}

// lockFailingData forwards everything but makes the writer lock fail, which is the
// last thing that can go wrong before the guard is reached inside the transaction.
type lockFailingData struct {
	api.ModuleData
	err error
}

func (d lockFailingData) Mutate(ctx context.Context, tenant model.TenantID, fn func(store.Scope) error) error {
	return d.ModuleData.Mutate(ctx, tenant, func(sc store.Scope) error {
		return fn(lockFailingScope{Scope: sc, err: d.err})
	})
}

func (d lockFailingData) View(ctx context.Context, tenant model.TenantID, fn func(store.Scope) error) error {
	return d.ModuleData.View(ctx, tenant, func(sc store.Scope) error {
		return fn(lockFailingScope{Scope: sc, err: d.err})
	})
}

type lockFailingScope struct {
	store.Scope
	err error
}

func (s lockFailingScope) LockTransaction(context.Context, string) error { return s.err }

// TestNoReservationRouteAdmitsBeforeInactiveIsConfirmed walks EVERY route that can
// answer before the guard has run, on a tenant that DOES carry a committed
// frontier. Every one of them must refuse.
//
// The frontier is present on purpose: these routes cannot see it, and that is
// precisely why they may not admit. A route that answers "allowed" here has granted
// admission across a boundary it never read.
func TestNoReservationRouteAdmitsBeforeInactiveIsConfirmed(t *testing.T) {
	readFailure := errors.New("finops-test: the policy census could not be read")

	newFixture := func(t *testing.T) (*Module, store.Store, model.TenantID) {
		t.Helper()
		m, st, tenant, _ := newLifecycleFin(t)
		createBudget(t, st, tenant, "global-block", budgetSpec{
			Dimension: "global", Period: "monthly", LimitMicroUSD: 10 * oneUSD, Action: "block",
		})
		// An enforcing identity budget and an enforcing agent_group budget, so the
		// admission path really issues the second and third Views.
		createBudget(t, st, tenant, "identity-block", budgetSpec{
			Dimension: "identity", Key: "id-1", Period: "monthly", LimitMicroUSD: 10 * oneUSD, Action: "block",
		})
		createBudget(t, st, tenant, "agent-group-block", budgetSpec{
			Dimension: "agent_group", Key: "g-1", Period: "monthly", LimitMicroUSD: 10 * oneUSD, Action: "block",
		})
		if _, err := m.BeginLifecycleActivation(context.Background(), tenant, LifecycleActivationRequest{
			Evidence: []EvidenceRef{labEvidence("quiescence")},
		}); err != nil {
			t.Fatalf("Begin: %v", err)
		}
		return m, st, tenant
	}

	dims := SpendDims{AgentRef: "agent-1"}

	for _, tc := range []struct {
		name string
		call func(t *testing.T, m *Module, tenant model.TenantID) (BudgetReservation, error)
	}{
		{"ReserveBudget: the budget census fails", func(t *testing.T, m *Module, tenant model.TenantID) (BudgetReservation, error) {
			m.UseData(&failingViewData{ModuleData: m.data, allow: 0, err: readFailure})
			return m.ReserveBudget(context.Background(), tenant, dims, oneUSD)
		}},
		{"ReserveBudget: the identity resolution fails", func(t *testing.T, m *Module, tenant model.TenantID) (BudgetReservation, error) {
			m.UseData(&failingViewData{ModuleData: m.data, allow: 1, err: readFailure})
			return m.ReserveBudget(context.Background(), tenant, dims, oneUSD)
		}},
		{"ReserveBudget: the agent-group resolution fails", func(t *testing.T, m *Module, tenant model.TenantID) (BudgetReservation, error) {
			m.UseData(&failingViewData{ModuleData: m.data, allow: 2, err: readFailure})
			return m.ReserveBudget(context.Background(), tenant, dims, oneUSD)
		}},
		{"ReserveBudget: the transaction cannot be opened", func(t *testing.T, m *Module, tenant model.TenantID) (BudgetReservation, error) {
			m.UseData(&failingViewData{ModuleData: m.data, allow: 99, failMutate: true, err: readFailure})
			return m.ReserveBudget(context.Background(), tenant, dims, oneUSD)
		}},
		{"ReserveBudget: the writer lock cannot be taken", func(t *testing.T, m *Module, tenant model.TenantID) (BudgetReservation, error) {
			m.UseData(lockFailingData{ModuleData: m.data, err: readFailure})
			return m.ReserveBudget(context.Background(), tenant, dims, oneUSD)
		}},
		{"ReserveSpendLimit: the policy list fails", func(t *testing.T, m *Module, tenant model.TenantID) (BudgetReservation, error) {
			m.UseData(&failingViewData{ModuleData: m.data, allow: 0, err: readFailure})
			return m.ReserveSpendLimit(context.Background(), tenant, "actor-a", nil, oneUSD)
		}},
		{"ReserveSpendLimit: the writer lock cannot be taken", func(t *testing.T, m *Module, tenant model.TenantID) (BudgetReservation, error) {
			createSpendLimitPolicy(t, m, tenant)
			m.UseData(lockFailingData{ModuleData: m.data, err: readFailure})
			return m.ReserveSpendLimit(context.Background(), tenant, "actor-a", nil, oneUSD)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, st, tenant := newFixture(t)
			res, err := tc.call(t, m, tenant)
			if res.Allowed {
				t.Fatalf("admitted without ever confirming this tenant has no frontier: %+v (err=%v)", res, err)
			}
			if err == nil {
				t.Fatalf("the refusal carries no error to tell a caller WHY: %+v", res)
			}
			if attemptCode(err) == "" {
				t.Fatalf("the refusal is untyped (%v); a caller cannot classify it", err)
			}
			if res.Handle != "" {
				t.Fatalf("a refused reservation returned a handle: %+v", res)
			}
			if n := len(countReservations(t, st, tenant)); n != 0 {
				t.Fatalf("%d reservation rows were written by a refused call", n)
			}
		})
	}
}

// createSpendLimitPolicy stores one per-actor spend limit so ReserveSpendLimit has a
// target to evaluate.
func createSpendLimitPolicy(t testing.TB, m *Module, tenant model.TenantID) {
	t.Helper()
	st := m.data.(finopsTestData).st
	if err := st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		_, err := sc.Policies().Create(context.Background(), model.Policy{
			Name: "seat", Kind: policyKindSpendLimit, Enabled: true,
			Spec: map[string]any{
				"scope_type": "user", "scope_key": "actor-a",
				"amount_micro_usd": int64(10 * oneUSD), "period": "monthly",
			},
		})
		return err
	}); err != nil {
		t.Fatalf("create spend limit: %v", err)
	}
}

// TestAPriorConfirmationIsNotThisRequestsConfirmation: a call that DID confirm the
// tenant inactive and admitted does not license the next call to admit when its own
// pre-transaction read fails. Confirmation is per attempt.
func TestAPriorConfirmationIsNotThisRequestsConfirmation(t *testing.T) {
	m, st, tenant, _ := newLifecycleFin(t)
	createBudget(t, st, tenant, "global-block", budgetSpec{
		Dimension: "global", Period: "monthly", LimitMicroUSD: 10 * oneUSD, Action: "block",
	})
	// First call: no frontier, confirmed inactive, admitted. This is the positive
	// control the correction must not break.
	first, err := m.ReserveBudget(context.Background(), tenant, SpendDims{}, oneUSD)
	if err != nil || !first.Allowed || first.Handle == "" {
		t.Fatalf("the confirmed-inactive admission regressed: %+v err=%v", first, err)
	}
	if err := m.ReleaseReservation(context.Background(), tenant, first.Handle); err != nil {
		t.Fatalf("release: %v", err)
	}

	// Second call on the same module: its own census read fails, so THIS attempt has
	// confirmed nothing.
	real := m.data
	m.UseData(&failingViewData{ModuleData: real, allow: 0, err: errors.New("finops-test: read failure")})
	res, err := m.ReserveBudget(context.Background(), tenant, SpendDims{}, oneUSD)
	if res.Allowed {
		t.Fatalf("the previous request's confirmation was reused as this one's: %+v (err=%v)", res, err)
	}
	if attemptCode(err) == "" {
		t.Fatalf("the refusal is untyped: %v", err)
	}
	m.UseData(real)

	// And with the read working again it admits exactly as before.
	third, err := m.ReserveBudget(context.Background(), tenant, SpendDims{}, oneUSD)
	if err != nil || !third.Allowed {
		t.Fatalf("the admission did not come back after the transient failure: %+v err=%v", third, err)
	}
}

// =============================================================================
// CORRECTION R2 — a first import only ever adopts a PENDING group
// =============================================================================

// TestAFirstImportRefusesATerminalOrMixedGroupBeforeAnyWrite is the return's
// counterexample made executable. A committed group of 7 was money the tenant had
// already been released from; adopting it turned every child back to active and
// cleared settled_at, so the hold came back from the dead — with an audit event
// saying an import happened, and no evidence that any obligation existed.
//
// The refusal must land BEFORE the parent, the children and the audit, so the three
// assertions after each case are the whole point.
func TestAFirstImportRefusesATerminalOrMixedGroupBeforeAnyWrite(t *testing.T) {
	for _, tc := range []struct {
		name   string
		states []string
	}{
		{"a wholly committed group", []string{resvStateCommitted}},
		{"a wholly released group", []string{resvStateReleased}},
		{"committed beside active", []string{resvStateActive, resvStateCommitted}},
		{"released beside expired", []string{resvStateExpired, resvStateReleased}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, st, tenant, _ := newLifecycleFin(t)
			bid := createBudget(t, st, tenant, "b", budgetSpec{
				Dimension: "global", Period: "monthly", LimitMicroUSD: 10 * oneUSD, Action: "block",
			})
			handle := model.NewID()
			var seed []model.Record
			for i, state := range tc.states {
				seed = append(seed, legacyChild(bid, handle, "seat-"+string(rune('a'+i)), "monthly",
					int64(i+1), 7*oneUSD, baseTime, baseTime.Add(time.Hour), state))
			}
			rows := seedGroup(t, st, tenant, seed...)
			auditBefore := len(auditActions(t, st, tenant))

			_, err := m.ImportLegacyHold(context.Background(), tenant, importFor(t, handle, rows))
			if attemptCode(err) != errCodeLegacyUnresolved {
				t.Fatalf("code = %q, want legacy_unresolved: a settled obligation is not importable (err=%v)", attemptCode(err), err)
			}
			if n := len(attemptRows(t, st, tenant)); n != 0 {
				t.Fatalf("%d attempt parents were written for a group that is not pending", n)
			}
			if got := len(auditActions(t, st, tenant)); got != auditBefore {
				t.Fatalf("the refused import wrote %d audit events", got-auditBefore)
			}
			for _, r := range countReservations(t, st, tenant) {
				if link, _ := linkageOf(r); link != linkageLegacy {
					t.Fatalf("child %s was linked by a refused import", r.String(model.ColID))
				}
				if r.String(colResvState) == resvStateActive && !containsString(tc.states, resvStateActive) {
					t.Fatalf("child %s was resurrected to active by a refused import", r.String(model.ColID))
				}
			}
		})
	}

	// The positive control the refusal must not swallow: active + expired is exactly
	// the pending shape a first import DOES adopt.
	t.Run("active beside expired still imports", func(t *testing.T) {
		m, st, tenant, _ := newLifecycleFin(t)
		bid := createBudget(t, st, tenant, "b", budgetSpec{
			Dimension: "global", Period: "monthly", LimitMicroUSD: 10 * oneUSD, Action: "block",
		})
		handle := model.NewID()
		rows := seedGroup(t, st, tenant,
			legacyChild(bid, handle, "", "monthly", 1, 4*oneUSD, baseTime, baseTime.Add(time.Hour), resvStateActive),
			legacyChild(bid, handle, "seat-a", "monthly", 2, 3*oneUSD, baseTime, baseTime.Add(time.Hour), resvStateExpired))
		req := importFor(t, handle, rows)
		view, err := m.ImportLegacyHold(context.Background(), tenant, req)
		if err != nil {
			t.Fatalf("the pending group no longer imports: %v", err)
		}
		// And the authorized exact replay still precedes the original-child OCC: the
		// versions the request names have moved, and it still resolves.
		replay, err := m.ImportLegacyHold(context.Background(), tenant, req)
		if err != nil || replay.AttemptRef != view.AttemptRef {
			t.Fatalf("the replay before the original-child OCC regressed: %+v err=%v", replay, err)
		}
	})
}

func containsString(all []string, want string) bool {
	for _, s := range all {
		if s == want {
			return true
		}
	}
	return false
}

// =============================================================================
// CORRECTION R3 — the census closes parents→children as well as children→parents
// =============================================================================

// TestBeginBlocksWhenAHeldParentHasLostItsChildren is the direction the first cut
// did not close. The census walked the reservation rows and validated each group it
// SAW; a parent whose only child was deleted owns no rows, so it was never visited
// and Begin certified a boundary over a ledger it had not reconciled.
//
// The hold reader already refused this state. Begin — which is what freezes the
// census a later activation is judged against — did not.
func TestBeginBlocksWhenAHeldParentHasLostItsChildren(t *testing.T) {
	m, st, tenant, _ := newLifecycleFin(t)
	bid := createBudget(t, st, tenant, "b", budgetSpec{
		Dimension: "global", Period: "monthly", LimitMicroUSD: 10 * oneUSD, Action: "block",
	})
	handle := model.NewID()
	rows := seedGroup(t, st, tenant,
		legacyChild(bid, handle, "", "monthly", 1, 7*oneUSD, baseTime, baseTime.Add(time.Hour), resvStateActive))
	if _, err := m.ImportLegacyHold(context.Background(), tenant, importFor(t, handle, rows)); err != nil {
		t.Fatalf("import: %v", err)
	}
	deleteReservation(t, st, tenant, mustID(t, rows[0].String(model.ColID)))

	_, err := m.BeginLifecycleActivation(context.Background(), tenant, LifecycleActivationRequest{
		Evidence: []EvidenceRef{labEvidence("quiescence")},
	})
	if attemptCode(err) != errCodeLegacyUnresolved {
		t.Fatalf("code = %q, want legacy_unresolved: a held parent with no live children is not a reconciled ledger (err=%v)", attemptCode(err), err)
	}
	if n := len(scopeRows(t, st, tenant)); n != 0 {
		t.Fatalf("a boundary was frozen over an unreconciled ledger (%d rows)", n)
	}
}

// TestBeginBlocksWhenALiveV1ChildContradictsItsParent covers the money/state half of
// the same closure: the group is present and its ids match, but a child's live
// amount no longer matches the immutable original, or a child is no longer active
// under a held parent. Cardinality alone does not establish a coherent hold.
func TestBeginBlocksWhenALiveV1ChildContradictsItsParent(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(model.Record)
	}{
		{"a mutated live amount", func(r model.Record) { r[colResvAmount] = int64(1) }},
		{"a child settled out from under a held parent", func(r model.Record) { r[colResvState] = resvStateCommitted }},
		{"a child re-pointed at another policy", func(r model.Record) { r[colResvPolicyRef] = model.NewID().String() }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, st, tenant, _ := newLifecycleFin(t)
			bid := createBudget(t, st, tenant, "b", budgetSpec{
				Dimension: "global", Period: "monthly", LimitMicroUSD: 10 * oneUSD, Action: "block",
			})
			handle := model.NewID()
			rows := seedGroup(t, st, tenant,
				legacyChild(bid, handle, "", "monthly", 1, 7*oneUSD, baseTime, baseTime.Add(time.Hour), resvStateActive))
			if _, err := m.ImportLegacyHold(context.Background(), tenant, importFor(t, handle, rows)); err != nil {
				t.Fatalf("import: %v", err)
			}
			mutateReservation(t, st, tenant, mustID(t, rows[0].String(model.ColID)), tc.edit)

			_, err := m.BeginLifecycleActivation(context.Background(), tenant, LifecycleActivationRequest{
				Evidence: []EvidenceRef{labEvidence("quiescence")},
			})
			if attemptCode(err) != errCodeLegacyUnresolved {
				t.Fatalf("code = %q, want legacy_unresolved (err=%v)", attemptCode(err), err)
			}
			if n := len(scopeRows(t, st, tenant)); n != 0 {
				t.Fatalf("a boundary was frozen over a contradictory group (%d rows)", n)
			}
		})
	}

	// The positive control: an untouched imported group, whose child VERSION has
	// legitimately advanced (the import itself moved it), does not block Begin.
	t.Run("a legitimately advanced version does not block", func(t *testing.T) {
		m, st, tenant, _ := newLifecycleFin(t)
		bid := createBudget(t, st, tenant, "b", budgetSpec{
			Dimension: "global", Period: "monthly", LimitMicroUSD: 10 * oneUSD, Action: "block",
		})
		handle := model.NewID()
		rows := seedGroup(t, st, tenant,
			legacyChild(bid, handle, "", "monthly", 1, 7*oneUSD, baseTime, baseTime.Add(time.Hour), resvStateActive))
		imported, err := m.ImportLegacyHold(context.Background(), tenant, importFor(t, handle, rows))
		if err != nil {
			t.Fatalf("import: %v", err)
		}
		before, _ := int64Cell(rows[0], model.ColVersion)
		after, _ := int64Cell(reservationByID(t, st, tenant, mustID(t, rows[0].String(model.ColID))), model.ColVersion)
		if after <= before {
			t.Fatalf("the import did not advance the child version (%d -> %d); the control would be vacuous", before, after)
		}
		view, err := m.BeginLifecycleActivation(context.Background(), tenant, LifecycleActivationRequest{
			Evidence: []EvidenceRef{labEvidence("quiescence")},
		})
		if err != nil {
			t.Fatalf("Begin over a coherent imported group: %v", err)
		}
		if view.PendingGroupCount != 0 {
			t.Fatalf("pending groups = %d, want 0: an imported group is not legacy pending", view.PendingGroupCount)
		}
		// AND THE PARENT CONTRIBUTED NO MONEY. The held total is exactly the one
		// child's amount: the parent is metadata, and a census that counted it would
		// have doubled the obligation it exists to describe.
		if got := reservedFor(t, m, st, tenant, bid, ""); !got.established() || got.MicroUSD != 7*oneUSD {
			t.Fatalf("held after a successful Begin = %+v, want exactly the child's 7 USD", got)
		}
		if _, gerr := m.GetAttempt(context.Background(), tenant, imported.AttemptRef); gerr != nil {
			t.Fatalf("the parent stopped being readable: %v", gerr)
		}
	})
}

// =============================================================================
// CORRECTION R4b — the frontier document must prove its own census
// =============================================================================

// TestACorruptedFrontierEntryIsRefusedRatherThanTrusted: the stored boundary carries
// the pending set, its count, its digest and the frontier digest that binds them.
// Editing one entry's group digest — the edit that would let an ALTERED group pass
// the import membership check — must invalidate the document, not travel with it.
func TestACorruptedFrontierEntryIsRefusedRatherThanTrusted(t *testing.T) {
	f := newFrontierFixture(t)
	f.begin(t)

	// Alter the pending entry's group digest, leaving every other stored field —
	// including the frontier digest — exactly as Begin wrote it.
	mutateFrontierDocument(t, f.st, f.t1, func(doc *jsonFrontier) {
		if len(doc.PendingGroups) != 1 {
			t.Fatalf("the fixture no longer has exactly one pending group")
		}
		doc.PendingGroups[0].GroupDigest = string(canonDigest("olivares.finops.lab-forgery",
			func(w *canonWriter) { w.str("a digest for a group nobody censused") }))
	})

	// Every reader of the boundary must refuse it.
	_, _, err := readScopeThrough(t, f.st, f.t1)
	if attemptCode(err) != errCodeLedgerIndeterminate {
		t.Fatalf("frontier lookup: code = %q, want ledger_indeterminate (err=%v)", attemptCode(err), err)
	}
	res, rerr := f.m.ReserveBudget(context.Background(), f.t1, SpendDims{}, oneUSD)
	if res.Allowed {
		t.Fatalf("a covered wrapper admitted against an unreadable boundary: %+v", res)
	}
	if attemptCode(rerr) != errCodeLedgerIndeterminate {
		t.Fatalf("guard: code = %q, want ledger_indeterminate (err=%v)", attemptCode(rerr), rerr)
	}
	_, ierr := f.m.ImportLegacyHold(context.Background(), f.t1, importFor(t, f.pending, f.rows))
	if attemptCode(ierr) != errCodeLedgerIndeterminate {
		t.Fatalf("import: code = %q, want ledger_indeterminate (err=%v)", attemptCode(ierr), ierr)
	}
}

// mutateFrontierDocument edits the stored census document in place, keeping the row
// otherwise untouched.
func mutateFrontierDocument(t testing.TB, st store.Store, tenant model.TenantID, edit func(*jsonFrontier)) {
	t.Helper()
	if err := st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(lifecycleScopeKind)
		if err != nil {
			return err
		}
		rows, _, err := repo.List(context.Background(), model.Query{Limit: 2})
		if err != nil {
			return err
		}
		if len(rows) != 1 {
			t.Fatalf("%d frontier rows", len(rows))
		}
		var doc jsonFrontier
		if uerr := strictUnmarshal(rows[0].String(colScopeFrontier), &doc); uerr != nil {
			return uerr
		}
		edit(&doc)
		body, merr := marshalBounded(doc, maxFrontierPayloadBytes, "frontier")
		if merr != nil {
			return merr
		}
		rows[0][colScopeFrontier] = body
		_, err = repo.Update(context.Background(), rows[0])
		return err
	}); err != nil {
		t.Fatalf("mutate frontier document: %v", err)
	}
}

// =============================================================================
// CORRECTION R5b — the real alert caller, with a late occurred_at
// =============================================================================

// TestALateCostSampleCannotBeAlertedAgainstAnUndatedHold exercises the CALLER, not
// the helper. Ingestion evaluates budgets for the period of the sample's
// occurred_at, so a cost that arrives late is evaluated against a window that has
// already closed. An imported hold whose historical accounting instant was never
// established cannot be allocated to that window — but the first cut counted it in
// full and published an exact effective amount, which is how 4 of real cost plus 7
// of undated hold crossed a 10 limit that nothing proves it crossed.
//
// Current and future admission stay conservative: the same 7 is still held in full
// there, and the second half of this case says so.
func TestALateCostSampleCannotBeAlertedAgainstAnUndatedHold(t *testing.T) {
	m, st, tenant, _ := newLifecycleFin(t)
	bid := createBudget(t, st, tenant, "monthly-block", budgetSpec{
		Dimension: "global", Period: "monthly", LimitMicroUSD: 10 * oneUSD, Action: "block",
		Thresholds: []float64{1.0},
	})
	handle := model.NewID()
	rows := seedGroup(t, st, tenant,
		legacyChild(bid, handle, "", "monthly", 1, 7*oneUSD, baseTime, baseTime.Add(time.Hour), resvStateActive))
	if _, err := m.ImportLegacyHold(context.Background(), tenant, importFor(t, handle, rows)); err != nil {
		t.Fatalf("import: %v", err)
	}

	// A cost of 4 that OCCURRED two months ago and is ingested now.
	late := baseTime.AddDate(0, -2, 0)
	m.ingest(t, tenant, mkCost("openai", "gpt", "s1", 1, 1, 4*oneUSD, late))

	// The evaluation of that closed window must not publish an exact figure that
	// rests on the undated hold.
	pStart, hasLower := periodStart("monthly", late)
	var eval budgetEvaluation
	if err := st.View(context.Background(), tenant, func(sc store.Scope) error {
		p, gerr := sc.Policies().Get(context.Background(), bid)
		if gerr != nil {
			return gerr
		}
		spec := parseBudgetSpec(p.Spec)
		spec.fillDefaults()
		var eerr error
		eval, eerr = evaluateBudgetAmount(context.Background(), sc, p, spec, late, m.clock.Now().Time())
		return eerr
	}); err != nil {
		t.Fatalf("evaluate the historical window: %v", err)
	}
	_ = hasLower
	if eval.PeriodStart != pStart {
		t.Fatalf("the evaluation did not select the sample's own period: %v vs %v", eval.PeriodStart, pStart)
	}
	if eval.Amount.Class == amountExact {
		t.Fatalf("an exact effective amount was published for a closed window that includes an undated hold: %+v", eval.Amount)
	}
	if eval.Dynamic.State == dynamicKnown {
		t.Fatalf("the undated hold was allocated to a closed historical window as a known %d", eval.Dynamic.MicroUSD)
	}

	// Current admission is unchanged: the same obligation is held in full.
	if got := reservedFor(t, m, st, tenant, bid, ""); !got.established() || got.MicroUSD != 7*oneUSD {
		t.Fatalf("current admission lost the conservative hold: %+v", got)
	}
}

// =============================================================================
// RESIDUAL R1 — per-attempt state is reset BEFORE the transaction is opened
// =============================================================================

// retryThenFailToOpenData is the exact sequence the residual return names, staged
// in one call: the FIRST attempt really opens, really takes the lock and really
// confirms the tenant has no frontier, and is then lost to a seq conflict; the
// SECOND attempt fails to open at all, so its callback — and the guard inside it —
// never runs.
//
// Nothing is faked: the conflict comes from the reservation repository's own
// Create, and the opening failure is the store handle refusing to start a
// transaction, which is what an unavailable store does.
type retryThenFailToOpenData struct {
	api.ModuleData
	mu       sync.Mutex
	opens    int
	openErr  error
	callback int
	// succeedOnRetry makes the second attempt open normally, which is the
	// legitimate-retry positive control.
	succeedOnRetry bool
}

func (d *retryThenFailToOpenData) Mutate(ctx context.Context, tenant model.TenantID, fn func(store.Scope) error) error {
	d.mu.Lock()
	d.opens++
	n := d.opens
	d.mu.Unlock()
	if n == 1 {
		return d.ModuleData.Mutate(ctx, tenant, func(sc store.Scope) error {
			d.mu.Lock()
			d.callback++
			d.mu.Unlock()
			return fn(conflictingCreateScope{Scope: sc})
		})
	}
	if d.succeedOnRetry {
		return d.ModuleData.Mutate(ctx, tenant, func(sc store.Scope) error {
			d.mu.Lock()
			d.callback++
			d.mu.Unlock()
			return fn(sc)
		})
	}
	// The transaction does not open. fn is never called.
	return d.openErr
}

func (d *retryThenFailToOpenData) callbacks() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.callback
}

// conflictingCreateScope lets everything through until the reservation INSERT,
// which loses the seq race — the one failure the reserve loop is built to retry.
type conflictingCreateScope struct{ store.Scope }

func (s conflictingCreateScope) LockTransaction(ctx context.Context, key string) error {
	locker, ok := s.Scope.(store.TransactionLocker)
	if !ok {
		return errors.New("finops-test: wrapped scope provides no transaction lock")
	}
	return locker.LockTransaction(ctx, key)
}

func (s conflictingCreateScope) TransactionNow(ctx context.Context) (model.Timestamp, error) {
	clock, ok := s.Scope.(store.TransactionClock)
	if !ok {
		return model.Timestamp{}, errors.New("finops-test: wrapped scope provides no transaction clock")
	}
	return clock.TransactionNow(ctx)
}

func (s conflictingCreateScope) Ext(kind model.Kind) (store.GenericRepo, error) {
	repo, err := s.Scope.Ext(kind)
	if err != nil || kind != budgetReservationKind {
		return repo, err
	}
	return conflictingCreateRepo{GenericRepo: repo}, nil
}

type conflictingCreateRepo struct{ store.GenericRepo }

func (conflictingCreateRepo) Create(context.Context, model.Record) (model.Record, error) {
	return nil, fmt.Errorf("finops-test: lost the seq race: %w", store.ErrConflict)
}

// TestAConfirmationDoesNotSurviveARetryWhoseTransactionNeverOpens is the residual
// R1 control, and the sequence is INSIDE ONE CALL.
//
// The first attempt confirms the tenant has no frontier and is then lost to a seq
// conflict, so the loop retries — correctly. The second attempt never opens, so it
// confirms nothing; a frontier may have been committed in between. The confirmation
// flag lived outside the loop and was cleared only inside the callback, so it was
// still true when the callback did not run, and the call returned Allowed:true with
// an untyped error. That is an admission issued by an attempt that never looked.
func TestAConfirmationDoesNotSurviveARetryWhoseTransactionNeverOpens(t *testing.T) {
	openFailure := fmt.Errorf("finops-test: the transaction could not be opened: %w", store.ErrStoreUnavailable)

	m, st, tenant, _ := newLifecycleFin(t)
	createBudget(t, st, tenant, "global-block", budgetSpec{
		Dimension: "global", Period: "monthly", LimitMicroUSD: 10 * oneUSD, Action: "block",
	})
	data := &retryThenFailToOpenData{ModuleData: m.data, openErr: openFailure}
	m.UseData(data)

	res, err := m.ReserveBudget(context.Background(), tenant, SpendDims{}, oneUSD)
	if data.callbacks() != 1 {
		t.Fatalf("the fixture ran %d callbacks, want exactly the first attempt's: the sequence is not staged", data.callbacks())
	}
	if res.Allowed {
		t.Fatalf("an attempt whose transaction never opened inherited the previous attempt's confirmation: %+v (err=%v)", res, err)
	}
	if err == nil || attemptCode(err) == "" {
		t.Fatalf("the refusal is untyped (%v); a caller cannot classify it", err)
	}
	if !errors.Is(err, store.ErrStoreUnavailable) {
		t.Fatalf("the store cause lost its identity: errors.Is(store.ErrStoreUnavailable) is false (%v)", err)
	}
	if res.Handle != "" {
		t.Fatalf("a refused reservation returned handle %q", res.Handle)
	}
	if n := len(countReservations(t, st, tenant)); n != 0 {
		t.Fatalf("%d reservation rows survived a refused reservation", n)
	}
}

// TestALegitimateRetryStillSucceeds is the positive half: the same first-attempt
// conflict, and a second attempt that DOES open, must still admit. The correction
// must reset per-attempt state, not stop retrying.
func TestALegitimateRetryStillSucceeds(t *testing.T) {
	m, st, tenant, _ := newLifecycleFin(t)
	createBudget(t, st, tenant, "global-block", budgetSpec{
		Dimension: "global", Period: "monthly", LimitMicroUSD: 10 * oneUSD, Action: "block",
	})
	data := &retryThenFailToOpenData{ModuleData: m.data, succeedOnRetry: true}
	m.UseData(data)

	res, err := m.ReserveBudget(context.Background(), tenant, SpendDims{}, oneUSD)
	if err != nil || !res.Allowed || res.Handle == "" {
		t.Fatalf("a legitimate retry after a seq conflict no longer admits: %+v err=%v", res, err)
	}
	if data.callbacks() < 2 {
		t.Fatalf("the fixture ran %d callbacks; the case did not exercise a retry", data.callbacks())
	}
	if n := len(countReservations(t, st, tenant)); n != 1 {
		t.Fatalf("%d reservation rows, want the one the retry wrote", n)
	}
}

// failToOpenData refuses to open ANY transaction, so no callback and no guard ever
// runs. It is the settlement/sweep half of the same rule.
type failToOpenData struct {
	api.ModuleData
	err  error
	runs int
}

func (d *failToOpenData) Mutate(context.Context, model.TenantID, func(store.Scope) error) error {
	d.runs++
	return d.err
}

// postGuardFailureData preserves the real store transaction, writer lock and
// frontier read. Only the reservation repository operation after that guard fails.
type postGuardFailureData struct {
	api.ModuleData
	probe *orderProbe
	stage string
	cause error
	hits  *int
}

func (d postGuardFailureData) Mutate(ctx context.Context, tenant model.TenantID, fn func(store.Scope) error) error {
	return d.ModuleData.Mutate(ctx, tenant, func(sc store.Scope) error {
		return fn(postGuardFailureScope{orderProbeScope: orderProbeScope{Scope: sc, p: d.probe}, stage: d.stage, cause: d.cause, hits: d.hits})
	})
}

type postGuardFailureScope struct {
	orderProbeScope
	stage string
	cause error
	hits  *int
}

func (s postGuardFailureScope) Ext(kind model.Kind) (store.GenericRepo, error) {
	repo, err := s.orderProbeScope.Ext(kind)
	if err != nil || kind != budgetReservationKind {
		return repo, err
	}
	if s.stage == "repository" {
		*s.hits++
		return nil, s.cause
	}
	return postGuardFailureRepo{GenericRepo: repo, cause: s.cause, hits: s.hits}, nil
}

type postGuardFailureRepo struct {
	store.GenericRepo
	cause error
	hits  *int
}

func (r postGuardFailureRepo) List(context.Context, model.Query) ([]model.Record, model.Page, error) {
	*r.hits++
	return nil, model.Page{}, r.cause
}

// TestSettlementAndSweepOpeningFailuresAreTypedRefusals: Commit, Release and Sweep
// returned the store's raw error when the TRANSACTION ITSELF could not be opened.
// Nothing was written and nothing succeeded, so this is not a monetary defect — but
// a caller cannot classify it, and these are three of the five covered wrappers
// whose refusals are supposed to share one vocabulary. The store cause keeps its
// identity through errors.Is.
func TestSettlementAndSweepOpeningFailuresAreTypedRefusals(t *testing.T) {
	openFailure := fmt.Errorf("finops-test: the transaction could not be opened: %w", store.ErrStoreUnavailable)

	t.Run("CommitReservation", func(t *testing.T) {
		m, _, tenant, _ := newLifecycleFin(t)
		m.UseData(&failToOpenData{ModuleData: m.data, err: openFailure})
		err := m.CommitReservation(context.Background(), tenant, model.NewID().String(), oneUSD)
		requireTypedOpeningRefusal(t, err)
	})
	t.Run("ReleaseReservation", func(t *testing.T) {
		m, _, tenant, _ := newLifecycleFin(t)
		m.UseData(&failToOpenData{ModuleData: m.data, err: openFailure})
		err := m.ReleaseReservation(context.Background(), tenant, model.NewID().String())
		requireTypedOpeningRefusal(t, err)
	})
	t.Run("SweepExpiredReservations", func(t *testing.T) {
		m, _, tenant, _ := newLifecycleFin(t)
		m.UseData(&failToOpenData{ModuleData: m.data, err: openFailure})
		n, err := m.SweepExpiredReservations(context.Background(), tenant)
		requireTypedOpeningRefusal(t, err)
		if n != 0 {
			t.Fatalf("a refused sweep reported %d swept", n)
		}
	})

	// AND THE POSTURES AFTER A CONFIRMED-INACTIVE GUARD ARE UNCHANGED. These are the
	// same three calls on a working store with no frontier: their adjudicated
	// results — ErrNotFound for a handle that never existed, a real sweep count —
	// are not reclassified by this correction.
	t.Run("the confirmed-inactive postures are untouched", func(t *testing.T) {
		m, st, tenant, _ := newLifecycleFin(t)
		createBudget(t, st, tenant, "b", budgetSpec{
			Dimension: "global", Period: "monthly", LimitMicroUSD: 10 * oneUSD, Action: "block",
		})
		if err := m.CommitReservation(context.Background(), tenant, model.NewID().String(), oneUSD); err != store.ErrNotFound {
			t.Fatalf("commit of an absent handle = %v, want store.ErrNotFound", err)
		}
		if err := m.ReleaseReservation(context.Background(), tenant, model.NewID().String()); err != store.ErrNotFound {
			t.Fatalf("release of an absent handle = %v, want store.ErrNotFound", err)
		}
		res, err := m.ReserveBudget(context.Background(), tenant, SpendDims{}, oneUSD)
		if err != nil || !res.Allowed {
			t.Fatalf("reserve on a confirmed-inactive tenant: %+v err=%v", res, err)
		}
		if err := m.CommitReservation(context.Background(), tenant, res.Handle, oneUSD); err != nil {
			t.Fatalf("commit of a live handle: %v", err)
		}
		if n, err := m.SweepExpiredReservations(context.Background(), tenant); err != nil || n != 0 {
			t.Fatalf("sweep on a confirmed-inactive tenant: swept=%d err=%v", n, err)
		}
	})

	for _, operation := range []struct {
		name string
		run  func(*Module, model.TenantID) error
	}{
		{"commit", func(m *Module, tenant model.TenantID) error {
			return m.CommitReservation(context.Background(), tenant, model.NewID().String(), oneUSD)
		}},
		{"release", func(m *Module, tenant model.TenantID) error {
			return m.ReleaseReservation(context.Background(), tenant, model.NewID().String())
		}},
		{"sweep", func(m *Module, tenant model.TenantID) error {
			_, err := m.SweepExpiredReservations(context.Background(), tenant)
			return err
		}},
	} {
		for _, stage := range []string{"repository", "list"} {
			t.Run(operation.name+" preserves post-guard "+stage+" failure", func(t *testing.T) {
				m, st, tenant, _ := newLifecycleFin(t)
				cause := errors.New("finops-test: failure after an inactive frontier was read")
				probe := &orderProbe{}
				hits := 0
				m.UseData(postGuardFailureData{ModuleData: m.data, probe: probe, stage: stage, cause: cause, hits: &hits})
				err := operation.run(m, tenant)
				if hits != 1 || !containsString(probe.seen(), "frontier-read") {
					t.Fatalf("fixture did not reach the post-guard failure: hits=%d events=%v", hits, probe.seen())
				}
				if err != cause || attemptCode(err) != "" {
					t.Fatalf("post-guard error was reclassified: got %T %v (code=%q), want original cause", err, err, attemptCode(err))
				}
				if n := len(countReservations(t, st, tenant)); n != 0 {
					t.Fatalf("failed operation left %d reservation rows", n)
				}
			})
		}
	}
}

func requireTypedOpeningRefusal(t testing.TB, err error) {
	t.Helper()
	if attemptCode(err) != errCodeStoreUnavailable {
		t.Fatalf("code = %q, want store_unavailable (err=%v)", attemptCode(err), err)
	}
	if !errors.Is(err, store.ErrStoreUnavailable) {
		t.Fatalf("the store cause lost its identity through the typed wrapper: %v", err)
	}
}

// =============================================================================
// RESIDUAL R3/R4 — the live attribution is compared against the WHOLE original
// =============================================================================

// TestEveryImmutablePolicyFactOfAChildIsCheckedAgainstItsOriginal. The previous
// correction compared policy_ref, dim_key, period_start, handle, state and amount.
// The reservation row also carries policy_kind, period and a NULLABLE dimension,
// and those are historical facts of the imported obligation too: a child that was
// a monthly budget target is not a total one, and a dimension the history never
// recorded is not one it recorded as empty. None of them is a transition any
// operation of this cut can authorize, so a live row that disagrees with the
// immutable snapshot is a contradiction — and it must stop Begin AND the hold
// reader, not just one of them.
//
// The child VERSION is deliberately not compared: the import itself advances it.
// No current policy is resolved; every expectation comes from the snapshot.
func TestEveryImmutablePolicyFactOfAChildIsCheckedAgainstItsOriginal(t *testing.T) {
	for _, tc := range []struct {
		name        string
		originalNil bool // the original row's dimension cell is absent
		edit        func(model.Record)
	}{
		{"policy_kind", false, func(r model.Record) { r[colResvPolicyKind] = policyKindSpendLimit }},
		{"period", false, func(r model.Record) { r[colResvPeriod] = "total" }},
		{"dimension value", false, func(r model.Record) { r[colResvDimension] = "provider" }},
		{"dimension removed where history recorded one", false, func(r model.Record) { delete(r, colResvDimension) }},
		{"dimension added where history recorded none", true, func(r model.Record) { r[colResvDimension] = "" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, st, tenant, _ := newLifecycleFin(t)
			bid := createBudget(t, st, tenant, "b", budgetSpec{
				Dimension: "global", Period: "monthly", LimitMicroUSD: 10 * oneUSD, Action: "block",
			})
			handle := model.NewID()
			seed := legacyChild(bid, handle, "", "monthly", 1, 7*oneUSD, baseTime, baseTime.Add(time.Hour), resvStateActive)
			if tc.originalNil {
				delete(seed, colResvDimension)
			}
			rows := seedGroup(t, st, tenant, seed)
			if _, err := m.ImportLegacyHold(context.Background(), tenant, importFor(t, handle, rows)); err != nil {
				t.Fatalf("import: %v", err)
			}
			// The hold is coherent before the edit.
			if got := reservedFor(t, m, st, tenant, bid, ""); !got.established() || got.MicroUSD != 7*oneUSD {
				t.Fatalf("held before the edit = %+v", got)
			}

			mutateReservation(t, st, tenant, mustID(t, rows[0].String(model.ColID)), tc.edit)

			if got := reservedFor(t, m, st, tenant, bid, ""); got.established() {
				t.Fatalf("the hold reader published an established %d over a child whose %s contradicts its original",
					got.MicroUSD, tc.name)
			}
			_, err := m.BeginLifecycleActivation(context.Background(), tenant, LifecycleActivationRequest{
				Evidence: []EvidenceRef{labEvidence("quiescence")},
			})
			if attemptCode(err) != errCodeLegacyUnresolved {
				t.Fatalf("Begin: code = %q, want legacy_unresolved (err=%v)", attemptCode(err), err)
			}
			if n := len(scopeRows(t, st, tenant)); n != 0 {
				t.Fatalf("a boundary was frozen over a contradictory child (%d rows)", n)
			}
		})
	}

	// The positive control the five refusals must not swallow: an untouched import,
	// including one whose original dimension is NULL, stays coherent for both
	// readers even though its child version has legitimately advanced.
	for _, nilDim := range []bool{false, true} {
		name := "an untouched import stays coherent"
		if nilDim {
			name += " with a NULL original dimension"
		}
		t.Run(name, func(t *testing.T) {
			m, st, tenant, _ := newLifecycleFin(t)
			bid := createBudget(t, st, tenant, "b", budgetSpec{
				Dimension: "global", Period: "monthly", LimitMicroUSD: 10 * oneUSD, Action: "block",
			})
			handle := model.NewID()
			seed := legacyChild(bid, handle, "", "monthly", 1, 7*oneUSD, baseTime, baseTime.Add(time.Hour), resvStateActive)
			if nilDim {
				delete(seed, colResvDimension)
			}
			rows := seedGroup(t, st, tenant, seed)
			if _, err := m.ImportLegacyHold(context.Background(), tenant, importFor(t, handle, rows)); err != nil {
				t.Fatalf("import: %v", err)
			}
			if got := reservedFor(t, m, st, tenant, bid, ""); !got.established() || got.MicroUSD != 7*oneUSD {
				t.Fatalf("held = %+v, want an established 7 USD", got)
			}
			if _, err := m.BeginLifecycleActivation(context.Background(), tenant, LifecycleActivationRequest{
				Evidence: []EvidenceRef{labEvidence("quiescence")},
			}); err != nil {
				t.Fatalf("Begin over a coherent imported group: %v", err)
			}
		})
	}
}

// =============================================================================
// RESIDUAL R4 — the stored Begin request is validated by its own creation facts
// =============================================================================

// TestAMutatedStoredBeginRequestCannotBecomeTheReplayIdentity. The frontier
// document keeps the canonical original Begin request so an uncertain
// acknowledgement can be resolved by repeating it. The commitments covered the
// census; they did not cover that request, so editing ONE field of it — the
// ExpectedVersion, from the 0 a first Begin necessarily carries, to 1 — left every
// digest, instant and evidence intact and the document readable. The consequence is
// not a new frontier or a grant: it is that the GENUINE original request now
// conflicts while a request nobody made replays successfully. The durable identity
// of the Begin has been replaced.
//
// Both derivable facts are checked: a first Begin's ExpectedVersion is zero, and
// its evidence is the evidence the same document preserves. No new digest and no
// contract change is needed to say either.
func TestAMutatedStoredBeginRequestCannotBecomeTheReplayIdentity(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*jsonFrontier)
	}{
		{"the expected version", func(doc *jsonFrontier) { doc.OriginalRequest.ExpectedVersion = 1 }},
		{"the request evidence", func(doc *jsonFrontier) {
			doc.OriginalRequest.Evidence = encodeEvidenceRefs([]EvidenceRef{labEvidence("evidence-nobody-presented")})
		}},
		{"the request evidence dropped", func(doc *jsonFrontier) {
			doc.OriginalRequest.Evidence = encodeEvidenceRefs(nil)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFrontierFixture(t)
			f.begin(t)
			mutateFrontierDocument(t, f.st, f.t1, tc.edit)

			// The shared decoder refuses the document, so every reader of it does.
			if _, _, err := readScopeThrough(t, f.st, f.t1); attemptCode(err) != errCodeLedgerIndeterminate {
				t.Fatalf("frontier lookup: code = %q, want ledger_indeterminate (err=%v)", attemptCode(err), err)
			}
			res, rerr := f.m.ReserveBudget(context.Background(), f.t1, SpendDims{}, oneUSD)
			if res.Allowed || attemptCode(rerr) != errCodeLedgerIndeterminate {
				t.Fatalf("guard: allowed=%v code=%q", res.Allowed, attemptCode(rerr))
			}
			// And the replay: the genuine original request must NOT be answered by a
			// forged identity, in either direction.
			_, berr := f.m.BeginLifecycleActivation(context.Background(), f.t1, LifecycleActivationRequest{
				Evidence: []EvidenceRef{labEvidence("quiescence"), labEvidence("caller-readiness")},
			})
			if attemptCode(berr) != errCodeLedgerIndeterminate {
				t.Fatalf("replay of the genuine request: code = %q, want ledger_indeterminate (err=%v)", attemptCode(berr), berr)
			}
		})
	}

	// The positive control: an untouched boundary still replays exactly, and still
	// conflicts for a different request.
	t.Run("the genuine replay is preserved", func(t *testing.T) {
		f := newFrontierFixture(t)
		first := f.begin(t)
		replay := f.begin(t)
		if replay.ID != first.ID || replay.FrontierDigest != first.FrontierDigest {
			t.Fatalf("the genuine replay changed: %+v vs %+v", replay, first)
		}
		if _, err := f.m.BeginLifecycleActivation(context.Background(), f.t1, LifecycleActivationRequest{
			Evidence: []EvidenceRef{labEvidence("a-different-claim")},
		}); attemptCode(err) != errCodeAttemptIdentityConflict {
			t.Fatalf("a different request: code = %q, want attempt_identity_conflict", attemptCode(err))
		}
	})
}
