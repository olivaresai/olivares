// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/runtime"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/sessions"
)

const (
	workOutboxPumpJobName     = "sessions-work-outbox"
	workOutboxPumpIntervalEnv = "OLIVARES_WORK_OUTBOX_INTERVAL"
	defaultWorkOutboxInterval = 15 * time.Second
)

type workOutboxPump struct {
	st       store.Store
	sessions *sessions.Module
	interval time.Duration
	log      *slog.Logger
}

func newWorkOutboxPump(getenv func(string) string, st store.Store, sm *sessions.Module, log *slog.Logger) *workOutboxPump {
	if sm == nil {
		return nil
	}
	interval := defaultWorkOutboxInterval
	if raw := strings.TrimSpace(getenv(workOutboxPumpIntervalEnv)); raw != "" {
		parsed, err := time.ParseDuration(raw)
		if err != nil || parsed <= 0 {
			log.Warn("sessions-work-outbox: invalid interval; using default", "value", raw, "default", interval.String())
		} else {
			interval = parsed
		}
	}
	return &workOutboxPump{st: st, sessions: sm, interval: interval, log: log}
}

func (p *workOutboxPump) register(rt *runtime.Runtime) error {
	return rt.SchedulePeriodic(workOutboxPumpJobName, p.interval, false, p.runOnce)
}

func (p *workOutboxPump) runOnce(ctx context.Context) error {
	if !p.st.Leader().Active() {
		return nil
	}
	var tenants []model.TenantID
	if err := p.st.System(ctx, func(sc store.SystemScope) error {
		orgs, err := sc.ListOrgs(ctx)
		if err != nil {
			return err
		}
		for _, org := range orgs {
			if !org.TenantID.IsZero() && !org.TenantID.IsSystem() {
				tenants = append(tenants, org.TenantID)
			}
		}
		return nil
	}); err != nil {
		p.log.Warn("sessions-work-outbox: cannot enumerate tenants", "err", err)
		return fmt.Errorf("sessions-work-outbox: enumerate tenants: %w", err)
	}
	var failures []error
	for _, tenant := range tenants {
		if reaped, err := p.sessions.ReapWorkLeases(ctx, tenant, 200); err != nil {
			p.log.Warn("sessions-work-lease: tenant reap failed; expired authority stays deny-closed",
				"tenant", tenant.String(), "err", err)
			failures = append(failures, fmt.Errorf("tenant %s lease reap: %w", tenant, err))
		} else if reaped > 0 {
			p.log.Info("sessions-work-lease: recovered expired ownership",
				"tenant", tenant.String(), "reaped", reaped)
		}
		if err := p.sessions.DrainWorkOutbox(ctx, tenant, 200); err != nil {
			p.log.Warn("sessions-work-outbox: tenant drain failed; event remains durable", "tenant", tenant.String(), "err", err)
			failures = append(failures, fmt.Errorf("tenant %s: %w", tenant, err))
		}
	}
	return errors.Join(failures...)
}
