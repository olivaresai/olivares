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

func TestSpendByDimension(t *testing.T) {
	m, st, tenant, _ := newFin(t)
	m.ingest(t, tenant, mkCost("anthropic", "claude-opus-4-8", "", 10, 5, 100, baseTime))
	m.ingest(t, tenant, mkCost("anthropic", "claude-opus-4-8", "", 10, 5, 200, baseTime.Add(time.Minute)))
	m.ingest(t, tenant, mkCost("google", "gemini-1.5-flash", "", 10, 5, 50, baseTime.Add(2*time.Minute)))

	var out spendResponse
	if err := st.View(context.Background(), tenant, func(sc store.Scope) error {
		var e error
		out, e = spendByDimension(context.Background(), sc, "model", time.Time{}, false, time.Time{}, false)
		return e
	}); err != nil {
		t.Fatal(err)
	}
	if out.TotalMicroUSD != 350 {
		t.Errorf("total = %d, want 350", out.TotalMicroUSD)
	}
	if len(out.Buckets) != 2 || out.Buckets[0].Key != "claude-opus-4-8" || out.Buckets[0].CostMicroUSD != 300 {
		t.Errorf("buckets = %+v, want opus(300) first", out.Buckets)
	}
}

func TestSummarize(t *testing.T) {
	m, st, tenant, _ := newFin(t)
	m.ingest(t, tenant, mkCost("anthropic", "claude-opus-4-8", "", 100, 50, 300, baseTime))
	m.ingest(t, tenant, mkCost("google", "gemini-1.5-flash", "", 20, 10, 50, baseTime.Add(time.Minute)))

	var out summaryResponse
	if err := st.View(context.Background(), tenant, func(sc store.Scope) error {
		var e error
		out, e = summarize(context.Background(), sc, time.Time{}, false, time.Time{}, false)
		return e
	}); err != nil {
		t.Fatal(err)
	}
	if out.TotalMicroUSD != 350 || out.InputTokens != 120 || out.OutputTokens != 60 {
		t.Errorf("totals = %+v", out)
	}
	if len(out.ByModel) != 2 || len(out.ByProvider) != 2 {
		t.Errorf("breakdown = byModel %d byProvider %d", len(out.ByModel), len(out.ByProvider))
	}
}

func TestForecastProjectsAboveSpend(t *testing.T) {
	m, st, tenant, _ := newFin(t)
	m.ingest(t, tenant, mkCost("anthropic", "claude-opus-4-8", "", 10, 5, 500, baseTime))

	var out forecastResponse
	if err := st.View(context.Background(), tenant, func(sc store.Scope) error {
		var e error
		out, e = forecastPeriod(context.Background(), sc, "monthly", baseTime, 0)
		return e
	}); err != nil {
		t.Fatal(err)
	}
	if out.SpendMicroUSD != 500 {
		t.Errorf("spend = %d, want 500", out.SpendMicroUSD)
	}
	if out.ProjectedMicroUSD <= 500 {
		t.Errorf("projected %d should exceed spend at one-third of the month", out.ProjectedMicroUSD)
	}
}

func TestRecommendationsCheaperModel(t *testing.T) {
	m, st, tenant, _ := newFin(t)
	m.ingest(t, tenant, mkCost("anthropic", "claude-opus-4-8", "", 100, 50, 9000, baseTime))
	m.ingest(t, tenant, mkCost("google", "gemini-1.5-flash", "", 100, 50, 10, baseTime.Add(time.Minute)))
	// Give the governed models per-token rates (module X would normally enrich
	// these; here we set them directly to drive the savings estimate).
	setModelRates(t, st, tenant, "claude-opus-4-8", 15, 75)
	setModelRates(t, st, tenant, "gemini-1.5-flash", 1, 1)

	var recs []recommendationDTO
	if err := st.View(context.Background(), tenant, func(sc store.Scope) error {
		var e error
		recs, e = m.recommendations(context.Background(), sc, baseTime)
		return e
	}); err != nil {
		t.Fatal(err)
	}
	var cheaper *recommendationDTO
	sawCacheReco := false
	for i := range recs {
		switch recs[i].Kind {
		case "cheaper_model":
			if recs[i].Subject == "claude-opus-4-8" {
				cheaper = &recs[i]
			}
		case "cache_opportunity", "cache_savings":
			sawCacheReco = true
		}
	}
	if cheaper == nil {
		t.Fatalf("no cheaper_model recommendation for opus: %+v", recs)
	}
	// hypothetical on gemini = 100*1 + 50*1 = 150; savings = 9000 - 150 = 8850.
	if cheaper.EstimatedSavingsMicroUSD != 8850 {
		t.Errorf("estimated savings = %d, want 8850", cheaper.EstimatedSavingsMicroUSD)
	}
	// With no cache reads in this estate, the cache recommendation is the
	// "use caching" opportunity — measured, not a "not measurable" disclaimer.
	if !sawCacheReco {
		t.Error("recommendations must include a measured cache recommendation")
	}
}

func TestSummarizeMeasuresCacheSavings(t *testing.T) {
	m, st, tenant, _ := newFin(t)
	// A sample with cache reads: total input 1000 folds 600 uncached + 400 cache-read.
	c := mkCost("anthropic", "claude-opus-4-8", "", 1000, 50, 3000, baseTime)
	c.CacheReadTokens = 400
	m.ingest(t, tenant, c)
	setModelRates(t, st, tenant, "claude-opus-4-8", 15, 75) // 15 µUSD/token input

	var out summaryResponse
	if err := st.View(context.Background(), tenant, func(sc store.Scope) error {
		var e error
		out, e = summarize(context.Background(), sc, time.Time{}, false, time.Time{}, false)
		return e
	}); err != nil {
		t.Fatal(err)
	}
	if out.Cache.CacheReadTokens != 400 {
		t.Errorf("cache read tokens = %d, want 400", out.Cache.CacheReadTokens)
	}
	if out.Cache.UncachedInputTokens != 600 {
		t.Errorf("uncached input = %d, want 600 (1000 total − 400 cache read)", out.Cache.UncachedInputTokens)
	}
	// Realized saving = 400 tokens × 15 µUSD/tok × 0.9 = 5400 µUSD.
	if out.Cache.SavingsMicroUSD != 5400 {
		t.Errorf("cache savings = %d, want 5400", out.Cache.SavingsMicroUSD)
	}
	if out.Cache.HitRatePct != 40 { // 400 / 1000 total input
		t.Errorf("hit rate = %d%%, want 40%%", out.Cache.HitRatePct)
	}
}

func TestReconcileBilledVsEstimatedAndPriorityIsEstimatedOnly(t *testing.T) {
	m, st, tenant, _ := newFin(t)
	// Estimated stream: standard-tier $0.30 and a priority-tier $0.20 (priority is
	// never billed via cost_report, so it must show as estimated-only).
	est1 := mkCost("anthropic", "claude-opus-4-8", "", 100, 50, 300, baseTime)
	est1.ServiceTier = "standard"
	est2 := mkCost("anthropic", "claude-opus-4-8", "", 100, 50, 200, baseTime)
	est2.ServiceTier = "priority"
	m.ingest(t, tenant, est1)
	m.ingest(t, tenant, est2)
	// Billed stream: cost_report says the standard spend was actually $0.32.
	bill := mkCost("anthropic", "claude-opus-4-8", "", 0, 0, 320, baseTime)
	bill.Provenance = sdkmodel.ProvenanceBilled
	bill.ServiceTier = "standard"
	m.ingest(t, tenant, bill)

	var out reconciliationResponse
	if err := st.View(context.Background(), tenant, func(sc store.Scope) error {
		var e error
		out, e = reconcile(context.Background(), sc, time.Time{}, false, time.Time{}, false)
		return e
	}); err != nil {
		t.Fatal(err)
	}
	if !out.HasBilled || out.BilledTotalMicroUSD != 320 {
		t.Errorf("billed total = %d (has=%v), want 320", out.BilledTotalMicroUSD, out.HasBilled)
	}
	if out.EstimatedTotalMicroUSD != 500 { // 300 + 200, billed row excluded
		t.Errorf("estimated total = %d, want 500 (billed excluded)", out.EstimatedTotalMicroUSD)
	}
	if out.DriftMicroUSD != 320-500 {
		t.Errorf("drift = %d, want %d", out.DriftMicroUSD, 320-500)
	}
	if len(out.EstimatedOnlyTiers) != 1 || out.EstimatedOnlyTiers[0] != "priority" {
		t.Errorf("estimated-only tiers = %v, want [priority]", out.EstimatedOnlyTiers)
	}
}

func TestDefaultSpendExcludesBilled(t *testing.T) {
	m, st, tenant, _ := newFin(t)
	est := mkCost("anthropic", "claude-opus-4-8", "", 10, 5, 100, baseTime)
	bill := mkCost("anthropic", "claude-opus-4-8", "", 0, 0, 999, baseTime)
	bill.Provenance = sdkmodel.ProvenanceBilled
	m.ingest(t, tenant, est)
	m.ingest(t, tenant, bill)

	var out spendResponse
	if err := st.View(context.Background(), tenant, func(sc store.Scope) error {
		var e error
		out, e = spendByDimension(context.Background(), sc, "model", time.Time{}, false, time.Time{}, false)
		return e
	}); err != nil {
		t.Fatal(err)
	}
	// The billed row must NOT be summed into default spend (no double-count).
	if out.TotalMicroUSD != 100 {
		t.Errorf("default spend total = %d, want 100 (billed row excluded)", out.TotalMicroUSD)
	}
}

// setModelRates sets a governed model's coarse per-token cost fields.
func setModelRates(t *testing.T, st store.Store, tenant model.TenantID, name string, in, out int64) {
	t.Helper()
	if err := st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		ms, _, err := sc.Models().List(context.Background(), model.Query{Filters: []model.Filter{eq("name", name)}, Limit: 1})
		if err != nil {
			return err
		}
		md := ms[0]
		md.InputCostMicroUSD = in
		md.OutputCostMicroUSD = out
		_, err = sc.Models().Update(context.Background(), md)
		return err
	}); err != nil {
		t.Fatalf("setModelRates: %v", err)
	}
}
