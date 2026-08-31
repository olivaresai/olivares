// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package vertex

import (
	"context"
	"net/url"
	"strings"

	"github.com/olivaresai/olivares/connectors/modelprovider"
)

// This file holds the Vertex AI model catalog: the declared, operator-maintainable model
// list (offline-usable) per publisher, the per-family list pricing and the declared
// capability sets, plus the live per-model enrichment.
//
// There is NO stable v1 publisher-models LIST API on Vertex (v1 exposes only a per-model
// GET; a list exists only on the preview v1beta1 surface). So — unlike the gemini
// connector, which lists /v1beta/models live — this connector keeps a declared model list
// and ENRICHES each id via GET /v1/publishers/{publisher}/models/{id} (launchStage /
// versionState) when credentialed. A per-model 404 is tolerated: an id the operator
// declared that the project cannot see keeps its declared entry rather than aborting the
// snapshot (we never fail the whole catalog for one missing model).
//
// IMPORTANT: the prices below are declared LIST prices to VERIFY against the provider's
// pricing page (Gemini: cloud.google.com/vertex-ai/generative-ai/pricing; Claude on
// Vertex: the Anthropic price for the model) — not fabricated metrics (docs/SECURITY-HARDENING.md
// contract). They are matched by model-family prefix (longest-prefix-first) so a new
// model version inherits its family's list price until the operator overrides it. The
// declared id list is a maintainable default; operators refresh it.

// pricingAsOf stamps the declared prices with the date they were recorded.
const pricingAsOf = "2026-06-01"

// declaredModel is one declared catalog entry: a publisher + a bare model id + a label.
type declaredModel struct {
	publisher   string
	id          string
	displayName string
}

// declaredModels is the offline fallback model list (and the set of ids the live mode
// enriches). It names the current Gemini and Claude-on-Vertex foundation models so the
// catalog is useful air-gapped; operators refresh it. Each gets its family pricing + the
// publisher's declared capability set.
var declaredModels = []declaredModel{
	{"google", "gemini-2.5-pro", "Gemini 2.5 Pro"},
	{"google", "gemini-2.5-flash", "Gemini 2.5 Flash"},
	{"google", "gemini-2.0-flash", "Gemini 2.0 Flash"},
	{"anthropic", "claude-opus-4-1", "Claude Opus 4.1 (Vertex)"},
	{"anthropic", "claude-sonnet-4-5", "Claude Sonnet 4.5 (Vertex)"},
	{"anthropic", "claude-3-5-haiku", "Claude Haiku 3.5 (Vertex)"},
}

// geminiCapabilities / claudeCapabilities are the declared capability sets per publisher
// family (surfaced by module X, README.md). Only constants that EXIST in
// modelprovider/catalog.go are used. Claude on Vertex additionally declares extended
// thinking; Gemini does not declare an Anthropic-style thinking surface here.
var (
	geminiCapabilities = []modelprovider.Capability{
		modelprovider.CapStreaming, modelprovider.CapToolUse, modelprovider.CapVision,
		modelprovider.CapPDF, modelprovider.CapStructuredOutputs, modelprovider.CapPromptCaching,
		modelprovider.CapBatch,
	}
	claudeCapabilities = []modelprovider.Capability{
		modelprovider.CapStreaming, modelprovider.CapToolUse, modelprovider.CapVision,
		modelprovider.CapPDF, modelprovider.CapStructuredOutputs, modelprovider.CapPromptCaching,
		modelprovider.CapBatch, modelprovider.CapExtendedThinking,
	}
)

// family is a declared price keyed by a model-id prefix, matched longest-prefix-first.
type family struct {
	prefix  string
	pricing modelprovider.ModelPricing
}

// vertexFamilies are the declared family list prices (USD per million tokens, base tier,
// AsOf pricingAsOf) — verify against each provider's pricing page. Matched longest-prefix-
// first so claude-3-5-haiku beats claude (and gemini-2.5-pro beats gemini-2.5).
var vertexFamilies = []family{
	{prefix: "gemini-2.5-pro", pricing: mp(1.25, 10.00, 0.31)},
	{prefix: "gemini-2.5-flash", pricing: mp(0.30, 2.50, 0.075)},
	{prefix: "gemini-2.0-flash", pricing: mp(0.10, 0.40, 0.025)},
	{prefix: "gemini-1.5-pro", pricing: mp(1.25, 5.00, 0)},
	{prefix: "gemini-1.5-flash", pricing: mp(0.075, 0.30, 0)},
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

// pricingFor returns the declared pricing for a model id, matched by the longest family
// prefix. ok is false when no family matches (the connector then leaves Model.Pricing nil
// rather than guess a price).
func pricingFor(modelID string) (modelprovider.ModelPricing, bool) {
	best := -1
	for i, f := range vertexFamilies {
		if strings.HasPrefix(modelID, f.prefix) {
			if best < 0 || len(f.prefix) > len(vertexFamilies[best].prefix) {
				best = i
			}
		}
	}
	if best < 0 {
		return modelprovider.ModelPricing{}, false
	}
	return vertexFamilies[best].pricing, true
}

// capabilitiesFor returns the declared capability set for a publisher.
func capabilitiesFor(publisher string) []modelprovider.Capability {
	if publisher == "anthropic" {
		return append([]modelprovider.Capability(nil), claudeCapabilities...)
	}
	return append([]modelprovider.Capability(nil), geminiCapabilities...)
}

// Snapshot returns the Vertex catalog. With a credential it enriches each declared model
// via the publisher-model GET (read-only) and stamps launch-stage/deprecation; with no
// credential it returns the declared offline catalog. Keys/Workspaces are intentionally
// empty: Vertex has no per-token API-key/workspace inventory the way the Anthropic/OpenAI
// admin APIs do (project/IAM inventory is the gcp-audit connector's domain).
func (s *Source) Snapshot(ctx context.Context) (modelprovider.Catalog, error) {
	cat := modelprovider.Catalog{
		Provider: modelprovider.Provider{
			Ref: providerRef, Kind: modelprovider.KindHostedAPI,
			Title: "Google (Gemini Enterprise Agent Platform)", BaseURL: s.cfg.aiplatformEndpoint,
		},
		CapturedAt: s.clock(),
	}
	models := make([]modelprovider.Model, 0, len(declaredModels))
	for _, d := range declaredModels {
		if !s.publisherEnabled(d.publisher) {
			continue
		}
		m := buildModel(d)
		if s.cfg.tokens != nil {
			if err := ctx.Err(); err != nil {
				return modelprovider.Catalog{}, err
			}
			if err := s.enrichModel(ctx, &m, d); err != nil {
				// A per-model 404 (the project cannot see this publisher model) keeps the
				// declared entry; any other error aborts the snapshot (a real fault).
				if !isStatus(err, 404) {
					return modelprovider.Catalog{}, err
				}
			}
		}
		models = append(models, m)
	}
	cat.Models = models
	return cat, nil
}

// publisherEnabled reports whether the operator configured this publisher.
func (s *Source) publisherEnabled(publisher string) bool {
	for _, p := range s.cfg.publishers {
		if p == publisher {
			return true
		}
	}
	return false
}

// buildModel assembles the declared modelprovider.Model for a catalog entry, enriched with
// the declared family pricing + the publisher's declared capability set.
func buildModel(d declaredModel) modelprovider.Model {
	m := modelprovider.Model{
		ProviderRef:      providerRef,
		Ref:              d.id,
		DisplayName:      d.displayName,
		Capabilities:     capabilitiesFor(d.publisher),
		CapabilitySource: "declared",
	}
	if p, ok := pricingFor(d.id); ok {
		pc := p
		m.Pricing = &pc
	}
	return m
}

// enrichModel reads the live publisher-model resource and refines the declared entry with
// its launch stage / version state (the deprecation signal). It never overwrites the
// declared capabilities/pricing — the publisher-model GET does not return per-token
// pricing, and its capability surface is not the cross-vendor flag set module X renders.
func (s *Source) enrichModel(ctx context.Context, m *modelprovider.Model, d declaredModel) error {
	var pm publisherModel
	full := joinURL(s.cfg.aiplatformEndpoint, "/v1/publishers/"+url.PathEscape(d.publisher)+"/models/"+url.PathEscape(d.id), nil)
	if err := s.getURL(ctx, full, &pm); err != nil {
		return err
	}
	m.CapabilitySource = "live"
	if deprecatedStage(pm.LaunchStage) || strings.EqualFold(pm.VersionState, "PUBLISHER_MODEL_VERSION_STATE_DEPRECATED") {
		m.Deprecated = true
	}
	return nil
}

// deprecatedStage reports whether a publisher-model launchStage marks it deprecated.
func deprecatedStage(stage string) bool {
	return strings.EqualFold(strings.TrimSpace(stage), "DEPRECATED")
}
