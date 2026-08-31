// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package mistral

import (
	"context"

	"github.com/olivaresai/olivares/connectors/modelprovider"
)

// catalog.go holds the declared Mistral model family + list pricing, and the Snapshot
// that exposes the catalog (live GET /v1/models, enriched with the declared pricing +
// capabilities) to module X through CatalogProvider.
//
// The Models API (GET /v1/models) is VERIFIED-SHAPE and REAL: it returns each model's id,
// per-model capability booleans (completion_chat / completion_fim / function_calling /
// fine_tuning / vision / classification) and max_context_length. Live mode reads it and
// maps those booleans to the cross-vendor capability flags, then enriches with the
// declared family list pricing here. Offline (no credential) it returns the declared
// model list. Prices are list values to VERIFY against mistral.ai/pricing (stamped AsOf,
// Source=PricingList) — never fabricated telemetry (ARCHITECTURE.md).

// pricingAsOf stamps the declared prices with the date they were recorded/verified.
const pricingAsOf = "2026-06-20"

// chatCaps is the capability set for Mistral's text/chat models: streaming, tool/function
// calling and schema-constrained (json) structured outputs, plus async batch. Vision and
// extended thinking (reasoning) are added per-family where Mistral documents them. Only
// constants that exist in modelprovider/catalog.go are used (no invented flags).
var chatCaps = []modelprovider.Capability{
	modelprovider.CapStreaming,
	modelprovider.CapToolUse,
	modelprovider.CapStructuredOutputs,
	modelprovider.CapBatch,
}

// withCaps returns chatCaps plus the extra flags, as a fresh slice.
func withCaps(extra ...modelprovider.Capability) []modelprovider.Capability {
	out := append([]modelprovider.Capability(nil), chatCaps...)
	return append(out, extra...)
}

// family is a declared price + capability set keyed by a model-id prefix, matched
// longest-prefix-first (so "ministral-8b" beats "ministral", and "mistral-large" does not
// shadow "mistral-large-3" — the more specific dated id wins). Prices are USD per million
// tokens (list, AsOf pricingAsOf). VERIFY against mistral.ai/pricing.
type family struct {
	prefix       string
	pricing      modelprovider.ModelPricing
	capabilities []modelprovider.Capability
	context      int64 // declared max context tokens (0 = unknown / let live API decide)
}

// price builds a USD/MTok list ModelPricing (no public cached-input tier on Mistral, so
// CacheReadPerMTokUSD stays 0; batch is a global 50% discount, not a cache tier).
func price(in, out float64) modelprovider.ModelPricing {
	return modelprovider.ModelPricing{
		InputPerMTokUSD: in, OutputPerMTokUSD: out,
		Currency: "USD", AsOf: pricingAsOf, Source: modelprovider.PricingList,
	}
}

// mistralFamilies are matched longest-prefix-first by familyFor. Prices VERIFIED against
// mistral.ai/pricing (USD/MTok) on pricingAsOf. Context windows: most premier/open chat
// models are 128K (131072); Codestral is 256K (262144); the legacy Mixtral open models are
// 32K/64K. Embeddings bill input only (output price 0). The newest dated ids resolve via
// the "-latest" aliases; live GET /v1/models.max_context_length is authoritative at runtime.
var mistralFamilies = []family{
	{prefix: "mistral-large", pricing: price(0.5, 1.5), capabilities: withCaps(modelprovider.CapVision), context: 131072},
	{prefix: "mistral-medium", pricing: price(1.5, 7.5), capabilities: withCaps(modelprovider.CapVision), context: 131072},
	{prefix: "mistral-small", pricing: price(0.1, 0.3), capabilities: withCaps(modelprovider.CapVision), context: 131072},
	{prefix: "magistral-medium", pricing: price(2, 5), capabilities: withCaps(modelprovider.CapExtendedThinking), context: 131072},
	{prefix: "magistral-small", pricing: price(0.5, 1.5), capabilities: withCaps(modelprovider.CapExtendedThinking), context: 131072},
	{prefix: "codestral-embed", pricing: price(0.15, 0), capabilities: nil, context: 0},
	{prefix: "codestral", pricing: price(0.3, 0.9), capabilities: chatCaps, context: 262144},
	{prefix: "devstral-medium", pricing: price(0.4, 2), capabilities: chatCaps, context: 131072},
	{prefix: "devstral-small", pricing: price(0.1, 0.3), capabilities: chatCaps, context: 131072},
	{prefix: "ministral-3b", pricing: price(0.1, 0.1), capabilities: withCaps(modelprovider.CapVision), context: 131072},
	{prefix: "ministral-8b", pricing: price(0.15, 0.15), capabilities: withCaps(modelprovider.CapVision), context: 131072},
	{prefix: "ministral-3-14b", pricing: price(0.2, 0.2), capabilities: withCaps(modelprovider.CapVision), context: 131072},
	{prefix: "open-mistral-nemo", pricing: price(0.15, 0.15), capabilities: []modelprovider.Capability{modelprovider.CapStreaming, modelprovider.CapBatch}, context: 131072},
	{prefix: "open-mixtral-8x22b", pricing: price(2, 6), capabilities: withCaps(), context: 65536},
	{prefix: "open-mixtral-8x7b", pricing: price(0.7, 0.7), capabilities: []modelprovider.Capability{modelprovider.CapStreaming, modelprovider.CapBatch}, context: 32768},
	{prefix: "voxtral-small", pricing: price(0.1, 0.4), capabilities: withCaps(), context: 32768},
	{prefix: "mistral-embed", pricing: price(0.1, 0), capabilities: nil, context: 0},
}

// declaredModelIDs is the offline Mistral model list returned by Snapshot when no
// credential is configured (live mode replaces it with GET /v1/models). It names the
// current la Plateforme models so the catalog is useful air-gapped; operators refresh it
// from the API as the family evolves. Each "-latest" alias resolves to the current dated
// version at runtime.
var declaredModelIDs = []struct{ id, displayName string }{
	{"mistral-large-latest", "Mistral Large"},
	{"mistral-medium-latest", "Mistral Medium"},
	{"mistral-small-latest", "Mistral Small"},
	{"magistral-medium-latest", "Magistral Medium (reasoning)"},
	{"magistral-small-latest", "Magistral Small (reasoning)"},
	{"codestral-latest", "Codestral"},
	{"devstral-medium-latest", "Devstral Medium"},
	{"devstral-small-latest", "Devstral Small"},
	{"ministral-3b-latest", "Ministral 3B"},
	{"ministral-8b-latest", "Ministral 8B"},
	{"open-mistral-nemo", "Mistral NeMo"},
	{"mistral-embed", "Mistral Embed"},
	{"codestral-embed", "Codestral Embed"},
}

// familyFor returns the declared pricing/capabilities/context for a model id, matched by
// the longest family prefix. ok is false when no family matches (the connector then leaves
// Model.Pricing nil rather than guess a price).
func familyFor(modelID string) (family, bool) {
	best := -1
	for i, f := range mistralFamilies {
		if hasPrefix(modelID, f.prefix) {
			if best < 0 || len(f.prefix) > len(mistralFamilies[best].prefix) {
				best = i
			}
		}
	}
	if best < 0 {
		return family{}, false
	}
	return mistralFamilies[best], true
}

// hasPrefix is strings.HasPrefix without importing strings into this file.
func hasPrefix(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}

// buildDeclaredModel assembles a declared (offline) Mistral model, enriching it with the
// family list pricing + capabilities. CapabilitySource is "declared".
func buildDeclaredModel(id, displayName string) modelprovider.Model {
	m := modelprovider.Model{
		ProviderRef:      modelprovider.ProviderMistral,
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

// declaredCatalogModels builds the offline Mistral model list.
func declaredCatalogModels() []modelprovider.Model {
	out := make([]modelprovider.Model, 0, len(declaredModelIDs))
	for _, d := range declaredModelIDs {
		out = append(out, buildDeclaredModel(d.id, d.displayName))
	}
	return out
}

// liveCapabilities maps the per-model capability booleans the Models API reports to the
// cross-vendor capability flags. Streaming is implicit for any chat model. Structured
// outputs / batch are NOT booleans on /v1/models, so they are folded in from the declared
// family (a refinement, never a downgrade): live read first, declared as the backstop.
func liveCapabilities(c modelCapabilities) []modelprovider.Capability {
	var caps []modelprovider.Capability
	if c.CompletionChat {
		caps = append(caps, modelprovider.CapStreaming)
	}
	if c.FunctionCalling {
		caps = append(caps, modelprovider.CapToolUse)
	}
	if c.Vision {
		caps = append(caps, modelprovider.CapVision)
	}
	return caps
}

// Snapshot returns the Mistral catalog. With a credential it reads GET /v1/models live and
// enriches each model with the declared family pricing + the live capability booleans;
// with no credential it returns the declared offline catalog. The Models API is read-only
// and carries no key/secret material. When the Admin API has been gathered (admin_api_key
// set), workspace and key inventory populate the catalog's Workspaces and Keys slices.
func (s *Source) Snapshot(ctx context.Context) (modelprovider.Catalog, error) {
	cat := modelprovider.Catalog{
		Provider: modelprovider.Provider{
			Ref: modelprovider.ProviderMistral, Kind: modelprovider.KindHostedAPI,
			Title: "Mistral AI", BaseURL: s.baseURL,
		},
		CapturedAt: s.clock().UTC(),
	}
	if s.credential == "" || s.client == nil {
		cat.Models = declaredCatalogModels()
	} else {
		models, err := s.fetchModels(ctx)
		if err != nil {
			return modelprovider.Catalog{}, err
		}
		cat.Models = models
	}
	// Enrich catalog with Admin API inventory (populated by gatherAdminInventory).
	for _, w := range s.adminWorkspaceEntries {
		cat.Workspaces = append(cat.Workspaces, modelprovider.WorkspaceRef{
			ID:   w.ID,
			Name: w.Name,
		})
	}
	for _, k := range s.adminKeyEntries {
		cat.Keys = append(cat.Keys, modelprovider.KeyRef{
			ID:           k.ID,
			Name:         k.Name,
			WorkspaceRef: k.WorkspaceID,
			CreatedAt:    parseTime(k.CreatedAt),
		})
	}
	return cat, nil
}

// fetchModels reads GET /v1/models (no pagination — the API returns the full list in one
// data array) and builds the live catalog: live capability booleans + declared family
// pricing. CapabilitySource is "live" (the booleans came from the provider API); a model
// with no declared family keeps nil pricing (never a guessed price). Fine-tuned cards are
// included as-is (they carry an id + capabilities like any other).
func (s *Source) fetchModels(ctx context.Context) ([]modelprovider.Model, error) {
	var resp modelsResponse
	if err := s.client.GetJSON(ctx, defaultModelsND, nil, &resp); err != nil {
		return nil, err
	}
	out := make([]modelprovider.Model, 0, len(resp.Data))
	for _, c := range resp.Data {
		if c.ID == "" {
			continue
		}
		m := modelprovider.Model{
			ProviderRef:      modelprovider.ProviderMistral,
			Ref:              c.ID,
			DisplayName:      firstNonEmpty(c.Name, c.ID),
			CapabilitySource: "live",
			ContextWindow:    c.MaxContextLength,
			CreatedAt:        unixTime(c.Created),
			Deprecated:       c.Deprecation != "",
		}
		m.Capabilities = liveCapabilities(c.Capabilities)
		if f, ok := familyFor(c.ID); ok {
			pc := f.pricing
			m.Pricing = &pc
			// Fold in the declared structured-outputs/batch flags the live booleans don't
			// carry (a refinement of the live set, never a downgrade).
			m.Capabilities = mergeCaps(m.Capabilities, f.capabilities)
			if m.ContextWindow == 0 {
				m.ContextWindow = f.context
			}
		}
		out = append(out, m)
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

// firstNonEmpty returns the first non-empty string, or "".
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
