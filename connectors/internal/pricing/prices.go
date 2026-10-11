// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package pricing

import (
	"math"
	"time"

	genai_prices "github.com/pydantic/genai-prices/packages/go"

	"github.com/olivaresai/olivares/connectors/modelprovider"
)

// Embedded list prices (issue #202). The genai-prices Go package (MIT) compiles the
// pydantic/genai-prices dataset into the binary at release time, so an appliance
// with no network still prices every model the dataset covers, and the numbers are
// maintained upstream instead of in per-connector hand tables. It replaces the hand
// tables as the source of declared list prices; a curated fallback below keeps only
// the rows the dataset does not carry.
//
// datasetAsOf is the release date of the vendored dataset (genai-prices v0.1.9,
// 2026-09-25) — the date every dataset answer is stamped with. Bump it together
// with the module version.
const datasetAsOf = "2026-09-25"

// perMTok is the unit the derived rates are expressed in (USD per million
// tokens, the unit providers publish).
const perMTok = 1_000_000

// probeTokens is the volume each tier is probed at: 100,000 tokens, BELOW every
// volume-tier boundary the vendored dataset declares (its lowest tier start is
// 128,000, on Gemini long-context rows). The dataset selects a tier by total
// input tokens, so probing a full million would price tiered models at their
// long-context rate — claude-sonnet-4-5 at $6/MTok instead of its standard $3 —
// and every ordinary request would meter at double list price.
// rate = probeTotal × (perMTok / probeTokens); published rates carry at most a
// few decimals, so the rescale is rounded at ten decimal places to shed float
// noise without touching real data.
const probeTokens = 100_000

// rateScale and ratePrecision implement the rescale above.
const (
	rateScale     = perMTok / probeTokens // exactly 10
	ratePrecision = 10
)

// datasetTime pins conditional dataset prices (e.g. rates that changed on a
// date) to the snapshot the AsOf stamp names, so a lookup is deterministic on
// any appliance clock and the stamp never lies about which tariff was picked.
var datasetTime = time.Date(2026, 9, 25, 0, 0, 0, 0, time.UTC)

// rate derives one tier's per-MTok rate from the probe total.
func rate(total float64) float64 {
	return math.Round(total*rateScale*math.Pow(10, ratePrecision)) / math.Pow(10, ratePrecision)
}

// dataset is the release-embedded calculator. A NewCalculator failure leaves it
// nil and For answers not-found, so pricing fails open to the operator/fallback
// paths instead of erroring on the metering path. (The library's own package
// init panics on corrupt embedded data before this could matter; that data is
// compiled in, so a build that reaches boot has already passed that point.)
var dataset = newDataset()

func newDataset() *genai_prices.Calculator {
	c, err := genai_prices.NewCalculator()
	if err != nil {
		return nil
	}
	return c
}

// For returns the declared list pricing the embedded dataset carries for one
// provider's model, as USD per million tokens per tier. ok is false when the
// dataset has no such provider/model — it never guesses a price.
//
// Rates are derived by pricing probeTokens of each tier through the dataset
// calculator (see probeTokens: always the standard tier, never a long-context
// one). The dataset's usage buckets are hierarchical the way the OTel token
// conventions are: input_tokens is the INCLUSIVE input total and the cache
// buckets are subsets of it, so a tier probe declares the parent buckets too
// (all input as the probed tier) and its total is that tier's rate. A cache
// tier the dataset does not declare prices at the model's base input rate —
// the amount the provider charges for those tokens — so a derived rate equal
// to InputPerMTokUSD means "no separate tier", never a fabricated one.
func For(providerID, modelID string) (modelprovider.ModelPricing, bool) {
	if dataset == nil || modelID == "" || providerID == "" {
		return modelprovider.ModelPricing{}, false
	}
	total := func(u genai_prices.Usage) (float64, bool) {
		res, err := dataset.Calculate(genai_prices.PriceRequest{
			Usage: u, Model: modelID, ProviderID: providerID, Timestamp: datasetTime,
		})
		if err != nil {
			return 0, false
		}
		return res.TotalPrice, true
	}
	input, ok := total(genai_prices.Usage{genai_prices.UsageInputTokens: probeTokens})
	if !ok {
		return modelprovider.ModelPricing{}, false
	}
	output, ok := total(genai_prices.Usage{genai_prices.UsageOutputTokens: probeTokens})
	if !ok {
		return modelprovider.ModelPricing{}, false
	}
	if input <= 0 && output <= 0 {
		// A row priced only in non-token units (per audio hour, per page) has no
		// per-token list price to declare; reporting it as a $0 token price would
		// meter real usage as free.
		return modelprovider.ModelPricing{}, false
	}
	// Every input token of the probe is the probed cache tier, so each total is
	// that tier's rate. A probe that FAILS after the input probe succeeded is a
	// data anomaly, not an undeclared tier (undeclared tiers price at the base
	// input rate): the whole lookup answers not-found rather than return a
	// silently zero-priced tier.
	cacheRead, ok := total(genai_prices.Usage{
		genai_prices.UsageInputTokens: probeTokens, genai_prices.UsageCacheReadTokens: probeTokens,
	})
	if !ok {
		return modelprovider.ModelPricing{}, false
	}
	cacheWrite5m, ok := total(genai_prices.Usage{
		genai_prices.UsageInputTokens: probeTokens, genai_prices.UsageCacheWriteTokens: probeTokens,
		genai_prices.UsageCacheWrite5MTokens: probeTokens,
	})
	if !ok {
		return modelprovider.ModelPricing{}, false
	}
	cacheWrite1h, ok := total(genai_prices.Usage{
		genai_prices.UsageInputTokens: probeTokens, genai_prices.UsageCacheWriteTokens: probeTokens,
		genai_prices.UsageCacheWrite1HTokens: probeTokens,
	})
	if !ok {
		return modelprovider.ModelPricing{}, false
	}
	return modelprovider.ModelPricing{
		InputPerMTokUSD:     rate(input),
		OutputPerMTokUSD:    rate(output),
		CacheReadPerMTokUSD: rate(cacheRead),
		// The untiered cache-write rate is the 5-minute tier: the standard tier
		// providers publish, and the rate DeriveCostMicroUSD applies to untiered
		// cache-write usage.
		CacheWritePerMTokUSD:   rate(cacheWrite5m),
		CacheWrite1hPerMTokUSD: rate(cacheWrite1h),
		Currency:               "USD",
		AsOf:                   datasetAsOf,
		Source:                 modelprovider.PricingList,
	}, true
}
