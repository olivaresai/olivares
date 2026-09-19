// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"log/slog"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/runtime"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/finops"
)

// admissionreconcile.go closes the FinOps admission lifecycle: the caller that
// holds headroom Commits or Releases it, and this loop reconciles what no caller
// ever settled. Without it the reservation TTL is the only thing that ever
// retires a hold whose caller died mid-call, and the drift the reconciliation
// read reports has nobody to act on it — the job existed, with its route and its
// CLI verb, and no in-process caller at all.
//
// It runs on the runtime's OWN periodic scheduler (the retention-sweep /
// deploy-drift precedent — never a second timer), so the goroutine is owned and
// stopped by the runtime's shutdown, and it is leader-gated like every other loop
// that writes: the sweep terminalizes rows and the drift finding is evidence, and
// neither should be forked across a pair. The gate is read before the tick AND
// before every tenant, because a pass over a large estate outlives an election.
//
// The module owns every semantic. This loop is cadence plus tenant enumeration,
// which is a System operation the module itself cannot perform.

const (
	// admissionReconcileJobName is the runtime scheduler's job name.
	admissionReconcileJobName = "finops-admission-reconcile"
	// admissionReconcileInterval is the cadence. It matches the reservation TTL
	// (modules/finops: 5 minutes) on purpose: a hold that lapses is swept within
	// one TTL of lapsing, so the window in which the ledger shows a lapsed row
	// nobody has dealt with is bounded by the same constant that created it.
	// Shorter would re-read every tenant's reservation ledger for nothing on an
	// idle estate; longer would let the console's drift line describe a state the
	// engine had already resolved.
	admissionReconcileInterval = 5 * time.Minute
)

// admissionReconciler is the periodic settlement of the FinOps reserve ledger.
type admissionReconciler struct {
	st       store.Store
	fin      *finops.Module
	interval time.Duration
	log      *slog.Logger
}

// newAdmissionReconciler builds the loop. nil when FinOps is not wired — there is
// no ledger to reconcile then, and a loop that enumerates tenants to call nothing
// is a tick that only costs.
func newAdmissionReconciler(st store.Store, fin *finops.Module, log *slog.Logger) *admissionReconciler {
	if st == nil || fin == nil {
		return nil
	}
	return &admissionReconciler{st: st, fin: fin, interval: admissionReconcileInterval, log: log}
}

// register schedules the loop on the runtime's own scheduler (before Start).
func (l *admissionReconciler) register(rt *runtime.Runtime) error {
	return rt.SchedulePeriodic(admissionReconcileJobName, l.interval, false, l.runOnce)
}

// runOnce reconciles every business tenant. A per-tenant failure is logged and the
// remaining tenants still run: reconciliation is idempotent (the sweep re-selects
// by the same expiry predicate), so a partial pass costs nothing but a tick — and
// that is also why losing leadership mid-pass can simply end it.
// Logged fields are COUNTS ONLY — never row content (docs/SECURITY-HARDENING.md).
func (l *admissionReconciler) runOnce(ctx context.Context) error {
	if !l.st.Leader().Active() {
		l.log.Debug("finops-admission-reconcile skipped: this node is a standby, not the active writer")
		return nil
	}
	tenants, err := servedBusinessTenants(ctx, l.st)
	if err != nil {
		l.log.Warn("finops-admission-reconcile: cannot enumerate orgs; skipping this tick", "err", err)
		return nil
	}
	for done, t := range tenants {
		if err := ctx.Err(); err != nil {
			return err
		}
		// LEADERSHIP IS RE-READ BEFORE EVERY TENANT, not once for the whole pass.
		// A pass over a large estate outlives an election: a node demoted after the
		// first of five hundred tenants would otherwise keep sweeping and filing the
		// drift finding for the other four hundred and ninety-nine while the new
		// leader does the same work — the forked pair this gate exists to prevent.
		// The pass simply stops and says so; the active writer starts a new one on
		// its own tick, and the sweep re-selects by the same expiry predicate, so
		// nothing is lost by stopping in the middle. This is the run-lineage repair
		// loop's per-page check, at the granularity this loop paginates in.
		if !l.st.Leader().Active() {
			l.log.Info("finops-admission-reconcile: pass stopped before exhaustion; the active writer starts a new pass",
				"stopped", "leadership", "tenants_done", done, "tenants_total", len(tenants))
			return nil
		}
		l.reconcileTenant(ctx, t)
	}
	return nil
}

// reconcileTenant runs the job for one tenant and reports what it found. A pass
// that swept nothing and found no drift says nothing: this loop runs on every
// install, and the healthy answer is the overwhelmingly common one.
func (l *admissionReconciler) reconcileTenant(ctx context.Context, tenant model.TenantID) {
	report, err := l.fin.ReconcileReservations(ctx, tenant)
	if err != nil {
		l.log.Warn("finops-admission-reconcile: tenant pass failed; continuing with the remaining tenants",
			"tenant", tenant.String(), "err", err)
		return
	}
	if report.SweptExpired == 0 && !report.Drift {
		return
	}
	// Drift means a caller took headroom and never gave it back, so it is reported
	// and never repaired here — the module files the posture finding, and this line
	// is what an operator greps before opening the console read.
	l.log.Info("finops-admission-reconcile: tenant pass completed",
		"tenant", tenant.String(),
		"swept_expired", report.SweptExpired,
		"active", report.Active,
		"active_lapsed", report.ActiveLapsed,
		"expired_unsettled", report.ExpiredUnsettled,
		"idempotency_orphans", report.IdempotencyOrphans,
		"drift", report.Drift)
}
