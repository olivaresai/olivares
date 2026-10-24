// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package finops

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// getBudgetStatus is GET /budgets/{id}/status as the handler answers it, whatever the code.
func getBudgetStatus(m *Module, st store.Store, tenant model.TenantID, id string) *httptest.ResponseRecorder {
	mc := api.ModuleContext{Tenant: tenant, Data: consoleScopedData{tsScopedData: tsScopedData{st: st, tenant: tenant}, data: m.data}}
	req := httptest.NewRequest("GET", "/budgets/"+id+"/status", nil)
	rc := chi.NewRouteContext()
	rc.URLParams.Add("id", id)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rc))
	rec := httptest.NewRecorder()
	m.handleBudgetStatus(rec, req, mc)
	return rec
}

// budgetStatusOverHTTP is what GET /budgets/{id}/status answers for budget id: the
// read an operator's console makes, so the test sees the figures a person sees.
func budgetStatusOverHTTP(t *testing.T, m *Module, st store.Store, tenant model.TenantID, id model.ID) budgetStatusDTO {
	t.Helper()
	rec := getBudgetStatus(m, st, tenant, id.String())
	if rec.Code != 200 {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var out budgetStatusDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// TestUserGroupBudgetStatusShowsTheSpendItIsEnforcedOn: a user_group budget's status
// reports the members' spend in the period, the same figure CheckBudget denies on.
// Before, the status skipped the fan-out and published zero spend, so the one place a
// group's spend against its budget could be read said "nothing spent".
func TestUserGroupBudgetStatusShowsTheSpendItIsEnforcedOn(t *testing.T) {
	m, st, tenant, _ := newFin(t)
	now := m.clock.Now().Time()
	u1 := createCanonicalUser(t, st, "group member one").ID
	u2 := createCanonicalUser(t, st, "group member two").ID
	outsider := createCanonicalUser(t, st, "not in the group").ID
	group := createUserGroup(t, st, tenant, "contractors", u1, u2)
	id := createBudget(t, st, tenant, "contractors-monthly", budgetSpec{
		Dimension: "user_group", Key: group.ID.String(), Period: "monthly", LimitMicroUSD: 1000, Action: "block",
	})
	for _, c := range []struct {
		actor model.ID
		cost  int64
		at    time.Time
	}{{u1, 700, now}, {u2, 450, now.Add(time.Minute)}, {outsider, 9000, now.Add(2 * time.Minute)}} {
		s := mkCost("anthropic", "claude-opus-4-8", "", 10, 5, c.cost, c.at)
		s.Actor = c.actor.String()
		m.ingest(t, tenant, s)
	}
	// A member's spend outside the month is not this month's: one minute before it began,
	// and at the instant the next one starts (a period is half open).
	pStart, _ := periodStart("monthly", now)
	for _, at := range []time.Time{pStart.Add(-time.Minute), periodEnd("monthly", pStart)} {
		s := mkCost("anthropic", "claude-opus-4-8", "", 10, 5, 5000, at)
		s.Actor = u1.String()
		m.ingest(t, tenant, s)
	}

	status := budgetStatusOverHTTP(t, m, st, tenant, id)
	if status.SpendMicroUSD != 1150 {
		t.Errorf("spend_micro_usd = %d, want 1150 (the two members, not the outsider's 9000)", status.SpendMicroUSD)
	}
	if status.LimitMicroUSD != 1000 || !status.Over {
		t.Errorf("limit=%d over=%v, want 1000 and over (a floor above the limit is over it)", status.LimitMicroUSD, status.Over)
	}
	if !status.Truncated {
		t.Error("a member fan-out misses spend nobody attributed to a member: the figure is a floor and must say so (truncated)")
	}
	if status.Amount == nil || status.Amount.State == "complete" || status.Amount.Class != "unknown" || status.Amount.LegacyFields.SpendMicroUSD != legacyValueUnavailable {
		t.Errorf("a floor must never be certified: amount=%+v, want an unknown, incomplete amount and spend labeled %q", status.Amount, legacyValueUnavailable)
	}
	chk, err := m.CheckBudget(context.Background(), tenant, SpendDims{UserGroupRefs: []string{group.ID.String()}})
	if err != nil {
		t.Fatal(err)
	}
	if chk.SpendMicroUSD != status.SpendMicroUSD {
		t.Errorf("status shows %d but CheckBudget enforces on %d", status.SpendMicroUSD, chk.SpendMicroUSD)
	}
}

// TestAgentGroupBudgetStatusShowsTheSpendItIsEnforcedOn is the same for an agent group.
func TestAgentGroupBudgetStatusShowsTheSpendItIsEnforcedOn(t *testing.T) {
	m, st, tenant, _ := newFin(t)
	now := m.clock.Now().Time()
	a1 := createAgent(t, st, tenant, "agent-a", "agent-a", "")
	a2 := createAgent(t, st, tenant, "agent-b", "agent-b", "")
	outside := createAgent(t, st, tenant, "agent-out", "agent-out", "")
	createSession(t, st, tenant, "sess-a", a1)
	createSession(t, st, tenant, "sess-b", a2)
	createSession(t, st, tenant, "sess-out", outside)
	group := createAgentGroup(t, st, tenant, "payments-bots", a1, a2)
	id := createBudget(t, st, tenant, "bots-monthly", budgetSpec{
		Dimension: "agent_group", Key: group.Slug, Period: "monthly", LimitMicroUSD: 2000, Action: "alert",
	})
	m.ingest(t, tenant, mkCost("anthropic", "claude-opus-4-8", "sess-a", 10, 5, 600, now))
	m.ingest(t, tenant, mkCost("anthropic", "claude-opus-4-8", "sess-b", 10, 5, 399, now.Add(time.Minute)))
	m.ingest(t, tenant, mkCost("anthropic", "claude-opus-4-8", "sess-out", 10, 5, 5000, now.Add(2*time.Minute)))
	// A member's spend outside the month is not this month's.
	pStart, _ := periodStart("monthly", now)
	m.ingest(t, tenant, mkCost("anthropic", "claude-opus-4-8", "sess-a", 10, 5, 7000, pStart.Add(-time.Minute)))
	m.ingest(t, tenant, mkCost("anthropic", "claude-opus-4-8", "sess-a", 10, 5, 7000, periodEnd("monthly", pStart)))

	status := budgetStatusOverHTTP(t, m, st, tenant, id)
	if status.SpendMicroUSD != 999 || status.Over || status.ConsumedPct != 49 {
		t.Errorf("spend=%d over=%v consumed=%d%%, want 999, false, 49%%", status.SpendMicroUSD, status.Over, status.ConsumedPct)
	}
	if !status.Truncated {
		t.Error("an agent-group fan-out is a floor and must say so (truncated)")
	}
}

// TestGroupBudgetStatusWithoutMembersStaysUnknown: where the members cannot be read
// (no directory reader on the module's data, or a group that does not exist) the
// status keeps what it always said, no spend and truncated, and never answers 500 or
// invents a figure. Zero here is "not established", the flag says so.
func TestGroupBudgetStatusWithoutMembersStaysUnknown(t *testing.T) {
	t.Run("no directory reader", func(t *testing.T) {
		m, st, tenant, _ := newFin(t)
		u := createCanonicalUser(t, st, "member").ID
		group := createUserGroup(t, st, tenant, "contractors", u)
		id := createBudget(t, st, tenant, "contractors-monthly", budgetSpec{
			Dimension: "user_group", Key: group.ID.String(), Period: "monthly", LimitMicroUSD: 1000, Action: "alert",
		})
		s := mkCost("anthropic", "claude-opus-4-8", "", 10, 5, 700, m.clock.Now().Time())
		s.Actor = u.String()
		m.ingest(t, tenant, s)
		m.UseData(api.NewModuleData(st)) // no AuthView: the Community composition of a handle

		logged := captureLog(m)
		status := budgetStatusOverHTTP(t, m, st, tenant, id)
		if status.SpendMicroUSD != 0 || !status.Truncated {
			t.Errorf("spend=%d truncated=%v, want 0 and truncated", status.SpendMicroUSD, status.Truncated)
		}
		if got := logged.String(); !strings.Contains(got, "level=WARN") || !strings.Contains(got, id.String()) || !strings.Contains(got, "not established") {
			t.Errorf("a zero that means \"not established\" must be logged with its budget; log = %q", got)
		}
	})
	t.Run("group not found", func(t *testing.T) {
		m, st, tenant, _ := newFin(t)
		id := createBudget(t, st, tenant, "ghost-monthly", budgetSpec{
			Dimension: "user_group", Key: model.NewID().String(), Period: "monthly", LimitMicroUSD: 1000, Action: "alert",
		})
		status := budgetStatusOverHTTP(t, m, st, tenant, id)
		if status.SpendMicroUSD != 0 || !status.Truncated {
			t.Errorf("spend=%d truncated=%v, want 0 and truncated", status.SpendMicroUSD, status.Truncated)
		}
	})
}
