// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package models

import (
	"sort"
	"strings"

	mp "github.com/olivaresai/olivares/connectors/modelprovider"
)

// The reference catalog is the VERSIONED-IN-REPO source of model governance data
// (decision default): a maintainable table of model families with their
// declared API-feature capabilities and list pricing. It reads like a pricing
// page on purpose and is the operator-facing source of truth, distinct from the
// per-connector emit-time pricing in (which derives a CostSample's cost).
//
// These prices are DECLARED LIST DEFAULTS stamped with the date they were
// declared — NOT fabricated telemetry and NOT a guarantee (ARCHITECTURE.md). The
// operator verifies and overrides them against each provider's pricing page; a
// family with no entry stays unpriced (Pricing=nil), the model is never assigned
// an invented price. The capability flags reuse the cross-vendor matrix the
// connectors expose (mp.Capability) so module X renders one matrix and the
// router can require a capability across vendors.
const referencePricingAsOf = "2026-06-05"

// referenceGovernanceAsOf stamps the model-GOVERNANCE dimensions (lifecycle dates,
// retention class, access tier) and the Fable 5 / Mythos 5 launch pricing: the
// Anthropic model-deprecations, launch, pricing and data-retention pages were
// fetched on this date. It is deliberately distinct from
// referencePricingAsOf — the cross-vendor pricing table was last fully verified
// 2026-06-05 and bumping its stamp without re-verifying every row would fabricate
// provenance (ARCHITECTURE.md).
const referenceGovernanceAsOf = "2026-06-09"

// RetentionClass values (Anthropic api-and-data-retention / Covered Models,
// verified referenceGovernanceAsOf). The zero value "" means NOT VERIFIED, which
// is deny-closed under a require_zdr routing policy.
const (
	// retentionZDREligible: standard retention, eligible for zero-data-retention
	// arrangements at the org level.
	retentionZDREligible = "zdr_eligible"
	// retentionCovered: a designated Covered Model — forced data retention
	// (RetentionDays), NEVER available under ZDR.
	retentionCovered = "covered"
)

// accessTierGlasswing is the restricted Project Glasswing access program (Mythos
// models). A family with a non-empty AccessTier is deny-closed in routing unless
// the policy enrolls the tier in access_tiers (lifecycle.go).
const accessTierGlasswing = "glasswing"

// referenceModel is the declared governance reference for one model family. A
// model whose ref starts with Prefix (longest match wins) inherits these fields.
type referenceModel struct {
	// Family is the governance family label (e.g. "claude-opus").
	Family string
	// Prefix is the model-ref prefix this entry matches (longest-prefix wins).
	Prefix string
	// ProviderRef is the natural provider reference (mp.Provider* constant).
	ProviderRef string
	// Capabilities is the declared API-feature set for the family.
	Capabilities []mp.Capability
	// ContextWindow is the declared max input context in tokens (0 if unknown).
	ContextWindow int64
	// MaxOutputTokens is the declared max output in tokens (0 if unknown).
	MaxOutputTokens int64
	// Modality is the primary modality label ("text", "vision").
	Modality string
	// Pricing is the declared list price (nil for local inference: compute, not
	// $/token, so cost cannot be derived from a list price).
	Pricing *mp.ModelPricing

	// --- CLA-16 catalog dimensions (declared, AsOf-stamped, verify against the
	// vendor's docs; never fabricated). ---
	// ServiceTierEligibility lists the billing tiers the family can run on (e.g. the
	// Claude set: standard, batch, priority, priority_on_demand, flex, flex_discount).
	// Empty = not declared.
	ServiceTierEligibility []string
	// DataResidency lists the inference_geo regions the family supports (global, us).
	// Empty = the dimension is not applicable (e.g. models before Feb 2026, which
	// report inference_geo="not_available").
	DataResidency []string
	// USInferenceBurndownMult is the price multiplier for inference_geo="us" (1.1 on
	// Opus 4.6 / Sonnet 4.6 and later; 0 = no US-residency premium / not applicable).
	USInferenceBurndownMult float64
	// CapsToConfirm marks a family whose capability set is NOT yet verified against a
	// model card (e.g. a freshly-announced preview). The catalog lists the model but
	// flags its capabilities as unconfirmed rather than inventing them (ARCHITECTURE.md).
	CapsToConfirm bool

	// --- model-governance dimensions (declared, stamped
	// referenceGovernanceAsOf, verified against the vendor's lifecycle/retention
	// docs; never fabricated). ---
	// RetentionClass is the family's API data-retention class: "" = not verified
	// (deny-closed under a require_zdr routing policy), retentionZDREligible =
	// standard retention eligible for ZDR arrangements, retentionCovered = a
	// designated Covered Model (forced retention, no ZDR).
	RetentionClass string
	// RetentionDays is the forced retention period in days when RetentionClass is
	// retentionCovered (30 for Fable 5 / Mythos 5); 0 otherwise.
	RetentionDays int
	// AccessTier is the restricted-access program the family requires ("" =
	// generally available; accessTierGlasswing = Project Glasswing). Routing is
	// deny-closed on it: the policy must enroll the tier in access_tiers.
	AccessTier string
	// DeprecatedOn / RetiredOn are the family's lifecycle dates (ISO dates) on the
	// ANTHROPIC-OPERATED surfaces (direct API, claude-platform-aws, foundry).
	// Partner surfaces (Bedrock, Vertex) publish their OWN schedules; the
	// per-surface dates live in the connector registry
	// (connectors/claude-api/lifecycle.go). The module-side dates drive the policy
	// denies and follow the Anthropic-operated schedule. Empty = no published date
	// (NOT "never"). Requests to a retired model FAIL at the provider.
	DeprecatedOn string
	RetiredOn    string
	// ReplacementRef is the published successor model id (empty if none named). It
	// is non-sensitive and actionable, so a lifecycle deny may surface it.
	ReplacementRef string
}

// claudeServiceTiers is the confirmed Claude billing-tier set (Anthropic Usage &
// Cost API, verified jun-2026). Mythos Preview is the documented exception: Priority
// Tier is unavailable on it.
var (
	claudeServiceTiers       = []string{"standard", "batch", "priority", "priority_on_demand", "flex", "flex_discount"}
	claudeServiceTiersNoPrio = []string{"standard", "batch"}
	claudeDataResidency      = []string{"global", "us"}
)

// Capability shorthands. The Claude stack is declared in full for the 4.x
// families (the module-X requirement, README.md); cross-vendor families declare
// the analogs they actually expose. Operators override per model.
var (
	capsClaudeFull = []mp.Capability{
		mp.CapStreaming, mp.CapToolUse, mp.CapVision, mp.CapPDF, mp.CapStructuredOutputs,
		mp.CapPromptCaching, mp.CapBatch, mp.CapFiles, mp.CapExtendedThinking,
		mp.CapComputerUse, mp.CapMemoryTool, mp.CapContextManagement, mp.CapCitations,
	}
	capsClaudeHaiku = []mp.Capability{
		mp.CapStreaming, mp.CapToolUse, mp.CapVision, mp.CapPDF, mp.CapStructuredOutputs,
		mp.CapPromptCaching, mp.CapBatch, mp.CapFiles, mp.CapContextManagement, mp.CapCitations,
	}
	// capsClaudeFable is the VERIFIED Fable 5 / Mythos 5 set (Anthropic launch +
	// structured-outputs docs, referenceGovernanceAsOf): streaming, tool use,
	// vision, structured outputs, prompt caching, batch, extended (adaptive)
	// thinking, the memory tool and context editing/compaction. PDF / files /
	// citations / computer use are NOT yet verified for these models and are
	// omitted rather than assumed (ARCHITECTURE.md); effort and task budgets are
	// per-model knobs (mp.ModelCapabilities), not coarse Capability flags.
	capsClaudeFable = []mp.Capability{
		mp.CapStreaming, mp.CapToolUse, mp.CapVision, mp.CapStructuredOutputs,
		mp.CapPromptCaching, mp.CapBatch, mp.CapExtendedThinking,
		mp.CapMemoryTool, mp.CapContextManagement,
	}
	capsOpenAI = []mp.Capability{
		mp.CapStreaming, mp.CapToolUse, mp.CapVision, mp.CapStructuredOutputs,
		mp.CapPromptCaching, mp.CapBatch, mp.CapFiles,
	}
	capsGemini = []mp.Capability{
		mp.CapStreaming, mp.CapToolUse, mp.CapVision, mp.CapPDF,
		mp.CapStructuredOutputs, mp.CapContextManagement,
	}
	capsGLM = []mp.Capability{
		mp.CapStreaming, mp.CapToolUse, mp.CapStructuredOutputs,
		mp.CapPromptCaching, mp.CapExtendedThinking,
	}
	capsGLMVision = []mp.Capability{
		mp.CapStreaming, mp.CapToolUse, mp.CapStructuredOutputs,
		mp.CapPromptCaching, mp.CapExtendedThinking, mp.CapVision,
	}
)

// priceList builds a declared list-price entry (USD/MTok) stamped AsOf+list.
// cacheWrite5m is the standard (5-minute TTL) cache-write rate; cacheWrite1h is the
// distinct 1-hour TTL rate (~2× base input vs ~1.25× for 5m). A family without cache
// write tiers passes 0 for both.
func priceList(in, out, cacheWrite5m, cacheWrite1h, cacheRead float64) *mp.ModelPricing {
	return priceListAsOf(referencePricingAsOf, in, out, cacheWrite5m, cacheWrite1h, cacheRead)
}

// priceListAsOf is priceList with an explicit AsOf stamp, for families verified on
// a different date than the table-wide referencePricingAsOf (e.g. the 2026-06-09
// Fable 5 / Mythos 5 launch pricing) — the stamp must say when THAT price was
// verified, never inherit an older sweep's date.
func priceListAsOf(asOf string, in, out, cacheWrite5m, cacheWrite1h, cacheRead float64) *mp.ModelPricing {
	return &mp.ModelPricing{
		InputPerMTokUSD: in, OutputPerMTokUSD: out,
		CacheWritePerMTokUSD: cacheWrite5m, CacheWrite1hPerMTokUSD: cacheWrite1h,
		CacheReadPerMTokUSD: cacheRead,
		Currency:            "USD", AsOf: asOf, Source: mp.PricingList,
	}
}

// referenceTable is the declared family table, ordered so that longest-prefix
// matching is unambiguous (more specific prefixes appear; lookup picks the
// longest matching prefix regardless of order).
var referenceTable = []referenceModel{
	{
		// Current Opus (4.5/4.6/4.7/4.8): $5/$25 (verified, Anthropic pricing page).
		// Standard models are ZDR-eligible under org arrangements (Covered Models are
		// the documented exception list — api-and-data-retention, governance AsOf).
		Family: "claude-opus", Prefix: "claude-opus", ProviderRef: mp.ProviderAnthropic,
		Capabilities: capsClaudeFull, ContextWindow: 200000, MaxOutputTokens: 32000,
		Modality: "vision", Pricing: priceList(5, 25, 6.25, 10, 0.5),
		ServiceTierEligibility: claudeServiceTiers, DataResidency: claudeDataResidency,
		USInferenceBurndownMult: 1.1, RetentionClass: retentionZDREligible,
	},
	{
		// Opus 5. Its price EQUALS the generic claude-opus entry above ($5/$25,
		// cache 6.25/10/0.50), so this entry changes no figure — it exists for
		// PROVENANCE. Without it, claude-opus-5 resolved through the generic
		// prefix and the cost carried no verification stamp: lookupReference's
		// "the model stays unpriced rather than getting an invented price" guard
		// cannot fire for this family, because the generic prefix always matches.
		// Verified live against platform.claude.com/docs/en/about-claude/pricing
		// on 2026-08-27 (the planner adjudication, F-2).
		Family: "claude-opus-5", Prefix: "claude-opus-5", ProviderRef: mp.ProviderAnthropic,
		Capabilities: capsClaudeFull, ContextWindow: 200000, MaxOutputTokens: 32000,
		Modality: "vision", Pricing: priceListAsOf("2026-08-27", 5, 25, 6.25, 10, 0.50),
		ServiceTierEligibility: claudeServiceTiers, DataResidency: claudeDataResidency,
		USInferenceBurndownMult: 1.1, RetentionClass: retentionZDREligible,
	},
	{
		// Deprecated Opus 4.0 / 4.1 keep the higher tier ($15/$75); longer prefixes
		// than "claude-opus" so current 4.5+ ids resolve to the $5/$25 entry above.
		// Lifecycle (model-deprecations page, governance AsOf): Opus 4.0 deprecated
		// 2026-04-14, retires 2026-06-15, replacement claude-opus-4-8.
		Family: "claude-opus-4-0", Prefix: "claude-opus-4-0", ProviderRef: mp.ProviderAnthropic,
		Capabilities: capsClaudeFull, ContextWindow: 200000, MaxOutputTokens: 32000,
		Modality: "vision", Pricing: priceList(15, 75, 18.75, 30, 1.5),
		ServiceTierEligibility: claudeServiceTiers, RetentionClass: retentionZDREligible,
		DeprecatedOn: "2026-04-14", RetiredOn: "2026-06-15", ReplacementRef: "claude-opus-4-8",
	},
	{
		// The DATED Opus 4.0 id (claude-opus-4-20250514) does not start with the
		// "claude-opus-4-0" alias prefix, so the same family carries a second prefix
		// entry. "claude-opus-4-2025" cannot collide with claude-opus-4-1-20250805
		// (differs at the "1" vs "2").
		Family: "claude-opus-4-0", Prefix: "claude-opus-4-2025", ProviderRef: mp.ProviderAnthropic,
		Capabilities: capsClaudeFull, ContextWindow: 200000, MaxOutputTokens: 32000,
		Modality: "vision", Pricing: priceList(15, 75, 18.75, 30, 1.5),
		ServiceTierEligibility: claudeServiceTiers, RetentionClass: retentionZDREligible,
		DeprecatedOn: "2026-04-14", RetiredOn: "2026-06-15", ReplacementRef: "claude-opus-4-8",
	},
	{
		// Opus 4.1 (alias + dated 20250805): deprecated 2026-06-05, retires
		// 2026-08-05, replacement claude-opus-4-8 (model-deprecations page).
		Family: "claude-opus-4-1", Prefix: "claude-opus-4-1", ProviderRef: mp.ProviderAnthropic,
		Capabilities: capsClaudeFull, ContextWindow: 200000, MaxOutputTokens: 32000,
		Modality: "vision", Pricing: priceList(15, 75, 18.75, 30, 1.5),
		ServiceTierEligibility: claudeServiceTiers, RetentionClass: retentionZDREligible,
		DeprecatedOn: "2026-06-05", RetiredOn: "2026-08-05", ReplacementRef: "claude-opus-4-8",
	},
	{
		Family: "claude-sonnet", Prefix: "claude-sonnet", ProviderRef: mp.ProviderAnthropic,
		Capabilities: capsClaudeFull, ContextWindow: 200000, MaxOutputTokens: 64000,
		Modality: "vision", Pricing: priceList(3, 15, 3.75, 6, 0.3),
		ServiceTierEligibility: claudeServiceTiers, DataResidency: claudeDataResidency,
		USInferenceBurndownMult: 1.1, RetentionClass: retentionZDREligible,
	},
	{
		// Claude Sonnet 5 (GA, VERIFIED 2026-07-03): durable list price $3/$15
		// (cache 2.50/4/0.20), 1M context, 128K output. RetentionClass inherits the
		// Standard Claude verification from the claude-sonnet family, not a new
		// retention verification.
		//
		// ⛔ THIS ENTRY CARRIED $3/$15 FOR 55 DAYS AND IT WAS A PREDICTION, NOT A
		// PRICE. The 2026-07-03 comment said the introductory $2/$10 "is promotional
		// and belongs in operator overrides, not the reference list price" — i.e. it
		// encoded an expectation that the price would rise on 2026-09-01. Anthropic
		// cancelled that increase: the pricing page now states the introductory price
		// IS the standard price. Nothing re-read the prediction, so every Sonnet 5
		// cost the console showed was 1.5x high. Verified live against
		// platform.claude.com/docs/en/about-claude/pricing on 2026-08-27.
		//
		// A dated figure records WHEN someone looked, never that what they saw is
		// still true. This one is now in the re-verification cadence.
		Family: "claude-sonnet-5", Prefix: "claude-sonnet-5", ProviderRef: mp.ProviderAnthropic,
		Capabilities: capsClaudeFull, ContextWindow: 1_000_000, MaxOutputTokens: 128_000,
		Modality: "vision", Pricing: priceListAsOf("2026-08-27", 2, 10, 2.50, 4, 0.20),
		ServiceTierEligibility: claudeServiceTiers, DataResidency: claudeDataResidency,
		USInferenceBurndownMult: 1.1, RetentionClass: retentionZDREligible,
	},
	{
		// Sonnet 4 (alias claude-sonnet-4-0): same family-wide $3/$15 list price, but
		// with the published lifecycle — deprecated 2026-04-14, retires 2026-06-15,
		// replacement claude-sonnet-4-6 (model-deprecations page, governance AsOf).
		// The prefix is longer than "claude-sonnet", so claude-sonnet-4-6 keeps
		// matching the lifecycle-free entry above (differs at the "6" vs "0"). No
		// DataResidency: Sonnet 4 predates inference_geo (Sonnet 4.6+ only).
		Family: "claude-sonnet-4-0", Prefix: "claude-sonnet-4-0", ProviderRef: mp.ProviderAnthropic,
		Capabilities: capsClaudeFull, ContextWindow: 200000, MaxOutputTokens: 64000,
		Modality: "vision", Pricing: priceList(3, 15, 3.75, 6, 0.3),
		ServiceTierEligibility: claudeServiceTiers, RetentionClass: retentionZDREligible,
		DeprecatedOn: "2026-04-14", RetiredOn: "2026-06-15", ReplacementRef: "claude-sonnet-4-6",
	},
	{
		// The DATED Sonnet 4 id (claude-sonnet-4-20250514) needs its own prefix for
		// the same reason as claude-opus-4-2025 above.
		Family: "claude-sonnet-4-0", Prefix: "claude-sonnet-4-2025", ProviderRef: mp.ProviderAnthropic,
		Capabilities: capsClaudeFull, ContextWindow: 200000, MaxOutputTokens: 64000,
		Modality: "vision", Pricing: priceList(3, 15, 3.75, 6, 0.3),
		ServiceTierEligibility: claudeServiceTiers, RetentionClass: retentionZDREligible,
		DeprecatedOn: "2026-04-14", RetiredOn: "2026-06-15", ReplacementRef: "claude-sonnet-4-6",
	},
	{
		// Current Haiku 4.5: $1/$5 (verified). Retired Haiku 3.5 lives under the
		// longer "claude-3-5-haiku" prefix (retired entry below, unpriced).
		Family: "claude-haiku", Prefix: "claude-haiku", ProviderRef: mp.ProviderAnthropic,
		Capabilities: capsClaudeHaiku, ContextWindow: 200000, MaxOutputTokens: 32000,
		Modality: "vision", Pricing: priceList(1, 5, 1.25, 2, 0.10),
		// Haiku 4.5 predates inference_geo (Feb 2026 / 4.6+), so no US-residency tier.
		ServiceTierEligibility: claudeServiceTiers, RetentionClass: retentionZDREligible,
	},
	{
		// Claude Fable 5 (GA 2026-06-09): $10/$50, cache write $12.50 (5m) / $20 (1h),
		// cache read $1 — verified, Anthropic pricing page, governance AsOf. 1M-token
		// context at STANDARD pricing (no long-context premium), 128K max output.
		// Designated a Covered Model: forced 30-day retention, NOT ZDR-eligible.
		// GA flagship — priority eligibility follows the standard API tiers published
		// for Fable 5 in the rate-limits page.
		Family: "claude-fable", Prefix: "claude-fable", ProviderRef: mp.ProviderAnthropic,
		Capabilities: capsClaudeFable, ContextWindow: 1_000_000, MaxOutputTokens: 128_000,
		Modality: "vision", Pricing: priceListAsOf(referenceGovernanceAsOf, 10, 50, 12.50, 20, 1),
		ServiceTierEligibility: claudeServiceTiers, DataResidency: claudeDataResidency,
		USInferenceBurndownMult: 1.1,
		RetentionClass:          retentionCovered, RetentionDays: 30,
	},
	{
		// Claude Mythos 5 (2026-06-09): Fable 5 capabilities WITHOUT safety
		// classifiers (launch page) at the same published pricing, but NOT generally
		// available — limited availability via Project Glasswing only, hence
		// AccessTier (deny-closed in routing) and no Priority eligibility. The prefix
		// is longer than the preview's "claude-mythos", so it wins the prefix match.
		// Covered Model like Fable 5: forced 30-day retention, no ZDR.
		Family: "claude-mythos-5", Prefix: "claude-mythos-5", ProviderRef: mp.ProviderAnthropic,
		Capabilities: capsClaudeFable, ContextWindow: 1_000_000, MaxOutputTokens: 128_000,
		Modality: "vision", Pricing: priceListAsOf(referenceGovernanceAsOf, 10, 50, 12.50, 20, 1),
		ServiceTierEligibility: claudeServiceTiersNoPrio, DataResidency: claudeDataResidency,
		USInferenceBurndownMult: 1.1,
		RetentionClass:          retentionCovered, RetentionDays: 30,
		AccessTier: accessTierGlasswing,
	},
	{
		// Claude Mythos Preview: the model EXISTS (confirmed across Anthropic docs),
		// but its capabilities and pricing are NOT verified — listed with caps flagged
		// to-confirm and Pricing nil (never fabricated). Priority Tier is documented as
		// unavailable on Mythos, so it is excluded from the tier eligibility set.
		// Deprecations-page note: "will be retired after claude-mythos-5 becomes
		// available" — Mythos 5 became available 2026-06-09, so the preview is
		// deprecated as of that date; no retirement date is published (RetiredOn "").
		// Glasswing-gated like its successor. RetentionClass stays "" (not verified
		// for the preview — deny-closed under require_zdr).
		Family: "claude-mythos", Prefix: "claude-mythos", ProviderRef: mp.ProviderAnthropic,
		Capabilities: nil, ContextWindow: 200000, Modality: "vision", Pricing: nil,
		ServiceTierEligibility: claudeServiceTiersNoPrio, DataResidency: claudeDataResidency,
		CapsToConfirm: true,
		AccessTier:    accessTierGlasswing,
		DeprecatedOn:  "2026-06-09", ReplacementRef: "claude-mythos-5",
	},
	// --- RETIRED claude-3 generation (model-deprecations page, governance AsOf).
	// Minimal entries that exist so the retired-model routing deny (lifecycle.go)
	// recognizes them: lifecycle dates + replacement only, Pricing nil (legacy list
	// prices are NOT resurrected module-side — a retired model must never be priced
	// into a cost decision), Capabilities nil. Dates are the Anthropic-operated
	// schedule; claude-3-5-haiku is still served on Bedrock/Vertex (their own
	// schedules live in the connector registry). Standard (non-Covered) models:
	// RetentionClass zdr_eligible.
	{
		Family: "claude-3-opus", Prefix: "claude-3-opus", ProviderRef: mp.ProviderAnthropic,
		RetentionClass: retentionZDREligible,
		DeprecatedOn:   "2025-06-30", RetiredOn: "2026-01-05", ReplacementRef: "claude-opus-4-8",
	},
	{
		Family: "claude-3-7-sonnet", Prefix: "claude-3-7-sonnet", ProviderRef: mp.ProviderAnthropic,
		RetentionClass: retentionZDREligible,
		DeprecatedOn:   "2025-10-28", RetiredOn: "2026-02-19", ReplacementRef: "claude-sonnet-4-6",
	},
	{
		// Covers both dated ids (20241022 and 20240620) — same schedule.
		Family: "claude-3-5-sonnet", Prefix: "claude-3-5-sonnet", ProviderRef: mp.ProviderAnthropic,
		RetentionClass: retentionZDREligible,
		DeprecatedOn:   "2025-08-13", RetiredOn: "2025-10-28", ReplacementRef: "claude-sonnet-4-6",
	},
	{
		// "claude-3-sonnet" cannot collide with claude-3-5/3-7-sonnet (differs at the
		// "s" vs "5"/"7").
		Family: "claude-3-sonnet", Prefix: "claude-3-sonnet", ProviderRef: mp.ProviderAnthropic,
		RetentionClass: retentionZDREligible,
		DeprecatedOn:   "2025-01-21", RetiredOn: "2025-07-21", ReplacementRef: "claude-sonnet-4-6",
	},
	{
		Family: "claude-3-5-haiku", Prefix: "claude-3-5-haiku", ProviderRef: mp.ProviderAnthropic,
		RetentionClass: retentionZDREligible,
		DeprecatedOn:   "2025-12-19", RetiredOn: "2026-02-19", ReplacementRef: "claude-haiku-4-5-20251001",
	},
	{
		Family: "claude-3-haiku", Prefix: "claude-3-haiku", ProviderRef: mp.ProviderAnthropic,
		RetentionClass: retentionZDREligible,
		DeprecatedOn:   "2026-02-19", RetiredOn: "2026-04-20", ReplacementRef: "claude-haiku-4-5-20251001",
	},
	// --- Cross-vendor families. RetentionClass stays "" on every OpenAI/Gemini/GLM
	// entry: their retention/ZDR posture is NOT verified against vendor docs, and
	// "" is deny-closed under a require_zdr routing policy — the honest behavior
	// (ARCHITECTURE.md) until an operator verifies and overrides.
	{
		Family: "gpt-4o-mini", Prefix: "gpt-4o-mini", ProviderRef: mp.ProviderOpenAI,
		Capabilities: capsOpenAI, ContextWindow: 128000, MaxOutputTokens: 16384,
		Modality: "vision", Pricing: priceList(0.15, 0.6, 0, 0, 0.075),
	},
	{
		Family: "gpt-4o", Prefix: "gpt-4o", ProviderRef: mp.ProviderOpenAI,
		Capabilities: capsOpenAI, ContextWindow: 128000, MaxOutputTokens: 16384,
		Modality: "vision", Pricing: priceList(2.5, 10, 0, 0, 1.25),
	},
	{
		Family: "o1", Prefix: "o1", ProviderRef: mp.ProviderOpenAI,
		Capabilities: capsOpenAI, ContextWindow: 200000, MaxOutputTokens: 100000,
		Modality: "text", Pricing: priceList(15, 60, 0, 0, 7.5),
	},
	{
		Family: "gemini-2.0-flash", Prefix: "gemini-2.0-flash", ProviderRef: mp.ProviderGoogle,
		Capabilities: capsGemini, ContextWindow: 1000000, MaxOutputTokens: 8192,
		Modality: "vision", Pricing: priceList(0.1, 0.4, 0, 0, 0),
	},
	{
		Family: "gemini-1.5-flash", Prefix: "gemini-1.5-flash", ProviderRef: mp.ProviderGoogle,
		Capabilities: capsGemini, ContextWindow: 1000000, MaxOutputTokens: 8192,
		Modality: "vision", Pricing: priceList(0.075, 0.3, 0, 0, 0),
	},
	{
		Family: "gemini-1.5-pro", Prefix: "gemini-1.5-pro", ProviderRef: mp.ProviderGoogle,
		Capabilities: capsGemini, ContextWindow: 2000000, MaxOutputTokens: 8192,
		Modality: "vision", Pricing: priceList(1.25, 5, 0, 0, 0),
	},
	// The thirteen GLM figures below were re-verified LIVE against
	// docs.z.ai/guides/overview/pricing on 2026-08-27T01:07Z by the planner, and all
	// thirteen held at the values they had carried since 2026-07-08 - so the stamp
	// moves and the numbers do not. The owner of that check is named on purpose:
	// an unowned stamp is how the Sonnet 5 list price stayed 1.5x high for 55 days.
	// Staleness is age x provider volatility, not age alone.
	{
		Family: "glm-5.2", Prefix: "glm-5.2", ProviderRef: mp.ProviderGLM,
		Capabilities: capsGLM, ContextWindow: 1_000_000,
		Modality: "text", Pricing: priceListAsOf("2026-08-27", 1.40, 4.40, 0, 0, 0.26),
		RetentionClass: "", AccessTier: "",
	},
	{
		Family: "glm-4.7-flashx", Prefix: "glm-4.7-flashx", ProviderRef: mp.ProviderGLM,
		Capabilities: capsGLM, ContextWindow: 128_000,
		Modality: "text", Pricing: priceListAsOf("2026-08-27", 0.07, 0.40, 0, 0, 0.01),
		RetentionClass: "", AccessTier: "",
	},
	{
		Family: "glm-4.7-flash", Prefix: "glm-4.7-flash", ProviderRef: mp.ProviderGLM,
		Capabilities: capsGLM, ContextWindow: 128_000,
		Modality: "text", Pricing: priceListAsOf("2026-08-27", 0, 0, 0, 0, 0),
		RetentionClass: "", AccessTier: "",
	},
	{
		Family: "glm-4.7", Prefix: "glm-4.7", ProviderRef: mp.ProviderGLM,
		Capabilities: capsGLM,
		Modality:     "text", Pricing: priceListAsOf("2026-08-27", 0.60, 2.20, 0, 0, 0.11),
		RetentionClass: "", AccessTier: "",
	},
	{
		Family: "glm-4.6v", Prefix: "glm-4.6v", ProviderRef: mp.ProviderGLM,
		Capabilities: capsGLMVision,
		Modality:     "vision", Pricing: priceListAsOf("2026-08-27", 0.30, 0.90, 0, 0, 0.05),
		RetentionClass: "", AccessTier: "",
	},
	{
		Family: "glm-4.6", Prefix: "glm-4.6", ProviderRef: mp.ProviderGLM,
		Capabilities: capsGLM, ContextWindow: 200_000, MaxOutputTokens: 128_000,
		Modality: "text", Pricing: priceListAsOf("2026-08-27", 0.60, 2.20, 0, 0, 0.11),
		RetentionClass: "", AccessTier: "",
	},
	{
		Family: "glm-4.5v", Prefix: "glm-4.5v", ProviderRef: mp.ProviderGLM,
		Capabilities: capsGLMVision,
		Modality:     "vision", Pricing: priceListAsOf("2026-08-27", 0.60, 1.80, 0, 0, 0.11),
		RetentionClass: "", AccessTier: "",
	},
	{
		Family: "glm-4.5-airx", Prefix: "glm-4.5-airx", ProviderRef: mp.ProviderGLM,
		Capabilities: capsGLM, ContextWindow: 128_000, MaxOutputTokens: 96_000,
		Modality: "text", Pricing: priceListAsOf("2026-08-27", 1.10, 4.50, 0, 0, 0.22),
		RetentionClass: "", AccessTier: "",
	},
	{
		Family: "glm-4.5-air", Prefix: "glm-4.5-air", ProviderRef: mp.ProviderGLM,
		Capabilities: capsGLM, ContextWindow: 128_000, MaxOutputTokens: 96_000,
		Modality: "text", Pricing: priceListAsOf("2026-08-27", 0.20, 1.10, 0, 0, 0.03),
		RetentionClass: "", AccessTier: "",
	},
	{
		Family: "glm-4.5-x", Prefix: "glm-4.5-x", ProviderRef: mp.ProviderGLM,
		Capabilities: capsGLM, ContextWindow: 128_000, MaxOutputTokens: 96_000,
		Modality: "text", Pricing: priceListAsOf("2026-08-27", 2.20, 8.90, 0, 0, 0.45),
		RetentionClass: "", AccessTier: "",
	},
	{
		Family: "glm-4.5-flash", Prefix: "glm-4.5-flash", ProviderRef: mp.ProviderGLM,
		Capabilities: capsGLM, ContextWindow: 128_000, MaxOutputTokens: 96_000,
		Modality: "text", Pricing: priceListAsOf("2026-08-27", 0, 0, 0, 0, 0),
		RetentionClass: "", AccessTier: "",
	},
	{
		Family: "glm-4.5", Prefix: "glm-4.5", ProviderRef: mp.ProviderGLM,
		Capabilities: capsGLM, ContextWindow: 128_000, MaxOutputTokens: 96_000,
		Modality: "text", Pricing: priceListAsOf("2026-08-27", 0.60, 2.20, 0, 0, 0.11),
		RetentionClass: "", AccessTier: "",
	},
	{
		Family: "glm-4-32b-0414-128k", Prefix: "glm-4-32b-0414-128k", ProviderRef: mp.ProviderGLM,
		Capabilities: capsGLM, ContextWindow: 128_000,
		Modality: "text", Pricing: priceListAsOf("2026-08-27", 0.10, 0.10, 0, 0, 0),
		RetentionClass: "", AccessTier: "",
	},
}

// lookupReference returns the declared reference for a model ref by longest-prefix
// match, or ok=false when no family matches (the model stays unpriced rather than
// getting an invented price). Matching is case-insensitive on the model ref.
func lookupReference(modelRef string) (referenceModel, bool) {
	ref := strings.ToLower(strings.TrimSpace(modelRef))
	if ref == "" {
		return referenceModel{}, false
	}
	best := -1
	var match referenceModel
	for _, e := range referenceTable {
		if strings.HasPrefix(ref, e.Prefix) && len(e.Prefix) > best {
			best = len(e.Prefix)
			match = e
		}
	}
	return match, best >= 0
}

// MaxCoveredRetentionDays reports the provider-forced retention floor declared
// in the reference catalog: the maximum RetentionDays across families whose
// RetentionClass is retentionCovered, plus the deduplicated Family labels of
// every covered family, sorted (deterministic). The Covered Models designation
// was verified 2026-06-10 against Anthropic's api-and-data-retention page:
// Claude Fable 5 and Claude Mythos 5 carry forced ≥30-day retention with NO ZDR
// (a ZDR org gets a 400 invoking them), effective 2026-06-09
// (referenceGovernanceAsOf). Consumes this as the tenant retention-schedule
// floor (the retention contract's annotate-not-reject rule): deleting OUR copy before
// the floor is legitimate; promising total deletion below it is not. Returns
// (0, nil) when no family is covered — the caller reports
// provider_floor_known=false, never a fabricated floor.
func MaxCoveredRetentionDays() (days int, families []string) {
	seen := map[string]bool{}
	for _, e := range referenceTable {
		if e.RetentionClass != retentionCovered || seen[e.Family] {
			continue
		}
		seen[e.Family] = true
		families = append(families, e.Family)
		if e.RetentionDays > days {
			days = e.RetentionDays
		}
	}
	sort.Strings(families)
	return days, families
}

// perTokenMicroUSD converts a USD/MTok list price to integer micro-USD per token
// (the core Model.InputCostMicroUSD/OutputCostMicroUSD unit). A price of $P per
// million tokens is exactly P micro-USD per token (pricing.go), so the value
// is round(P). This per-token integer is coarse for sub-µUSD models (it floors to
// 0); the precise USD/MTok figure is preserved in Model.Metadata and used for the
// catalog display, while the authoritative cost of any real usage is the
// connector-derived CostSample, never this convenience field.
func perTokenMicroUSD(perMTokUSD float64) int64 {
	if perMTokUSD <= 0 {
		return 0
	}
	return int64(perMTokUSD + 0.5)
}
