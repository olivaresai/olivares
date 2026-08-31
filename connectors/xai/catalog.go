// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package xai

import (
	"context"
	"time"

	"github.com/olivaresai/olivares/connectors/modelprovider"
)

// catalog.go holds the declared Grok model family + list pricing, and the Snapshot that
// exposes the catalog (live GET /v1/language-models, or the declared offline set) plus the
// API-key inventory to module X through CatalogProvider.
//
// Live mode reads GET /v1/language-models on the inference plane (xai- key) and derives
// pricing FROM THE API: the *_token_price fields are integers in USD CENTS per 100,000,000
// (1e8) tokens, so USD per 1M tokens = field / 10000 (priceFromCents). Capabilities come
// from the model's modalities (image input → vision) plus the declared family flags. Offline
// (no inference key) it returns the declared Grok catalog. Prices VERIFIED against
// docs.x.ai/docs/models on pricingAsOf (Source=PricingList) — never fabricated (ARCHITECTURE.md).

// pricingAsOf stamps the declared prices with the date they were recorded/verified.
const pricingAsOf = "2026-06-20"

// centsPer1e8PerMTok converts an xAI integer price (USD cents per 1e8 tokens) to USD per
// 1M tokens. 1e8 tokens for `cents` cents → cents/100 USD per 1e8 tokens → ×(1e6/1e8) per
// MTok = cents/100 × 0.01 = cents/10000.
const centsPer1e8Divisor = 10000.0

// chatCaps is the baseline capability set for the Grok chat models (streaming + function
// calling + structured outputs — the flags the official model pages list). Vision is added
// from modalities; extended thinking (reasoning) is added per-family. Prompt caching is a
// PRICING feature (cached_prompt_text_token_price), not a per-model-page capability flag, so
// it is reflected in the cache-read price, not tagged as CapPromptCaching (per the docs).
var chatCaps = []modelprovider.Capability{
	modelprovider.CapStreaming,
	modelprovider.CapToolUse,
	modelprovider.CapStructuredOutputs,
}

// withCaps returns chatCaps plus the extra flags, as a fresh slice.
func withCaps(extra ...modelprovider.Capability) []modelprovider.Capability {
	out := append([]modelprovider.Capability(nil), chatCaps...)
	return append(out, extra...)
}

// family is a declared price + capability set keyed by a model-id prefix, matched
// longest-prefix-first. Prices are USD per million tokens (list, AsOf pricingAsOf).
type family struct {
	prefix       string
	pricing      modelprovider.ModelPricing
	capabilities []modelprovider.Capability
	context      int64
}

// price builds a USD/MTok list ModelPricing with the cached-read tier (xAI prices a cached
// input read; it has no separate cache-write charge, so CacheWritePerMTokUSD stays 0).
func price(in, out, cached float64) modelprovider.ModelPricing {
	return modelprovider.ModelPricing{
		InputPerMTokUSD: in, OutputPerMTokUSD: out, CacheReadPerMTokUSD: cached,
		Currency: "USD", AsOf: pricingAsOf, Source: modelprovider.PricingList,
	}
}

// grokFamilies are matched longest-prefix-first by familyFor. Prices VERIFIED against
// docs.x.ai/docs/models on pricingAsOf (USD/MTok). The longer reasoning/non-reasoning
// prefixes win over the generic "grok-4.20" fallback. Legacy/enterprise-only ids (grok-4,
// grok-3, grok-3-mini, grok-4.1-fast) are intentionally NOT declared — they are absent from
// the current public pricing page, so the live /v1/language-models endpoint is the source
// of truth for whatever an account actually has.
var grokFamilies = []family{
	{prefix: "grok-4.3", pricing: price(1.25, 2.50, 0.20), capabilities: withCaps(modelprovider.CapVision), context: 1_000_000},
	{prefix: "grok-4.20-0309-reasoning", pricing: price(1.25, 2.50, 0.20), capabilities: withCaps(modelprovider.CapExtendedThinking), context: 1_000_000},
	{prefix: "grok-4.20-0309-non-reasoning", pricing: price(1.25, 2.50, 0.20), capabilities: chatCaps, context: 1_000_000},
	{prefix: "grok-4.20-multi-agent", pricing: price(1.25, 2.50, 0.20), capabilities: chatCaps, context: 1_000_000},
	{prefix: "grok-4.20", pricing: price(1.25, 2.50, 0.20), capabilities: chatCaps, context: 1_000_000},
	{prefix: "grok-build", pricing: price(1.00, 2.00, 0.20), capabilities: withCaps(), context: 256_000},
}

// declaredModelIDs is the offline Grok model list returned by Snapshot when no inference key
// is configured (live mode replaces it with GET /v1/language-models). It names the current
// public Grok catalog so it is useful air-gapped; operators refresh it from the API.
var declaredModelIDs = []struct{ id, displayName string }{
	{"grok-4.3", "Grok 4.3"},
	{"grok-4.20-0309-reasoning", "Grok 4.20 (reasoning)"},
	{"grok-4.20-0309-non-reasoning", "Grok 4.20 (non-reasoning)"},
	{"grok-4.20-multi-agent-0309", "Grok 4.20 (multi-agent)"},
	{"grok-build-0.1", "Grok Build 0.1"},
}

// familyFor returns the declared pricing/capabilities/context for a model id, matched by the
// longest family prefix. ok is false when no family matches (the connector then leaves
// Model.Pricing nil rather than guess a price).
func familyFor(modelID string) (family, bool) {
	best := -1
	for i, f := range grokFamilies {
		if hasPrefix(modelID, f.prefix) {
			if best < 0 || len(f.prefix) > len(grokFamilies[best].prefix) {
				best = i
			}
		}
	}
	if best < 0 {
		return family{}, false
	}
	return grokFamilies[best], true
}

// hasPrefix is strings.HasPrefix without importing strings into this file.
func hasPrefix(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}

// buildDeclaredModel assembles a declared (offline) Grok model with its family pricing +
// capabilities. CapabilitySource is "declared".
func buildDeclaredModel(id, displayName string) modelprovider.Model {
	m := modelprovider.Model{
		ProviderRef:      modelprovider.ProviderXAI,
		Ref:              id,
		DisplayName:      displayName,
		CapabilitySource: "declared",
	}
	if f, ok := familyFor(id); ok {
		pc := f.pricing
		m.Pricing = &pc
		m.Capabilities = append([]modelprovider.Capability(nil), f.capabilities...)
		m.ContextWindow = f.context
	}
	return m
}

// declaredCatalogModels builds the offline Grok model list.
func declaredCatalogModels() []modelprovider.Model {
	out := make([]modelprovider.Model, 0, len(declaredModelIDs))
	for _, d := range declaredModelIDs {
		out = append(out, buildDeclaredModel(d.id, d.displayName))
	}
	return out
}

// Snapshot returns the xAI catalog. With an inference key it reads GET /v1/language-models
// live (prices derived from the API's cents-per-1e8-tokens fields, capabilities from
// modalities + the declared family) and otherwise returns the declared Grok catalog. With a
// management key it ALSO lists the team's API keys as inventory (masked hint only — never a
// secret) and records the team as the workspace. Either credential alone is enough; with
// neither, Snapshot returns the declared catalog.
func (s *Source) Snapshot(ctx context.Context) (modelprovider.Catalog, error) {
	cat := modelprovider.Catalog{
		Provider: modelprovider.Provider{
			Ref: modelprovider.ProviderXAI, Kind: modelprovider.KindHostedAPI,
			Title: "xAI", BaseURL: s.inferenceBaseURL,
		},
		CapturedAt: s.clock().UTC(),
	}

	if s.inferenceKey != "" && s.infClient != nil {
		models, err := s.fetchModels(ctx)
		if err != nil {
			return modelprovider.Catalog{}, err
		}
		cat.Models = models
	} else {
		cat.Models = declaredCatalogModels()
	}

	// The management-plane key/workspace inventory is BEST-EFFORT: it uses a SEPARATE
	// credential from the inference catalog, so a management-plane fault (not-entitled,
	// 5xx, transport blip, unresolved team) must never discard the live Models already
	// fetched from the inference plane. On any error here the inventory is left empty and
	// the catalog is returned intact — Snapshot's contract is "either credential alone is
	// enough". The observable management-fault signal lives in Gather (keysUnavailable
	// posture), which Snapshot cannot emit.
	if s.managementKey != "" && s.mgmtClient != nil {
		if team, err := s.resolveTeam(ctx); err == nil && team != "" {
			if keys, err := s.listKeys(ctx, team); err == nil {
				cat.Keys = keyInventory(keys)
				cat.Workspaces = []modelprovider.WorkspaceRef{{ID: team, Name: team}}
			}
		}
	}
	return cat, nil
}

// fetchModels reads GET /v1/language-models and builds the live catalog: pricing derived
// from the API's cents-per-1e8-tokens fields, capabilities from modalities (image input →
// vision) merged with the declared family flags. CapabilitySource is "live". A model with
// no API price and no declared family keeps nil pricing (never a guessed price).
func (s *Source) fetchModels(ctx context.Context) ([]modelprovider.Model, error) {
	var resp languageModelsResponse
	if err := s.infClient.GetJSON(ctx, languageModelsPath, nil, &resp); err != nil {
		return nil, err
	}
	out := make([]modelprovider.Model, 0, len(resp.Models))
	for _, lm := range resp.Models {
		if lm.ID == "" {
			continue
		}
		m := modelprovider.Model{
			ProviderRef:      modelprovider.ProviderXAI,
			Ref:              lm.ID,
			DisplayName:      lm.ID,
			CapabilitySource: "live",
			CreatedAt:        unixTime(lm.Created),
		}
		if p, ok := pricingFromAPI(lm); ok {
			m.Pricing = &p
		} else if f, ok := familyFor(lm.ID); ok {
			pc := f.pricing
			m.Pricing = &pc
		}
		m.Capabilities = liveCapabilities(lm)
		out = append(out, m)
	}
	return out, nil
}

// pricingFromAPI derives ModelPricing from the live model's cents-per-1e8-tokens fields.
// ok is false when the model reports no input price (a non-priced/embedding entry), so the
// caller can fall back to the declared family rather than record a $0 price.
func pricingFromAPI(lm languageModel) (modelprovider.ModelPricing, bool) {
	if lm.PromptTextTokenPrice <= 0 {
		return modelprovider.ModelPricing{}, false
	}
	return modelprovider.ModelPricing{
		InputPerMTokUSD:     float64(lm.PromptTextTokenPrice) / centsPer1e8Divisor,
		OutputPerMTokUSD:    float64(lm.CompletionTextTokenPrice) / centsPer1e8Divisor,
		CacheReadPerMTokUSD: float64(lm.CachedPromptTextPrice) / centsPer1e8Divisor,
		Currency:            "USD", AsOf: pricingAsOf, Source: modelprovider.PricingList,
	}, true
}

// liveCapabilities maps the live model's modalities + declared family to capability flags:
// always streaming; vision when image is an input modality; plus the declared family's
// chat/reasoning flags (a refinement, never a downgrade).
func liveCapabilities(lm languageModel) []modelprovider.Capability {
	caps := []modelprovider.Capability{modelprovider.CapStreaming}
	for _, m := range lm.InputModalities {
		if m == "image" {
			caps = append(caps, modelprovider.CapVision)
		}
	}
	if f, ok := familyFor(lm.ID); ok {
		caps = mergeCaps(caps, f.capabilities)
	} else {
		// No declared family: function calling + structured outputs are universal on the
		// Grok chat models, so include them rather than under-report a live model.
		caps = mergeCaps(caps, []modelprovider.Capability{modelprovider.CapToolUse, modelprovider.CapStructuredOutputs})
	}
	return caps
}

// mergeCaps unions two capability slices, preserving order and dropping duplicates.
func mergeCaps(a, b []modelprovider.Capability) []modelprovider.Capability {
	seen := make(map[modelprovider.Capability]bool, len(a)+len(b))
	out := make([]modelprovider.Capability, 0, len(a)+len(b))
	for _, c := range append(append([]modelprovider.Capability(nil), a...), b...) {
		if !seen[c] {
			seen[c] = true
			out = append(out, c)
		}
	}
	return out
}

// unixTime converts a Unix-seconds timestamp to UTC, returning the zero time for a
// zero/absent value so a missing provider timestamp never aborts a snapshot.
func unixTime(sec int64) time.Time {
	if sec == 0 {
		return time.Time{}
	}
	return time.Unix(sec, 0).UTC()
}
