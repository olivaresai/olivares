// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package cohere

import (
	"context"
	"net/url"

	"github.com/olivaresai/olivares/connectors/modelprovider"
)

// catalog.go holds the declared Cohere model family + list pricing, and the Snapshot
// that exposes the catalog (live GET /v1/models, enriched with the declared pricing +
// capabilities) to module X through CatalogProvider.
//
// The Models API (GET /v1/models) is VERIFIED-SHAPE and REAL: it returns each model's
// name, endpoints[], context_length, finetuned, and is_deprecated. Live mode reads it
// and maps those endpoints to the cross-vendor capability flags, then enriches with the
// declared family list pricing here. Offline (no credential) it returns the declared
// model list. Prices are list values to VERIFY against cohere.com/pricing (stamped AsOf,
// Source=PricingList) — never fabricated telemetry (ARCHITECTURE.md).

// pricingAsOf stamps the declared prices with the date they were recorded/verified.
const pricingAsOf = "2026-06-28"

// chatCaps is the capability set for Cohere's chat/command models: streaming, tool/function
// calling, and structured outputs. Only constants that exist in modelprovider/catalog.go
// are used (no invented flags).
var chatCaps = []modelprovider.Capability{
	modelprovider.CapStreaming,
	modelprovider.CapToolUse,
	modelprovider.CapStructuredOutputs,
}

// family is a declared price + capability set keyed by a model name prefix, matched
// longest-prefix-first (so "embed-english-v3.0" beats "embed"). Prices are USD per
// million tokens (list, AsOf pricingAsOf). VERIFY against cohere.com/pricing.
type family struct {
	prefix       string
	pricing      modelprovider.ModelPricing
	capabilities []modelprovider.Capability
	context      int64 // declared max context tokens (0 = unknown / let live API decide)
}

// price builds a USD/MTok list ModelPricing (input and output tiers). Cohere has no
// public cache-read/cache-write pricing tier, so cache fields stay 0.
func price(in, out float64) modelprovider.ModelPricing {
	return modelprovider.ModelPricing{
		InputPerMTokUSD: in, OutputPerMTokUSD: out,
		Currency: "USD", AsOf: pricingAsOf, Source: modelprovider.PricingList,
	}
}

// cohereFamilies are matched longest-prefix-first by familyFor. Prices VERIFIED against
// cohere.com/pricing (USD/MTok) on pricingAsOf. Context windows from the Cohere docs.
//
// Rerank pricing is per-search (not per-token): $2.00 per 1K searches. We express this
// as an input-per-MTok approximation so the Meter helper can produce a CostSample — the
// operator should verify and override if their usage diverges.
var cohereFamilies = []family{
	// Command models (chat/generation)
	{prefix: "command-r-plus", pricing: price(2.5, 10.0), capabilities: chatCaps, context: 128000},
	{prefix: "command-r", pricing: price(0.15, 0.60), capabilities: chatCaps, context: 128000},
	{prefix: "command-a", pricing: price(2.5, 10.0), capabilities: chatCaps, context: 256000},

	// Embed models (embeddings only — output price 0)
	{prefix: "embed-english-v3.0", pricing: price(0.10, 0), capabilities: nil, context: 512},
	{prefix: "embed-multilingual-v3.0", pricing: price(0.10, 0), capabilities: nil, context: 512},
	{prefix: "embed-v4.0", pricing: price(0.10, 0), capabilities: nil, context: 0},

	// Rerank models (per-search pricing approximated as input-per-MTok)
	{prefix: "rerank-v3.5", pricing: price(2.0, 0), capabilities: nil, context: 0},
}

// declaredModelIDs is the offline Cohere model list returned by Snapshot when no
// credential is configured (live mode replaces it with GET /v1/models). It names the
// current Cohere models so the catalog is useful air-gapped; operators refresh it from
// the API as the family evolves.
var declaredModelIDs = []struct{ id, displayName string }{
	{"command-r-plus", "Command R+"},
	{"command-r", "Command R"},
	{"command-a", "Command A"},
	{"embed-v4.0", "Embed v4.0"},
	{"embed-english-v3.0", "Embed English v3.0"},
	{"embed-multilingual-v3.0", "Embed Multilingual v3.0"},
	{"rerank-v3.5", "Rerank v3.5"},
}

// familyFor returns the declared pricing/capabilities/context for a model name, matched
// by the longest family prefix. ok is false when no family matches (the connector then
// leaves Model.Pricing nil rather than guess a price).
func familyFor(modelName string) (family, bool) {
	best := -1
	for i, f := range cohereFamilies {
		if hasPrefix(modelName, f.prefix) {
			if best < 0 || len(f.prefix) > len(cohereFamilies[best].prefix) {
				best = i
			}
		}
	}
	if best < 0 {
		return family{}, false
	}
	return cohereFamilies[best], true
}

// hasPrefix is strings.HasPrefix without importing strings into this file.
func hasPrefix(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}

// buildDeclaredModel assembles a declared (offline) Cohere model, enriching it with the
// family list pricing + capabilities. CapabilitySource is "declared".
func buildDeclaredModel(id, displayName string) modelprovider.Model {
	m := modelprovider.Model{
		ProviderRef:      modelprovider.ProviderCohere,
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

// declaredCatalogModels builds the offline Cohere model list.
func declaredCatalogModels() []modelprovider.Model {
	out := make([]modelprovider.Model, 0, len(declaredModelIDs))
	for _, d := range declaredModelIDs {
		out = append(out, buildDeclaredModel(d.id, d.displayName))
	}
	return out
}

// liveCapabilities maps the per-model endpoints array the Models API reports to the
// cross-vendor capability flags. Cohere uses endpoints like "chat", "embed", "rerank",
// "generate". Streaming is implicit for any chat model.
func liveCapabilities(endpoints []string) []modelprovider.Capability {
	var caps []modelprovider.Capability
	for _, ep := range endpoints {
		switch ep {
		case "chat":
			caps = append(caps, modelprovider.CapStreaming, modelprovider.CapToolUse)
		}
	}
	return caps
}

// Snapshot returns the Cohere catalog. With a credential it reads GET /v1/models live
// and enriches each model with the declared family pricing + the live endpoint
// capabilities; with no credential it returns the declared offline catalog. The Models
// API is read-only and carries no key/secret material.
func (s *Source) Snapshot(ctx context.Context) (modelprovider.Catalog, error) {
	cat := modelprovider.Catalog{
		Provider: modelprovider.Provider{
			Ref: modelprovider.ProviderCohere, Kind: modelprovider.KindHostedAPI,
			Title: "Cohere", BaseURL: s.baseURL,
		},
		CapturedAt: s.clock().UTC(),
	}
	if s.credential == "" || s.client == nil {
		cat.Models = declaredCatalogModels()
		return cat, nil
	}
	models, err := s.fetchModels(ctx)
	if err != nil {
		return modelprovider.Catalog{}, err
	}
	cat.Models = models
	return cat, nil
}

// fetchModels reads GET /v1/models with cursor-based pagination (page_token /
// next_page_token) and builds the live catalog: live endpoint capabilities + declared
// family pricing. CapabilitySource is "live" (the endpoints came from the provider API);
// a model with no declared family keeps nil pricing (never a guessed price). Fine-tuned
// models are included as-is.
func (s *Source) fetchModels(ctx context.Context) ([]modelprovider.Model, error) {
	var out []modelprovider.Model
	pageToken := ""
	for i := 0; i < s.maxPages; i++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var resp modelsResponse
		q := url.Values{}
		q.Set("page_size", "1000")
		if pageToken != "" {
			q.Set("page_token", pageToken)
		}
		if err := s.client.GetJSON(ctx, defaultModelsPath, q, &resp); err != nil {
			return nil, err
		}
		for _, c := range resp.Models {
			if c.Name == "" {
				continue
			}
			m := modelprovider.Model{
				ProviderRef:      modelprovider.ProviderCohere,
				Ref:              c.Name,
				DisplayName:      c.Name,
				CapabilitySource: "live",
				ContextWindow:    c.ContextLength,
				Deprecated:       c.IsDeprecated,
			}
			m.Capabilities = liveCapabilities(c.Endpoints)
			if f, ok := familyFor(c.Name); ok {
				pc := f.pricing
				m.Pricing = &pc
				// Fold in the declared structured-outputs flag the live endpoints don't
				// carry (a refinement of the live set, never a downgrade).
				m.Capabilities = mergeCaps(m.Capabilities, f.capabilities)
				if m.ContextWindow == 0 {
					m.ContextWindow = f.context
				}
			}
			out = append(out, m)
		}
		if resp.NextPageToken == "" {
			break
		}
		pageToken = resp.NextPageToken
	}
	return out, nil
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
