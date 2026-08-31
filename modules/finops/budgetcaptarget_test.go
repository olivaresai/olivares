// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package finops

import (
	"context"
	"testing"

	"github.com/olivaresai/olivares/core/model"
)

// TestBudgetCapTarget proves the backstop seam resolves a capped budget id to its
// concrete upstream subject (dimension + key) — the offending api_key or workspace — and
// returns ok=false for a non-budget / unknown id (so the backstop never guesses a target).
func TestBudgetCapTarget(t *testing.T) {
	m, st, tenant, _ := newFin(t)

	keyBudget := createBudget(t, st, tenant, "key-cap", budgetSpec{
		Dimension: "api_key", Key: "apikey_off", Period: "monthly", LimitMicroUSD: 1000, Action: "block",
	})
	wsBudget := createBudget(t, st, tenant, "ws-cap", budgetSpec{
		Dimension: "workspace", Key: "wrkspc_z", Period: "monthly", LimitMicroUSD: 1000, Action: "block",
	})
	globalBudget := createBudget(t, st, tenant, "global-cap", budgetSpec{
		Dimension: "global", Period: "monthly", LimitMicroUSD: 1000, Action: "block",
	})

	ctx := context.Background()

	dim, key, ok, err := m.BudgetCapTarget(ctx, tenant, keyBudget.String())
	if err != nil || !ok || dim != "api_key" || key != "apikey_off" {
		t.Fatalf("api_key budget = (%q,%q,%v,%v), want (api_key,apikey_off,true,nil)", dim, key, ok, err)
	}

	dim, key, ok, err = m.BudgetCapTarget(ctx, tenant, wsBudget.String())
	if err != nil || !ok || dim != "workspace" || key != "wrkspc_z" {
		t.Fatalf("workspace budget = (%q,%q,%v,%v), want (workspace,wrkspc_z,true,nil)", dim, key, ok, err)
	}

	// A global budget resolves but carries no scoping key (no surgical upstream target).
	dim, key, ok, err = m.BudgetCapTarget(ctx, tenant, globalBudget.String())
	if err != nil || !ok || dim != "global" || key != "" {
		t.Fatalf("global budget = (%q,%q,%v,%v), want (global,\"\",true,nil)", dim, key, ok, err)
	}

	// An unknown id is ok=false (not an error) — the backstop declines to actuate.
	_, _, ok, err = m.BudgetCapTarget(ctx, tenant, model.NewID().String())
	if err != nil || ok {
		t.Fatalf("unknown id = (ok=%v,err=%v), want (false,nil)", ok, err)
	}
}
