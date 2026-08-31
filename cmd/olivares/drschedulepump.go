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
	"github.com/olivaresai/olivares/core/runtime"
	"github.com/olivaresai/olivares/core/store"
)

// drschedulepump.go drives the console backup schedule: a leader-gated
// periodic job that evaluates the persisted DR schedule and runs a due backup
// through the exact same path the console trigger uses. It is the composition-
// root counterpart to the report schedule pump — the API server owns the
// persisted config + cron due-evaluation (core/api/dr_schedule.go); this loop
// supplies the clock and the cadence the server cannot supply for itself.

const (
	drScheduleJobName     = "dr-backup-schedule"
	drScheduleIntervalEnv = "OLIVARES_DR_SCHEDULE_INTERVAL"
	// defaultDRScheduleInterval: schedules fire at minute granularity, so a
	// 1-minute tick keeps cron cadences (e.g. "0 2 * * *") accurate without a
	// per-second poll. An operator may widen it; "0" disables the pump.
	defaultDRScheduleInterval = time.Minute
)

type drSchedulePump struct {
	st       store.Store
	api      *api.Server
	interval time.Duration
	clock    func() time.Time
	log      *slog.Logger
}

// newDRSchedulePump builds the pump. nil when the API server has no DR surface
// or the operator disabled the cadence (interval "0").
func newDRSchedulePump(getenv func(string) string, st store.Store, apiSrv *api.Server, log *slog.Logger) *drSchedulePump {
	if apiSrv == nil {
		return nil
	}
	interval, ok := drScheduleInterval(getenv(drScheduleIntervalEnv), log)
	if !ok {
		return nil
	}
	return &drSchedulePump{st: st, api: apiSrv, interval: interval, clock: time.Now, log: log}
}

// drScheduleInterval parses the cadence env (default 1m). ok=false only on the
// explicit zero (disable), warned loudly; an unparseable/negative value keeps
// the default rather than changing behavior on a typo.
func drScheduleInterval(raw string, log *slog.Logger) (time.Duration, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return defaultDRScheduleInterval, true
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d < 0 {
		log.Warn("dr-schedule: "+drScheduleIntervalEnv+" is not a valid non-negative duration; using the default", "value", raw, "default", defaultDRScheduleInterval.String())
		return defaultDRScheduleInterval, true
	}
	if d == 0 {
		log.Warn("dr-schedule: pump DISABLED (" + drScheduleIntervalEnv + "=0): the console backup schedule fires only when the pump runs; enable it or the schedule is configuration, not execution")
		return 0, false
	}
	return d, true
}

// register schedules the pump on the runtime's own scheduler (before Start).
func (p *drSchedulePump) register(rt *runtime.Runtime) error {
	return rt.SchedulePeriodic(drScheduleJobName, p.interval, false, p.runOnce)
}

// runOnce evaluates the schedule once. Leader-gated (only the active writer
// backs up and records runs); a failure is logged and the next tick retries —
// the schedule bookkeeping records the failure for the console.
func (p *drSchedulePump) runOnce(ctx context.Context) error {
	if !p.st.Leader().Active() {
		return nil
	}
	ran, err := p.api.RunDueScheduledBackup(ctx, p.clock().UTC())
	if err != nil {
		p.log.Warn("dr-schedule: scheduled backup attempt failed; will retry on the next due instant", "err", err)
		return nil
	}
	if ran {
		p.log.Info("dr-schedule: scheduled backup ran")
	}
	return nil
}
