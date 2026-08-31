// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package gemini

import (
	"strings"

	"github.com/olivaresai/olivares/connectors/modelprovider"
)

// This file holds the declared, operator-maintainable Gemini catalog data:
// per-family list pricing and the declared capability set. Live mode replaces the
// offline model list with the provider's /v1beta/models response and enriches each
// model with the pricing/capabilities declared here; pricing is matched by model
// family prefix (longest-prefix-first) so a new model version inherits its
// family's declared list price until the operator overrides it.
//
// IMPORTANT: these are declared LIST prices to VERIFY against ai.google.dev/pricing
// — not fabricated metrics (docs/SECURITY-HARDENING.md contract). Gemini prices are TIERED by
// prompt context length (a long-context request is billed at a higher rate); this
// flat table records only the BASE tier. Operators override the table for the
// long-context tiers and for negotiated rates.

// pricingAsOf stamps the declared prices with the date they were recorded.
const pricingAsOf = "2026-06-01"

// geminiBaseCapabilities is the declared capability set the modern Gemini models
// support (surfaced by module X, README.md). CapStreaming is added per model when
// the API reports the streamGenerateContent generation method; the rest are
// declared here. Only constants that EXIST in modelprovider/catalog.go are used:
// the contract has no dedicated function-calling flag (tool use covers it via
// CapToolUse), and Gemini has no Anthropic-style computer-use/memory/citations/
// context-management surface in this API, so those flags are deliberately omitted.
var geminiBaseCapabilities = []modelprovider.Capability{
	modelprovider.CapToolUse,
	modelprovider.CapVision,
	modelprovider.CapPDF,
	modelprovider.CapStructuredOutputs,
	modelprovider.CapPromptCaching,
	modelprovider.CapBatch,
}

// family is a declared price keyed by a model-id prefix. Matching by prefix means
// a new "gemini-2.5-pro-*" version inherits the 2.5 Pro tier until the operator
// updates the table.
type family struct {
	prefix  string
	pricing modelprovider.ModelPricing
}

// geminiFamilies are matched longest-prefix-first by pricingFor, so gemini-2.5-pro
// beats a (hypothetical) gemini-2.5 entry. Prices are USD per million tokens
// (list, base tier, AsOf pricingAsOf) — verify against ai.google.dev/pricing.
var geminiFamilies = []family{
	{
		prefix: "gemini-2.5-pro",
		pricing: modelprovider.ModelPricing{
			InputPerMTokUSD: 1.25, OutputPerMTokUSD: 10.00,
			CacheReadPerMTokUSD: 0.31,
			Currency:            "USD", AsOf: pricingAsOf, Source: modelprovider.PricingList,
		},
	},
	{
		prefix: "gemini-2.5-flash",
		pricing: modelprovider.ModelPricing{
			InputPerMTokUSD: 0.30, OutputPerMTokUSD: 2.50,
			CacheReadPerMTokUSD: 0.075,
			Currency:            "USD", AsOf: pricingAsOf, Source: modelprovider.PricingList,
		},
	},
	{
		prefix: "gemini-2.0-flash",
		pricing: modelprovider.ModelPricing{
			InputPerMTokUSD: 0.10, OutputPerMTokUSD: 0.40,
			CacheReadPerMTokUSD: 0.025,
			Currency:            "USD", AsOf: pricingAsOf, Source: modelprovider.PricingList,
		},
	},
	{
		prefix: "gemini-1.5-pro",
		pricing: modelprovider.ModelPricing{
			InputPerMTokUSD: 1.25, OutputPerMTokUSD: 5.00,
			Currency: "USD", AsOf: pricingAsOf, Source: modelprovider.PricingList,
		},
	},
	{
		prefix: "gemini-1.5-flash",
		pricing: modelprovider.ModelPricing{
			InputPerMTokUSD: 0.075, OutputPerMTokUSD: 0.30,
			Currency: "USD", AsOf: pricingAsOf, Source: modelprovider.PricingList,
		},
	},
}

// pricingFor returns the declared pricing for a model id, matched by the longest
// family prefix. ok is false when no family matches (the connector then leaves
// Model.Pricing nil rather than guess a price). The second and third returns are
// reserved declared context/output limits (0 here: Gemini reports the limits live
// in the models API, so the offline table does not duplicate them).
func pricingFor(modelID string) (modelprovider.ModelPricing, int64, int64, bool) {
	best := -1
	for i, f := range geminiFamilies {
		if strings.HasPrefix(modelID, f.prefix) {
			if best < 0 || len(f.prefix) > len(geminiFamilies[best].prefix) {
				best = i
			}
		}
	}
	if best < 0 {
		return modelprovider.ModelPricing{}, 0, 0, false
	}
	return geminiFamilies[best].pricing, 0, 0, true
}

// trimModelPrefix maps the API's "models/gemini-2.5-pro" name to the bare ref
// "gemini-2.5-pro" used everywhere else (Usage.ModelRef, pricing prefixes).
func trimModelPrefix(name string) string {
	return strings.TrimPrefix(name, "models/")
}

// declaredModelIDs is the offline fallback model list returned by Snapshot when no
// api_key is configured (live mode replaces it with /v1beta/models). It names the
// current Gemini models so the catalog is useful air-gapped; operators refresh it
// from the API. Each gets its family pricing + the declared capability set.
var declaredModelIDs = []struct {
	id          string
	displayName string
}{
	{"gemini-2.5-pro", "Gemini 2.5 Pro"},
	{"gemini-2.5-flash", "Gemini 2.5 Flash"},
	{"gemini-2.0-flash", "Gemini 2.0 Flash"},
}

// buildModel assembles a modelprovider.Model for a Gemini model id, enriching it
// with the declared family pricing and the declared capability set. It is the
// single place live and offline catalog builds converge. contextWindow and
// maxOutput come from the live models API (0 offline); genMethods is the live
// supportedGenerationMethods list (nil offline) used to add CapStreaming.
func buildModel(id, displayName string, contextWindow, maxOutput int64, genMethods []string) modelprovider.Model {
	caps := append([]modelprovider.Capability(nil), geminiBaseCapabilities...)
	if containsMethod(genMethods, "streamGenerateContent") {
		caps = append(caps, modelprovider.CapStreaming)
	}
	m := modelprovider.Model{
		ProviderRef:     modelprovider.ProviderGoogle,
		Ref:             id,
		DisplayName:     displayName,
		Capabilities:    caps,
		ContextWindow:   contextWindow,
		MaxOutputTokens: maxOutput,
	}
	if p, _, _, ok := pricingFor(id); ok {
		pc := p
		m.Pricing = &pc
	}
	return m
}

// containsMethod reports whether methods contains m.
func containsMethod(methods []string, m string) bool {
	for _, x := range methods {
		if x == m {
			return true
		}
	}
	return false
}
