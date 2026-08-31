// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package openai

import (
	"github.com/olivaresai/olivares/connectors/modelprovider"
	"github.com/olivaresai/olivares/sdk/model"
)

// This file holds the declared, operator-maintainable OpenAI catalog data:
// per-family list pricing and the per-family capability set. Live mode replaces
// the offline model list with the provider's /v1/models response and enriches each
// model with the pricing/capabilities declared here; pricing is matched by model
// family prefix (longest match wins, so gpt-4o-mini beats gpt-4o) so a new model
// version inherits its family's declared list price until the operator overrides
// it. These are list prices to VERIFY against openai.com/pricing — not fabricated
// metrics (docs/SECURITY-HARDENING.md contract).

// pricingAsOf stamps the declared prices with the date they were recorded.
const pricingAsOf = "2026-07-04"

// openAIStackCapabilities is the capability set surfaced for the modern GPT-4o /
// GPT-4.1 families (README.md): they support streaming, tool/function calling,
// vision input, structured outputs, prompt caching, batch and the files API. Only
// constants that exist in modelprovider/catalog.go are used (no invented flags).
var openAIStackCapabilities = []modelprovider.Capability{
	modelprovider.CapStreaming,
	modelprovider.CapToolUse,
	modelprovider.CapVision,
	modelprovider.CapStructuredOutputs,
	modelprovider.CapPromptCaching,
	modelprovider.CapBatch,
	modelprovider.CapFiles,
}

// family is a declared price + capability set keyed by a model-id prefix. Matching
// by prefix means a new "gpt-4o-*" version inherits the gpt-4o tier until the
// operator updates the table; longest-prefix-first resolution keeps the more
// specific mini/nano tiers from being shadowed by their parent family.
type family struct {
	prefix       string
	pricing      modelprovider.ModelPricing
	capabilities []modelprovider.Capability
	context      int64
	maxOutput    int64
}

// openAIFamilies are matched longest-prefix-first by pricingFor. Prices are USD per
// million tokens (list, AsOf pricingAsOf unless an entry specifies otherwise). OpenAI
// bills a cached-input read tier. Before GPT-5.6 there was no separate cache-write
// charge, so CacheWritePerMTokUSD stays 0 for those families; GPT-5.6 and later bill
// cache writes separately. VERIFY against openai.com/pricing.
var openAIFamilies = []family{
	{
		prefix: "gpt-5.6-terra",
		pricing: modelprovider.ModelPricing{
			InputPerMTokUSD: 2.50, OutputPerMTokUSD: 15.00,
			CacheWritePerMTokUSD: 3.125, CacheReadPerMTokUSD: 0.25,
			Currency: "USD", AsOf: "2026-07-15", Source: modelprovider.PricingList,
		},
		capabilities: openAIStackCapabilities,
		context:      1050000,
		maxOutput:    128000,
	},
	{
		prefix: "gpt-5.6-luna",
		pricing: modelprovider.ModelPricing{
			InputPerMTokUSD: 1.00, OutputPerMTokUSD: 6.00,
			CacheWritePerMTokUSD: 1.25, CacheReadPerMTokUSD: 0.10,
			Currency: "USD", AsOf: "2026-07-15", Source: modelprovider.PricingList,
		},
		capabilities: openAIStackCapabilities,
		context:      1050000,
		maxOutput:    128000,
	},
	{
		prefix: "gpt-5.6",
		pricing: modelprovider.ModelPricing{
			InputPerMTokUSD: 5.00, OutputPerMTokUSD: 30.00,
			CacheWritePerMTokUSD: 6.25, CacheReadPerMTokUSD: 0.50,
			Currency: "USD", AsOf: "2026-07-15", Source: modelprovider.PricingList,
		},
		capabilities: openAIStackCapabilities,
		context:      1050000,
		maxOutput:    128000,
	},
	{
		prefix: "gpt-5.5",
		pricing: modelprovider.ModelPricing{
			InputPerMTokUSD: 5.00, OutputPerMTokUSD: 30.00,
			CacheWritePerMTokUSD: 0, CacheReadPerMTokUSD: 0.50,
			Currency: "USD", AsOf: pricingAsOf, Source: modelprovider.PricingList,
		},
		capabilities: openAIStackCapabilities,
		context:      1050000,
		maxOutput:    128000,
	},
	{
		prefix: "gpt-4o-mini",
		pricing: modelprovider.ModelPricing{
			InputPerMTokUSD: 0.15, OutputPerMTokUSD: 0.60,
			CacheWritePerMTokUSD: 0, CacheReadPerMTokUSD: 0.075,
			Currency: "USD", AsOf: pricingAsOf, Source: modelprovider.PricingList,
		},
		capabilities: openAIStackCapabilities,
	},
	{
		prefix: "gpt-4o",
		pricing: modelprovider.ModelPricing{
			InputPerMTokUSD: 2.50, OutputPerMTokUSD: 10.00,
			CacheWritePerMTokUSD: 0, CacheReadPerMTokUSD: 1.25,
			Currency: "USD", AsOf: pricingAsOf, Source: modelprovider.PricingList,
		},
		capabilities: openAIStackCapabilities,
	},
	{
		prefix: "gpt-4.1-mini",
		pricing: modelprovider.ModelPricing{
			InputPerMTokUSD: 0.40, OutputPerMTokUSD: 1.60,
			CacheWritePerMTokUSD: 0, CacheReadPerMTokUSD: 0.10,
			Currency: "USD", AsOf: pricingAsOf, Source: modelprovider.PricingList,
		},
		capabilities: openAIStackCapabilities,
	},
	{
		prefix: "gpt-4.1-nano",
		pricing: modelprovider.ModelPricing{
			InputPerMTokUSD: 0.10, OutputPerMTokUSD: 0.40,
			CacheWritePerMTokUSD: 0, CacheReadPerMTokUSD: 0.025,
			Currency: "USD", AsOf: pricingAsOf, Source: modelprovider.PricingList,
		},
		capabilities: openAIStackCapabilities,
	},
	{
		prefix: "gpt-4.1",
		pricing: modelprovider.ModelPricing{
			InputPerMTokUSD: 2.00, OutputPerMTokUSD: 8.00,
			CacheWritePerMTokUSD: 0, CacheReadPerMTokUSD: 0.50,
			Currency: "USD", AsOf: pricingAsOf, Source: modelprovider.PricingList,
		},
		capabilities: openAIStackCapabilities,
	},
}

// pricingFor returns the declared pricing and capability set for a model id,
// matched by the longest family prefix. ok is false when no family matches (the
// connector then leaves Model.Pricing nil rather than guess a price; an o-series or
// otherwise unknown id falls here).
func pricingFor(modelID string) (modelprovider.ModelPricing, []modelprovider.Capability, int64, int64, bool) {
	best := -1
	for i, f := range openAIFamilies {
		if hasPrefix(modelID, f.prefix) {
			if best < 0 || len(f.prefix) > len(openAIFamilies[best].prefix) {
				best = i
			}
		}
	}
	if best < 0 {
		return modelprovider.ModelPricing{}, nil, 0, 0, false
	}
	f := openAIFamilies[best]
	return f.pricing, f.capabilities, f.context, f.maxOutput, true
}

// hasPrefix is strings.HasPrefix without importing strings into this small file.
func hasPrefix(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}

// declaredModelIDs is the offline fallback model list returned by Snapshot when no
// credential is configured (live mode replaces it with /v1/models). It names the
// current GPT-4o / GPT-4.1 models so the catalog is useful air-gapped; operators
// refresh it from the API. Each gets its family pricing + capability set.
var declaredModelIDs = []struct {
	id          string
	displayName string
}{
	{"gpt-5.5", "GPT-5.5"},
	{"gpt-4o", "GPT-4o"},
	{"gpt-4o-mini", "GPT-4o mini"},
	{"gpt-4.1", "GPT-4.1"},
}

// openAIModelDeprecations is exact-id lifecycle data from
// https://developers.openai.com/api/docs/deprecations, verified 2026-07-04. Do not
// prefix-match this table: a live dated variant must not inherit a deprecation row
// unless the authority names that exact model id.
var openAIModelDeprecations = map[string]struct {
	deprecatedOn   string
	retiresOn      string
	replacementRef string
}{
	"gpt-5-2025-08-07":      {"2026-06-11", "2026-12-11", "gpt-5.5"},
	"gpt-5-mini-2025-08-07": {"2026-06-11", "2026-12-11", "gpt-5.4-mini"},
	"gpt-5-nano-2025-08-07": {"2026-06-11", "2026-12-11", "gpt-5.4-nano"},
	"gpt-5-pro-2025-10-06":  {"2026-06-11", "2026-12-11", "gpt-5.5-pro"},
	"o3-2025-04-16":         {"2026-06-11", "2026-12-11", "gpt-5.5"},
	"o3-pro-2025-06-10":     {"2026-06-11", "2026-12-11", "gpt-5.5-pro"},

	"computer-use-preview": {"2026-04-22", "2026-07-23", ""},
	"gpt-5-chat-latest":    {"2026-04-22", "2026-07-23", ""},
	"gpt-5-codex":          {"2026-04-22", "2026-07-23", ""},
	"gpt-5.1-chat-latest":  {"2026-04-22", "2026-07-23", ""},
	"gpt-5.1-codex":        {"2026-04-22", "2026-07-23", ""},
	"o3-deep-research":     {"2026-04-22", "2026-07-23", ""},

	"gpt-3.5-turbo-0125": {"2026-04-22", "2026-10-23", ""},
	"gpt-4-0613":         {"2026-04-22", "2026-10-23", ""},
	"gpt-4-turbo":        {"2026-04-22", "2026-10-23", ""},
	"gpt-4o-2024-05-13":  {"2026-04-22", "2026-10-23", ""},
	"o1-2024-12-17":      {"2026-04-22", "2026-10-23", ""},
	"o1-pro-2025-03-19":  {"2026-04-22", "2026-10-23", ""},
	"o3-mini-2025-01-31": {"2026-04-22", "2026-10-23", ""},
}

// buildModel assembles a modelprovider.Model for an OpenAI model id, enriching it
// with the declared family pricing and capability set. It is the single place live
// and offline catalog builds converge. A model with no family match keeps nil
// pricing and no capabilities (the connector never guesses a price).
func buildModel(providerRef, id, displayName string) modelprovider.Model {
	m := modelprovider.Model{
		ProviderRef: providerRef,
		Ref:         id,
		DisplayName: displayName,
	}
	if p, caps, contextWindow, maxOutput, ok := pricingFor(id); ok {
		pc := p
		m.Pricing = &pc
		m.Capabilities = append([]modelprovider.Capability(nil), caps...)
		m.ContextWindow = contextWindow
		m.MaxOutputTokens = maxOutput
	}
	if r, ok := openAIModelDeprecations[id]; ok {
		m.Deprecated = true
		m.Retirements = []modelprovider.ModelRetirement{{
			Surface:        model.GatewayDirect,
			DeprecatedOn:   r.deprecatedOn,
			RetiresOn:      r.retiresOn,
			ReplacementRef: r.replacementRef,
			AsOf:           pricingAsOf,
		}}
	}
	return m
}
