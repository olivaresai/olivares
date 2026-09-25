// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package finops

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// -----------------------------------------------------------------------------
// LOST ACKNOWLEDGMENTS. A write whose transaction failed before its callback finished
// changed nothing and is tried again at the same claim. A write whose callback finished
// and whose transaction still failed may have committed, and the caller asks the row:
// the version and the hold it names say whether that write is there. Nothing is written
// twice and nothing is guessed.
// -----------------------------------------------------------------------------

// reserveThroughFault runs one Reserve of req whose nth write fails the way mode names,
// and returns the answer and the fault. A fault that never fired fails the test.
func reserveThroughFault(t *testing.T, m *Module, tenant model.TenantID, req AdmissionRequest, nth int64, mode faultMode) (Reservation, *faultData) {
	t.Helper()
	base := m.data
	d := &faultData{ModuleData: base, nth: nth, mode: mode}
	m.data = d
	res, err := m.Reserve(pausedCtx(context.Background()), tenant, req)
	m.data = base
	if err != nil {
		t.Fatalf("reserve through the fault: %v", err)
	}
	if !d.fired.Load() {
		t.Fatal("the injected fault never fired; the test proves nothing")
	}
	return res, d
}

// assertPublished asserts that res is a fresh admission whose row is reserved at version,
// naming res's hold, and that the hold has both components active.
func assertPublished(t *testing.T, m *Module, st store.Store, tenant model.TenantID, key string, res Reservation, version int64) {
	t.Helper()
	if !res.Allowed || res.Replayed || res.Handle == "" {
		t.Fatalf("the caller was answered %+v, want an admission under its own hold", res)
	}
	row := admissionRowOf(t, m, tenant, key)
	if row.state != admStateReserved || row.handle.String() != res.Handle || row.version != version || len(row.owed) != 0 {
		t.Fatalf("the row is %s at version %d naming %s owing %d, want reserved at %d naming %s",
			row.state, row.version, row.handle, len(row.owed), version, res.Handle)
	}
	rows := activeRowsUnder(t, st, tenant, holdID(res.Handle))
	if kinds := componentsOf(rows); len(rows) != 2 || kinds[policyKindBudget] != 1 || kinds[policyKindSpendLimit] != 1 {
		t.Fatalf("the hold has %d active row(s) %v, want one of each component", len(rows), kinds)
	}
}

// refusedWritesData fails the marked caller's writes from the from-th to the to-th
// before their transactions open.
type refusedWritesData struct {
	api.ModuleData
	from, to int64
	writes   atomic.Int64
}

func (d *refusedWritesData) Mutate(ctx context.Context, tenant model.TenantID, fn func(store.Scope) error) error {
	if marked(ctx) {
		if n := d.writes.Add(1); n >= d.from && n <= d.to {
			return errInjectedFault
		}
	}
	return d.ModuleData.Mutate(ctx, tenant, fn)
}

// TestRolledBackPublishRetriedOnce: a publication that changed nothing is tried once more
// at the same claim, and a second failure gives the claim back — its hold released, its
// row released naming nothing — and refuses deny-closed.
func TestRolledBackPublishRetriedOnce(t *testing.T) {
	forEachAdmissionEngine(t, runRolledBackPublishRetriedOnce)
}

func runRolledBackPublishRetriedOnce(t *testing.T, cfg store.Config) {
	t.Run("the retry publishes", func(t *testing.T) {
		m, st, tenant, _ := openFinCfg(t, cfg)
		m.clock = &fakeClock{t: baseTime}
		seedBudgetAndSeatLimit(t, st, tenant)
		req := seatRequest("model_gateway/publication-retried")
		res, d := reserveThroughFault(t, m, tenant, req, 3, faultBeforeCallback)
		assertPublished(t, m, st, tenant, req.IdempotencyKey, res, 3)
		if n := d.writes.Load(); n != 4 {
			t.Fatalf("the caller wrote %d times, want a claim, a create, the publication that never opened and its one retry", n)
		}
	})

	t.Run("a second failure gives the claim back", func(t *testing.T) {
		m, st, tenant, _ := openFinCfg(t, cfg)
		m.clock = &fakeClock{t: baseTime}
		seedBudgetAndSeatLimit(t, st, tenant)
		req := seatRequest("model_gateway/publication-failed-twice")
		base := m.data
		m.data = &refusedWritesData{ModuleData: base, from: 3, to: 4}
		res, err := m.Reserve(pausedCtx(context.Background()), tenant, req)
		m.data = base
		if err != nil || res.Allowed || res.Handle != "" || res.Reason != ReasonStoreUnreachable {
			t.Fatalf("a claim that could not be published: %+v err=%v; want a deny-closed refusal", res, err)
		}
		row := admissionRowOf(t, m, tenant, req.IdempotencyKey)
		if row.state != admStateReleased || !row.handle.isZero() || row.version != 3 {
			t.Fatalf("the row is %s at version %d naming %q, want it given back after exactly two publications",
				row.state, row.version, row.handle)
		}
		if got := ledgerCounts(t, st, tenant, baseTime); got != (ledgerCount{Released: 2}) {
			t.Fatalf("the ledger holds %+v, want the hold's two rows released", got)
		}
	})
}

// blindAfterFault is faultData whose reads fail for the marked caller once the fault
// has fired: a caller that lost a write's acknowledgment and then lost the store.
type blindAfterFault struct{ *faultData }

func (d blindAfterFault) View(ctx context.Context, tenant model.TenantID, fn func(store.Scope) error) error {
	if marked(ctx) && d.fired.Load() {
		return errInjectedFault
	}
	return d.faultData.View(ctx, tenant, fn)
}

// TestLostPublishAckResolvedByIdentity: a publication that committed and lost its
// acknowledgment is found by the row — reserved, one version on, naming the hold — and the
// caller is admitted under that hold; one that rolled back is tried once more. A caller
// that cannot read the row either is refused deny-closed, and the hold it published is
// neither lost nor doubled: a retry is answered with it, and it lapses with its TTL.
func TestLostPublishAckResolvedByIdentity(t *testing.T) {
	forEachAdmissionEngine(t, runLostPublishAckResolvedByIdentity)
}

func runLostPublishAckResolvedByIdentity(t *testing.T, cfg store.Config) {
	for _, tc := range []struct {
		name   string
		mode   faultMode
		writes int64
	}{
		{"the publication committed", faultAfterCommit, 3},
		{"the publication rolled back", faultAfterRollback, 4},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, st, tenant, _ := openFinCfg(t, cfg)
			m.clock = &fakeClock{t: baseTime}
			seedBudgetAndSeatLimit(t, st, tenant)
			req := seatRequest("model_gateway/publication-unacknowledged")
			res, d := reserveThroughFault(t, m, tenant, req, 3, tc.mode)
			assertPublished(t, m, st, tenant, req.IdempotencyKey, res, 3)
			if n := d.writes.Load(); n != tc.writes {
				t.Fatalf("the caller wrote %d times, want %d", n, tc.writes)
			}
		})
	}

	t.Run("the row cannot be read", func(t *testing.T) {
		m, st, tenant, _ := openFinCfg(t, cfg)
		clk := &fakeClock{t: baseTime}
		m.clock = clk
		ctx := context.Background()
		seedBudgetAndSeatLimit(t, st, tenant)
		req := seatRequest("model_gateway/publication-unconfirmed")
		base := m.data
		d := &faultData{ModuleData: base, nth: 3, mode: faultAfterCommit}
		m.data = blindAfterFault{d}
		res, err := m.Reserve(pausedCtx(ctx), tenant, req)
		m.data = base
		if !d.fired.Load() {
			t.Fatal("the injected fault never fired; the test proves nothing")
		}
		if err != nil || res.Allowed || res.Handle != "" || res.Reason != ReasonStoreUnreachable {
			t.Fatalf("a caller that could not confirm its publication: %+v err=%v; want a deny-closed refusal", res, err)
		}
		row := admissionRowOf(t, m, tenant, req.IdempotencyKey)
		if row.state != admStateReserved || row.version != 3 || len(activeRowsUnder(t, st, tenant, row.handle)) != 2 {
			t.Fatalf("the committed publication is %s at version %d, want reserved at 3 with both components", row.state, row.version)
		}

		retry, err := m.Reserve(ctx, tenant, req)
		if err != nil || !retry.Allowed || !retry.Replayed || retry.Handle != row.handle.String() {
			t.Fatalf("the retry was answered %+v err=%v, want the published hold %s", retry, err, row.handle)
		}
		if got := ledgerCounts(t, st, tenant, clk.t); got != (ledgerCount{Active: 2}) {
			t.Fatalf("the ledger holds %+v, want the one hold's two rows", got)
		}
		clk.advance(reservationTTL + time.Second)
		if n, err := m.SweepExpiredReservations(ctx, tenant); err != nil || n != 2 {
			t.Fatalf("the sweep past the TTL expired %d row(s) err=%v, want the hold's two", n, err)
		}
		if got := ledgerCounts(t, st, tenant, clk.t); got != (ledgerCount{Expired: 2}) {
			t.Fatalf("after the TTL the ledger holds %+v, want the two rows expired", got)
		}
	})
}

// TestLostClaimAckResolvedByIdentity: a claim that committed and lost its acknowledgment
// is found by the row — pending, naming the caller's hold — and the caller goes on under
// it; one that rolled back is claimed again. Either way the key holds once.
func TestLostClaimAckResolvedByIdentity(t *testing.T) {
	forEachAdmissionEngine(t, runLostClaimAckResolvedByIdentity)
}

func runLostClaimAckResolvedByIdentity(t *testing.T, cfg store.Config) {
	for _, tc := range []struct {
		name   string
		mode   faultMode
		writes int64
	}{
		{"the claim committed", faultAfterCommit, 3},
		{"the claim rolled back", faultAfterRollback, 4},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, st, tenant, _ := openFinCfg(t, cfg)
			m.clock = &fakeClock{t: baseTime}
			seedBudgetAndSeatLimit(t, st, tenant)
			req := seatRequest("model_gateway/claim-unacknowledged")
			res, d := reserveThroughFault(t, m, tenant, req, 1, tc.mode)
			assertPublished(t, m, st, tenant, req.IdempotencyKey, res, 3)
			if n := d.writes.Load(); n != tc.writes {
				t.Fatalf("the caller wrote %d times, want %d", n, tc.writes)
			}
			if got := ledgerCounts(t, st, tenant, baseTime); got != (ledgerCount{Active: 2}) {
				t.Fatalf("the ledger holds %+v, want one hold of two components", got)
			}
		})
	}
}

// stageClaimWithHold stages a claim for req and creates its hold through the real
// create, and stops before the publication: a caller that died holding money.
func stageClaimWithHold(t *testing.T, m *Module, tenant model.TenantID, req AdmissionRequest) holdID {
	t.Helper()
	ctx := context.Background()
	h := stagePendingClaim(t, m, tenant, req)
	row := admissionRowOf(t, m, tenant, req.IdempotencyKey)
	now := m.clock.Now().Time()
	budgets, _, err := m.budgetTargets(ctx, tenant, req.Dims, now)
	if err != nil {
		t.Fatalf("the staged claim's budgets: %v", err)
	}
	seats, err := m.spendLimitTargets(ctx, tenant, req.ActorRef, req.Groups, now)
	if err != nil {
		t.Fatalf("the staged claim's seat limits: %v", err)
	}
	if out, w, err := m.create(ctx, tenant, row.token(), h, budgets, seats, req.EstimateMicroUSD, now); err != nil || w != writeCommitted || !out.issued {
		t.Fatalf("create the staged claim's hold: %+v err=%v", out, err)
	}
	return h
}

// TestLostTakeoverAckResolvedByIdentity: a takeover that committed and lost its
// acknowledgment is found by the row — pending, naming the caller's hold — and the caller
// goes on under it; one that rolled back is taken over again. Either way the dead
// claimant's hold is released by the create, once, and the key holds once.
func TestLostTakeoverAckResolvedByIdentity(t *testing.T) {
	forEachAdmissionEngine(t, runLostTakeoverAckResolvedByIdentity)
}

func runLostTakeoverAckResolvedByIdentity(t *testing.T, cfg store.Config) {
	for _, tc := range []struct {
		name   string
		mode   faultMode
		writes int64
	}{
		{"the takeover committed", faultAfterCommit, 3},
		{"the takeover rolled back", faultAfterRollback, 4},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, st, tenant, _ := openFinCfg(t, cfg)
			clk := &fakeClock{t: baseTime}
			m.clock = clk
			seedBudgetAndSeatLimit(t, st, tenant)
			req := seatRequest("model_gateway/takeover-unacknowledged")
			dead := stageClaimWithHold(t, m, tenant, req)
			clk.advance(admissionClaimTakeover + time.Second)

			res, d := reserveThroughFault(t, m, tenant, req, 1, tc.mode)
			if res.Handle == dead.String() {
				t.Fatalf("the caller was answered with the dead claimant's hold %s", dead)
			}
			assertPublished(t, m, st, tenant, req.IdempotencyKey, res, 5)
			if n := d.writes.Load(); n != tc.writes {
				t.Fatalf("the caller wrote %d times, want %d", n, tc.writes)
			}
			for _, r := range ledgerRowsUnder(t, st, tenant, dead) {
				if r.String(colResvState) != resvStateReleased || r.Int(colResvActual) != 0 {
					t.Fatalf("a row of the dead claimant's hold is %s with actual %d, want released with 0",
						r.String(colResvState), r.Int(colResvActual))
				}
			}
			if got := ledgerCounts(t, st, tenant, clk.t); got != (ledgerCount{Active: 2, Released: 2}) {
				t.Fatalf("the ledger holds %+v, want the dead hold released and the new one active", got)
			}
		})
	}
}

// TestLostCreateAckResolvedByIdentity: a create that committed and lost its
// acknowledgment is found by the row — pending, one version on, naming the caller's hold —
// and is not created again; one that rolled back is created again at the same claim.
// Either way the hold has exactly its two rows.
func TestLostCreateAckResolvedByIdentity(t *testing.T) {
	forEachAdmissionEngine(t, runLostCreateAckResolvedByIdentity)
}

func runLostCreateAckResolvedByIdentity(t *testing.T, cfg store.Config) {
	for _, tc := range []struct {
		name   string
		mode   faultMode
		writes int64
	}{
		{"the create committed", faultAfterCommit, 3},
		{"the create rolled back", faultAfterRollback, 4},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, st, tenant, _ := openFinCfg(t, cfg)
			m.clock = &fakeClock{t: baseTime}
			seedBudgetAndSeatLimit(t, st, tenant)
			req := seatRequest("model_gateway/create-unacknowledged")
			res, d := reserveThroughFault(t, m, tenant, req, 2, tc.mode)
			assertPublished(t, m, st, tenant, req.IdempotencyKey, res, 3)
			if n := d.writes.Load(); n != tc.writes {
				t.Fatalf("the caller wrote %d times, want %d", n, tc.writes)
			}
			if n := len(countReservations(t, st, tenant)); n != 2 {
				t.Fatalf("the ledger has %d row(s), want the one hold's two", n)
			}
		})
	}
}
