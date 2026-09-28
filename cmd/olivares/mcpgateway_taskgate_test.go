// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"errors"
	"net/http"
	"testing"

	mcpc "github.com/olivaresai/olivares/connectors/mcp"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/modules/finops"
)

type mcpTaskBudgetChecker struct {
	chk  finops.BudgetCheck
	err  error
	dims finops.SpendDims
	// reqs records every admission request the gate built.
	reqs []finops.AdmissionRequest
}

func (c *mcpTaskBudgetChecker) CheckBudget(_ context.Context, _ model.TenantID, dims finops.SpendDims) (finops.BudgetCheck, error) {
	c.dims = dims
	if c.err != nil {
		return finops.BudgetCheck{Allowed: true}, c.err
	}
	return c.chk, nil
}

func (c *mcpTaskBudgetChecker) CheckSpendLimit(context.Context, model.TenantID, string, []string) (finops.SpendLimitCheck, error) {
	return finops.SpendLimitCheck{Allowed: true}, nil
}

func (c *mcpTaskBudgetChecker) Reserve(_ context.Context, _ model.TenantID, req finops.AdmissionRequest) (finops.Reservation, error) {
	c.dims = req.Dims
	c.reqs = append(c.reqs, req)
	return fakeAdmissionReserve(c.chk, c.err, req)
}

func (c *mcpTaskBudgetChecker) Commit(context.Context, model.TenantID, string, int64) error {
	return nil
}
func (c *mcpTaskBudgetChecker) Release(context.Context, model.TenantID, string) error { return nil }

func TestMCPTaskGateBudgetAdapter(t *testing.T) {
	ctx := context.Background()
	tenant := model.TenantID("tenant_test")
	intent := mcpc.TaskIntent{Tenant: tenant.String(), Subject: "agent-a", Tool: "search", TaskID: "task-1"}

	t.Run("allow forwards dimensions", func(t *testing.T) {
		checker := &mcpTaskBudgetChecker{chk: finops.BudgetCheck{Allowed: true}}
		dec, err := (mcpTaskGate{fin: checker, tenant: tenant}).AuthorizeTask(ctx, intent)
		if err != nil || !dec.Allow {
			t.Fatalf("allow decision = %+v err=%v", dec, err)
		}
		if checker.dims.AgentRef != "agent-a" || checker.dims.SessionRef != "task-1" ||
			checker.dims.Gateway != "mcp" || checker.dims.CostType != "task" {
			t.Fatalf("budget dims not forwarded correctly: %+v", checker.dims)
		}
		// A durable task's cost is accounted as it runs, so the gate asks admission under the
		// task's own key, holds nothing it could not settle, and never admits blind.
		if len(checker.reqs) != 1 {
			t.Fatalf("admission asked %d time(s), want exactly 1", len(checker.reqs))
		}
		if req := checker.reqs[0]; req.IdempotencyKey != "mcp_task/task-1" || req.EstimateMicroUSD != 0 ||
			req.Unreachable != finops.UnreachableDeny || req.Scope != finops.AdmissionScopeScheduledJob {
			t.Fatalf("admission request = key %q estimate %d unreachable %q scope %q, want mcp_task/task-1, 0, deny, scheduled_job",
				req.IdempotencyKey, req.EstimateMicroUSD, req.Unreachable, req.Scope)
		}
	})

	t.Run("block and throttle map status", func(t *testing.T) {
		block := &mcpTaskBudgetChecker{chk: finops.BudgetCheck{Allowed: false, Action: "block"}}
		dec, err := (mcpTaskGate{fin: block, tenant: tenant}).AuthorizeTask(ctx, intent)
		if err != nil || dec.Allow || dec.DeniedStatus != http.StatusPaymentRequired {
			t.Fatalf("block decision = %+v err=%v, want 402 deny", dec, err)
		}
		throttle := &mcpTaskBudgetChecker{chk: finops.BudgetCheck{Allowed: false, Action: "throttle"}}
		dec, err = (mcpTaskGate{fin: throttle, tenant: tenant}).AuthorizeTask(ctx, intent)
		if err != nil || dec.Allow || dec.DeniedStatus != http.StatusTooManyRequests {
			t.Fatalf("throttle decision = %+v err=%v, want 429 deny", dec, err)
		}
	})

	// Admission refuses a key whose row failed its integrity check in every posture; the
	// task gate shows it as a 503 with that reason, not as a budget cap reached.
	t.Run("integrity refusal is a 503", func(t *testing.T) {
		checker := &mcpTaskBudgetChecker{chk: finops.BudgetCheck{
			Allowed: false, Action: "block", Reason: finops.ReasonAdmissionIntegrity,
		}}
		dec, err := (mcpTaskGate{fin: checker, tenant: tenant}).AuthorizeTask(ctx, intent)
		if err != nil || dec.Allow || dec.DeniedStatus != http.StatusServiceUnavailable || dec.Reason != finops.ReasonAdmissionIntegrity {
			t.Fatalf("integrity decision = %+v err=%v, want 503 %q", dec, err, finops.ReasonAdmissionIntegrity)
		}
	})

	t.Run("unreachable ledger fails closed", func(t *testing.T) {
		checker := &mcpTaskBudgetChecker{err: errors.New("finops unavailable")}
		dec, err := (mcpTaskGate{fin: checker, tenant: tenant}).AuthorizeTask(ctx, intent)
		if err != nil || dec.Allow || dec.DeniedStatus != http.StatusServiceUnavailable {
			t.Fatalf("an unreachable ledger must deny the task 503, got %+v err=%v", dec, err)
		}
	})
}
