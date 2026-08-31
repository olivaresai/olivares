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
	"github.com/olivaresai/olivares/modules/notify"
)

// notifypump.go is the cross-tenant durable-outbox pump for module XV: per tick
// it calls the notify module's exported tenant-scoped NotifyDispatchDue, which claims
// due outbox rows, delivers them through the connector dispatcher, and retries with
// backoff or dead-letters on exhaustion. It is pure cadence + tenant enumeration — a
// System operation the module itself cannot perform — on the runtime's EXISTING
// periodic scheduler (the eventing-pump precedent, never a parallel timer).
//
// HA: only the ACTIVE writer pumps — claims are writes, so a standby gates out
// per tick exactly like the eventing pump (a promoted standby starts on its next tick).
// Claims are optimistic-version-safe (never double-taken); outcome writes are ownership-
// checked against the claim version, so a writer that outlived its stale window backs
// off instead of clobbering a rescuer. Re-delivery in that overlap is exactly what at-
// least-once + the stable idempotency key (sdk.IdempotencyKeyField) permit.
const (
	notifyPumpJobName = "notify-dispatch"
	// notifyPumpIntervalEnv configures the cadence: a Go duration (default 5s — a
	// notification is human-facing, so first-attempt latency is kept short); "0"
	// disables the loop with a loud warning (only fresh events would deliver — and the
	// event handler no longer delivers inline, so 0 means nothing delivers).
	notifyPumpIntervalEnv     = "OLIVARES_NOTIFY_DISPATCH_INTERVAL"
	defaultNotifyPumpInterval = 5 * time.Second
)

// notifyPump drives the periodic tenant-scoped outbox dispatch pass.
type notifyPump struct {
	st       store.Store
	nm       *notify.Module
	interval time.Duration
	log      *slog.Logger
}

// newNotifyPump builds the pump from the environment. nil when the module is unwired
// or the operator explicitly disabled it (interval 0) — the disable warns loudly
// because, with delivery now out of band of the bus handler, a disabled pump means NO
// notification is ever delivered.
func newNotifyPump(getenv func(string) string, st store.Store, nm *notify.Module, log *slog.Logger) *notifyPump {
	if nm == nil {
		return nil
	}
	interval, ok := notifyPumpInterval(getenv(notifyPumpIntervalEnv), log)
	if !ok {
		return nil
	}
	return &notifyPump{st: st, nm: nm, interval: interval, log: log}
}

// notifyPumpInterval parses the cadence env. ok=false ONLY on the explicit zero; an
// unparseable or negative value keeps the default rather than silently changing the
// schedule on a typo (the eventing-pump posture).
func notifyPumpInterval(raw string, log *slog.Logger) (time.Duration, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return defaultNotifyPumpInterval, true
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d < 0 {
		log.Warn("notify-dispatch: "+notifyPumpIntervalEnv+" is not a valid non-negative duration; using the default", "value", raw, "default", defaultNotifyPumpInterval.String())
		return defaultNotifyPumpInterval, true
	}
	if d == 0 {
		log.Warn("notify-dispatch: pump DISABLED (" + notifyPumpIntervalEnv + "=0): NO notification will be delivered — routing now enqueues to the durable outbox, which only this pump drains")
		return 0, false
	}
	return d, true
}

// register schedules the pump on the runtime's own scheduler (before Start).
func (p *notifyPump) register(rt *runtime.Runtime) error {
	return rt.SchedulePeriodic(notifyPumpJobName, p.interval, false, p.runOnce)
}

// runOnce pumps every business tenant. A per-tenant failure is logged and the remaining
// tenants still pump (a pass is idempotent: the next tick re-scans the same due
// predicate). Logged fields are COUNTS/ids ONLY — never notification content.
func (p *notifyPump) runOnce(ctx context.Context) error {
	if !p.st.Leader().Active() {
		p.log.Debug("notify-dispatch skipped: this node is a standby, not the active writer")
		return nil
	}
	tenants, err := p.businessTenants(ctx)
	if err != nil {
		p.log.Warn("notify-dispatch: cannot enumerate orgs; skipping this tick", "err", err)
		return nil
	}
	for _, t := range tenants {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := p.nm.NotifyDispatchDue(ctx, t); err != nil {
			p.log.Warn("notify-dispatch: tenant pass failed; continuing with the remaining tenants", "tenant", t.String(), "err", err)
		}
	}
	return nil
}

// businessTenants enumerates the orgs to pump. The reserved SYSTEM tenant is skipped
// deliberately (notify routes on tenant-scoped findings/approvals, like eventing).
func (p *notifyPump) businessTenants(ctx context.Context) ([]model.TenantID, error) {
	return servedBusinessTenants(ctx, p.st)
}
