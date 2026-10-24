// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build unix

package sessions

import (
	"context"
	"strconv"
	"testing"

	"github.com/olivaresai/olivares/core/model"
)

// openCodeUsageLaunch starts an OpenCode run on the fixture's free model and takes
// one turn per prompt.
func openCodeUsageLaunch(t *testing.T, fixture openCodeFixture, prompts int, opts ...Option) (*Module, model.TenantID, runDTO) {
	t.Helper()
	m, _, tenant, prof := openCodeHarness(t, AuthSourceAccountHome, opts...)
	setOpenCodeFixture(t, prof, fixture)
	ctx := context.Background()
	dto, err := createProfiledTestRun(t, m, ctx, tenant, CreateRunParams{
		Transport: TransportStreamJSON, Isolation: IsolationNative,
		Actor: "user:u1", ActorKind: model.ActorUser,
		ProviderProfileRef: prof.Ref, Model: "opencode/big-pickle",
	})
	if err != nil {
		t.Fatalf("createRun: %v", err)
	}
	t.Cleanup(func() { _, _ = m.stopRun(context.Background(), tenant, dto.RunRef, "user:u1", model.ActorUser) })
	for i := 0; i < prompts; i++ {
		if err := m.sendTextInput(ctx, tenant, dto.RunRef, "go"); err != nil {
			t.Fatalf("input %d: %v", i, err)
		}
		want := int64(i+1) * 4
		waitFor(t, "the turn's tokens on the run", func() bool {
			d, _ := m.getRun(ctx, tenant, dto.RunRef)
			return d.OutputTokens != nil && *d.OutputTokens == want
		})
	}
	return m, tenant, dto
}

// #2891: OpenCode reports each turn's tokens on its prompt result (ACP
// PromptResponse.usage) and the session's cumulative cost on usage_update (measured
// on 1.19.3, opencode/glm-5.3-flash-free: {"amount":0,"currency":"USD"}). Both reach
// the run and the spend ledger: the turns' tokens add up, and the cost the tool
// reports as 0 is recorded as 0, a free model, not as "not reported".
func TestOpenCodeFreeModelUsageIsRecordedWithCostZero(t *testing.T) {
	sink := &recordingCostSink{}
	free := 0.0
	m, tenant, dto := openCodeUsageLaunch(t, openCodeFixture{
		SessionID:   "ses-free",
		PromptUsage: map[string]int64{"inputTokens": 2994, "outputTokens": 3, "totalTokens": 2998, "thoughtTokens": 1},
		UsageCost:   &free,
	}, 2, WithSessionCostSink(sink))
	got, _ := m.getRun(context.Background(), tenant, dto.RunRef)
	if got.InputTokens == nil || *got.InputTokens != 2*2994 {
		t.Fatalf("input_tokens = %s, want the two turns' 5988", int64Text(got.InputTokens))
	}
	if got.CostMicroUSD == nil || *got.CostMicroUSD != 0 {
		t.Fatalf("cost_micro_usd = %s, want the 0 OpenCode reported", int64Text(got.CostMicroUSD))
	}
	if got.UsageModelRef != "opencode/big-pickle" {
		t.Fatalf("usage_model_ref = %q, want the session's model", got.UsageModelRef)
	}
	waitFor(t, "two spend rows", func() bool { return len(sink.all()) == 2 })
	for _, row := range sink.all() {
		if row.InputTokens != 2994 || row.OutputTokens != 4 || row.CostMicroUSD != 0 || !row.CostFromProviderClient ||
			row.ProviderRef != providerDriverOpenCode || row.ModelRef != "opencode/big-pickle" {
			t.Fatalf("spend row = %+v, want one free turn reported by OpenCode", row)
		}
	}
}

// A paid model's cumulative cost is credited per turn as what it added.
func TestOpenCodeReportedCostIsCreditedAsItGrows(t *testing.T) {
	sink := &recordingCostSink{}
	perTurn := 0.0125
	m, tenant, dto := openCodeUsageLaunch(t, openCodeFixture{
		SessionID:   "ses-paid",
		PromptUsage: map[string]int64{"inputTokens": 100, "outputTokens": 4, "totalTokens": 104},
		UsageCost:   &perTurn,
	}, 2, WithSessionCostSink(sink))
	waitFor(t, "the cost of both turns", func() bool {
		d, _ := m.getRun(context.Background(), tenant, dto.RunRef)
		return d.CostMicroUSD != nil && *d.CostMicroUSD == 25000
	})
	waitFor(t, "two spend rows", func() bool { return len(sink.all()) == 2 })
	for _, row := range sink.all() {
		if row.CostMicroUSD != 12500 || !row.CostFromProviderClient {
			t.Fatalf("spend row = %+v, want 12500 per turn from OpenCode", row)
		}
	}
}

// Tokens without a cost report leave the cost unknown, and a currency other than
// US dollars is no report: neither becomes a 0 that reads as free.
func TestOpenCodeUsageWithoutAUSDCostLeavesTheCostUnknown(t *testing.T) {
	for _, tc := range []struct {
		name    string
		fixture openCodeFixture
	}{
		{"no usage_update", openCodeFixture{SessionID: "ses-tokens", PromptUsage: map[string]int64{"inputTokens": 10, "outputTokens": 4}}},
		{"another currency", openCodeFixture{SessionID: "ses-eur", PromptUsage: map[string]int64{"inputTokens": 10, "outputTokens": 4},
			UsageCost: new(float64), UsageCurrency: "EUR"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, tenant, dto := openCodeUsageLaunch(t, tc.fixture, 1)
			if got, _ := m.getRun(context.Background(), tenant, dto.RunRef); got.CostMicroUSD != nil {
				t.Fatalf("cost_micro_usd = %d, want unknown", *got.CostMicroUSD)
			}
		})
	}
}

// A cost the tool reported, 0 included, is the tool's figure: the list price is not
// put over it.
func TestOpenCodeReportedZeroCostIsNotListPriced(t *testing.T) {
	priced := 0
	pricer := func(string, string, TurnTokens) (int64, bool) { priced++; return 999, true }
	free := 0.0
	m, tenant, dto := openCodeUsageLaunch(t, openCodeFixture{
		SessionID:   "ses-free-priced",
		PromptUsage: map[string]int64{"inputTokens": 10, "outputTokens": 4},
		UsageCost:   &free,
	}, 1, WithListPricer(pricer))
	got, _ := m.getRun(context.Background(), tenant, dto.RunRef)
	if got.CostMicroUSD == nil || *got.CostMicroUSD != 0 || priced != 0 {
		t.Fatalf("cost_micro_usd = %s after %d list-price lookups, want OpenCode's 0 and none", int64Text(got.CostMicroUSD), priced)
	}
}

// A turn whose result carries no usage still credits the cost the agent reported
// during it.
func TestOpenCodeCostReportedDuringATurnWithoutUsageIsCredited(t *testing.T) {
	perTurn := 0.0125
	m, _, tenant, prof := openCodeHarness(t, AuthSourceAccountHome)
	setOpenCodeFixture(t, prof, openCodeFixture{SessionID: "ses-cost-only", UsageCost: &perTurn})
	ctx := context.Background()
	dto, err := openCodeLaunch(t, m, tenant, prof)
	if err != nil {
		t.Fatalf("createRun: %v", err)
	}
	t.Cleanup(func() { _, _ = m.stopRun(context.Background(), tenant, dto.RunRef, "user:u1", model.ActorUser) })
	if err := m.sendTextInput(ctx, tenant, dto.RunRef, "go"); err != nil {
		t.Fatalf("input: %v", err)
	}
	waitFor(t, "the turn's cost on the run", func() bool {
		d, _ := m.getRun(ctx, tenant, dto.RunRef)
		return d.CostMicroUSD != nil && *d.CostMicroUSD == 12500
	})
}

// A resumed conversation's cost is OpenCode's lifetime total (1.19.3: usage.ts sums every
// assistant message; resume sends no usage_update), so the first total a resumed launch
// sees already holds what earlier launches spent. It is that launch's baseline: the
// first resumed turn's own cost is unknown rather than the history billed again, and
// later turns are credited by what they add.
func TestOpenCodeResumedCostIsABaselineNotNewSpend(t *testing.T) {
	perTurn := 0.0125
	usage := map[string]int64{"inputTokens": 100, "outputTokens": 4, "totalTokens": 104}
	m, _, tenant, prof := openCodeHarness(t, AuthSourceAccountHome)
	setOpenCodeFixture(t, prof, openCodeFixture{SessionID: "ses-cost-resume", PromptUsage: usage, UsageCost: &perTurn})
	ctx := context.Background()
	first, err := openCodeLaunch(t, m, tenant, prof)
	if err != nil {
		t.Fatalf("createRun: %v", err)
	}
	if err := m.sendTextInput(ctx, tenant, first.RunRef, "go"); err != nil {
		t.Fatalf("input: %v", err)
	}
	waitFor(t, "the first launch's cost", func() bool {
		d, _ := m.getRun(ctx, tenant, first.RunRef)
		return d.CostMicroUSD != nil && *d.CostMicroUSD == 12500
	})
	if _, err := m.stopRun(ctx, tenant, first.RunRef, "user:u1", model.ActorUser); err != nil {
		t.Fatalf("stop: %v", err)
	}
	setOpenCodeFixture(t, prof, openCodeFixture{ResumeEmptyObject: true, PromptUsage: usage, UsageCost: &perTurn, HistoryCost: perTurn})
	resumed, err := m.resumeRun(ctx, tenant, first.RunRef, "user:u1", model.ActorUser, "")
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	t.Cleanup(func() { _, _ = m.stopRun(context.Background(), tenant, resumed.RunRef, "user:u1", model.ActorUser) })
	for i, want := range []int64{8, 12} {
		if err := m.sendTextInput(ctx, tenant, resumed.RunRef, "again"); err != nil {
			t.Fatalf("input %d after resume: %v", i, err)
		}
		waitFor(t, "the resumed turn's tokens", func() bool {
			d, _ := m.getRun(ctx, tenant, resumed.RunRef)
			return d.OutputTokens != nil && *d.OutputTokens == want
		})
		wantCost := []int64{12500, 25000}[i]
		if got, _ := m.getRun(ctx, tenant, resumed.RunRef); got.CostMicroUSD == nil || *got.CostMicroUSD != wantCost {
			t.Fatalf("after resumed turn %d cost_micro_usd = %s, want %d: the history is not billed again", i+1, int64Text(got.CostMicroUSD), wantCost)
		}
	}
}

// A lifetime total that goes down (a reverted message, or a broken agent) credits
// nothing; the next rise is credited from the highest total seen, never again in full.
func TestOpenCodeLowerCostTotalIsNotCreditedAgain(t *testing.T) {
	m, tenant, dto := openCodeUsageLaunch(t, openCodeFixture{
		SessionID:       "ses-cost-lower",
		PromptUsage:     map[string]int64{"inputTokens": 100, "outputTokens": 4, "totalTokens": 104},
		UsageCostTotals: []float64{0.02, 0.01, 0.03},
	}, 3)
	if got, _ := m.getRun(context.Background(), tenant, dto.RunRef); got.CostMicroUSD == nil || *got.CostMicroUSD != 30000 {
		t.Fatalf("cost_micro_usd = %s, want 30000: totals 0.02, 0.01, 0.03 cost 0.03 in all", int64Text(got.CostMicroUSD))
	}
}

// Whether an agent's input counts its cached tokens is read from its own total.
func TestACPUsageReadsCachedTokensFromTheTotal(t *testing.T) {
	for _, tc := range []struct {
		name string
		u    acpUsage
		want modelUsage
	}{
		{"cached outside input", acpUsage{TotalTokens: 170, InputTokens: 100, OutputTokens: 10, CachedReadTokens: 50, CachedWriteTokens: 10},
			modelUsage{InputTokens: 100, OutputTokens: 10, CacheReadTokens: 50, CacheCreationTokens: 10}},
		{"cached inside input", acpUsage{TotalTokens: 110, InputTokens: 100, OutputTokens: 10, CachedReadTokens: 50, CachedWriteTokens: 10},
			modelUsage{InputTokens: 40, OutputTokens: 10, CacheReadTokens: 50, CacheCreationTokens: 10}},
		{"OpenCode 1.19.3", acpUsage{TotalTokens: 2998, InputTokens: 2994, OutputTokens: 3, ThoughtTokens: 1},
			modelUsage{InputTokens: 2994, OutputTokens: 4}},
	} {
		if got := tc.u.tokens(); got != tc.want {
			t.Errorf("%s: %+v, want %+v", tc.name, got, tc.want)
		}
	}
}

// int64Text shows a counter that may be absent.
func int64Text(p *int64) string {
	if p == nil {
		return "absent"
	}
	return strconv.FormatInt(*p, 10)
}
