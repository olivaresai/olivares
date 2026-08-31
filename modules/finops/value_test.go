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
)

// TestIdentityBudgetEnforcedViaAgentResolution proves a per-identity (NHI/SPIFFE)
// dollar budget DENIES at its cap, with the firm identity resolved on BOTH sides: the
// ingest stamps identity_ref from session→agent→IdentityID, and CheckBudget resolves
// the seam's AgentRef to the same firm identity — so the budget aggregates and enforces
// on the firm key without any seam change.
func TestIdentityBudgetEnforcedViaAgentResolution(t *testing.T) {
	m, st, tenant, _ := newFin(t)
	now := m.clock.Now().Time()
	const spiffe = "spiffe://acme/agent/billing"
	idID := createIdentity(t, st, tenant, spiffe, "workload_identity", "spiffe")
	createAgent(t, st, tenant, "billing-agent", "agent-ext-1", idID)
	createSession(t, st, tenant, "sess-1", agentIDOf(t, st, tenant, "agent-ext-1"))

	// Spend 1500 attributed to the session → stamped onto the firm identity.
	m.ingest(t, tenant, mkCost("anthropic", "claude-opus-4-8", "sess-1", 10, 5, 1500, now))

	// The read-model row carries the resolved firm identity.
	rows := costSampleRows(t, st, tenant)
	if len(rows) != 1 || rows[0].String(colIdentityRef) != spiffe {
		t.Fatalf("identity_ref = %q, want %q", firstIdentityRef(rows), spiffe)
	}
	if rows[0].String(colIdentityKind) != "workload_identity" || rows[0].String(colIdentitySource) != "spiffe" {
		t.Errorf("identity kind/source = %q/%q, want workload_identity/spiffe",
			rows[0].String(colIdentityKind), rows[0].String(colIdentitySource))
	}

	// A blocking budget on that firm identity, already over its 1000 cap.
	createBudget(t, st, tenant, "nhi-cap", budgetSpec{
		Dimension: "identity", Key: spiffe, Period: "monthly", LimitMicroUSD: 1000, Action: "block",
	})

	// The seam knows only the AgentRef; CheckBudget resolves it to the firm identity.
	chk, err := m.CheckBudget(context.Background(), tenant, SpendDims{AgentRef: "agent-ext-1"})
	if err != nil {
		t.Fatal(err)
	}
	if chk.Allowed || chk.Action != "block" {
		t.Fatalf("identity budget over cap must deny via agent resolution: %+v", chk)
	}

	// A request that resolves to no firm identity is unaffected by the identity budget.
	other, err := m.CheckBudget(context.Background(), tenant, SpendDims{AgentRef: "stranger"})
	if err != nil {
		t.Fatal(err)
	}
	if !other.Allowed {
		t.Fatalf("unresolved identity must not be denied by an identity budget: %+v", other)
	}
}

// TestIdentityBudgetThrottleViaAPIKey proves the firmer api-key path: a cost sample
// whose APIKeyRef IS a roster identity is attributed to it, and a throttling identity
// budget is enforced when the seam supplies the same APIKeyRef.
func TestIdentityBudgetThrottleViaAPIKey(t *testing.T) {
	m, st, tenant, _ := newFin(t)
	now := m.clock.Now().Time()
	const apikey = "apikey_live_123"
	createIdentity(t, st, tenant, apikey, "credential", "anthropic")

	c := mkCost("anthropic", "claude-opus-4-8", "", 10, 5, 800, now)
	c.APIKeyRef = apikey
	m.ingest(t, tenant, c)

	rows := costSampleRows(t, st, tenant)
	if len(rows) != 1 || rows[0].String(colIdentityRef) != apikey {
		t.Fatalf("api-key identity_ref = %q, want %q", firstIdentityRef(rows), apikey)
	}

	createBudget(t, st, tenant, "key-cap", budgetSpec{
		Dimension: "identity", Key: apikey, Period: "monthly", LimitMicroUSD: 500, Action: "throttle",
	})
	chk, err := m.CheckBudget(context.Background(), tenant, SpendDims{APIKeyRef: apikey})
	if err != nil {
		t.Fatal(err)
	}
	if chk.Allowed || chk.Action != "throttle" {
		t.Fatalf("identity budget (api-key) over cap must throttle: %+v", chk)
	}
}

// TestUnresolvedIdentityNeverFabricated proves a sample that resolves to no roster
// identity leaves identity_ref empty — honest, never guessed.
func TestUnresolvedIdentityNeverFabricated(t *testing.T) {
	m, st, tenant, _ := newFin(t)
	c := mkCost("anthropic", "claude-opus-4-8", "", 10, 5, 100, baseTime)
	c.APIKeyRef = "apikey_not_in_roster"
	c.Actor = "someone@example.com"
	m.ingest(t, tenant, c)
	rows := costSampleRows(t, st, tenant)
	if len(rows) != 1 || rows[0].String(colIdentityRef) != "" {
		t.Fatalf("identity_ref = %q, want empty (no roster match)", firstIdentityRef(rows))
	}
}

// TestCostPerOutcomeAndCancellationRisk proves the value join: cost-per-outcome,
// net value, satisfied counts, and the cancellation-risk signal (burn without
// successful outcomes).
func TestCostPerOutcomeAndCancellationRisk(t *testing.T) {
	m, st, tenant, _ := newFin(t)
	// Agent A: cost + a satisfied (value) and a failed outcome.
	createAgent(t, st, tenant, "agent-a", "agent-a", "")
	createSession(t, st, tenant, "sess-a", agentIDOf(t, st, tenant, "agent-a"))
	m.ingest(t, tenant, mkCost("anthropic", "claude-opus-4-8", "sess-a", 10, 5, 1000, baseTime))
	m.ingestOutcomeT(t, tenant, outcomeIngestRequest{
		SubjectKind: "agent", SubjectRef: "agent-a", OutcomeRef: "outc_1",
		Verdict: "satisfied", ValueMicroUSD: 5000, OccurredAt: baseTime, Source: "operator",
	})
	m.ingestOutcomeT(t, tenant, outcomeIngestRequest{
		SubjectKind: "agent", SubjectRef: "agent-a", OutcomeRef: "outc_2",
		Verdict: "failed", OccurredAt: baseTime, Source: "cma",
	})
	// Agent B: cost, NO outcomes → cancellation risk.
	createAgent(t, st, tenant, "agent-b", "agent-b", "")
	createSession(t, st, tenant, "sess-b", agentIDOf(t, st, tenant, "agent-b"))
	m.ingest(t, tenant, mkCost("anthropic", "claude-opus-4-8", "sess-b", 10, 5, 500, baseTime))

	view := runValue(t, m, st, tenant, "agent", 0)
	a := bucketFor(view, "agent-a")
	if a == nil {
		t.Fatal("missing bucket for agent-a")
	}
	if a.CostMicroUSD != 1000 || a.Outcomes != 2 || a.Satisfied != 1 || a.Unsatisfied != 1 {
		t.Errorf("agent-a = cost %d outcomes %d sat %d unsat %d, want 1000/2/1/1",
			a.CostMicroUSD, a.Outcomes, a.Satisfied, a.Unsatisfied)
	}
	if a.ValueMicroUSD != 5000 || a.NetValueMicroUSD != 4000 {
		t.Errorf("agent-a value/net = %d/%d, want 5000/4000", a.ValueMicroUSD, a.NetValueMicroUSD)
	}
	if a.CostPerOutcomeMicroUSD != 500 || a.CostPerSatisfiedMicroUSD != 1000 {
		t.Errorf("agent-a cost-per-outcome/satisfied = %d/%d, want 500/1000",
			a.CostPerOutcomeMicroUSD, a.CostPerSatisfiedMicroUSD)
	}
	if a.CancellationRisk {
		t.Error("agent-a has a satisfied outcome; must NOT be cancellation-risk")
	}

	b := bucketFor(view, "agent-b")
	if b == nil || !b.CancellationRisk || b.RiskReason != "spend with no recorded outcomes" {
		t.Errorf("agent-b cancellation risk = %+v, want risk with no-outcomes reason", b)
	}
	// Both agents are session-attributed, so total_cost == sum(buckets) and there is no
	// unattributed remainder here.
	if view.TotalCostMicroUSD != 1500 || view.UnattributedCostMicroUSD != 0 {
		t.Errorf("totals = cost %d unattributed %d, want 1500/0", view.TotalCostMicroUSD, view.UnattributedCostMicroUSD)
	}
}

// TestValueTotalIncludesUnattributed proves the CFO total reflects ALL spend: a sample
// with no agent attribution is NOT a bucket, but its cost is surfaced as the
// unattributed remainder and folded into total_cost (so net_value is not overstated).
func TestValueTotalIncludesUnattributed(t *testing.T) {
	m, st, tenant, _ := newFin(t)
	createAgent(t, st, tenant, "agent-x", "agent-x", "")
	createSession(t, st, tenant, "sess-x", agentIDOf(t, st, tenant, "agent-x"))
	m.ingest(t, tenant, mkCost("anthropic", "claude-opus-4-8", "sess-x", 10, 5, 400, baseTime))
	// Provider-stream cost with no session/agent → unattributed for the agent dimension.
	m.ingest(t, tenant, mkCost("anthropic", "claude-opus-4-8", "", 10, 5, 600, baseTime))

	view := runValue(t, m, st, tenant, "agent", 0)
	if view.TotalCostMicroUSD != 1000 || view.UnattributedCostMicroUSD != 600 {
		t.Errorf("totals = cost %d unattributed %d, want 1000/600", view.TotalCostMicroUSD, view.UnattributedCostMicroUSD)
	}
	if bucketFor(view, "") != nil {
		t.Error("the unattributed \"\" key must not be a bucket")
	}
	if x := bucketFor(view, "agent-x"); x == nil || x.CostMicroUSD != 400 {
		t.Errorf("agent-x bucket = %+v, want cost 400", x)
	}
}

// TestCancellationRiskAllUnsatisfied proves burn with only unsuccessful outcomes is
// also flagged.
func TestCancellationRiskAllUnsatisfied(t *testing.T) {
	m, st, tenant, _ := newFin(t)
	createAgent(t, st, tenant, "agent-c", "agent-c", "")
	createSession(t, st, tenant, "sess-c", agentIDOf(t, st, tenant, "agent-c"))
	m.ingest(t, tenant, mkCost("anthropic", "claude-opus-4-8", "sess-c", 10, 5, 700, baseTime))
	m.ingestOutcomeT(t, tenant, outcomeIngestRequest{
		SubjectKind: "agent", SubjectRef: "agent-c", OutcomeRef: "outc_x",
		Verdict: "max_iterations_reached", OccurredAt: baseTime, Source: "cma",
	})
	view := runValue(t, m, st, tenant, "agent", 0)
	c := bucketFor(view, "agent-c")
	if c == nil || !c.CancellationRisk || c.RiskReason != "outcomes recorded but none satisfied" {
		t.Errorf("agent-c = %+v, want cancellation risk (none satisfied)", c)
	}
	// Raising the material-burn floor above the spend clears the flag.
	view = runValue(t, m, st, tenant, "agent", 1000)
	c = bucketFor(view, "agent-c")
	if c == nil || c.CancellationRisk {
		t.Errorf("agent-c below the risk floor must not flag: %+v", c)
	}
}

// TestFallbackCreditAware proves a fallback_attempt cost counts toward burn but
// is broken out as creditable, so fallback credit neither inflates nor hides it.
func TestFallbackCreditAware(t *testing.T) {
	m, st, tenant, _ := newFin(t)
	createAgent(t, st, tenant, "agent-f", "agent-f", "")
	createSession(t, st, tenant, "sess-f", agentIDOf(t, st, tenant, "agent-f"))
	// The serving line + a billed mid-stream fallback decline on the same session.
	m.ingest(t, tenant, mkCost("anthropic", "claude-opus-4-8", "sess-f", 10, 5, 1000, baseTime))
	fa := mkCost("anthropic", "claude-haiku-4-5", "sess-f", 10, 5, 300, baseTime.Add(time.Second))
	fa.CostType = costTypeFallbackAttempt
	m.ingest(t, tenant, fa)

	view := runValue(t, m, st, tenant, "agent", 0)
	f := bucketFor(view, "agent-f")
	if f == nil {
		t.Fatal("missing bucket for agent-f")
	}
	if f.CostMicroUSD != 1300 {
		t.Errorf("burn = %d, want 1300 (fallback_attempt counts toward burn)", f.CostMicroUSD)
	}
	if f.CreditableMicroUSD != 300 {
		t.Errorf("creditable = %d, want 300 (the fallback_attempt portion surfaced)", f.CreditableMicroUSD)
	}
}

// TestOutcomeDedupAndRevise proves a re-posted outcome (same source+outcome_ref)
// upserts in place — verdict and value revise, never a duplicate row.
func TestOutcomeDedupAndRevise(t *testing.T) {
	m, st, tenant, _ := newFin(t)
	post := func(verdict string, value int64) {
		m.ingestOutcomeT(t, tenant, outcomeIngestRequest{
			SubjectKind: "identity", SubjectRef: "spiffe://acme/x", OutcomeRef: "outc_dup",
			Verdict: verdict, ValueMicroUSD: value, OccurredAt: baseTime, Source: "cma",
		})
	}
	post("failed", 0)
	post("satisfied", 1000) // revise the same outcome

	var rows []outcomeDTO
	if err := st.View(context.Background(), tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(outcomeKind)
		if err != nil {
			return err
		}
		recs, _, err := repo.List(context.Background(), model.Query{Limit: listCap})
		for _, r := range recs {
			rows = append(rows, toOutcomeDTO(r))
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("outcome rows = %d, want 1 (deduped)", len(rows))
	}
	if rows[0].Verdict != "satisfied" || rows[0].ValueMicroUSD != 1000 {
		t.Errorf("revised outcome = %s/%d, want satisfied/1000", rows[0].Verdict, rows[0].ValueMicroUSD)
	}
	if rows[0].IdentityRef != "spiffe://acme/x" {
		t.Errorf("identity subject ref = %q, want spiffe://acme/x", rows[0].IdentityRef)
	}
}

// TestValueSummaryCancellationList proves the CFO summary ranks cancellation-risk
// subjects and totals the creditable burn.
func TestValueSummaryCancellationList(t *testing.T) {
	m, st, tenant, _ := newFin(t)
	createAgent(t, st, tenant, "agent-burn", "agent-burn", "")
	createSession(t, st, tenant, "sess-burn", agentIDOf(t, st, tenant, "agent-burn"))
	m.ingest(t, tenant, mkCost("anthropic", "claude-opus-4-8", "sess-burn", 10, 5, 2000, baseTime))

	var sum valueSummaryResponse
	if err := st.View(context.Background(), tenant, func(sc store.Scope) error {
		var e error
		sum, e = m.valueSummary(context.Background(), sc, "agent", time.Time{}, false, time.Time{}, false, 0)
		return e
	}); err != nil {
		t.Fatal(err)
	}
	if sum.TotalCostMicroUSD != 2000 || sum.TotalOutcomes != 0 {
		t.Errorf("summary totals = cost %d outcomes %d, want 2000/0", sum.TotalCostMicroUSD, sum.TotalOutcomes)
	}
	if len(sum.CancellationRisk) != 1 || sum.CancellationRisk[0].Key != "agent-burn" {
		t.Fatalf("cancellation-risk list = %+v, want [agent-burn]", sum.CancellationRisk)
	}
	if sum.Note == "" {
		t.Error("summary with no outcomes must carry the honest no-outcomes note")
	}
}

// TestLateResolvedIdentityRestampedOnReDelivery proves the dedup re-stamp invariant:
// a bucket first ingested before the roster links its api-key is re-attributed to the
// firm identity when the SAME byte-identical bucket is re-delivered after the link
// (sampleChanged must notice the identity delta, not only the token/cost delta).
func TestLateResolvedIdentityRestampedOnReDelivery(t *testing.T) {
	m, st, tenant, _ := newFin(t)
	const apikey = "apikey_late_link"
	c := mkCost("anthropic", "claude-opus-4-8", "", 10, 5, 700, baseTime)
	c.APIKeyRef = apikey
	// First ingest: the api-key is not yet in the roster → identity_ref empty (honest).
	m.ingest(t, tenant, c)
	if got := firstIdentityRef(costSampleRows(t, st, tenant)); got != "" {
		t.Fatalf("identity_ref = %q, want empty before the roster link", got)
	}
	// The roster links the api-key to a firm identity; the SAME bucket is re-pulled.
	createIdentity(t, st, tenant, apikey, "credential", "anthropic")
	m.ingest(t, tenant, c)

	rows := costSampleRows(t, st, tenant)
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1 (re-delivery upserts in place, no duplicate)", len(rows))
	}
	if rows[0].String(colIdentityRef) != apikey {
		t.Errorf("identity_ref = %q, want %q (re-stamped on identical re-pull after the late link)",
			rows[0].String(colIdentityRef), apikey)
	}
}

// TestSummaryCostPerOutcomeUsesAttributedCost proves the summary divides ATTRIBUTED
// cost (not the unattributed-inclusive total) by outcomes, while net-value still uses
// the full total.
func TestSummaryCostPerOutcomeUsesAttributedCost(t *testing.T) {
	m, st, tenant, _ := newFin(t)
	createAgent(t, st, tenant, "agent-v", "agent-v", "")
	createSession(t, st, tenant, "sess-v", agentIDOf(t, st, tenant, "agent-v"))
	m.ingest(t, tenant, mkCost("anthropic", "claude-opus-4-8", "sess-v", 10, 5, 1000, baseTime)) // attributed
	m.ingest(t, tenant, mkCost("anthropic", "claude-opus-4-8", "", 10, 5, 4000, baseTime))       // unattributed
	for _, ref := range []string{"o1", "o2"} {
		m.ingestOutcomeT(t, tenant, outcomeIngestRequest{
			SubjectKind: "agent", SubjectRef: "agent-v", OutcomeRef: ref,
			Verdict: "satisfied", OccurredAt: baseTime, Source: "operator",
		})
	}
	var sum valueSummaryResponse
	if err := st.View(context.Background(), tenant, func(sc store.Scope) error {
		var e error
		sum, e = m.valueSummary(context.Background(), sc, "agent", time.Time{}, false, time.Time{}, false, 0)
		return e
	}); err != nil {
		t.Fatal(err)
	}
	if sum.TotalCostMicroUSD != 5000 || sum.UnattributedCostMicroUSD != 4000 {
		t.Errorf("totals = cost %d unattributed %d, want 5000/4000", sum.TotalCostMicroUSD, sum.UnattributedCostMicroUSD)
	}
	// Attributed 1000 / 2 outcomes = 500 (NOT 5000/2 = 2500).
	if sum.CostPerOutcomeMicroUSD != 500 || sum.CostPerSatisfiedMicroUSD != 500 {
		t.Errorf("cost-per-outcome/satisfied = %d/%d, want 500/500 (attributed cost, not total)",
			sum.CostPerOutcomeMicroUSD, sum.CostPerSatisfiedMicroUSD)
	}
}

// TestOutcomeRepostNeverBlanksIdentity proves a re-post never blanks a prior derived
// attribution: after the agent (and its identity binding) is gone, a re-post keeps the
// firm identity stamped on the first post.
func TestOutcomeRepostNeverBlanksIdentity(t *testing.T) {
	m, st, tenant, _ := newFin(t)
	idID := createIdentity(t, st, tenant, "spiffe://acme/r", "workload_identity", "spiffe")
	aID := createAgent(t, st, tenant, "agent-r", "agent-r", idID)
	post := func() {
		m.ingestOutcomeT(t, tenant, outcomeIngestRequest{
			SubjectKind: "agent", SubjectRef: "agent-r", OutcomeRef: "outc_r",
			Verdict: "satisfied", OccurredAt: baseTime, Source: "cma",
		})
	}
	post() // identity_ref = spiffe://acme/r stamped
	deleteAgent(t, st, tenant, aID)
	post() // re-resolve now yields no identity → the guard keeps the prior attribution

	rows := outcomeRows(t, st, tenant)
	if len(rows) != 1 || rows[0].IdentityRef != "spiffe://acme/r" {
		t.Errorf("outcome identity_ref = %v, want one row with spiffe://acme/r (never blanked)", rows)
	}
}

// TestOutcomeValidateIdempotencyRule proves the bridge rejects a non-idempotent post
// (no outcome_ref and no occurred_at), so a retry cannot double-count value.
func TestOutcomeValidateIdempotencyRule(t *testing.T) {
	if msg := (outcomeIngestRequest{SubjectKind: "agent", SubjectRef: "a", Verdict: "satisfied"}).validate(); msg == "" {
		t.Error("outcome with neither outcome_ref nor occurred_at must be rejected (idempotency)")
	}
	ok := outcomeIngestRequest{SubjectKind: "agent", SubjectRef: "a", Verdict: "satisfied", OutcomeRef: "o1"}
	if msg := ok.validate(); msg != "" {
		t.Errorf("outcome with an outcome_ref must validate: %s", msg)
	}
}

// --- test helpers ------------------------------------------------------------

func deleteAgent(t *testing.T, st store.Store, tenant model.TenantID, id model.ID) {
	t.Helper()
	if err := st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		return sc.Agents().Delete(context.Background(), id)
	}); err != nil {
		t.Fatalf("deleteAgent: %v", err)
	}
}

func outcomeRows(t *testing.T, st store.Store, tenant model.TenantID) []outcomeDTO {
	t.Helper()
	var out []outcomeDTO
	if err := st.View(context.Background(), tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(outcomeKind)
		if err != nil {
			return err
		}
		recs, _, err := repo.List(context.Background(), model.Query{Limit: listCap})
		for _, r := range recs {
			out = append(out, toOutcomeDTO(r))
		}
		return err
	}); err != nil {
		t.Fatalf("outcomeRows: %v", err)
	}
	return out
}

func agentIDOf(t *testing.T, st store.Store, tenant model.TenantID, externalID string) model.ID {
	t.Helper()
	var id model.ID
	if err := st.View(context.Background(), tenant, func(sc store.Scope) error {
		a, ok, err := findOne(context.Background(), sc.Agents(), eq("external_id", externalID))
		if err != nil {
			return err
		}
		if ok {
			id = a.ID
		}
		return nil
	}); err != nil {
		t.Fatalf("agentIDOf: %v", err)
	}
	if id.IsZero() {
		t.Fatalf("agent %q not found", externalID)
	}
	return id
}

func runValue(t *testing.T, m *Module, st store.Store, tenant model.TenantID, dim string, minCost int64) valueResponse {
	t.Helper()
	var out valueResponse
	if err := st.View(context.Background(), tenant, func(sc store.Scope) error {
		var e error
		out, e = m.costPerOutcome(context.Background(), sc, dim, time.Time{}, false, time.Time{}, false, minCost)
		return e
	}); err != nil {
		t.Fatalf("costPerOutcome: %v", err)
	}
	return out
}

func bucketFor(v valueResponse, key string) *valueBucketDTO {
	for i := range v.Buckets {
		if v.Buckets[i].Key == key {
			return &v.Buckets[i]
		}
	}
	return nil
}

func firstIdentityRef(rows []model.Record) string {
	if len(rows) == 0 {
		return "<no rows>"
	}
	return rows[0].String(colIdentityRef)
}
