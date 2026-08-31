// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package models_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/runtime"
	"github.com/olivaresai/olivares/modules/models"
	"github.com/olivaresai/olivares/sdk"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

// programmableBudgetGate returns whatever its closure yields, so one harness (one
// seeded estate + policy) can exercise allow / block / throttle / error in sequence.
type programmableBudgetGate struct {
	fn func() (models.BudgetDecision, error)
}

func (g programmableBudgetGate) Check(context.Context, model.TenantID, models.BudgetDims) (models.BudgetDecision, error) {
	return g.fn()
}

// TestResolveBudgetEnforcement proves the FinOps pre-flight (FIN-08) on the model-router
// resolve: an enforcing budget at its cap DENIES the routing decision (Denial-of-Wallet)
// — the gateway gets no target — while an allowed/erroring gate leaves routing intact
// (opt-in + fail-open).
func TestResolveBudgetEnforcement(t *testing.T) {
	var verdict models.BudgetDecision
	var gateErr error
	gate := programmableBudgetGate{fn: func() (models.BudgetDecision, error) { return verdict, gateErr }}

	m := models.New(models.WithBudgetGate(gate))
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "e@acme.com", auth.RoleEditor)
	viewer := h.roleToken(admin, tenant, "v@acme.com", auth.RoleViewer)

	// Populate the governed estate through the real runtime + bus.
	rt := runtime.New(runtime.Options{})
	if err := rt.AddModule(m, sdk.Config{}); err != nil {
		t.Fatal(err)
	}
	src := &fakeSource{costs: []sdkmodel.CostSample{
		{ProviderRef: "anthropic", ModelRef: "claude-opus-4-8", InputTokens: 10, OutputTokens: 5, CostMicroUSD: 99, OccurredAt: time.Now()},
		{ProviderRef: "google", ModelRef: "gemini-1.5-flash", InputTokens: 10, OutputTokens: 5, CostMicroUSD: 1, OccurredAt: time.Now()},
	}}
	if err := rt.AddSource(src, sdk.Config{}, tenant.String()); err != nil {
		t.Fatal(err)
	}
	if err := rt.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = rt.Stop(ctx)
	})
	h.waitModels(tenant, 2)

	r := h.do("POST", "/v1/m/models/routing-policies", editor, map[string]any{
		"name": "cheap-default", "enabled": true, "strategy": "cost",
	}, tenantHdr(tenant))
	if r.code != http.StatusCreated {
		t.Fatalf("create policy = %d %s", r.code, r.raw)
	}
	id := r.body["id"].(string)
	resolve := func() resp {
		return h.do("POST", "/v1/m/models/routing-policies/"+id+"/resolve", viewer, nil, tenantHdr(tenant))
	}

	// 1. Allowed: resolve succeeds, picking the cheapest governed model.
	verdict, gateErr = models.BudgetDecision{Allowed: true}, nil
	if r = resolve(); r.code != http.StatusOK || r.body["resolved"] != true || r.body["budget_action"] != nil {
		t.Fatalf("allowed budget must resolve cleanly, got %d %s", r.code, r.raw)
	}
	if primary, _ := r.body["primary"].(map[string]any); primary["model_ref"] != "gemini-1.5-flash" {
		t.Fatalf("resolved primary = %v, want gemini-1.5-flash", r.body["primary"])
	}

	// 2. Block: 402, resolved=false, no usable target, budget_action=block.
	verdict, gateErr = models.BudgetDecision{Action: "block", BudgetRef: "b1", Reason: `budget "router-cap" block cap reached (monthly)`}, nil
	if r = resolve(); r.code != http.StatusPaymentRequired || r.body["resolved"] != false || r.body["budget_action"] != "block" {
		t.Fatalf("block budget must deny resolve with 402, got %d %s", r.code, r.raw)
	}
	if r.body["primary"] != nil {
		t.Fatalf("a budget-denied resolve must hand back NO target, got primary=%v", r.body["primary"])
	}
	// /resolve is read-tier (a viewer reaches it): the denial reason must NOT disclose
	// the operator's budget NAME, and must carry no USD amount (docs/SECURITY-HARDENING.md).
	if strings.Contains(r.raw, "router-cap") || strings.Contains(r.raw, "$") {
		t.Fatalf("read-tier resolve denial must not leak the budget name or USD amount, got %s", r.raw)
	}

	// 3. Throttle: 429, budget_action=throttle.
	verdict, gateErr = models.BudgetDecision{Action: "throttle", BudgetRef: "b2"}, nil
	if r = resolve(); r.code != http.StatusTooManyRequests || r.body["resolved"] != false || r.body["budget_action"] != "throttle" {
		t.Fatalf("throttle budget must deny resolve with 429, got %d %s", r.code, r.raw)
	}

	// 4. Gate error: fail OPEN — resolve proceeds unchanged.
	verdict, gateErr = models.BudgetDecision{}, errors.New("synthetic finops outage")
	if r = resolve(); r.code != http.StatusOK || r.body["resolved"] != true {
		t.Fatalf("a budget-gate error must fail OPEN (resolve proceeds), got %d %s", r.code, r.raw)
	}
}
