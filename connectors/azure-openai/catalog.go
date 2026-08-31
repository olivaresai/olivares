// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package azureopenai

import (
	"context"
	"strings"

	"github.com/olivaresai/olivares/connectors/modelprovider"
	"github.com/olivaresai/olivares/sdk/model"
)

// This file builds the Azure OpenAI / Foundry catalog from the ARM enumeration: the
// Cognitive Services accounts are the Workspaces; each account's DEPLOYMENTS are the
// catalog Models (Ref = the deployment name — the inference-callable id and the Azure
// Monitor ModelDeploymentName dimension). Each model is enriched with declared family
// list pricing + capabilities and the account-model lifecycle (deprecation → retirement).
//
// IMPORTANT: the prices below are declared LIST prices to VERIFY against the provider's
// pricing pages (Azure OpenAI: the Azure pricing page; Claude on Foundry: the Anthropic
// price) — not fabricated metrics (docs/SECURITY-HARDENING.md contract). They are matched by UNDERLYING
// MODEL family prefix (longest-prefix-first), not by the arbitrary deployment name.

// pricingAsOf stamps the declared prices with the date they were recorded.
const pricingAsOf = "2026-06-01"

// family is a declared price keyed by an underlying-model-id prefix, matched longest-first.
type family struct {
	prefix  string
	pricing modelprovider.ModelPricing
}

// azureFamilies are the declared family list prices (USD per million tokens, base tier,
// AsOf pricingAsOf) — verify against the provider pricing pages.
var azureFamilies = []family{
	{prefix: "gpt-4o-mini", pricing: mp(0.15, 0.60, 0.075)},
	{prefix: "gpt-4o", pricing: mp(2.50, 10.00, 1.25)},
	{prefix: "gpt-4.1-mini", pricing: mp(0.40, 1.60, 0.10)},
	{prefix: "gpt-4.1", pricing: mp(2.00, 8.00, 0.50)},
	{prefix: "gpt-4-turbo", pricing: mp(10.00, 30.00, 0)},
	{prefix: "gpt-4-32k", pricing: mp(60.00, 120.00, 0)},
	{prefix: "gpt-4", pricing: mp(30.00, 60.00, 0)},
	{prefix: "gpt-35-turbo", pricing: mp(0.50, 1.50, 0)},
	{prefix: "gpt-3.5-turbo", pricing: mp(0.50, 1.50, 0)},
	{prefix: "o1-mini", pricing: mp(1.10, 4.40, 0.55)},
	{prefix: "o1", pricing: mp(15.00, 60.00, 7.50)},
	{prefix: "o3-mini", pricing: mp(1.10, 4.40, 0.55)},
	{prefix: "o3", pricing: mp(2.00, 8.00, 0.50)},
	{prefix: "o4-mini", pricing: mp(1.10, 4.40, 0.275)},
	{prefix: "claude-opus", pricing: mp(15.00, 75.00, 1.50)},
	{prefix: "claude-sonnet", pricing: mp(3.00, 15.00, 0.30)},
	{prefix: "claude-3-7-sonnet", pricing: mp(3.00, 15.00, 0.30)},
	{prefix: "claude-3-5-sonnet", pricing: mp(3.00, 15.00, 0.30)},
	{prefix: "claude-3-5-haiku", pricing: mp(0.80, 4.00, 0.08)},
	{prefix: "claude-haiku", pricing: mp(0.80, 4.00, 0.08)},
}

// mp is a small constructor for a declared list price (USD/MTok, base tier).
func mp(in, out, cacheRead float64) modelprovider.ModelPricing {
	return modelprovider.ModelPricing{
		InputPerMTokUSD: in, OutputPerMTokUSD: out, CacheReadPerMTokUSD: cacheRead,
		Currency: "USD", AsOf: pricingAsOf, Source: modelprovider.PricingList,
	}
}

// pricingFor returns the declared pricing for an underlying model id, matched by the
// longest family prefix. ok is false when no family matches.
func pricingFor(modelName string) (modelprovider.ModelPricing, bool) {
	id := strings.ToLower(strings.TrimSpace(modelName))
	best := -1
	for i, f := range azureFamilies {
		if strings.HasPrefix(id, f.prefix) {
			if best < 0 || len(f.prefix) > len(azureFamilies[best].prefix) {
				best = i
			}
		}
	}
	if best < 0 {
		return modelprovider.ModelPricing{}, false
	}
	return azureFamilies[best].pricing, true
}

// capability sets per surface family.
var (
	openAIChatCaps = []modelprovider.Capability{
		modelprovider.CapStreaming, modelprovider.CapToolUse, modelprovider.CapVision,
		modelprovider.CapStructuredOutputs, modelprovider.CapPromptCaching, modelprovider.CapBatch,
	}
	openAIReasoningCaps = []modelprovider.Capability{
		modelprovider.CapStreaming, modelprovider.CapToolUse, modelprovider.CapStructuredOutputs,
		modelprovider.CapExtendedThinking,
	}
	claudeCaps = []modelprovider.Capability{
		modelprovider.CapStreaming, modelprovider.CapToolUse, modelprovider.CapVision,
		modelprovider.CapPDF, modelprovider.CapStructuredOutputs, modelprovider.CapPromptCaching,
		modelprovider.CapBatch, modelprovider.CapExtendedThinking,
	}
)

// capabilitiesFor returns the declared capability set for a deployment's underlying model,
// keyed by the model format and id family.
func capabilitiesFor(format, modelName string) []modelprovider.Capability {
	if strings.EqualFold(strings.TrimSpace(format), "Anthropic") {
		return append([]modelprovider.Capability(nil), claudeCaps...)
	}
	id := strings.ToLower(strings.TrimSpace(modelName))
	if strings.HasPrefix(id, "o1") || strings.HasPrefix(id, "o3") || strings.HasPrefix(id, "o4") {
		return append([]modelprovider.Capability(nil), openAIReasoningCaps...)
	}
	return append([]modelprovider.Capability(nil), openAIChatCaps...)
}

// Snapshot returns the Azure OpenAI / Foundry catalog. With a credential it enumerates the
// Cognitive Services accounts (Workspaces) and their deployments (Models) across the
// configured/visible subscriptions and enriches each with declared pricing/capabilities +
// account-model lifecycle. With no credential it returns the empty catalog (Azure has no
// offline-discoverable inventory). Keys are intentionally empty: Azure account keys are
// secrets (listKeys returns the value), so they are never inventoried (minimal-data).
func (s *Source) Snapshot(ctx context.Context) (modelprovider.Catalog, error) {
	cat := modelprovider.Catalog{
		Provider: modelprovider.Provider{
			Ref: providerRef, Kind: modelprovider.KindHostedAPI,
			Title: "Azure OpenAI / Foundry", BaseURL: s.cfg.managementEndpoint,
		},
		CapturedAt: s.clock(),
	}
	if s.cfg.tokens == nil {
		return cat, nil // offline: nothing to discover
	}

	subs, err := s.resolveSubscriptions(ctx)
	if err != nil {
		return modelprovider.Catalog{}, err
	}
	asOf := s.clock().Format("2006-01-02")
	for _, sub := range subs {
		if err := ctx.Err(); err != nil {
			return modelprovider.Catalog{}, err
		}
		accounts, err := s.listAccounts(ctx, sub)
		if err != nil {
			return modelprovider.Catalog{}, err
		}
		for _, a := range accounts {
			if err := ctx.Err(); err != nil {
				return modelprovider.Catalog{}, err
			}
			cat.Workspaces = append(cat.Workspaces, workspaceFromAccount(a))
			models, err := s.modelsForAccount(ctx, a, asOf)
			if err != nil {
				return modelprovider.Catalog{}, err
			}
			cat.Models = append(cat.Models, models...)
		}
	}
	return cat, nil
}

// workspaceFromAccount maps a Cognitive Services account to a catalog Workspace.
func workspaceFromAccount(a account) modelprovider.WorkspaceRef {
	return modelprovider.WorkspaceRef{
		ID:   a.ID,
		Name: a.Name,
		Geo:  a.Location,
	}
}

// modelsForAccount lists an account's deployments and maps each to a catalog Model,
// enriched with the account-model lifecycle (deprecation → retirement). A deployment-list
// failure aborts the snapshot (a real fault); an account-model-list failure is tolerated
// (the deployment inventory stands without lifecycle enrichment).
func (s *Source) modelsForAccount(ctx context.Context, a account, asOf string) ([]modelprovider.Model, error) {
	deployments, err := s.listDeployments(ctx, a.ID)
	if err != nil {
		return nil, err
	}
	lifecycle := s.accountLifecycle(ctx, a.ID)

	out := make([]modelprovider.Model, 0, len(deployments))
	for _, d := range deployments {
		out = append(out, buildModel(d, lifecycle, asOf))
	}
	return out, nil
}

// accountLifecycle reads the account's deployable-model catalog and indexes it by
// name@version for deprecation enrichment. A read failure degrades to an empty index (the
// deployment inventory still stands) rather than aborting the snapshot.
func (s *Source) accountLifecycle(ctx context.Context, accountID string) map[string]accountModel {
	models, err := s.listAccountModels(ctx, accountID)
	if err != nil {
		return nil
	}
	idx := make(map[string]accountModel, len(models))
	for _, m := range models {
		idx[modelKey(m.Name, m.Version)] = m
	}
	return idx
}

// buildModel maps one deployment to a catalog Model. Ref is the deployment name (the
// inference-callable id, matching the usage stream's ModelRef); the underlying model
// format/name/version drive the declared pricing/capabilities; the account-model lifecycle
// supplies deprecation/retirement.
func buildModel(d deployment, lifecycle map[string]accountModel, asOf string) modelprovider.Model {
	mdl := d.Properties.Model
	m := modelprovider.Model{
		ProviderRef:      providerRef,
		Ref:              d.Name,
		DisplayName:      displayName(d),
		Capabilities:     capabilitiesFor(mdl.Format, mdl.Name),
		CapabilitySource: "live",
	}
	if p, ok := pricingFor(mdl.Name); ok {
		pc := p
		m.Pricing = &pc
	}
	if lc, ok := lifecycle[modelKey(mdl.Name, mdl.Version)]; ok {
		applyLifecycle(&m, lc, asOf)
	}
	return m
}

// applyLifecycle marks deprecation and records the per-surface retirement (Foundry) from
// an account-model's lifecycle.
func applyLifecycle(m *modelprovider.Model, lc accountModel, asOf string) {
	status := strings.ToLower(strings.TrimSpace(lc.LifecycleStatus))
	if status == "deprecated" || status == "deprecating" {
		m.Deprecated = true
	}
	if r := strings.TrimSpace(lc.Deprecation.Inference); r != "" {
		m.Retirements = append(m.Retirements, modelprovider.ModelRetirement{
			Surface:   model.GatewayFoundry,
			RetiresOn: r,
			AsOf:      asOf,
		})
	}
}

// displayName renders a human label for a deployment's underlying model + sku.
func displayName(d deployment) string {
	mdl := d.Properties.Model
	parts := strings.TrimSpace(mdl.Name)
	if v := strings.TrimSpace(mdl.Version); v != "" {
		parts += " " + v
	}
	var tags []string
	if f := strings.TrimSpace(mdl.Format); f != "" {
		tags = append(tags, f)
	}
	if sku := strings.TrimSpace(d.Sku.Name); sku != "" {
		tags = append(tags, sku)
	}
	if len(tags) > 0 {
		parts += " (" + strings.Join(tags, "/") + ")"
	}
	if parts == "" {
		return d.Name
	}
	return parts
}

// modelKey is the name@version index key (lower-cased) joining a deployment's underlying
// model to the account-model lifecycle catalog.
func modelKey(name, version string) string {
	return strings.ToLower(strings.TrimSpace(name)) + "@" + strings.ToLower(strings.TrimSpace(version))
}
