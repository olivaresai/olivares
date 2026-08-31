// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package modelprovider

import (
	"testing"
	"time"

	"github.com/olivaresai/olivares/sdk/model"
)

// sonnetPricing is a representative declared price used by the golden tests:
// $3 / MTok input, $15 / MTok output, $3.75 cache write, $0.30 cache read.
var sonnetPricing = ModelPricing{
	InputPerMTokUSD:      3.0,
	OutputPerMTokUSD:     15.0,
	CacheWritePerMTokUSD: 3.75,
	CacheReadPerMTokUSD:  0.30,
	Currency:             "USD",
	AsOf:                 "2026-06-01",
	Source:               PricingList,
}

func TestDeriveCostMicroUSD_Golden(t *testing.T) {
	// $P / MTok == P micro-USD / token, so the arithmetic is count × price.
	cases := []struct {
		name string
		u    Usage
		want int64
	}{
		{"input+output", Usage{InputTokens: 1000, OutputTokens: 500}, 1000*3 + 500*15},
		{"cache tiers", Usage{CacheWriteTokens: 1000, CacheReadTokens: 2000}, 3750 + 600},
		{"all tiers", Usage{InputTokens: 1000, OutputTokens: 500, CacheWriteTokens: 1000, CacheReadTokens: 2000}, 3000 + 7500 + 3750 + 600},
		{"zero", Usage{}, 0},
		{"rounding", Usage{InputTokens: 1}, 3}, // 1 token × $3/MTok = 3 micro-USD
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := sonnetPricing.DeriveCostMicroUSD(c.u)
			if got != c.want {
				t.Fatalf("DeriveCostMicroUSD = %d, want %d", got, c.want)
			}
		})
	}
}

func TestDeriveCostMicroUSD_Rounding(t *testing.T) {
	// A fractional micro-USD must round to nearest, never truncate to a silent 0.
	p := ModelPricing{InputPerMTokUSD: 0.5} // 0.5 micro-USD / token
	if got := p.DeriveCostMicroUSD(Usage{InputTokens: 1}); got != 1 {
		t.Fatalf("0.5 micro-USD rounded = %d, want 1", got)
	}
	if got := p.DeriveCostMicroUSD(Usage{InputTokens: 3}); got != 2 { // 1.5 -> 2
		t.Fatalf("1.5 micro-USD rounded = %d, want 2", got)
	}
}

func TestToCostSample_DerivesAndFoldsCache(t *testing.T) {
	now := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	u := Usage{
		ProviderRef: ProviderAnthropic, ModelRef: "claude-sonnet-4-5", SessionRef: "sess-1",
		InputTokens: 1000, OutputTokens: 500, CacheWriteTokens: 1000, CacheReadTokens: 2000,
		OccurredAt: now,
	}
	cs := ToCostSample(u, sonnetPricing)

	if cs.ProviderRef != ProviderAnthropic || cs.ModelRef != "claude-sonnet-4-5" || cs.SessionRef != "sess-1" {
		t.Fatalf("refs not carried verbatim: %+v", cs)
	}
	// InputTokens folds the cache tiers: 1000 + 1000 + 2000.
	if cs.InputTokens != 4000 {
		t.Fatalf("InputTokens = %d, want 4000 (uncached+cacheWrite+cacheRead)", cs.InputTokens)
	}
	if cs.OutputTokens != 500 {
		t.Fatalf("OutputTokens = %d, want 500", cs.OutputTokens)
	}
	if cs.CostMicroUSD != 3000+7500+3750+600 {
		t.Fatalf("CostMicroUSD = %d, want %d", cs.CostMicroUSD, 3000+7500+3750+600)
	}
	if !cs.OccurredAt.Equal(now) {
		t.Fatalf("OccurredAt = %v, want %v", cs.OccurredAt, now)
	}
}

func TestToCostSampleWithCost_PrefersAPIAmount(t *testing.T) {
	u := Usage{ProviderRef: ProviderOpenAI, ModelRef: "gpt-4o", InputTokens: 10, OutputTokens: 10}
	// API reported an authoritative figure that differs from any derivation.
	cs := ToCostSampleWithCost(u, 123456)
	if cs.CostMicroUSD != 123456 {
		t.Fatalf("CostMicroUSD = %d, want 123456 (API amount wins)", cs.CostMicroUSD)
	}
	// A negative (unknown) cost clamps to 0, never goes negative.
	if got := ToCostSampleWithCost(u, -5); got.CostMicroUSD != 0 {
		t.Fatalf("negative cost = %d, want 0", got.CostMicroUSD)
	}
}

func TestTotalInputTokens(t *testing.T) {
	u := Usage{InputTokens: 5, CacheWriteTokens: 3, CacheCreation1hTokens: 4, CacheCreation5mTokens: 6, CacheReadTokens: 2}
	if got := u.TotalInputTokens(); got != 20 {
		t.Fatalf("TotalInputTokens = %d, want 20", got)
	}
}

// sonnetPricingTTL adds the distinct 1-hour cache-write rate (2× base = $6/MTok).
var sonnetPricingTTL = ModelPricing{
	InputPerMTokUSD: 3.0, OutputPerMTokUSD: 15.0,
	CacheWritePerMTokUSD: 3.75, CacheWrite1hPerMTokUSD: 6, CacheReadPerMTokUSD: 0.30,
	Currency: "USD", AsOf: "2026-06-01", Source: PricingList,
}

func TestDeriveCostMicroUSD_PerTTLCacheWrite(t *testing.T) {
	// 5m write priced at 3.75, 1h write priced DISTINCTLY at 6 — not both at 3.75.
	u := Usage{CacheCreation5mTokens: 1000, CacheCreation1hTokens: 1000}
	if got := sonnetPricingTTL.DeriveCostMicroUSD(u); got != 1000*3.75+1000*6 {
		t.Fatalf("per-TTL cost = %d, want %d", got, int64(1000*3.75+1000*6))
	}
	// When no distinct 1h rate is declared, 1h falls back to the 5m rate (never 0).
	noTTL := ModelPricing{CacheWritePerMTokUSD: 3.75}
	if got := noTTL.DeriveCostMicroUSD(Usage{CacheCreation1hTokens: 1000}); got != 3750 {
		t.Fatalf("1h fallback cost = %d, want 3750 (5m rate, never free)", got)
	}
}

func TestToCostSample_CarriesCacheAndDimensions(t *testing.T) {
	u := Usage{
		ProviderRef: ProviderAnthropic, ModelRef: "claude-sonnet-4-6",
		InputTokens: 1000, OutputTokens: 500,
		CacheCreation5mTokens: 400, CacheCreation1hTokens: 100, CacheReadTokens: 2000,
		WorkspaceRef: "wrkspc_01", APIKeyRef: "apikey_01", ServiceTier: "standard",
		ContextWindow: "0-200k", InferenceGeo: "us",
		Gateway: model.GatewayDirect, Provenance: model.ProvenanceEstimated, CostType: "tokens",
	}
	cs := ToCostSample(u, sonnetPricingTTL)
	// InputTokens still folds the full input volume (v1 meaning): 1000+400+100+2000.
	if cs.InputTokens != 3500 {
		t.Fatalf("InputTokens = %d, want 3500", cs.InputTokens)
	}
	// And the cache breakdown is carried additionally.
	if cs.CacheReadTokens != 2000 || cs.CacheCreation5mTokens != 400 || cs.CacheCreation1hTokens != 100 {
		t.Fatalf("cache split not carried: %+v", cs)
	}
	if cs.WorkspaceRef != "wrkspc_01" || cs.APIKeyRef != "apikey_01" || cs.ServiceTier != "standard" ||
		cs.ContextWindow != "0-200k" || cs.InferenceGeo != "us" {
		t.Fatalf("attribution dimensions not carried: %+v", cs)
	}
	if cs.Gateway != model.GatewayDirect || cs.Provenance != model.ProvenanceEstimated || cs.CostType != "tokens" {
		t.Fatalf("surface/provenance not carried: %+v", cs)
	}
}
