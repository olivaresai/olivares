// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"errors"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/runtime"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/notify"
	"github.com/olivaresai/olivares/modules/orchestration"
)

// orchworkflowpump.go is the cross-tenant workflow-run pump: per tick it
// calls the orchestration module's exported tenant-scoped AdvanceWorkflowRuns
// for every business org. Phase 2 of a run drains synchronously in-request, so
// this pump owns everything that outlives a request: wait steps, approval-gate
// polling, kill-switch-frozen runs resuming, and crash-orphaned claims failing
// honestly. Same posture as the cadence pump: the runtime's EXISTING
// periodic scheduler, leader-gated per tick, idempotent per pass (the runner's
// claim-then-act makes an overlap advance a no-op).
const (
	orchWorkflowPumpJobName = "orchestration-workflow"
	// orchWorkflowPumpIntervalEnv configures the pump cadence: a Go duration
	// (default 15s — a resolved approval-gate/elapsed wait advances within one
	// tick); "0" disables the loop with a loud warning.
	orchWorkflowPumpIntervalEnv     = "OLIVARES_ORCH_WORKFLOW_INTERVAL"
	defaultOrchWorkflowPumpInterval = 15 * time.Second

	// Per-tenant workflow caps (bounded operator input, docs/SECURITY-HARDENING.md).
	orchWorkflowMaxEnv      = "OLIVARES_ORCH_WORKFLOW_MAX"
	orchWorkflowStepsMaxEnv = "OLIVARES_ORCH_WORKFLOW_STEPS_MAX"
)

// loadWorkflowLimits parses the per-tenant workflow/step caps. Zero return
// values keep the module defaults; an unparseable or non-positive value warns
// and keeps the default rather than silently changing the posture on a typo.
func loadWorkflowLimits(getenv func(string) string, log *slog.Logger) (workflows, steps int) {
	parse := func(key string) int {
		raw := strings.TrimSpace(getenv(key))
		if raw == "" {
			return 0
		}
		n, err := strconv.Atoi(raw)
		if err != nil || n <= 0 {
			log.Warn("orchestration-workflow: "+key+" is not a positive integer; using the default", "value", raw)
			return 0
		}
		return n
	}
	return parse(orchWorkflowMaxEnv), parse(orchWorkflowStepsMaxEnv)
}

// orchNotifyTester adapts the notify module's exported route test to the
// orchestration NotifyTester seam (modules never import each other; the
// composition root owns the bridge). The notify side runs the SAME
// claim-then-send, ledger-recorded path as the manual admin verb.
type orchNotifyTester struct {
	n *notify.Module
}

func (t orchNotifyTester) Test(ctx context.Context, tenant model.TenantID, routeRef string) (string, string, error) {
	_, status, detail, err := t.n.RunRouteTest(ctx, tenant, model.ID(strings.TrimSpace(routeRef)))
	return status, detail, err
}

func (t orchNotifyTester) LookupRoute(ctx context.Context, tenant model.TenantID, routeRef string) (string, bool, error) {
	return t.n.LookupRoute(ctx, tenant, model.ID(strings.TrimSpace(routeRef)))
}

// RouteFingerprint bridges the notify module's opaque route-target digest to the
// orchestration D-06 seam (freeze at approval, block-on-change at execution).
func (t orchNotifyTester) RouteFingerprint(ctx context.Context, tenant model.TenantID, routeRef string) (string, bool, error) {
	return t.n.RouteFingerprint(ctx, tenant, model.ID(strings.TrimSpace(routeRef)))
}

// TestBound bridges the notify module's ATOMIC verify-and-deliver seam (hole c1), mapping notify's route-changed sentinel to the orchestration one so
// the acting step blocks rather than delivering to a re-pointed destination.
func (t orchNotifyTester) TestBound(ctx context.Context, tenant model.TenantID, routeRef, expectedFingerprint, operationID string) (string, string, error) {
	status, detail, err := t.n.TestBound(ctx, tenant, model.ID(strings.TrimSpace(routeRef)), expectedFingerprint, operationID)
	if errors.Is(err, notify.ErrRouteBindingChanged) {
		return "", "", orchestration.ErrRouteBindingChanged
	}
	return status, detail, err
}

// orchWorkflowPump drives the periodic per-tenant workflow-run advance.
type orchWorkflowPump struct {
	st       store.Store
	orch     *orchestration.Module
	interval time.Duration
	log      *slog.Logger
}

// newOrchWorkflowPump builds the pump from the environment. nil when the
// operator explicitly disabled it (interval 0) — then waits and approval
// gates only advance when a phase-2 request happens to drain them, which
// stalls paused runs, so the disable warns loudly.
func newOrchWorkflowPump(getenv func(string) string, st store.Store, orch *orchestration.Module, log *slog.Logger) *orchWorkflowPump {
	interval, ok := orchWorkflowPumpInterval(getenv(orchWorkflowPumpIntervalEnv), log)
	if !ok {
		return nil
	}
	return &orchWorkflowPump{st: st, orch: orch, interval: interval, log: log}
}

// orchWorkflowPumpInterval parses the cadence env. ok=false ONLY on the
// explicit zero; an unparseable or negative value keeps the default (the
// cadence-pump posture).
func orchWorkflowPumpInterval(raw string, log *slog.Logger) (time.Duration, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return defaultOrchWorkflowPumpInterval, true
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d < 0 {
		log.Warn("orchestration-workflow: "+orchWorkflowPumpIntervalEnv+" is not a valid non-negative duration; using the default", "value", raw, "default", defaultOrchWorkflowPumpInterval.String())
		return defaultOrchWorkflowPumpInterval, true
	}
	if d == 0 {
		log.Warn("orchestration-workflow: pump DISABLED (" + orchWorkflowPumpIntervalEnv + "=0): wait/approval-gate workflow steps will NOT advance in the background — a paused run stalls until the next in-request drain")
		return 0, false
	}
	return d, true
}

// register schedules the pump on the runtime's own scheduler (before Start).
func (p *orchWorkflowPump) register(rt *runtime.Runtime) error {
	return rt.SchedulePeriodic(orchWorkflowPumpJobName, p.interval, false, p.runOnce)
}

// runOnce advances every business tenant's running workflows. A per-tenant
// failure is logged and the remaining tenants still advance. Logged fields are
// COUNTS ONLY — never subject refs (docs/SECURITY-HARDENING.md).
func (p *orchWorkflowPump) runOnce(ctx context.Context) error {
	if !p.st.Leader().Active() {
		p.log.Debug("orchestration-workflow skipped: this node is a standby, not the active writer")
		return nil
	}
	tenants, err := p.businessTenants(ctx)
	if err != nil {
		p.log.Warn("orchestration-workflow: cannot enumerate orgs; skipping this tick", "err", err)
		return nil
	}
	for _, t := range tenants {
		if err := ctx.Err(); err != nil {
			return err
		}
		p.orch.AdvanceWorkflowRuns(ctx, api.ModuleContext{Tenant: t, Data: api.NewScopedData(p.st, t)})
	}
	return nil
}

// businessTenants enumerates the orgs to advance (the cadence-pump rule: the
// reserved SYSTEM tenant is skipped — runs are tenant-scoped facts).
func (p *orchWorkflowPump) businessTenants(ctx context.Context) ([]model.TenantID, error) {
	return servedBusinessTenants(ctx, p.st)
}
