// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package finops

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// failingAuthData is a module data handle whose directory read fails.
type failingAuthData struct{ api.ModuleData }

func (failingAuthData) AuthView(context.Context, func(store.AuthScope) error) error {
	return errors.New("auth store down")
}

// A group budget whose members cannot be established says so (zero, truncated); a read that
// FAILED says so too, and never as a zero. The status must tell the two apart.
func TestGroupBudgetStatusFailuresAreNotZero(t *testing.T) {
	t.Run("a directory read that fails answers 500", func(t *testing.T) {
		m, st, tenant, _ := newFin(t)
		u := createCanonicalUser(t, st, "member").ID
		group := createUserGroup(t, st, tenant, "contractors", u)
		id := createBudget(t, st, tenant, "contractors", budgetSpec{
			Dimension: "user_group", Key: group.ID.String(), Period: "monthly", LimitMicroUSD: 1000,
		})
		m.UseData(failingAuthData{api.NewModuleData(st)})
		if rec := getBudgetStatus(m, st, tenant, id.String()); rec.Code != http.StatusInternalServerError {
			t.Fatalf("status = %d: %s, want 500 (a failed read is not a zero)", rec.Code, rec.Body.String())
		}
	})
}

// Deleting an agent is an ordinary admin action and leaves its membership row behind (agent_group_members has
// no foreign key). The group is then counted over the members that are left: a floor, flagged truncated, that
// neither fails the status read nor the Business recommendations loop that calls budgetStatus for every budget
// (module-overlay/finops/analytics_enterprise.go), and that CheckBudget and the reserve path enforce on instead
// of failing open (or, for a fail_closed budget, denying every request of the group).
func TestAgentGroupWithADeletedMemberIsCountedOverTheOthers(t *testing.T) {
	for _, failClosed := range []bool{false, true} {
		name := "a fail-open budget"
		if failClosed {
			name = "a fail_closed budget"
		}
		t.Run(name, func(t *testing.T) {
			m, st, tenant, _ := newFin(t)
			now := m.clock.Now().Time()
			a1 := createAgent(t, st, tenant, "agent-a", "agent-a", "")
			a2 := createAgent(t, st, tenant, "agent-b", "agent-b", "")
			createSession(t, st, tenant, "sess-a", a1)
			group := createAgentGroup(t, st, tenant, "payments-bots", a1, a2)
			id := createBudget(t, st, tenant, "bots", budgetSpec{
				Dimension: "agent_group", Key: group.Slug, Period: "monthly", LimitMicroUSD: 500, Action: "block", FailClosed: failClosed,
			})
			m.ingest(t, tenant, mkCost("anthropic", "claude-opus-4-8", "sess-a", 10, 5, 600, now))
			if err := st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
				return sc.Agents().Delete(context.Background(), a2)
			}); err != nil {
				t.Fatal(err)
			}

			logged := captureLog(m)
			status := budgetStatusOverHTTP(t, m, st, tenant, id)
			if status.SpendMicroUSD != 600 || !status.Truncated || !status.Over {
				t.Errorf("spend=%d truncated=%v over=%v, want the surviving member's 600, truncated and over", status.SpendMicroUSD, status.Truncated, status.Over)
			}
			if got := logged.String(); !strings.Contains(got, "level=WARN") || !strings.Contains(got, "agent_group=payments-bots") || !strings.Contains(got, "agent="+a2.String()) {
				t.Errorf("the status read must warn which agent of which group it left out; log = %q", got)
			}
			statusLog := logged.Len()

			// The entry point the Business overlay calls for every budget: one dangling member must not
			// fail it, or one deleted agent takes the whole recommendations answer down.
			var viaOverlay budgetStatusDTO
			if err := st.View(context.Background(), tenant, func(sc store.Scope) error {
				p, err := sc.Policies().Get(context.Background(), id)
				if err != nil {
					return err
				}
				viaOverlay, err = budgetStatus(context.Background(), sc, p, now)
				return err
			}); err != nil {
				t.Fatalf("budgetStatus failed for a group with a deleted member: %v", err)
			}
			if viaOverlay.SpendMicroUSD != 600 {
				t.Errorf("budgetStatus spend = %d, want 600", viaOverlay.SpendMicroUSD)
			}

			dims := SpendDims{AgentRef: "agent-a"}
			chk, err := m.CheckBudget(context.Background(), tenant, dims)
			if err != nil {
				t.Fatal(err)
			}
			if chk.Allowed || chk.SpendMicroUSD != status.SpendMicroUSD {
				t.Errorf("CheckBudget = %+v, want a block on the same 600 the status shows", chk)
			}
			res, err := m.ReserveBudget(context.Background(), tenant, dims, 1)
			if err != nil {
				t.Fatal(err)
			}
			if res.Allowed || res.SpendMicroUSD != status.SpendMicroUSD {
				t.Errorf("ReserveBudget = %+v, want a refusal on the same 600 the status shows", res)
			}
			if logged.Len() != statusLog {
				t.Errorf("a request must not log (CheckBudget and the reserve path run per request); log grew: %q", logged.String()[statusLog:])
			}
		})
	}
}

// What a group budget cannot resolve stays a zero and truncated, never an error: a key that
// is no group id, a group of another tenant, an agent group that does not exist.
func TestGroupBudgetStatusUnresolvedStaysUnknown(t *testing.T) {
	m, st, tenant, _ := newFin(t)
	now := m.clock.Now().Time()
	u := createCanonicalUser(t, st, "member").ID

	var other model.TenantID
	if err := st.System(context.Background(), func(sys store.SystemScope) error {
		org, err := sys.CreateOrg(context.Background(), model.Org{Name: "other", Slug: "other-" + uniqueSlugSuffix(t), Status: model.StatusActive})
		other = org.TenantID
		return err
	}); err != nil {
		t.Fatal(err)
	}
	theirs := createUserGroup(t, st, other, "theirs", u)
	s := mkCost("anthropic", "claude-opus-4-8", "", 10, 5, 700, now)
	s.Actor = u.String()
	m.ingest(t, tenant, s)

	for name, spec := range map[string]budgetSpec{
		"a user_group key that is no id":        {Dimension: "user_group", Key: "contractors"},
		"a group of another tenant":             {Dimension: "user_group", Key: theirs.ID.String()},
		"an agent_group that does not exist":    {Dimension: "agent_group", Key: "nobody"},
		"an agent_group id that does not exist": {Dimension: "agent_group", Key: model.NewID().String()},
		"a user_group id that does not exist":   {Dimension: "user_group", Key: model.NewID().String()},
	} {
		t.Run(name, func(t *testing.T) {
			spec.Period, spec.LimitMicroUSD = "monthly", 1000
			id := createBudget(t, st, tenant, name, spec)
			logged := captureLog(m)
			status := budgetStatusOverHTTP(t, m, st, tenant, id)
			if status.SpendMicroUSD != 0 || !status.Truncated {
				t.Errorf("spend=%d truncated=%v, want 0 and truncated (unresolved, not an error, not another tenant's spend)", status.SpendMicroUSD, status.Truncated)
			}
			if got := logged.String(); !strings.Contains(got, id.String()) || !strings.Contains(got, "not established") {
				t.Errorf("a zero that means \"not established\" must be logged with its budget; log = %q", got)
			}
		})
	}
}

// A group at its limit is over it.
func TestGroupBudgetStatusAtTheLimit(t *testing.T) {
	m, st, tenant, _ := newFin(t)
	now := m.clock.Now().Time()
	u := createCanonicalUser(t, st, "member").ID
	group := createUserGroup(t, st, tenant, "contractors", u)
	id := createBudget(t, st, tenant, "contractors", budgetSpec{
		Dimension: "user_group", Key: group.ID.String(), Period: "monthly", LimitMicroUSD: 1000,
	})
	spend := func(cost int64, at time.Time) {
		s := mkCost("anthropic", "claude-opus-4-8", "", 10, 5, cost, at)
		s.Actor = u.String()
		m.ingest(t, tenant, s)
	}
	spend(999, now)
	if status := budgetStatusOverHTTP(t, m, st, tenant, id); status.Over || status.SpendMicroUSD != 999 {
		t.Fatalf("at 999 of 1000: spend=%d over=%v, want 999 and not over", status.SpendMicroUSD, status.Over)
	}
	spend(1, now.Add(time.Minute))
	if status := budgetStatusOverHTTP(t, m, st, tenant, id); !status.Over || status.SpendMicroUSD != 1000 {
		t.Errorf("at 1000 of 1000: spend=%d over=%v, want 1000 and over", status.SpendMicroUSD, status.Over)
	}
}

// The handler's own answers around the read it was split into two scopes for.
func TestBudgetStatusHandlerRefusals(t *testing.T) {
	m, st, tenant, _ := newFin(t)
	// A stored policy that is not a budget.
	var notBudget model.ID
	if err := st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		p, err := sc.Policies().Create(context.Background(), model.Policy{Name: "not-a-budget", Kind: policyKindSpendLimit, Enabled: true})
		notBudget = p.ID
		return err
	}); err != nil {
		t.Fatal(err)
	}
	for name, tc := range map[string]struct {
		id   string
		want int
	}{
		"no id":                         {id: "00000000-0000-0000-0000-000000000000", want: http.StatusBadRequest},
		"an id nobody has":              {id: model.NewID().String(), want: http.StatusNotFound},
		"a policy that is not a budget": {id: notBudget.String(), want: http.StatusNotFound},
	} {
		t.Run(name, func(t *testing.T) {
			if rec := getBudgetStatus(m, st, tenant, tc.id); rec.Code != tc.want {
				t.Errorf("status = %d: %s, want %d", rec.Code, rec.Body.String(), tc.want)
			}
		})
	}
}
