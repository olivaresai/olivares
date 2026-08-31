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
	"github.com/olivaresai/olivares/modules/eventing"
)

// eventingpump.go is the cross-tenant dispatch pump: per tick it calls
// the eventing module's exported tenant-scoped DispatchDue (retry cadence and
// crash recovery — the module's in-process nudge already gives fresh events
// low-latency first attempts) and PruneExpired (the retention/replay window)
// for every business org. It is pure cadence + tenant enumeration — a System
// operation the modules themselves cannot perform — on the runtime's
// EXISTING periodic scheduler (the retention-sweep precedent, never a parallel
// timer).
//
// HA: only the ACTIVE writer pumps — claims and prunes are writes, so a
// standby gates out per tick exactly like the retention sweep (a promoted
// standby starts pumping on its next tick). Claims are optimistic-version-safe
// (never double-taken), a gracefully demoted node is fenced by the store's
// write gate (ErrNotLeader), and outcome writes are ownership-checked against
// the claim version (a writer that outlived its stale window backs off instead
// of clobbering the rescuer's state) — re-delivery in that overlap is still
// possible, which is exactly what at-least-once + the idempotency key permit.
const (
	eventingPumpJobName = "eventing-dispatch"
	// eventingPumpIntervalEnv configures the pump cadence: a Go duration
	// (default 15s — the shortest retry delay is 30s, so retries fire at most
	// one tick late); "0" disables the loop with a loud warning.
	eventingPumpIntervalEnv     = "OLIVARES_EVENTING_DISPATCH_INTERVAL"
	defaultEventingPumpInterval = 15 * time.Second
)

// eventingPump drives the periodic tenant-scoped dispatch + prune pass.
type eventingPump struct {
	st       store.Store
	evt      *eventing.Module
	interval time.Duration
	log      *slog.Logger
}

// newEventingPump builds the pump from the environment. nil when the operator
// explicitly disabled it (interval 0) — then retries and crash recovery depend
// entirely on fresh-event nudges, which is almost never what an operator
// wants, so the disable warns loudly.
func newEventingPump(getenv func(string) string, st store.Store, evt *eventing.Module, log *slog.Logger) *eventingPump {
	interval, ok := eventingPumpInterval(getenv(eventingPumpIntervalEnv), log)
	if !ok {
		return nil
	}
	return &eventingPump{st: st, evt: evt, interval: interval, log: log}
}

// eventingPumpInterval parses the cadence env. ok=false ONLY on the explicit
// zero; an unparseable or negative value keeps the default rather than
// silently changing the schedule on a typo (the retention-sweep posture).
func eventingPumpInterval(raw string, log *slog.Logger) (time.Duration, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return defaultEventingPumpInterval, true
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d < 0 {
		log.Warn("eventing-dispatch: "+eventingPumpIntervalEnv+" is not a valid non-negative duration; using the default", "value", raw, "default", defaultEventingPumpInterval.String())
		return defaultEventingPumpInterval, true
	}
	if d == 0 {
		log.Warn("eventing-dispatch: pump DISABLED (" + eventingPumpIntervalEnv + "=0): webhook retries, crash recovery and event-log pruning will NOT run — only fresh-event nudges deliver")
		return 0, false
	}
	return d, true
}

// register schedules the pump on the runtime's own scheduler (before Start).
func (p *eventingPump) register(rt *runtime.Runtime) error {
	return rt.SchedulePeriodic(eventingPumpJobName, p.interval, false, p.runOnce)
}

// runOnce pumps every business tenant. A per-tenant failure is logged and the
// remaining tenants still pump (a pass is idempotent: the next tick re-scans
// the same due predicate). Logged fields are COUNTS ONLY — never row content
// and never an endpoint URL (docs/SECURITY-HARDENING.md).
func (p *eventingPump) runOnce(ctx context.Context) error {
	if !p.st.Leader().Active() {
		p.log.Debug("eventing-dispatch skipped: this node is a standby, not the active writer")
		return nil
	}
	tenants, err := p.businessTenants(ctx)
	if err != nil {
		p.log.Warn("eventing-dispatch: cannot enumerate orgs; skipping this tick", "err", err)
		return nil
	}
	for _, t := range tenants {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := p.evt.DispatchDue(ctx, t); err != nil {
			p.log.Warn("eventing-dispatch: tenant pass failed; continuing with the remaining tenants", "tenant", t.String(), "err", err)
		}
		if pruned, err := p.evt.PruneExpired(ctx, t); err != nil {
			p.log.Warn("eventing-dispatch: tenant prune failed; continuing", "tenant", t.String(), "err", err)
		} else if pruned > 0 {
			p.log.Info("eventing-dispatch: pruned expired events/deliveries", "tenant", t.String(), "rows", pruned)
		}
	}
	return nil
}

// businessTenants enumerates the orgs to pump. The reserved SYSTEM tenant is
// skipped deliberately: platform events are tenant-scoped facts (the capture
// path drops system-tenant events for the same reason).
func (p *eventingPump) businessTenants(ctx context.Context) ([]model.TenantID, error) {
	return servedBusinessTenants(ctx, p.st)
}
