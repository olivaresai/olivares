// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package finops

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// -----------------------------------------------------------------------------
// TAKING A KEY OVER. A claim whose caller is gone is taken over, and the takeover owes
// every hold the row named; the new claimant's create settles them before it holds. A
// caller that lost its key while it evaluated writes nothing more, and is answered from
// the row that now owns the key.
// -----------------------------------------------------------------------------

type pausedReserveOutcome struct {
	res Reservation
	err error
}

// publishAfterObservedPendingRead acts as the caller that holds a pending claim: once
// the waiter has COMPLETED the read that returned the pending row, it creates and
// publishes the claim's hold through the real writers. It returns the hold, or "" if the
// holder could not reach its verdict — which a caller must check, because a test whose
// fixture failed proves nothing.
func publishAfterObservedPendingRead(t *testing.T, m *Module, tenant model.TenantID, req AdmissionRequest, d *pausedReadData) <-chan string {
	t.Helper()
	ctx := context.Background()
	finished := make(chan string, 1)
	go func() {
		defer close(d.resume)
		<-d.reached
		row, found, err := m.readRow(ctx, tenant, req.IdempotencyKey)
		if err != nil || !found {
			finished <- ""
			return
		}
		now := m.clock.Now().Time()
		budgets, _, err := m.budgetTargets(ctx, tenant, req.Dims, now)
		if err != nil {
			finished <- ""
			return
		}
		out, w, err := m.create(ctx, tenant, row.token(), row.handle, budgets, nil, req.EstimateMicroUSD, now)
		if err != nil || w != writeCommitted || out.denied {
			finished <- ""
			return
		}
		if w, err := m.publish(ctx, tenant, out.tok, row.handle, out.issued); err != nil || w != writeCommitted {
			finished <- ""
			return
		}
		finished <- row.handle.String()
	}()
	return finished
}

func awaitPausedReserve(t *testing.T, done <-chan pausedReserveOutcome) pausedReserveOutcome {
	t.Helper()
	select {
	case got := <-done:
		return got
	case <-time.After(10 * time.Second):
		t.Fatal("the paused caller never finished")
	}
	return pausedReserveOutcome{}
}

// TestTakeoverSettlesPredecessorInCreate: a caller that created its hold and was paused
// before publishing loses the key to a caller that takes the stale claim over. The
// successor's create releases the predecessor's hold in the same transaction that holds
// its own, and publishes; the resumed predecessor finds the key moved, writes nothing,
// and is answered with the successor's hold.
func TestTakeoverSettlesPredecessorInCreate(t *testing.T) {
	forEachAdmissionEngine(t, runTakeoverSettlesPredecessorInCreate)
}

func runTakeoverSettlesPredecessorInCreate(t *testing.T, cfg store.Config) {
	m, st, tenant, _ := openFinCfg(t, cfg)
	clk := &fakeClock{t: baseTime}
	m.clock = clk
	seedBudgetAndSeatLimit(t, st, tenant)
	req := seatRequest("model_gateway/predecessor")
	d := &pausedMutateData{ModuleData: m.data, nth: 3, reached: make(chan struct{}), resume: make(chan struct{})}
	m.data = d
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	done := make(chan pausedReserveOutcome, 1)
	go func() {
		r, e := m.Reserve(pausedCtx(ctx), tenant, req)
		done <- pausedReserveOutcome{r, e}
	}()
	awaitPausedMutate(t, d)
	predecessor := admissionRowOf(t, m, tenant, req.IdempotencyKey).handle
	if n := len(activeRowsUnder(t, st, tenant, predecessor)); n != 2 {
		close(d.resume)
		t.Fatalf("fixture: the predecessor's create left %d active row(s), want both components", n)
	}
	clk.advance(admissionClaimTakeover + time.Second)
	successor, err := m.Reserve(ctx, tenant, req)
	close(d.resume)
	delayed := awaitPausedReserve(t, done)

	if err != nil || !successor.Allowed || successor.Handle == "" || successor.Handle == predecessor.String() {
		t.Fatalf("the successor must be admitted under a hold of its own: %+v err=%v", successor, err)
	}
	if delayed.err != nil || !delayed.res.Allowed || !delayed.res.Replayed || delayed.res.Handle != successor.Handle {
		t.Fatalf("the predecessor was answered %+v err=%v, want a replay of %s", delayed.res, delayed.err, successor.Handle)
	}
	row := admissionRowOf(t, m, tenant, req.IdempotencyKey)
	if row.state != admStateReserved || row.handle.String() != successor.Handle || len(row.owed) != 0 {
		t.Fatalf("the row is %+v, want the successor's admission owing nothing", row)
	}
	for _, r := range ledgerRowsUnder(t, st, tenant, predecessor) {
		if r.String(colResvState) != resvStateReleased || r.Int(colResvActual) != 0 {
			t.Fatalf("a predecessor row is %s with actual %d, want released with 0", r.String(colResvState), r.Int(colResvActual))
		}
	}
	if n := len(activeRowsUnder(t, st, tenant, holdID(successor.Handle))); n != 2 {
		t.Fatalf("the successor holds %d active row(s), want both components", n)
	}
	if n := d.writes.Load(); n != 3 {
		t.Fatalf("the predecessor wrote %d times, want its claim, its create and the one publication the fence refused", n)
	}
}

// TestTwoStaleReadersOfOnePendingClaimLeaveOneHold: two callers read the same abandoned
// claim and each may take it over — exactly one does. The other is answered with the
// winner's hold, and the key holds once.
func TestTwoStaleReadersOfOnePendingClaimLeaveOneHold(t *testing.T) {
	forEachAdmissionEngine(t, runTwoStaleReadersOfOnePendingClaimLeaveOneHold)
}

func runTwoStaleReadersOfOnePendingClaimLeaveOneHold(t *testing.T, cfg store.Config) {
	m, st, tenant, _ := openFinCfg(t, cfg)
	clk := &fakeClock{t: baseTime}
	m.clock = clk
	createBudget(t, st, tenant, "cap", budgetSpec{Dimension: "global", Period: "monthly", LimitMicroUSD: 10 * oneUSD, Action: "block"})
	req := AdmissionRequest{Scope: AdmissionScopeModelGateway, IdempotencyKey: "model_gateway/abandoned-claim", EstimateMicroUSD: oneUSD}
	stagePendingClaim(t, m, tenant, req)
	clk.advance(admissionClaimTakeover + time.Second)

	d := &pausedReadData{ModuleData: m.data, nth: 1, reached: make(chan struct{}), resume: make(chan struct{})}
	m.data = d
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	first := make(chan pausedReserveOutcome, 1)
	go func() {
		r, e := m.Reserve(pausedCtx(ctx), tenant, req)
		first <- pausedReserveOutcome{r, e}
	}()
	awaitPausedRead(t, d)
	second, err := m.Reserve(ctx, tenant, req)
	close(d.resume)
	delayed := awaitPausedReserve(t, first)

	if err != nil || delayed.err != nil || !second.Allowed || !delayed.res.Allowed {
		t.Fatalf("both stale readers must be answered: delayed=%+v second=%+v err=%v", delayed, second, err)
	}
	if delayed.res.Handle != second.Handle || ledgerCounts(t, st, tenant, clk.t).Active != 1 {
		t.Fatalf("one stale key produced conflicting leaders: %s and %s", second.Handle, delayed.res.Handle)
	}
}

// TestTheOriginalClaimantDoesNotReplaceItsSuccessor: the caller that staged the claim is
// its owner until the claim is believed dead; once a successor has taken the key over,
// that caller resuming is a stranger. It does not publish over the successor, and it
// never took a hold on its way there: its create was fenced.
func TestTheOriginalClaimantDoesNotReplaceItsSuccessor(t *testing.T) {
	forEachAdmissionEngine(t, runTheOriginalClaimantDoesNotReplaceItsSuccessor)
}

func runTheOriginalClaimantDoesNotReplaceItsSuccessor(t *testing.T, cfg store.Config) {
	m, st, tenant, _ := openFinCfg(t, cfg)
	clk := &fakeClock{t: baseTime}
	m.clock = clk
	createBudget(t, st, tenant, "cap", budgetSpec{Dimension: "global", Period: "monthly", LimitMicroUSD: 10 * oneUSD, Action: "block"})
	req := AdmissionRequest{Scope: AdmissionScopeModelGateway, IdempotencyKey: "model_gateway/original-owner", EstimateMicroUSD: oneUSD}

	d := &pausedReadData{ModuleData: m.data, nth: 2, reached: make(chan struct{}), resume: make(chan struct{})}
	m.data = d
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	original := make(chan pausedReserveOutcome, 1)
	go func() {
		r, e := m.Reserve(pausedCtx(ctx), tenant, req)
		original <- pausedReserveOutcome{r, e}
	}()
	awaitPausedRead(t, d)
	clk.advance(admissionClaimTakeover + time.Second)
	successor, err := m.Reserve(ctx, tenant, req)
	close(d.resume)
	delayed := awaitPausedReserve(t, original)

	if err != nil || !successor.Allowed || successor.Handle == "" {
		t.Fatalf("the successor must be admitted: %+v %v", successor, err)
	}
	row := admissionRowOf(t, m, tenant, req.IdempotencyKey)
	if row.handle.String() != successor.Handle || ledgerCounts(t, st, tenant, clk.t).Active != 1 {
		t.Fatalf("the original claimant overwrote its successor: successor=%s stored=%s delayed=%+v",
			successor.Handle, row.handle, delayed)
	}
}

// TestAFreshlyTakenOverClaimCannotBeStolenAtOnce: a takeover goes from pending to
// pending — the owner changes, not the state — so the new claim is dated at its own
// acquisition. A claim that inherited its dead owner's date would be born stale and be
// taken from a claimant that is alive and mid-evaluation.
func TestAFreshlyTakenOverClaimCannotBeStolenAtOnce(t *testing.T) {
	forEachAdmissionEngine(t, runAFreshlyTakenOverClaimCannotBeStolenAtOnce)
}

func runAFreshlyTakenOverClaimCannotBeStolenAtOnce(t *testing.T, cfg store.Config) {
	m, st, tenant, _ := openFinCfg(t, cfg)
	clk := &fakeClock{t: baseTime}
	m.clock = clk
	ctx := context.Background()
	createBudget(t, st, tenant, "cap", budgetSpec{
		Dimension: "global", Period: "monthly", LimitMicroUSD: 10 * oneUSD, Action: "block",
	})
	req := AdmissionRequest{
		Scope: AdmissionScopeModelGateway, EstimateMicroUSD: oneUSD,
		IdempotencyKey: "model_gateway/recovered-by-a-live-caller",
	}

	stagePendingClaim(t, m, tenant, req)
	clk.advance(admissionClaimTakeover + time.Second)
	abandoned := admissionRowOf(t, m, tenant, req.IdempotencyKey)
	if !m.staleClaim(abandoned) {
		t.Fatalf("the fixture did not produce a stale claim to recover: %+v", abandoned)
	}
	next := abandoned
	next.owed = owedHolds{abandoned.handle}
	next.handle = newHoldID()
	next.stateAt = m.clock.Now()
	if _, w, err := m.takeOver(ctx, tenant, abandoned, next); w != writeCommitted || err != nil {
		t.Fatalf("take the abandoned claim over: %v", err)
	}
	recovered := admissionRowOf(t, m, tenant, req.IdempotencyKey)
	if m.staleClaim(recovered) {
		t.Fatalf("the claim was born stale: it inherited the date of the owner it replaced (%+v)", recovered)
	}

	d := &pausedReadData{ModuleData: m.data, nth: 1, reached: make(chan struct{}), resume: make(chan struct{})}
	m.data = d
	finished := publishAfterObservedPendingRead(t, m, tenant, req, d)

	res, err := m.Reserve(pausedCtx(ctx), tenant, req)
	claimed := <-finished
	if err != nil {
		t.Fatalf("the waiter errored: %v", err)
	}
	if claimed == "" {
		t.Fatal("the recovering caller could not reach its verdict; the test proves nothing")
	}
	if !res.Allowed || !res.Replayed || res.Handle != claimed {
		t.Fatalf("a caller stole a claim taken over a moment ago: got %+v, want the recovering caller's hold %q", res, claimed)
	}
	if got := ledgerCounts(t, st, tenant, clk.t); got.Active != 1 {
		t.Fatalf("one recovered key left %+v, want exactly 1 active row", got)
	}
}

// TestManyCallersOnOneStaleKeyLeaveOneHoldAndOneAnswer: every caller reads the same
// abandoned claim and may recover it; exactly one takes it over, the rest are handed its
// hold, and the key holds once.
func TestManyCallersOnOneStaleKeyLeaveOneHoldAndOneAnswer(t *testing.T) {
	forEachAdmissionEngine(t, runManyCallersOnOneStaleKeyLeaveOneHoldAndOneAnswer)
}

func runManyCallersOnOneStaleKeyLeaveOneHoldAndOneAnswer(t *testing.T, cfg store.Config) {
	m, st, tenant, _ := openFinCfg(t, cfg)
	clk := &fakeClock{t: baseTime}
	m.clock = clk
	ctx := context.Background()
	createBudget(t, st, tenant, "cap", budgetSpec{
		Dimension: "global", Period: "monthly", LimitMicroUSD: 10 * oneUSD, Action: "block",
	})
	req := AdmissionRequest{
		Scope: AdmissionScopeModelGateway, EstimateMicroUSD: oneUSD,
		IdempotencyKey: "model_gateway/one-key-many-recoverers",
	}
	stagePendingClaim(t, m, tenant, req)
	clk.advance(admissionClaimTakeover + time.Second)

	const N = 16
	var wg sync.WaitGroup
	start := make(chan struct{})
	answers := make([]Reservation, N)
	errs := make([]error, N)
	wg.Add(N)
	for i := 0; i < N; i++ {
		go func() {
			defer wg.Done()
			<-start
			answers[i], errs[i] = m.Reserve(ctx, tenant, req)
		}()
	}
	close(start)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("caller %d errored: %v", i, err)
		}
		if !answers[i].Allowed || answers[i].Handle == "" {
			t.Fatalf("caller %d was refused a key with headroom: %+v", i, answers[i])
		}
		if answers[i].Handle != answers[0].Handle {
			t.Fatalf("callers %d and 0 recovered one key into different holds (%s and %s)", i, answers[i].Handle, answers[0].Handle)
		}
	}
	if got := ledgerCounts(t, st, tenant, clk.t); got.Active != 1 {
		t.Fatalf("%d callers of one key left %+v, want exactly 1 active row", N, got)
	}
	row := admissionRowOf(t, m, tenant, req.IdempotencyKey)
	if row.state != admStateReserved || row.handle.String() != answers[0].Handle {
		t.Fatalf("the row is %s naming %s, want reserved naming %s", row.state, row.handle, answers[0].Handle)
	}
}

// TestAnUndatedReservedRowDoesNotOrphanItsLiveHold: a row written before rows were dated
// cannot be shown to be a young retry, so it is evaluated afresh — and the hold it still
// names is released by the new claim's create rather than left withholding beside a
// second one.
func TestAnUndatedReservedRowDoesNotOrphanItsLiveHold(t *testing.T) {
	forEachAdmissionEngine(t, runAnUndatedReservedRowDoesNotOrphanItsLiveHold)
}

func runAnUndatedReservedRowDoesNotOrphanItsLiveHold(t *testing.T, cfg store.Config) {
	m, st, tenant, _ := openFinCfg(t, cfg)
	m.clock = &fakeClock{t: baseTime}
	createBudget(t, st, tenant, "cap", budgetSpec{Dimension: "global", Period: "monthly", LimitMicroUSD: 10 * oneUSD, Action: "block"})
	req := AdmissionRequest{Scope: AdmissionScopeModelGateway, IdempotencyKey: "model_gateway/undated-row", EstimateMicroUSD: oneUSD}
	ctx := context.Background()

	first, err := m.Reserve(ctx, tenant, req)
	if err != nil || !first.Allowed || first.Handle == "" {
		t.Fatalf("first: %+v %v", first, err)
	}
	setStoredCell(t, st, tenant, req.IdempotencyKey, colAdmStateAt, nil)
	if row := admissionRowOf(t, m, tenant, req.IdempotencyKey); !row.stateAt.IsZero() {
		t.Fatalf("the undated row was not established: %+v", row)
	}

	again, err := m.Reserve(ctx, tenant, req)
	if err != nil || !again.Allowed || again.Replayed || again.Handle == first.Handle {
		t.Fatalf("retry: %+v %v; want a new evaluation", again, err)
	}
	if got := ledgerCounts(t, st, tenant, baseTime); got.Active != 1 || got.Released != 1 {
		t.Fatalf("the undated retry left %+v, want the earlier hold released and one active", got)
	}
}

// TestADelayedAbandonmentDoesNotClearItsSuccessorsAdmission: a caller whose claim was
// taken over while it evaluated reaches a verdict for a key it no longer holds. Giving
// that claim up would clear the successor's admission; the caller instead finds the key
// moved and is answered from the successor's row.
func TestADelayedAbandonmentDoesNotClearItsSuccessorsAdmission(t *testing.T) {
	forEachAdmissionEngine(t, runADelayedAbandonmentDoesNotClearItsSuccessorsAdmission)
}

func runADelayedAbandonmentDoesNotClearItsSuccessorsAdmission(t *testing.T, cfg store.Config) {
	m, st, tenant, _ := openFinCfg(t, cfg)
	clk := &fakeClock{t: baseTime}
	m.clock = clk
	createBudget(t, st, tenant, "cap", budgetSpec{Dimension: "global", Period: "monthly", LimitMicroUSD: 3 * oneUSD / 2, Action: "block"})
	req := AdmissionRequest{Scope: AdmissionScopeModelGateway, IdempotencyKey: "model_gateway/late-abandonment", EstimateMicroUSD: oneUSD}

	d := &pausedReadData{ModuleData: m.data, nth: 2, reached: make(chan struct{}), resume: make(chan struct{})}
	m.data = d
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	original := make(chan pausedReserveOutcome, 1)
	go func() {
		r, e := m.Reserve(pausedCtx(ctx), tenant, req)
		original <- pausedReserveOutcome{r, e}
	}()
	awaitPausedRead(t, d)
	clk.advance(admissionClaimTakeover + time.Second)
	successor, err := m.Reserve(ctx, tenant, req)
	close(d.resume)
	delayed := awaitPausedReserve(t, original)

	if err != nil || !successor.Allowed || successor.Handle == "" {
		t.Fatalf("the successor must be admitted: %+v %v", successor, err)
	}
	if delayed.err != nil || !delayed.res.Allowed || !delayed.res.Replayed || delayed.res.Handle != successor.Handle {
		t.Fatalf("the delayed caller was answered %+v err=%v, want a replay of %s", delayed.res, delayed.err, successor.Handle)
	}
	row := admissionRowOf(t, m, tenant, req.IdempotencyKey)
	if row.state != admStateReserved || row.handle.String() != successor.Handle {
		t.Fatalf("a delayed abandonment cleared its successor's admission: %s naming %s", row.state, row.handle)
	}
	if got := ledgerCounts(t, st, tenant, clk.t); got != (ledgerCount{Active: 1}) {
		t.Fatalf("a delayed abandonment moved the ledger: %+v", got)
	}
}

// TestAFailedPublicationReturnsItsOwnHoldAndKeepsTheSuccessors: a caller that loses its
// key while it evaluates must not keep money the successor does not own, and must not
// touch the successor's. Its create is fenced, so it never takes a hold it could not
// publish: nothing of its own is left withholding, and the successor's admission stands.
func TestAFailedPublicationReturnsItsOwnHoldAndKeepsTheSuccessors(t *testing.T) {
	forEachAdmissionEngine(t, runAFailedPublicationReturnsItsOwnHoldAndKeepsTheSuccessors)
}

func runAFailedPublicationReturnsItsOwnHoldAndKeepsTheSuccessors(t *testing.T, cfg store.Config) {
	m, st, tenant, _ := openFinCfg(t, cfg)
	clk := &fakeClock{t: baseTime}
	m.clock = clk
	createBudget(t, st, tenant, "cap", budgetSpec{Dimension: "global", Period: "monthly", LimitMicroUSD: 10 * oneUSD, Action: "block"})
	req := AdmissionRequest{Scope: AdmissionScopeModelGateway, IdempotencyKey: "model_gateway/unpublishable-verdict", EstimateMicroUSD: oneUSD}

	d := &pausedReadData{ModuleData: m.data, nth: 2, reached: make(chan struct{}), resume: make(chan struct{})}
	m.data = d
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	original := make(chan pausedReserveOutcome, 1)
	go func() {
		r, e := m.Reserve(pausedCtx(ctx), tenant, req)
		original <- pausedReserveOutcome{r, e}
	}()
	awaitPausedRead(t, d)
	intent := admissionRowOf(t, m, tenant, req.IdempotencyKey).handle
	clk.advance(admissionClaimTakeover + time.Second)
	successor, err := m.Reserve(ctx, tenant, req)
	close(d.resume)
	delayed := awaitPausedReserve(t, original)

	if err != nil || !successor.Allowed || successor.Handle == "" {
		t.Fatalf("the successor must be admitted: %+v %v", successor, err)
	}
	if delayed.err != nil || !delayed.res.Allowed || !delayed.res.Replayed || delayed.res.Handle != successor.Handle {
		t.Fatalf("the caller that lost its key was answered from its own verdict: %+v err=%v, want a replay of %s",
			delayed.res, delayed.err, successor.Handle)
	}
	if rows := ledgerRowsUnder(t, st, tenant, intent); len(rows) != 0 {
		t.Fatalf("the caller that lost its key holds %d row(s) under its claim", len(rows))
	}
	if got := ledgerCounts(t, st, tenant, clk.t); got != (ledgerCount{Active: 1}) {
		t.Fatalf("the ledger holds %+v, want only the successor's row", got)
	}
}

// legacyPair seeds, for req's key, the pair an earlier build published at tp: a budget
// hold h1 whose one row expires at h1End and a seat hold h2 whose one row expires at
// h2End, each of req's estimate under budget and limit, both created before the row was
// dated. With dated false the row is undated.
func legacyPair(t *testing.T, st store.Store, tenant model.TenantID, req AdmissionRequest, budget, limit model.ID, tp, h1End, h2End time.Time, dated bool) (holdID, holdID) {
	t.Helper()
	h1, h2 := newHoldID(), newHoldID()
	created := tp.Add(-time.Second)
	seedReservation(t, st, tenant, ledgerRow(budget, "b", h1, 1, req.EstimateMicroUSD, created, h1End, resvStateActive))
	seedReservation(t, st, tenant, ledgerRow(limit, "s", h2, 1, req.EstimateMicroUSD, created, h2End, resvStateActive))
	stateAt := tp
	if !dated {
		stateAt = time.Time{}
	}
	seedAdmission(t, st, tenant, admissionRecordFor(req, admStateReserved, h1, h2, stateAt))
	return h1, h2
}

// withheld sums, per component, what the tenant's ledger withholds at now: the amounts
// of the active rows whose expiry has not passed.
func withheld(t *testing.T, st store.Store, tenant model.TenantID, now time.Time) (budget, seat int64) {
	t.Helper()
	for _, r := range countReservations(t, st, tenant) {
		if r.String(colResvState) != resvStateActive {
			continue
		}
		exp, err := model.ParseTimestamp(r.String(colResvExpiresAt))
		if err != nil || !exp.Time().After(now) {
			continue
		}
		switch r.String(colResvPolicyKind) {
		case policyKindBudget:
			budget += r.Int(colResvAmount)
		case policyKindSpendLimit:
			seat += r.Int(colResvAmount)
		}
	}
	return budget, seat
}

// assertBusyAnswer asserts the answer a caller gets for a key it may neither replay nor
// take over when it stops waiting: the refusal of a key in flight, with no hold.
func assertBusyAnswer(t *testing.T, res Reservation, err error) {
	t.Helper()
	if err != nil || res.Allowed || res.Replayed || res.Handle != "" || res.Reason != ReasonStoreUnreachable {
		t.Fatalf("a key held back was answered %+v err=%v; want the refusal of a key in flight", res, err)
	}
}

// assertKeyUnwritten asserts that the row of key is as before and the ledger as ledger.
func assertKeyUnwritten(t *testing.T, m *Module, st store.Store, tenant model.TenantID, key string, before admissionRow, ledger map[string]model.Record) {
	t.Helper()
	after := admissionRowOf(t, m, tenant, key)
	if after.version != before.version || after.state != before.state || after.handle != before.handle ||
		after.spendHandle != before.spendHandle || len(after.owed) != len(before.owed) {
		t.Fatalf("the key's row was written: before %+v, after %+v", before, after)
	}
	assertRowsUnchanged(t, "the ledger", ledger, rowsByID(t, st, tenant, budgetReservationKind))
}

// assertKeyBusy asks for req's key with a short deadline, as a caller that waits no
// longer would, and asserts the busy answer and that nothing was written.
func assertKeyBusy(t *testing.T, m *Module, st store.Store, tenant model.TenantID, req AdmissionRequest) {
	t.Helper()
	before := admissionRowOf(t, m, tenant, req.IdempotencyKey)
	ledger := rowsByID(t, st, tenant, budgetReservationKind)
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	res, err := m.Reserve(ctx, tenant, req)
	cancel()
	assertBusyAnswer(t, res, err)
	assertKeyUnwritten(t, m, st, tenant, req.IdempotencyKey, before, ledger)
}

// assertTakenOver asserts that res is a new admission of req's key under a hold of its
// own, neither half of the pair h1, h2, holding both components, and that its row owes
// nothing.
func assertTakenOver(t *testing.T, m *Module, st store.Store, tenant model.TenantID, req AdmissionRequest, res Reservation, err error, h1, h2 holdID) {
	t.Helper()
	if err != nil || !res.Allowed || res.Replayed || res.Handle == "" || res.Handle == h1.String() || res.Handle == h2.String() {
		t.Fatalf("the key must be taken over under a new hold: %+v err=%v", res, err)
	}
	row := admissionRowOf(t, m, tenant, req.IdempotencyKey)
	if row.state != admStateReserved || row.handle.String() != res.Handle || !row.spendHandle.isZero() || len(row.owed) != 0 {
		t.Fatalf("the taken-over row is %s naming %s and %q owing %d", row.state, row.handle, row.spendHandle, len(row.owed))
	}
	rows := activeRowsUnder(t, st, tenant, holdID(res.Handle))
	if kinds := componentsOf(rows); len(rows) != 2 || kinds[policyKindBudget] != 1 || kinds[policyKindSpendLimit] != 1 {
		t.Fatalf("the new hold has %d active row(s) %v, want one of each component", len(rows), kinds)
	}
}

// incompleteHoldWrites serves the marked caller's write transactions with a ledger whose
// scan of one hold ends on a page that promises more rows and gives no cursor.
type incompleteHoldWrites struct {
	api.ModuleData
	hold holdID
}

func (d incompleteHoldWrites) Mutate(ctx context.Context, tenant model.TenantID, fn func(store.Scope) error) error {
	if !marked(ctx) {
		return d.ModuleData.Mutate(ctx, tenant, fn)
	}
	return d.ModuleData.Mutate(ctx, tenant, func(sc store.Scope) error {
		return fn(incompleteHoldScope{Scope: sc, hold: d.hold})
	})
}

// incompleteHoldScope forwards the transaction lock and clock of the scope it wraps.
type incompleteHoldScope struct {
	store.Scope
	hold holdID
}

func (s incompleteHoldScope) LockTransaction(ctx context.Context, key string) error {
	locker, ok := s.Scope.(store.TransactionLocker)
	if !ok {
		return errors.New("finops-test: wrapped scope provides no transaction lock")
	}
	return locker.LockTransaction(ctx, key)
}

func (s incompleteHoldScope) TransactionNow(ctx context.Context) (model.Timestamp, error) {
	clock, ok := s.Scope.(store.TransactionClock)
	if !ok {
		return model.Timestamp{}, errors.New("finops-test: wrapped scope provides no transaction clock")
	}
	return clock.TransactionNow(ctx)
}

func (s incompleteHoldScope) Ext(kind model.Kind) (store.GenericRepo, error) {
	repo, err := s.Scope.Ext(kind)
	if err != nil || kind != budgetReservationKind {
		return repo, err
	}
	return incompleteHoldRepo{GenericRepo: repo, hold: s.hold}, nil
}

type incompleteHoldRepo struct {
	store.GenericRepo
	hold holdID
}

func (r incompleteHoldRepo) List(ctx context.Context, q model.Query) ([]model.Record, model.Page, error) {
	recs, page, err := r.GenericRepo.List(ctx, q)
	for _, f := range q.Filters {
		if f.Column == colResvHandle && f.Value == r.hold.String() {
			return recs, model.Page{HasMore: true}, err
		}
	}
	return recs, page, err
}

// TestLegacyHalfWithholdingWaits: a pair an earlier build published, dated inside its
// replay window, with one hold lapsed and the other still withholding, is neither
// replayed — a replay needs every hold that has rows to withhold — nor taken over, in
// either lapse order: the key is busy, and a caller that stops waiting meets the refusal
// of a key in flight, with nothing written. A caller still waiting when the other hold
// lapses takes the key over. The takeover reads both holds again in its own transaction
// and at its own instant, and that read decides; if it cannot read a hold completely it
// refuses deny-closed and writes nothing.
func TestLegacyHalfWithholdingWaits(t *testing.T) {
	forEachAdmissionEngine(t, runLegacyHalfWithholdingWaits)
}

func runLegacyHalfWithholdingWaits(t *testing.T, cfg store.Config) {
	tp := baseTime
	early, late := tp.Add(100*time.Second), tp.Add(290*time.Second)
	setup := func(t *testing.T, h1End, h2End time.Time) (*Module, store.Store, model.TenantID, AdmissionRequest, holdID, holdID) {
		t.Helper()
		m, st, tenant, _ := openFinCfg(t, cfg)
		budget, limit := seedBudgetAndSeatLimit(t, st, tenant)
		req := seatRequest("model_gateway/legacy-pair")
		h1, h2 := legacyPair(t, st, tenant, req, budget, limit, tp, h1End, h2End, true)
		return m, st, tenant, req, h1, h2
	}

	for _, tc := range []struct {
		name         string
		h1End, h2End time.Time
		budget, seat int64
	}{
		{"the budget hold lapsed first", early, late, 0, 2 * oneUSD},
		{"the seat hold lapsed first", late, early, 2 * oneUSD, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, st, tenant, req, _, _ := setup(t, tc.h1End, tc.h2End)
			m.clock = &fakeClock{t: tp.Add(150 * time.Second)}
			assertKeyBusy(t, m, st, tenant, req)
			if b, s := withheld(t, st, tenant, tp.Add(150*time.Second)); b != tc.budget || s != tc.seat {
				t.Fatalf("withheld %d/%d, want %d/%d", b, s, tc.budget, tc.seat)
			}
		})
	}

	t.Run("the other hold lapses while the caller waits", func(t *testing.T) {
		m, st, tenant, req, h1, h2 := setup(t, early, late)
		m.clock = &steppingClock{t: late.Add(-time.Second), step: 50 * time.Millisecond}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		res, err := m.Reserve(ctx, tenant, req)
		assertTakenOver(t, m, st, tenant, req, res, err, h1, h2)
		for _, h := range []holdID{h1, h2} {
			if n := len(activeRowsUnder(t, st, tenant, h)); n != 1 {
				t.Fatalf("a lapsed hold was rewritten by the takeover: %d active row(s) under %s", n, h)
			}
		}
		if b, s := withheld(t, st, tenant, late.Add(time.Second)); b != 2*oneUSD || s != 2*oneUSD {
			t.Fatalf("withheld %d/%d, want only the new hold's", b, s)
		}
	})

	t.Run("the takeover's own read decides", func(t *testing.T) {
		m, st, tenant, req, _, _ := setup(t, early, late)
		clk := &fakeClock{t: tp.Add(291 * time.Second)}
		m.clock = clk
		before := admissionRowOf(t, m, tenant, req.IdempotencyKey)
		ledger := rowsByID(t, st, tenant, budgetReservationKind)
		d := &pausedMutateData{ModuleData: m.data, nth: 1, reached: make(chan struct{}), resume: make(chan struct{})}
		m.data = d
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		done := make(chan pausedReserveOutcome, 1)
		go func() {
			r, e := m.Reserve(pausedCtx(ctx), tenant, req)
			done <- pausedReserveOutcome{r, e}
		}()
		awaitPausedMutate(t, d)
		// The read that chose to take the key over saw both holds lapsed; the takeover
		// runs at an instant at which the seat hold still withholds.
		clk.advance(-141 * time.Second)
		close(d.resume)
		time.AfterFunc(250*time.Millisecond, cancel)
		got := awaitPausedReserve(t, done)
		assertBusyAnswer(t, got.res, got.err)
		assertKeyUnwritten(t, m, st, tenant, req.IdempotencyKey, before, ledger)
		if b, s := withheld(t, st, tenant, tp.Add(150*time.Second)); b != 0 || s != 2*oneUSD {
			t.Fatalf("withheld %d/%d, want the seat hold's only", b, s)
		}
	})

	t.Run("a hold the takeover cannot read completely", func(t *testing.T) {
		m, st, tenant, req, _, h2 := setup(t, early, late)
		m.clock = &fakeClock{t: tp.Add(291 * time.Second)}
		before := admissionRowOf(t, m, tenant, req.IdempotencyKey)
		ledger := rowsByID(t, st, tenant, budgetReservationKind)
		base := m.data
		m.data = incompleteHoldWrites{ModuleData: base, hold: h2}
		res, err := m.Reserve(pausedCtx(context.Background()), tenant, req)
		m.data = base
		if err != nil || res.Allowed || res.Replayed || res.Handle != "" || res.Reason != ReasonStoreUnreachable {
			t.Fatalf("a takeover that cannot read a hold completely was answered %+v err=%v; want a deny-closed refusal", res, err)
		}
		assertKeyUnwritten(t, m, st, tenant, req.IdempotencyKey, before, ledger)
		if b, s := withheld(t, st, tenant, tp.Add(291*time.Second)); b != 0 || s != 0 {
			t.Fatalf("withheld %d/%d, want nothing", b, s)
		}
	})
}

// TestLegacyPairProgressesAfterBothLapse: the key a half-withholding pair held back is
// taken over as any other once both holds have lapsed inside the window: the new claim
// owes both, its create releases nothing that no longer withholds and leaves the lapsed
// rows to the sweep, and the new hold is published.
func TestLegacyPairProgressesAfterBothLapse(t *testing.T) {
	forEachAdmissionEngine(t, runLegacyPairProgressesAfterBothLapse)
}

func runLegacyPairProgressesAfterBothLapse(t *testing.T, cfg store.Config) {
	m, st, tenant, _ := openFinCfg(t, cfg)
	tp := baseTime
	clk := &fakeClock{t: tp.Add(150 * time.Second)}
	m.clock = clk
	budget, limit := seedBudgetAndSeatLimit(t, st, tenant)
	req := seatRequest("model_gateway/legacy-pair")
	h1, h2 := legacyPair(t, st, tenant, req, budget, limit, tp, tp.Add(100*time.Second), tp.Add(290*time.Second), true)

	assertKeyBusy(t, m, st, tenant, req)
	clk.advance(141 * time.Second)
	res, err := m.Reserve(context.Background(), tenant, req)
	assertTakenOver(t, m, st, tenant, req, res, err, h1, h2)
	for _, h := range []holdID{h1, h2} {
		if rows := activeRowsUnder(t, st, tenant, h); len(rows) != 1 {
			t.Fatalf("a lapsed hold was rewritten by the takeover: %d active row(s) under %s", len(rows), h)
		}
	}
	if got := ledgerCounts(t, st, tenant, clk.t); got != (ledgerCount{Active: 4, ActiveLapsed: 2}) {
		t.Fatalf("the ledger holds %+v, want the pair's two lapsed rows and the new hold's two", got)
	}
	if b, s := withheld(t, st, tenant, clk.t); b != 2*oneUSD || s != 2*oneUSD {
		t.Fatalf("withheld %d/%d, want only the new hold's", b, s)
	}
}

// TestUndatedLegacyPairIsTakenOver: an undated pair cannot be shown to be a young retry,
// so it is outside the window and never holds its key back, whatever its holds do. Its
// key is taken over; the create releases the hold that still withholds, leaves the lapsed
// one to the sweep and publishes a new hold.
func TestUndatedLegacyPairIsTakenOver(t *testing.T) {
	forEachAdmissionEngine(t, runUndatedLegacyPairIsTakenOver)
}

func runUndatedLegacyPairIsTakenOver(t *testing.T, cfg store.Config) {
	m, st, tenant, _ := openFinCfg(t, cfg)
	tp := baseTime
	now := tp.Add(150 * time.Second)
	m.clock = &fakeClock{t: now}
	budget, limit := seedBudgetAndSeatLimit(t, st, tenant)
	req := seatRequest("model_gateway/undated-pair")
	h1, h2 := legacyPair(t, st, tenant, req, budget, limit, tp, tp.Add(100*time.Second), tp.Add(290*time.Second), false)

	res, err := m.Reserve(context.Background(), tenant, req)
	assertTakenOver(t, m, st, tenant, req, res, err, h1, h2)
	if rows := activeRowsUnder(t, st, tenant, h1); len(rows) != 1 {
		t.Fatalf("the lapsed budget hold has %d active row(s), want its one row left to the sweep", len(rows))
	}
	rows := ledgerRowsUnder(t, st, tenant, h2)
	if len(rows) != 1 || rows[0].String(colResvState) != resvStateReleased || rows[0].Int(colResvActual) != 0 ||
		rows[0].String(colResvSettledAt) != model.NewTimestamp(now).String() {
		t.Fatalf("the withholding seat hold is %+v, want released with 0 at the takeover's create", rows)
	}
	if b, s := withheld(t, st, tenant, now); b != 2*oneUSD || s != 2*oneUSD {
		t.Fatalf("withheld %d/%d, want only the new hold's", b, s)
	}
}
