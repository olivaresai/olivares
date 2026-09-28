// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package finops

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// -----------------------------------------------------------------------------
// RECOVERY. A caller that stops between its claim and its publication, or whose give-back
// fails, leaves a pending row that still names its hold and every hold it owed. Nobody
// else will settle that money: the recovery pass retires such a claim once it is stale —
// its rows released, the row released naming nothing — well before the rows lapse. It
// never writes a claim still in flight, never writes a published hold, and never writes
// an actual amount. A row it cannot identify is counted and left as it is.
// -----------------------------------------------------------------------------

// recoverOK runs one recovery pass and fails the test on an error.
func recoverOK(t *testing.T, m *Module, tenant model.TenantID) AdmissionRecovery {
	t.Helper()
	rep, err := m.RecoverAdmissions(context.Background(), tenant)
	if err != nil {
		t.Fatalf("recover: %v", err)
	}
	return rep
}

// reconcileOK runs one reconciliation and fails the test on an error.
func reconcileOK(t *testing.T, m *Module, tenant model.TenantID) AdmissionReconciliation {
	t.Helper()
	rep, err := m.ReconcileReservations(context.Background(), tenant)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	return rep
}

// noStop is want as a module that states no stop of the earlier writers reports it.
func noStop(want AdmissionRecovery) AdmissionRecovery {
	want.LegacyStop = string(legacyStopAbsent)
	return want
}

// assertRecovery asserts one pass's report.
func assertRecovery(t *testing.T, what string, got, want AdmissionRecovery) {
	t.Helper()
	if got != want {
		t.Fatalf("%s reported %+v, want %+v", what, got, want)
	}
}

// assertRetired asserts that the row of key was retired: released, naming nothing and
// owing nothing.
func assertRetired(t *testing.T, m *Module, tenant model.TenantID, key string) {
	t.Helper()
	row := admissionRowOf(t, m, tenant, key)
	if row.state != admStateReleased || !row.handle.isZero() || !row.spendHandle.isZero() || len(row.owed) != 0 || row.owedErr != nil {
		t.Fatalf("the row of %s is %s naming %q/%q owing %v (%v), want released naming nothing",
			key, row.state, row.handle, row.spendHandle, row.owed, row.owedErr)
	}
}

// stopAfter makes the marked caller stop writing after its nth write: every later write
// fails before its transaction opens, as if its process had stopped there.
func stopAfter(d api.ModuleData, n int64) *refusedWritesData {
	return &refusedWritesData{ModuleData: d, from: n + 1, to: 1 << 40}
}

// stagedAfterCreate runs a Reserve of req whose caller stops right after its create, and
// returns the claim it leaves: pending, naming the hold whose rows the create inserted.
func stagedAfterCreate(t *testing.T, m *Module, tenant model.TenantID, req AdmissionRequest) admissionRow {
	t.Helper()
	base := m.data
	m.data = stopAfter(base, 2)
	res, err := m.Reserve(pausedCtx(context.Background()), tenant, req)
	m.data = base
	if err != nil || res.Allowed || res.Handle != "" {
		t.Fatalf("a caller that stopped after its create was answered %+v err=%v; want a refusal", res, err)
	}
	claim := admissionRowOf(t, m, tenant, req.IdempotencyKey)
	if claim.state != admStatePending || claim.handle.isZero() || claim.version != 2 {
		t.Fatalf("the row is %s at version %d naming %q, want the claim its create left", claim.state, claim.version, claim.handle)
	}
	return claim
}

// recoveryOutcome is the result of a pass run on another goroutine.
type recoveryOutcome struct {
	rep AdmissionRecovery
	err error
}

// awaitRecovery waits for a pass run on another goroutine.
func awaitRecovery(t *testing.T, done <-chan recoveryOutcome) recoveryOutcome {
	t.Helper()
	select {
	case got := <-done:
		return got
	case <-time.After(10 * time.Second):
		t.Fatal("the paused pass never finished")
	}
	return recoveryOutcome{}
}

// beginFrontier commits the tenant's activation frontier, whose census gives every
// pending legacy group of the ledger as it stands to the attempt lifecycle.
func beginFrontier(t *testing.T, m *Module, tenant model.TenantID) {
	t.Helper()
	WithAttemptEvidenceVerifier(newLabVerifier(tenant))(m)
	if _, err := m.BeginLifecycleActivation(context.Background(), tenant, LifecycleActivationRequest{
		Evidence: []EvidenceRef{labEvidence("quiescence"), labEvidence("caller-readiness")},
	}); err != nil {
		t.Fatalf("begin the frontier: %v", err)
	}
}

// heldBy seeds one row of component under h, of 2 000 000 µUSD, created at baseTime under
// a policy of its own, in state: active until its TTL, or settled at baseTime.
func heldBy(t *testing.T, st store.Store, tenant model.TenantID, h holdID, component, state string) {
	t.Helper()
	rec := ledgerRow(model.NewID(), component, h, 1, 2*oneUSD, baseTime, baseTime.Add(5*time.Minute), resvStateActive)
	if state != resvStateActive {
		rec[colResvState] = state
		rec[colResvSettledAt] = model.NewTimestamp(baseTime).String()
		if state == resvStateCommitted {
			rec[colResvActual] = measured
		}
	}
	seedReservation(t, st, tenant, rec)
}

// namedAtInsert witnesses every ledger row inserted through it: inside the inserting
// transaction and before the insert, it reads the admission row of key and counts the
// row as unnamed unless that row already names the hold the ledger row carries.
type namedAtInsert struct {
	api.ModuleData
	key      string
	inserted atomic.Int64
	unnamed  atomic.Int64
}

func (d *namedAtInsert) Mutate(ctx context.Context, tenant model.TenantID, fn func(store.Scope) error) error {
	return d.ModuleData.Mutate(ctx, tenant, func(sc store.Scope) error {
		return fn(witnessScope{Scope: sc, data: d})
	})
}

// witnessScope forwards the transaction lock and clock of the scope it wraps, as
// insertConflictScope does, and hands out a witnessing ledger repository.
type witnessScope struct {
	store.Scope
	data *namedAtInsert
}

func (s witnessScope) LockTransaction(ctx context.Context, key string) error {
	locker, ok := s.Scope.(store.TransactionLocker)
	if !ok {
		return errors.New("finops-test: wrapped scope provides no transaction lock")
	}
	return locker.LockTransaction(ctx, key)
}

func (s witnessScope) TransactionNow(ctx context.Context) (model.Timestamp, error) {
	clock, ok := s.Scope.(store.TransactionClock)
	if !ok {
		return model.Timestamp{}, errors.New("finops-test: wrapped scope provides no transaction clock")
	}
	return clock.TransactionNow(ctx)
}

func (s witnessScope) Ext(kind model.Kind) (store.GenericRepo, error) {
	repo, err := s.Scope.Ext(kind)
	if err != nil || kind != budgetReservationKind {
		return repo, err
	}
	return witnessRepo{GenericRepo: repo, scope: s.Scope, data: s.data}, nil
}

type witnessRepo struct {
	store.GenericRepo
	scope store.Scope
	data  *namedAtInsert
}

func (r witnessRepo) Create(ctx context.Context, rec model.Record) (model.Record, error) {
	row, found, err := rowOfKey(ctx, r.scope, r.data.key)
	r.data.inserted.Add(1)
	if err != nil || !found || row.handle.String() != rec.String(colResvHandle) {
		r.data.unnamed.Add(1)
	}
	return r.GenericRepo.Create(ctx, rec)
}

// unmarkedWritesRefused, once armed, fails the writes of every caller but the marked one
// from the from-th such write on, before their transactions open.
type unmarkedWritesRefused struct {
	api.ModuleData
	from   int64
	armed  atomic.Bool
	writes atomic.Int64
}

func (d *unmarkedWritesRefused) Mutate(ctx context.Context, tenant model.TenantID, fn func(store.Scope) error) error {
	if !marked(ctx) && d.armed.Load() && d.writes.Add(1) >= d.from {
		return errInjectedFault
	}
	return d.ModuleData.Mutate(ctx, tenant, fn)
}

// TestHoldWithFailedPublicationCompensationAndRetentionIsRecoveredBeforeTTL: a caller
// whose publication fails twice and whose give-back fails too is refused, and the claim
// it leaves still names its hold. The recovery pass after the claim goes stale retires it
// and releases both components of the hold, with an actual of zero, before their TTL.
func TestHoldWithFailedPublicationCompensationAndRetentionIsRecoveredBeforeTTL(t *testing.T) {
	forEachAdmissionEngine(t, runHoldWithFailedPublicationCompensationAndRetentionIsRecoveredBeforeTTL)
}

func runHoldWithFailedPublicationCompensationAndRetentionIsRecoveredBeforeTTL(t *testing.T, cfg store.Config) {
	m, st, tenant, clk := settlementFixture(t, cfg)
	req := seatRequest("model_gateway/unpublishable")
	base := m.data
	m.data = &refusedWritesData{ModuleData: base, from: 3, to: 5}
	res, err := m.Reserve(pausedCtx(context.Background()), tenant, req)
	m.data = base
	if err != nil || res.Allowed || res.Handle != "" || res.Reason != ReasonStoreUnreachable {
		t.Fatalf("a caller that could neither publish nor give back was answered %+v err=%v; want a refusal", res, err)
	}
	claim := admissionRowOf(t, m, tenant, req.IdempotencyKey)
	if claim.state != admStatePending || claim.handle.isZero() || claim.version != 2 {
		t.Fatalf("the row is %s at version %d naming %q, want the claim after its create", claim.state, claim.version, claim.handle)
	}
	h := claim.handle
	assertRowsUnder(t, st, tenant, h, 2, resvStateActive, 0, time.Time{})

	// The first recovery pass after the claim went stale.
	clk.advance(admissionClaimTakeover + 31*time.Second)
	rep := recoverOK(t, m, tenant)
	assertRecovery(t, "the pass", rep, noStop(AdmissionRecovery{PendingRetired: 1, OwedReleased: 2}))
	assertRetired(t, m, tenant, req.IdempotencyKey)
	assertRowsUnder(t, st, tenant, h, 2, resvStateReleased, 0, clk.t)
	for _, r := range ledgerRowsUnder(t, st, tenant, h) {
		exp, err := model.ParseTimestamp(r.String(colResvExpiresAt))
		if err != nil || !clk.t.Before(exp.Time()) {
			t.Fatalf("the %s row was released at %s, not before it lapsed at %q", r.String(colResvPolicyKind), clk.t, r.String(colResvExpiresAt))
		}
	}
	assertWithheld(t, st, tenant, clk.t, 0, 0)
}

// TestInterruptionBeforeAssociationLeavesNoAnonymousHold: wherever a caller stops —
// after its claim, after its create, or at a publication whose outcome it could not read
// back — every ledger row under its hold was inserted while the key already named that
// hold, so no row is anybody's but the key's. The recovery pass retires the claim and
// releases every such row.
func TestInterruptionBeforeAssociationLeavesNoAnonymousHold(t *testing.T) {
	forEachAdmissionEngine(t, runInterruptionBeforeAssociationLeavesNoAnonymousHold)
}

func runInterruptionBeforeAssociationLeavesNoAnonymousHold(t *testing.T, cfg store.Config) {
	for _, c := range []struct {
		name    string
		leg     int
		version int64
		rows    int
	}{
		{"stopped after the claim", 1, 1, 0},
		{"stopped after the create", 2, 2, 2},
		{"stopped at a publication whose outcome was lost", 3, 2, 2},
	} {
		t.Run(c.name, func(t *testing.T) {
			m, st, tenant, clk := settlementFixture(t, cfg)
			req := seatRequest("model_gateway/interrupted")
			base := m.data
			witness := &namedAtInsert{key: req.IdempotencyKey}
			var fault *faultData
			switch c.leg {
			case 1:
				witness.ModuleData = stopAfter(base, 1)
			case 2:
				witness.ModuleData = stopAfter(base, 2)
			default:
				fault = &faultData{ModuleData: base, nth: 3, mode: faultAfterRollback}
				witness.ModuleData = blindAfterFault{fault}
			}
			m.data = witness
			res, err := m.Reserve(pausedCtx(context.Background()), tenant, req)
			m.data = base
			if fault != nil && !fault.fired.Load() {
				t.Fatal("the injected fault never fired; the test proves nothing")
			}
			if err != nil || res.Allowed || res.Handle != "" {
				t.Fatalf("an interrupted caller was answered %+v err=%v; want a refusal", res, err)
			}

			claim := admissionRowOf(t, m, tenant, req.IdempotencyKey)
			if claim.state != admStatePending || claim.handle.isZero() || claim.version != c.version {
				t.Fatalf("the row is %s at version %d naming %q, want the claim at version %d", claim.state, claim.version, claim.handle, c.version)
			}
			h := claim.handle
			if n := len(ledgerRowsUnder(t, st, tenant, h)); n != c.rows {
				t.Fatalf("%d ledger row(s) under the claimed hold, want %d", n, c.rows)
			}
			if n := len(countReservations(t, st, tenant)); n != c.rows {
				t.Fatalf("%d ledger row(s) in the tenant, want only the %d under the claimed hold", n, c.rows)
			}
			if inserted, unnamed := witness.inserted.Load(), witness.unnamed.Load(); inserted != int64(c.rows) || unnamed != 0 {
				t.Fatalf("%d ledger row(s) inserted, %d of them while the key did not name their hold", inserted, unnamed)
			}

			clk.advance(admissionClaimTakeover + 31*time.Second)
			rep := recoverOK(t, m, tenant)
			assertRecovery(t, "the pass", rep, noStop(AdmissionRecovery{PendingRetired: 1, OwedReleased: c.rows}))
			assertRetired(t, m, tenant, req.IdempotencyKey)
			if c.rows > 0 {
				assertRowsUnder(t, st, tenant, h, c.rows, resvStateReleased, 0, clk.t)
			}
			assertWithheld(t, st, tenant, clk.t, 0, 0)
		})
	}
}

// TestLostGenerationWithFailedCompensationKeepsTheSuccessorAndRecoversThePredecessor: a
// caller that created its hold is paused before it publishes; another caller takes the
// stale claim over — owing the first hold — and then can neither create nor give back.
// The first caller resumes, finds the key another caller's claim and writes nothing: the
// successor's claim stands. Once that claim is stale the recovery pass retires it and
// releases the first hold, which nobody else would settle; the successor's hold never had
// a row.
func TestLostGenerationWithFailedCompensationKeepsTheSuccessorAndRecoversThePredecessor(t *testing.T) {
	forEachAdmissionEngine(t, runLostGenerationWithFailedCompensationKeepsTheSuccessorAndRecoversThePredecessor)
}

func runLostGenerationWithFailedCompensationKeepsTheSuccessorAndRecoversThePredecessor(t *testing.T, cfg store.Config) {
	m, st, tenant, clk := settlementFixture(t, cfg)
	req := seatRequest("model_gateway/lost-generation")
	base := m.data
	defer func() { m.data = base }()
	faults := &unmarkedWritesRefused{ModuleData: base, from: 2}
	d := &pausedMutateData{ModuleData: faults, nth: 3, reached: make(chan struct{}), resume: make(chan struct{})}
	m.data = d
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	first := make(chan pausedReserveOutcome, 1)
	go func() {
		r, e := m.Reserve(pausedCtx(ctx), tenant, req)
		first <- pausedReserveOutcome{r, e}
	}()
	awaitPausedMutate(t, d)
	claim := admissionRowOf(t, m, tenant, req.IdempotencyKey)
	h := claim.handle
	if claim.state != admStatePending || h.isZero() || claim.version != 2 {
		t.Fatalf("the first caller's row is %s at version %d naming %q, want its claim after its create", claim.state, claim.version, h)
	}
	assertRowsUnder(t, st, tenant, h, 2, resvStateActive, 0, time.Time{})

	clk.advance(admissionClaimTakeover + time.Second)
	faults.armed.Store(true)
	successor, err := m.Reserve(context.Background(), tenant, req)
	faults.armed.Store(false)
	if err != nil || successor.Allowed || successor.Handle != "" {
		t.Fatalf("a caller whose create and give-back both failed was answered %+v err=%v; want a refusal", successor, err)
	}
	taken := admissionRowOf(t, m, tenant, req.IdempotencyKey)
	h2 := taken.handle
	if taken.state != admStatePending || h2.isZero() || h2 == h || !taken.owed.has(h) || taken.version != 3 {
		t.Fatalf("the successor's row is %s at version %d naming %q owing %v, want its claim owing the first hold",
			taken.state, taken.version, h2, taken.owed)
	}

	close(d.resume)
	time.AfterFunc(250*time.Millisecond, cancel)
	late := awaitPausedReserve(t, first)
	assertBusyAnswer(t, late.res, late.err)
	if kept := admissionRowOf(t, m, tenant, req.IdempotencyKey); kept.version != taken.version || kept.handle != h2 || kept.state != admStatePending {
		t.Fatalf("the resumed caller wrote the successor's row: %s at version %d naming %q", kept.state, kept.version, kept.handle)
	}
	assertRowsUnder(t, st, tenant, h, 2, resvStateActive, 0, time.Time{})

	// Once the successor's claim is stale, recovery settles both generations.
	clk.advance(admissionClaimTakeover)
	rep := recoverOK(t, m, tenant)
	assertRecovery(t, "the pass", rep, noStop(AdmissionRecovery{PendingRetired: 1, OwedReleased: 2}))
	assertRetired(t, m, tenant, req.IdempotencyKey)
	assertRowsUnder(t, st, tenant, h, 2, resvStateReleased, 0, clk.t)
	if n := len(ledgerRowsUnder(t, st, tenant, h2)); n != 0 {
		t.Fatalf("%d ledger row(s) under the successor's hold, want none", n)
	}
	assertWithheld(t, st, tenant, clk.t, 0, 0)
}

// TestRetireRechecksAgeUnderLock: the pass decides to retire a claim from a read, and the
// retirement decides again, under the writer lock, from its own read at its own instant.
// A claim that is young at that instant, or that another caller took over and published
// meanwhile, is not written.
func TestRetireRechecksAgeUnderLock(t *testing.T) {
	forEachAdmissionEngine(t, runRetireRechecksAgeUnderLock)
}

func runRetireRechecksAgeUnderLock(t *testing.T, cfg store.Config) {
	pausedPass := func(t *testing.T, m *Module, tenant model.TenantID) (*pausedMutateData, <-chan recoveryOutcome) {
		t.Helper()
		d := &pausedMutateData{ModuleData: m.data, nth: 1, reached: make(chan struct{}), resume: make(chan struct{})}
		m.data = d
		done := make(chan recoveryOutcome, 1)
		go func() {
			rep, err := m.RecoverAdmissions(pausedCtx(context.Background()), tenant)
			done <- recoveryOutcome{rep, err}
		}()
		awaitPausedMutate(t, d)
		return d, done
	}

	t.Run("young at the instant of the retirement", func(t *testing.T) {
		m, _, tenant, clk := settlementFixture(t, cfg)
		req := seatRequest("model_gateway/young-under-the-lock")
		stagePendingClaim(t, m, tenant, req)
		before := admissionRowOf(t, m, tenant, req.IdempotencyKey)
		clk.advance(admissionClaimTakeover + time.Second)
		base := m.data
		d, done := pausedPass(t, m, tenant)
		// The pass read the claim stale; at the instant its retirement runs, it is young.
		clk.advance(-20 * time.Second)
		close(d.resume)
		got := awaitRecovery(t, done)
		m.data = base
		if got.err != nil || got.rep.PendingRetired != 0 {
			t.Fatalf("the pass retired a claim young under the lock: %+v err=%v", got.rep, got.err)
		}
		if after := admissionRowOf(t, m, tenant, req.IdempotencyKey); after.version != before.version || after.state != admStatePending || after.handle != before.handle {
			t.Fatalf("the claim was written: %s at version %d naming %q", after.state, after.version, after.handle)
		}
		// Stale at the lock too, it is retired by the next pass.
		clk.advance(20 * time.Second)
		assertRecovery(t, "the next pass", recoverOK(t, m, tenant), noStop(AdmissionRecovery{PendingRetired: 1}))
		assertRetired(t, m, tenant, req.IdempotencyKey)
	})

	t.Run("taken over and published while the pass waited", func(t *testing.T) {
		m, st, tenant, clk := settlementFixture(t, cfg)
		req := seatRequest("model_gateway/published-under-the-pass")
		stagePendingClaim(t, m, tenant, req)
		clk.advance(admissionClaimTakeover + time.Second)
		base := m.data
		d, done := pausedPass(t, m, tenant)
		res := reserveOK(t, m, tenant, req)
		close(d.resume)
		got := awaitRecovery(t, done)
		m.data = base
		if got.err != nil || got.rep.PendingRetired != 0 {
			t.Fatalf("the pass retired a key another caller published: %+v err=%v", got.rep, got.err)
		}
		assertAdmission(t, m, tenant, req.IdempotencyKey, admStateReserved, holdID(res.Handle))
		assertRowsUnder(t, st, tenant, holdID(res.Handle), 2, resvStateActive, 0, time.Time{})
	})
}

// TestRecoveryNeverWritesYoungPending: a claim younger than the takeover bound is a
// caller in flight, and its create settles what it owes. The pass writes nothing to it —
// not the claim, not a hold it owes, not an owed list that does not decode — with or
// without an activation frontier; it reports what the claims still owe.
func TestRecoveryNeverWritesYoungPending(t *testing.T) {
	forEachAdmissionEngine(t, runRecoveryNeverWritesYoungPending)
}

func runRecoveryNeverWritesYoungPending(t *testing.T, cfg store.Config) {
	t.Run("claims in flight", func(t *testing.T) {
		m, st, tenant, clk := settlementFixture(t, cfg)
		owed := newHoldID()
		heldBy(t, st, tenant, owed, "b", resvStateActive)
		stageClaim(t, m, tenant, seatRequest("model_gateway/young-and-owing"), owedHolds{owed})
		garbled := seatRequest("model_gateway/young-and-undecodable")
		stagePendingClaim(t, m, tenant, garbled)
		setStoredCell(t, st, tenant, garbled.IdempotencyKey, colAdmOwedHandles, `["not an identity"`)
		clk.advance(admissionClaimTakeover - time.Second)
		admissions, ledger := rowsByID(t, st, tenant, admissionIdempotencyKind), rowsByID(t, st, tenant, budgetReservationKind)

		assertRecovery(t, "the pass", recoverOK(t, m, tenant), noStop(AdmissionRecovery{OwedRemaining: 1, Undecodable: 1}))
		assertRowsUnchanged(t, "the admission rows", admissions, rowsByID(t, st, tenant, admissionIdempotencyKind))
		assertRowsUnchanged(t, "the ledger", ledger, rowsByID(t, st, tenant, budgetReservationKind))
	})

	t.Run("a claim in flight under a frontier", func(t *testing.T) {
		m, st, tenant, clk := settlementFixture(t, cfg)
		owed := newHoldID()
		heldBy(t, st, tenant, owed, "b", resvStateActive)
		beginFrontier(t, m, tenant)
		stageClaim(t, m, tenant, seatRequest("model_gateway/young-under-a-frontier"), owedHolds{owed})
		clk.advance(admissionClaimTakeover - time.Second)
		admissions, ledger := rowsByID(t, st, tenant, admissionIdempotencyKind), rowsByID(t, st, tenant, budgetReservationKind)

		assertRecovery(t, "the pass", recoverOK(t, m, tenant), noStop(AdmissionRecovery{OwedRemaining: 1}))
		assertRowsUnchanged(t, "the admission rows", admissions, rowsByID(t, st, tenant, admissionIdempotencyKind))
		assertRowsUnchanged(t, "the ledger", ledger, rowsByID(t, st, tenant, budgetReservationKind))
	})
}

// TestUncertainRetireResolvedByIdentity: a retirement whose outcome is unknown is
// resolved by the row alone. Found one version on, released and naming nothing, it is
// done, with the rows it released. Found as it was, or not readable, it is unresolved,
// and the next pass decides from the row as it then stands.
func TestUncertainRetireResolvedByIdentity(t *testing.T) {
	forEachAdmissionEngine(t, runUncertainRetireResolvedByIdentity)
}

func runUncertainRetireResolvedByIdentity(t *testing.T, cfg store.Config) {
	for _, c := range []struct {
		name  string
		mode  faultMode
		blind bool
		want  AdmissionRecovery
	}{
		{"the retirement never opened", faultBeforeCallback, false, AdmissionRecovery{Unresolved: 1}},
		{"the retirement rolled back after it ran", faultAfterRollback, false, AdmissionRecovery{Unresolved: 1}},
		{"the retirement committed and its acknowledgment was lost", faultAfterCommit, false, AdmissionRecovery{PendingRetired: 1, OwedReleased: 2}},
		{"the retirement committed and the row could not be read", faultAfterCommit, true, AdmissionRecovery{Unresolved: 1}},
	} {
		t.Run(c.name, func(t *testing.T) {
			m, st, tenant, clk := settlementFixture(t, cfg)
			req := seatRequest("model_gateway/uncertain-retirement")
			claim := stagedAfterCreate(t, m, tenant, req)
			clk.advance(admissionClaimTakeover + 31*time.Second)

			base := m.data
			fault := &faultData{ModuleData: base, nth: 1, mode: c.mode}
			m.data = fault
			if c.blind {
				m.data = blindAfterFault{fault}
			}
			rep, err := m.RecoverAdmissions(pausedCtx(context.Background()), tenant)
			m.data = base
			if !fault.fired.Load() {
				t.Fatal("the injected fault never fired; the test proves nothing")
			}
			if err != nil {
				t.Fatalf("the pass: %v", err)
			}
			assertRecovery(t, "the pass", rep, noStop(c.want))

			next := noStop(AdmissionRecovery{})
			if c.mode != faultAfterCommit {
				assertRowsUnder(t, st, tenant, claim.handle, 2, resvStateActive, 0, time.Time{})
				clk.advance(time.Second)
				next = noStop(AdmissionRecovery{PendingRetired: 1, OwedReleased: 2})
			}
			assertRecovery(t, "the next pass", recoverOK(t, m, tenant), next)
			assertRetired(t, m, tenant, req.IdempotencyKey)
			assertRowsUnder(t, st, tenant, claim.handle, 2, resvStateReleased, 0, clk.t)
			assertWithheld(t, st, tenant, clk.t, 0, 0)
		})
	}
}

// TestLostAbandonAckRecovered: a caller refused by a spend limit gives its claim back.
// When that give-back committed and its acknowledgment was lost, the key is already
// released; when it did not commit, the claim stays pending naming a hold with no row,
// and the recovery pass retires it. Either way the caller was refused and no ledger row
// exists.
func TestLostAbandonAckRecovered(t *testing.T) {
	forEachAdmissionEngine(t, runLostAbandonAckRecovered)
}

func runLostAbandonAckRecovered(t *testing.T, cfg store.Config) {
	for _, c := range []struct {
		name     string
		mode     faultMode
		released bool
	}{
		{"the give-back committed and its acknowledgment was lost", faultAfterCommit, true},
		{"the give-back rolled back after it ran", faultAfterRollback, false},
		{"the give-back never opened", faultBeforeCallback, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			m, st, tenant, clk := settlementFixture(t, cfg)
			spent := mkCost("anthropic", "model", "s1", 1, 1, 10*oneUSD, baseTime)
			spent.Actor = "a"
			m.ingest(t, tenant, spent)
			req := seatRequest("model_gateway/refused-and-given-back")

			res, _ := reserveThroughFault(t, m, tenant, req, 3, c.mode)
			if res.Allowed || !res.SpendLimit || res.Handle != "" {
				t.Fatalf("the seat limit must refuse, marked as one: %+v", res)
			}
			row := admissionRowOf(t, m, tenant, req.IdempotencyKey)
			if c.released && (row.state != admStateReleased || !row.handle.isZero()) {
				t.Fatalf("the row is %s naming %q, want the give-back that committed", row.state, row.handle)
			}
			if !c.released && (row.state != admStatePending || row.handle.isZero()) {
				t.Fatalf("the row is %s naming %q, want the claim the give-back did not release", row.state, row.handle)
			}
			if n := len(countReservations(t, st, tenant)); n != 0 {
				t.Fatalf("a refused create left %d ledger row(s)", n)
			}

			clk.advance(admissionClaimTakeover + 31*time.Second)
			want := AdmissionRecovery{}
			if !c.released {
				want.PendingRetired = 1
			}
			assertRecovery(t, "the pass", recoverOK(t, m, tenant), noStop(want))
			assertRetired(t, m, tenant, req.IdempotencyKey)
			if n := len(countReservations(t, st, tenant)); n != 0 {
				t.Fatalf("recovery left %d ledger row(s)", n)
			}
		})
	}
}

// TestUndecodableOwedClearsAfterWindow: a claim whose owed list does not decode refuses
// its key and is reported, and nothing is written to it until five minutes after its
// date. Then the pass clears the list — every hold it could have named was taken before
// that date and has lapsed — and retires the claim. The text is never reported, logged
// or put in an error. Only a dated claim of this build is cleared: a published row, a
// claim an earlier build staged or an undated claim whose list does not decode is only
// ever reported.
func TestUndecodableOwedClearsAfterWindow(t *testing.T) {
	forEachAdmissionEngine(t, runUndecodableOwedClearsAfterWindow)
}

func runUndecodableOwedClearsAfterWindow(t *testing.T, cfg store.Config) {
	m, st, tenant, clk := settlementFixture(t, cfg)
	logs := captureLog(m)
	const garbled = `["not an identity"`
	req := seatRequest("model_gateway/garbled")
	stagePendingClaim(t, m, tenant, req)
	setStoredCell(t, st, tenant, req.IdempotencyKey, colAdmOwedHandles, garbled)
	published := seatRequest("model_gateway/garbled-published")
	seedAdmission(t, st, tenant, admissionRecordFor(published, admStateCommitted, newHoldID(), "", baseTime))
	setStoredCell(t, st, tenant, published.IdempotencyKey, colAdmOwedHandles, garbled)
	unchanged := func(t *testing.T, key string, before admissionRow) {
		t.Helper()
		after := admissionRowOf(t, m, tenant, key)
		if after.version != before.version || after.owedRaw != garbled || !errors.Is(after.owedErr, errOwedUndecodable) {
			t.Fatalf("%s was written: version %d → %d, text %v", key, before.version, after.version, after.owedRaw)
		}
	}
	claim, row := admissionRowOf(t, m, tenant, req.IdempotencyKey), admissionRowOf(t, m, tenant, published.IdempotencyKey)

	clk.advance(admissionClaimTakeover + time.Second)
	if res, err := m.Reserve(context.Background(), tenant, req); err != nil || res.Allowed {
		t.Fatalf("a key whose owed list does not decode was answered %+v err=%v", res, err)
	}
	assertRecovery(t, "the pass", recoverOK(t, m, tenant), noStop(AdmissionRecovery{Undecodable: 1}))
	job := reconcileOK(t, m, tenant)
	if job.Undecodable != 2 || !job.Drift || job.FindingRef == "" || job.Quiet() {
		t.Fatalf("the reconciliation reported %+v, want both rows undecodable, drift and a finding", job)
	}
	for _, text := range []string{string(mustJSON(t, job)), logs.String()} {
		if strings.Contains(text, "not an identity") {
			t.Fatalf("the stored text reached a report or a log line: %s", text)
		}
	}
	unchanged(t, req.IdempotencyKey, claim)

	clk.t = baseTime.Add(5*time.Minute - time.Nanosecond)
	assertRecovery(t, "the pass before five minutes", recoverOK(t, m, tenant), noStop(AdmissionRecovery{Undecodable: 1}))
	unchanged(t, req.IdempotencyKey, claim)

	clk.t = baseTime.Add(5 * time.Minute)
	assertRecovery(t, "the pass at five minutes", recoverOK(t, m, tenant), noStop(AdmissionRecovery{UndecodableCleared: 1, PendingRetired: 1}))
	assertRetired(t, m, tenant, req.IdempotencyKey)
	unchanged(t, published.IdempotencyKey, row)
	if n := len(countReservations(t, st, tenant)); n != 0 {
		t.Fatalf("clearing the list wrote %d ledger row(s)", n)
	}

	t.Run("only a dated claim of this build is cleared", func(t *testing.T) {
		m, st, tenant, clk := settlementFixture(t, cfg)
		for _, rec := range []model.Record{
			admissionRecord("model_gateway/garbled-earlier-claim", admStatePending, "", "", baseTime.Add(-600*time.Second)),
			admissionRecord("model_gateway/garbled-undated-claim", admStatePending, newHoldID(), "", time.Time{}),
		} {
			seedAdmission(t, st, tenant, rec)
			setStoredCell(t, st, tenant, rec.String(colAdmKey), colAdmOwedHandles, garbled)
		}
		admissions := rowsByID(t, st, tenant, admissionIdempotencyKind)
		clk.advance(24 * time.Hour)
		assertRecovery(t, "the pass", recoverOK(t, m, tenant), noStop(AdmissionRecovery{Undecodable: 2}))
		assertRowsUnchanged(t, "the admission rows", admissions, rowsByID(t, st, tenant, admissionIdempotencyKind))
	})
}

// mustJSON marshals v, as a report travels to an operator.
func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

// TestUncertainUndecodableClearResolved: a clear whose outcome is unknown is resolved by
// the row. The list found gone is a clear that committed, and the claim is retired in
// the same pass; the text found still there, or a row that cannot be read, is
// unresolved, nothing more is written to the claim, and the next pass completes it.
func TestUncertainUndecodableClearResolved(t *testing.T) {
	forEachAdmissionEngine(t, runUncertainUndecodableClearResolved)
}

func runUncertainUndecodableClearResolved(t *testing.T, cfg store.Config) {
	for _, c := range []struct {
		name  string
		mode  faultMode
		blind bool
		first AdmissionRecovery
		next  AdmissionRecovery
	}{
		{"the clear never opened", faultBeforeCallback, false,
			AdmissionRecovery{Unresolved: 1}, AdmissionRecovery{UndecodableCleared: 1, PendingRetired: 1}},
		{"the clear rolled back after it ran", faultAfterRollback, false,
			AdmissionRecovery{Unresolved: 1}, AdmissionRecovery{UndecodableCleared: 1, PendingRetired: 1}},
		{"the clear committed and its acknowledgment was lost", faultAfterCommit, false,
			AdmissionRecovery{UndecodableCleared: 1, PendingRetired: 1}, AdmissionRecovery{}},
		{"the clear committed and the row could not be read", faultAfterCommit, true,
			AdmissionRecovery{Unresolved: 1}, AdmissionRecovery{PendingRetired: 1}},
	} {
		t.Run(c.name, func(t *testing.T) {
			m, st, tenant, clk := settlementFixture(t, cfg)
			req := seatRequest("model_gateway/garbled-uncertain")
			stagePendingClaim(t, m, tenant, req)
			setStoredCell(t, st, tenant, req.IdempotencyKey, colAdmOwedHandles, `["not an identity"`)
			before := admissionRowOf(t, m, tenant, req.IdempotencyKey)
			clk.t = baseTime.Add(5 * time.Minute)

			base := m.data
			fault := &faultData{ModuleData: base, nth: 1, mode: c.mode}
			m.data = fault
			if c.blind {
				m.data = blindAfterFault{fault}
			}
			rep, err := m.RecoverAdmissions(pausedCtx(context.Background()), tenant)
			m.data = base
			if !fault.fired.Load() {
				t.Fatal("the injected fault never fired; the test proves nothing")
			}
			if err != nil {
				t.Fatalf("the pass: %v", err)
			}
			assertRecovery(t, "the pass", rep, noStop(c.first))
			if c.mode != faultAfterCommit {
				after := admissionRowOf(t, m, tenant, req.IdempotencyKey)
				if after.version != before.version || after.owedErr == nil {
					t.Fatalf("a clear that did not commit left the row at version %d with list error %v", after.version, after.owedErr)
				}
			}
			assertRecovery(t, "the next pass", recoverOK(t, m, tenant), noStop(c.next))
			assertRetired(t, m, tenant, req.IdempotencyKey)
		})
	}
}

// TestCensusedOwedHoldsDropped: under an activation frontier recovery writes no ledger
// row. A stale claim's owed hold whose group the frontier's census names, or whose every
// row is settled, is the attempt lifecycle's: it is dropped from the list, and a claim
// that owes nothing else is retired. A hold the census does not name and that still
// holds money stays owed and is reported as blocked. An owes_release row an earlier build
// left is released naming nothing when the lifecycle owns every hold it names, and is
// left whole, each of its holds blocked, when it does not.
func TestCensusedOwedHoldsDropped(t *testing.T) {
	forEachAdmissionEngine(t, runCensusedOwedHoldsDropped)
}

func runCensusedOwedHoldsDropped(t *testing.T, cfg store.Config) {
	for _, c := range []struct {
		name  string
		state string
	}{
		{"the census names the owed hold", resvStateActive},
		{"every row of the owed hold is settled", resvStateCommitted},
	} {
		t.Run(c.name, func(t *testing.T) {
			m, st, tenant, clk := settlementFixture(t, cfg)
			owed := newHoldID()
			heldBy(t, st, tenant, owed, "b", c.state)
			req := seatRequest("model_gateway/owes-the-lifecycle")
			stageClaim(t, m, tenant, req, owedHolds{owed})
			beginFrontier(t, m, tenant)
			ledger := rowsByID(t, st, tenant, budgetReservationKind)
			clk.advance(admissionClaimTakeover + time.Second)

			assertRecovery(t, "the pass", recoverOK(t, m, tenant), noStop(AdmissionRecovery{PendingRetired: 1, OwedCleared: 1}))
			assertRetired(t, m, tenant, req.IdempotencyKey)
			assertRowsUnchanged(t, "the ledger", ledger, rowsByID(t, st, tenant, budgetReservationKind))
		})
	}

	t.Run("a hold the census does not name stays owed", func(t *testing.T) {
		m, st, tenant, clk := settlementFixture(t, cfg)
		censused, later := newHoldID(), newHoldID()
		heldBy(t, st, tenant, censused, "b", resvStateActive)
		beginFrontier(t, m, tenant)
		heldBy(t, st, tenant, later, "s", resvStateActive)
		req := seatRequest("model_gateway/owes-past-the-census")
		intent := stageClaim(t, m, tenant, req, owedHolds{censused, later})
		before := admissionRowOf(t, m, tenant, req.IdempotencyKey)
		ledger := rowsByID(t, st, tenant, budgetReservationKind)
		clk.advance(admissionClaimTakeover + time.Second)

		rep := recoverOK(t, m, tenant)
		assertRecovery(t, "the pass", rep, noStop(AdmissionRecovery{OwedCleared: 1, FrontierBlocked: 1}))
		if rep.Quiet() {
			t.Fatal("a blocked hold reported quiet")
		}
		row := admissionRowOf(t, m, tenant, req.IdempotencyKey)
		if row.state != admStatePending || row.handle != intent || len(row.owed) != 1 || !row.owed.has(later) ||
			row.version != before.version+1 || row.stateAt.String() != before.stateAt.String() {
			t.Fatalf("the claim is %s at version %d naming %q owing %v dated %s, want it still pending, owing only the blocked hold",
				row.state, row.version, row.handle, row.owed, row.stateAt)
		}
		assertRowsUnchanged(t, "the ledger", ledger, rowsByID(t, st, tenant, budgetReservationKind))

		assertRecovery(t, "the next pass", recoverOK(t, m, tenant), noStop(AdmissionRecovery{FrontierBlocked: 1}))
		if again := admissionRowOf(t, m, tenant, req.IdempotencyKey); again.version != row.version {
			t.Fatalf("the next pass wrote the blocked claim: version %d → %d", row.version, again.version)
		}
	})

	t.Run("an owes_release row whose every hold the lifecycle owns", func(t *testing.T) {
		m, st, tenant, clk := settlementFixture(t, cfg)
		h1, h2 := newHoldID(), newHoldID()
		read := seedOwesRelease(t, m, st, tenant, model.NewID(), model.NewID(), h1, h2, baseTime.Add(-20*time.Second))
		beginFrontier(t, m, tenant)
		ledger := rowsByID(t, st, tenant, budgetReservationKind)

		assertRecovery(t, "the pass", recoverOK(t, m, tenant), noStop(AdmissionRecovery{LegacyRetired: 1, OwedCleared: 2}))
		assertLegacySettled(t, m, tenant, read, clk.t)
		assertRowsUnchanged(t, "the ledger", ledger, rowsByID(t, st, tenant, budgetReservationKind))
	})

	t.Run("an owes_release row with a hold past the census", func(t *testing.T) {
		m, st, tenant, _ := settlementFixture(t, cfg)
		censused, later := newHoldID(), newHoldID()
		const key = "model_gateway/owes-past-the-census"
		seedAdmission(t, st, tenant, admissionRecord(key, admStateOwesRelease, censused, later, baseTime.Add(-20*time.Second)))
		heldBy(t, st, tenant, censused, "b", resvStateActive)
		beginFrontier(t, m, tenant)
		heldBy(t, st, tenant, later, "s", resvStateActive)
		read := admissionRowOf(t, m, tenant, key)
		ledger := rowsByID(t, st, tenant, budgetReservationKind)

		for _, pass := range []string{"the pass", "the next pass"} {
			rep := recoverOK(t, m, tenant)
			assertRecovery(t, pass, rep, noStop(AdmissionRecovery{FrontierBlocked: 2}))
			if rep.Quiet() {
				t.Fatalf("%s reported quiet over two blocked holds", pass)
			}
			assertLegacyUnchanged(t, m, tenant, read)
		}
		assertRowsUnchanged(t, "the ledger", ledger, rowsByID(t, st, tenant, budgetReservationKind))
	})
}

// TestLegacyRetirementAcrossPasses: a legacy claim is left by every pass while the stop
// is absent, and by the passes before a stated stop is usable; the first pass at which it
// is usable retires it and touches no ledger row. The holds an earlier writer took and
// no row names lapse on their own, and the next reconciliation sweeps them.
func TestLegacyRetirementAcrossPasses(t *testing.T) {
	forEachAdmissionEngine(t, runLegacyRetirementAcrossPasses)
}

func runLegacyRetirementAcrossPasses(t *testing.T, cfg store.Config) {
	t.Run("a stop that becomes usable", func(t *testing.T) {
		m, st, tenant, clk, budget, limit := legacyFixture(t, cfg)
		t0 := baseTime
		const key = "model_gateway/legacy-claim-across-passes"
		seedAdmission(t, st, tenant, admissionRecord(key, admStatePending, "", "", t0))
		created := t0.Add(320 * time.Second)
		h1, h2 := newHoldID(), newHoldID()
		seedReservation(t, st, tenant, ledgerRow(budget, "b", h1, 1, 2*oneUSD, created, created.Add(5*time.Minute), resvStateActive))
		seedReservation(t, st, tenant, ledgerRow(limit, "s", h2, 1, 2*oneUSD, created, created.Add(5*time.Minute), resvStateActive))
		WithLegacyWriterStop(mustLegacyStop(t, stopText(t0.Add(400*time.Second))))(m)
		read := admissionRowOf(t, m, tenant, key)
		ledger := rowsByID(t, st, tenant, budgetReservationKind)

		clk.t = t0.Add(650 * time.Second)
		rep := recoverOK(t, m, tenant)
		assertRecovery(t, "the pass at 650 s", rep, AdmissionRecovery{LegacyPending: 1, LegacyStop: string(legacyStopWaiting)})
		if rep.Quiet() {
			t.Fatal("a pass that left a legacy claim reported quiet")
		}
		assertLegacyUnchanged(t, m, tenant, read)

		clk.t = t0.Add(700 * time.Second)
		assertRecovery(t, "the pass at 700 s", recoverOK(t, m, tenant), AdmissionRecovery{LegacyRetired: 1, LegacyStop: string(legacyStopUsable)})
		assertLegacySettled(t, m, tenant, read, clk.t)
		assertRowsUnchanged(t, "the unnamed holds", ledger, rowsByID(t, st, tenant, budgetReservationKind))

		clk.t = t0.Add(900 * time.Second)
		job := reconcileOK(t, m, tenant)
		if job.SweptExpired != 2 || job.ExpiredUnsettled != 2 || !job.Drift {
			t.Fatalf("the reconciliation reported %+v, want the two lapsed holds swept and reported", job)
		}
		for _, h := range []holdID{h1, h2} {
			assertRowsUnder(t, st, tenant, h, 1, resvStateExpired, 0, clk.t)
		}
	})

	t.Run("no stop", func(t *testing.T) {
		m, st, tenant, clk, _, _ := legacyFixture(t, cfg)
		for _, key := range []string{"model_gateway/legacy-claim-dated", "model_gateway/legacy-claim-undated"} {
			at := baseTime.Add(-600 * time.Second)
			if strings.HasSuffix(key, "undated") {
				at = time.Time{}
			}
			seedAdmission(t, st, tenant, admissionRecord(key, admStatePending, "", "", at))
		}
		admissions := rowsByID(t, st, tenant, admissionIdempotencyKind)
		for _, after := range []time.Duration{61 * time.Second, 700 * time.Second, 24 * time.Hour} {
			clk.t = baseTime.Add(after)
			rep := recoverOK(t, m, tenant)
			assertRecovery(t, "the pass", rep, noStop(AdmissionRecovery{LegacyPending: 2}))
			if rep.Quiet() {
				t.Fatal("a pass that left legacy claims reported quiet")
			}
		}
		assertRowsUnchanged(t, "the legacy claims", admissions, rowsByID(t, st, tenant, admissionIdempotencyKind))
	})
}

// TestLostLegacySettleResolvedAtNextPass: the pass settles an owes_release row once;
// when that write fails it reports it unresolved and the next pass settles it, and when
// it committed with its acknowledgment lost, the pass finds it by the row and the next
// pass has nothing left to do.
func TestLostLegacySettleResolvedAtNextPass(t *testing.T) {
	forEachAdmissionEngine(t, runLostLegacySettleResolvedAtNextPass)
}

func runLostLegacySettleResolvedAtNextPass(t *testing.T, cfg store.Config) {
	for _, c := range []struct {
		name  string
		mode  faultMode
		blind bool
		first AdmissionRecovery
		next  AdmissionRecovery
	}{
		{"the settlement rolled back after it ran", faultAfterRollback, false,
			AdmissionRecovery{Unresolved: 1}, AdmissionRecovery{LegacyRetired: 1, OwedReleased: 2}},
		{"the settlement committed and its acknowledgment was lost", faultAfterCommit, false,
			AdmissionRecovery{LegacyRetired: 1, OwedReleased: 2}, AdmissionRecovery{}},
		{"the settlement committed and the row could not be read", faultAfterCommit, true,
			AdmissionRecovery{Unresolved: 1}, AdmissionRecovery{}},
	} {
		t.Run(c.name, func(t *testing.T) {
			m, st, tenant, clk, budget, limit := legacyFixture(t, cfg)
			h1, h2 := newHoldID(), newHoldID()
			read := seedOwesRelease(t, m, st, tenant, budget, limit, h1, h2, baseTime.Add(-20*time.Second))

			base := m.data
			fault := &faultData{ModuleData: base, nth: 1, mode: c.mode}
			m.data = fault
			if c.blind {
				m.data = blindAfterFault{fault}
			}
			rep, err := m.RecoverAdmissions(pausedCtx(context.Background()), tenant)
			m.data = base
			if !fault.fired.Load() {
				t.Fatal("the injected fault never fired; the test proves nothing")
			}
			if err != nil {
				t.Fatalf("the pass: %v", err)
			}
			assertRecovery(t, "the pass", rep, noStop(c.first))
			if c.mode != faultAfterCommit {
				assertLegacyUnchanged(t, m, tenant, read)
				clk.advance(time.Second)
			}
			assertRecovery(t, "the next pass", recoverOK(t, m, tenant), noStop(c.next))
			assertLegacySettled(t, m, tenant, read, clk.t)
			assertRowsUnder(t, st, tenant, h1, 1, resvStateReleased, 0, clk.t)
			assertRowsUnder(t, st, tenant, h2, 1, resvStateReleased, 0, clk.t)
		})
	}
}

// TestRecoveryCountsCorruptRowsAndContinues: a row that fails its integrity check is
// counted once per pass, is never written, and does not stop the pass. A claim whose
// handle slot is no identity, and a dead claimant's claim whose hold a published row
// names too, count three rows; the stale claim beside them is retired, and the settled
// key beside them is left alone. The job's inspection counts the same rows once more
// only as the same rows. A corrupt published row, which no recovery pass reads, is
// counted by the inspection, in either form. No report, log line or error carries a
// stored value.
func TestRecoveryCountsCorruptRowsAndContinues(t *testing.T) {
	forEachAdmissionEngine(t, runRecoveryCountsCorruptRowsAndContinues)
}

func runRecoveryCountsCorruptRowsAndContinues(t *testing.T, cfg store.Config) {
	storedValues := func(t *testing.T, texts []string, values ...string) {
		t.Helper()
		for _, text := range texts {
			for _, v := range values {
				if strings.Contains(text, v) {
					t.Fatalf("a stored value (%q) reached a report or a log line: %s", v, text)
				}
			}
		}
	}

	t.Run("the passes", func(t *testing.T) {
		m, st, tenant, clk := settlementFixture(t, cfg)
		logs := captureLog(m)
		badSlot := seatRequest("model_gateway/claim-with-a-bad-slot")
		rec := admissionRecordFor(badSlot, admStatePending, "", "", baseTime)
		rec[colAdmHandle] = "not-a-hold"
		seedAdmission(t, st, tenant, rec)
		deadClaim, sharer := seatRequest("model_gateway/dead-claim-sharing-a-hold"), seatRequest("model_gateway/published-sharing-a-hold")
		shared := newHoldID()
		seedAdmission(t, st, tenant, admissionRecordFor(deadClaim, admStatePending, shared, "", baseTime))
		seedAdmission(t, st, tenant, admissionRecordFor(sharer, admStateReserved, shared, "", baseTime))
		heldBy(t, st, tenant, shared, "b", resvStateActive)
		heldBy(t, st, tenant, shared, "s", resvStateActive)
		staleClaim := seatRequest("model_gateway/stale-claim")
		staleHold := stagedAfterCreate(t, m, tenant, staleClaim).handle
		settledLate := seatRequest("model_gateway/settled-late")
		settledHold := holdID(reserveOK(t, m, tenant, settledLate).Handle)
		if err := m.Release(context.Background(), tenant, settledHold.String()); err != nil {
			t.Fatalf("release: %v", err)
		}
		if err := m.Commit(context.Background(), tenant, settledHold.String(), measured); err != nil {
			t.Fatalf("late commit: %v", err)
		}
		untouched := map[string]model.Record{}
		for id, r := range rowsByID(t, st, tenant, admissionIdempotencyKind) {
			switch r.String(colAdmKey) {
			case badSlot.IdempotencyKey, deadClaim.IdempotencyKey, sharer.IdempotencyKey, settledLate.IdempotencyKey:
				untouched[id] = r
			}
		}
		sharedRows := map[string]model.Record{}
		for id, r := range rowsByID(t, st, tenant, budgetReservationKind) {
			if r.String(colResvHandle) == shared.String() {
				sharedRows[id] = r
			}
		}
		clk.advance(admissionClaimTakeover + 30*time.Second)

		rep := recoverOK(t, m, tenant)
		assertRecovery(t, "the pass", rep, noStop(AdmissionRecovery{PendingRetired: 1, OwedReleased: 2, Corrupt: 3}))
		if rep.Outstanding() != 3 || rep.Quiet() {
			t.Fatalf("the pass reported outstanding %d, quiet %v; want the three corrupt rows outstanding", rep.Outstanding(), rep.Quiet())
		}
		assertRetired(t, m, tenant, staleClaim.IdempotencyKey)
		assertRowsUnder(t, st, tenant, staleHold, 2, resvStateReleased, 0, clk.t)
		assertRowsUnder(t, st, tenant, shared, 2, resvStateActive, 0, time.Time{})
		assertRowsUnder(t, st, tenant, settledHold, 2, resvStateCommitted, measured, time.Time{})

		job := reconcileOK(t, m, tenant)
		if job.Corrupt != 3 || job.PendingRetired != 0 || job.Outstanding() != 3 || !job.Drift || job.FindingRef == "" || job.Quiet() {
			t.Fatalf("the reconciliation reported %+v; want each corrupt row once, drift and a finding", job)
		}
		after := rowsByID(t, st, tenant, admissionIdempotencyKind)
		for id, r := range untouched {
			if moved := changedColumns(r, after[id]); len(moved) > 0 {
				t.Fatalf("the row of %s changed %v", r.String(colAdmKey), moved)
			}
		}
		ledgerAfter := rowsByID(t, st, tenant, budgetReservationKind)
		for id, r := range sharedRows {
			if moved := changedColumns(r, ledgerAfter[id]); len(moved) > 0 {
				t.Fatalf("a row under the shared hold changed %v", moved)
			}
		}
		storedValues(t, []string{string(mustJSON(t, rep)), string(mustJSON(t, job)), logs.String()}, "not-a-hold", shared.String())
	})

	t.Run("the inspection, a corrupt published row alone", func(t *testing.T) {
		for _, form := range []string{"a spend slot that is not an identity", "two rows naming one hold"} {
			t.Run(form, func(t *testing.T) {
				m, st, tenant, clk := settlementFixture(t, cfg)
				logs := captureLog(m)
				h := newHoldID()
				want := 1
				rec := admissionRecordFor(seatRequest("model_gateway/published-corrupt"), admStateReserved, h, "", baseTime)
				if form == "a spend slot that is not an identity" {
					rec[colAdmSpendHandle] = "not-a-hold"
				} else {
					seedAdmission(t, st, tenant, admissionRecordFor(seatRequest("model_gateway/published-twin"), admStateReserved, h, "", baseTime))
					want = 2
				}
				seedAdmission(t, st, tenant, rec)
				heldBy(t, st, tenant, h, "b", resvStateActive)
				heldBy(t, st, tenant, h, "s", resvStateActive)
				admissions, ledger := rowsByID(t, st, tenant, admissionIdempotencyKind), rowsByID(t, st, tenant, budgetReservationKind)
				clk.advance(100 * time.Second)

				read, err := m.InspectReservations(context.Background(), tenant)
				if err != nil || read.Corrupt != want || read.Outstanding() != want || !read.Drift || read.FindingRef != "" {
					t.Fatalf("the read reported %+v err=%v; want the corrupt row(s) counted, drift and no finding", read, err)
				}
				job := reconcileOK(t, m, tenant)
				if job.Corrupt != want || job.Outstanding() != want || job.SweptExpired != 0 || job.Active != 2 ||
					!job.Drift || job.FindingRef == "" || job.Quiet() {
					t.Fatalf("the reconciliation reported %+v; want the corrupt row(s) counted once, drift and a finding", job)
				}
				assertRowsUnchanged(t, "the admission rows", admissions, rowsByID(t, st, tenant, admissionIdempotencyKind))
				assertRowsUnchanged(t, "the ledger", ledger, rowsByID(t, st, tenant, budgetReservationKind))
				storedValues(t, []string{string(mustJSON(t, read)), string(mustJSON(t, job)), logs.String()}, "not-a-hold", h.String())
			})
		}
	})
}

// -----------------------------------------------------------------------------
// RECOVERY OF EVERY HOLD THE KEY EVER OWNED. A pair an earlier build published has two
// holds, a pooled budget hold and a per-seat spend hold, each settled in its own
// transaction by that build, so the pair can come apart: one half settled and the other
// still withholding money, or one half lapsed and the other still live. These pairs are
// seeded as that build left them; this build cannot split one.
// -----------------------------------------------------------------------------

var errTransactionUnavailable = errors.New("fixture: write transaction unavailable")

// transactionFault refuses to OPEN the write transactions whose ordinal is named, and
// lets every other one — and every read — reach the real store.
type transactionFault struct {
	api.ModuleData
	fail  map[int]bool
	calls int
	fired int
}

func (d *transactionFault) Mutate(ctx context.Context, tenant model.TenantID, fn func(store.Scope) error) error {
	d.calls++
	if d.fail[d.calls] {
		d.fired++
		return errTransactionUnavailable
	}
	return d.ModuleData.Mutate(ctx, tenant, fn)
}

// withFault runs one call with the named write transactions unavailable, and puts the
// real data handle back afterwards so the assertions read the real store.
func withFault(m *Module, ordinals map[int]bool, call func()) *transactionFault {
	original := m.data
	fault := &transactionFault{ModuleData: original, fail: ordinals}
	m.data = fault
	call()
	m.data = original
	return fault
}

// holdState returns the lifecycle state of the one reservation row under a hold.
func holdState(t *testing.T, m *Module, tenant model.TenantID, h holdID) string {
	t.Helper()
	var state string
	if err := m.data.View(context.Background(), tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(budgetReservationKind)
		if err != nil {
			return err
		}
		rows, incomplete, err := scanReservations(context.Background(), repo, []model.Filter{eq(colResvHandle, h.String())})
		if err != nil {
			return err
		}
		if incomplete != "" || len(rows) != 1 {
			t.Fatalf("the fixture expected one complete reservation row, rows=%d incomplete=%q", len(rows), incomplete)
		}
		state = rows[0].String(colResvState)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return state
}

// pairedRequest is an admission that both a pooled budget and a per-seat spend limit
// hold — the budget and seat limit of seedBudgetAndSeatLimit — and those two policies.
func pairedRequest(t *testing.T, st store.Store, tenant model.TenantID, key string) (AdmissionRequest, model.ID, model.ID) {
	t.Helper()
	budget, limit := seedBudgetAndSeatLimit(t, st, tenant)
	return AdmissionRequest{Scope: AdmissionScopeModelGateway, ActorRef: "a", EstimateMicroUSD: oneUSD, IdempotencyKey: key}, budget, limit
}

// halfSettledPair seeds the pair an earlier build published at tp whose budget hold was
// released and whose seat hold still withholds until h2End: what that build left when
// the second of its two settlements could not commit.
func halfSettledPair(t *testing.T, st store.Store, tenant model.TenantID, req AdmissionRequest, budget, limit model.ID, tp, h2End time.Time) (holdID, holdID) {
	t.Helper()
	h1, h2 := legacyPair(t, st, tenant, req, budget, limit, tp, tp.Add(5*time.Minute), h2End, true)
	if err := st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(budgetReservationKind)
		if err != nil {
			return err
		}
		rows, _, err := repo.List(context.Background(), model.Query{Filters: []model.Filter{eq(colResvHandle, h1.String())}, Limit: 2})
		if err != nil || len(rows) != 1 {
			t.Fatalf("fixture: %d row(s) under the budget hold, err=%v", len(rows), err)
		}
		rows[0][colResvState] = resvStateReleased
		rows[0][colResvActual] = int64(0)
		rows[0][colResvSettledAt] = model.NewTimestamp(tp).String()
		_, err = repo.Update(context.Background(), rows[0])
		return err
	}); err != nil {
		t.Fatalf("settle the budget hold: %v", err)
	}
	return h1, h2
}

// withholdingHolds is the number of reservation rows still keeping money from other
// callers: active rows whose expiry has not passed.
func withholdingHolds(t *testing.T, m *Module, tenant model.TenantID) int {
	t.Helper()
	report, err := m.InspectReservations(context.Background(), tenant)
	if err != nil {
		t.Fatal(err)
	}
	return report.Active - report.ActiveLapsed
}

// TestAHalfSettledPairHoldsItsKeyUntilItsSurvivingHoldLapses: a pair whose two holds were
// both settled is taken over by the next call into one new hold, and its settled rows
// stay as they were. A pair whose budget hold was released while its seat hold still
// withholds holds its key back inside its window: the call is refused, nothing is
// written, and the seat hold is never released. Once it lapses, the next call takes the
// key over into one new hold, and the sweep records the seat hold's expiry.
func TestAHalfSettledPairHoldsItsKeyUntilItsSurvivingHoldLapses(t *testing.T) {
	forEachAdmissionEngine(t, runAHalfSettledPairHoldsItsKeyUntilItsSurvivingHoldLapses)
}

func runAHalfSettledPairHoldsItsKeyUntilItsSurvivingHoldLapses(t *testing.T, cfg store.Config) {
	for _, failSecond := range []bool{false, true} {
		name := "both_settlements_commit"
		if failSecond {
			name = "second_settlement_unavailable"
		}
		t.Run(name, func(t *testing.T) {
			m, st, tenant, _ := openFinCfg(t, cfg)
			clk := &fakeClock{t: baseTime}
			m.clock = clk
			ctx := context.Background()
			req, budget, limit := pairedRequest(t, st, tenant, "model_gateway/half-settled-pair")
			h2End := baseTime.Add(290 * time.Second)
			var h1, h2 holdID
			if failSecond {
				h1, h2 = halfSettledPair(t, st, tenant, req, budget, limit, baseTime, h2End)
			} else {
				h1, h2 = legacyPair(t, st, tenant, req, budget, limit, baseTime, baseTime.Add(5*time.Minute), h2End, true)
				if err := m.Release(ctx, tenant, h1.String()); err != nil {
					t.Fatal(err)
				}
			}

			if got := holdState(t, m, tenant, h1); got != resvStateReleased {
				t.Fatalf("the first settlement did not commit: %q", got)
			}
			spendBefore := holdState(t, m, tenant, h2)
			if failSecond && spendBefore != resvStateActive {
				t.Fatalf("the second settlement did not leave a live hold: %q", spendBefore)
			}
			if !failSecond && spendBefore != resvStateReleased {
				t.Fatalf("the positive settlement was incomplete: %q", spendBefore)
			}

			if !failSecond {
				recovered, err := m.Reserve(ctx, tenant, req)
				assertTakenOver(t, m, st, tenant, req, recovered, err, h1, h2)
				report, err := m.InspectReservations(ctx, tenant)
				if err != nil {
					t.Fatal(err)
				}
				if got := holdState(t, m, tenant, h2); got != resvStateReleased || report.Active != 2 {
					t.Fatalf("recovery left the predecessor spend hold %s and %d active rows; "+
						"want that hold released and only the two rows of the current admission", got, report.Active)
				}
				return
			}

			// Inside the window the pair holds its key back: the call is refused, and its
			// only write is its refusal's audit record.
			fault := withFault(m, map[int]bool{}, func() { assertKeyBusy(t, m, st, tenant, req) })
			if fault.calls != 1 {
				t.Fatalf("the refused call opened %d write(s), want only its refusal's audit record", fault.calls)
			}
			row := admissionRowOf(t, m, tenant, req.IdempotencyKey)
			if row.state != admStateReserved || row.handle != h1 || row.spendHandle != h2 || len(row.owed) != 0 {
				t.Fatalf("the row is %s naming %q/%q owing %v, want the pair as it was", row.state, row.handle, row.spendHandle, row.owed)
			}
			report, err := m.InspectReservations(ctx, tenant)
			if err != nil {
				t.Fatal(err)
			}
			if got := holdState(t, m, tenant, h2); got != resvStateActive || report.Active != 1 {
				t.Fatalf("the seat hold is %s with %d active row(s); want it withholding, alone", got, report.Active)
			}

			// After the seat hold lapses, the key is taken over into one new hold.
			clk.t = h2End.Add(time.Second)
			recovered, err := m.Reserve(ctx, tenant, req)
			assertTakenOver(t, m, st, tenant, req, recovered, err, h1, h2)
			if got := holdState(t, m, tenant, h2); got != resvStateActive {
				t.Fatalf("the lapsed seat hold was rewritten as %q; its expiry is what happened to it", got)
			}
			if job := reconcileOK(t, m, tenant); job.SweptExpired != 1 {
				t.Fatalf("the reconciliation swept %d row(s), want the lapsed seat hold", job.SweptExpired)
			}
			assertRowsUnder(t, st, tenant, h2, 1, resvStateExpired, 0, clk.t)
		})
	}
}

// TestRecoveringAPartlyExpiredPairReleasesOnlyItsLiveHold: outside its window a pair
// whose budget hold lapsed and whose seat hold outlived it is taken over into one new
// hold. The live seat hold is released, because no admission names it any more; the
// lapsed budget hold is left exactly as it is, because its expiry is what happened to it.
func TestRecoveringAPartlyExpiredPairReleasesOnlyItsLiveHold(t *testing.T) {
	forEachAdmissionEngine(t, runRecoveringAPartlyExpiredPairReleasesOnlyItsLiveHold)
}

func runRecoveringAPartlyExpiredPairReleasesOnlyItsLiveHold(t *testing.T, cfg store.Config) {
	m, st, tenant, _ := openFinCfg(t, cfg)
	clk := &fakeClock{t: baseTime}
	m.clock = clk
	ctx := context.Background()
	req, budget, limit := pairedRequest(t, st, tenant, "model_gateway/partly-expired-pair")
	h1, h2 := legacyPair(t, st, tenant, req, budget, limit, baseTime, baseTime.Add(5*time.Minute), baseTime.Add(20*time.Minute), true)
	clk.advance(admissionReplayWindow + time.Minute)

	if n := withholdingHolds(t, m, tenant); n != 1 {
		t.Fatalf("the fixture wanted one lapsed and one live hold, %d are withholding money", n)
	}
	recovered, err := m.Reserve(ctx, tenant, req)
	assertTakenOver(t, m, st, tenant, req, recovered, err, h1, h2)
	if got := holdState(t, m, tenant, h1); got != resvStateActive {
		t.Fatalf("the lapsed hold was rewritten as %q; its expiry is what happened to it", got)
	}
	if got := holdState(t, m, tenant, h2); got != resvStateReleased {
		t.Fatalf("the live half of a partly expired pair is %q, want released: it withholds money "+
			"that no admission points at", got)
	}
	if n := withholdingHolds(t, m, tenant); n != 2 {
		t.Fatalf("%d holds withhold money after recovery, want only the two of the current admission", n)
	}
}

// TestTheSurvivingHoldOfAPairIsNeverReleasedEarly: a call on a half-settled pair inside
// its window reaches no recovery write at all — the write that could not open is never
// asked for — and is refused. The row still names both holds and owes nothing, and the
// seat hold still withholds. The key stays busy until the seat hold lapses; then the next
// call takes it over into one new hold. The seat hold is never released: it lapses, and
// the sweep records its expiry with an actual of zero.
func TestTheSurvivingHoldOfAPairIsNeverReleasedEarly(t *testing.T) {
	forEachAdmissionEngine(t, runTheSurvivingHoldOfAPairIsNeverReleasedEarly)
}

func runTheSurvivingHoldOfAPairIsNeverReleasedEarly(t *testing.T, cfg store.Config) {
	m, st, tenant, _ := openFinCfg(t, cfg)
	clk := &fakeClock{t: baseTime}
	m.clock = clk
	req, budget, limit := pairedRequest(t, st, tenant, "model_gateway/unreleasable-recovery")
	h2End := baseTime.Add(290 * time.Second)
	h1, h2 := halfSettledPair(t, st, tenant, req, budget, limit, baseTime, h2End)

	var refused Reservation
	var rerr error
	fault := withFault(m, map[int]bool{2: true}, func() {
		ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
		refused, rerr = m.Reserve(ctx, tenant, req)
		cancel()
	})
	if fault.fired != 0 || fault.calls != 1 {
		t.Fatalf("the busy key opened %d write(s) and reached the unavailable one %d time(s); "+
			"want only its refusal's audit record", fault.calls, fault.fired)
	}
	if refused.Allowed {
		t.Fatalf("a caller was admitted while the money the key owes is unaccounted for: %+v (err=%v)", refused, rerr)
	}
	row := admissionRowOf(t, m, tenant, req.IdempotencyKey)
	if row.state != admStateReserved || len(row.owed) != 0 || row.owedErr != nil {
		t.Fatalf("the row is %q owing %v: a busy key is not written", row.state, row.owed)
	}
	if row.handle != h1 || row.spendHandle != h2 {
		t.Fatalf("the retained record names %q/%q, not the pair the key owned (%s/%s)", row.handle, row.spendHandle, h1, h2)
	}
	if got := holdState(t, m, tenant, h2); got != resvStateActive {
		t.Fatalf("the fixture expected the hold to be still live, got %q", got)
	}

	// Busy until the seat hold lapses, then recovered under one new hold.
	clk.t = baseTime.Add(150 * time.Second)
	assertKeyBusy(t, m, st, tenant, req)
	clk.t = h2End.Add(time.Second)
	retried, err := m.Reserve(context.Background(), tenant, req)
	assertTakenOver(t, m, st, tenant, req, retried, err, h1, h2)
	if got := holdState(t, m, tenant, h2); got != resvStateActive {
		t.Fatalf("the seat hold is %q after the takeover; it lapsed, and nothing released it", got)
	}
	if job := reconcileOK(t, m, tenant); job.SweptExpired != 1 {
		t.Fatalf("the reconciliation swept %d row(s), want the lapsed seat hold", job.SweptExpired)
	}
	assertRowsUnder(t, st, tenant, h2, 1, resvStateExpired, 0, clk.t)
	if n := withholdingHolds(t, m, tenant); n != 2 {
		t.Fatalf("%d holds withhold money, want only the two of the recovered admission", n)
	}
}
