// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package finops

import (
	"context"
	"testing"
)

func TestSessionBudgetsReportTheMostRestrictiveScopedLimitOnly(t *testing.T) {
	m, st, tenant, _ := newFin(t)
	createBudget(t, st, tenant, "this-session", budgetSpec{Dimension: "session", Key: "osn_a", Period: "total", LimitMicroUSD: 25_000_000, Action: "alert"})
	createBudget(t, st, tenant, "its-agent", budgetSpec{Dimension: "agent", Key: "agent-1", Period: "monthly", LimitMicroUSD: 40_000_000, Action: "block"})
	createBudget(t, st, tenant, "company", budgetSpec{Dimension: "global", Period: "monthly", LimitMicroUSD: 5_000_000, Action: "alert"})
	createBudget(t, st, tenant, "a-model", budgetSpec{Dimension: "model", Key: "m", Period: "monthly", LimitMicroUSD: 1_000_000, Action: "alert"})

	got, err := m.SessionBudgets(context.Background(), tenant, []SessionBudgetSubject{
		{Key: "run-a", SessionRef: "osn_a", AgentRef: "agent-1"},
		{Key: "run-b", SessionRef: "osn_b", AgentRef: "agent-1"},
		{Key: "run-c", SessionRef: "osn_c"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got["run-a"] != (SessionBudget{LimitMicroUSD: 25_000_000, Period: "total"}) {
		t.Fatalf("run-a = %+v, want its own session budget, the lower of the two scoped to it", got["run-a"])
	}
	if got["run-b"] != (SessionBudget{LimitMicroUSD: 40_000_000, Period: "monthly"}) {
		t.Fatalf("run-b = %+v, want its agent's budget", got["run-b"])
	}
	if _, ok := got["run-c"]; ok {
		t.Fatalf("run-c = %+v, want none: only global and per-model budgets reach it", got["run-c"])
	}
}
