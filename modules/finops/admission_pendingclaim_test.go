// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package finops

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// stagePendingClaim writes the row a caller stages before it evaluates the budgets,
// and stops there — which is exactly what a crash between the two leaves behind.
func stagePendingClaim(t *testing.T, m *Module, tenant model.TenantID, req AdmissionRequest) {
	t.Helper()
	if _, err := m.writeIdempotency(context.Background(), tenant, admissionWrite{
		row: idempotencyRow{
			key: req.IdempotencyKey, payloadHash: admissionPayloadHash(req),
			scope: req.Scope, estimate: req.EstimateMicroUSD,
		},
		to: admStatePending,
	}); err != nil {
		t.Fatalf("stage the pending claim: %v", err)
	}
}

// TestAStalePendingClaimIsTakenOverAndAnsweredFromTheLedger closes the state the
// module could enter and never leave. A claim is staged BEFORE the budgets are
// evaluated, so a caller that dies in that window leaves the row pending and
// nothing clears it — and since a stable-key caller stages one on every call, a
// single crash made that run's key refuse for good. The old answer was
// "budget store unreachable (deny-closed)" after a 330 ms busy wait, forever.
//
// Both directions are asserted, because a takeover that always refused would pass
// the first half: the recovered key answers from the LEDGER, which says yes under a
// cap with headroom and no once the cap is blown.
func TestAStalePendingClaimIsTakenOverAndAnsweredFromTheLedger(t *testing.T) {
	forEachAdmissionEngine(t, runAStalePendingClaimIsTakenOverAndAnsweredFromTheLedger)
}

func runAStalePendingClaimIsTakenOverAndAnsweredFromTheLedger(t *testing.T, cfg store.Config) {
	m, st, tenant, _ := openFinCfg(t, cfg)
	clk := &fakeClock{t: baseTime}
	m.clock = clk
	ctx := context.Background()
	createBudget(t, st, tenant, "global-block", budgetSpec{
		Dimension: "global", Period: "monthly", LimitMicroUSD: 5 * oneUSD, Action: "block",
	})

	withRoom := AdmissionRequest{Scope: AdmissionScopeSessionLaunch, IdempotencyKey: "session_launch/run-crashed"}
	stagePendingClaim(t, m, tenant, withRoom)
	clk.advance(admissionClaimTakeover + time.Second)

	res, err := m.Reserve(ctx, tenant, withRoom)
	if err != nil {
		t.Fatalf("reserve over a stale claim: %v", err)
	}
	if res.Reason == ReasonStoreUnreachable {
		t.Fatalf("a claim nobody is holding refused the key as an unreadable ledger: %+v", res)
	}
	if !res.Allowed {
		t.Fatalf("the recovered key was refused under a cap with headroom: %+v", res)
	}

	// And the ledger is what answers it: blow the cap, strand another claim, and the
	// takeover must say no.
	m.ingest(t, tenant, mkCost("anthropic", "model", "s1", 1, 1, 10*oneUSD, baseTime))
	overCap := AdmissionRequest{Scope: AdmissionScopeSessionLaunch, IdempotencyKey: "session_launch/run-crashed-2"}
	stagePendingClaim(t, m, tenant, overCap)
	clk.advance(admissionClaimTakeover + time.Second)

	denied, err := m.Reserve(ctx, tenant, overCap)
	if err != nil {
		t.Fatalf("reserve over a stale claim on a blown cap: %v", err)
	}
	if denied.Allowed {
		t.Fatalf("the takeover admitted a run over a cap 10 USD past its 5 USD limit: %+v", denied)
	}
	if denied.Reason == ReasonStoreUnreachable {
		t.Fatalf("the takeover refused with the deny-closed reason instead of the budget's: %+v", denied)
	}
	if denied.Action != "block" {
		t.Fatalf("Action = %q, want block", denied.Action)
	}
}

// TestAFreshPendingClaimIsWaitedForNotTakenOver is the property the takeover must
// not break, and the reason the bound exists at all. A claim staged a moment ago is
// another caller MID-EVALUATION: taking it over would put two callers on one key,
// and each would take its own hold of the same money. The waiter here gets the
// claim-holder's verdict and its handle, and the ledger holds exactly once.
//
// The module's clock is frozen, so "fresh" is a fact and not a race: no amount of
// real time spent waiting can age the staged claim past the takeover bound.
//
// AND THE INTERLEAVING IS OBSERVED, not hoped for. The holder publishes its verdict
// only once the waiter has COMPLETED THE READ that returned the pending row, so the
// test cannot pass without the waiter having met the state it is supposed to wait
// for. It used to sleep 5 ms first: if the main goroutine reached Reserve after the
// publication it never saw a pending row at all, and a test that only sometimes
// exercises waiting cannot tell waiting apart from taking over.
func TestAFreshPendingClaimIsWaitedForNotTakenOver(t *testing.T) {
	forEachAdmissionEngine(t, runAFreshPendingClaimIsWaitedForNotTakenOver)
}

func runAFreshPendingClaimIsWaitedForNotTakenOver(t *testing.T, cfg store.Config) {
	m, st, tenant, _ := openFinCfg(t, cfg)
	m.clock = &fakeClock{t: baseTime}
	ctx := context.Background()
	createBudget(t, st, tenant, "global-block", budgetSpec{
		Dimension: "global", Period: "monthly", LimitMicroUSD: 10 * oneUSD, Action: "block",
	})
	req := AdmissionRequest{
		Scope: AdmissionScopeModelGateway, EstimateMicroUSD: oneUSD,
		IdempotencyKey: "model_gateway/held-by-a-live-caller",
	}
	stagePendingClaim(t, m, tenant, req)

	// The waiter pauses after the read that returns the pending row; that pause is the
	// evidence, and it is what the holder waits for before publishing.
	d := &pausedReadData{ModuleData: m.data, nth: 1, reached: make(chan struct{}), resume: make(chan struct{})}
	m.data = d

	finished := publishAfterObservedPendingRead(t, m, tenant, req, d)

	res, err := m.Reserve(pausedCtx(ctx), tenant, req)
	claimed := <-finished
	if err != nil {
		t.Fatalf("the waiter errored: %v", err)
	}
	if claimed == "" {
		t.Fatal("the claim holder could not reach its verdict; the test proves nothing")
	}
	if !res.Allowed || !res.Replayed {
		t.Fatalf("the waiter did not take the claim holder's verdict: %+v", res)
	}
	if res.Handle != claimed {
		t.Fatalf("the waiter came back with handle %q, not the one the claim holder took (%q): "+
			"two callers evaluated one key and each took its own hold", res.Handle, claimed)
	}

	report, err := m.InspectReservations(ctx, tenant)
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	if report.Active != 1 {
		t.Fatalf("one key evaluated once left %d active hold(s), want exactly 1: %+v", report.Active, report)
	}
}

// TestThePauseBetweenClaimPollsIsRealAndCancellable pins the third half of the
// finding: the wait used to be a SPIN. Sixty-four attempts of thirty-two reads of a
// row that was never going to change, measured at 330 ms of CPU, for a verdict that
// another goroutine or another process has to produce. Two properties, because each
// has its own way of being lost: the pause has to actually pause, and it has to end
// the moment the caller stops waiting rather than finish a wait nobody is listening
// to.
func TestThePauseBetweenClaimPollsIsRealAndCancellable(t *testing.T) {
	m := &Module{}

	start := time.Now()
	if err := m.pauseBetweenClaimPolls(context.Background()); err != nil {
		t.Fatalf("an uncancelled pause returned %v", err)
	}
	if elapsed := time.Since(start); elapsed < admissionClaimPoll {
		t.Fatalf("the pause took %v, less than the %v it is for: the wait is a spin", elapsed, admissionClaimPoll)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := m.pauseBetweenClaimPolls(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("a pause under a cancelled context returned %v, want it to carry context.Canceled", err)
	}
}
