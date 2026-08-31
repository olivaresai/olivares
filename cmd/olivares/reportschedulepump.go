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
	"github.com/olivaresai/olivares/modules/reporting"
)

// reportschedulepump.go drives the report scheduler: a leader-gated
// periodic job that, per business tenant, runs every DUE schedule and records its
// outcome. It is the composition-root counterpart to the retention sweep — the
// module owns the persisted schedules + run history (store_providers.go) and the
// due-evaluation (cron.go); this loop supplies the clock and the per-tenant
// ModuleContext the module cannot build for itself.
//
// It is INERT in the community build: newReportSchedulePump returns nil when the
// reporting scheduler seam is not wired (SchedulerWired() is false), so the open
// binary never schedules reports — on-demand generation is unchanged.

const (
	reportScheduleJobName     = "reporting-schedule"
	reportScheduleIntervalEnv = "OLIVARES_REPORTING_SCHEDULE_INTERVAL"
	// defaultReportScheduleInterval: schedules fire at minute granularity, so a
	// 1-minute tick keeps cron cadences (e.g. "0 2 * * *") accurate without a
	// per-second poll. An operator may widen it; "0" disables the pump.
	defaultReportScheduleInterval = time.Minute
)

type reportSchedulePump struct {
	st       store.Store
	rep      *reporting.Module
	interval time.Duration
	clock    func() time.Time
	log      *slog.Logger
}

// newReportSchedulePump builds the pump. nil when the reporting scheduler is not
// wired (community build) or the operator disabled the cadence (interval "0").
func newReportSchedulePump(getenv func(string) string, st store.Store, rep *reporting.Module, log *slog.Logger) *reportSchedulePump {
	if rep == nil || !rep.SchedulerWired() {
		return nil
	}
	interval, ok := reportScheduleInterval(getenv(reportScheduleIntervalEnv), log)
	if !ok {
		return nil
	}
	return &reportSchedulePump{st: st, rep: rep, interval: interval, clock: time.Now, log: log}
}

// reportScheduleInterval parses the cadence env (default 1m). ok=false only on the
// explicit zero (disable), warned loudly; an unparseable/negative value keeps the
// default rather than changing behavior on a typo.
func reportScheduleInterval(raw string, log *slog.Logger) (time.Duration, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return defaultReportScheduleInterval, true
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d < 0 {
		log.Warn("reporting-schedule: "+reportScheduleIntervalEnv+" is not a valid non-negative duration; using the default", "value", raw, "default", defaultReportScheduleInterval.String())
		return defaultReportScheduleInterval, true
	}
	if d == 0 {
		log.Warn("reporting-schedule: pump DISABLED (" + reportScheduleIntervalEnv + "=0): scheduled reports fire only when the pump runs; enable it or schedules are configuration, not execution")
		return 0, false
	}
	return d, true
}

// register schedules the pump on the runtime's own scheduler (before Start).
func (p *reportSchedulePump) register(rt *runtime.Runtime) error {
	return rt.SchedulePeriodic(reportScheduleJobName, p.interval, false, p.runOnce)
}

// runOnce runs every due schedule for every business tenant. Leader-gated (only
// the active writer records runs); a per-tenant failure is logged and the
// remaining tenants still run.
func (p *reportSchedulePump) runOnce(ctx context.Context) error {
	if !p.st.Leader().Active() {
		return nil
	}
	tenants, err := p.businessTenants(ctx)
	if err != nil {
		p.log.Warn("reporting-schedule: cannot enumerate orgs; skipping this tick", "err", err)
		return nil
	}
	now := p.clock().UTC()
	for _, t := range tenants {
		if err := ctx.Err(); err != nil {
			return err
		}
		mc := api.ModuleContext{Tenant: t, Data: api.NewScopedData(p.st, t)}
		ran, err := p.rep.RunDueSchedules(ctx, mc, now)
		if err != nil {
			p.log.Warn("reporting-schedule: tenant run failed; continuing with the remaining tenants", "tenant", t.String(), "err", err)
			continue
		}
		if ran > 0 {
			p.log.Info("reporting-schedule: generated scheduled reports", "tenant", t.String(), "reports", ran)
		}
	}
	return nil
}

// businessTenants enumerates the orgs to run schedules for (the reserved SYSTEM
// tenant is skipped — it holds no business report schedules).
func (p *reportSchedulePump) businessTenants(ctx context.Context) ([]model.TenantID, error) {
	return servedBusinessTenants(ctx, p.st)
}
