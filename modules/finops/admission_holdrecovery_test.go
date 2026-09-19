// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package finops

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// -----------------------------------------------------------------------------
// RECOVERY OF EVERY HOLD THE KEY EVER OWNED. One admission can own TWO monetary
// holds — a pooled budget hold and a per-seat spend hold — and they are settled in
// separate transactions. So the pair can come apart: one half settled and the other
// still withholding money, or one half expired and the other still live. Recovering
// such a row has to assess EACH handle on its own; a single all-or-nothing question
// about the pair answers "nothing to do" precisely when there is something to do.
// -----------------------------------------------------------------------------

var errTransactionUnavailable = errors.New("fixture: write transaction unavailable")

// transactionFault refuses to OPEN the write transactions whose ordinal is named, and
// lets every other one — and every read — reach the real store. That is how a pair that
// came apart is produced without pretending: the first settlement really commits and the
// second really does not, so the surviving hold is a real live reservation row.
//
// It wraps Mutate ONLY. A scope decorator is not usable here: the module's writer lock
// asserts on the scope as given and refuses to unwrap, so wrapping the scope would turn
// every fixture into a lock failure instead of the failure under test.
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

// holdState returns the lifecycle state of the one reservation row under a handle.
func holdState(t *testing.T, m *Module, tenant model.TenantID, handle string) string {
	t.Helper()
	var state string
	if err := m.data.View(context.Background(), tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(budgetReservationKind)
		if err != nil {
			return err
		}
		rows, incomplete, err := scanReservations(context.Background(), repo, []model.Filter{eq(colResvHandle, handle)})
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

// pairedRequest is an admission that takes BOTH a pooled budget hold and a per-seat
// spend hold, which is the only way a row owns two handles at once.
func pairedRequest(t *testing.T, m *Module, st store.Store, tenant model.TenantID, key string) AdmissionRequest {
	t.Helper()
	createBudget(t, st, tenant, "cap", budgetSpec{
		Dimension: "global", Period: "monthly", LimitMicroUSD: 10 * oneUSD, Action: "block",
	})
	if _, _, err := m.SpendLimitUpsert(context.Background(), tenant,
		userLimit("user:paired-seat", "1000", "monthly"), "user:admin"); err != nil {
		t.Fatal(err)
	}
	return AdmissionRequest{
		Scope: AdmissionScopeModelGateway, ActorRef: "user:paired-seat",
		EstimateMicroUSD: oneUSD, IdempotencyKey: key,
	}
}

// reservePair admits one paired request and returns its two distinct handles.
func reservePair(t *testing.T, m *Module, tenant model.TenantID, req AdmissionRequest) Reservation {
	t.Helper()
	res, err := m.Reserve(context.Background(), tenant, req)
	if err != nil || !res.Allowed || res.Handle == "" || res.SpendHandle == "" || res.Handle == res.SpendHandle {
		t.Fatalf("the fixture needs an admission holding a distinct pair: %+v %v", res, err)
	}
	return res
}

// TestRecoveringAHalfSettledPairReleasesItsSurvivingHold is the money statement of the
// defect. A settlement that commits its first transaction and cannot open its second
// leaves the row reserved with one hold already returned and one still withholding
// headroom. The retry that re-evaluates that row must release the survivor: asking
// whether the PAIR is live answers "no" — correctly, for a replay, since half of it is
// gone — and "no" was then read as "nothing left to recover", so the surviving half
// stayed active with no admission pointing at it while the retry took a second pair.
//
// The assertion is on the RESERVATION ROWS, not on the answer the caller got: a caller
// can be handed a perfectly good new pair while the ledger quietly withholds three.
func TestRecoveringAHalfSettledPairReleasesItsSurvivingHold(t *testing.T) {
	forEachAdmissionEngine(t, runRecoveringAHalfSettledPairReleasesItsSurvivingHold)
}

func runRecoveringAHalfSettledPairReleasesItsSurvivingHold(t *testing.T, cfg store.Config) {
	for _, failSecond := range []bool{false, true} {
		name := "both_settlements_commit"
		if failSecond {
			name = "second_settlement_unavailable"
		}
		t.Run(name, func(t *testing.T) {
			m, st, tenant, _ := openFinCfg(t, cfg)
			m.clock = &fakeClock{t: baseTime}
			ctx := context.Background()
			req := pairedRequest(t, m, st, tenant, "model_gateway/half-settled-pair")
			first := reservePair(t, m, tenant, req)

			// The second of the two settlement transactions is the one that cannot open;
			// the first commits for real.
			ordinals := map[int]bool{}
			if failSecond {
				ordinals[2] = true
			}
			var releaseErr error
			fault := withFault(m, ordinals, func() { releaseErr = m.Release(ctx, tenant, first.Handle) })

			if failSecond {
				if fault.fired != 1 || !errors.Is(releaseErr, errTransactionUnavailable) {
					t.Fatalf("the fault never reached the real second settlement: calls=%d err=%v", fault.calls, releaseErr)
				}
			} else if releaseErr != nil {
				t.Fatal(releaseErr)
			}

			budgetBefore := holdState(t, m, tenant, first.Handle)
			spendBefore := holdState(t, m, tenant, first.SpendHandle)
			if budgetBefore != resvStateReleased {
				t.Fatalf("the first settlement did not commit: %q", budgetBefore)
			}
			if failSecond && spendBefore != resvStateActive {
				t.Fatalf("the second settlement did not leave a live hold: %q", spendBefore)
			}
			if !failSecond && spendBefore != resvStateReleased {
				t.Fatalf("the positive settlement was incomplete: %q", spendBefore)
			}

			recovered, err := m.Reserve(ctx, tenant, req)
			if err != nil || !recovered.Allowed ||
				recovered.Handle == first.Handle || recovered.SpendHandle == first.SpendHandle {
				t.Fatalf("the retry was not re-evaluated into a fresh pair: %+v %v", recovered, err)
			}
			row, found, err := m.lookupIdempotency(ctx, tenant, req.IdempotencyKey)
			if err != nil || !found || row.handle != recovered.Handle || row.spendHandle != recovered.SpendHandle {
				t.Fatalf("the authoritative row is not bound to the recovered pair: %+v found=%v err=%v", row, found, err)
			}
			oldSpendAfter := holdState(t, m, tenant, first.SpendHandle)
			report, err := m.InspectReservations(ctx, tenant)
			if err != nil {
				t.Fatal(err)
			}
			if oldSpendAfter != resvStateReleased || report.Active != 2 {
				t.Fatalf("recovery left the predecessor spend hold %s and %d active rows; "+
					"want that hold released and only the two rows of the current admission",
					oldSpendAfter, report.Active)
			}
		})
	}
}

// extendHoldExpiry moves ONE reservation row's expiry, which is how a pair whose halves
// do not lapse together is staged: the two holds of an admission are separate
// reservations with their own expiries, and a seat hold taken a minute after the pooled
// one outlives it by a minute.
func extendHoldExpiry(t *testing.T, m *Module, tenant model.TenantID, handle string, at time.Time) {
	t.Helper()
	if err := m.data.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(budgetReservationKind)
		if err != nil {
			return err
		}
		rows, incomplete, err := scanReservations(context.Background(), repo, []model.Filter{eq(colResvHandle, handle)})
		if err != nil {
			return err
		}
		if incomplete != "" || len(rows) != 1 {
			t.Fatalf("the fixture expected one complete reservation row, rows=%d incomplete=%q", len(rows), incomplete)
		}
		rows[0][colResvExpiresAt] = model.NewTimestamp(at).String()
		_, err = repo.Update(context.Background(), rows[0])
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

// halfSettledPair leaves an admission whose pooled hold is released and whose seat hold
// is still withholding money, by failing the second of the two settlement transactions.
func halfSettledPair(t *testing.T, m *Module, tenant model.TenantID, req AdmissionRequest) Reservation {
	t.Helper()
	first := reservePair(t, m, tenant, req)
	var releaseErr error
	fault := withFault(m, map[int]bool{2: true}, func() {
		releaseErr = m.Release(context.Background(), tenant, first.Handle)
	})
	if fault.fired != 1 || !errors.Is(releaseErr, errTransactionUnavailable) {
		t.Fatalf("the fixture did not half-settle the pair: fired=%d err=%v", fault.fired, releaseErr)
	}
	if got := holdState(t, m, tenant, first.Handle); got != resvStateReleased {
		t.Fatalf("the pooled hold was not settled: %q", got)
	}
	if got := holdState(t, m, tenant, first.SpendHandle); got != resvStateActive {
		t.Fatalf("the seat hold was not left withholding money: %q", got)
	}
	return first
}

// withholdingHolds is the number of reservation rows still keeping money from other
// callers: active rows whose expiry has not passed. It is the count that matters, because
// a row past its expiry already withholds nothing — the ceiling excludes it — and turning
// one into a release would record a settlement nobody performed.
func withholdingHolds(t *testing.T, m *Module, tenant model.TenantID) int {
	t.Helper()
	report, err := m.InspectReservations(context.Background(), tenant)
	if err != nil {
		t.Fatal(err)
	}
	return report.Active - report.ActiveLapsed
}

// TestRecoveringAPartlyExpiredPairReleasesOnlyItsLiveHold is the same defect reached by
// time instead of by a failed settlement, and it is why the question has to be asked per
// handle rather than of the pair. The pooled hold has lapsed and the seat hold has not.
// Asking whether the pair is live answers "no" — and the live half was then left
// withholding money with no admission pointing at it.
//
// The lapsed half must be left exactly as it is: it withholds nothing already, and
// flipping it to released would file a settlement this call never made in place of the
// expiry that is what actually happened to it.
func TestRecoveringAPartlyExpiredPairReleasesOnlyItsLiveHold(t *testing.T) {
	forEachAdmissionEngine(t, runRecoveringAPartlyExpiredPairReleasesOnlyItsLiveHold)
}

func runRecoveringAPartlyExpiredPairReleasesOnlyItsLiveHold(t *testing.T, cfg store.Config) {
	m, st, tenant, _ := openFinCfg(t, cfg)
	clk := &fakeClock{t: baseTime}
	m.clock = clk
	ctx := context.Background()
	req := pairedRequest(t, m, st, tenant, "model_gateway/partly-expired-pair")
	first := reservePair(t, m, tenant, req)

	extendHoldExpiry(t, m, tenant, first.SpendHandle, baseTime.Add(20*time.Minute))
	clk.advance(admissionReplayWindow + time.Minute)

	if n := withholdingHolds(t, m, tenant); n != 1 {
		t.Fatalf("the fixture wanted one lapsed and one live hold, %d are withholding money", n)
	}

	recovered, err := m.Reserve(ctx, tenant, req)
	if err != nil || !recovered.Allowed ||
		recovered.Handle == first.Handle || recovered.SpendHandle == first.SpendHandle {
		t.Fatalf("the retry was not re-evaluated into a fresh pair: %+v %v", recovered, err)
	}
	if got := holdState(t, m, tenant, first.Handle); got != resvStateActive {
		t.Fatalf("the lapsed hold was rewritten as %q; its expiry is what happened to it", got)
	}
	if got := holdState(t, m, tenant, first.SpendHandle); got != resvStateReleased {
		t.Fatalf("the live half of a partly expired pair is %q, want released: it withholds money "+
			"that no admission points at", got)
	}
	if n := withholdingHolds(t, m, tenant); n != 2 {
		t.Fatalf("%d holds withhold money after recovery, want only the two of the current admission", n)
	}
}

// TestAFailedRecoveryKeepsTheHoldItCouldNotRelease is the other obligation: a release
// that did not happen must not be reported as one. Recovery takes the row over BEFORE it
// releases anything, so between those two moments the row is the only record of money the
// key owns — and if the release cannot commit, that record has to survive and be
// retryable rather than be replaced by a fresh admission.
//
// So the caller is refused rather than admitted: handing it a new pair while the old
// money is unaccounted for is exactly how a hold stops being anybody's. The next call on
// the key finds the unfinished effect named on the row and completes it.
func TestAFailedRecoveryKeepsTheHoldItCouldNotRelease(t *testing.T) {
	forEachAdmissionEngine(t, runAFailedRecoveryKeepsTheHoldItCouldNotRelease)
}

func runAFailedRecoveryKeepsTheHoldItCouldNotRelease(t *testing.T, cfg store.Config) {
	m, st, tenant, _ := openFinCfg(t, cfg)
	m.clock = &fakeClock{t: baseTime}
	ctx := context.Background()
	req := pairedRequest(t, m, st, tenant, "model_gateway/unreleasable-recovery")
	first := halfSettledPair(t, m, tenant, req)

	// Recovery: the row is taken over first, then the surviving hold is released. That
	// release is the transaction that cannot open.
	var refused Reservation
	var rerr error
	fault := withFault(m, map[int]bool{2: true}, func() {
		refused, rerr = m.Reserve(ctx, tenant, req)
	})
	if fault.fired != 1 {
		t.Fatalf("the fault never reached the recovery release: fired=%d calls=%d", fault.fired, fault.calls)
	}
	if refused.Allowed {
		t.Fatalf("a caller was admitted while the money the key owes is unaccounted for: %+v (err=%v)", refused, rerr)
	}

	row, found, err := m.lookupIdempotency(ctx, tenant, req.IdempotencyKey)
	if err != nil || !found {
		t.Fatalf("lookup: found=%v err=%v", found, err)
	}
	if row.state != admStateOwesRelease {
		t.Fatalf("the row is %q and no longer records the release it could not complete: "+
			"the hold %s is live and nothing points at it", row.state, first.SpendHandle)
	}
	if row.handle != first.Handle || row.spendHandle != first.SpendHandle {
		t.Fatalf("the retained record names %q/%q, not the pair the key owned (%s/%s)",
			row.handle, row.spendHandle, first.Handle, first.SpendHandle)
	}
	if got := holdState(t, m, tenant, first.SpendHandle); got != resvStateActive {
		t.Fatalf("the fixture expected the hold to be still live, got %q", got)
	}

	// The next call on the key completes what the failed one left owing.
	retried, err := m.Reserve(ctx, tenant, req)
	if err != nil || !retried.Allowed || retried.Handle == "" || retried.SpendHandle == "" {
		t.Fatalf("the retry did not recover the key: %+v %v", retried, err)
	}
	if got := holdState(t, m, tenant, first.SpendHandle); got != resvStateReleased {
		t.Fatalf("the retry left the owed hold %q; a retained record that nobody acts on is not recovery", got)
	}
	if n := withholdingHolds(t, m, tenant); n != 2 {
		t.Fatalf("%d holds withhold money, want only the two of the recovered admission", n)
	}
}

// TestAFailedPublicationKeepsTheHoldsItCouldNotRelease is the same obligation on the
// caller's OWN money. A caller that reserved a pair and then could not publish its
// verdict owes both of those holds back; round-6 behaviour returned them and discarded
// whether the return succeeded, so a compensation that failed left two live holds and a
// row that said nothing about them.
//
// The publication is the last write of an admission, and the two releases that compensate
// it follow; here none of the three can commit, and the record of what is still owed has
// to be what survives.
func TestAFailedPublicationKeepsTheHoldsItCouldNotRelease(t *testing.T) {
	forEachAdmissionEngine(t, runAFailedPublicationKeepsTheHoldsItCouldNotRelease)
}

func runAFailedPublicationKeepsTheHoldsItCouldNotRelease(t *testing.T, cfg store.Config) {
	m, st, tenant, _ := openFinCfg(t, cfg)
	m.clock = &fakeClock{t: baseTime}
	ctx := context.Background()
	req := pairedRequest(t, m, st, tenant, "model_gateway/unpublishable-pair")

	// A fresh paired admission writes its claim, its two holds and then its verdict; the
	// verdict and the two releases that compensate it are the transactions denied here.
	var refused Reservation
	fault := withFault(m, map[int]bool{4: true, 5: true, 6: true}, func() {
		refused, _ = m.Reserve(ctx, tenant, req)
	})
	if fault.fired != 3 {
		t.Fatalf("the fault did not reach the publication and both compensating releases: fired=%d calls=%d",
			fault.fired, fault.calls)
	}
	if refused.Allowed {
		t.Fatalf("a caller was admitted by a verdict that was never published: %+v", refused)
	}

	row, found, err := m.lookupIdempotency(ctx, tenant, req.IdempotencyKey)
	if err != nil || !found {
		t.Fatalf("lookup: found=%v err=%v", found, err)
	}
	if row.state != admStateOwesRelease || row.handle == "" || row.spendHandle == "" {
		t.Fatalf("the row is %q naming %q/%q: the two holds the caller could not hand back "+
			"are live and nothing points at them", row.state, row.handle, row.spendHandle)
	}
	owedBudget, owedSpend := row.handle, row.spendHandle
	if got := holdState(t, m, tenant, owedBudget); got != resvStateActive {
		t.Fatalf("the fixture expected the pooled hold still live, got %q", got)
	}
	if got := holdState(t, m, tenant, owedSpend); got != resvStateActive {
		t.Fatalf("the fixture expected the seat hold still live, got %q", got)
	}

	retried, err := m.Reserve(ctx, tenant, req)
	if err != nil || !retried.Allowed || retried.Handle == owedBudget || retried.SpendHandle == owedSpend {
		t.Fatalf("the retry did not recover the key into a fresh pair: %+v %v", retried, err)
	}
	if got := holdState(t, m, tenant, owedBudget); got != resvStateReleased {
		t.Fatalf("the owed pooled hold is %q after a retry, want released", got)
	}
	if got := holdState(t, m, tenant, owedSpend); got != resvStateReleased {
		t.Fatalf("the owed seat hold is %q after a retry, want released", got)
	}
	if n := withholdingHolds(t, m, tenant); n != 2 {
		t.Fatalf("%d holds withhold money, want only the two of the recovered admission", n)
	}
}
