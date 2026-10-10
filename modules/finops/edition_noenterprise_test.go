// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build !enterprise

package finops

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

type editionRegistrar map[string]api.ModuleHandler

func (reg editionRegistrar) Handle(method, pattern string, _ auth.Permission, h api.ModuleHandler) {
	reg[method+" "+pattern] = h
}
func (reg editionRegistrar) HandleEntity(method, pattern string, p auth.Permission, _ api.EntityRef, h api.ModuleHandler) {
	reg.Handle(method, pattern, p, h)
}

func TestCommunityCannotAuthorBudgets(t *testing.T) {
	m, st, tenant, _ := newFin(t)
	id := createBudget(t, st, tenant, "stored", budgetSpec{Dimension: "global", LimitMicroUSD: 10, Period: "monthly", Action: "block"})
	reg := editionRegistrar{}
	m.APIRoutes(reg)
	for _, route := range []string{"POST /budgets", "PUT /budgets/{id}"} {
		t.Run(route, func(t *testing.T) {
			h, ok := reg[route]
			if !ok {
				t.Fatal("published route missing")
			}
			w := httptest.NewRecorder()
			r := httptest.NewRequest(strings.Fields(route)[0], "/budgets", strings.NewReader(`{"name":"changed","dimension":"global","limit_micro_usd":1000,"period":"monthly","action":"block","enabled":true}`))
			rc := chi.NewRouteContext()
			rc.URLParams.Add("id", id.String())
			r = r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rc))
			h(w, r, api.ModuleContext{Tenant: tenant, Data: api.NewScopedData(st, tenant)})
			if w.Code != http.StatusNotImplemented {
				t.Errorf("%s = %d; want 501", route, w.Code)
			}
		})
	}
	if err := st.View(context.Background(), tenant, func(sc store.Scope) error {
		rows, _, err := sc.Policies().List(context.Background(), model.Query{})
		if err != nil {
			return err
		}
		if len(rows) != 1 || rows[0].Name != "stored" {
			t.Errorf("edition refusal mutated stored policies: %+v", rows)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
func TestCommunityCannotUpsertSpendLimits(t *testing.T) {
	m, _, tenant, _ := newFin(t)
	amount := "100"
	_, _, err := m.SpendLimitUpsert(context.Background(), tenant, SpendLimitSpec{Scope: SpendLimitScope{Type: "organization"}, Amount: &amount, Period: "daily"}, "user:admin")
	if !errors.Is(err, ErrNotInEdition) {
		t.Fatalf("Community spend-limit write = %v; want an edition refusal", err)
	}
}

func TestCommunityStoredBudgetDoesNotComputePaidForecast(t *testing.T) {
	m, st, tenant, _ := newFin(t)
	id := createBudget(t, st, tenant, "stored forecast cap", budgetSpec{Dimension: "global", LimitMicroUSD: 1000, Period: "monthly", Action: "block"})
	m.ingest(t, tenant, mkCost("provider", "model", "", 1, 1, 300, baseTime))
	var status budgetStatusDTO
	if err := st.View(context.Background(), tenant, func(sc store.Scope) error {
		p, err := sc.Policies().Get(context.Background(), id)
		if err != nil {
			return err
		}
		status, err = budgetStatus(context.Background(), sc, p, baseTime)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if status.ProjectedMicroUSD != 0 || status.ProjectedPct != 0 {
		t.Fatalf("Community computed paid budget forecast: %d micro-USD (%d%%)", status.ProjectedMicroUSD, status.ProjectedPct)
	}
	if status.Amount == nil || status.Amount.ForecastCertified || status.Amount.LegacyFields.ProjectedMicroUSD != legacyValueUnavailable {
		t.Fatalf("Community forecast metadata did not report unavailable: %+v", status.Amount)
	}
}

func TestCommunityStoredBudgetStillEnforcesAndCanBeRemoved(t *testing.T) {
	m, st, tenant, _ := newFin(t)
	now := m.clock.Now().Time()
	id := createBudget(t, st, tenant, "stored cap", budgetSpec{Dimension: "global", LimitMicroUSD: 10, Period: "monthly", Action: "block"})
	m.ingest(t, tenant, mkCost("provider", "model", "", 1, 1, 20, now))
	decision, err := m.CheckBudget(context.Background(), tenant, SpendDims{})
	if err != nil || decision.Allowed {
		t.Fatalf("stored budget did not block: %+v %v", decision, err)
	}
	reg := editionRegistrar{}
	m.APIRoutes(reg)
	mc := api.ModuleContext{Tenant: tenant, Data: api.NewScopedData(st, tenant)}
	w := httptest.NewRecorder()
	reg["GET /budgets"](w, httptest.NewRequest("GET", "/budgets", nil), mc)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), id.String()) {
		t.Fatalf("stored budget read: %d %s", w.Code, w.Body.String())
	}
	r := httptest.NewRequest("DELETE", "/budgets/"+id.String(), nil)
	rc := chi.NewRouteContext()
	rc.URLParams.Add("id", id.String())
	r = r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rc))
	w = httptest.NewRecorder()
	reg["DELETE /budgets/{id}"](w, r, mc)
	if w.Code != http.StatusNoContent {
		t.Fatalf("stored budget removal: %d %s", w.Code, w.Body.String())
	}
	decision, err = m.CheckBudget(context.Background(), tenant, SpendDims{})
	if err != nil || !decision.Allowed {
		t.Fatalf("removed budget still blocks: %+v %v", decision, err)
	}
}
