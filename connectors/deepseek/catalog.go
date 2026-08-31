// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package deepseek

import (
	"github.com/olivaresai/olivares/connectors/modelprovider"
	"github.com/olivaresai/olivares/sdk/model"
)

// catalog.go holds the declared DeepSeek model family + list pricing, and the helpers
// that expose the catalog (live GET /models, enriched with the declared pricing +
// capabilities) to module X through CatalogProvider.
//
// The Models API (GET /models) is VERIFIED-SHAPE and REAL: it returns the OpenAI-
// compatible {object:"list",data:[{id,object,created,owned_by},...]} shape. Unlike
// Mistral/OpenAI, DeepSeek models do NOT carry per-model capability booleans in the API
// response, so capabilities are always from the declared family (CapabilitySource="live"
// means the model ID came from the live API; the capabilities are still declared).
//
// Prices are list values verified against api-docs.deepseek.com /updates, /news/news260424
// and /quick_start/pricing on 2026-07-04 (stamped AsOf, Source=PricingList) — never
// fabricated telemetry (ARCHITECTURE.md).

// pricingAsOf stamps the declared prices with the date they were recorded/verified.
const pricingAsOf = "2026-07-04"

// chatCaps is the capability set for DeepSeek's chat model: streaming, tool/function
// calling and structured outputs. Only constants that exist in modelprovider/catalog.go
// are used (no invented flags).
var chatCaps = []modelprovider.Capability{
	modelprovider.CapStreaming,
	modelprovider.CapToolUse,
	modelprovider.CapStructuredOutputs,
}

// reasonerCaps is the capability set for DeepSeek's reasoning model: streaming plus
// extended thinking (the "thinking" tokens DeepSeek Reasoner produces). Tool use is
// supported by R1 as well.
var reasonerCaps = []modelprovider.Capability{
	modelprovider.CapStreaming,
	modelprovider.CapToolUse,
	modelprovider.CapExtendedThinking,
}

// v4Caps reflects the v4 family serving both non-thinking and thinking modes (verified
// 2026-07-04): keep the chat surface flags and include extended thinking.
var v4Caps = []modelprovider.Capability{
	modelprovider.CapStreaming,
	modelprovider.CapToolUse,
	modelprovider.CapStructuredOutputs,
	modelprovider.CapExtendedThinking,
}

// family is a declared price + capability set keyed by a model-id prefix, matched
// longest-prefix-first (so a dated id like "deepseek-chat-20260601" beats the base
// "deepseek-chat"). Prices are USD per million tokens (list, AsOf pricingAsOf).
// VERIFY against platform.deepseek.com/api-docs/pricing.
type family struct {
	prefix       string
	pricing      modelprovider.ModelPricing
	capabilities []modelprovider.Capability
	context      int64 // declared max context tokens (0 = unknown / let live API decide)
	maxOutput    int64
	deprecated   bool
	retirements  []modelprovider.ModelRetirement
}

// price builds a USD/MTok list ModelPricing with an optional cache-read tier. DeepSeek
// offers cache-read pricing (called "cache hit" on their pricing page) but no cache-write
// tier (the caching is server-side and automatic).
func price(in, out, cacheRead float64) modelprovider.ModelPricing {
	return modelprovider.ModelPricing{
		InputPerMTokUSD:     in,
		OutputPerMTokUSD:    out,
		CacheReadPerMTokUSD: cacheRead,
		Currency:            "USD",
		AsOf:                pricingAsOf,
		Source:              modelprovider.PricingList,
	}
}

// deepseekFamilies are matched longest-prefix-first by familyFor. Prices VERIFIED against
// api-docs.deepseek.com/quick_start/pricing (USD/MTok) on pricingAsOf. Context windows:
// deepseek-v4-flash/pro = 1M with 384K max output; legacy deepseek-chat (V3) and
// deepseek-reasoner (R1) keep their published 64K windows and are deprecated on the
// direct first-party surface with retirement after 2026-07-24 15:59 UTC.
var deepseekFamilies = []family{
	{prefix: "deepseek-v4-flash", pricing: price(0.14, 0.28, 0.0028), capabilities: v4Caps, context: 1_000_000, maxOutput: 384_000},
	{prefix: "deepseek-v4-pro", pricing: price(0.435, 0.87, 0.003625), capabilities: v4Caps, context: 1_000_000, maxOutput: 384_000},
	{prefix: "deepseek-chat", pricing: price(0.27, 1.10, 0.07), capabilities: chatCaps, context: 65536, deprecated: true, retirements: deepseekLegacyRetirements()},
	{prefix: "deepseek-reasoner", pricing: price(0.55, 2.19, 0.14), capabilities: reasonerCaps, context: 65536, deprecated: true, retirements: deepseekLegacyRetirements()},
}

// declaredModels is the offline DeepSeek model list returned by Snapshot when no
// credential is configured (live mode replaces it with GET /models). It names the
// current DeepSeek models so the catalog is useful air-gapped; operators refresh it
// from the API as the family evolves.
var declaredModels = []struct{ id, displayName string }{
	{"deepseek-v4-flash", "DeepSeek V4 Flash"},
	{"deepseek-v4-pro", "DeepSeek V4 Pro"},
	{"deepseek-chat", "DeepSeek Chat (V3)"},
	{"deepseek-reasoner", "DeepSeek Reasoner (R1)"},
}

func deepseekLegacyRetirements() []modelprovider.ModelRetirement {
	return []modelprovider.ModelRetirement{{
		Surface:        model.GatewayDirect,
		DeprecatedOn:   "2026-04-24",
		RetiresOn:      "2026-07-24",
		ReplacementRef: "deepseek-v4-flash",
		AsOf:           "2026-07-04",
	}}
}

// familyFor returns the declared pricing/capabilities/context for a model id, matched by
// the longest family prefix. ok is false when no family matches (the connector then leaves
// Model.Pricing nil rather than guess a price).
func familyFor(modelID string) (family, bool) {
	best := -1
	for i, f := range deepseekFamilies {
		if hasPrefix(modelID, f.prefix) {
			if best < 0 || len(f.prefix) > len(deepseekFamilies[best].prefix) {
				best = i
			}
		}
	}
	if best < 0 {
		return family{}, false
	}
	return deepseekFamilies[best], true
}

// hasPrefix is strings.HasPrefix without importing strings into this file.
func hasPrefix(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}

// buildDeclaredModel assembles a declared (offline) DeepSeek model, enriching it with the
// family list pricing + capabilities. CapabilitySource is "declared".
func buildDeclaredModel(id, displayName string) modelprovider.Model {
	m := modelprovider.Model{
		ProviderRef:      modelprovider.ProviderDeepSeek,
		Ref:              id,
		DisplayName:      displayName,
		CapabilitySource: "declared",
	}
	if f, ok := familyFor(id); ok {
		pc := f.pricing
		m.Pricing = &pc
		m.Capabilities = append([]modelprovider.Capability(nil), f.capabilities...)
		m.ContextWindow = f.context
		m.MaxOutputTokens = f.maxOutput
		m.Deprecated = f.deprecated
		m.Retirements = append([]modelprovider.ModelRetirement(nil), f.retirements...)
	}
	return m
}

// declaredCatalogModels builds the offline DeepSeek model list.
func declaredCatalogModels() []modelprovider.Model {
	out := make([]modelprovider.Model, 0, len(declaredModels))
	for _, d := range declaredModels {
		out = append(out, buildDeclaredModel(d.id, d.displayName))
	}
	return out
}
