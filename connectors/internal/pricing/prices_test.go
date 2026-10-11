// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package pricing

import (
	"sync"
	"testing"

	"github.com/olivaresai/olivares/connectors/modelprovider"
)

// TestClaudePricingForEmbeddedDataset is the red test of the embedded-price switch:
// the shared Claude lookup answers from the genai-prices dataset embedded at release
// time (issue #202), so it resolves models the hand table never declared and stamps
// every dataset answer with the single dataset snapshot date.
func TestClaudePricingForEmbeddedDataset(t *testing.T) {
	// claude-opus-4-8 was never in the hand table; the dataset prices it (and its
	// whole 4.x generation) at the verified 5/25 tier with both cache-write tiers.
	p, ok := ClaudePricingFor("claude-opus-4-8")
	if !ok {
		t.Fatal("expected embedded dataset pricing for claude-opus-4-8")
	}
	want := modelprovider.ModelPricing{
		InputPerMTokUSD: 5, OutputPerMTokUSD: 25,
		CacheWritePerMTokUSD: 6.25, CacheWrite1hPerMTokUSD: 10, CacheReadPerMTokUSD: 0.50,
		Currency: "USD", AsOf: datasetAsOf, Source: modelprovider.PricingList,
	}
	if p != want {
		t.Fatalf("pricing = %+v, want %+v", p, want)
	}
	// A dated snapshot of a known model keeps that model's tariff through the
	// dataset's family patterns instead of falling through to a generic family.
	if p, ok := ClaudePricingFor("claude-opus-5-5-20260927"); !ok || p.InputPerMTokUSD != 4 {
		t.Fatalf("snapshot pricing = %+v ok=%v, want the opus-5-5 $4 tariff", p, ok)
	}
	// The dated Haiku 3.5 API spelling resolves through its dataset alias at the
	// published 0.80/4 rates (the dataset spells the same model claude-3-5-haiku).
	if p, ok := ClaudePricingFor("claude-haiku-3-5-20241022"); !ok || p.InputPerMTokUSD != 0.80 || p.CacheReadPerMTokUSD != 0.08 {
		t.Fatalf("haiku-3-5 pricing = %+v ok=%v, want dataset 0.80/4 with 0.08 cache read", p, ok)
	}
}

// TestClaudePricingForFallbackBeyondDataset pins the one Claude price the dataset
// does not carry: Mythos 5 is limited-availability with a published list price
// Olivares verified by hand, so the fallback keeps pricing it (never a guessed 0).
func TestClaudePricingForFallbackBeyondDataset(t *testing.T) {
	p, ok := ClaudePricingFor("claude-mythos-5")
	if !ok {
		t.Fatal("expected fallback pricing for claude-mythos-5 beyond the dataset")
	}
	if p.InputPerMTokUSD != 10 || p.OutputPerMTokUSD != 50 || p.CacheReadPerMTokUSD != 1 {
		t.Fatalf("mythos pricing = %+v, want the verified 10/50 with $1 cache read", p)
	}
}

// TestForDoesNotGuess guards the never-guess rule at the dataset boundary: an
// unknown model, an empty id and an unknown or empty provider stay unpriced.
func TestForDoesNotGuess(t *testing.T) {
	for _, tc := range []struct{ provider, model string }{
		{"anthropic", "claude-opus-9-9"},
		{"anthropic", ""},
		{"anthropic", "claude-opus-4-20250515"}, // wrong snapshot date: no pattern guess
		{"not-a-provider", "claude-opus-5-5"},
		{"", "claude-opus-5-5"},
	} {
		t.Run(tc.provider+"/"+tc.model, func(t *testing.T) {
			if p, ok := For(tc.provider, tc.model); ok || p != (modelprovider.ModelPricing{}) {
				t.Fatalf("For(%q, %q) = %+v ok=%v, want no pricing", tc.provider, tc.model, p, ok)
			}
		})
	}
}

// TestForUsesStandardTierForTieredModels pins the probe volume's reason for
// existing: the dataset carries long-context volume tiers (claude-sonnet-4-5
// doubles above 200k input tokens), and For must answer the STANDARD tier a
// pricing page headlines — never the long-context rate an oversized probe
// would select. A full-million-token probe returns 6/15/0.6/7.5/12 here (the
// output-only probe carries no input tokens, so its rate stays on the base
// tier — the mix reviewers found incoherent, not a clean doubling).
func TestForUsesStandardTierForTieredModels(t *testing.T) {
	for _, id := range []string{"claude-sonnet-4-5", "claude-sonnet-4-5-20250929"} {
		p, ok := For(modelprovider.ProviderAnthropic, id)
		if !ok {
			t.Fatalf("expected dataset pricing for %s", id)
		}
		want := modelprovider.ModelPricing{
			InputPerMTokUSD: 3, OutputPerMTokUSD: 15,
			CacheWritePerMTokUSD: 3.75, CacheWrite1hPerMTokUSD: 6, CacheReadPerMTokUSD: 0.30,
			Currency: "USD", AsOf: datasetAsOf, Source: modelprovider.PricingList,
		}
		if p != want {
			t.Fatalf("%s = %+v, want the standard tier %+v", id, p, want)
		}
	}
}

// TestForCacheTiersDefaultToInputRate pins the tier semantics For documents: a
// cache tier the dataset does not declare prices at the model's base input rate
// (gpt-4o declares a cache-read tier but no cache-write tier), never a
// fabricated 0 and never an error.
func TestForCacheTiersDefaultToInputRate(t *testing.T) {
	p, ok := For("openai", "gpt-4o")
	if !ok {
		t.Fatal("expected dataset pricing for openai/gpt-4o")
	}
	if p.InputPerMTokUSD <= 0 {
		t.Fatalf("input rate = %v, want a declared positive rate", p.InputPerMTokUSD)
	}
	if p.CacheReadPerMTokUSD == 0 || p.CacheReadPerMTokUSD == p.InputPerMTokUSD {
		t.Fatalf("cache-read rate = %v, want the distinct declared tier (1.25 vs input 2.5)", p.CacheReadPerMTokUSD)
	}
	if p.CacheWritePerMTokUSD != p.InputPerMTokUSD || p.CacheWrite1hPerMTokUSD != p.InputPerMTokUSD {
		t.Fatalf("undeclared cache-write tiers = %v/%v, want the base input rate %v",
			p.CacheWritePerMTokUSD, p.CacheWrite1hPerMTokUSD, p.InputPerMTokUSD)
	}
}

// TestDatasetAliasesStayResolvable: every alias must point at a dataset row, so
// an upstream rename cannot silently strand an API spelling (its price would
// fall back to hand data or vanish).
func TestDatasetAliasesStayResolvable(t *testing.T) {
	for apiID, datasetID := range datasetAliases {
		if _, ok := For(modelprovider.ProviderAnthropic, datasetID); !ok {
			t.Errorf("alias %q -> %q: the dataset spelling no longer resolves", apiID, datasetID)
		}
	}
}

// TestClaudePricingForConcurrentLookups: meters resolve prices from concurrent
// requests through the one shared embedded calculator; run under -race this
// fails if that sharing is not safe, and it pins that concurrent lookups return
// whole rows (no torn rates) for every lookup class.
func TestClaudePricingForConcurrentLookups(t *testing.T) {
	cases := map[string]float64{ // model -> input rate
		"claude-opus-5-5":           4,
		"claude-opus-4-8":           5,
		"claude-haiku-3-5-20241022": 0.80, // alias path
		"claude-mythos-5":           10,   // fallback path
		"claude-opus-9-9":           0,    // not-found path
	}
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				for id, want := range cases {
					p, ok := ClaudePricingFor(id)
					if want == 0 {
						if ok {
							t.Errorf("%q unexpectedly priced %+v", id, p)
						}
						continue
					}
					if !ok || p.InputPerMTokUSD != want || p.AsOf == "" {
						t.Errorf("%q = %+v ok=%v, want input %v with a date", id, p, ok, want)
					}
				}
			}
		}()
	}
	wg.Wait()
}
