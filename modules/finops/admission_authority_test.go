// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package finops

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/engine/enginetest"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// pausedReadData pauses ONE caller after a real read transaction has closed. That is
// what a descheduled caller looks like to the database: its read committed and
// returned real values, and the world then moved on before it acted on them. It does
// not hold a lock open and it does not manufacture a stale value — a paused caller
// that never read the row would prove nothing about a fence that compares what the
// caller read.
//
// The caller to pause is named by a context value, so the other callers in a test
// run at full speed; nth selects WHICH of its completed reads pauses, which is how a
// test chooses the point in the decision the interleaving must attack.
type pausedCallKey struct{}
type pausedReadData struct {
	api.ModuleData
	nth     int64
	reads   atomic.Int64
	reached chan struct{}
	resume  chan struct{}
}

func (d *pausedReadData) View(ctx context.Context, tenant model.TenantID, fn func(store.Scope) error) error {
	err := d.ModuleData.View(ctx, tenant, fn)
	if err == nil && ctx.Value(pausedCallKey{}) == "old" && d.reads.Add(1) == d.nth {
		close(d.reached)
		select {
		case <-d.resume:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return err
}

// pausedCtx marks the one caller pausedReadData pauses.
func pausedCtx(ctx context.Context) context.Context {
	return context.WithValue(ctx, pausedCallKey{}, "old")
}

// forEachAdmissionEngine runs one ownership oracle against every storage this module
// supports. PostgreSQL is not a formality here and SQLite is not a substitute for it: on
// PostgreSQL two writers of one key are separate transactions under READ COMMITTED, and
// the version predicate is the only thing that can decide between them, while SQLite
// admits a single writer and serializes them on its own. A fence proven on one engine says
// nothing about the other.
//
// The PostgreSQL leg SKIPS by name when no database is authorized, rather than being
// omitted: a leg that quietly does not exist reports a pass for work nobody ran.
func forEachAdmissionEngine(t *testing.T, body func(*testing.T, store.Config)) {
	t.Helper()
	t.Run("sqlite", func(t *testing.T) {
		body(t, store.Config{Engine: store.EngineSQLite, DSN: ":memory:", Debug: true})
	})
	t.Run("postgres", func(t *testing.T) {
		if !enginetest.PostgresAvailable(t) {
			t.Skipf("%s unset: this PostgreSQL leg is NOT RUN. This is not a pass.", enginetest.EnvSuperuserDSN)
		}
		body(t, store.Config{Engine: store.EnginePostgres, DSN: enginetest.IsolatedPostgres(t).App, MaxConns: 8})
	})
}

type pausedReserveOutcome struct {
	res Reservation
	err error
}

func awaitPausedRead(t *testing.T, d *pausedReadData) {
	t.Helper()
	select {
	case <-d.reached:
	case <-time.After(5 * time.Second):
		t.Fatal("the paused caller never completed the read the interleaving needs")
	}
}

// TestTwoStaleReadersOfOnePendingClaimLeaveOneHold is the ownership fence stated as
// money. Two callers read the SAME abandoned pending row and each finds it stale, so
// each is entitled to take it over — and exactly one may, because a takeover is what
// grants the right to evaluate the key. Without a fence both evaluate and each takes
// its own hold of the same money under one idempotency key, which is the one thing
// the key exists to prevent.
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
	delayed := <-first

	if err != nil || delayed.err != nil || !second.Allowed || !delayed.res.Allowed {
		t.Fatalf("both stale readers must be answered: delayed=%+v second=%+v err=%v", delayed, second, err)
	}
	report, err := m.InspectReservations(ctx, tenant)
	if err != nil {
		t.Fatal(err)
	}
	if delayed.res.Handle != second.Handle || report.Active != 1 {
		t.Fatalf("one stale key produced conflicting leaders: first=%s delayed=%s active=%d; want one shared hold",
			second.Handle, delayed.res.Handle, report.Active)
	}
}

// TestTheOriginalClaimantDoesNotReplaceItsSuccessor is the other interleaving of the
// same authority, and it is the one wall time cannot fence. The caller that STAGED
// the claim is the legitimate owner right up to the moment its claim is believed
// dead; after a successor has taken the key over, that same caller resuming is a
// stranger. It must not publish its verdict over the successor's admission, and the
// hold it took on its way there must be returned rather than left behind.
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
	delayed := <-original

	if err != nil || !successor.Allowed || successor.Handle == "" {
		t.Fatalf("the successor must be admitted: %+v %v", successor, err)
	}
	row, found, err := m.lookupIdempotency(ctx, tenant, req.IdempotencyKey)
	if err != nil || !found {
		t.Fatalf("lookup: found=%v err=%v", found, err)
	}
	report, err := m.InspectReservations(ctx, tenant)
	if err != nil {
		t.Fatal(err)
	}
	if row.handle != successor.Handle || report.Active != 1 {
		t.Fatalf("the original claimant overwrote its successor: successor=%s stored=%s delayed=%+v active=%d",
			successor.Handle, row.handle, delayed, report.Active)
	}
}

// TestADelayedCommitDoesNotReplaceTheCurrentAdmission separates the two things a
// settlement does. Returning or charging the money of the handle it was given is
// always right, and stays right however late it arrives. Writing the idempotency row
// is a claim of AUTHORITY over the key, and the key may already belong to a newer
// call — in which case the late settlement must leave that call's admission alone,
// or the next retry is handed a handle to money nobody is holding.
func TestADelayedCommitDoesNotReplaceTheCurrentAdmission(t *testing.T) {
	forEachAdmissionEngine(t, runADelayedCommitDoesNotReplaceTheCurrentAdmission)
}

func runADelayedCommitDoesNotReplaceTheCurrentAdmission(t *testing.T, cfg store.Config) {
	m, st, tenant, _ := openFinCfg(t, cfg)
	clk := &fakeClock{t: baseTime}
	m.clock = clk
	createBudget(t, st, tenant, "cap", budgetSpec{Dimension: "global", Period: "monthly", LimitMicroUSD: 10 * oneUSD, Action: "block"})
	req := AdmissionRequest{Scope: AdmissionScopeModelGateway, IdempotencyKey: "model_gateway/late-settlement", EstimateMicroUSD: oneUSD}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	first, err := m.Reserve(ctx, tenant, req)
	if err != nil || !first.Allowed || first.Handle == "" {
		t.Fatalf("first: %+v %v", first, err)
	}

	d := &pausedReadData{ModuleData: m.data, nth: 1, reached: make(chan struct{}), resume: make(chan struct{})}
	m.data = d
	settled := make(chan error, 1)
	go func() { settled <- m.Commit(pausedCtx(ctx), tenant, first.Handle, oneUSD) }()
	awaitPausedRead(t, d)
	clk.advance(admissionReplayWindow + time.Second)
	second, err := m.Reserve(ctx, tenant, req)
	close(d.resume)
	lateErr := <-settled

	if err != nil || lateErr != nil || !second.Allowed || second.Handle == first.Handle {
		t.Fatalf("a late settlement of the old handle must succeed and a new call must be admitted: new=%+v err=%v lateErr=%v",
			second, err, lateErr)
	}
	row, found, err := m.lookupIdempotency(ctx, tenant, req.IdempotencyKey)
	if err != nil || !found {
		t.Fatalf("lookup: found=%v err=%v", found, err)
	}
	replay, err := m.Reserve(ctx, tenant, req)
	if err != nil {
		t.Fatal(err)
	}
	if row.handle != second.Handle || replay.Handle != second.Handle {
		t.Fatalf("the late settlement replaced the current admission: current=%s stored=%s next=%+v",
			second.Handle, row.handle, replay)
	}
}

// TestARepeatedSettlementDoesNotExtendTheReplayWindow pins what the state-entry
// stamp means. The window is measured from the moment the row REACHED the state it
// is in, so a settlement that repeats a settlement already applied changes nothing
// and must move nothing: a caller that retries its commit every four minutes would
// otherwise carry its first verdict for as long as it keeps retrying, over a cap
// that has since been blown.
func TestARepeatedSettlementDoesNotExtendTheReplayWindow(t *testing.T) {
	forEachAdmissionEngine(t, runARepeatedSettlementDoesNotExtendTheReplayWindow)
}

func runARepeatedSettlementDoesNotExtendTheReplayWindow(t *testing.T, cfg store.Config) {
	m, st, tenant, _ := openFinCfg(t, cfg)
	clk := &fakeClock{t: baseTime}
	m.clock = clk
	createBudget(t, st, tenant, "cap", budgetSpec{Dimension: "global", Period: "monthly", LimitMicroUSD: 5 * oneUSD, Action: "block"})
	req := AdmissionRequest{Scope: AdmissionScopeModelGateway, IdempotencyKey: "model_gateway/retried-commit", EstimateMicroUSD: oneUSD}
	ctx := context.Background()

	first, err := m.Reserve(ctx, tenant, req)
	if err != nil || !first.Allowed || first.Handle == "" {
		t.Fatalf("first: %+v %v", first, err)
	}
	if err := m.Commit(ctx, tenant, first.Handle, oneUSD); err != nil {
		t.Fatal(err)
	}
	m.ingest(t, tenant, mkCost("anthropic", "model", "s1", 1, 1, 50*oneUSD, baseTime))

	clk.advance(4 * time.Minute)
	if err := m.Commit(ctx, tenant, first.Handle, oneUSD); err != nil {
		t.Fatal(err)
	}
	clk.advance(2 * time.Minute)

	again, err := m.Reserve(ctx, tenant, req)
	if err != nil {
		t.Fatal(err)
	}
	if again.Allowed || again.Replayed {
		t.Fatalf("a repeated settlement renewed a six-minute-old committed verdict over an exhausted cap: %+v", again)
	}
}

// TestAnUndatedReservedRowDoesNotOrphanItsLiveHold is the upgrade. A row written
// before the state-entry column existed carries no stamp, and an undated row cannot
// be shown to be a young retry, so it is re-evaluated — that part is right. What was
// wrong is what happened to the hold the undated row still records: it stayed active
// with nothing pointing at it while the retry took a second one, so one call held
// twice. Re-evaluating a row means the hold it recorded stops being withheld.
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
	row, found, err := m.lookupIdempotency(ctx, tenant, req.IdempotencyKey)
	if err != nil || !found {
		t.Fatalf("lookup: found=%v err=%v", found, err)
	}
	// Reproduce the NULL the nullable column gives a row written before it existed.
	if err := m.data.Mutate(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(admissionIdempotencyKind)
		if err != nil {
			return err
		}
		rec, err := repo.Get(ctx, row.id)
		if err != nil {
			return err
		}
		rec[colAdmStateAt] = nil
		_, err = repo.Update(ctx, rec)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	row, found, err = m.lookupIdempotency(ctx, tenant, req.IdempotencyKey)
	if err != nil || !found || !row.stateAt.IsZero() {
		t.Fatalf("the undated row was not established: found=%v row=%+v err=%v", found, row, err)
	}

	again, err := m.Reserve(ctx, tenant, req)
	if err != nil || !again.Allowed {
		t.Fatalf("retry: %+v %v", again, err)
	}
	report, err := m.InspectReservations(ctx, tenant)
	if err != nil {
		t.Fatal(err)
	}
	if report.Active != 1 {
		t.Fatalf("the undated retry orphaned a live pre-upgrade hold: first=%s new=%s active=%d",
			first.Handle, again.Handle, report.Active)
	}
}

// publishAfterObservedPendingRead starts the caller that HOLDS a pending claim and has
// it reach its verdict only once the waiter has COMPLETED the read that returned the
// pending row. That completed read is the evidence: a publication that merely happened
// to win the race would leave the waiter never having met a pending row at all, and a
// test that only sometimes exercises waiting cannot tell waiting apart from taking over.
//
// It returns the handle the holder took, or "" if the holder could not reach a verdict —
// which a caller must check, because a test whose fixture failed proves nothing.
func publishAfterObservedPendingRead(
	t *testing.T, m *Module, tenant model.TenantID, req AdmissionRequest, d *pausedReadData,
) <-chan string {
	t.Helper()
	ctx := context.Background()
	finished := make(chan string, 1)
	go func() {
		defer close(d.resume)
		<-d.reached
		held, err := m.ReserveBudget(ctx, tenant, req.Dims, req.EstimateMicroUSD)
		if err != nil || !held.Allowed {
			finished <- ""
			return
		}
		row, found, err := m.lookupIdempotency(ctx, tenant, req.IdempotencyKey)
		if err != nil || !found {
			finished <- ""
			return
		}
		if _, err := m.writeIdempotency(ctx, tenant, admissionWrite{
			row: row, to: admStateReserved, handle: held.Handle,
		}); err != nil {
			finished <- ""
			return
		}
		finished <- held.Handle
	}()
	return finished
}

// TestAFreshlyTakenOverClaimCannotBeStolenAtOnce is the positive half of the takeover,
// and without it a fence is indistinguishable from a module that recovers a key by
// letting every caller in turn seize it.
//
// A takeover goes from pending to PENDING: the state string does not change, the owner
// does. So the new claim's lease has to be measured from ITS OWN acquisition — if it
// inherited the dead owner's stamp it would be born already past the takeover bound,
// and the very next caller would take it from a claimant that is alive and
// mid-evaluation. The module's clock does not move here: the claim is fresh by the same
// arithmetic that made its predecessor stale, so this is a fact and not a race.
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

	// A claim nobody is holding, aged past the bound, and then taken over — exactly the
	// state a recovering caller leaves behind while it evaluates the budgets.
	stagePendingClaim(t, m, tenant, req)
	clk.advance(admissionClaimTakeover + time.Second)
	abandoned, found, err := m.lookupIdempotency(ctx, tenant, req.IdempotencyKey)
	if err != nil || !found || !m.staleClaim(abandoned) {
		t.Fatalf("the fixture did not produce a stale claim to recover: found=%v row=%+v err=%v", found, abandoned, err)
	}
	if _, err := m.writeIdempotency(ctx, tenant, admissionWrite{
		row: abandoned, to: admStatePending, acquire: true,
	}); err != nil {
		t.Fatalf("take the abandoned claim over: %v", err)
	}
	recovered, found, err := m.lookupIdempotency(ctx, tenant, req.IdempotencyKey)
	if err != nil || !found {
		t.Fatalf("read the recovered claim: found=%v err=%v", found, err)
	}
	if m.staleClaim(recovered) {
		t.Fatalf("the claim was born stale: it inherited the stamp of the owner it replaced (%+v)", recovered)
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
		t.Fatalf("a caller stole a claim taken over a moment ago: got %+v, want the recovering caller's hold %q",
			res, claimed)
	}
	report, err := m.InspectReservations(ctx, tenant)
	if err != nil {
		t.Fatal(err)
	}
	if report.Active != 1 {
		t.Fatalf("one recovered key left %d active hold(s), want exactly 1: %+v", report.Active, report)
	}
}

// TestManyCallersOnOneStaleKeyLeaveOneHoldAndOneAnswer is the property the whole fence
// exists for, stated as the invariant an operator can check: whatever the interleaving,
// one key is one hold and one answer.
//
// Every caller here reads the same abandoned claim and every one of them is entitled to
// recover it, so the takeover is contended N ways. Exactly one may acquire it; the rest
// find a claim that is now fresh and are handed ITS verdict. A count of holds is the
// oracle because it is the thing money is made of: N answers that agree while the ledger
// holds N times would be N callers politely reporting the same double charge.
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
		i := i
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
			t.Fatalf("callers %d and 0 recovered one key into different holds (%s and %s): "+
				"each evaluated the key and each took its own money",
				i, answers[i].Handle, answers[0].Handle)
		}
	}
	report, err := m.InspectReservations(ctx, tenant)
	if err != nil {
		t.Fatal(err)
	}
	if report.Active != 1 {
		t.Fatalf("%d callers of one key left %d active hold(s), want exactly 1: %+v", N, report.Active, report)
	}
	row, found, err := m.lookupIdempotency(ctx, tenant, req.IdempotencyKey)
	if err != nil || !found {
		t.Fatalf("lookup: found=%v err=%v", found, err)
	}
	if row.state != admStateReserved || row.handle != answers[0].Handle {
		t.Fatalf("the authoritative row does not name the hold every caller was given: state=%q handle=%s, want reserved on %s",
			row.state, row.handle, answers[0].Handle)
	}
}
