// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package glm

import (
	"context"

	"github.com/olivaresai/olivares/connectors/modelprovider"
)

// catalog.go holds the declared GLM model family + verified USD list pricing, and
// exposes the declared catalog to module X through CatalogProvider.
//
// The GLM /models path is intentionally NOT decoded here: its response shape is
// undocumented and UNVERIFIED. Snapshot always returns this declared catalog with
// CapabilitySource="declared". Prices are z.ai USD list values verified for E1
// on pricingAsOf; BigModel-only CNY prices and JS-rendered rows are not converted.

// pricingAsOf stamps the declared prices with the date they were recorded/verified.
const pricingAsOf = "2026-07-08"

var (
	glmReasoningCaps = []modelprovider.Capability{
		modelprovider.CapStreaming,
		modelprovider.CapToolUse,
		modelprovider.CapStructuredOutputs,
		modelprovider.CapPromptCaching,
		modelprovider.CapExtendedThinking,
	}
	glmCachedCaps = []modelprovider.Capability{
		modelprovider.CapStreaming,
		modelprovider.CapToolUse,
		modelprovider.CapStructuredOutputs,
		modelprovider.CapPromptCaching,
	}
	glmVisionReasoningCaps = []modelprovider.Capability{
		modelprovider.CapStreaming,
		modelprovider.CapToolUse,
		modelprovider.CapStructuredOutputs,
		modelprovider.CapPromptCaching,
		modelprovider.CapExtendedThinking,
		modelprovider.CapVision,
	}
	glmBasicCaps = []modelprovider.Capability{
		modelprovider.CapStreaming,
		modelprovider.CapToolUse,
		modelprovider.CapStructuredOutputs,
	}
)

// family is a declared model-id prefix, matched longest-prefix-first. pricing is nil
// only when the USD price is UNVERIFIED; those rows keep declared capabilities/limits
// for catalog usefulness but Meter and Model.Pricing remain unpriced.
type family struct {
	prefix       string
	pricing      *modelprovider.ModelPricing
	capabilities []modelprovider.Capability
	context      int64
	maxOutput    int64
}

// price builds a USD/MTok list ModelPricing with an optional cache-read tier. GLM has
// no verified cache-write tier, so those fields remain 0.
func price(in, out, cacheRead float64) *modelprovider.ModelPricing {
	return &modelprovider.ModelPricing{
		InputPerMTokUSD:     in,
		OutputPerMTokUSD:    out,
		CacheReadPerMTokUSD: cacheRead,
		Currency:            "USD",
		AsOf:                pricingAsOf,
		Source:              modelprovider.PricingList,
	}
}

// glmFamilies are matched longest-prefix-first by declaredFamilyFor. The
// glm-4-plus and glm-4-flashx rows deliberately carry nil pricing because their USD
// prices are UNVERIFIED; this also prevents glm-4-flashx from inheriting the free
// glm-4-flash price via prefix matching.
var glmFamilies = []family{
	{prefix: "glm-5.2", pricing: price(1.40, 4.40, 0.26), capabilities: glmReasoningCaps, context: 1_000_000},
	{prefix: "glm-5.1", pricing: price(1.40, 4.40, 0.26), capabilities: glmReasoningCaps},
	{prefix: "glm-5-turbo", pricing: price(1.20, 4.00, 0.24), capabilities: glmReasoningCaps},
	{prefix: "glm-5", pricing: price(1.00, 3.20, 0.20), capabilities: glmReasoningCaps},
	{prefix: "glm-4.7-flashx", pricing: price(0.07, 0.40, 0.01), capabilities: glmCachedCaps, context: 128_000},
	{prefix: "glm-4.7-flash", pricing: price(0, 0, 0), capabilities: glmCachedCaps, context: 128_000},
	{prefix: "glm-4.7", pricing: price(0.60, 2.20, 0.11), capabilities: glmReasoningCaps},
	{prefix: "glm-4.6v", pricing: price(0.30, 0.90, 0.05), capabilities: glmVisionReasoningCaps},
	{prefix: "glm-4.6", pricing: price(0.60, 2.20, 0.11), capabilities: glmReasoningCaps, context: 200_000, maxOutput: 128_000},
	{prefix: "glm-4.5v", pricing: price(0.60, 1.80, 0.11), capabilities: glmVisionReasoningCaps},
	{prefix: "glm-4.5-airx", pricing: price(1.10, 4.50, 0.22), capabilities: glmReasoningCaps, context: 128_000, maxOutput: 96_000},
	{prefix: "glm-4.5-air", pricing: price(0.20, 1.10, 0.03), capabilities: glmReasoningCaps, context: 128_000, maxOutput: 96_000},
	{prefix: "glm-4.5-x", pricing: price(2.20, 8.90, 0.45), capabilities: glmReasoningCaps, context: 128_000, maxOutput: 96_000},
	{prefix: "glm-4.5-flash", pricing: price(0, 0, 0), capabilities: glmReasoningCaps, context: 128_000, maxOutput: 96_000},
	{prefix: "glm-4.5", pricing: price(0.60, 2.20, 0.11), capabilities: glmReasoningCaps, context: 128_000, maxOutput: 96_000},
	{prefix: "glm-4-32b-0414-128k", pricing: price(0.10, 0.10, 0), capabilities: glmBasicCaps, context: 128_000},
	{prefix: "glm-4-plus", capabilities: glmBasicCaps, context: 128_000, maxOutput: 4096},
	{prefix: "glm-4-flashx", capabilities: glmBasicCaps, context: 128_000, maxOutput: 16_384},
	{prefix: "glm-4-flash", pricing: price(0, 0, 0), capabilities: glmBasicCaps, context: 128_000, maxOutput: 16_384},
}

// declaredModels is the offline GLM model list returned by Snapshot. It names the
// declared families so the catalog is useful air-gapped; Snapshot does not replace it
// with a live /models body because that shape is undocumented.
var declaredModels = []struct{ id, displayName string }{
	{"glm-5.2", "GLM-5.2"},
	{"glm-5.1", "GLM-5.1"},
	{"glm-5-turbo", "GLM-5-Turbo"},
	{"glm-5", "GLM-5"},
	{"glm-4.7-flashx", "GLM-4.7-FlashX"},
	{"glm-4.7-flash", "GLM-4.7-Flash"},
	{"glm-4.7", "GLM-4.7"},
	{"glm-4.6v", "GLM-4.6V"},
	{"glm-4.6", "GLM-4.6"},
	{"glm-4.5v", "GLM-4.5V"},
	{"glm-4.5-airx", "GLM-4.5-AirX"},
	{"glm-4.5-air", "GLM-4.5-Air"},
	{"glm-4.5-x", "GLM-4.5-X"},
	{"glm-4.5-flash", "GLM-4.5-Flash"},
	{"glm-4.5", "GLM-4.5"},
	{"glm-4-32b-0414-128k", "GLM-4 32B 0414 128K"},
	{"glm-4-plus", "GLM-4-Plus"},
	{"glm-4-flashx", "GLM-4-FlashX"},
	{"glm-4-flash", "GLM-4-Flash"},
}

// Snapshot returns the declared GLM catalog. It never calls or parses /models because
// the response schema is undocumented; Gather owns the separate liveness probe.
func (s *Source) Snapshot(_ context.Context) (modelprovider.Catalog, error) {
	return modelprovider.Catalog{
		Provider: modelprovider.Provider{
			Ref:     modelprovider.ProviderGLM,
			Kind:    modelprovider.KindHostedAPI,
			Title:   "Zhipu GLM (Z.ai)",
			BaseURL: s.baseURL,
		},
		Models:     declaredCatalogModels(),
		CapturedAt: s.clock().UTC(),
	}, nil
}

// familyFor returns the declared priced family for a model id, matched by the longest
// family prefix. ok is false when no priced USD family matches, including USD-unverified
// rows such as glm-4-plus and glm-4-flashx.
func familyFor(modelID string) (family, bool) {
	f, ok := declaredFamilyFor(modelID)
	if !ok || f.pricing == nil {
		return family{}, false
	}
	return f, true
}

func declaredFamilyFor(modelID string) (family, bool) {
	best := -1
	for i, f := range glmFamilies {
		if hasPrefix(modelID, f.prefix) {
			if best < 0 || len(f.prefix) > len(glmFamilies[best].prefix) {
				best = i
			}
		}
	}
	if best < 0 {
		return family{}, false
	}
	return glmFamilies[best], true
}

// hasPrefix is strings.HasPrefix without importing strings into this file.
func hasPrefix(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}

// buildDeclaredModel assembles a declared GLM model, enriching it with verified USD
// pricing when available. CapabilitySource is always "declared".
func buildDeclaredModel(id, displayName string) modelprovider.Model {
	m := modelprovider.Model{
		ProviderRef:      modelprovider.ProviderGLM,
		Ref:              id,
		DisplayName:      displayName,
		CapabilitySource: "declared",
	}
	if f, ok := declaredFamilyFor(id); ok {
		if f.pricing != nil {
			pc := *f.pricing
			m.Pricing = &pc
		}
		m.Capabilities = append([]modelprovider.Capability(nil), f.capabilities...)
		m.ContextWindow = f.context
		m.MaxOutputTokens = f.maxOutput
	}
	return m
}

// declaredCatalogModels builds the declared GLM model list.
func declaredCatalogModels() []modelprovider.Model {
	out := make([]modelprovider.Model, 0, len(declaredModels))
	for _, d := range declaredModels {
		out = append(out, buildDeclaredModel(d.id, d.displayName))
	}
	return out
}
