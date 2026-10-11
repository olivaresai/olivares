// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Package pricing provides the declared list prices connectors meter cost with
// when a provider has no billing API. Prices come from the genai-prices dataset
// embedded in the binary at release time (see prices.go); the curated tables in
// this file carry only what that dataset does not, each stamped with its
// declaration date. None of this is fabricated telemetry: it is declared price
// data, and operators still override it (modelprovider.PricingOperator).
package pricing

import "github.com/olivaresai/olivares/connectors/modelprovider"

// datasetAliases maps an Anthropic API model spelling the dataset does not use
// to the dataset's spelling of the same model, so the API id a response reports
// resolves without duplicating its numbers here.
var datasetAliases = map[string]string{
	// Anthropic's dated Haiku 3.5 id; the dataset spells it generation-first.
	"claude-haiku-3-5-20241022": "claude-3-5-haiku-20241022",
}

// claudeFallback holds the Claude prices the embedded dataset does not carry.
// Every entry names WHY it is not in the dataset; when the dataset adds the
// model, the row and its reason go.
var claudeFallback = map[string]modelprovider.ModelPricing{
	// Limited availability (Project Glasswing, not generally available), so the
	// dataset has no row; the published Fable 5 pricing it shares was verified on
	// the pricing + launch pages 2026-06-09.
	"claude-mythos-5": {
		InputPerMTokUSD: 10, OutputPerMTokUSD: 50,
		CacheWritePerMTokUSD: 12.50, CacheWrite1hPerMTokUSD: 20, CacheReadPerMTokUSD: 1,
		Currency: "USD", AsOf: "2026-06-09", Source: modelprovider.PricingList,
	},
}

// ClaudePricingFor returns declared Claude list pricing for modelID: the
// embedded dataset first (see For), then the curated fallback for models the
// dataset lacks. Returns ok=false for unknown models — never guesses a price.
func ClaudePricingFor(modelID string) (modelprovider.ModelPricing, bool) {
	if p, ok := For(modelprovider.ProviderAnthropic, datasetAliases[modelID]); ok {
		return p, true
	}
	if p, ok := For(modelprovider.ProviderAnthropic, modelID); ok {
		return p, true
	}
	p, ok := claudeFallback[modelID]
	return p, ok
}
