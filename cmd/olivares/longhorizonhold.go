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
)

// longhorizonhold.go is the composition-root glue for the OPTIONAL commercial
// long-horizon legal-hold orchestrator (enterprise/wormretention, -tags enterprise) — the
// records-vault counterpart to retentionsweep.go. It is a leader-gated periodic loop on
// the runtime's OWN scheduler that, per business tenant, asks the closed reconciler to
// RECONCILE the WORM archive's object-lock legal holds with the tenant's active engine
// legal holds (holds.go): a hold set in the engine propagates to an indefinite object-lock
// legal hold on the archived segments covering that tenant, preserving the evidence beyond
// the segments' own retention.
//
// The loop is pure cadence + tenant enumeration (a System operation the modules cannot
// perform); ALL orchestration semantics — which segments, how the object lock is set
// and verified — live in the closed reconciler behind the longHorizonHold seam. The
// default build supplies a nil reconciler (wire_noenterprise.go), so newLongHorizonHold
// returns nil and the loop is never registered: byte-identical open behavior, no rug-pull.
//
// Apply-only by design: the reconciler ADDS object-lock legal holds when a covering hold
// is active (over-preservation is always the safe direction); it never AUTO-LIFTS — lifting
// an object-lock legal hold on sealed evidence is a deliberate, separately invoked act.
//
// HA: only the ACTIVE writer reconciles (a standby gates out per tick), exactly like
// the retention sweep and the checkpointer.

const (
	// longHorizonHoldJobName is the runtime scheduler's job name.
	longHorizonHoldJobName = "audit-legalhold-reconcile"
	// longHorizonHoldIntervalEnv configures the cadence: a Go duration (default 6h);
	// "0" disables the loop with a loud warning.
	longHorizonHoldIntervalEnv     = "OLIVARES_AUDIT_LEGALHOLD_INTERVAL"
	defaultLongHorizonHoldInterval = 6 * time.Hour
)

// longHorizonHold is the narrow seam the loop drives. The closed
// enterprise/wormretention orchestrator satisfies it under -tags enterprise; the default
// build supplies nil (newLongHorizonHold in wire_noenterprise.go).
type longHorizonHold interface {
	// ReconcileTenant ensures the tenant's archived WORM segments carry an object-lock
	// legal hold whenever the tenant has an active engine legal hold (apply-only,
	// idempotent). A tenant with no active hold is a cheap no-op.
	ReconcileTenant(ctx context.Context, tenant model.TenantID) error
}

// longHorizonHoldLoop drives the periodic per-tenant reconciliation.
type longHorizonHoldLoop struct {
	st       store.Store
	recon    longHorizonHold
	interval time.Duration
	log      *slog.Logger
}

// newLongHorizonHoldLoop builds the loop. nil when there is no reconciler (the default
// build, or the operator did not opt the enterprise add-on in) or the operator disabled
// the cadence (interval 0).
func newLongHorizonHoldLoop(getenv func(string) string, st store.Store, recon longHorizonHold, log *slog.Logger) *longHorizonHoldLoop {
	if recon == nil {
		return nil
	}
	interval, ok := longHorizonHoldInterval(getenv(longHorizonHoldIntervalEnv), log)
	if !ok {
		return nil
	}
	return &longHorizonHoldLoop{st: st, recon: recon, interval: interval, log: log}
}

// longHorizonHoldInterval parses the cadence env. ok=false ONLY on the explicit zero
// (disabling the loop is a legitimate operator choice, warned loudly); an unparseable or
// negative value keeps the default rather than silently changing the schedule on a typo.
func longHorizonHoldInterval(raw string, log *slog.Logger) (time.Duration, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return defaultLongHorizonHoldInterval, true
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d < 0 {
		log.Warn("audit-legalhold: "+longHorizonHoldIntervalEnv+" is not a valid non-negative duration; using the default", "value", raw, "default", defaultLongHorizonHoldInterval.String())
		return defaultLongHorizonHoldInterval, true
	}
	if d == 0 {
		log.Warn("audit-legalhold: reconciliation loop DISABLED (" + longHorizonHoldIntervalEnv + "=0): object-lock legal holds on the archive are no longer reconciled with engine legal holds")
		return 0, false
	}
	return d, true
}

// register schedules the reconciliation on the runtime's own scheduler (before Start).
func (l *longHorizonHoldLoop) register(rt *runtime.Runtime) error {
	return rt.SchedulePeriodic(longHorizonHoldJobName, l.interval, false, l.runOnce)
}

// runOnce reconciles every business tenant. A per-tenant failure is logged and the
// remaining tenants still reconcile (the reconciler's apply-only object-lock writes are
// idempotent, so a partial pass converges on the next tick).
func (l *longHorizonHoldLoop) runOnce(ctx context.Context) error {
	if !l.st.Leader().Active() {
		l.log.Debug("audit-legalhold reconcile skipped: this node is a standby, not the active writer")
		return nil
	}
	tenants, err := businessTenantIDs(ctx, l.st)
	if err != nil {
		l.log.Warn("audit-legalhold: cannot enumerate orgs; skipping this tick", "err", err)
		return nil
	}
	for _, t := range tenants {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := l.recon.ReconcileTenant(ctx, t); err != nil {
			l.log.Warn("audit-legalhold: tenant reconcile failed; continuing with the remaining tenants", "tenant", t.String(), "err", err)
		}
	}
	return nil
}

// businessTenantIDs enumerates the business orgs (the reserved SYSTEM tenant is skipped:
// legal holds are a per-business-tenant act, mirroring the retention sweep's enumeration).
func businessTenantIDs(ctx context.Context, st store.Store) ([]model.TenantID, error) {
	return servedBusinessTenants(ctx, st)
}
