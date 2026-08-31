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
	"github.com/olivaresai/olivares/modules/siemforward"
)

// ledgerforwardpump.go is the cross-tenant ledger-forward pump: per tick it
// calls the siemforward module's exported tenant-scoped ForwardDue, which walks the
// tamper-evident audit ledger from a per-tenant cursor and hands each sealed record
// to the eventing engine (IngestAudit) for durable SIEM delivery. It is the
// at-least-once driver — the ledger is the authoritative, replayable source, so a
// crash or restart resumes the walk from the persisted cursor and IngestAudit dedups
// any record re-walked. It is pure cadence + tenant enumeration (a System op the
// modules cannot perform) on the runtime's EXISTING periodic scheduler — the
// eventing-pump precedent, never a parallel timer.
//
// HA: only the ACTIVE writer forwards — the cursor advance is a write, so a
// standby gates out per tick exactly like the eventing pump; a promoted standby
// resumes from the persisted cursor on its next tick, re-enqueueing idempotently.
const (
	ledgerForwardPumpJobName = "siem-ledger-forward"
	// ledgerForwardIntervalEnv configures the cadence: a Go duration (default 15s);
	// "0" disables the loop with a loud warning (the ledger then stops flowing to
	// SIEM towers — only findings keep flowing through the bus).
	ledgerForwardIntervalEnv     = "OLIVARES_SIEM_FORWARD_INTERVAL"
	defaultLedgerForwardInterval = 15 * time.Second
)

// ledgerForwardPump drives the periodic tenant-scoped ledger-forward pass.
type ledgerForwardPump struct {
	st       store.Store
	sf       *siemforward.Module
	interval time.Duration
	log      *slog.Logger
}

// newLedgerForwardPump builds the pump from the environment. nil when the module is
// unwired or the operator explicitly disabled it (interval 0); the disable warns
// loudly because the ledger then never reaches a SIEM control tower.
func newLedgerForwardPump(getenv func(string) string, st store.Store, sf *siemforward.Module, log *slog.Logger) *ledgerForwardPump {
	if sf == nil {
		return nil
	}
	interval, ok := ledgerForwardInterval(getenv(ledgerForwardIntervalEnv), log)
	if !ok {
		return nil
	}
	return &ledgerForwardPump{st: st, sf: sf, interval: interval, log: log}
}

// ledgerForwardInterval parses the cadence env. ok=false ONLY on the explicit zero;
// an unparseable or negative value keeps the default rather than silently changing
// the schedule on a typo (the eventing-pump posture).
func ledgerForwardInterval(raw string, log *slog.Logger) (time.Duration, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return defaultLedgerForwardInterval, true
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d < 0 {
		log.Warn("siem-ledger-forward: "+ledgerForwardIntervalEnv+" is not a valid non-negative duration; using the default", "value", raw, "default", defaultLedgerForwardInterval.String())
		return defaultLedgerForwardInterval, true
	}
	if d == 0 {
		log.Warn("siem-ledger-forward: pump DISABLED (" + ledgerForwardIntervalEnv + "=0): the audit ledger will NOT be forwarded to SIEM control towers")
		return 0, false
	}
	return d, true
}

// register schedules the pump on the runtime's own scheduler (before Start).
func (p *ledgerForwardPump) register(rt *runtime.Runtime) error {
	return rt.SchedulePeriodic(ledgerForwardPumpJobName, p.interval, false, p.runOnce)
}

// runOnce forwards every business tenant's new ledger records. A per-tenant failure
// is logged and the remaining tenants still forward (a pass is idempotent: the next
// tick resumes from the same cursor). Logged fields are COUNTS ONLY — never record
// content (docs/SECURITY-HARDENING.md).
func (p *ledgerForwardPump) runOnce(ctx context.Context) error {
	if !p.st.Leader().Active() {
		p.log.Debug("siem-ledger-forward skipped: this node is a standby, not the active writer")
		return nil
	}
	tenants, err := p.businessTenants(ctx)
	if err != nil {
		p.log.Warn("siem-ledger-forward: cannot enumerate orgs; skipping this tick", "err", err)
		return nil
	}
	for _, t := range tenants {
		if err := ctx.Err(); err != nil {
			return err
		}
		if n, err := p.sf.ForwardDue(ctx, t); err != nil {
			p.log.Warn("siem-ledger-forward: tenant pass failed; continuing with the remaining tenants", "tenant", t.String(), "err", err)
		} else if n > 0 {
			p.log.Debug("siem-ledger-forward: forwarded ledger records", "tenant", t.String(), "records", n)
		}
	}
	return nil
}

// businessTenants enumerates the orgs to forward. The reserved SYSTEM tenant is
// skipped: a tenant's SIEM receives that tenant's ledger; cross-tenant/system events
// are out of scope for tenant SIEM forwarding (the same boundary the eventing pump
// and capture path apply).
func (p *ledgerForwardPump) businessTenants(ctx context.Context) ([]model.TenantID, error) {
	return servedBusinessTenants(ctx, p.st)
}
