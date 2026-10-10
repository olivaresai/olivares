// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package pricing

import (
	"testing"

	"github.com/olivaresai/olivares/connectors/modelprovider"
)

func TestClaudePricingForKnownModel(t *testing.T) {
	p, ok := ClaudePricingFor("claude-sonnet-4-20250514")
	if !ok {
		t.Fatal("expected pricing for claude-sonnet-4-20250514")
	}
	if p.InputPerMTokUSD != 3.00 {
		t.Errorf("InputPerMTokUSD = %v, want 3.00", p.InputPerMTokUSD)
	}
	if p.OutputPerMTokUSD != 15.00 {
		t.Errorf("OutputPerMTokUSD = %v, want 15.00", p.OutputPerMTokUSD)
	}
}

func TestClaudePricingForUnknownModel(t *testing.T) {
	_, ok := ClaudePricingFor("unknown-model-v99")
	if ok {
		t.Fatal("expected no pricing for unknown model")
	}
}

// TestClaudeFallbackCoversOnlyDatasetGaps: the curated fallback is not a second
// price table — it carries exactly the models the embedded dataset lacks, each
// with a reason. If the dataset adds one, the row must go.
func TestClaudeFallbackCoversOnlyDatasetGaps(t *testing.T) {
	for id := range claudeFallback {
		if _, ok := For(modelprovider.ProviderAnthropic, id); ok {
			t.Errorf("%q is priced by the dataset; the fallback row is a duplicate", id)
		}
	}
	if _, ok := claudeFallback["claude-mythos-5"]; !ok {
		t.Error("claude-mythos-5 (limited availability, not in the dataset) must stay in the fallback")
	}
}

func TestClaudePricingForDatedRates(t *testing.T) {
	for _, tc := range []struct {
		id                                    string
		input, output, write5m, write1h, read float64
	}{
		{"claude-sonnet-4-20250514", 3, 15, 3.75, 6, 0.30},
		{"claude-opus-4-20250514", 15, 75, 18.75, 30, 1.50},
		{"claude-haiku-3-5-20241022", 0.80, 4, 1, 1.60, 0.08},
	} {
		t.Run(tc.id, func(t *testing.T) {
			want := modelprovider.ModelPricing{
				InputPerMTokUSD: tc.input, OutputPerMTokUSD: tc.output,
				CacheWritePerMTokUSD: tc.write5m, CacheWrite1hPerMTokUSD: tc.write1h,
				CacheReadPerMTokUSD: tc.read,
				Currency:            "USD", AsOf: datasetAsOf, Source: modelprovider.PricingList,
			}
			if got, ok := ClaudePricingFor(tc.id); !ok || got != want {
				t.Fatalf("pricing = %+v, ok %v; want %+v", got, ok, want)
			}
		})
	}
}

func TestClaudePricingForDoesNotGuessSnapshots(t *testing.T) {
	// claude-3-5-haiku-20241022 and *-pro family spellings ARE dataset data now
	// and resolve; only ids no dataset row or pattern covers stay unpriced.
	for _, id := range []string{"", "claude-opus", "claude-opus-4-20250515", "claude-opus-9-9"} {
		if p, ok := ClaudePricingFor(id); ok || p != (modelprovider.ModelPricing{}) {
			t.Errorf("unknown %q returned pricing %+v, ok %v", id, p, ok)
		}
	}
}

func TestClaudePricingForCurrentRates(t *testing.T) {
	for _, tc := range []struct {
		id                                    string
		input, output, write5m, write1h, read float64
		cost                                  int64
	}{
		{"claude-opus-5-5", 4, 20, 5, 8, 0.20, 47000},
		{"claude-sonnet-5-5", 2, 10, 2.50, 4, 0.20, 23600},
		{"claude-haiku-4-5-20251001", 1, 5, 1.25, 2, 0.10, 11800},
		{"claude-fable-5-1", 10, 50, 12.50, 20, 0.25, 117250},
	} {
		t.Run(tc.id, func(t *testing.T) {
			want := modelprovider.ModelPricing{
				InputPerMTokUSD: tc.input, OutputPerMTokUSD: tc.output,
				CacheWritePerMTokUSD: tc.write5m, CacheWrite1hPerMTokUSD: tc.write1h,
				CacheReadPerMTokUSD: tc.read,
				Currency:            "USD", AsOf: datasetAsOf, Source: modelprovider.PricingList,
			}
			p, ok := ClaudePricingFor(tc.id)
			if !ok || p != want {
				t.Fatalf("pricing = %+v, ok %v; want %+v", p, ok, want)
			}
			cost := p.DeriveCostMicroUSD(modelprovider.Usage{
				InputTokens: 1000, OutputTokens: 2000,
				CacheCreation5mTokens: 400, CacheCreation1hTokens: 100, CacheReadTokens: 1000,
			})
			if cost != tc.cost {
				t.Fatalf("derived cost = %d micro-USD, want %d", cost, tc.cost)
			}
		})
	}
	// Dated snapshots of a known model keep that model's dataset tariff.
	for _, tc := range []struct {
		id    string
		input float64
	}{
		{"claude-opus-5-5-20260927", 4},
		{"claude-sonnet-5-5-20261001", 2},
	} {
		if p, ok := ClaudePricingFor(tc.id); !ok || p.InputPerMTokUSD != tc.input {
			t.Errorf("snapshot %q pricing = %+v ok %v, want input %v", tc.id, p, ok, tc.input)
		}
	}
	for _, id := range []string{"claude-opus-5-5-pro", "claude-fable-5-1-pro"} {
		if p, ok := ClaudePricingFor(id); ok || p != (modelprovider.ModelPricing{}) {
			t.Errorf("unknown %q returned pricing %+v, ok %v", id, p, ok)
		}
	}
}
