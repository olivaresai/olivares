// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package codex

import (
	"context"
	"net/url"

	"github.com/olivaresai/olivares/connectors/modelprovider"
	"github.com/olivaresai/olivares/sdk/model"
)

// catalog.go holds the declared Codex model family + the Snapshot that exposes it (and
// the workspace/automation-key inventory) to module X through CatalogProvider.
//
// Declared list pricing is VERIFY-against-openai.com/pricing (the same convention as
// the openai connector's catalog.go), stamped AsOf + Source=list so module XI never
// shows it as authoritative. NOTE the cost OBSERVATION path does NOT use this table: a
// Codex CostSample carries the provider's OWN estimated_cost (Analytics) or billed
// amount (Costs API), so the connector never derives Codex cost from a guessed token
// price. The table only enriches the catalog DISPLAY (module X).

// pricingAsOf stamps the declared prices with the date they were recorded.
const pricingAsOf = "2026-06-01"

// codexCapabilities is the capability set surfaced for the Codex coding-agent family:
// streaming, tool/function calling, structured outputs, prompt caching and extended
// thinking. Only constants that exist in modelprovider/catalog.go are used.
var codexCapabilities = []modelprovider.Capability{
	modelprovider.CapStreaming,
	modelprovider.CapToolUse,
	modelprovider.CapStructuredOutputs,
	modelprovider.CapPromptCaching,
	modelprovider.CapExtendedThinking,
}

// family is a declared price + capability set keyed by a model-id prefix, matched
// longest-prefix-first (so codex-mini beats codex) — the same scheme as the openai
// connector.
type family struct {
	prefix       string
	pricing      modelprovider.ModelPricing
	capabilities []modelprovider.Capability
}

// codexFamilies are matched longest-prefix-first. Prices are USD per million tokens
// (list, AsOf pricingAsOf). VERIFY against openai.com/pricing.
var codexFamilies = []family{
	{
		prefix: "gpt-5.6-terra",
		pricing: modelprovider.ModelPricing{
			InputPerMTokUSD: 2.50, OutputPerMTokUSD: 15.00,
			CacheWritePerMTokUSD: 3.125, CacheReadPerMTokUSD: 0.25,
			Currency: "USD", AsOf: "2026-07-15", Source: modelprovider.PricingList,
		},
		capabilities: codexCapabilities,
	},
	{
		prefix: "gpt-5.6-luna",
		pricing: modelprovider.ModelPricing{
			InputPerMTokUSD: 1.00, OutputPerMTokUSD: 6.00,
			CacheWritePerMTokUSD: 1.25, CacheReadPerMTokUSD: 0.10,
			Currency: "USD", AsOf: "2026-07-15", Source: modelprovider.PricingList,
		},
		capabilities: codexCapabilities,
	},
	{
		prefix: "gpt-5.6",
		pricing: modelprovider.ModelPricing{
			InputPerMTokUSD: 5.00, OutputPerMTokUSD: 30.00,
			CacheWritePerMTokUSD: 6.25, CacheReadPerMTokUSD: 0.50,
			Currency: "USD", AsOf: "2026-07-15", Source: modelprovider.PricingList,
		},
		capabilities: codexCapabilities,
	},
	{
		prefix: "gpt-5-codex-mini",
		pricing: modelprovider.ModelPricing{
			InputPerMTokUSD: 0.25, OutputPerMTokUSD: 2.00,
			CacheReadPerMTokUSD: 0.025, Currency: "USD", AsOf: pricingAsOf, Source: modelprovider.PricingList,
		},
		capabilities: codexCapabilities,
	},
	{
		prefix: "gpt-5-codex",
		pricing: modelprovider.ModelPricing{
			InputPerMTokUSD: 1.25, OutputPerMTokUSD: 10.00,
			CacheReadPerMTokUSD: 0.125, Currency: "USD", AsOf: pricingAsOf, Source: modelprovider.PricingList,
		},
		capabilities: codexCapabilities,
	},
	{
		prefix: "codex-mini",
		pricing: modelprovider.ModelPricing{
			InputPerMTokUSD: 1.50, OutputPerMTokUSD: 6.00,
			CacheReadPerMTokUSD: 0.375, Currency: "USD", AsOf: pricingAsOf, Source: modelprovider.PricingList,
		},
		capabilities: codexCapabilities,
	},
}

// declaredModelIDs is the offline Codex model list. It names the current Codex models
// so the catalog is useful air-gapped; operators refresh it as the family evolves.
var declaredModelIDs = []struct {
	id          string
	displayName string
}{
	{"gpt-5.6-sol", "GPT-5.6 Sol"},
	{"gpt-5.6-terra", "GPT-5.6 Terra"},
	{"gpt-5.6-luna", "GPT-5.6 Luna"},
	{"gpt-5-codex", "GPT-5 Codex"},
	{"gpt-5-codex-mini", "GPT-5 Codex mini"},
	{"codex-mini-latest", "Codex mini (latest)"},
}

var codexModelDeprecations = map[string]struct {
	deprecatedOn   string
	retiresOn      string
	replacementRef string
}{
	"gpt-5-codex":   {"2026-04-22", "2026-07-23", "gpt-5.6"},
	"gpt-5.1-codex": {"2026-04-22", "2026-07-23", "gpt-5.6"},
}

// pricingFor returns the declared pricing + capabilities for a model id, matched by the
// longest family prefix. ok is false when no family matches (the connector then leaves
// Model.Pricing nil rather than guess).
func pricingFor(modelID string) (modelprovider.ModelPricing, []modelprovider.Capability, bool) {
	best := -1
	for i, f := range codexFamilies {
		if hasPrefix(modelID, f.prefix) {
			if best < 0 || len(f.prefix) > len(codexFamilies[best].prefix) {
				best = i
			}
		}
	}
	if best < 0 {
		return modelprovider.ModelPricing{}, nil, false
	}
	f := codexFamilies[best]
	return f.pricing, f.capabilities, true
}

// hasPrefix is strings.HasPrefix without importing strings into this file.
func hasPrefix(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}

// buildModel assembles a Codex modelprovider.Model, enriching it with the declared
// family pricing + capabilities. CapabilitySource is "declared" (there is no verified
// Codex-specific models API to read live), so a consumer never shows it as live.
func buildModel(id, displayName string) modelprovider.Model {
	m := modelprovider.Model{
		ProviderRef:      modelprovider.ProviderOpenAICodex,
		Ref:              id,
		DisplayName:      displayName,
		CapabilitySource: "declared",
	}
	if p, caps, ok := pricingFor(id); ok {
		pc := p
		m.Pricing = &pc
		m.Capabilities = append([]modelprovider.Capability(nil), caps...)
	}
	if r, ok := codexModelDeprecations[id]; ok {
		m.Deprecated = true
		m.Retirements = []modelprovider.ModelRetirement{{
			Surface:        model.GatewayDirect,
			DeprecatedOn:   r.deprecatedOn,
			RetiresOn:      r.retiresOn,
			ReplacementRef: r.replacementRef,
			AsOf:           "2026-07-15",
		}}
	}
	return m
}

// declaredCatalogModels builds the offline Codex model list.
func declaredCatalogModels() []modelprovider.Model {
	out := make([]modelprovider.Model, 0, len(declaredModelIDs))
	for _, d := range declaredModelIDs {
		out = append(out, buildModel(d.id, d.displayName))
	}
	return out
}

// Snapshot returns the Codex catalog. The model list is the declared family (there is
// no verified Codex-specific models API). With a credential it ALSO lists the org's
// projects (workspaces — the scope of Codex access-token identity) and admin API keys
// (the automation-identity inventory) via the real OpenAI org admin API, read-only and
// metadata-only (never a key value — only the masked redacted value). With no
// credential it returns the declared offline catalog (models only).
func (s *Source) Snapshot(ctx context.Context) (modelprovider.Catalog, error) {
	cat := modelprovider.Catalog{
		Provider: modelprovider.Provider{
			Ref: modelprovider.ProviderOpenAICodex, Kind: modelprovider.KindHostedAPI,
			Title: "OpenAI Codex", BaseURL: s.baseURL,
		},
		Models:     declaredCatalogModels(),
		CapturedAt: s.clock().UTC(),
	}
	if s.credential == "" || s.client == nil {
		return cat, nil
	}

	ws, err := s.fetchProjects(ctx)
	if err != nil {
		return modelprovider.Catalog{}, err
	}
	cat.Workspaces = ws

	keys, err := s.fetchKeys(ctx)
	if err != nil {
		return modelprovider.Catalog{}, err
	}
	cat.Keys = keys
	return cat, nil
}

// fetchProjects lists /v1/organization/projects as workspace inventory (the scope a
// Codex access token is issued against). Cursor pagination via last_id + has_more.
func (s *Source) fetchProjects(ctx context.Context) ([]modelprovider.WorkspaceRef, error) {
	var out []modelprovider.WorkspaceRef
	after := ""
	for i := 0; i < s.maxPages; i++ {
		var resp projectsResponse
		q := url.Values{"limit": {"100"}}
		if after != "" {
			q.Set("after_id", after)
		}
		if err := s.client.GetJSON(ctx, projectsPath, q, &resp); err != nil {
			return nil, err
		}
		for _, p := range resp.Data {
			out = append(out, modelprovider.WorkspaceRef{
				ID: p.ID, Name: p.Name,
				Archived: p.Status == "archived", CreatedAt: unixTime(p.CreatedAt),
			})
		}
		if !resp.HasMore || resp.LastID == "" {
			break
		}
		after = resp.LastID
	}
	return out, nil
}

// fetchKeys lists /v1/organization/admin_api_keys as automation-identity inventory
// metadata (no secrets — only the masked redacted value). Cursor pagination.
func (s *Source) fetchKeys(ctx context.Context) ([]modelprovider.KeyRef, error) {
	var out []modelprovider.KeyRef
	after := ""
	for i := 0; i < s.maxPages; i++ {
		var resp adminKeysResponse
		q := url.Values{"limit": {"100"}}
		if after != "" {
			q.Set("after_id", after)
		}
		if err := s.client.GetJSON(ctx, adminKeysPath, q, &resp); err != nil {
			return nil, err
		}
		for _, k := range resp.Data {
			out = append(out, modelprovider.KeyRef{
				ID: k.ID, Name: k.Name,
				Status: k.Status, Hint: k.RedactedValue, CreatedAt: unixTime(k.CreatedAt),
			})
		}
		if !resp.HasMore || resp.LastID == "" {
			break
		}
		after = resp.LastID
	}
	return out, nil
}
