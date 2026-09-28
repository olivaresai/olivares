// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package finops

import (
	"context"
	"strconv"
	"testing"

	"github.com/olivaresai/olivares/core/store"
)

// TestReserveOfZeroHoldsNothing pins what an admission with no amount leaves behind:
// nothing. A hold of zero never withheld a micro-USD from anyone, so a row for it would
// protect no caller and serve no settlement; it would only accumulate until the TTL
// retired it and the reconciliation reported it as drift the engine made itself. The
// answer is unchanged — every enforcing budget is still evaluated — and there is no
// handle, because a handle is a promise to settle and there is nothing to settle.
func TestReserveOfZeroHoldsNothing(t *testing.T) {
	forEachAdmissionEngine(t, runReserveOfZeroHoldsNothing)
}

func runReserveOfZeroHoldsNothing(t *testing.T, cfg store.Config) {
	m, st, tenant, _ := openFinCfg(t, cfg)
	m.clock = &fakeClock{t: baseTime}
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
	if got := ledgerCounts(t, st, tenant, baseTime); got != (ledgerCount{}) {
		t.Fatalf("after %d zero-amount admissions the ledger holds %+v; it must hold nothing", launches, got)
	}
}

// TestReserveOfZeroStillRefusesAnExhaustedCap is the half that must NOT change. A
// YES/NO caller with no amount is still asking a real question, and a budget already
// over its cap still answers no — reserving nothing is not checking nothing.
func TestReserveOfZeroStillRefusesAnExhaustedCap(t *testing.T) {
	forEachAdmissionEngine(t, runReserveOfZeroStillRefusesAnExhaustedCap)
}

func runReserveOfZeroStillRefusesAnExhaustedCap(t *testing.T, cfg store.Config) {
	m, st, tenant, _ := openFinCfg(t, cfg)
	m.clock = &fakeClock{t: baseTime}
	ctx := context.Background()
	createBudget(t, st, tenant, "global-block", budgetSpec{
		Dimension: "global", Period: "monthly", LimitMicroUSD: oneUSD, Action: "block",
	})
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

// TestReserveOfZeroIsReEvaluatedUnderAStableKey is the half of idempotency a hold of
// zero cannot buy. A replay hands a retry the hold its call took; an admission that took
// no hold has nothing to hand back, so replaying it would only freeze its answer. A
// session resume re-runs the launch gate under the same run reference precisely so a
// budget change since the launch is honored, and here the cap is blown between the two.
func TestReserveOfZeroIsReEvaluatedUnderAStableKey(t *testing.T) {
	forEachAdmissionEngine(t, runReserveOfZeroIsReEvaluatedUnderAStableKey)
}

func runReserveOfZeroIsReEvaluatedUnderAStableKey(t *testing.T, cfg store.Config) {
	m, st, tenant, _ := openFinCfg(t, cfg)
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

	// A key never used before refuses too, so the refusal above is the cap and not the key.
	fresh, err := m.Reserve(ctx, tenant, AdmissionRequest{
		Scope: AdmissionScopeSessionLaunch, IdempotencyKey: "session_launch/run-43",
	})
	if err != nil || fresh.Allowed {
		t.Fatalf("a fresh key over the same blown cap must refuse: %+v err=%v", fresh, err)
	}
}

// TestReserveWithAnAmountStillReplaysItsHold is the property that must NOT move: a retry
// of a key whose admission does hold gets the original handle back and the ledger still
// holds exactly once.
func TestReserveWithAnAmountStillReplaysItsHold(t *testing.T) {
	forEachAdmissionEngine(t, runReserveWithAnAmountStillReplaysItsHold)
}

func runReserveWithAnAmountStillReplaysItsHold(t *testing.T, cfg store.Config) {
	m, st, tenant, _ := openFinCfg(t, cfg)
	m.clock = &fakeClock{t: baseTime}
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
	if got := ledgerCounts(t, st, tenant, baseTime); got.Active != 1 {
		t.Fatalf("after one admission and one retry the ledger holds %+v, want exactly 1 active row", got)
	}
}

// TestResumeUnderHeadroomIsAllowedAgain is the positive side of the re-evaluation, and it
// is not implied by the refusal above: a module that refused every handle-less resume
// would pass that one. The second launch of a run under a cap that still has room is
// allowed, on the ledger's answer rather than on the first launch's.
func TestResumeUnderHeadroomIsAllowedAgain(t *testing.T) {
	forEachAdmissionEngine(t, runResumeUnderHeadroomIsAllowedAgain)
}

func runResumeUnderHeadroomIsAllowedAgain(t *testing.T, cfg store.Config) {
	m, st, tenant, _ := openFinCfg(t, cfg)
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
