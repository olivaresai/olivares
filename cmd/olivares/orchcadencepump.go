// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/runtime"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/orchestration"
)

// orchcadencepump.go is the cross-tenant cadence-miss pump: per tick it
// calls the orchestration module's exported tenant-scoped RunCadenceScan for
// every business org. Before it, the anti-evasion check ran ONLY when a human
// read schedules/graph/flows — an estate nobody was watching never raised the
// "schedule went silent" Finding, which is exactly the evasion the check exists
// to catch. Pure cadence + tenant enumeration — a System operation the modules
// themselves cannot perform — on the runtime's EXISTING periodic
// scheduler (the eventing-pump precedent, never a parallel timer).
//
// HA: the scan writes (sticky missed_at, a decision row, a Finding), so
// only the ACTIVE writer pumps — a standby gates out per tick exactly like the
// sweeps, and a promoted standby starts scanning on its next tick. The scan is
// idempotent per tick (a still-missed schedule keeps its sticky marker; a
// recovered one clears it), so an overlap re-scan is harmless.
const (
	orchCadencePumpJobName = "orchestration-cadence"
	// orchCadencePumpIntervalEnv configures the pump cadence: a Go duration
	// (default 1m — the shortest schedule interval is 60s, so a miss is
	// detected at most one tick after its grace window closes); "0" disables
	// the loop with a loud warning.
	orchCadencePumpIntervalEnv     = "OLIVARES_ORCH_CADENCE_INTERVAL"
	defaultOrchCadencePumpInterval = time.Minute
)

// orchCadencePump drives the periodic tenant-scoped cadence-miss scan.
type orchCadencePump struct {
	st       store.Store
	orch     *orchestration.Module
	interval time.Duration
	log      *slog.Logger
}

// newOrchCadencePump builds the pump from the environment. nil when the
// operator explicitly disabled it (interval 0) — then cadence-miss detection
// falls back to read-time piggybacking (someone must LOOK at schedules), which
// defeats the anti-evasion posture, so the disable warns loudly.
func newOrchCadencePump(getenv func(string) string, st store.Store, orch *orchestration.Module, log *slog.Logger) *orchCadencePump {
	interval, ok := orchCadencePumpInterval(getenv(orchCadencePumpIntervalEnv), log)
	if !ok {
		return nil
	}
	return &orchCadencePump{st: st, orch: orch, interval: interval, log: log}
}

// orchCadencePumpInterval parses the cadence env. ok=false ONLY on the explicit
// zero; an unparseable or negative value keeps the default rather than silently
// changing the schedule on a typo (the retention-sweep posture).
func orchCadencePumpInterval(raw string, log *slog.Logger) (time.Duration, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return defaultOrchCadencePumpInterval, true
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d < 0 {
		log.Warn("orchestration-cadence: "+orchCadencePumpIntervalEnv+" is not a valid non-negative duration; using the default", "value", raw, "default", defaultOrchCadencePumpInterval.String())
		return defaultOrchCadencePumpInterval, true
	}
	if d == 0 {
		log.Warn("orchestration-cadence: pump DISABLED (" + orchCadencePumpIntervalEnv + "=0): cadence-miss detection only runs when someone READS schedules/graph/flows — an unwatched estate raises no missed-schedule Finding")
		return 0, false
	}
	return d, true
}

// register schedules the pump on the runtime's own scheduler (before Start).
func (p *orchCadencePump) register(rt *runtime.Runtime) error {
	return rt.SchedulePeriodic(orchCadencePumpJobName, p.interval, false, p.runOnce)
}

// runOnce scans every business tenant. A per-tenant failure is logged and the
// remaining tenants still scan (a pass is idempotent: the next tick re-evaluates
// the same overdue predicate). Logged fields are COUNTS ONLY — never subject
// refs (docs/SECURITY-HARDENING.md).
func (p *orchCadencePump) runOnce(ctx context.Context) error {
	if !p.st.Leader().Active() {
		p.log.Debug("orchestration-cadence skipped: this node is a standby, not the active writer")
		return nil
	}
	tenants, err := p.businessTenants(ctx)
	if err != nil {
		p.log.Warn("orchestration-cadence: cannot enumerate orgs; skipping this tick", "err", err)
		return nil
	}
	for _, t := range tenants {
		if err := ctx.Err(); err != nil {
			return err
		}
		p.orch.RunCadenceScan(ctx, api.ModuleContext{Tenant: t, Data: api.NewScopedData(p.st, t)})
	}
	return nil
}

// businessTenants enumerates the orgs to scan. The reserved SYSTEM tenant is
// skipped deliberately: schedules are tenant-scoped facts (the eventing pump's
// rule).
func (p *orchCadencePump) businessTenants(ctx context.Context) ([]model.TenantID, error) {
	return servedBusinessTenants(ctx, p.st)
}
