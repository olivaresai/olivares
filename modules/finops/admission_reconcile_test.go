// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package finops

import (
	"context"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// -----------------------------------------------------------------------------
// RECONCILIATION. The job recovers, sweeps expired holds, inspects what is left and files
// a finding when the ledger drifted from what its callers settled; the read route reports
// the same and writes nothing. A report is quiet only when nothing was done and nothing is
// left outstanding: live holds and settled history are not work.
// -----------------------------------------------------------------------------

var _ interface {
	Reserve(context.Context, model.TenantID, AdmissionRequest) (Reservation, error)
	Commit(context.Context, model.TenantID, string, int64) error
	Release(context.Context, model.TenantID, string) error
	RecoverAdmissions(context.Context, model.TenantID) (AdmissionRecovery, error)
	ReconcileReservations(context.Context, model.TenantID) (AdmissionReconciliation, error)
	InspectReservations(context.Context, model.TenantID) (AdmissionReconciliation, error)
} = (*Module)(nil)

// TestReportQuietOnlyWhenCountersZero: a report is quiet exactly when every counter of
// work done and every outstanding counter is zero. Outstanding sums what is left: owed
// holds, legacy claims and owes_release rows, unresolved writes, undecodable lists,
// blocked holds and corrupt rows. Expired, committed, released and active rows are
// history or live holds, and the stop's state is not work either. Over a live published
// hold both passes are quiet; a legacy claim left under no stop keeps them from it. An
// owed list a published row still carries is outstanding only while a row under the
// hold it names is active: once the sweep has expired that row, it names no money.
func TestReportQuietOnlyWhenCountersZero(t *testing.T) {
	type counter struct {
		name string
		set  func(*AdmissionRecovery)
	}
	outstanding := []counter{
		{"owed_remaining", func(r *AdmissionRecovery) { r.OwedRemaining = 1 }},
		{"legacy_pending", func(r *AdmissionRecovery) { r.LegacyPending = 1 }},
		{"legacy_owes_release", func(r *AdmissionRecovery) { r.LegacyOwesRelease = 1 }},
		{"unresolved", func(r *AdmissionRecovery) { r.Unresolved = 1 }},
		{"undecodable", func(r *AdmissionRecovery) { r.Undecodable = 1 }},
		{"frontier_blocked", func(r *AdmissionRecovery) { r.FrontierBlocked = 1 }},
		{"corrupt", func(r *AdmissionRecovery) { r.Corrupt = 1 }},
	}
	done := []counter{
		{"pending_retired", func(r *AdmissionRecovery) { r.PendingRetired = 1 }},
		{"owed_released", func(r *AdmissionRecovery) { r.OwedReleased = 1 }},
		{"owed_cleared", func(r *AdmissionRecovery) { r.OwedCleared = 1 }},
		{"legacy_retired", func(r *AdmissionRecovery) { r.LegacyRetired = 1 }},
		{"undecodable_cleared", func(r *AdmissionRecovery) { r.UndecodableCleared = 1 }},
	}

	t.Run("the counters", func(t *testing.T) {
		if !(AdmissionRecovery{}).Quiet() || !(AdmissionReconciliation{}).Quiet() {
			t.Fatal("an empty report is not quiet")
		}
		for _, c := range outstanding {
			var r AdmissionRecovery
			c.set(&r)
			if r.Outstanding() != 1 || r.Quiet() || (AdmissionReconciliation{AdmissionRecovery: r}).Quiet() {
				t.Errorf("%s: outstanding %d, quiet %v; want it outstanding and not quiet", c.name, r.Outstanding(), r.Quiet())
			}
		}
		for _, c := range done {
			var r AdmissionRecovery
			c.set(&r)
			if r.Outstanding() != 0 || r.Quiet() || (AdmissionReconciliation{AdmissionRecovery: r}).Quiet() {
				t.Errorf("%s: outstanding %d, quiet %v; want work done, nothing outstanding, not quiet", c.name, r.Outstanding(), r.Quiet())
			}
		}
		all := AdmissionRecovery{OwedRemaining: 1, LegacyPending: 2, LegacyOwesRelease: 4, Unresolved: 8, Undecodable: 16, FrontierBlocked: 32, Corrupt: 64}
		if got := all.Outstanding(); got != 127 {
			t.Errorf("outstanding sums to %d, want 127: each outstanding counter once", got)
		}
		for name, r := range map[string]AdmissionReconciliation{
			"swept_expired":       {SweptExpired: 1},
			"active_lapsed":       {ActiveLapsed: 1},
			"idempotency_orphans": {IdempotencyOrphans: 1},
		} {
			if r.Quiet() {
				t.Errorf("%s: a reconciliation that swept or found drift reported quiet", name)
			}
		}
		for name, r := range map[string]AdmissionReconciliation{
			"expired_unsettled": {ExpiredUnsettled: 1},
			"committed":         {Committed: 1},
			"released":          {Released: 1},
			"active":            {Active: 1},
			"legacy_stop":       {AdmissionRecovery: AdmissionRecovery{LegacyStop: string(legacyStopWaiting)}},
		} {
			if !r.Quiet() {
				t.Errorf("%s: history, a live hold or the stop's state made a reconciliation not quiet", name)
			}
		}
	})

	t.Run("the passes", func(t *testing.T) {
		forEachAdmissionEngine(t, func(t *testing.T, cfg store.Config) {
			m, st, tenant, clk := settlementFixture(t, cfg)
			reserveOK(t, m, tenant, seatRequest("model_gateway/live"))
			clk.advance(admissionClaimTakeover + 31*time.Second)
			if rep := recoverOK(t, m, tenant); !rep.Quiet() {
				t.Fatalf("recovery over a live hold reported %+v, not quiet", rep)
			}
			if job := reconcileOK(t, m, tenant); !job.Quiet() || job.Active != 2 {
				t.Fatalf("reconciliation over a live hold reported %+v, want quiet with its two active rows", job)
			}

			seedAdmission(t, st, tenant, admissionRecord("model_gateway/legacy-claim", admStatePending, "", "", baseTime.Add(-600*time.Second)))
			rep := recoverOK(t, m, tenant)
			assertRecovery(t, "recovery beside a legacy claim", rep, noStop(AdmissionRecovery{LegacyPending: 1}))
			if rep.Quiet() {
				t.Fatal("a legacy claim left under no stop reported quiet")
			}
			if job := reconcileOK(t, m, tenant); job.Quiet() || job.LegacyPending != 1 {
				t.Fatalf("reconciliation beside a legacy claim reported %+v, want it outstanding", job)
			}
		})
	})

	t.Run("an owed list on a published row", func(t *testing.T) {
		forEachAdmissionEngine(t, func(t *testing.T, cfg store.Config) {
			m, st, tenant, clk := settlementFixture(t, cfg)
			owed := newHoldID()
			heldBy(t, st, tenant, owed, "b", resvStateActive)
			cell, err := owedHolds{owed}.encode()
			if err != nil {
				t.Fatal(err)
			}
			rec := admissionRecord("model_gateway/published-owing", admStateCommitted, newHoldID(), "", baseTime)
			rec[colAdmOwedHandles] = cell
			seedAdmission(t, st, tenant, rec)

			if job := reconcileOK(t, m, tenant); job.OwedRemaining != 1 || job.Quiet() {
				t.Fatalf("with the owed hold withholding the reconciliation reported %+v, want it outstanding", job)
			}
			clk.advance(reservationTTL + time.Second)
			if read, err := m.InspectReservations(context.Background(), tenant); err != nil || read.OwedRemaining != 1 {
				t.Fatalf("with the owed hold lapsed and not yet swept the read reported %+v err=%v, want it outstanding", read, err)
			}
			if job := reconcileOK(t, m, tenant); job.SweptExpired != 1 || job.OwedRemaining != 0 {
				t.Fatalf("after the sweep expired the owed hold the reconciliation reported %+v, want the list no longer outstanding", job)
			}
			if job := reconcileOK(t, m, tenant); !job.Quiet() {
				t.Fatalf("the next reconciliation reported %+v, want quiet", job)
			}
		})
	})
}

// TestPassesIgnoreLivePublishedHold: a published hold that still withholds is its
// caller's. A recovery pass and a reconciliation before it lapses write nothing — not the
// admission, not a ledger row, not a finding — and the caller's commit then settles it
// exactly as if no pass had run.
func TestPassesIgnoreLivePublishedHold(t *testing.T) {
	forEachAdmissionEngine(t, runPassesIgnoreLivePublishedHold)
}

func runPassesIgnoreLivePublishedHold(t *testing.T, cfg store.Config) {
	m, st, tenant, host := openFinCfg(t, cfg)
	clk := &fakeClock{t: baseTime}
	m.clock = clk
	seedBudgetAndSeatLimit(t, st, tenant)
	req := seatRequest("model_gateway/live")
	h := holdID(reserveOK(t, m, tenant, req).Handle)
	admissions, ledger := rowsByID(t, st, tenant, admissionIdempotencyKind), rowsByID(t, st, tenant, budgetReservationKind)

	clk.advance(admissionClaimTakeover + 31*time.Second)
	assertRecovery(t, "the recovery pass", recoverOK(t, m, tenant), noStop(AdmissionRecovery{}))
	clk.t = baseTime.Add(299 * time.Second)
	job := reconcileOK(t, m, tenant)
	if !job.Quiet() || job.Drift || job.FindingRef != "" || job.SweptExpired != 0 || job.Active != 2 {
		t.Fatalf("the reconciliation reported %+v, want quiet over the two active rows", job)
	}
	assertRowsUnchanged(t, "the admission rows", admissions, rowsByID(t, st, tenant, admissionIdempotencyKind))
	assertRowsUnchanged(t, "the ledger", ledger, rowsByID(t, st, tenant, budgetReservationKind))
	if n := len(host.findings()); n != 0 {
		t.Fatalf("the passes filed %d finding(s) over a live hold", n)
	}

	if err := m.Commit(context.Background(), tenant, h.String(), measured); err != nil {
		t.Fatalf("commit: %v", err)
	}
	assertAdmission(t, m, tenant, req.IdempotencyKey, admStateCommitted, h)
	assertRowsUnder(t, st, tenant, h, 2, resvStateCommitted, measured, clk.t)
	assertWithheld(t, st, tenant, clk.t, 0, 0)
}

// TestLateCommitLowersExpiredUnsettled: a hold that lapsed is swept as expired and the
// reconciliation reports it, with a finding. A commit that arrives after the sweep records
// the measured cost and keeps the instant the sweep ended the withholding; the next
// reconciliation reports no expired row. The finding already filed stays filed, and no new
// one is filed.
func TestLateCommitLowersExpiredUnsettled(t *testing.T) {
	forEachAdmissionEngine(t, runLateCommitLowersExpiredUnsettled)
}

func runLateCommitLowersExpiredUnsettled(t *testing.T, cfg store.Config) {
	m, st, tenant, host := openFinCfg(t, cfg)
	clk := &fakeClock{t: baseTime}
	m.clock = clk
	seedBudgetAndSeatLimit(t, st, tenant)
	req := seatRequest("model_gateway/lapsed-then-committed")
	h := holdID(reserveOK(t, m, tenant, req).Handle)

	clk.advance(reservationTTL + time.Second)
	swept := clk.t
	job := reconcileOK(t, m, tenant)
	if job.SweptExpired != 2 || job.ExpiredUnsettled != 2 || !job.Drift || job.FindingRef == "" {
		t.Fatalf("the reconciliation reported %+v, want both rows swept, reported expired, and a finding", job)
	}
	filed := len(host.findings())

	clk.advance(time.Minute)
	if err := m.Commit(context.Background(), tenant, h.String(), measured); err != nil {
		t.Fatalf("late commit: %v", err)
	}
	assertAdmission(t, m, tenant, req.IdempotencyKey, admStateCommitted, h)
	assertRowsUnder(t, st, tenant, h, 2, resvStateCommitted, measured, swept)

	again := reconcileOK(t, m, tenant)
	if again.ExpiredUnsettled != 0 || again.Committed != 2 || again.Drift || !again.Quiet() {
		t.Fatalf("the next reconciliation reported %+v, want no expired row and both committed", again)
	}
	if n := len(host.findings()); n != filed {
		t.Fatalf("findings went from %d to %d; the filed one stays and no new one is filed", filed, n)
	}
}

// TestReconcileReservationsReportsExpiredUnsettled: a hold left unsettled past its TTL is
// swept and reported as drift, with a posture finding.
func TestReconcileReservationsReportsExpiredUnsettled(t *testing.T) {
	forEachAdmissionEngine(t, runReconcileReservationsReportsExpiredUnsettled)
}

func runReconcileReservationsReportsExpiredUnsettled(t *testing.T, cfg store.Config) {
	m, st, tenant, host := openFinCfg(t, cfg)
	clk := &fakeClock{t: baseTime}
	m.clock = clk
	createBudget(t, st, tenant, "global-block", budgetSpec{
		Dimension: "global", Period: "monthly", LimitMicroUSD: 5 * oneUSD, Action: "block",
	})
	res, err := m.Reserve(context.Background(), tenant, AdmissionRequest{
		Scope: AdmissionScopeModelGateway, EstimateMicroUSD: oneUSD, IdempotencyKey: "drift-1",
	})
	if err != nil || !res.Allowed {
		t.Fatalf("reserve: %+v err=%v", res, err)
	}
	clk.advance(reservationTTL + 1)
	report, err := m.ReconcileReservations(context.Background(), tenant)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if !report.Drift || report.ExpiredUnsettled < 1 {
		t.Fatalf("expired unsettle must be drift: %+v", report)
	}
	found := false
	for _, f := range host.findings() {
		if f.Kind == findingKindReservationDrift {
			found = true
		}
	}
	if !found {
		t.Fatal("drift must emit a posture finding, not stay silent")
	}
}

// TestInspectReservationsReadsWithoutWriting pins the difference between the two
// reconciliation routes. The read needs only budget READ, so it must not recover, sweep
// or file a finding: a read permission that moves the ledger is a write wearing a read's
// name. The same ledger state still reports drift both ways — a lapsed hold the job has
// not swept is ActiveLapsed on the read and ExpiredUnsettled after the job. A stale claim
// is left as it is by the read, which reports nothing retired, and retired by the job.
func TestInspectReservationsReadsWithoutWriting(t *testing.T) {
	forEachAdmissionEngine(t, runInspectReservationsReadsWithoutWriting)
}

func runInspectReservationsReadsWithoutWriting(t *testing.T, cfg store.Config) {
	m, st, tenant, host := openFinCfg(t, cfg)
	clk := &fakeClock{t: baseTime}
	m.clock = clk
	createBudget(t, st, tenant, "global-block", budgetSpec{
		Dimension: "global", Period: "monthly", LimitMicroUSD: 5 * oneUSD, Action: "block",
	})
	res, err := m.Reserve(context.Background(), tenant, AdmissionRequest{
		Scope: AdmissionScopeModelGateway, EstimateMicroUSD: oneUSD, IdempotencyKey: "read-only-1",
	})
	if err != nil || !res.Allowed {
		t.Fatalf("reserve: %+v err=%v", res, err)
	}
	clk.advance(reservationTTL + 1)

	before := len(host.findings())
	report, err := m.InspectReservations(context.Background(), tenant)
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	if !report.Drift || report.ActiveLapsed < 1 {
		t.Fatalf("a lapsed hold must read as drift: %+v", report)
	}
	if report.SweptExpired != 0 {
		t.Fatalf("the read swept %d hold(s): it must not sweep", report.SweptExpired)
	}
	if report.FindingRef != "" {
		t.Fatalf("the read filed %q: it must emit no finding", report.FindingRef)
	}
	if got := len(host.findings()); got != before {
		t.Fatalf("findings went from %d to %d across a READ", before, got)
	}

	// The second read sees the same ledger: nothing the first one did changed it.
	again, err := m.InspectReservations(context.Background(), tenant)
	if err != nil {
		t.Fatalf("second inspect: %v", err)
	}
	if again.ActiveLapsed != report.ActiveLapsed || again.Active != report.Active {
		t.Fatalf("a read changed the ledger: %+v then %+v", report, again)
	}

	// And the JOB, over that same state, does sweep and does file the finding.
	job, err := m.ReconcileReservations(context.Background(), tenant)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if job.SweptExpired < 1 {
		t.Fatalf("the job must sweep the lapsed hold: %+v", job)
	}
	if !job.Drift || job.FindingRef == "" {
		t.Fatalf("the job must report drift with its finding: %+v", job)
	}
	if got := len(host.findings()); got <= before {
		t.Fatalf("the job filed no finding: %d then %d", before, got)
	}

	t.Run("a stale claim is left to the job", func(t *testing.T) {
		m, st, tenant, clk := settlementFixture(t, cfg)
		req := seatRequest("model_gateway/stale-under-a-read")
		claim := stagedAfterCreate(t, m, tenant, req)
		clk.advance(admissionClaimTakeover + time.Second)
		admissions, ledger := rowsByID(t, st, tenant, admissionIdempotencyKind), rowsByID(t, st, tenant, budgetReservationKind)

		read, err := m.InspectReservations(context.Background(), tenant)
		if err != nil || read.PendingRetired != 0 || read.OwedReleased != 0 || read.FindingRef != "" {
			t.Fatalf("the read reported %+v err=%v; want nothing retired, released or filed", read, err)
		}
		assertRowsUnchanged(t, "the admission rows", admissions, rowsByID(t, st, tenant, admissionIdempotencyKind))
		assertRowsUnchanged(t, "the ledger", ledger, rowsByID(t, st, tenant, budgetReservationKind))

		job := reconcileOK(t, m, tenant)
		if job.PendingRetired != 1 || job.OwedReleased != 2 {
			t.Fatalf("the job reported %+v; want the stale claim retired and its two rows released", job)
		}
		assertRetired(t, m, tenant, req.IdempotencyKey)
		assertRowsUnder(t, st, tenant, claim.handle, 2, resvStateReleased, 0, clk.t)
	})
}

// TestReserveNoEnforcingBudgetAdmits: with no enforcing budget a request is admitted.
func TestReserveNoEnforcingBudgetAdmits(t *testing.T) {
	forEachAdmissionEngine(t, runReserveNoEnforcingBudgetAdmits)
}

func runReserveNoEnforcingBudgetAdmits(t *testing.T, cfg store.Config) {
	m, _, tenant, _ := openFinCfg(t, cfg)
	res, err := m.Reserve(context.Background(), tenant, AdmissionRequest{
		Scope: AdmissionScopeScheduledJob, EstimateMicroUSD: oneUSD, IdempotencyKey: "no-budget",
	})
	if err != nil || !res.Allowed {
		t.Fatalf("no enforcing budget must admit: %+v err=%v", res, err)
	}
}
