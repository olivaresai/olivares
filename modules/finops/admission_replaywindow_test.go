// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package finops

import (
	"context"
	"testing"
	"time"
)

// The three tests below are one property read three ways: a replay answers for the
// SAME call within a bounded retry window, and outside it the request is a new
// question the ledger answers.
//
// The key they use is the shape the model gateway used to mint — a session
// reference and a digest of the request — because that key is stable by
// construction: send the same bytes twice and you get the same key. Before the
// window, a committed verdict under such a key returned `Allowed:true` with the
// ledger unread, forever.

// TestReplayWithinTheWindowHoldsAndChargesOnce is the half that must not move. A
// retry of one call gets the hold the first call took and the charge it already
// made — one hold, one charge, Replayed.
func TestReplayWithinTheWindowHoldsAndChargesOnce(t *testing.T) {
	m, st, tenant, _ := newFin(t)
	clk := &fakeClock{t: baseTime}
	m.clock = clk
	ctx := context.Background()
	createBudget(t, st, tenant, "global-block", budgetSpec{
		Dimension: "global", Period: "monthly", LimitMicroUSD: 10 * oneUSD, Action: "block",
	})
	req := AdmissionRequest{
		Scope: AdmissionScopeModelGateway, EstimateMicroUSD: oneUSD,
		IdempotencyKey: "model_gateway/sess-1/" + stableDigest,
	}

	first, err := m.Reserve(ctx, tenant, req)
	if err != nil || !first.Allowed || first.Handle == "" {
		t.Fatalf("first call: %+v err=%v", first, err)
	}
	if err := m.Commit(ctx, tenant, first.Handle, oneUSD); err != nil {
		t.Fatalf("commit: %v", err)
	}

	clk.advance(admissionReplayWindow / 2)
	retry, err := m.Reserve(ctx, tenant, req)
	if err != nil {
		t.Fatalf("retry: %v", err)
	}
	if !retry.Allowed || !retry.Replayed {
		t.Fatalf("a retry inside the replay window must replay the call it repeats: %+v", retry)
	}
	if retry.Handle != first.Handle {
		t.Fatalf("the replay handed back handle %q, not the one the call took (%q)", retry.Handle, first.Handle)
	}

	report, err := m.InspectReservations(ctx, tenant)
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	if report.Active != 0 || report.Committed != 1 {
		t.Fatalf("one call retried once left %+v: it must leave exactly one committed hold and no active one", report)
	}
}

// TestOutsideTheWindowABlownCapRefusesTheSameBytes is the one that was broken. The
// same bytes sent again is a SEPARATE call, not a retry, and a separate call is
// admitted against the ledger as it stands now — which by then is past its cap.
func TestOutsideTheWindowABlownCapRefusesTheSameBytes(t *testing.T) {
	m, st, tenant, _ := newFin(t)
	clk := &fakeClock{t: baseTime}
	m.clock = clk
	ctx := context.Background()
	createBudget(t, st, tenant, "global-block", budgetSpec{
		Dimension: "global", Period: "monthly", LimitMicroUSD: 5 * oneUSD, Action: "block",
	})
	req := AdmissionRequest{
		Scope: AdmissionScopeModelGateway, EstimateMicroUSD: oneUSD,
		IdempotencyKey: "model_gateway/sess-2/" + stableDigest,
	}

	first, err := m.Reserve(ctx, tenant, req)
	if err != nil || !first.Allowed {
		t.Fatalf("first call: %+v err=%v", first, err)
	}
	if err := m.Commit(ctx, tenant, first.Handle, oneUSD); err != nil {
		t.Fatalf("commit: %v", err)
	}

	// The tenant spends 50 USD against a 5 USD cap, and the window closes.
	m.ingest(t, tenant, mkCost("anthropic", "model", "s1", 1, 1, 50*oneUSD, baseTime))
	clk.advance(admissionReplayWindow + time.Second)

	again, err := m.Reserve(ctx, tenant, req)
	if err != nil {
		t.Fatalf("second call: %v", err)
	}
	if again.Allowed {
		t.Fatalf("the same bytes were admitted past a cap 50 USD over its 5 USD limit: %+v", again)
	}
	if again.Replayed {
		t.Fatalf("a call outside the replay window was answered as a retry: %+v", again)
	}
	if again.Action != "block" {
		t.Fatalf("Action = %q, want block", again.Action)
	}

	// The control: a key never used before refuses too, so the refusal above is the
	// cap and not the key.
	fresh, err := m.Reserve(ctx, tenant, AdmissionRequest{
		Scope: AdmissionScopeModelGateway, EstimateMicroUSD: oneUSD,
		IdempotencyKey: "model_gateway/sess-2/never-seen",
	})
	if err != nil || fresh.Allowed {
		t.Fatalf("a fresh key over the same blown cap must refuse: %+v err=%v", fresh, err)
	}
}

// TestOutsideTheWindowWithHeadroomTakesANewHold is the positive oracle of the pair.
// Re-evaluating is not refusing: the same bytes under a cap that still has room are
// admitted, and — because this is a new call and not a retry — they take a hold of
// their own instead of being handed the settled one.
func TestOutsideTheWindowWithHeadroomTakesANewHold(t *testing.T) {
	m, st, tenant, _ := newFin(t)
	clk := &fakeClock{t: baseTime}
	m.clock = clk
	ctx := context.Background()
	createBudget(t, st, tenant, "global-block", budgetSpec{
		Dimension: "global", Period: "monthly", LimitMicroUSD: 10 * oneUSD, Action: "block",
	})
	req := AdmissionRequest{
		Scope: AdmissionScopeModelGateway, EstimateMicroUSD: oneUSD,
		IdempotencyKey: "model_gateway/sess-3/" + stableDigest,
	}

	first, err := m.Reserve(ctx, tenant, req)
	if err != nil || !first.Allowed || first.Handle == "" {
		t.Fatalf("first call: %+v err=%v", first, err)
	}
	if err := m.Commit(ctx, tenant, first.Handle, oneUSD); err != nil {
		t.Fatalf("commit: %v", err)
	}

	clk.advance(admissionReplayWindow + time.Second)
	again, err := m.Reserve(ctx, tenant, req)
	if err != nil || !again.Allowed {
		t.Fatalf("the same bytes under a cap with headroom must be admitted: %+v err=%v", again, err)
	}
	if again.Replayed {
		t.Fatalf("a call outside the replay window was answered as a retry: %+v", again)
	}
	if again.Handle == "" || again.Handle == first.Handle {
		t.Fatalf("a separate call must take a hold of its own; it was handed %q (first: %q)", again.Handle, first.Handle)
	}

	report, err := m.InspectReservations(ctx, tenant)
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	if report.Active != 1 || report.Committed != 1 {
		t.Fatalf("after one settled call and one fresh one: %+v", report)
	}
}

// TestTheReplayWindowCannotOutliveTheHoldItHandsBack pins the relation the window's
// value rests on. A replay of a RESERVED row hands back a hold; past the reservation
// TTL that hold is expired and holds nothing. If the window were the longer of the
// two, a replay could return a handle to headroom nobody is withholding — and
// re-evaluating a row whose hold is still live would orphan that hold.
func TestTheReplayWindowCannotOutliveTheHoldItHandsBack(t *testing.T) {
	if admissionReplayWindow > reservationTTL {
		t.Fatalf("admissionReplayWindow (%v) outlives reservationTTL (%v)", admissionReplayWindow, reservationTTL)
	}
}

// stableDigest stands in for the hex SHA-256 of a request body: the component that
// made the model gateway's key the same on every identical call.
const stableDigest = "6f1c0d3b0f4b5a2e8c7d9a1b3e5f7092a4c6e8b0d2f4061a3c5e7981b3d5f709"
