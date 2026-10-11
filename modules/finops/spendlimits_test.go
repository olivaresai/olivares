// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package finops

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

type spendLimitClock struct{ at time.Time }

func (c spendLimitClock) Now() model.Timestamp { return model.NewTimestamp(c.at) }

func cents(v string) *string { return &v }

func userLimit(user, amount, period string) SpendLimitSpec {
	return SpendLimitSpec{Scope: SpendLimitScope{Type: "user", UserID: user}, Amount: cents(amount), Period: period}
}

func TestSpendLimitEffectiveResolutionAndPeriods(t *testing.T) {
	m, st, tenant, _ := newFin(t)
	m.UseStanding(auth.NewAuthenticator(st, nil))
	now := time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)
	m.clock = spendLimitClock{at: now}
	u1 := createCanonicalUser(t, st, "spend limit group member one").ID
	u2 := createCanonicalUser(t, st, "spend limit group member two").ID
	g1 := createUserGroup(t, st, tenant, "g1", u1, u2)
	g2 := createUserGroup(t, st, tenant, "g2", u2)
	ctx := context.Background()
	upsert := func(spec SpendLimitSpec) {
		t.Helper()
		if _, _, err := seedStoredSpendLimit(m, ctx, tenant, spec, "user:admin"); err != nil {
			t.Fatal(err)
		}
	}
	upsert(SpendLimitSpec{Scope: SpendLimitScope{Type: "organization"}, Amount: cents("1000"), Period: "monthly"})
	upsert(SpendLimitSpec{Scope: SpendLimitScope{Type: "rbac_group", RBACGroupID: g1.ID.String()}, Amount: cents("300"), Period: "monthly"})
	upsert(SpendLimitSpec{Scope: SpendLimitScope{Type: "rbac_group", RBACGroupID: g2.ID.String()}, Amount: cents("200"), Period: "monthly"})
	upsert(userLimit("user:"+u1.String(), "500", "monthly"))
	upsert(SpendLimitSpec{Scope: SpendLimitScope{Type: "user", UserID: "user:unlimited"}, Amount: nil, Period: "monthly"})
	upsert(SpendLimitSpec{Scope: SpendLimitScope{Type: "organization"}, Amount: cents("0"), Period: "daily"})

	cost := mkCost("anthropic", "claude-opus-4-8", "", 1, 1, 314025000, now)
	cost.Actor = "user:" + u1.String()
	m.ingest(t, tenant, cost)
	result, err := m.SpendLimitEffective(ctx, tenant, SpendLimitEffectiveOptions{
		UserIDs: []string{"user:" + u1.String(), "user:" + u2.String(), "user:outside", "user:unlimited"},
		Periods: []string{"monthly"}, Limit: 20,
	})
	if err != nil || len(result.Data) != 4 {
		t.Fatalf("effective rows=%+v err=%v", result, err)
	}
	byActor := map[string]SpendLimitEffectiveRow{}
	for _, row := range result.Data {
		byActor[row.Actor.UserID] = row
	}
	if got := byActor["user:"+u1.String()]; got.Source == nil || got.Source.Type != "user" || got.Amount == nil || *got.Amount != "500" || got.PeriodToDateSpend != "31402.5" {
		t.Fatalf("user override = %+v", got)
	}
	if got := byActor["user:"+u2.String()]; got.Source == nil || got.Source.Type != "rbac_group" || got.Source.RBACGroupID != g2.ID.String() || got.Amount == nil || *got.Amount != "200" {
		t.Fatalf("group min = %+v", got)
	}
	if got := byActor["user:outside"]; got.Source == nil || got.Source.Type != "organization" || got.Amount == nil || *got.Amount != "1000" {
		t.Fatalf("org fallback = %+v", got)
	}
	if got := byActor["user:unlimited"]; got.Source == nil || got.Source.Type != "user" || got.Amount != nil {
		t.Fatalf("unlimited override = %+v", got)
	}
	check, err := m.CheckSpendLimit(ctx, tenant, "user:outside", nil)
	if err != nil || check.Allowed || check.Period != "daily" {
		t.Fatalf("zero daily cap = %+v err=%v", check, err)
	}
}

func TestCheckSpendLimitUsesCallerGroupsAndFailsOpen(t *testing.T) {
	m, _, tenant, _ := newFin(t)
	now := time.Now().UTC()
	m.clock = spendLimitClock{at: now}
	ctx := context.Background()
	groupID := model.NewID().String()
	if _, _, err := seedStoredSpendLimit(m, ctx, tenant, SpendLimitSpec{Scope: SpendLimitScope{Type: "rbac_group", RBACGroupID: groupID}, Amount: cents("1"), Period: "weekly"}, "admin"); err != nil {
		t.Fatal(err)
	}
	cost := mkCost("anthropic", "claude-opus-4-8", "", 1, 1, 10000, now)
	cost.Actor = "user:u1"
	m.ingest(t, tenant, cost)
	if chk, err := m.CheckSpendLimit(ctx, tenant, "user:u1", nil); err != nil || !chk.Allowed {
		t.Fatalf("without caller group = %+v err=%v", chk, err)
	}
	if chk, err := m.CheckSpendLimit(ctx, tenant, "user:u1", []string{groupID}); err != nil || chk.Allowed {
		t.Fatalf("with caller group = %+v err=%v", chk, err)
	}
	m.data = failingSpendLimitData{}
	if chk, err := m.CheckSpendLimit(ctx, tenant, "user:u1", []string{groupID}); err == nil || !chk.Allowed {
		t.Fatalf("read failure must return allow+error: %+v err=%v", chk, err)
	}
}

func TestCheckSpendLimitTruncatedAggregateFailsClosed(t *testing.T) {
	for _, tc := range []struct {
		name      string
		truncated bool
		wantAllow bool
	}{
		{name: "complete aggregate below limit allows", wantAllow: true},
		{name: "truncated aggregate below limit denies", truncated: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, _, tenant, _ := newFin(t)
			ctx := context.Background()
			if _, _, err := seedStoredSpendLimit(m, ctx, tenant, userLimit("user:u1", "1", "monthly"), "admin"); err != nil {
				t.Fatal(err)
			}
			forceAggregateResult(m, 1, tc.truncated)

			chk, err := m.CheckSpendLimit(ctx, tenant, "user:u1", nil)
			if err != nil {
				t.Fatal(err)
			}
			if chk.Allowed != tc.wantAllow {
				t.Fatalf("CheckSpendLimit() = %+v, want allowed=%v", chk, tc.wantAllow)
			}
			if tc.truncated && chk.SpendMicroUSD >= chk.LimitMicroUSD {
				t.Fatalf("test fixture must deny because of truncation while observed spend is below limit: %+v", chk)
			}
		})
	}
}

// A concurrent-insert race on Postgres can leave duplicate rows for one logical
// (scope, period) key (no unique constraint is possible on the JSON spec). Under
// duplicates the MOST RESTRICTIVE cap must govern — never a stale looser one —
// and the next upsert must heal back to a single row.

// Enforcement matches group caps against the S256 nested-group closure
// (principal.GroupsIn carries ancestor groups), so the effective view must
// resolve the same population: a member of a CHILD group nested under a capped
// PARENT shows the parent-group cap, not the org fallback.
func TestSpendLimitEffectiveSeesNestedGroupMembers(t *testing.T) {
	m, st, tenant, _ := newFin(t)
	now := time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)
	m.clock = spendLimitClock{at: now}
	ctx := context.Background()
	nested := createCanonicalUser(t, st, "nested group member").ID
	parent := createUserGroup(t, st, tenant, "parent")
	child := createUserGroup(t, st, tenant, "child", nested)
	if err := st.AuthMutate(ctx, func(as store.AuthScope) error {
		child.ParentGroupID = parent.ID
		_, err := as.Groups().Update(ctx, child)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := seedStoredSpendLimit(m, ctx, tenant, SpendLimitSpec{Scope: SpendLimitScope{Type: "rbac_group", RBACGroupID: parent.ID.String()}, Amount: cents("400"), Period: "monthly"}, "user:admin"); err != nil {
		t.Fatal(err)
	}
	result, err := m.SpendLimitEffective(ctx, tenant, SpendLimitEffectiveOptions{
		UserIDs: []string{"user:" + nested.String()}, Periods: []string{"monthly"}, Limit: 10,
	})
	if err != nil || len(result.Data) != 1 {
		t.Fatalf("effective = %+v err=%v", result, err)
	}
	row := result.Data[0]
	if row.Source == nil || row.Source.Type != "rbac_group" || row.Source.RBACGroupID != parent.ID.String() || row.Amount == nil || *row.Amount != "400" {
		t.Fatalf("nested member row = %+v (want the parent-group cap)", row)
	}
}

type failingSpendLimitData struct{}

func (failingSpendLimitData) View(context.Context, model.TenantID, func(store.Scope) error) error {
	return errors.New("store unavailable")
}
func (failingSpendLimitData) Mutate(context.Context, model.TenantID, func(store.Scope) error) error {
	return errors.New("store unavailable")
}

var _ api.ModuleData = failingSpendLimitData{}
