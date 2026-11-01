// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"log/slog"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/runtime"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/governance"
	"github.com/olivaresai/olivares/modules/sourcescope"
)

const accessExpiryJobName = "access-expiry"

// accessPump is the sub-minute materialization of grant and binding expiry on
// the runtime's existing scheduler. A binding is checked against its expiry on every
// resolution; a scoped grant leaves the Cedar projection on the next tick, so it stops
// within one interval.
type accessPump struct {
	st       store.Store
	gov      *governance.Module
	ss       *sourcescope.Module
	interval time.Duration
	log      *slog.Logger
	tenants  func(context.Context) ([]model.TenantID, error)
}

func newAccessPump(st store.Store, gov *governance.Module, ss *sourcescope.Module, log *slog.Logger) *accessPump {
	return &accessPump{
		st: st, gov: gov, ss: ss, interval: 30 * time.Second, log: log,
		tenants: func(ctx context.Context) ([]model.TenantID, error) { return servedWorkTenants(ctx, st) },
	}
}

func (p *accessPump) register(rt *runtime.Runtime) error {
	name := accessExpiryJobName
	if p.gov == nil {
		name = "sourcescope-" + accessExpiryJobName
	}
	if p.ss == nil {
		name = "governance-" + accessExpiryJobName
	}
	return rt.SchedulePeriodic(name, p.interval, false, p.runOnce)
}

// ponytail: every tick takes one write transaction per tenant, even when nothing has lapsed;
// add a read-only pre-check when measured contention with policy writers shows up.
func (p *accessPump) runOnce(ctx context.Context) error {
	if !p.st.Leader().Active() {
		p.log.Debug("access-expiry skipped: this node is a standby")
		return nil
	}
	tenants, err := p.tenants(ctx)
	if err != nil {
		p.log.Warn("access-expiry: cannot enumerate orgs; skipping this tick", "err", err)
		return nil
	}
	for _, tenant := range tenants {
		if err := ctx.Err(); err != nil {
			return err
		}
		if p.gov != nil {
			if _, err := p.gov.SweepGrantExpiry(ctx, tenant); err != nil {
				p.log.Warn("access-expiry: scoped-grant pass failed; continuing", "tenant", tenant.String(), "err", err)
			}
		}
		if p.ss != nil {
			if _, err := p.ss.SweepExpiredBindings(ctx, tenant); err != nil {
				p.log.Warn("access-expiry: binding pass failed; continuing", "tenant", tenant.String(), "err", err)
			}
		}
	}
	return nil
}
