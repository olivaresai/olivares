// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package finops

import (
	"context"
	"testing"
	"time"

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

func TestBudgetStatusConsumptionAndProjection(t *testing.T) {
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
	// Run-rate projection extrapolates above the spend-so-far (more month remains).
	if statusDTO.ProjectedMicroUSD <= statusDTO.SpendMicroUSD {
		t.Errorf("projected %d should exceed spend %d at one-third of the month", statusDTO.ProjectedMicroUSD, statusDTO.SpendMicroUSD)
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
