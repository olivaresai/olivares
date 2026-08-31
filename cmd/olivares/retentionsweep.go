// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/runtime"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/compliance"
)

// retentionsweep.go is the engine's retention-sweep loop: the
// "repeatable" pillar of defensible deletion. It runs on the runtime's EXISTING
// periodic scheduler (the deploy-drift precedent — never a parallel timer) and,
// per tick, calls the compliance module's exported tenant-scoped RunRetention
// for every business org. The module owns ALL semantics — only enabled,
// gate-approved purge schedules execute, every batch re-checks the active
// holds, and each class with activity seals an append-only retention_run
// certificate + self-audit — so this loop is pure cadence + tenant enumeration
// (a System operation the modules themselves cannot perform).
//
// HA: only the ACTIVE writer sweeps — a sweep destroys rows and writes
// certificates, so a standby gates out per tick exactly like the checkpointer
// (a promoted standby starts sweeping on its next tick; no forked destruction).
//
// Postgres (R2, inherited from the checkpointer): without an --admin-dsn
// BYPASSRLS pool, ListOrgs on the application role may return empty — the loop
// then sweeps nothing, and boot already warned loudly about the gap.

const (
	// retentionSweepJobName is the runtime scheduler's job name (contract §6).
	retentionSweepJobName = "retention-sweep"
	// retentionSweepIntervalEnv configures the sweep cadence: a Go duration
	// (default 24h); "0" disables the loop with a loud warning.
	retentionSweepIntervalEnv     = "OLIVARES_RETENTION_SWEEP_INTERVAL"
	defaultRetentionSweepInterval = 24 * time.Hour
)

// retentionSweepLoop drives the periodic tenant-scoped retention sweep.
type retentionSweepLoop struct {
	st       store.Store
	comp     *compliance.Module
	interval time.Duration
	log      *slog.Logger
}

// newRetentionSweepLoop builds the loop from the environment. nil when the
// operator explicitly disabled it (interval 0) — appropriate only when an
// external scheduler drives POST /retention/sweep per tenant. The loop is
// otherwise ALWAYS on: with no enabled purge schedules a pass is a cheap
// no-op (the motor is inert until a tenant creates policies, §2).
func newRetentionSweepLoop(getenv func(string) string, st store.Store, comp *compliance.Module, log *slog.Logger) *retentionSweepLoop {
	interval, ok := retentionSweepInterval(getenv(retentionSweepIntervalEnv), log)
	if !ok {
		return nil
	}
	return &retentionSweepLoop{st: st, comp: comp, interval: interval, log: log}
}

// retentionSweepInterval parses the cadence env. ok=false ONLY on the explicit
// zero ("0", "0s", …): disabling destruction-side automation is a legitimate
// operator choice, warned loudly (the --checkpoint-interval=0 posture). An
// unparseable or negative value keeps the default rather than silently
// changing the schedule's behavior on a typo.
func retentionSweepInterval(raw string, log *slog.Logger) (time.Duration, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return defaultRetentionSweepInterval, true
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d < 0 {
		log.Warn("retention-sweep: "+retentionSweepIntervalEnv+" is not a valid non-negative duration; using the default", "value", raw, "default", defaultRetentionSweepInterval.String())
		return defaultRetentionSweepInterval, true
	}
	if d == 0 {
		log.Warn("retention-sweep: loop DISABLED (" + retentionSweepIntervalEnv + "=0): approved purge schedules execute only via POST /v1/m/compliance/retention/sweep — run it out of band or retention is documentation, not disposition")
		return 0, false
	}
	return d, true
}

// register schedules the sweep on the runtime's own scheduler (before Start).
func (l *retentionSweepLoop) register(rt *runtime.Runtime) error {
	return rt.SchedulePeriodic(retentionSweepJobName, l.interval, false, l.runOnce)
}

// runOnce sweeps every business tenant. A per-tenant failure is logged and the
// remaining tenants still sweep (the module's own batching keeps a partial pass
// idempotent: the next run re-counts by the same age predicate). Logged fields
// are COUNTS ONLY — never row content (docs/SECURITY-HARDENING.md).
func (l *retentionSweepLoop) runOnce(ctx context.Context) error {
	if !l.st.Leader().Active() {
		l.log.Debug("retention-sweep skipped: this node is a standby, not the active writer")
		return nil
	}
	tenants, err := l.businessTenants(ctx)
	if err != nil {
		l.log.Warn("retention-sweep: cannot enumerate orgs; skipping this tick", "err", err)
		return nil
	}
	for _, t := range tenants {
		if err := ctx.Err(); err != nil {
			return err
		}
		sum, err := l.comp.RunRetention(ctx, t)
		if err != nil {
			l.log.Warn("retention-sweep: tenant sweep failed; continuing with the remaining tenants", "tenant", t.String(), "err", err)
			continue
		}
		if sum.Examined > 0 || sum.Purged > 0 || sum.ExcludedHeld > 0 || sum.SkippedClassHolds > 0 || sum.Truncated {
			l.log.Info("retention-sweep: tenant sweep completed",
				"tenant", t.String(), "classes", len(sum.Classes),
				"examined", sum.Examined, "purged", sum.Purged,
				"excluded_held", sum.ExcludedHeld, "skipped_class_holds", sum.SkippedClassHolds,
				"truncated", sum.Truncated)
		}
	}
	return nil
}

// businessTenants enumerates the orgs to sweep. The reserved SYSTEM tenant is
// SKIPPED deliberately: it holds auth/cross-tenant ledger events, never
// business retention policies, and a sweep pass there could only ever no-op or
// write noise into the reserved chain (the archive loop, by contrast, MUST
// cover it — auditarchive.go).
func (l *retentionSweepLoop) businessTenants(ctx context.Context) ([]model.TenantID, error) {
	return servedBusinessTenants(ctx, l.st)
}
