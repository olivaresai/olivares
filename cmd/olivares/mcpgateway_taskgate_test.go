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

	t.Run("checker error fails open", func(t *testing.T) {
		checker := &mcpTaskBudgetChecker{err: errors.New("finops unavailable")}
		dec, err := (mcpTaskGate{fin: checker, tenant: tenant}).AuthorizeTask(ctx, intent)
		if err != nil || !dec.Allow {
			t.Fatalf("budget checker errors must fail open, got %+v err=%v", dec, err)
		}
	})
}
