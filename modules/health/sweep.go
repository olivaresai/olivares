// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package health

import (
	"context"
	"fmt"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

// sweepLoop runs the staleness sweep on a ticker until the module stops. It is the
// proactive engine: it alerts on a subject that goes silent without waiting for a
// read (the anti-evasion staleness signal, docs/SECURITY-HARDENING.md).
func (m *Module) sweepLoop(ctx context.Context) {
	defer m.wg.Done()
	ticker := time.NewTicker(m.sweepInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.sweepAll(ctx)
		}
	}
}

// sweepAll sweeps every tenant that has produced a health signal in this process's
// lifetime (the data seam cannot enumerate tenants, so the module tracks the set
// it has seen). A per-tenant failure is logged, never fatal to the others.
func (m *Module) sweepAll(ctx context.Context) {
	for _, tenant := range m.seenTenants() {
		if err := m.sweepTenant(ctx, tenant); err != nil {
			m.debugf("health: sweep tenant failed", "tenant", tenant.String(), "err", err)
		}
	}
}

// sweepTenant scans one tenant's active checks: it transitions a subject overdue
// vs its cadence to degraded, then down (opening/escalating an incident), and
// evaluates each SLA target against the trailing window. Findings and SSE
// snapshots are emitted AFTER the transaction commits.
func (m *Module) sweepTenant(ctx context.Context, tenant model.TenantID) error {
	now := m.clock.Now().Time()
	var emits []func()
	err := m.data.Mutate(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(checkKind)
		if err != nil {
			return err
		}
		checks, err := listAll(ctx, repo, eq(colDesiredStat, "active"))
		if err != nil {
			return err
		}
		for _, check := range checks {
			changed := false

			if newState := m.deriveStaleState(check, now); newState != "" {
				t, err := m.applyStateTx(ctx, sc, check, newState, causeSweep, -1, "silence", now)
				if err != nil {
					return err
				}
				// applyStateTx refreshed last_checked_at in-memory even on a same-state
				// sweep, so persist it (a stably-down subject's "last checked" should
				// still advance). A same-state pass creates no event/incident/finding.
				changed = true
				if t.happened {
					tt := t // capture per iteration
					emits = append(emits, func() { m.publishTransition(ctx, tenant, tt) })
				}
			}

			slaChanged, alert, err := m.evaluateSLATx(ctx, sc, check, now)
			if err != nil {
				return err
			}
			if slaChanged {
				changed = true
				if alert != nil {
					a := alert
					emits = append(emits, func() { m.emitSLA(ctx, tenant, a) })
				}
			}

			if changed {
				if _, err := repo.Update(ctx, check); err != nil {
					return err
				}
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	for _, e := range emits {
		e()
	}
	return nil
}

// deriveStaleState computes the staleness-derived state for an active check, or ""
// for "no change". The sweep only ever DEGRADES or marks DOWN — recovery to
// healthy comes exclusively from real liveness (an edge or a probe report), never
// from the sweep deciding a subject is "not stale yet", so a fresh check never
// emits a spurious recovery. The baseline is the subject's last LIVENESS
// (last_seen), falling back to created_at — deliberately NOT last_checked: the
// sweep itself advances last_checked when it persists the degraded transition, so
// using it as the baseline would reset the down-escalation clock at each step (a
// subject would need degradedAfter+downAfter of silence to reach down, not
// downAfter). Liveness-anchored, escalation measures from when the subject actually
// went silent.
func (m *Module) deriveStaleState(check model.Record, now time.Time) string {
	interval := check.Int(colExpectedIvl)
	if interval <= 0 {
		interval = defaultExpectedInterval
	}
	grace := check.Int(colGraceFactor)
	if grace <= 0 {
		grace = defaultGraceFactor
	}
	baseline := check.String(colLastSeenAt)
	if baseline == "" {
		baseline = check.String(model.ColCreatedAt)
	}
	baseTS, err := model.ParseTimestamp(baseline)
	if err != nil {
		return ""
	}
	age := now.Sub(baseTS.Time()).Seconds()
	degradedAfter := float64(interval * grace)
	downAfter := degradedAfter * float64(m.downMultiple)
	switch {
	case age >= downAfter:
		return stateDown
	case age >= degradedAfter:
		return stateDegraded
	default:
		return ""
	}
}

// emitSLA emits the SLA-breach finding for a newly-breaching check (best-effort,
// after commit). It carries only the uptime/target figures, never raw data.
func (m *Module) emitSLA(ctx context.Context, tenant model.TenantID, a *slaAlert) {
	title := fmt.Sprintf("SLA breach: %s %s uptime %s < target %s",
		a.subjectKind, clamp(a.subjectRef, 120), formatPPM(a.uptimePPM), formatPPM(a.targetPPM))
	detail := fmt.Sprintf("sla:uptime_ppm=%d,target_ppm=%d", a.uptimePPM, a.targetPPM)
	m.emitFinding(ctx, tenant, busSLABreach, sdkmodel.SeverityHigh, a.subjectKind, a.subjectRef, title, detail)
}

// formatPPM renders a parts-per-million uptime as a human percentage (999000 →
// "99.9000%").
func formatPPM(ppm int64) string {
	return fmt.Sprintf("%.4f%%", float64(ppm)/float64(ppmFull)*100)
}
