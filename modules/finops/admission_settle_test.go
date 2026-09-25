// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package finops

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// -----------------------------------------------------------------------------
// SETTLEMENT. Commit and Release settle both components of a hold in one transaction,
// with the admission row that names it. The money is settled however late the call
// arrives: a commit after a release, an expiry or a takeover records what the effect
// did, and keeps the instant the withholding ended. The key is written only while it
// still names the hold, so an old hold never overwrites a newer admission.
//
// The fixture unless a test says otherwise: budget B and seat limit S of actor "a"
// (seedBudgetAndSeatLimit), requests of actor "a" for 2 000 000 µUSD, and a measured
// cost of 1 500 000 µUSD.
// -----------------------------------------------------------------------------

// measured is the actual cost the settlements record.
const measured = int64(1_500_000)

// reserveOK reserves req and fails the test unless it is admitted under a hold.
func reserveOK(t *testing.T, m *Module, tenant model.TenantID, req AdmissionRequest) Reservation {
	t.Helper()
	res, err := m.Reserve(context.Background(), tenant, req)
	if err != nil || !res.Allowed || res.Handle == "" {
		t.Fatalf("reserve %s: %+v err=%v", req.IdempotencyKey, res, err)
	}
	return res
}

// assertRowsUnder asserts that h has exactly n ledger rows, each in state with actual,
// and, when at is not zero, each settled at at.
func assertRowsUnder(t *testing.T, st store.Store, tenant model.TenantID, h holdID, n int, state string, actual int64, at time.Time) {
	t.Helper()
	rows := ledgerRowsUnder(t, st, tenant, h)
	if len(rows) != n {
		t.Fatalf("%d ledger row(s) under %s, want %d", len(rows), h, n)
	}
	for _, r := range rows {
		kind := r.String(colResvPolicyKind)
		if r.String(colResvState) != state || r.Int(colResvActual) != actual {
			t.Fatalf("the %s row under %s is %s with actual %d, want %s with %d", kind, h, r.String(colResvState), r.Int(colResvActual), state, actual)
		}
		if !at.IsZero() && r.String(colResvSettledAt) != model.NewTimestamp(at).String() {
			t.Fatalf("the %s row under %s was settled at %q, want %s", kind, h, r.String(colResvSettledAt), model.NewTimestamp(at))
		}
	}
}

// assertAdmission asserts that the row of key is in state, naming h and owing nothing.
func assertAdmission(t *testing.T, m *Module, tenant model.TenantID, key, state string, h holdID) admissionRow {
	t.Helper()
	row := admissionRowOf(t, m, tenant, key)
	if row.state != state || row.handle != h || len(row.owed) != 0 {
		t.Fatalf("the row of %s is %s naming %q owing %d, want %s naming %q owing nothing", key, row.state, row.handle, len(row.owed), state, h)
	}
	return row
}

// assertWithheld asserts what the ledger withholds at now, per component.
func assertWithheld(t *testing.T, st store.Store, tenant model.TenantID, now time.Time, budget, seat int64) {
	t.Helper()
	if b, s := withheld(t, st, tenant, now); b != budget || s != seat {
		t.Fatalf("withheld %d/%d, want %d/%d", b, s, budget, seat)
	}
}

// settlementFixture is one tenant under the standard fixture with a clock at baseTime.
func settlementFixture(t *testing.T, cfg store.Config) (*Module, store.Store, model.TenantID, *fakeClock) {
	t.Helper()
	m, st, tenant, _ := openFinCfg(t, cfg)
	clk := &fakeClock{t: baseTime}
	m.clock = clk
	seedBudgetAndSeatLimit(t, st, tenant)
	return m, st, tenant, clk
}

// TestCommitSettlesBothComponents: a commit of a hold settles its budget row and its
// seat row in one transaction, at the measured cost and the commit's instant, and the
// admission becomes committed. A caller with no seat limit has one budget row, which
// is settled alike.
func TestCommitSettlesBothComponents(t *testing.T) {
	forEachAdmissionEngine(t, runCommitSettlesBothComponents)
}

func runCommitSettlesBothComponents(t *testing.T, cfg store.Config) {
	t.Run("a budget row and a seat row", func(t *testing.T) {
		m, st, tenant, clk := settlementFixture(t, cfg)
		req := seatRequest("model_gateway/k")
		h := holdID(reserveOK(t, m, tenant, req).Handle)
		clk.advance(10 * time.Second)
		if err := m.Commit(context.Background(), tenant, h.String(), measured); err != nil {
			t.Fatalf("commit: %v", err)
		}
		assertAdmission(t, m, tenant, req.IdempotencyKey, admStateCommitted, h)
		assertRowsUnder(t, st, tenant, h, 2, resvStateCommitted, measured, clk.t)
		for _, r := range ledgerRowsUnder(t, st, tenant, h) {
			if r.Int(colResvAmount) != 2*oneUSD {
				t.Fatalf("the %s row holds %d, want the estimate", r.String(colResvPolicyKind), r.Int(colResvAmount))
			}
		}
		if kinds := componentsOf(ledgerRowsUnder(t, st, tenant, h)); kinds[policyKindBudget] != 1 || kinds[policyKindSpendLimit] != 1 {
			t.Fatalf("the hold's rows are %v, want one of each component", kinds)
		}
		assertWithheld(t, st, tenant, clk.t, 0, 0)
	})

	t.Run("an actor with no seat limit", func(t *testing.T) {
		m, st, tenant, clk := settlementFixture(t, cfg)
		req := AdmissionRequest{Scope: AdmissionScopeModelGateway, ActorRef: "c", EstimateMicroUSD: 2 * oneUSD, IdempotencyKey: "model_gateway/k"}
		h := holdID(reserveOK(t, m, tenant, req).Handle)
		if err := m.Commit(context.Background(), tenant, h.String(), measured); err != nil {
			t.Fatalf("commit: %v", err)
		}
		assertAdmission(t, m, tenant, req.IdempotencyKey, admStateCommitted, h)
		assertRowsUnder(t, st, tenant, h, 1, resvStateCommitted, measured, clk.t)
		if kinds := componentsOf(ledgerRowsUnder(t, st, tenant, h)); kinds[policyKindBudget] != 1 {
			t.Fatalf("the hold's rows are %v, want its one budget row", kinds)
		}
	})
}

// TestReleaseSettlesBothComponents: a release of a hold returns both components'
// headroom in one transaction, with an actual of zero, and the admission becomes
// released.
func TestReleaseSettlesBothComponents(t *testing.T) {
	forEachAdmissionEngine(t, runReleaseSettlesBothComponents)
}

func runReleaseSettlesBothComponents(t *testing.T, cfg store.Config) {
	m, st, tenant, clk := settlementFixture(t, cfg)
	req := seatRequest("model_gateway/k")
	h := holdID(reserveOK(t, m, tenant, req).Handle)
	clk.advance(10 * time.Second)
	if err := m.Release(context.Background(), tenant, h.String()); err != nil {
		t.Fatalf("release: %v", err)
	}
	assertAdmission(t, m, tenant, req.IdempotencyKey, admStateReleased, h)
	assertRowsUnder(t, st, tenant, h, 2, resvStateReleased, 0, clk.t)
	assertWithheld(t, st, tenant, clk.t, 0, 0)
}

// TestRepeatedCommitIsReplay: a commit repeated with the same amount — a retry, or a
// call whose acknowledgment was lost — answers nil and writes nothing: not a ledger row
// and not the admission row, whose state and date stay as the first commit left them.
func TestRepeatedCommitIsReplay(t *testing.T) {
	forEachAdmissionEngine(t, runRepeatedCommitIsReplay)
}

func runRepeatedCommitIsReplay(t *testing.T, cfg store.Config) {
	m, st, tenant, clk := settlementFixture(t, cfg)
	req := seatRequest("model_gateway/k")
	h := holdID(reserveOK(t, m, tenant, req).Handle)
	if err := m.Commit(context.Background(), tenant, h.String(), measured); err != nil {
		t.Fatalf("commit: %v", err)
	}
	first := clk.t
	admissions, ledger := rowsByID(t, st, tenant, admissionIdempotencyKind), rowsByID(t, st, tenant, budgetReservationKind)
	clk.advance(time.Minute)
	if err := m.Commit(context.Background(), tenant, h.String(), measured); err != nil {
		t.Fatalf("the repeated commit: %v", err)
	}
	assertRowsUnchanged(t, "the admission rows", admissions, rowsByID(t, st, tenant, admissionIdempotencyKind))
	assertRowsUnchanged(t, "the ledger", ledger, rowsByID(t, st, tenant, budgetReservationKind))
	assertRowsUnder(t, st, tenant, h, 2, resvStateCommitted, measured, first)
}

// TestCommitWithOtherActualConflicts: a commit of a committed hold with another amount
// is ErrSettlementConflict and writes nothing: the first measured cost stands.
func TestCommitWithOtherActualConflicts(t *testing.T) {
	forEachAdmissionEngine(t, runCommitWithOtherActualConflicts)
}

func runCommitWithOtherActualConflicts(t *testing.T, cfg store.Config) {
	m, st, tenant, clk := settlementFixture(t, cfg)
	req := seatRequest("model_gateway/k")
	h := holdID(reserveOK(t, m, tenant, req).Handle)
	if err := m.Commit(context.Background(), tenant, h.String(), measured); err != nil {
		t.Fatalf("commit: %v", err)
	}
	first := clk.t
	admissions, ledger := rowsByID(t, st, tenant, admissionIdempotencyKind), rowsByID(t, st, tenant, budgetReservationKind)
	clk.advance(time.Minute)
	if err := m.Commit(context.Background(), tenant, h.String(), measured+1); !errors.Is(err, ErrSettlementConflict) {
		t.Fatalf("a commit with another amount: %v, want ErrSettlementConflict", err)
	}
	assertRowsUnchanged(t, "the admission rows", admissions, rowsByID(t, st, tenant, admissionIdempotencyKind))
	assertRowsUnchanged(t, "the ledger", ledger, rowsByID(t, st, tenant, budgetReservationKind))
	assertAdmission(t, m, tenant, req.IdempotencyKey, admStateCommitted, h)
	assertRowsUnder(t, st, tenant, h, 2, resvStateCommitted, measured, first)
}

// TestReleaseNeverOverwritesCommit: a release after a commit answers nil and writes
// nothing: a fact beats an earlier absence, never the other way round.
func TestReleaseNeverOverwritesCommit(t *testing.T) {
	forEachAdmissionEngine(t, runReleaseNeverOverwritesCommit)
}

func runReleaseNeverOverwritesCommit(t *testing.T, cfg store.Config) {
	m, st, tenant, clk := settlementFixture(t, cfg)
	req := seatRequest("model_gateway/k")
	h := holdID(reserveOK(t, m, tenant, req).Handle)
	if err := m.Commit(context.Background(), tenant, h.String(), measured); err != nil {
		t.Fatalf("commit: %v", err)
	}
	first := clk.t
	admissions, ledger := rowsByID(t, st, tenant, admissionIdempotencyKind), rowsByID(t, st, tenant, budgetReservationKind)
	clk.advance(time.Minute)
	if err := m.Release(context.Background(), tenant, h.String()); err != nil {
		t.Fatalf("a release after the commit: %v", err)
	}
	assertRowsUnchanged(t, "the admission rows", admissions, rowsByID(t, st, tenant, admissionIdempotencyKind))
	assertRowsUnchanged(t, "the ledger", ledger, rowsByID(t, st, tenant, budgetReservationKind))
	assertAdmission(t, m, tenant, req.IdempotencyKey, admStateCommitted, h)
	assertRowsUnder(t, st, tenant, h, 2, resvStateCommitted, measured, first)
}

// TestLateCommitAfterReleaseOrExpiry: a commit that arrives after the hold was released,
// or after it lapsed and the sweep expired it, still records that the effect ran: every
// row becomes committed with the measured cost and keeps the instant its withholding
// ended, and the admission becomes committed.
func TestLateCommitAfterReleaseOrExpiry(t *testing.T) {
	forEachAdmissionEngine(t, runLateCommitAfterReleaseOrExpiry)
}

func runLateCommitAfterReleaseOrExpiry(t *testing.T, cfg store.Config) {
	t.Run("after a release", func(t *testing.T) {
		m, st, tenant, clk := settlementFixture(t, cfg)
		req := seatRequest("model_gateway/k")
		h := holdID(reserveOK(t, m, tenant, req).Handle)
		clk.advance(10 * time.Second)
		released := clk.t
		if err := m.Release(context.Background(), tenant, h.String()); err != nil {
			t.Fatalf("release: %v", err)
		}
		clk.advance(10 * time.Second)
		if err := m.Commit(context.Background(), tenant, h.String(), measured); err != nil {
			t.Fatalf("the late commit: %v", err)
		}
		assertAdmission(t, m, tenant, req.IdempotencyKey, admStateCommitted, h)
		assertRowsUnder(t, st, tenant, h, 2, resvStateCommitted, measured, released)
		assertWithheld(t, st, tenant, clk.t, 0, 0)
	})

	t.Run("after the sweep expired the hold", func(t *testing.T) {
		m, st, tenant, clk := settlementFixture(t, cfg)
		req := seatRequest("model_gateway/k")
		h := holdID(reserveOK(t, m, tenant, req).Handle)
		clk.advance(reservationTTL + 10*time.Second)
		swept := clk.t
		if n, err := m.SweepExpiredReservations(context.Background(), tenant); err != nil || n != 2 {
			t.Fatalf("the sweep expired %d row(s) err=%v, want the hold's two", n, err)
		}
		clk.advance(10 * time.Second)
		if err := m.Commit(context.Background(), tenant, h.String(), measured); err != nil {
			t.Fatalf("the late commit: %v", err)
		}
		assertAdmission(t, m, tenant, req.IdempotencyKey, admStateCommitted, h)
		assertRowsUnder(t, st, tenant, h, 2, resvStateCommitted, measured, swept)
	})
}

// TestLateCommitAfterTakeoverKeepsSuccessor: the commit of a hold whose key has moved to
// a newer admission settles that hold's money and writes nothing else: the key's row
// still publishes the successor's hold, which still withholds. The rows keep the instant
// their withholding ended — the sweep's, or the takeover's create's — or take the
// commit's when they were still active. It holds for a hold the caller received and for
// one it never did.
func TestLateCommitAfterTakeoverKeepsSuccessor(t *testing.T) {
	forEachAdmissionEngine(t, runLateCommitAfterTakeoverKeepsSuccessor)
}

func runLateCommitAfterTakeoverKeepsSuccessor(t *testing.T, cfg store.Config) {
	// successorStands asserts that the key still publishes successor, unchanged by the
	// commit, and that its hold still withholds both components.
	successorStands := func(t *testing.T, m *Module, st store.Store, tenant model.TenantID, key string, successor holdID, before admissionRow) {
		t.Helper()
		after := assertAdmission(t, m, tenant, key, admStateReserved, successor)
		if after.version != before.version {
			t.Fatalf("the late commit wrote the successor's row: version %d → %d", before.version, after.version)
		}
		assertRowsUnder(t, st, tenant, successor, 2, resvStateActive, 0, time.Time{})
	}

	for _, swept := range []bool{true, false} {
		name := "the commit comes before any sweep"
		if swept {
			name = "the sweep comes first"
		}
		t.Run(name, func(t *testing.T) {
			m, st, tenant, clk := settlementFixture(t, cfg)
			req := seatRequest("model_gateway/k")
			h := holdID(reserveOK(t, m, tenant, req).Handle)
			clk.advance(reservationTTL + time.Second)
			successor := reserveOK(t, m, tenant, req)
			if successor.Replayed || successor.Handle == h.String() {
				t.Fatalf("after the window a new call must take the key over: %+v", successor)
			}
			before := admissionRowOf(t, m, tenant, req.IdempotencyKey)
			clk.advance(10 * time.Second)
			settledAt := clk.t
			if swept {
				if n, err := m.SweepExpiredReservations(context.Background(), tenant); err != nil || n != 2 {
					t.Fatalf("the sweep expired %d row(s) err=%v, want the old hold's two", n, err)
				}
				clk.advance(10 * time.Second)
			}
			if err := m.Commit(context.Background(), tenant, h.String(), measured); err != nil {
				t.Fatalf("the late commit: %v", err)
			}
			assertRowsUnder(t, st, tenant, h, 2, resvStateCommitted, measured, settledAt)
			successorStands(t, m, st, tenant, req.IdempotencyKey, holdID(successor.Handle), before)
			assertWithheld(t, st, tenant, clk.t, 2*oneUSD, 2*oneUSD)
		})
	}

	t.Run("a hold the caller never received", func(t *testing.T) {
		// A creates H1 and cannot publish it or give it back; B takes over and can
		// neither create nor give back; C takes over, its create releases H1, and C
		// publishes H3. The commit of H1 is late and changes nothing but H1.
		m, st, tenant, clk := settlementFixture(t, cfg)
		req := seatRequest("model_gateway/k")
		ctx := pausedCtx(context.Background())
		base := m.data

		m.data = &refusedWritesData{ModuleData: base, from: 3, to: 5}
		if res, err := m.Reserve(ctx, tenant, req); err != nil || res.Allowed {
			t.Fatalf("A: %+v err=%v; want a refusal", res, err)
		}
		h1 := admissionRowOf(t, m, tenant, req.IdempotencyKey).handle
		clk.advance(admissionClaimTakeover + time.Second)
		m.data = &refusedWritesData{ModuleData: base, from: 2, to: 1 << 30}
		if res, err := m.Reserve(ctx, tenant, req); err != nil || res.Allowed {
			t.Fatalf("B: %+v err=%v; want a refusal", res, err)
		}
		h2 := admissionRowOf(t, m, tenant, req.IdempotencyKey).handle
		m.data = base
		clk.advance(admissionClaimTakeover + time.Second)
		releasedAt := clk.t
		c := reserveOK(t, m, tenant, req)
		before := admissionRowOf(t, m, tenant, req.IdempotencyKey)
		assertRowsUnder(t, st, tenant, h1, 2, resvStateReleased, 0, releasedAt)

		clk.advance(10 * time.Second)
		if err := m.Commit(context.Background(), tenant, h1.String(), measured); err != nil {
			t.Fatalf("the late commit of H1: %v", err)
		}
		assertRowsUnder(t, st, tenant, h1, 2, resvStateCommitted, measured, releasedAt)
		if rows := ledgerRowsUnder(t, st, tenant, h2); len(rows) != 0 {
			t.Fatalf("%d row(s) under the hold B never created", len(rows))
		}
		successorStands(t, m, st, tenant, req.IdempotencyKey, holdID(c.Handle), before)
	})

	t.Run("a hold the caller received", func(t *testing.T) {
		// R publishes H1; after the window R' takes over and can neither create nor give
		// back; R'' takes over, leaves the lapsed H1 to the sweep and publishes H3.
		m, st, tenant, clk := settlementFixture(t, cfg)
		req := seatRequest("model_gateway/k")
		h1 := holdID(reserveOK(t, m, tenant, req).Handle)
		clk.advance(reservationTTL + time.Second)
		base := m.data
		m.data = &refusedWritesData{ModuleData: base, from: 2, to: 1 << 30}
		if res, err := m.Reserve(pausedCtx(context.Background()), tenant, req); err != nil || res.Allowed {
			t.Fatalf("R': %+v err=%v; want a refusal", res, err)
		}
		h2 := admissionRowOf(t, m, tenant, req.IdempotencyKey).handle
		m.data = base
		clk.advance(admissionClaimTakeover + time.Second)
		third := reserveOK(t, m, tenant, req)
		before := admissionRowOf(t, m, tenant, req.IdempotencyKey)
		assertRowsUnder(t, st, tenant, h1, 2, resvStateActive, 0, time.Time{})

		clk.advance(10 * time.Second)
		if err := m.Commit(context.Background(), tenant, h1.String(), measured); err != nil {
			t.Fatalf("the late commit of H1: %v", err)
		}
		assertRowsUnder(t, st, tenant, h1, 2, resvStateCommitted, measured, clk.t)
		if rows := ledgerRowsUnder(t, st, tenant, h2); len(rows) != 0 {
			t.Fatalf("%d row(s) under the hold R' never created", len(rows))
		}
		successorStands(t, m, st, tenant, req.IdempotencyKey, holdID(third.Handle), before)
	})
}

// TestUncertainCommitRetryEndsCommitted: a commit whose transaction's end is unknown is
// retried by the caller after the hold has lapsed, and ends committed with the measured
// cost either way: if the first commit took effect the retry is a repeat; if it did not,
// the retry commits the rows, keeping the sweep's instant when the sweep came first.
func TestUncertainCommitRetryEndsCommitted(t *testing.T) {
	forEachAdmissionEngine(t, runUncertainCommitRetryEndsCommitted)
}

func runUncertainCommitRetryEndsCommitted(t *testing.T, cfg store.Config) {
	for _, tc := range []struct {
		name  string
		mode  faultMode
		sweep bool
	}{
		{"the first commit took effect", faultAfterCommit, false},
		{"the first commit rolled back, then the sweep", faultAfterRollback, true},
		{"the first commit rolled back, no sweep", faultAfterRollback, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, st, tenant, clk := settlementFixture(t, cfg)
			req := seatRequest("model_gateway/k")
			h := holdID(reserveOK(t, m, tenant, req).Handle)
			clk.advance(10 * time.Second)
			firstAt := clk.t
			base := m.data
			d := &faultData{ModuleData: base, nth: 1, mode: tc.mode}
			m.data = d
			if err := m.Commit(pausedCtx(context.Background()), tenant, h.String(), measured); err == nil || !d.fired.Load() {
				t.Fatalf("the uncertain commit answered %v (fault fired: %v); want an error", err, d.fired.Load())
			}
			m.data = base

			clk.advance(reservationTTL)
			want := clk.t
			if tc.sweep {
				if _, err := m.SweepExpiredReservations(context.Background(), tenant); err != nil {
					t.Fatalf("sweep: %v", err)
				}
				clk.advance(10 * time.Second)
			} else if tc.mode == faultAfterCommit {
				want = firstAt
			}
			if err := m.Commit(context.Background(), tenant, h.String(), measured); err != nil {
				t.Fatalf("the retry: %v", err)
			}
			assertAdmission(t, m, tenant, req.IdempotencyKey, admStateCommitted, h)
			assertRowsUnder(t, st, tenant, h, 2, resvStateCommitted, measured, want)
		})
	}
}

// TestCommitOnPendingIntentRefused: a hold whose admission is still a claim — before its
// create, or after it and before the publication — was handed to no caller, so no caller
// can settle it: Commit and Release are ErrAdmissionPending and write nothing.
func TestCommitOnPendingIntentRefused(t *testing.T) {
	forEachAdmissionEngine(t, runCommitOnPendingIntentRefused)
}

func runCommitOnPendingIntentRefused(t *testing.T, cfg store.Config) {
	m, st, tenant, _ := settlementFixture(t, cfg)
	claimed := stagePendingClaim(t, m, tenant, seatRequest("model_gateway/claimed"))
	created := stageClaimWithHold(t, m, tenant, seatRequest("model_gateway/created"))
	admissions, ledger := rowsByID(t, st, tenant, admissionIdempotencyKind), rowsByID(t, st, tenant, budgetReservationKind)

	for _, h := range []holdID{claimed, created} {
		if err := m.Commit(context.Background(), tenant, h.String(), measured); !errors.Is(err, ErrAdmissionPending) {
			t.Errorf("commit of %s: %v, want ErrAdmissionPending", h, err)
		}
		if err := m.Release(context.Background(), tenant, h.String()); !errors.Is(err, ErrAdmissionPending) {
			t.Errorf("release of %s: %v, want ErrAdmissionPending", h, err)
		}
	}
	assertRowsUnchanged(t, "the admission rows", admissions, rowsByID(t, st, tenant, admissionIdempotencyKind))
	assertRowsUnchanged(t, "the ledger", ledger, rowsByID(t, st, tenant, budgetReservationKind))
	if rows := ledgerRowsUnder(t, st, tenant, claimed); len(rows) != 0 {
		t.Fatalf("%d row(s) under a claim that never created", len(rows))
	}
}

// TestMalformedHandleIsInvalid: a handle that is not a hold identity — not a UUID, or a
// UUID spelled as the store never writes one — and a negative amount are
// ErrInvalidAdmission, and nothing is read or written for them.
func TestMalformedHandleIsInvalid(t *testing.T) {
	forEachAdmissionEngine(t, runMalformedHandleIsInvalid)
}

func runMalformedHandleIsInvalid(t *testing.T, cfg store.Config) {
	m, st, tenant, _ := settlementFixture(t, cfg)
	h := reserveOK(t, m, tenant, seatRequest("model_gateway/k")).Handle
	admissions, ledger := rowsByID(t, st, tenant, admissionIdempotencyKind), rowsByID(t, st, tenant, budgetReservationKind)
	ctx := context.Background()

	for _, bad := range []string{"x1", strings.ToUpper(h), "{" + h + "}"} {
		if err := m.Commit(ctx, tenant, bad, measured); !errors.Is(err, ErrInvalidAdmission) {
			t.Errorf("commit of %q: %v, want ErrInvalidAdmission", bad, err)
		}
		if err := m.Release(ctx, tenant, bad); !errors.Is(err, ErrInvalidAdmission) {
			t.Errorf("release of %q: %v, want ErrInvalidAdmission", bad, err)
		}
	}
	if err := m.Commit(ctx, tenant, h, -1); !errors.Is(err, ErrInvalidAdmission) {
		t.Errorf("commit of a negative amount: %v, want ErrInvalidAdmission", err)
	}
	assertRowsUnchanged(t, "the admission rows", admissions, rowsByID(t, st, tenant, admissionIdempotencyKind))
	assertRowsUnchanged(t, "the ledger", ledger, rowsByID(t, st, tenant, budgetReservationKind))
}

// linkToAttempt marks the first ledger row under h as belonging to the attempt
// lifecycle.
func linkToAttempt(t *testing.T, st store.Store, tenant model.TenantID, h holdID) {
	t.Helper()
	if err := st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(budgetReservationKind)
		if err != nil {
			return err
		}
		recs, _, err := repo.List(context.Background(), model.Query{Filters: []model.Filter{eq(colResvHandle, h.String())}, Limit: 1})
		if err != nil || len(recs) != 1 {
			t.Fatalf("fixture: rows under %s: %d err=%v", h, len(recs), err)
		}
		recs[0][colResvAttemptRef] = "0123456789abcdef0123456789abcdef"
		recs[0][colResvLifecycleVersion] = lifecycleLinkageVersion
		_, err = repo.Update(context.Background(), recs[0])
		return err
	}); err != nil {
		t.Fatalf("link a row to an attempt: %v", err)
	}
}

// TestSettlementKeepsLedgerRefusals: the ledger's own refusals stand for a settlement
// through the admission — under a committed activation frontier, and for a hold one of
// whose rows belongs to the attempt lifecycle, Commit and Release are the typed
// lifecycle_api_required refusal and write neither a ledger row nor the admission row.
func TestSettlementKeepsLedgerRefusals(t *testing.T) {
	forEachAdmissionEngine(t, runSettlementKeepsLedgerRefusals)
}

func runSettlementKeepsLedgerRefusals(t *testing.T, cfg store.Config) {
	refused := func(t *testing.T, m *Module, st store.Store, tenant model.TenantID, h string) {
		t.Helper()
		admissions, ledger := rowsByID(t, st, tenant, admissionIdempotencyKind), rowsByID(t, st, tenant, budgetReservationKind)
		if err := m.Commit(context.Background(), tenant, h, measured); attemptCode(err) != errCodeLifecycleAPIRequired {
			t.Errorf("commit: code %q (err=%v), want lifecycle_api_required", attemptCode(err), err)
		}
		if err := m.Release(context.Background(), tenant, h); attemptCode(err) != errCodeLifecycleAPIRequired {
			t.Errorf("release: code %q (err=%v), want lifecycle_api_required", attemptCode(err), err)
		}
		assertRowsUnchanged(t, "the admission rows", admissions, rowsByID(t, st, tenant, admissionIdempotencyKind))
		assertRowsUnchanged(t, "the ledger", ledger, rowsByID(t, st, tenant, budgetReservationKind))
	}

	t.Run("under a committed frontier", func(t *testing.T) {
		m, st, tenant, _ := settlementFixture(t, cfg)
		WithAttemptEvidenceVerifier(newLabVerifier(tenant))(m)
		h := reserveOK(t, m, tenant, seatRequest("model_gateway/k")).Handle
		if _, err := m.BeginLifecycleActivation(context.Background(), tenant, LifecycleActivationRequest{
			Evidence: []EvidenceRef{labEvidence("quiescence"), labEvidence("caller-readiness")},
		}); err != nil {
			t.Fatalf("begin the frontier: %v", err)
		}
		refused(t, m, st, tenant, h)
	})

	t.Run("a row of the hold belongs to an attempt", func(t *testing.T) {
		m, st, tenant, _ := settlementFixture(t, cfg)
		h := reserveOK(t, m, tenant, seatRequest("model_gateway/k")).Handle
		linkToAttempt(t, st, tenant, holdID(h))
		refused(t, m, st, tenant, h)
	})
}

// TestSpendOnlyHoldIsSettled: with no budget, a request of an actor with a seat limit is
// held by its one seat row, and the handle settles it like any other hold.
func TestSpendOnlyHoldIsSettled(t *testing.T) {
	forEachAdmissionEngine(t, runSpendOnlyHoldIsSettled)
}

func runSpendOnlyHoldIsSettled(t *testing.T, cfg store.Config) {
	m, st, tenant, _ := openFinCfg(t, cfg)
	clk := &fakeClock{t: baseTime}
	m.clock = clk
	if err := st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		_, err := sc.Policies().Create(context.Background(), model.Policy{
			Name: "S", Kind: policyKindSpendLimit, Enabled: true,
			Spec: map[string]any{
				"scope_type": "user", "scope_key": "a",
				"amount_micro_usd": int64(10 * oneUSD), "period": "daily",
			},
		})
		return err
	}); err != nil {
		t.Fatalf("create the spend limit: %v", err)
	}
	req := seatRequest("model_gateway/k")
	h := holdID(reserveOK(t, m, tenant, req).Handle)
	if kinds := componentsOf(ledgerRowsUnder(t, st, tenant, h)); kinds[policyKindSpendLimit] != 1 || kinds[policyKindBudget] != 0 {
		t.Fatalf("the hold's rows are %v, want its one seat row", kinds)
	}
	if err := m.Commit(context.Background(), tenant, h.String(), measured); err != nil {
		t.Fatalf("commit: %v", err)
	}
	assertAdmission(t, m, tenant, req.IdempotencyKey, admStateCommitted, h)
	assertRowsUnder(t, st, tenant, h, 1, resvStateCommitted, measured, clk.t)
	assertWithheld(t, st, tenant, clk.t, 0, 0)
}

// TestCommitSettlesLegacyPair: a pair an earlier build published — a budget hold and a
// seat hold — is replayed with its budget hold, and a commit of either hold settles the
// rows under both, and the admission, in one transaction.
func TestCommitSettlesLegacyPair(t *testing.T) {
	forEachAdmissionEngine(t, runCommitSettlesLegacyPair)
}

func runCommitSettlesLegacyPair(t *testing.T, cfg store.Config) {
	for _, bySeat := range []bool{false, true} {
		name := "committed by its budget hold"
		if bySeat {
			name = "committed by its seat hold"
		}
		t.Run(name, func(t *testing.T) {
			m, st, tenant, _ := openFinCfg(t, cfg)
			tu := baseTime
			clk := &fakeClock{t: tu}
			m.clock = clk
			budget, limit := seedBudgetAndSeatLimit(t, st, tenant)
			req := seatRequest("model_gateway/legacy-c")
			dated := tu.Add(-60 * time.Second)
			h1, h2 := legacyPair(t, st, tenant, req, budget, limit, dated, dated.Add(reservationTTL), dated.Add(reservationTTL), true)
			clk.advance(100 * time.Second)

			retry, err := m.Reserve(context.Background(), tenant, req)
			if err != nil || !retry.Allowed || !retry.Replayed || retry.Handle != h1.String() {
				t.Fatalf("a retry of the pair: %+v err=%v, want a replay of its budget hold", retry, err)
			}
			settle := h1
			if bySeat {
				settle = h2
			}
			if err := m.Commit(context.Background(), tenant, settle.String(), measured); err != nil {
				t.Fatalf("commit: %v", err)
			}
			row := admissionRowOf(t, m, tenant, req.IdempotencyKey)
			if row.state != admStateCommitted || row.handle != h1 || row.spendHandle != h2 {
				t.Fatalf("the pair's row is %s naming %q and %q, want committed naming both", row.state, row.handle, row.spendHandle)
			}
			assertRowsUnder(t, st, tenant, h1, 1, resvStateCommitted, measured, clk.t)
			assertRowsUnder(t, st, tenant, h2, 1, resvStateCommitted, measured, clk.t)
		})
	}
}

// TestCommitSettlesLegacySpendOnlyRow: an earlier build published a seat hold alone in
// the seat slot when no budget applied. Its retry is handed that hold, and the hold
// settles its row and the admission — also after a release, as a late commit that keeps
// the release's instant.
func TestCommitSettlesLegacySpendOnlyRow(t *testing.T) {
	forEachAdmissionEngine(t, runCommitSettlesLegacySpendOnlyRow)
}

func runCommitSettlesLegacySpendOnlyRow(t *testing.T, cfg store.Config) {
	seed := func(t *testing.T) (*Module, store.Store, model.TenantID, *fakeClock, AdmissionRequest, holdID) {
		t.Helper()
		m, st, tenant, _ := openFinCfg(t, cfg)
		clk := &fakeClock{t: baseTime}
		m.clock = clk
		_, limit := seedBudgetAndSeatLimit(t, st, tenant)
		req := seatRequest("model_gateway/legacy-j")
		h2 := newHoldID()
		dated := baseTime.Add(-30 * time.Second)
		seedReservation(t, st, tenant, ledgerRow(limit, "s", h2, 1, req.EstimateMicroUSD, dated.Add(-time.Second), dated.Add(reservationTTL), resvStateActive))
		seedAdmission(t, st, tenant, admissionRecordFor(req, admStateReserved, "", h2, dated))
		retry, err := m.Reserve(context.Background(), tenant, req)
		if err != nil || !retry.Allowed || !retry.Replayed || retry.Handle != h2.String() {
			t.Fatalf("a retry of the spend-only row: %+v err=%v, want a replay of its seat hold", retry, err)
		}
		return m, st, tenant, clk, req, h2
	}

	t.Run("committed", func(t *testing.T) {
		m, st, tenant, clk, req, h2 := seed(t)
		clk.advance(10 * time.Second)
		if err := m.Commit(context.Background(), tenant, h2.String(), measured); err != nil {
			t.Fatalf("commit: %v", err)
		}
		row := admissionRowOf(t, m, tenant, req.IdempotencyKey)
		if row.state != admStateCommitted || row.spendHandle != h2 || !row.handle.isZero() {
			t.Fatalf("the spend-only row is %s naming %q and %q, want committed naming its seat hold", row.state, row.handle, row.spendHandle)
		}
		assertRowsUnder(t, st, tenant, h2, 1, resvStateCommitted, measured, clk.t)
	})

	t.Run("released, then committed", func(t *testing.T) {
		m, st, tenant, clk, req, h2 := seed(t)
		clk.advance(10 * time.Second)
		released := clk.t
		if err := m.Release(context.Background(), tenant, h2.String()); err != nil {
			t.Fatalf("release: %v", err)
		}
		if row := admissionRowOf(t, m, tenant, req.IdempotencyKey); row.state != admStateReleased || row.spendHandle != h2 {
			t.Fatalf("after the release the row is %s naming %q, want released naming the seat hold", row.state, row.spendHandle)
		}
		assertRowsUnder(t, st, tenant, h2, 1, resvStateReleased, 0, released)
		clk.advance(10 * time.Second)
		if err := m.Commit(context.Background(), tenant, h2.String(), measured); err != nil {
			t.Fatalf("the late commit: %v", err)
		}
		if row := admissionRowOf(t, m, tenant, req.IdempotencyKey); row.state != admStateCommitted || row.spendHandle != h2 {
			t.Fatalf("after the late commit the row is %s naming %q, want committed", row.state, row.spendHandle)
		}
		assertRowsUnder(t, st, tenant, h2, 1, resvStateCommitted, measured, released)
	})
}

// TestLostAckOrderingsEndCommitted: whichever write of the admission lost its
// acknowledgment, or rolled back and was tried again, the caller was admitted under one
// hold, and the commit of that hold ends where an admission with no fault ends:
// committed, both components at the measured cost, nothing withheld.
func TestLostAckOrderingsEndCommitted(t *testing.T) {
	forEachAdmissionEngine(t, runLostAckOrderingsEndCommitted)
}

func runLostAckOrderingsEndCommitted(t *testing.T, cfg store.Config) {
	for _, tc := range []struct {
		name  string
		nth   int64
		mode  faultMode
		stale bool
	}{
		{"a publication that never opened, tried again", 3, faultBeforeCallback, false},
		{"a publication whose acknowledgment was lost", 3, faultAfterCommit, false},
		{"a claim whose acknowledgment was lost", 1, faultAfterCommit, false},
		{"a takeover whose acknowledgment was lost", 1, faultAfterCommit, true},
		{"a create whose acknowledgment was lost", 2, faultAfterCommit, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, st, tenant, clk := settlementFixture(t, cfg)
			req := seatRequest("model_gateway/k")
			var dead holdID
			if tc.stale {
				dead = stageClaimWithHold(t, m, tenant, req)
				clk.advance(admissionClaimTakeover + time.Second)
			}
			res, _ := reserveThroughFault(t, m, tenant, req, tc.nth, tc.mode)
			if !res.Allowed || res.Handle == "" || res.Handle == dead.String() {
				t.Fatalf("the admission: %+v", res)
			}
			clk.advance(10 * time.Second)
			if err := m.Commit(context.Background(), tenant, res.Handle, measured); err != nil {
				t.Fatalf("commit: %v", err)
			}
			assertAdmission(t, m, tenant, req.IdempotencyKey, admStateCommitted, holdID(res.Handle))
			assertRowsUnder(t, st, tenant, holdID(res.Handle), 2, resvStateCommitted, measured, clk.t)
			if tc.stale {
				assertRowsUnder(t, st, tenant, dead, 2, resvStateReleased, 0, time.Time{})
			}
			assertWithheld(t, st, tenant, clk.t, 0, 0)
		})
	}
}

// TestADelayedCommitDoesNotReplaceTheCurrentAdmission: a settlement does two things.
// Settling the money of the hold it was given is right however late it arrives. Writing
// the admission row is authority over the key, and the key may already belong to a newer
// call — then the late settlement leaves that call's admission alone, or the next retry
// would be handed a hold nobody is withholding.
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

	first := reserveOK(t, m, tenant, req)
	d := &pausedMutateData{ModuleData: m.data, nth: 1, reached: make(chan struct{}), resume: make(chan struct{})}
	m.data = d
	settled := make(chan error, 1)
	go func() { settled <- m.Commit(pausedCtx(ctx), tenant, first.Handle, oneUSD) }()
	awaitPausedMutate(t, d)
	clk.advance(admissionReplayWindow + time.Second)
	second, err := m.Reserve(ctx, tenant, req)
	close(d.resume)
	lateErr := <-settled

	if err != nil || lateErr != nil || !second.Allowed || second.Handle == first.Handle {
		t.Fatalf("a late settlement of the old hold must succeed and a new call must be admitted: new=%+v err=%v lateErr=%v",
			second, err, lateErr)
	}
	assertRowsUnder(t, st, tenant, holdID(first.Handle), 1, resvStateCommitted, oneUSD, clk.t)
	row := admissionRowOf(t, m, tenant, req.IdempotencyKey)
	replay, err := m.Reserve(ctx, tenant, req)
	if err != nil {
		t.Fatal(err)
	}
	if row.state != admStateReserved || row.handle.String() != second.Handle || replay.Handle != second.Handle {
		t.Fatalf("the late settlement replaced the current admission: current=%s stored=%s (%s) next=%+v",
			second.Handle, row.handle, row.state, replay)
	}
}

// TestARepeatedSettlementDoesNotExtendTheReplayWindow: the window is measured from the
// moment the row reached its state, so a settlement that repeats one already applied
// moves nothing — else a caller that retried its commit every four minutes would carry
// its first verdict for as long as it kept retrying, over a cap since blown.
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

	first := reserveOK(t, m, tenant, req)
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

// TestADelayedReleaseDoesNotReplaceTheCurrentAdmission is the release beside the
// commit: returning the headroom of the hold it was given is right however late it
// arrives, and writing the admission row is a claim on the key, which may already belong
// to a newer call whose hold the next retry has to be handed.
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

	first := reserveOK(t, m, tenant, req)
	d := &pausedMutateData{ModuleData: m.data, nth: 1, reached: make(chan struct{}), resume: make(chan struct{})}
	m.data = d
	released := make(chan error, 1)
	go func() { released <- m.Release(pausedCtx(ctx), tenant, first.Handle) }()
	awaitPausedMutate(t, d)
	clk.advance(admissionReplayWindow + time.Second)
	second, err := m.Reserve(ctx, tenant, req)
	close(d.resume)
	lateErr := <-released

	if err != nil || lateErr != nil || !second.Allowed || second.Handle == first.Handle {
		t.Fatalf("a late release of the old hold must succeed and a new call must be admitted: new=%+v err=%v lateErr=%v",
			second, err, lateErr)
	}
	row := admissionRowOf(t, m, tenant, req.IdempotencyKey)
	if row.state != admStateReserved || row.handle.String() != second.Handle {
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
	if got := ledgerCounts(t, st, tenant, clk.t); got.Active != 1 || got.Released != 1 {
		t.Fatalf("after a late release the ledger holds %+v, want the old hold released and only the successor's active", got)
	}
}

// TestReserveCommitReleaseLifecycle: a hold is committed, and a commit repeated with the
// same amount is idempotent; another hold is released, and a repeated release is too.
func TestReserveCommitReleaseLifecycle(t *testing.T) {
	forEachAdmissionEngine(t, runReserveCommitReleaseLifecycle)
}

func runReserveCommitReleaseLifecycle(t *testing.T, cfg store.Config) {
	m, st, tenant, _ := openFinCfg(t, cfg)
	m.clock = &fakeClock{t: baseTime}
	ctx := context.Background()
	createBudget(t, st, tenant, "global-block", budgetSpec{
		Dimension: "global", Period: "monthly", LimitMicroUSD: 5 * oneUSD, Action: "block",
	})
	res, err := m.Reserve(ctx, tenant, AdmissionRequest{
		Scope: AdmissionScopeSessionLaunch, EstimateMicroUSD: oneUSD, IdempotencyKey: "life-1",
	})
	if err != nil || !res.Allowed {
		t.Fatalf("reserve: %+v err=%v", res, err)
	}
	if err := m.Commit(ctx, tenant, res.Handle, 500_000); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if err := m.Commit(ctx, tenant, res.Handle, 500_000); err != nil {
		t.Fatalf("commit must be idempotent: %v", err)
	}
	released, err := m.Reserve(ctx, tenant, AdmissionRequest{
		Scope: AdmissionScopeScheduledJob, EstimateMicroUSD: oneUSD, IdempotencyKey: "life-2",
	})
	if err != nil || !released.Allowed {
		t.Fatalf("second reserve: %+v err=%v", released, err)
	}
	if err := m.Release(ctx, tenant, released.Handle); err != nil {
		t.Fatalf("release: %v", err)
	}
	if err := m.Release(ctx, tenant, released.Handle); err != nil {
		t.Fatalf("release must be idempotent: %v", err)
	}
}

// TestCommitEmptyHandleIsNoop: an admission that held nothing was handed no handle, and
// settling the empty handle is a no-op.
func TestCommitEmptyHandleIsNoop(t *testing.T) {
	forEachAdmissionEngine(t, runCommitEmptyHandleIsNoop)
}

func runCommitEmptyHandleIsNoop(t *testing.T, cfg store.Config) {
	m, _, tenant, _ := openFinCfg(t, cfg)
	if err := m.Commit(context.Background(), tenant, "", 1); err != nil {
		t.Fatalf("empty commit: %v", err)
	}
	if err := m.Release(context.Background(), tenant, ""); err != nil {
		t.Fatalf("empty release: %v", err)
	}
}

// TestReserveWithAnAmountStillHolds is the positive control of a hold of zero: a caller
// that carries an estimate still gets a row and a handle, and settling it returns the
// ledger to nothing withheld.
func TestReserveWithAnAmountStillHolds(t *testing.T) {
	forEachAdmissionEngine(t, runReserveWithAnAmountStillHolds)
}

func runReserveWithAnAmountStillHolds(t *testing.T, cfg store.Config) {
	m, st, tenant, _ := openFinCfg(t, cfg)
	m.clock = &fakeClock{t: baseTime}
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
	if got := ledgerCounts(t, st, tenant, baseTime); got.Active != 1 {
		t.Fatalf("Active = %d after a 1 USD hold, want 1", got.Active)
	}
	if err := m.Commit(ctx, tenant, res.Handle, oneUSD); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if got := ledgerCounts(t, st, tenant, baseTime); got.Active != 0 || got.Committed != 1 {
		t.Fatalf("after settlement: %+v", got)
	}
}

// TestSettlementRefusesCorruptRow: a hold that leads to a corrupt admission row — a slot
// that is not a hold identity beside it, a handle slot that is none while the hold sits
// in the seat slot, or a hold that two rows name — cannot be identified as a settlement.
// Commit and Release are ErrAdmissionIntegrity, which is neither a conflict nor a pending
// claim, and neither writes a ledger row or an admission row.
func TestSettlementRefusesCorruptRow(t *testing.T) {
	forEachAdmissionEngine(t, runSettlementRefusesCorruptRow)
}

func runSettlementRefusesCorruptRow(t *testing.T, cfg store.Config) {
	m, st, tenant, _ := openFinCfg(t, cfg)
	clk := &fakeClock{t: baseTime}
	m.clock = clk
	budget, limit := seedBudgetAndSeatLimit(t, st, tenant)
	seq := int64(0)
	held := func(h holdID, components ...string) {
		for _, c := range components {
			seq++
			policy := budget
			if c == "s" {
				policy = limit
			}
			seedReservation(t, st, tenant, ledgerRow(policy, c, h, seq, 2*oneUSD, baseTime, baseTime.Add(reservationTTL), resvStateActive))
		}
	}

	// A reserved row whose hold is valid and whose seat slot is no identity.
	badSeatSlot := newHoldID()
	rec := admissionRecordFor(seatRequest("model_gateway/k"), admStateReserved, badSeatSlot, "", baseTime)
	rec[colAdmSpendHandle] = "x1"
	seedAdmission(t, st, tenant, rec)
	held(badSeatSlot, "b", "s")
	// A dead claimant's pending row and another key's reserved row naming one hold.
	shared := newHoldID()
	seedAdmission(t, st, tenant, admissionRecordFor(seatRequest("model_gateway/k1"), admStatePending, shared, "", baseTime))
	seedAdmission(t, st, tenant, admissionRecordFor(seatRequest("model_gateway/k2"), admStateReserved, shared, "", baseTime))
	held(shared, "b", "s")
	// A pair whose budget slot is no identity and whose seat hold is valid.
	seatOnly := newHoldID()
	rec = admissionRecordFor(seatRequest("model_gateway/k3"), admStateReserved, "", seatOnly, baseTime)
	rec[colAdmHandle] = "x1"
	seedAdmission(t, st, tenant, rec)
	held(seatOnly, "s")
	clk.advance(admissionClaimTakeover * 2)

	admissions, ledger := rowsByID(t, st, tenant, admissionIdempotencyKind), rowsByID(t, st, tenant, budgetReservationKind)
	for _, tc := range []struct {
		name string
		h    holdID
	}{
		{"a seat slot that is not an identity", badSeatSlot},
		{"a hold two rows name", shared},
		{"a seat hold beside a budget slot that is not an identity", seatOnly},
	} {
		for _, op := range []struct {
			name string
			run  func() error
		}{
			{"commit", func() error { return m.Commit(context.Background(), tenant, tc.h.String(), measured) }},
			{"release", func() error { return m.Release(context.Background(), tenant, tc.h.String()) }},
		} {
			err := op.run()
			if !errors.Is(err, ErrAdmissionIntegrity) || !errors.Is(err, errAdmissionRowCorrupt) ||
				errors.Is(err, ErrSettlementConflict) || errors.Is(err, ErrAdmissionPending) {
				t.Errorf("%s, %s: %v; want the integrity failure alone", tc.name, op.name, err)
			}
			if err != nil && strings.Contains(err.Error(), "x1") {
				t.Errorf("%s, %s: the error carries a stored value: %v", tc.name, op.name, err)
			}
		}
	}
	assertRowsUnchanged(t, "the admission rows", admissions, rowsByID(t, st, tenant, admissionIdempotencyKind))
	assertRowsUnchanged(t, "the ledger", ledger, rowsByID(t, st, tenant, budgetReservationKind))
}

// TestLateCommitBesideCorruptRow: a corrupt row of another key changes nothing for a
// valid one: its hold is released and then committed late, keeping the release's
// instant, and the corrupt row stays exactly as stored.
func TestLateCommitBesideCorruptRow(t *testing.T) {
	forEachAdmissionEngine(t, runLateCommitBesideCorruptRow)
}

func runLateCommitBesideCorruptRow(t *testing.T, cfg store.Config) {
	m, st, tenant, clk := settlementFixture(t, cfg)
	rec := admissionRecordFor(seatRequest("model_gateway/x"), admStatePending, "", "", baseTime)
	rec[colAdmHandle] = "x1"
	corrupt := seedAdmission(t, st, tenant, rec)

	req := seatRequest("model_gateway/k")
	h := holdID(reserveOK(t, m, tenant, req).Handle)
	clk.advance(10 * time.Second)
	released := clk.t
	if err := m.Release(context.Background(), tenant, h.String()); err != nil {
		t.Fatalf("release: %v", err)
	}
	clk.advance(10 * time.Second)
	if err := m.Commit(context.Background(), tenant, h.String(), measured); err != nil {
		t.Fatalf("the late commit: %v", err)
	}
	assertAdmission(t, m, tenant, req.IdempotencyKey, admStateCommitted, h)
	assertRowsUnder(t, st, tenant, h, 2, resvStateCommitted, measured, released)
	for _, r := range extRows(t, st, tenant, admissionIdempotencyKind) {
		if r.String(model.ColID) == corrupt.String(model.ColID) &&
			(r.String(colAdmHandle) != "x1" || r.Int(model.ColVersion) != corrupt.Int(model.ColVersion)) {
			t.Fatalf("the corrupt row changed: handle %q version %d", r.String(colAdmHandle), r.Int(model.ColVersion))
		}
	}
}

// TestUndecodableOwedStillSettlesOwnHold: a row whose owed list does not decode is not
// corrupt — its slots decode — so a commit of its own hold settles the hold and the row,
// and writes the owed text back exactly as it was stored.
func TestUndecodableOwedStillSettlesOwnHold(t *testing.T) {
	forEachAdmissionEngine(t, runUndecodableOwedStillSettlesOwnHold)
}

func runUndecodableOwedStillSettlesOwnHold(t *testing.T, cfg store.Config) {
	m, st, tenant, clk := settlementFixture(t, cfg)
	req := seatRequest("model_gateway/k")
	h := holdID(reserveOK(t, m, tenant, req).Handle)
	const garbled = `["not an identity"`
	setStoredCell(t, st, tenant, req.IdempotencyKey, colAdmOwedHandles, garbled)

	clk.advance(10 * time.Second)
	if err := m.Commit(context.Background(), tenant, h.String(), measured); err != nil {
		t.Fatalf("commit: %v", err)
	}
	row := admissionRowOf(t, m, tenant, req.IdempotencyKey)
	if row.state != admStateCommitted || row.handle != h || row.owedRaw != garbled || !errors.Is(row.owedErr, errOwedUndecodable) {
		t.Fatalf("the row is %s naming %q with owed text %v, want committed with the text unchanged", row.state, row.handle, row.owedRaw)
	}
	assertRowsUnder(t, st, tenant, h, 2, resvStateCommitted, measured, clk.t)
}

// TestLegacyWaitSeesConcurrentSettlement: a caller waiting on a pair that holds its key
// back reads the key again after each pause and follows what a settlement did meanwhile.
// A commit of the pair makes the row a recent commit, which the waiting retry is answered
// with; a release lets the caller take the key over under a hold of its own.
func TestLegacyWaitSeesConcurrentSettlement(t *testing.T) {
	forEachAdmissionEngine(t, runLegacyWaitSeesConcurrentSettlement)
}

func runLegacyWaitSeesConcurrentSettlement(t *testing.T, cfg store.Config) {
	tp := baseTime
	for _, commit := range []bool{true, false} {
		name := "a commit of the pair"
		if !commit {
			name = "a release of the pair"
		}
		t.Run(name, func(t *testing.T) {
			m, st, tenant, _ := openFinCfg(t, cfg)
			clk := &fakeClock{t: tp.Add(150 * time.Second)}
			m.clock = clk
			budget, limit := seedBudgetAndSeatLimit(t, st, tenant)
			req := seatRequest("model_gateway/legacy-pair")
			h1, h2 := legacyPair(t, st, tenant, req, budget, limit, tp, tp.Add(100*time.Second), tp.Add(290*time.Second), true)

			d := &pausedReadData{ModuleData: m.data, nth: 1, reached: make(chan struct{}), resume: make(chan struct{})}
			m.data = d
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			done := make(chan pausedReserveOutcome, 1)
			go func() {
				r, e := m.Reserve(pausedCtx(ctx), tenant, req)
				done <- pausedReserveOutcome{r, e}
			}()
			awaitPausedRead(t, d)
			clk.advance(10 * time.Second)
			settledAt := clk.t
			var err error
			if commit {
				err = m.Commit(ctx, tenant, h1.String(), measured)
			} else {
				err = m.Release(ctx, tenant, h1.String())
			}
			close(d.resume)
			got := awaitPausedReserve(t, done)
			if err != nil {
				t.Fatalf("the settlement: %v", err)
			}

			if commit {
				if got.err != nil || !got.res.Allowed || !got.res.Replayed || got.res.Handle != h1.String() {
					t.Fatalf("the waiting retry was answered %+v err=%v, want a replay of the committed budget hold", got.res, got.err)
				}
				row := admissionRowOf(t, m, tenant, req.IdempotencyKey)
				if row.state != admStateCommitted || row.handle != h1 || row.spendHandle != h2 {
					t.Fatalf("the pair's row is %s naming %q and %q, want committed naming both", row.state, row.handle, row.spendHandle)
				}
				assertRowsUnder(t, st, tenant, h1, 1, resvStateCommitted, measured, settledAt)
				assertRowsUnder(t, st, tenant, h2, 1, resvStateCommitted, measured, settledAt)
				assertWithheld(t, st, tenant, clk.t, 0, 0)
				return
			}
			assertTakenOver(t, m, st, tenant, req, got.res, got.err, h1, h2)
			assertRowsUnder(t, st, tenant, h1, 1, resvStateReleased, 0, settledAt)
			assertRowsUnder(t, st, tenant, h2, 1, resvStateReleased, 0, settledAt)
			assertWithheld(t, st, tenant, clk.t, 2*oneUSD, 2*oneUSD)
		})
	}
}

// TestLateCommitAfterLegacyWait: the key a half-withholding pair held back is taken over
// once both holds lapse; the sweep then expires the pair's rows, and a commit of the
// budget hold that arrives after all that is a late commit of that hold alone, keeping
// the sweep's instant. The other hold stays expired, and the new admission stands.
func TestLateCommitAfterLegacyWait(t *testing.T) {
	forEachAdmissionEngine(t, runLateCommitAfterLegacyWait)
}

func runLateCommitAfterLegacyWait(t *testing.T, cfg store.Config) {
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
	before := admissionRowOf(t, m, tenant, req.IdempotencyKey)

	clk.advance(9 * time.Second)
	swept := clk.t
	if n, err := m.SweepExpiredReservations(context.Background(), tenant); err != nil || n != 2 {
		t.Fatalf("the sweep expired %d row(s) err=%v, want the pair's two", n, err)
	}
	clk.advance(10 * time.Second)
	if err := m.Commit(context.Background(), tenant, h1.String(), measured); err != nil {
		t.Fatalf("the late commit: %v", err)
	}
	assertRowsUnder(t, st, tenant, h1, 1, resvStateCommitted, measured, swept)
	assertRowsUnder(t, st, tenant, h2, 1, resvStateExpired, 0, swept)
	after := assertAdmission(t, m, tenant, req.IdempotencyKey, admStateReserved, holdID(res.Handle))
	if after.version != before.version {
		t.Fatalf("the late commit wrote the new admission: version %d → %d", before.version, after.version)
	}
	assertWithheld(t, st, tenant, clk.t, 2*oneUSD, 2*oneUSD)
}
