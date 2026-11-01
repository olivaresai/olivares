// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package pricing

import (
	"errors"
	"os"
	"strings"
	"sync"
	"testing"

	genai_prices "github.com/pydantic/genai-prices/packages/go"

	"github.com/olivaresai/olivares/connectors/modelprovider"
)

// TestDatasetAsOfMatchesLinkedModule ties the snapshot date to the dataset the module
// requires: a bump of the requirement in connectors/go.mod that leaves datasetVersion
// (and so datasetAsOf) behind would stamp every price with an older release's date.
func TestDatasetAsOfMatchesLinkedModule(t *testing.T) {
	const module = "github.com/pydantic/genai-prices/packages/go"
	data, err := os.ReadFile("../../go.mod")
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		if f := strings.Fields(line); len(f) >= 2 && f[0] == module {
			if f[1] != datasetVersion {
				t.Fatalf("connectors/go.mod requires %s %s, but datasetVersion is %s: set datasetVersion and datasetAsOf to that release and its date",
					module, f[1], datasetVersion)
			}
			return
		}
	}
	t.Fatalf("connectors/go.mod does not require %s", module)
}

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

// TestForRetriesTransientCalculateError reproduces the red main of issue #2620. The
// dataset guards each regex match with a 100 ms wall-clock timeout, and a CPU stall
// can trip it on a model the dataset does carry. One errored lookup must not read as
// an unpriced model: the lookup runs again and answers the tariff.
func TestForRetriesTransientCalculateError(t *testing.T) {
	original := calculate
	t.Cleanup(func() { calculate = original })
	timeouts := 1
	calculate = func(request genai_prices.PriceRequest) (genai_prices.PriceCalculation, error) {
		if timeouts > 0 {
			timeouts--
			return genai_prices.PriceCalculation{}, errors.New("match timeout after 100ms")
		}
		return original(request)
	}
	p, ok := For(modelprovider.ProviderAnthropic, "claude-opus-5-5")
	if !ok || p.InputPerMTokUSD != 4 || p.OutputPerMTokUSD != 20 {
		t.Fatalf("pricing = %+v ok=%v, want the opus-5-5 $4/$20 tariff after one timed-out match", p, ok)
	}
}

// TestForAnswersNotFoundWhenEveryAttemptErrors keeps the never-guess rule under the
// retry: a lookup that keeps failing still answers not-found, never a price.
func TestForAnswersNotFoundWhenEveryAttemptErrors(t *testing.T) {
	original := calculate
	t.Cleanup(func() { calculate = original })
	calls := 0
	calculate = func(genai_prices.PriceRequest) (genai_prices.PriceCalculation, error) {
		calls++
		return genai_prices.PriceCalculation{}, errors.New("match timeout after 100ms")
	}
	if p, ok := For(modelprovider.ProviderAnthropic, "claude-opus-5-5"); ok || p != (modelprovider.ModelPricing{}) {
		t.Fatalf("pricing = %+v ok=%v, want no price while every attempt errors", p, ok)
	}
	if calls != 3 {
		t.Fatalf("calls = %d, want three attempts before not-found", calls)
	}
}

// TestForAnswersNotFoundWhenALaterProbeKeepsFailing guards the probes after input and
// output: a cache tier that keeps failing must not read as a free $0 tier.
func TestForAnswersNotFoundWhenALaterProbeKeepsFailing(t *testing.T) {
	original := calculate
	t.Cleanup(func() { calculate = original })
	calculate = func(request genai_prices.PriceRequest) (genai_prices.PriceCalculation, error) {
		if _, cacheRead := request.Usage[genai_prices.UsageCacheReadTokens]; cacheRead {
			return genai_prices.PriceCalculation{}, errors.New("match timeout after 100ms")
		}
		return original(request)
	}
	if p, ok := For(modelprovider.ProviderAnthropic, "claude-opus-5-5"); ok || p != (modelprovider.ModelPricing{}) {
		t.Fatalf("pricing = %+v ok=%v, want no price while the cache-read probe keeps failing", p, ok)
	}
}

// TestForDoesNotRetryAMissingModel: a model the dataset lacks errors the same way on
// every run, so For looks it up once and answers not-found.
func TestForDoesNotRetryAMissingModel(t *testing.T) {
	original := calculate
	t.Cleanup(func() { calculate = original })
	calls := 0
	calculate = func(request genai_prices.PriceRequest) (genai_prices.PriceCalculation, error) {
		calls++
		return original(request)
	}
	if p, ok := For(modelprovider.ProviderAnthropic, "claude-opus-9-9"); ok || p != (modelprovider.ModelPricing{}) {
		t.Fatalf("pricing = %+v ok=%v, want no price for an unknown model", p, ok)
	}
	if calls != 1 {
		t.Fatalf("calls = %d, want one lookup for a missing model", calls)
	}
}
