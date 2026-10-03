// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/finops"
)

// admissionreconcile.go is the scheduling owner of FinOps admission recovery. A caller
// that stops between its claim and its publication, or whose give-back fails, leaves a
// claim naming money that no other caller settles unless it retries the same key, and a
// hold whose caller disappears after publication withholds until its TTL. The module
// decides what to do about each (RecoverAdmissions, ReconcileReservations); this loop
// runs those passes for every served tenant, whether or not a request ever reaches it, on
// the runtime's own periodic scheduler — the retention sweep's precedent, never a second
// timer — so the runtime owns and stops its goroutines.
//
// Two jobs share the scheduler:
//   - the recover job runs a recovery pass every minute. A claim is stale thirty seconds
//     after it was taken, and its hold lapses five minutes after it was created, so the
//     hold of a caller that stopped is released before it lapses whenever two
//     consecutive passes take less than three and a half minutes together;
//   - the reconcile job runs every five minutes: a recovery pass, then the sweep of
//     lapsed holds, the inspection and the drift finding.
//
// Only the active writer runs them. Leadership is read before each tick and again before
// every tenant, because a pass over a large estate outlives an election. A tenant whose
// service is withdrawn is not visited: its stale claims wait until service resumes, and
// its holds lapse by their TTL. The module owns every semantic; this loop is cadence,
// tenants and leadership. Its lines carry counts only, never row content.

const (
	// admissionRecoverJobName is the recover job's scheduler name.
	admissionRecoverJobName = "finops-admission-recover"
	// defaultAdmissionRecoverInterval is the recover job's cadence. With the thirty
	// seconds after which a claim is stale and the five minutes a hold withholds, it lets
	// recovery release a stopped caller's hold before that hold lapses, when two
	// consecutive passes take under three and a half minutes together.
	defaultAdmissionRecoverInterval = time.Minute
	// admissionReconcileJobName is the reconcile job's scheduler name.
	admissionReconcileJobName = "finops-admission-reconcile"
	// admissionReconcileInterval is the reconcile job's cadence. It matches the
	// reservation TTL on purpose: a hold that lapses is swept within one TTL of lapsing,
	// and a shorter cadence would re-read every tenant's ledger for nothing.
	admissionReconcileInterval = 5 * time.Minute
	// admissionLegacyWriterStopEnv states the instant every writer of the earlier
	// admission build stopped, as an RFC 3339 instant in UTC.
	admissionLegacyWriterStopEnv = "OLIVARES_FINOPS_ADMISSION_LEGACY_WRITERS_STOPPED_AT"
)

// admissionRecoverInterval is the cadence boot registers for the recover job. Production
// never assigns it; a boot-level test shortens it to observe the scheduled job without
// waiting a minute.
var admissionRecoverInterval = defaultAdmissionRecoverInterval

// admissionLegacyWriterStop reads, once, the operator's statement of when every writer of
// the earlier admission build stopped. The composition root hands the result to the module
// at construction, so both jobs and the reconcile route read the same statement for the
// life of the process. Text that is not an RFC 3339 instant in UTC is reported here, once,
// and yields a stop under which no claim that build staged is retired; the text itself is
// not logged.
func admissionLegacyWriterStop(getenv func(string) string, log *slog.Logger) finops.LegacyWriterStop {
	stop, err := finops.ParseLegacyWriterStop(getenv(admissionLegacyWriterStopEnv))
	if err != nil {
		log.Error("finops-admission: "+admissionLegacyWriterStopEnv+" is not an RFC 3339 instant in UTC; "+
			"no claim the earlier admission build staged is retired until it is corrected", "err", err)
	}
	return stop
}

// admissionJobScheduler is the one runtime method the loop needs: *runtime.Runtime.
type admissionJobScheduler interface {
	SchedulePeriodic(name string, interval time.Duration, runImmediately bool, job func(context.Context) error) error
}

// admissionReconciler runs the FinOps admission recovery jobs for every served tenant.
type admissionReconciler struct {
	st           store.Store
	fin          *finops.Module
	recoverEvery time.Duration
	log          *slog.Logger
}

// newAdmissionReconciler builds the loop over the composed store and the FinOps module. It
// is nil when FinOps is not composed: there is then no ledger to recover, and a loop that
// enumerates tenants to call nothing is a tick that only costs.
func newAdmissionReconciler(st store.Store, fin *finops.Module, log *slog.Logger) *admissionReconciler {
	if st == nil || fin == nil {
		return nil
	}
	return &admissionReconciler{st: st, fin: fin, recoverEvery: admissionRecoverInterval, log: log}
}

// register schedules both jobs on the runtime's own scheduler. It must run before Start,
// since the scheduler refuses later registrations. Neither job runs at Start: after a
// restart the first recovery pass comes one interval later.
func (l *admissionReconciler) register(s admissionJobScheduler) error {
	if err := s.SchedulePeriodic(admissionRecoverJobName, l.recoverEvery, false, l.recoverOnce); err != nil {
		return err
	}
	return s.SchedulePeriodic(admissionReconcileJobName, admissionReconcileInterval, false, l.reconcileOnce)
}

// admissionPass runs one job's pass for one tenant. It reports whether the pass was quiet,
// with the counts its line carries when it was not.
type admissionPass func(ctx context.Context, tenant model.TenantID) (quiet bool, counts []any, err error)

// recoverOnce is one tick of the recover job.
func (l *admissionReconciler) recoverOnce(ctx context.Context) error {
	return l.runOnce(ctx, admissionRecoverJobName, func(ctx context.Context, tenant model.TenantID) (bool, []any, error) {
		report, err := l.fin.RecoverAdmissions(ctx, tenant)
		return report.Quiet(), recoveryCounts(report), err
	})
}

// reconcileOnce is one tick of the reconcile job.
func (l *admissionReconciler) reconcileOnce(ctx context.Context) error {
	return l.runOnce(ctx, admissionReconcileJobName, func(ctx context.Context, tenant model.TenantID) (bool, []any, error) {
		report, err := l.fin.ReconcileReservations(ctx, tenant)
		return report.Quiet(), reconciliationCounts(report), err
	})
}

// runOnce runs pass for every served tenant. A tenant whose pass fails is logged with its
// error and the remaining tenants still run; once every tenant has run, the tick returns
// an error that says how many passes failed, and the scheduler logs it. Every pass is safe
// to repeat, which is also why a node that loses leadership may simply stop: the tick
// returns nil and the active writer starts its own. A quiet pass logs nothing: the healthy
// answer is the common one, on every install.
func (l *admissionReconciler) runOnce(ctx context.Context, job string, pass admissionPass) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !l.st.Leader().Active() {
		l.log.Debug(job + " skipped: this node is a standby, not the active writer")
		return nil
	}
	tenants, err := servedWorkTenants(ctx, l.st)
	if err != nil {
		// An enumeration cut short by the engine lifecycle is shutdown, not a fault.
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		l.log.Warn(job+": cannot enumerate orgs; skipping this tick", "err", err)
		return nil
	}
	failed := 0
	for done, tenant := range tenants {
		if err := ctx.Err(); err != nil {
			return err
		}
		// Read again before every tenant: a node demoted in the middle of a pass stops
		// here instead of writing beside the new active writer.
		if !l.st.Leader().Active() {
			l.log.Info(job+": pass stopped before exhaustion; the active writer starts a new pass",
				"stopped", "leadership", "tenants_done", done, "tenants_total", len(tenants))
			return nil
		}
		quiet, counts, err := pass(ctx, tenant)
		switch {
		case err != nil && ctx.Err() != nil:
			return ctx.Err()
		case err != nil:
			failed++
			l.log.Warn(job+": tenant pass failed; continuing with the remaining tenants",
				"tenant", tenant.String(), "err", err)
		case !quiet:
			l.log.Info(job+": tenant pass completed", append([]any{"tenant", tenant.String()}, counts...)...)
		}
	}
	if failed > 0 {
		return fmt.Errorf("%s: %d of %d tenant passes failed", job, failed, len(tenants))
	}
	return nil
}

// recoveryCounts is the line of a recovery pass: what it did, what it left outstanding,
// and the state of the operator's stop of the earlier writers.
func recoveryCounts(r finops.AdmissionRecovery) []any {
	return []any{
		"pending_retired", r.PendingRetired,
		"owed_released", r.OwedReleased,
		"owed_cleared", r.OwedCleared,
		"owed_remaining", r.OwedRemaining,
		"legacy_pending", r.LegacyPending,
		"legacy_retired", r.LegacyRetired,
		"legacy_owes_release", r.LegacyOwesRelease,
		"legacy_stop", r.LegacyStop,
		"unresolved", r.Unresolved,
		"undecodable", r.Undecodable,
		"undecodable_cleared", r.UndecodableCleared,
		"frontier_blocked", r.FrontierBlocked,
		"corrupt", r.Corrupt,
		"outstanding", r.Outstanding(),
	}
}

// reconciliationCounts is the line of a reconcile pass: the ledger's counts and drift,
// then those of the recovery pass it ran first.
func reconciliationCounts(r finops.AdmissionReconciliation) []any {
	return append([]any{
		"swept_expired", r.SweptExpired,
		"active", r.Active,
		"active_lapsed", r.ActiveLapsed,
		"expired_unsettled", r.ExpiredUnsettled,
		"idempotency_orphans", r.IdempotencyOrphans,
		"drift", r.Drift,
	}, recoveryCounts(r.AdmissionRecovery)...)
}
