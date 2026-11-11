// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package finops

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

func TestBudgetCapEmitsHardCapSignal(t *testing.T) {
	m, st, tenant, fh := newFin(t)
	createBudget(t, st, tenant, "blocking-cap", budgetSpec{
		Dimension: "global", Period: "monthly", LimitMicroUSD: 1000,
		Thresholds: []float64{1.0}, Action: "block",
	})
	// Cross the limit.
	m.ingest(t, tenant, mkCost("anthropic", "claude-opus-4-8", "", 10, 5, 1200, baseTime))

	findings := fh.findings()
	if len(findings) != 1 {
		t.Fatalf("emitted findings = %d, want 1", len(findings))
	}
	f := findings[0]
	if f.Kind != "finops_budget_cap" {
		t.Errorf("kind = %q, want finops_budget_cap (hard-cap signal)", f.Kind)
	}
	if f.Severity != sdkmodel.SeverityCritical {
		t.Errorf("block cap severity = %q, want critical", f.Severity)
	}
}

func TestCheckBudgetEnforcesBlockButNotAlert(t *testing.T) {
	m, st, tenant, _ := newFin(t)
	// CheckBudget evaluates the period containing the module clock's "now"; ingest at
	// that same instant so the sample lands in the period the check aggregates.
	now := m.clock.Now().Time()
	// An alert-only budget over its limit must NOT deny (showback).
	createBudget(t, st, tenant, "alert-only", budgetSpec{
		Dimension: "model", Key: "claude-opus-4-8", Period: "monthly", LimitMicroUSD: 100, Action: "alert",
	})
	m.ingest(t, tenant, mkCost("anthropic", "claude-opus-4-8", "", 10, 5, 500, now))

	chk, err := m.CheckBudget(context.Background(), tenant, SpendDims{ModelRef: "claude-opus-4-8"})
	if err != nil {
		t.Fatal(err)
	}
	if !chk.Allowed {
		t.Fatalf("alert-only budget must not deny: %+v", chk)
	}

	// Add a blocking budget on the same model, already over limit → deny.
	createBudget(t, st, tenant, "hard-block", budgetSpec{
		Dimension: "model", Key: "claude-opus-4-8", Period: "monthly", LimitMicroUSD: 100, Action: "block",
	})
	chk, err = m.CheckBudget(context.Background(), tenant, SpendDims{ModelRef: "claude-opus-4-8"})
	if err != nil {
		t.Fatal(err)
	}
	if chk.Allowed || chk.Action != "block" {
		t.Fatalf("block budget over limit must deny: %+v", chk)
	}
	// A request the budget does not scope (different model) is unaffected.
	other, err := m.CheckBudget(context.Background(), tenant, SpendDims{ModelRef: "claude-haiku-4-5"})
	if err != nil {
		t.Fatal(err)
	}
	if !other.Allowed {
		t.Fatalf("unscoped request must be allowed: %+v", other)
	}
}

func TestCheckBudgetReservedCapacityCountsTowardLimit(t *testing.T) {
	m, st, tenant, _ := newFin(t)
	// Limit 1000, reserved 950, actual spend 100 → effective 1050 ≥ 1000 → throttle.
	now := m.clock.Now().Time()
	createBudget(t, st, tenant, "reserved", budgetSpec{
		Dimension: "global", Period: "monthly", LimitMicroUSD: 1000,
		Action: "throttle", ReservedMicroUSD: 950,
	})
	m.ingest(t, tenant, mkCost("anthropic", "claude-opus-4-8", "", 10, 5, 100, now))

	chk, err := m.CheckBudget(context.Background(), tenant, SpendDims{ModelRef: "claude-opus-4-8"})
	if err != nil {
		t.Fatal(err)
	}
	if chk.Allowed || chk.Action != "throttle" {
		t.Fatalf("reserved capacity must count toward the limit: %+v", chk)
	}

	// budgetStatus must agree with CheckBudget: effective (100 + 950) ≥ 1000 → Over,
	// remaining negative, consumed ≥ 100% — not "10% consumed, not over".
	var bid model.ID
	if err := st.View(context.Background(), tenant, func(sc store.Scope) error {
		ps, _, e := sc.Policies().List(context.Background(), model.Query{Filters: []model.Filter{eq("kind", policyKindBudget)}, Limit: 1})
		if e != nil {
			return e
		}
		bid = ps[0].ID
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	var status budgetStatusDTO
	if err := st.View(context.Background(), tenant, func(sc store.Scope) error {
		p, e := sc.Policies().Get(context.Background(), bid)
		if e != nil {
			return e
		}
		status, e = budgetStatus(context.Background(), sc, p, now)
		return e
	}); err != nil {
		t.Fatal(err)
	}
	if !status.Over || status.RemainingMicroUSD >= 0 || status.ConsumedPct < 100 {
		t.Errorf("status must reflect reserved capacity: over=%v remaining=%d consumed=%d%% (want over/negative/≥100)",
			status.Over, status.RemainingMicroUSD, status.ConsumedPct)
	}
	if status.SpendMicroUSD != 100 {
		t.Errorf("SpendMicroUSD = %d, want 100 (raw actual spend, not effective)", status.SpendMicroUSD)
	}
}

func TestCheckBudgetTruncatedAggregateFailsClosed(t *testing.T) {
	for _, tc := range []struct {
		name      string
		truncated bool
		wantAllow bool
	}{
		{name: "complete aggregate below limit allows", wantAllow: true},
		{name: "truncated aggregate below limit denies", truncated: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, st, tenant, _ := newFin(t)
			createBudget(t, st, tenant, "scan-cap", budgetSpec{
				Dimension: "global", Period: "monthly", LimitMicroUSD: 2000, Action: "block",
			})
			forceAggregateResult(m, 1, tc.truncated)

			chk, err := m.CheckBudget(context.Background(), tenant, SpendDims{})
			if err != nil {
				t.Fatal(err)
			}
			if chk.Allowed != tc.wantAllow {
				t.Fatalf("CheckBudget() = %+v, want allowed=%v", chk, tc.wantAllow)
			}
			if tc.truncated && (chk.Action != "block" || chk.SpendMicroUSD >= chk.LimitMicroUSD || chk.Reason != "budget aggregate truncated at scan cap; enforced fail-closed") {
				t.Fatalf("truncated aggregate must deny below the observed limit: %+v", chk)
			}
		})
	}
}

func TestCheckBudgetAgentGroupFanOut(t *testing.T) {
	m, st, tenant, _ := newFin(t)
	now := m.clock.Now().Time()
	a1 := createAgent(t, st, tenant, "agent-a", "agent-a", "")
	a2 := createAgent(t, st, tenant, "agent-b", "agent-b", "")
	outside := createAgent(t, st, tenant, "agent-out", "agent-out", "")
	createSession(t, st, tenant, "sess-a", a1)
	createSession(t, st, tenant, "sess-b", a2)
	createSession(t, st, tenant, "sess-out", outside)
	group := createAgentGroup(t, st, tenant, "payments-bots", a1, a2)

	createBudget(t, st, tenant, "agent-group-cap", budgetSpec{
		Dimension: "agent_group", Key: group.Slug, Period: "monthly", LimitMicroUSD: 1000, Action: "block",
	})
	m.ingest(t, tenant, mkCost("anthropic", "claude-opus-4-8", "sess-a", 10, 5, 600, now))
	m.ingest(t, tenant, mkCost("anthropic", "claude-opus-4-8", "sess-b", 10, 5, 399, now.Add(time.Minute)))
	m.ingest(t, tenant, mkCost("anthropic", "claude-opus-4-8", "sess-out", 10, 5, 5000, now.Add(2*time.Minute)))

	chk, err := m.CheckBudget(context.Background(), tenant, SpendDims{AgentRef: "agent-a"})
	if err != nil {
		t.Fatal(err)
	}
	if !chk.Allowed {
		t.Fatalf("agent_group spend just under limit must allow: %+v", chk)
	}

	m.ingest(t, tenant, mkCost("anthropic", "claude-opus-4-8", "sess-a", 10, 5, 2, now.Add(3*time.Minute)))
	chk, err = m.CheckBudget(context.Background(), tenant, SpendDims{AgentRef: "agent-a"})
	if err != nil {
		t.Fatal(err)
	}
	if chk.Allowed || chk.Action != "block" || chk.SpendMicroUSD != 1001 {
		t.Fatalf("agent_group spend just over limit must block at 1001: %+v", chk)
	}

	other, err := m.CheckBudget(context.Background(), tenant, SpendDims{AgentRef: "agent-out"})
	if err != nil {
		t.Fatal(err)
	}
	if !other.Allowed {
		t.Fatalf("agent outside the group must not match the group budget: %+v", other)
	}
}

func TestCheckBudgetUserGroupFanOut(t *testing.T) {
	m, st, tenant, _ := newFin(t)
	now := m.clock.Now().Time()
	u1 := createCanonicalUser(t, st, "budget group member one").ID
	u2 := createCanonicalUser(t, st, "budget group member two").ID
	group := createUserGroup(t, st, tenant, "engineering", u1, u2)
	createBudget(t, st, tenant, "user-group-cap", budgetSpec{
		Dimension: "user_group", Key: group.ID.String(), Period: "monthly", LimitMicroUSD: 1000, Action: "block",
	})
	c1 := mkCost("anthropic", "claude-opus-4-8", "", 10, 5, 700, now)
	c1.Actor = u1.String()
	m.ingest(t, tenant, c1)
	c2 := mkCost("anthropic", "claude-opus-4-8", "", 10, 5, 450, now.Add(time.Minute))
	c2.Actor = u2.String()
	m.ingest(t, tenant, c2)

	chk, err := m.CheckBudget(context.Background(), tenant, SpendDims{UserGroupRefs: []string{group.ID.String()}})
	if err != nil {
		t.Fatal(err)
	}
	if chk.Allowed || chk.Action != "block" || chk.SpendMicroUSD != 1150 {
		t.Fatalf("user_group spend over cap must block via member fan-out: %+v", chk)
	}

	empty, err := m.CheckBudget(context.Background(), tenant, SpendDims{})
	if err != nil {
		t.Fatal(err)
	}
	if !empty.Allowed {
		t.Fatalf("empty UserGroupRefs must not match a user_group budget: %+v", empty)
	}
}

func TestCheckBudgetGroupFailClosed(t *testing.T) {
	t.Run("fail closed denies", func(t *testing.T) {
		m, st, tenant, _ := newFin(t)
		missing := model.NewID().String()
		createBudget(t, st, tenant, "broken-group-cap", budgetSpec{
			Dimension: "user_group", Key: missing, Period: "monthly", LimitMicroUSD: 1000,
			Action: "block", FailClosed: true,
		})
		chk, err := m.CheckBudget(context.Background(), tenant, SpendDims{UserGroupRefs: []string{missing}})
		if err != nil {
			t.Fatal(err)
		}
		if chk.Allowed || chk.Action != "block" || chk.Reason != "group budget check failed (fail-closed)" {
			t.Fatalf("fail_closed group lookup failure must deny: %+v", chk)
		}
	})

	t.Run("default fail open propagates error", func(t *testing.T) {
		m, st, tenant, _ := newFin(t)
		missing := model.NewID().String()
		createBudget(t, st, tenant, "broken-group-cap", budgetSpec{
			Dimension: "user_group", Key: missing, Period: "monthly", LimitMicroUSD: 1000, Action: "block",
		})
		chk, err := m.CheckBudget(context.Background(), tenant, SpendDims{UserGroupRefs: []string{missing}})
		if err == nil {
			t.Fatal("expected the group lookup error to propagate in fail-open mode")
		}
		if !chk.Allowed {
			t.Fatalf("default group lookup failure must fail open: %+v", chk)
		}
	})
}

func TestPeriodStart(t *testing.T) {
	at := time.Date(2026, 6, 10, 15, 30, 0, 0, time.UTC) // a Wednesday
	cases := map[string]time.Time{
		"daily":   time.Date(2026, 6, 10, 0, 0, 0, 0, time.UTC),
		"weekly":  time.Date(2026, 6, 8, 0, 0, 0, 0, time.UTC), // Monday
		"monthly": time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC),
	}
	for period, want := range cases {
		got, ok := periodStart(period, at)
		if !ok || !got.Equal(want) {
			t.Errorf("periodStart(%s) = %v (ok=%v), want %v", period, got, ok, want)
		}
	}
	if _, ok := periodStart("total", at); ok {
		t.Error("total period must have no lower bound")
	}
}

func TestBudgetThresholdCrossingRecordsAndEmits(t *testing.T) {
	m, st, tenant, fh := newFin(t)
	// Global monthly budget of 1000 µUSD, alerting at 50% and 100%.
	createBudget(t, st, tenant, "monthly-cap", budgetSpec{
		Dimension: "global", Period: "monthly", LimitMicroUSD: 1000, Thresholds: []float64{0.5, 1.0},
	})

	// First sample: 600 µUSD → crosses 50% (>=500), not 100%.
	m.ingest(t, tenant, mkCost("anthropic", "claude-opus-4-8", "", 10, 5, 600, baseTime))
	// Second sample: +500 → total 1100 → crosses 100% (50% already alerted).
	m.ingest(t, tenant, mkCost("anthropic", "claude-haiku-4-5", "", 10, 5, 500, baseTime.Add(time.Hour)))
	// Re-deliver both: deduped, so no new alerts.
	m.ingest(t, tenant, mkCost("anthropic", "claude-opus-4-8", "", 10, 5, 600, baseTime))
	m.ingest(t, tenant, mkCost("anthropic", "claude-haiku-4-5", "", 10, 5, 500, baseTime.Add(time.Hour)))

	// Two alert rows recorded (50% and 100%), no duplicates.
	alerts := listAlerts(t, st, tenant)
	if len(alerts) != 2 {
		t.Fatalf("alert rows = %d, want 2", len(alerts))
	}
	// Two findings emitted on the bus, the 100% one is High severity.
	findings := fh.findings()
	if len(findings) != 2 {
		t.Fatalf("emitted findings = %d, want 2", len(findings))
	}
	sawHigh := false
	for _, f := range findings {
		if f.Kind != "finops_budget" || f.SubjectKind != "budget" {
			t.Errorf("finding shape = %+v", f)
		}
		if f.Severity == sdkmodel.SeverityHigh {
			sawHigh = true
		}
	}
	if !sawHigh {
		t.Error("100% crossing must emit a High-severity finding")
	}
}

func TestBudgetDimensionScoping(t *testing.T) {
	m, st, tenant, _ := newFin(t)
	// A budget scoped to a single model.
	createBudget(t, st, tenant, "opus-cap", budgetSpec{
		Dimension: "model", Key: "claude-opus-4-8", Period: "monthly", LimitMicroUSD: 1000, Thresholds: []float64{0.5},
	})
	// Spend on opus (counts) and on gemini (does not).
	m.ingest(t, tenant, mkCost("anthropic", "claude-opus-4-8", "", 10, 5, 400, baseTime))
	m.ingest(t, tenant, mkCost("google", "gemini-1.5-flash", "", 10, 5, 900, baseTime.Add(time.Minute)))

	// 400 of opus is below the 50% (500) threshold → no alert yet.
	if got := len(listAlerts(t, st, tenant)); got != 0 {
		t.Fatalf("alerts = %d, want 0 (opus under threshold; gemini out of scope)", got)
	}
	// One more opus sample crosses it.
	m.ingest(t, tenant, mkCost("anthropic", "claude-opus-4-8", "", 10, 5, 200, baseTime.Add(2*time.Minute)))
	if got := len(listAlerts(t, st, tenant)); got != 1 {
		t.Errorf("alerts = %d, want 1 (opus now at 600/1000)", got)
	}
}

func TestBudgetStatusConsumption(t *testing.T) {
	m, st, tenant, _ := newFin(t)
	id := createBudget(t, st, tenant, "cap", budgetSpec{
		Dimension: "global", Period: "monthly", LimitMicroUSD: 1000,
	})
	m.ingest(t, tenant, mkCost("anthropic", "claude-opus-4-8", "", 10, 5, 300, baseTime))

	// Evaluate at baseTime (10th of a 30-day month → ~1/3 elapsed).
	var statusDTO budgetStatusDTO
	if err := st.View(context.Background(), tenant, func(sc store.Scope) error {
		p, err := sc.Policies().Get(context.Background(), id)
		if err != nil {
			return err
		}
		statusDTO, err = budgetStatus(context.Background(), sc, p, baseTime)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if statusDTO.SpendMicroUSD != 300 || statusDTO.ConsumedPct != 30 {
		t.Errorf("status spend = %d (%d%%), want 300 (30%%)", statusDTO.SpendMicroUSD, statusDTO.ConsumedPct)
	}
	if statusDTO.Over {
		t.Error("budget should not be over at 30%")
	}
}

func TestBudgetEvalBoundsToPeriod(t *testing.T) {
	m, st, tenant, _ := newFin(t)
	// A DAILY budget of 1000. Spend lands on two different days; a late/out-of-order
	// day-1 sample must be evaluated against day 1's window only, never summed with
	// day-2 spend (the open-window bug would have fired a false breach).
	createBudget(t, st, tenant, "daily-cap", budgetSpec{
		Dimension: "global", Period: "daily", LimitMicroUSD: 1000, Thresholds: []float64{1.0},
	})
	day1 := time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC)
	day2 := time.Date(2026, 6, 11, 10, 0, 0, 0, time.UTC)

	// Day 2 spends 700 first; then a late Day 1 sample of 450 arrives.
	m.ingest(t, tenant, mkCost("anthropic", "claude-opus-4-8", "", 10, 5, 700, day2))
	m.ingest(t, tenant, mkCost("anthropic", "claude-opus-4-8", "", 10, 5, 450, day1))
	// Neither day is over 1000 on its own → no alert (the bug would sum 700+450).
	if got := len(listAlerts(t, st, tenant)); got != 0 {
		t.Fatalf("alerts = %d, want 0 (each day under its own limit)", got)
	}

	// A second Day 1 sample pushes Day 1 to 1050 → a real Day-1 breach.
	m.ingest(t, tenant, mkCost("anthropic", "claude-opus-4-8", "", 10, 5, 600, day1.Add(time.Hour)))
	alerts := listAlerts(t, st, tenant)
	if len(alerts) != 1 {
		t.Fatalf("alerts = %d, want 1 (Day 1 breach)", len(alerts))
	}
	// The recorded spend is Day 1 only (1050), not Day 1 + Day 2 (1750).
	if got := alerts[0].Int(colAlertSpend); got != 1050 {
		t.Errorf("alert spend = %d, want 1050 (period-bounded, not 1750)", got)
	}
}

// listAlerts returns the budget-alert rows in a tenant.
func listAlerts(t *testing.T, st store.Store, tenant model.TenantID) []model.Record {
	t.Helper()
	var out []model.Record
	if err := st.View(context.Background(), tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(budgetAlertKind)
		if err != nil {
			return err
		}
		recs, _, err := repo.List(context.Background(), model.Query{Limit: listCap})
		out = recs
		return err
	}); err != nil {
		t.Fatalf("listAlerts: %v", err)
	}
	return out
}

// TestCheckBudgetEnforcingBudgetBeyondFirstPageBlocks (D-04) reproduces the
// enforcement-path truncation in the pre-flight budget gate: a tenant with more than
// one page (listCap) of budgets whose single enforcing action=block budget sorts
// onto a LATER page. Before the keyset-drain fix, CheckBudget listed only the first
// page of budget policies and never even considered the block budget — so spend
// proceeded uncapped. The block budget counts static ReservedMicroUSD toward its
// limit, so it denies with zero settled spend (no cost ingestion needed).
func TestCheckBudgetEnforcingBudgetBeyondFirstPageBlocks(t *testing.T) {
	m, st, tenant, _ := newFin(t)

	// listCap alert-only filler budgets FIRST (earlier ids ⇒ page 1), then the single
	// enforcing block budget LAST (a later id ⇒ a later page). One transaction keeps
	// the setup fast while preserving monotonic UUIDv7 id ordering.
	if err := st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		for i := 0; i < listCap; i++ {
			if _, err := sc.Policies().Create(context.Background(), model.Policy{
				Name: "filler", Kind: policyKindBudget, Enabled: true,
				Spec: budgetSpec{Dimension: "global", Period: "monthly", LimitMicroUSD: 1_000_000, Action: "alert"}.toSpecMap(),
			}); err != nil {
				return err
			}
		}
		_, err := sc.Policies().Create(context.Background(), model.Policy{
			Name: "hard-block", Kind: policyKindBudget, Enabled: true,
			Spec: budgetSpec{Dimension: "global", Period: "monthly", LimitMicroUSD: 1000, ReservedMicroUSD: 1000, Action: "block"}.toSpecMap(),
		})
		return err
	}); err != nil {
		t.Fatal(err)
	}

	chk, err := m.CheckBudget(context.Background(), tenant, SpendDims{})
	if err != nil {
		t.Fatalf("CheckBudget: %v", err)
	}
	if chk.Allowed {
		t.Fatalf("an enforcing block budget on an unread page must deny; got %+v", chk)
	}
	if chk.Action != "block" {
		t.Fatalf("action = %q, want block; %+v", chk.Action, chk)
	}
}

// A member's spend is recorded under the actor form the engine writes, user:<id>, so the
// group's total must count it there as well as under a bare id.
func TestUserGroupBudgetCountsMemberSpendUnderPrincipalActor(t *testing.T) {
	m, st, tenant, _ := newFin(t)
	now := m.clock.Now().Time()
	u1 := createCanonicalUser(t, st, "member one").ID
	group := createUserGroup(t, st, tenant, "engineering", u1)
	createBudget(t, st, tenant, "group-cap", budgetSpec{
		Dimension: "user_group", Key: group.ID.String(), Period: "monthly", LimitMicroUSD: 1000, Action: "block",
	})
	c := mkCost("anthropic", "claude-opus-4-8", "", 10, 5, 1150, now)
	c.Actor = "user:" + u1.String()
	m.ingest(t, tenant, c)

	chk, err := m.CheckBudget(context.Background(), tenant, SpendDims{UserGroupRefs: []string{group.ID.String()}})
	if err != nil {
		t.Fatal(err)
	}
	if chk.Allowed || chk.SpendMicroUSD != 1150 {
		t.Fatalf("a member's spend under user:<id> must count against the group: %+v", chk)
	}
}

// raceStanding returns the standing of the accounts it is asked about and, on its
// first read only, runs between: the removal that completes after the writer read the
// standing and before it pinned it.
type raceStanding struct {
	auth.StandingReader
	between func()
}

func (r *raceStanding) Standing(ctx context.Context, tenant model.TenantID, users []model.ID) (map[model.ID]auth.Standing, error) {
	out, err := r.StandingReader.Standing(ctx, tenant, users)
	if r.between != nil {
		r.between()
		r.between = nil
	}
	return out, err
}

// A group budget never exempts an account whose removal from the organization
// completed: the write answers 409 subject_retired and stores nothing, whether the
// form named an account already retired or the removal finished between the
// writer's standing read and its retry, and re-admitting the account revives no
// exemption.
func TestUserGroupBudgetRefusesARetiredExemptActor(t *testing.T) {
	m, st, tenant, _ := newFin(t)
	ctx := context.Background()
	a := auth.NewAuthenticator(st, nil)
	actor := auth.Principal{Kind: auth.KindUser, UserID: model.NewID(), CredID: model.NewID(), Superadmin: true}
	member := func(t *testing.T, name string) model.ID {
		t.Helper()
		var id model.ID
		if err := st.AuthMutate(ctx, func(as store.AuthScope) error {
			u, err := as.Users().Create(ctx, model.User{
				Email: "finops-" + model.NewID().String() + "@example.test", DisplayName: name, Status: model.StatusActive,
				CredentialCustody: model.CustodyTenant, CustodyTenantID: tenant,
			})
			if err != nil {
				return err
			}
			id = u.ID
			_, err = as.Memberships().Create(ctx, model.Membership{UserID: u.ID, TargetTenantID: tenant, Role: auth.RoleViewer})
			return err
		}); err != nil {
			t.Fatalf("create member %s: %v", name, err)
		}
		return id
	}
	// retire removes the account from the tenant and completes the retirement, as the
	// retirement pump does once every module step ran clean.
	retire := func(t *testing.T, user model.ID) {
		t.Helper()
		if err := st.AuthMutate(ctx, func(as store.AuthScope) error {
			rec, err := a.OffboardFromTenant(ctx, as, actor, user, tenant, "test")
			if err != nil {
				return err
			}
			rec.RetirementState = model.RetirementRetired
			_, err = as.TenantExclusions().Update(ctx, rec)
			return err
		}); err != nil {
			t.Fatalf("retire %s: %v", user, err)
		}
	}
	write := func(user model.ID) error {
		spec := budgetSpec{
			Dimension: "user_group", Key: model.NewID().String(), Period: "monthly", LimitMicroUSD: 1000, Action: "block",
			ExemptActors: []string{"user:" + user.String()},
		}
		return m.fencedMutate(ctx, tenant, budgetSubjects(spec), func(sc store.Scope) error {
			_, err := sc.Policies().Create(ctx, model.Policy{Name: "group-cap", Kind: policyKindBudget, Enabled: true, Spec: spec.toSpecMap()})
			return err
		})
	}
	// naming reads the stored policies that name user, as the retirement step does.
	naming := func(t *testing.T, user model.ID) []string {
		t.Helper()
		var blocking []string
		if err := st.View(ctx, tenant, func(sc store.Scope) error {
			var err error
			blocking, _, err = policiesNaming(ctx, sc, user)
			return err
		}); err != nil {
			t.Fatalf("read policies naming %s: %v", user, err)
		}
		return blocking
	}
	refused := func(t *testing.T, user model.ID, err error) {
		t.Helper()
		if !errors.Is(err, auth.ErrSubjectRetired) {
			t.Fatalf("budget exempting the retired account = %v, want %v", err, auth.ErrSubjectRetired)
		}
		rec := httptest.NewRecorder()
		writeStoreError(rec, err)
		if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), `"subject_retired"`) {
			t.Fatalf("answered %d %s, want 409 subject_retired", rec.Code, rec.Body.String())
		}
		if got := naming(t, user); len(got) != 0 {
			t.Fatalf("stored policies naming the account = %v, want none", got)
		}
	}

	t.Run("an account already retired", func(t *testing.T) {
		user := member(t, "retired before the form")
		retire(t, user)
		m.UseStanding(a)
		refused(t, user, write(user))
	})

	t.Run("a removal that completes before the retry", func(t *testing.T) {
		user := member(t, "retired while the write waits")
		m.UseStanding(&raceStanding{StandingReader: a, between: func() { retire(t, user) }})
		refused(t, user, write(user))

		// The tenant re-admits the account it created: nothing it stored before revives.
		if _, err := a.GrantMembership(ctx, actor, user, tenant, auth.RoleViewer, ""); err != nil {
			t.Fatalf("re-admit: %v", err)
		}
		if got := naming(t, user); len(got) != 0 {
			t.Fatalf("policies naming the re-admitted account = %v, want none", got)
		}
	})
}

// An exception lets one named person continue at admission; everyone else in the group
// stays blocked.
func TestUserGroupBudgetExemptActorContinues(t *testing.T) {
	m, st, tenant, _ := newFin(t)
	now := m.clock.Now().Time()
	u1 := createCanonicalUser(t, st, "member one").ID
	u2 := createCanonicalUser(t, st, "member two").ID
	group := createUserGroup(t, st, tenant, "engineering", u1, u2)
	exempt, other := "user:"+u1.String(), "user:"+u2.String()
	m.UseStanding(auth.NewAuthenticator(st, nil))
	createFencedBudget(t, m, tenant, "group-cap", budgetSpec{
		Dimension: "user_group", Key: group.ID.String(), Period: "monthly", LimitMicroUSD: 1000, Action: "block",
		ExemptActors: []string{exempt},
	})
	c := mkCost("anthropic", "claude-opus-4-8", "", 10, 5, 1150, now)
	c.Actor = other
	m.ingest(t, tenant, c)
	groups := []string{group.ID.String()}
	ctx := context.Background()

	for _, tc := range []struct {
		actor string
		want  bool
	}{{exempt, true}, {other, false}} {
		res, err := m.Reserve(ctx, tenant, AdmissionRequest{Scope: AdmissionScopeSessionLaunch, Dims: SpendDims{UserGroupRefs: groups}, ActorRef: tc.actor, EstimateMicroUSD: 1, IdempotencyKey: t.Name() + tc.actor, Unreachable: UnreachableDeny})
		if err != nil || res.Allowed != tc.want {
			t.Fatalf("ReserveBudget(%s) = %+v, %v; want allowed=%v", tc.actor, res, err, tc.want)
		}
	}
	bad := budgetSpec{Dimension: "workspace", Key: "x", Period: "monthly", LimitMicroUSD: 1, ExemptActors: []string{exempt}}
	if msg := bad.validate(); msg == "" {
		t.Fatal("an exception on a budget that is not a group budget must be refused")
	}
	bad = budgetSpec{Dimension: "user_group", Key: "x", Period: "monthly", LimitMicroUSD: 1, ExemptActors: []string{"not-a-person"}}
	if msg := bad.validate(); msg == "" {
		t.Fatal("an exception that does not name a person must be refused")
	}
}
