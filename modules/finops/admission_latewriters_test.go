// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package finops

import (
	"context"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/store"
)

// -----------------------------------------------------------------------------
// EVERY LATE WRITER, not only the settlement that happens to commit. A caller can
// arrive late on four paths — it can commit, it can release, it can abandon the claim
// it staged, and it can compensate a verdict it could not publish — and each of them
// used to write the row unconditionally. A shared helper is not proof that all four
// hand it the right authority, so each has its own oracle here, and each asserts BOTH
// halves: the successor's admission survives untouched, and the late caller's own hold
// is accounted for rather than leaked or mistaken for the successor's.
// -----------------------------------------------------------------------------

// TestADelayedReleaseDoesNotReplaceTheCurrentAdmission is the release beside the
// commit. Returning the headroom of the handle it was given is right however late it
// arrives; writing the idempotency row is a claim on the KEY, and the key may already
// belong to a newer call whose hold the next retry has to be handed.
func TestADelayedReleaseDoesNotReplaceTheCurrentAdmission(t *testing.T) {
	forEachAdmissionEngine(t, runADelayedReleaseDoesNotReplaceTheCurrentAdmission)
}

func runADelayedReleaseDoesNotReplaceTheCurrentAdmission(t *testing.T, cfg store.Config) {
	m, st, tenant, _ := openFinCfg(t, cfg)
	clk := &fakeClock{t: baseTime}
	m.clock = clk
	createBudget(t, st, tenant, "cap", budgetSpec{Dimension: "global", Period: "monthly", LimitMicroUSD: 10 * oneUSD, Action: "block"})
	req := AdmissionRequest{Scope: AdmissionScopeModelGateway, IdempotencyKey: "model_gateway/late-release", EstimateMicroUSD: oneUSD}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	first, err := m.Reserve(ctx, tenant, req)
	if err != nil || !first.Allowed || first.Handle == "" {
		t.Fatalf("first: %+v %v", first, err)
	}

	d := &pausedReadData{ModuleData: m.data, nth: 1, reached: make(chan struct{}), resume: make(chan struct{})}
	m.data = d
	released := make(chan error, 1)
	go func() { released <- m.Release(pausedCtx(ctx), tenant, first.Handle) }()
	awaitPausedRead(t, d)
	clk.advance(admissionReplayWindow + time.Second)
	second, err := m.Reserve(ctx, tenant, req)
	close(d.resume)
	lateErr := <-released

	if err != nil || lateErr != nil || !second.Allowed || second.Handle == first.Handle {
		t.Fatalf("a late release of the old handle must succeed and a new call must be admitted: new=%+v err=%v lateErr=%v",
			second, err, lateErr)
	}
	row, found, err := m.lookupIdempotency(ctx, tenant, req.IdempotencyKey)
	if err != nil || !found {
		t.Fatalf("lookup: found=%v err=%v", found, err)
	}
	if row.state != admStateReserved || row.handle != second.Handle {
		t.Fatalf("the late release replaced the current admission: stored state=%q handle=%s, want reserved on %s",
			row.state, row.handle, second.Handle)
	}
	replay, err := m.Reserve(ctx, tenant, req)
	if err != nil {
		t.Fatal(err)
	}
	if !replay.Replayed || replay.Handle != second.Handle {
		t.Fatalf("the next retry was not handed the current hold: %+v, want a replay of %s", replay, second.Handle)
	}
	report, err := m.InspectReservations(ctx, tenant)
	if err != nil {
		t.Fatal(err)
	}
	if report.Active != 1 {
		t.Fatalf("after a late release %d holds are active, want only the successor's: %+v", report.Active, report)
	}
}

// TestADelayedAbandonmentDoesNotClearItsSuccessorsAdmission is the third late writer,
// and the one with no money of its own to settle. A caller whose claim was taken from
// it while it evaluated reaches a verdict for a key it no longer holds — here a DENY,
// because the successor took the last of the headroom. Writing that verdict as an
// abandonment would release the successor's admission and orphan the successor's hold:
// one caller's bad luck erasing another caller's answer. The refused write sends this
// caller back to read the row that now owns the key, and a retry of the same call is
// handed that call's admission, which is what the key is for.
func TestADelayedAbandonmentDoesNotClearItsSuccessorsAdmission(t *testing.T) {
	forEachAdmissionEngine(t, runADelayedAbandonmentDoesNotClearItsSuccessorsAdmission)
}

func runADelayedAbandonmentDoesNotClearItsSuccessorsAdmission(t *testing.T, cfg store.Config) {
	m, st, tenant, _ := openFinCfg(t, cfg)
	clk := &fakeClock{t: baseTime}
	m.clock = clk
	// Room for exactly one hold of the estimate, so the successor's admission is what
	// makes the delayed caller's own evaluation refuse.
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
	delayed := <-original

	if err != nil || !successor.Allowed || successor.Handle == "" {
		t.Fatalf("the successor must be admitted: %+v %v", successor, err)
	}
	if delayed.err != nil {
		t.Fatalf("the delayed caller errored: %v", delayed.err)
	}
	row, found, err := m.lookupIdempotency(ctx, tenant, req.IdempotencyKey)
	if err != nil || !found {
		t.Fatalf("lookup: found=%v err=%v", found, err)
	}
	if row.state != admStateReserved || row.handle != successor.Handle {
		t.Fatalf("a delayed abandonment cleared its successor's admission: stored state=%q handle=%s, want reserved on %s",
			row.state, row.handle, successor.Handle)
	}
	if !delayed.res.Allowed || !delayed.res.Replayed || delayed.res.Handle != successor.Handle {
		t.Fatalf("the delayed caller was answered from a claim it no longer held: %+v, want a replay of %s",
			delayed.res, successor.Handle)
	}
	report, err := m.InspectReservations(ctx, tenant)
	if err != nil {
		t.Fatal(err)
	}
	if report.Active != 1 || report.IdempotencyOrphans != 0 || report.Drift {
		t.Fatalf("a delayed abandonment drifted the ledger: %+v (successor=%s)", report, successor.Handle)
	}
}

// TestAFailedPublicationReturnsItsOwnHoldAndKeepsTheSuccessors is the compensation, and
// it is the half a fence alone does not deliver. Refusing the late caller's write keeps
// the successor's admission; it says nothing about the hold that caller took on its way
// to a verdict it cannot publish. That hold is not the successor's to keep and not this
// call's to leak, so it goes back — and it is RELEASED, which the ledger records, rather
// than left to lapse quietly at its TTL.
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
	clk.advance(admissionClaimTakeover + time.Second)
	successor, err := m.Reserve(ctx, tenant, req)
	close(d.resume)
	delayed := <-original

	if err != nil || !successor.Allowed || successor.Handle == "" {
		t.Fatalf("the successor must be admitted: %+v %v", successor, err)
	}
	if delayed.err != nil {
		t.Fatalf("the delayed caller errored: %v", delayed.err)
	}
	// The cap had room for both, so this caller DID take a hold before it found the key
	// gone. It is answered from the successor's admission, not from its own.
	if !delayed.res.Allowed || !delayed.res.Replayed || delayed.res.Handle != successor.Handle {
		t.Fatalf("the caller that could not publish was answered from its own verdict: %+v, want a replay of %s",
			delayed.res, successor.Handle)
	}
	report, err := m.InspectReservations(ctx, tenant)
	if err != nil {
		t.Fatal(err)
	}
	if report.Active != 1 || report.Drift {
		t.Fatalf("the unpublishable verdict leaked its hold: %+v (successor=%s)", report, successor.Handle)
	}
	// RELEASED, not lapsed: the count distinguishes a hold handed back by the caller
	// that took it from one nobody ever settled.
	if report.Released != 1 {
		t.Fatalf("released = %d, want the one hold the caller that could not publish handed back: %+v",
			report.Released, report)
	}
}
