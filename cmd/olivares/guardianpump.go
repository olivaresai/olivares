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
	"github.com/olivaresai/olivares/modules/governance"
)

// guardianpump.go is the cross-tenant guardian sweep: per tick it calls
// the governance module's exported tenant-scoped GuardianSweep, which advances
// every PENDING guardian containment whose approval reached a terminal state
// (approved → execute now; rejected/canceled → rejected; lapsed → expired).
// The finding-driven half of the loop is event-driven inside the module; this
// pump is the HITL half's executor cadence — a human's approval takes effect
// within one tick, not on the next matching finding. Pure cadence + tenant
// enumeration (a System operation modules cannot perform), on the runtime's
// EXISTING periodic scheduler, leader-gated per tick like every sweep
// (eventingpump.go is the template).
const (
	guardianPumpJobName = "guardian-sweep"
	// guardianPumpIntervalEnv configures the sweep cadence: a Go duration
	// (default 30s — a containment confirmation should bite promptly); "0"
	// disables the loop with a loud warning (approved containments would then
	// execute only when an operator triggers a sweep by other means — almost
	// never what anyone wants during an incident).
	guardianPumpIntervalEnv     = "OLIVARES_GUARDIAN_SWEEP_INTERVAL"
	defaultGuardianPumpInterval = 30 * time.Second
)

// guardianPump drives the periodic tenant-scoped guardian sweep.
type guardianPump struct {
	st       store.Store
	gov      *governance.Module
	interval time.Duration
	log      *slog.Logger
}

// newGuardianPump builds the pump from the environment. nil only when the
// operator explicitly disabled it (interval 0), which warns loudly.
func newGuardianPump(getenv func(string) string, st store.Store, gov *governance.Module, log *slog.Logger) *guardianPump {
	raw := strings.TrimSpace(getenv(guardianPumpIntervalEnv))
	interval := defaultGuardianPumpInterval
	if raw != "" {
		d, err := time.ParseDuration(raw)
		switch {
		case err != nil || d < 0:
			log.Warn("guardian-sweep: "+guardianPumpIntervalEnv+" is not a valid non-negative duration; using the default", "value", raw, "default", defaultGuardianPumpInterval.String())
		case d == 0:
			log.Warn("guardian-sweep: pump DISABLED (" + guardianPumpIntervalEnv + "=0): approved guardian containments will NOT execute automatically")
			return nil
		default:
			interval = d
		}
	}
	return &guardianPump{st: st, gov: gov, interval: interval, log: log}
}

// register schedules the pump on the runtime's own scheduler (before Start).
func (p *guardianPump) register(rt *runtime.Runtime) error {
	return rt.SchedulePeriodic(guardianPumpJobName, p.interval, false, p.runOnce)
}

// runOnce sweeps every business tenant. A per-tenant failure is logged and the
// remaining tenants still sweep (the pass is idempotent — every transition is
// state-change-guarded in the module). Logged fields are COUNTS ONLY.
func (p *guardianPump) runOnce(ctx context.Context) error {
	if !p.st.Leader().Active() {
		p.log.Debug("guardian-sweep skipped: this node is a standby, not the active writer")
		return nil
	}
	tenants, err := p.businessTenants(ctx)
	if err != nil {
		p.log.Warn("guardian-sweep: cannot enumerate orgs; skipping this tick", "err", err)
		return nil
	}
	for _, t := range tenants {
		if err := ctx.Err(); err != nil {
			return err
		}
		res, serr := p.gov.GuardianSweep(ctx, t)
		if serr != nil {
			p.log.Warn("guardian-sweep: tenant pass failed; continuing with the remaining tenants", "tenant", t.String(), "err", serr)
			continue
		}
		if res.Executed+res.Rejected+res.Expired+res.Failed > 0 {
			p.log.Info("guardian-sweep: advanced pending containments",
				"tenant", t.String(), "executed", res.Executed, "rejected", res.Rejected, "expired", res.Expired, "failed", res.Failed)
		}
	}
	return nil
}

// businessTenants enumerates the orgs to sweep (the eventing pump's rule: the
// reserved SYSTEM tenant is skipped — guardian state is tenant-scoped fact).
func (p *guardianPump) businessTenants(ctx context.Context) ([]model.TenantID, error) {
	return servedBusinessTenants(ctx, p.st)
}
