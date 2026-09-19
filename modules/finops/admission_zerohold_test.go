// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package finops

import (
	"context"
	"strconv"
	"testing"
)

// TestReserveOfZeroHoldsNothing pins what a reservation with no amount leaves
// behind: nothing. A zero hold never withheld a micro-USD from anyone — the
// ceiling sums the AMOUNTS, and zero adds zero — so the row it used to insert
// protected no caller and served no settlement. What it did do is accumulate:
// six of the seven in-process callers ask a YES/NO question they can never
// settle, and each of their launches left an active row that only the TTL would
// ever retire, which the reconciliation read then reports as drift the engine
// produced itself.
//
// The answer is unchanged — every enforcing budget is still evaluated under the
// writer lock and a cap that is already over still refuses. Only the row is gone,
// and with it the handle: a handle is a promise to settle, and there is nothing
// to settle.
func TestReserveOfZeroHoldsNothing(t *testing.T) {
	m, st, tenant, _ := newFin(t)
	ctx := context.Background()
	createBudget(t, st, tenant, "global-block", budgetSpec{
		Dimension: "global", Period: "monthly", LimitMicroUSD: 5 * oneUSD, Action: "block",
	})

	const launches = 3
	for i := 0; i < launches; i++ {
		res, err := m.Reserve(ctx, tenant, AdmissionRequest{
			Scope: AdmissionScopeSessionLaunch, IdempotencyKey: "zero-" + strconv.Itoa(i),
		})
		if err != nil || !res.Allowed {
			t.Fatalf("launch %d: %+v err=%v", i, res, err)
		}
		if res.Handle != "" {
			t.Fatalf("launch %d was handed handle %q for a hold of zero: nothing was reserved, so there is nothing to settle", i, res.Handle)
		}
	}

	report, err := m.InspectReservations(ctx, tenant)
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	if report.Active != 0 {
		t.Fatalf("after %d zero-amount admissions the ledger holds %d active reservation(s); it must hold none", launches, report.Active)
	}
	if report.Drift {
		t.Fatalf("zero-amount admissions produced drift the engine made itself: %+v", report)
	}
}

// TestReserveOfZeroStillRefusesAnExhaustedCap is the half that must NOT change.
// A YES/NO caller with no amount is still asking a real question, and a budget
// already over its cap still answers no — reserving nothing is not checking
// nothing.
func TestReserveOfZeroStillRefusesAnExhaustedCap(t *testing.T) {
	m, st, tenant, _ := newFin(t)
	m.clock = &fakeClock{t: baseTime}
	ctx := context.Background()
	createBudget(t, st, tenant, "global-block", budgetSpec{
		Dimension: "global", Period: "monthly", LimitMicroUSD: oneUSD, Action: "block",
	})
	// Spend the cap through a reservation that DOES carry an amount, then ask the
	// zero-amount question against it.
	if first, err := m.Reserve(ctx, tenant, AdmissionRequest{
		Scope: AdmissionScopeModelGateway, EstimateMicroUSD: 2 * oneUSD, IdempotencyKey: "over-1",
	}); err != nil || first.Allowed {
		t.Fatalf("a 2 USD hold against a 1 USD cap must be refused: %+v err=%v", first, err)
	}
	m.ingest(t, tenant, mkCost("anthropic", "model", "s1", 1, 1, 2*oneUSD, baseTime))

	res, err := m.Reserve(ctx, tenant, AdmissionRequest{
		Scope: AdmissionScopeSessionLaunch, IdempotencyKey: "zero-over-cap",
	})
	if err != nil {
		t.Fatalf("reserve: %v", err)
	}
	if res.Allowed {
		t.Fatalf("a cap already over its limit must refuse a zero-amount admission too: %+v", res)
	}
	if res.Action != "block" {
		t.Fatalf("Action = %q, want block", res.Action)
	}
}

// TestReserveWithAnAmountStillHolds is the control positive of the pair: the
// caller that DOES carry an estimate — the inference proxy, which learns the
// real cost and settles it — still gets a row and a handle, and settling it
// returns the ledger to zero.
func TestReserveWithAnAmountStillHolds(t *testing.T) {
	m, st, tenant, _ := newFin(t)
	ctx := context.Background()
	createBudget(t, st, tenant, "global-block", budgetSpec{
		Dimension: "global", Period: "monthly", LimitMicroUSD: 5 * oneUSD, Action: "block",
	})
	res, err := m.Reserve(ctx, tenant, AdmissionRequest{
		Scope: AdmissionScopeModelGateway, EstimateMicroUSD: oneUSD, IdempotencyKey: "held-1",
	})
	if err != nil || !res.Allowed || res.Handle == "" {
		t.Fatalf("an amount must still be held: %+v err=%v", res, err)
	}
	held, err := m.InspectReservations(ctx, tenant)
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	if held.Active != 1 {
		t.Fatalf("Active = %d after a 1 USD hold, want 1", held.Active)
	}
	if err := m.Commit(ctx, tenant, res.Handle, oneUSD); err != nil {
		t.Fatalf("commit: %v", err)
	}
	settled, err := m.InspectReservations(ctx, tenant)
	if err != nil {
		t.Fatalf("inspect after commit: %v", err)
	}
	if settled.Active != 0 || settled.Committed != 1 {
		t.Fatalf("after settlement: %+v", settled)
	}
}

// TestReserveOfZeroIsReEvaluatedUnderAStableKey is the half of the idempotency
// contract a hold of zero cannot buy. Replay exists so a RETRY hands back the
// hold the first call took instead of taking a second one; an admission that
// took no hold has nothing to hand back, so replaying it only freezes its
// answer.
//
// The callers this bites are the ones whose key is stable by design:
// `session_launch/<run>` and `mcp_task/<task>`. A session RESUME re-runs the
// launch gate with the same run reference precisely so a budget change since the
// last launch is honoured — and a frozen "allowed" is the one answer that makes
// that re-run read nothing. Here the cap is blown between the launch and the
// resume by real spend, and the resume must see it.
func TestReserveOfZeroIsReEvaluatedUnderAStableKey(t *testing.T) {
	m, st, tenant, _ := newFin(t)
	m.clock = &fakeClock{t: baseTime}
	ctx := context.Background()
	createBudget(t, st, tenant, "global-block", budgetSpec{
		Dimension: "global", Period: "monthly", LimitMicroUSD: 5 * oneUSD, Action: "block",
	})
	const key = "session_launch/run-42"
	req := AdmissionRequest{Scope: AdmissionScopeSessionLaunch, IdempotencyKey: key}

	launch, err := m.Reserve(ctx, tenant, req)
	if err != nil || !launch.Allowed {
		t.Fatalf("the launch was refused under a cap with headroom: %+v err=%v", launch, err)
	}
	if launch.Handle != "" {
		t.Fatalf("a launch holds nothing, so it is handed no handle: %q", launch.Handle)
	}

	// The tenant spends past the cap between the launch and the resume.
	m.ingest(t, tenant, mkCost("anthropic", "model", "s1", 1, 1, 10*oneUSD, baseTime))

	resume, err := m.Reserve(ctx, tenant, req)
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if resume.Allowed {
		t.Fatalf("the resume was admitted under the launch's answer over a cap that is now 10 USD past its 5 USD limit: %+v", resume)
	}
	if resume.Replayed {
		t.Fatalf("an admission that held nothing was replayed: %+v", resume)
	}
	if resume.Action != "block" {
		t.Fatalf("Action = %q, want block", resume.Action)
	}

	// A key that was never used before is the control the review's probe carries:
	// it refuses too, so the refusal above is the cap and not the key.
	fresh, err := m.Reserve(ctx, tenant, AdmissionRequest{
		Scope: AdmissionScopeSessionLaunch, IdempotencyKey: "session_launch/run-43",
	})
	if err != nil || fresh.Allowed {
		t.Fatalf("a fresh key over the same blown cap must refuse: %+v err=%v", fresh, err)
	}
}

// TestReserveWithAnAmountStillReplaysItsHold is the control positive of the pair
// and the property that must NOT move: a retry of a key whose admission DOES
// hold gets the original handle back and the ledger still holds exactly once.
// Without it, "an admission that holds nothing has nothing to replay" could be
// satisfied by a module that replays nothing at all and double-holds every retry.
func TestReserveWithAnAmountStillReplaysItsHold(t *testing.T) {
	m, st, tenant, _ := newFin(t)
	ctx := context.Background()
	createBudget(t, st, tenant, "global-block", budgetSpec{
		Dimension: "global", Period: "monthly", LimitMicroUSD: 10 * oneUSD, Action: "block",
	})
	req := AdmissionRequest{
		Scope: AdmissionScopeModelGateway, EstimateMicroUSD: oneUSD, IdempotencyKey: "model_gateway/held-1",
	}
	first, err := m.Reserve(ctx, tenant, req)
	if err != nil || !first.Allowed || first.Handle == "" {
		t.Fatalf("first: %+v err=%v", first, err)
	}
	second, err := m.Reserve(ctx, tenant, req)
	if err != nil || !second.Allowed || !second.Replayed {
		t.Fatalf("a retry of a held admission must replay: %+v err=%v", second, err)
	}
	if second.Handle != first.Handle {
		t.Fatalf("the replay handed back handle %q, not the one it took (%q)", second.Handle, first.Handle)
	}
	report, err := m.InspectReservations(ctx, tenant)
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	if report.Active != 1 {
		t.Fatalf("after one admission and one retry the ledger holds %d active reservation(s), want exactly 1", report.Active)
	}
}

// TestResumeUnderHeadroomIsAllowedAgain is the POSITIVE oracle of the
// re-evaluation, and it is not implied by the test above it. That one asserts
// that a resume over a blown cap is refused with Action "block" — which a module
// that refused EVERY handle-less resume would satisfy just as well, because the
// deny-closed refusal carries the same action. Re-evaluating is not refusing: the
// second launch of a run under a cap that still has room is allowed, and it is
// allowed on the ledger's answer rather than on the first launch's.
func TestResumeUnderHeadroomIsAllowedAgain(t *testing.T) {
	m, st, tenant, _ := newFin(t)
	m.clock = &fakeClock{t: baseTime}
	ctx := context.Background()
	createBudget(t, st, tenant, "global-block", budgetSpec{
		Dimension: "global", Period: "monthly", LimitMicroUSD: 5 * oneUSD, Action: "block",
	})
	req := AdmissionRequest{Scope: AdmissionScopeSessionLaunch, IdempotencyKey: "session_launch/run-resumed"}

	launch, err := m.Reserve(ctx, tenant, req)
	if err != nil || !launch.Allowed {
		t.Fatalf("the launch was refused under a cap with headroom: %+v err=%v", launch, err)
	}

	// The run spends 1 USD of its 5 and is launched again.
	m.ingest(t, tenant, mkCost("anthropic", "model", "s1", 1, 1, oneUSD, baseTime))

	resume, err := m.Reserve(ctx, tenant, req)
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if !resume.Allowed {
		t.Fatalf("a resume under a cap with 4 USD of its 5 USD left was refused: %+v", resume)
	}
	if resume.Reason == ReasonStoreUnreachable {
		t.Fatalf("the resume was refused as an unreadable ledger while the ledger answered: %+v", resume)
	}
	if resume.Replayed {
		t.Fatalf("an admission that held nothing was replayed: %+v", resume)
	}
	if resume.Handle != "" {
		t.Fatalf("a launch holds nothing, so it is handed no handle: %q", resume.Handle)
	}
}
