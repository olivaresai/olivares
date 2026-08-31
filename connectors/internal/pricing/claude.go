// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Package pricing provides shared declared model pricing tables for connectors
// that meter cost around the inference path (no billing API). Each table is a
// maintainable snapshot of the provider's published list prices — not fabricated
// telemetry — and carries its verification date. Operators should verify against
// the provider's pricing page; negotiated or local-inference rates are an
// operator concern (modelprovider.PricingOperator).
package pricing

import "github.com/olivaresai/olivares/connectors/modelprovider"

// ClaudePricingFor returns declared Claude list pricing for modelID.
// Returns ok=false for unknown models — never guesses a price.
func ClaudePricingFor(modelID string) (modelprovider.ModelPricing, bool) {
	p, ok := Claude[modelID]
	return p, ok
}

// Claude holds declared Claude list pricing (USD per million tokens, jun-2026).
// Source: anthropic.com/pricing — VERIFIED.
var Claude = map[string]modelprovider.ModelPricing{
	"claude-sonnet-4-20250514": {
		InputPerMTokUSD:     3.00,
		OutputPerMTokUSD:    15.00,
		CacheReadPerMTokUSD: 0.30,
	},
	"claude-opus-4-20250514": {
		InputPerMTokUSD:     15.00,
		OutputPerMTokUSD:    75.00,
		CacheReadPerMTokUSD: 1.50,
	},
	"claude-haiku-3-5-20241022": {
		InputPerMTokUSD:     0.80,
		OutputPerMTokUSD:    4.00,
		CacheReadPerMTokUSD: 0.08,
	},
}
