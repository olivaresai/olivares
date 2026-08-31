// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package claudeapi

import "github.com/olivaresai/olivares/connectors/modelprovider"

// This file holds the declared, operator-maintainable Claude catalog data:
// per-family list pricing and the Claude-stack capability set. Live mode replaces
// the offline model list with the provider's /v1/models response and enriches each
// model with the pricing/capabilities declared here; pricing is matched by model
// family prefix so a new model version inherits its family's declared list price
// until the operator overrides it. These are list prices to VERIFY against
// console.anthropic.com/pricing — not fabricated metrics (docs/SECURITY-HARDENING.md contract).

// pricingAsOf stamps the declared prices with the date they were recorded
// (re-verified 2026-06-09 against the pricing page + the Fable 5 / Mythos 5 launch
// page, which added the 10/50 tier).
const pricingAsOf = "2026-06-09"

// claudeStackCapabilities is the full Claude stack surfaced by module X (README.md
// §2): the modern Claude 4.x models support the whole set. It is declared per
// family below; a connector may refine it per model when the API exposes more.
var claudeStackCapabilities = []modelprovider.Capability{
	modelprovider.CapStreaming,
	modelprovider.CapToolUse,
	modelprovider.CapVision,
	modelprovider.CapPDF,
	modelprovider.CapStructuredOutputs,
	modelprovider.CapPromptCaching,
	modelprovider.CapBatch,
	modelprovider.CapFiles,
	modelprovider.CapExtendedThinking,
	modelprovider.CapComputerUse,
	modelprovider.CapMemoryTool,
	modelprovider.CapContextManagement,
	modelprovider.CapCitations,
}

// family is a declared price + context window keyed by a model-id prefix. The
// blended/cache prices are the long-stable Claude list prices per tier; matching
// by prefix means a new "claude-opus-*" version inherits the Opus tier until the
// operator updates the table.
type family struct {
	prefix        string
	pricing       modelprovider.ModelPricing
	contextWindow int64
	maxOutput     int64
}

// claudeFamilies are matched longest-prefix-first by pricingFor. Prices are USD
// per million tokens (list, AsOf pricingAsOf), VERIFIED against the Anthropic
// pricing page (platform.claude.com/docs/en/about-claude/pricing): current Opus
// (4.5/4.6/4.7/4.8) 5/25, current Sonnet (4.x) 3/15, current Haiku 4.5 1/5, with the
// two cache-write tiers (5m = 1.25× base input, 1h = 2.0× base input) and cache-read
// (0.1× base input). DEPRECATED Opus 4.0/4.1 keep their higher 15/75 tier and
// RETIRED Haiku 3.5 keeps 0.80/4 — matched by their longer, version-specific prefixes
// so the modern generic prefix resolves to the current price.
var claudeFamilies = []family{
	{
		prefix: "claude-opus",
		pricing: modelprovider.ModelPricing{
			InputPerMTokUSD: 5, OutputPerMTokUSD: 25,
			CacheWritePerMTokUSD: 6.25, CacheWrite1hPerMTokUSD: 10, CacheReadPerMTokUSD: 0.50,
			Currency: "USD", AsOf: pricingAsOf, Source: modelprovider.PricingList,
		},
		contextWindow: 200000, maxOutput: 32000,
	},
	{
		prefix: "claude-sonnet",
		pricing: modelprovider.ModelPricing{
			InputPerMTokUSD: 3, OutputPerMTokUSD: 15,
			CacheWritePerMTokUSD: 3.75, CacheWrite1hPerMTokUSD: 6, CacheReadPerMTokUSD: 0.30,
			Currency: "USD", AsOf: pricingAsOf, Source: modelprovider.PricingList,
		},
		contextWindow: 200000, maxOutput: 64000,
	},
	// Claude Sonnet 5: VERIFIED 2026-07-03 against models/overview + pricing. The
	// introductory $2/$10/MTok price through 2026-08-31 is promotional, so the durable
	// list price declared here stays $3/$15; operators can override during the promo
	// window. Remove this note after 2026-08-31 once the promo window is historical.
	{
		prefix: "claude-sonnet-5",
		pricing: modelprovider.ModelPricing{
			InputPerMTokUSD: 3, OutputPerMTokUSD: 15,
			CacheWritePerMTokUSD: 3.75, CacheWrite1hPerMTokUSD: 6, CacheReadPerMTokUSD: 0.30,
			Currency: "USD", AsOf: "2026-07-03", Source: modelprovider.PricingList,
		},
		contextWindow: 1000000, maxOutput: 128000,
	},
	{
		prefix: "claude-haiku",
		pricing: modelprovider.ModelPricing{
			InputPerMTokUSD: 1, OutputPerMTokUSD: 5,
			CacheWritePerMTokUSD: 1.25, CacheWrite1hPerMTokUSD: 2, CacheReadPerMTokUSD: 0.10,
			Currency: "USD", AsOf: pricingAsOf, Source: modelprovider.PricingList,
		},
		contextWindow: 200000, maxOutput: 64000,
	},
	// Fable 5 / Mythos 5 (launched + GA 2026-06-09): IDENTICAL pricing for both —
	// $10/MTok in, $50/MTok out, $12.50 5m cache write, $20 1h cache write, $1 cache
	// read; 1M context at standard pricing (no long-context premium); 128K max output.
	// VERIFIED 2026-06-09 against the pricing page + the launch page. The Mythos
	// prefix is the version-specific "claude-mythos-5": claude-mythos-preview is NOT
	// priced (no published list price — it stays unmatched, never a guessed price).
	{
		prefix: "claude-fable",
		pricing: modelprovider.ModelPricing{
			InputPerMTokUSD: 10, OutputPerMTokUSD: 50,
			CacheWritePerMTokUSD: 12.50, CacheWrite1hPerMTokUSD: 20, CacheReadPerMTokUSD: 1,
			Currency: "USD", AsOf: pricingAsOf, Source: modelprovider.PricingList,
		},
		contextWindow: 1000000, maxOutput: 128000,
	},
	{
		prefix: "claude-mythos-5",
		pricing: modelprovider.ModelPricing{
			InputPerMTokUSD: 10, OutputPerMTokUSD: 50,
			CacheWritePerMTokUSD: 12.50, CacheWrite1hPerMTokUSD: 20, CacheReadPerMTokUSD: 1,
			Currency: "USD", AsOf: pricingAsOf, Source: modelprovider.PricingList,
		},
		contextWindow: 1000000, maxOutput: 128000,
	},
	// Deprecated Opus 4.0 / 4.1 keep the higher tier (15/75). Longer prefixes than
	// "claude-opus" so the current 4.5+ ids fall through to the 5/25 entry above.
	{
		prefix: "claude-opus-4-0",
		pricing: modelprovider.ModelPricing{
			InputPerMTokUSD: 15, OutputPerMTokUSD: 75,
			CacheWritePerMTokUSD: 18.75, CacheWrite1hPerMTokUSD: 30, CacheReadPerMTokUSD: 1.5,
			Currency: "USD", AsOf: pricingAsOf, Source: modelprovider.PricingList,
		},
		contextWindow: 200000, maxOutput: 32000,
	},
	{
		prefix: "claude-opus-4-1",
		pricing: modelprovider.ModelPricing{
			InputPerMTokUSD: 15, OutputPerMTokUSD: 75,
			CacheWritePerMTokUSD: 18.75, CacheWrite1hPerMTokUSD: 30, CacheReadPerMTokUSD: 1.5,
			Currency: "USD", AsOf: pricingAsOf, Source: modelprovider.PricingList,
		},
		contextWindow: 200000, maxOutput: 32000,
	},
	// Legacy Claude 3 generation, named by tier (claude-3-5-haiku-…, claude-3-opus-…).
	{
		prefix: "claude-3-opus",
		pricing: modelprovider.ModelPricing{
			InputPerMTokUSD: 15, OutputPerMTokUSD: 75,
			CacheWritePerMTokUSD: 18.75, CacheWrite1hPerMTokUSD: 30, CacheReadPerMTokUSD: 1.5,
			Currency: "USD", AsOf: pricingAsOf, Source: modelprovider.PricingList,
		},
		contextWindow: 200000, maxOutput: 4096,
	},
	{
		prefix: "claude-3-5-sonnet",
		pricing: modelprovider.ModelPricing{
			InputPerMTokUSD: 3, OutputPerMTokUSD: 15,
			CacheWritePerMTokUSD: 3.75, CacheWrite1hPerMTokUSD: 6, CacheReadPerMTokUSD: 0.30,
			Currency: "USD", AsOf: pricingAsOf, Source: modelprovider.PricingList,
		},
		contextWindow: 200000, maxOutput: 8192,
	},
	{
		prefix: "claude-3-5-haiku",
		pricing: modelprovider.ModelPricing{
			InputPerMTokUSD: 0.80, OutputPerMTokUSD: 4,
			CacheWritePerMTokUSD: 1.0, CacheWrite1hPerMTokUSD: 1.60, CacheReadPerMTokUSD: 0.08,
			Currency: "USD", AsOf: pricingAsOf, Source: modelprovider.PricingList,
		},
		contextWindow: 200000, maxOutput: 8192,
	},
}

// pricingFor returns the declared pricing and context/output limits for a model
// id, matched by the longest family prefix. ok is false when no family matches
// (the connector then leaves Model.Pricing nil rather than guess a price).
func pricingFor(modelID string) (modelprovider.ModelPricing, int64, int64, bool) {
	best := -1
	for i, f := range claudeFamilies {
		if hasPrefix(modelID, f.prefix) {
			if best < 0 || len(f.prefix) > len(claudeFamilies[best].prefix) {
				best = i
			}
		}
	}
	if best < 0 {
		return modelprovider.ModelPricing{}, 0, 0, false
	}
	f := claudeFamilies[best]
	return f.pricing, f.contextWindow, f.maxOutput, true
}

// hasPrefix is strings.HasPrefix without importing strings into this small file.
func hasPrefix(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}

// declaredModels is the offline fallback model list returned by Snapshot when no
// admin credential is configured (live mode replaces it with /v1/models). It names
// the current Claude 4.x models so the catalog is useful air-gapped; operators
// refresh it from the API. Each gets its family pricing + the Claude stack.
var declaredModelIDs = []struct {
	id          string
	displayName string
}{
	{"claude-fable-5", "Claude Fable 5"}, // GA day 1 (launched 2026-06-09)
	{"claude-opus-4-8", "Claude Opus 4.8"},
	{"claude-sonnet-5", "Claude Sonnet 5"},
	{"claude-sonnet-4-6", "Claude Sonnet 4.6"},
	{"claude-haiku-4-5", "Claude Haiku 4.5"},
	// claude-mythos-5 is deliberately NOT declared: it is limited-availability via
	// Project Glasswing (restricted access tier, NOT generally available), so the
	// live /v1/models lists it only for orgs with Glasswing access — declaring it in
	// the offline catalog would fabricate access the org may not have.
}

// buildModel assembles a modelprovider.Model for a Claude model id, enriching it
// with the declared family pricing, context window and the Claude stack. It is the
// single place live and offline catalog builds converge.
func buildModel(id, displayName string) modelprovider.Model {
	m := modelprovider.Model{
		ProviderRef:  modelprovider.ProviderAnthropic,
		Ref:          id,
		DisplayName:  displayName,
		Capabilities: append([]modelprovider.Capability(nil), claudeStackCapabilities...),
		// Offline build: the capability set is the declared stack, not the live API
		// (ANT2-16). A live fetchModels overrides CapabilitySource with "live".
		CapabilitySource: "declared",
		// Per-surface retirement schedule (ANT2-03), attached to both the live and the
		// offline catalog so a model's divergent sunset dates travel with it.
		Retirements: RetirementsFor(id),
		// Per-surface context-window overlay (ANT2-01): non-nil only for a model whose
		// window diverges across surfaces (e.g. Opus 4.8: 1M standard, 200K on Foundry).
		SurfaceContextWindows: SurfaceContextWindowsFor(id),
	}
	if p, ctx, maxOut, ok := pricingFor(id); ok {
		pc := p
		m.Pricing = &pc
		m.ContextWindow = ctx
		m.MaxOutputTokens = maxOut
	}
	// The family table (above) carries a single coarse ContextWindow; the per-surface
	// overlay (contextwindow.go) is the precise, verified successor for the current
	// generation. Prefer its STANDARD window so Model.ContextWindow agrees with
	// SurfaceContextWindows (e.g. Opus 4.8 / Sonnet 4.6 report 1M, not the coarse 200K
	// floor) — both offline and live (fetchModels overrides MaxInput/MaxOutput, not
	// ContextWindow). An unlisted id keeps the family value.
	if std, ok := StandardContextWindow(id); ok {
		m.ContextWindow = std
	}
	// wire effort defaults and the 300k-output beta per (model, surface).
	if defEffort, _, ok := DefaultEffortFor(id); ok {
		m.DefaultEffort = defEffort
	}
	if maxOuts := SurfaceMaxOutputsFor(id); len(maxOuts) > 0 {
		m.SurfaceMaxOutputs = maxOuts
	}
	return m
}
