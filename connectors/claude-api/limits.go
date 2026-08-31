// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// This file declares the Anthropic PLATFORM LIMITS as versioned reference data
//: the API spend-tier ladder, the per-model-class rate limits and the
// 2026-05-06 subscription limit change. Like adminAPIFamilies (analytics.go) it is
// DESCRIPTIVE, AsOf-stamped data for governance/FinOps consumers — declared
// knowledge, never telemetry, never fabricated: every number below is verbatim from
// the cited authority, and what the authority does not publish (the absolute
// subscription token ceilings) is marked to-confirm, not invented (ARCHITECTURE.md). The org's ACTUAL limits remain the read-only Rate Limits API
// (governance.go, ANT2-05); this table is the published default ladder those
// org-specific values are compared against.
//
// Authority (fetched 2026-06-10): platform.claude.com/docs/en/api/rate-limits
// (post-2026-05-06 values); anthropic.com/news/higher-limits-spacex (the 2026-05-06
// subscription change).
package claudeapi

// limitsAsOf stamps the declared platform-limit tables with the date they were
// recorded from the rate-limits page.
const limitsAsOf = "2026-06-10"

// APIUsageTier is one row of the published API spend-tier ladder. Money here is
// WHOLE USD (int64, field names ...USD), a deliberate choice: these are published
// list THRESHOLDS an operator compares against a pricing page — reference data, not
// metered cost — so micro-USD precision would only obscure them. The metered cost
// path stays int64 micro-USD (model.CostSample), unchanged.
type APIUsageTier struct {
	// Tier is the tier name ("tier1".."tier4", "monthly_invoicing").
	Tier string
	// CreditPurchaseUSD is the cumulative credit purchase that advances an org into
	// this tier (0 where the tier is not credit-gated, i.e. monthly invoicing).
	CreditPurchaseUSD int64
	// MaxCreditPurchaseUSD is the maximum single credit purchase at this tier
	// (0 when not applicable — monthly invoicing buys no prepaid credit).
	MaxCreditPurchaseUSD int64
	// MonthlySpendLimitUSD is the monthly spend ceiling at this tier. It is 0 ONLY
	// when NoMonthlyLimit is true — never an unknown.
	MonthlySpendLimitUSD int64
	// NoMonthlyLimit marks the invoiced tier with no monthly spend limit.
	NoMonthlyLimit bool
	// AsOf stamps when the row was recorded.
	AsOf string
}

// apiUsageTiers is the published spend-tier ladder (rate-limits page, fetched
// 2026-06-10, post-2026-05-06 values).
var apiUsageTiers = []APIUsageTier{
	{Tier: "tier1", CreditPurchaseUSD: 5, MaxCreditPurchaseUSD: 500, MonthlySpendLimitUSD: 500, AsOf: limitsAsOf},
	{Tier: "tier2", CreditPurchaseUSD: 40, MaxCreditPurchaseUSD: 500, MonthlySpendLimitUSD: 500, AsOf: limitsAsOf},
	{Tier: "tier3", CreditPurchaseUSD: 200, MaxCreditPurchaseUSD: 1000, MonthlySpendLimitUSD: 1000, AsOf: limitsAsOf},
	{Tier: "tier4", CreditPurchaseUSD: 400, MaxCreditPurchaseUSD: 200000, MonthlySpendLimitUSD: 200000, AsOf: limitsAsOf},
	{Tier: "monthly_invoicing", NoMonthlyLimit: true, AsOf: limitsAsOf},
}

// ModelClassRateLimit is one (tier, model class) row of the published default
// rate-limit table: requests per minute plus input/output tokens per minute. Two
// table-wide rules from the rate-limits page: (1) limits are SHARED across
// inference_geo values (us traffic and global traffic draw the same bucket); and
// (2) ITPM is cache-aware — cache reads do NOT count toward ITPM — EXCEPT on the
// Haiku 3.5 class, where cache reads DO count (the per-row Note carries that
// caveat). Model classes are the page's grouping vocabulary, not exact model ids
// ("claude-sonnet-4.x" pools every Sonnet 4 version; "claude-opus-4.x" pools Opus
// 4.8/4.7/4.6/4.5/4.1/4).
type ModelClassRateLimit struct {
	// Tier is the spend tier the row applies to ("tier1".."tier4").
	Tier string
	// ModelClass is the rate-limit class the page groups models into.
	ModelClass string
	// RPM is requests per minute.
	RPM int64
	// ITPM is input tokens per minute (cache-aware except where Note says otherwise).
	ITPM int64
	// OTPM is output tokens per minute.
	OTPM int64
	// Note carries a per-class caveat verbatim from the page ("" = none).
	Note string
	// AsOf stamps when the row was recorded.
	AsOf string
}

// haiku35CacheNote is the one per-class ITPM caveat the rate-limits page publishes.
const haiku35CacheNote = "cache_read_input_tokens count toward ITPM on this model class only"

// modelClassRateLimits is the published default per-model-class table (rate-limits
// page, fetched 2026-06-10, post-2026-05-06 values). Five classes x four tiers.
var modelClassRateLimits = []ModelClassRateLimit{
	{Tier: "tier1", ModelClass: "claude-fable-5", RPM: 50, ITPM: 100_000, OTPM: 20_000, AsOf: limitsAsOf},
	{Tier: "tier2", ModelClass: "claude-fable-5", RPM: 1000, ITPM: 500_000, OTPM: 100_000, AsOf: limitsAsOf},
	{Tier: "tier3", ModelClass: "claude-fable-5", RPM: 2000, ITPM: 1_500_000, OTPM: 300_000, AsOf: limitsAsOf},
	{Tier: "tier4", ModelClass: "claude-fable-5", RPM: 4000, ITPM: 4_000_000, OTPM: 800_000, AsOf: limitsAsOf},

	{Tier: "tier1", ModelClass: "claude-sonnet-4.x", RPM: 50, ITPM: 30_000, OTPM: 8_000, AsOf: limitsAsOf},
	{Tier: "tier2", ModelClass: "claude-sonnet-4.x", RPM: 1000, ITPM: 450_000, OTPM: 90_000, AsOf: limitsAsOf},
	{Tier: "tier3", ModelClass: "claude-sonnet-4.x", RPM: 2000, ITPM: 800_000, OTPM: 160_000, AsOf: limitsAsOf},
	{Tier: "tier4", ModelClass: "claude-sonnet-4.x", RPM: 4000, ITPM: 2_000_000, OTPM: 400_000, AsOf: limitsAsOf},

	{Tier: "tier1", ModelClass: "claude-haiku-4-5", RPM: 50, ITPM: 50_000, OTPM: 10_000, AsOf: limitsAsOf},
	{Tier: "tier2", ModelClass: "claude-haiku-4-5", RPM: 1000, ITPM: 450_000, OTPM: 90_000, AsOf: limitsAsOf},
	{Tier: "tier3", ModelClass: "claude-haiku-4-5", RPM: 2000, ITPM: 1_000_000, OTPM: 200_000, AsOf: limitsAsOf},
	{Tier: "tier4", ModelClass: "claude-haiku-4-5", RPM: 4000, ITPM: 4_000_000, OTPM: 800_000, AsOf: limitsAsOf},

	{Tier: "tier1", ModelClass: "claude-haiku-3-5", RPM: 50, ITPM: 50_000, OTPM: 10_000, Note: haiku35CacheNote, AsOf: limitsAsOf},
	{Tier: "tier2", ModelClass: "claude-haiku-3-5", RPM: 1000, ITPM: 100_000, OTPM: 20_000, Note: haiku35CacheNote, AsOf: limitsAsOf},
	{Tier: "tier3", ModelClass: "claude-haiku-3-5", RPM: 2000, ITPM: 200_000, OTPM: 40_000, Note: haiku35CacheNote, AsOf: limitsAsOf},
	{Tier: "tier4", ModelClass: "claude-haiku-3-5", RPM: 4000, ITPM: 400_000, OTPM: 80_000, Note: haiku35CacheNote, AsOf: limitsAsOf},

	{Tier: "tier1", ModelClass: "claude-opus-4.x", RPM: 50, ITPM: 500_000, OTPM: 80_000, AsOf: limitsAsOf},
	{Tier: "tier2", ModelClass: "claude-opus-4.x", RPM: 1000, ITPM: 2_000_000, OTPM: 200_000, AsOf: limitsAsOf},
	{Tier: "tier3", ModelClass: "claude-opus-4.x", RPM: 2000, ITPM: 5_000_000, OTPM: 400_000, AsOf: limitsAsOf},
	{Tier: "tier4", ModelClass: "claude-opus-4.x", RPM: 4000, ITPM: 10_000_000, OTPM: 800_000, AsOf: limitsAsOf},
}

// SubscriptionLimitChange records one published change to the SUBSCRIPTION
// (Pro/Max/Team/Enterprise seat) rate limits. Only the published RELATIVE facts are
// declared — the multiplier, the removed peak-hours reduction and the effective
// date; the absolute subscription token ceilings are NOT published by the authority
// and are therefore to-confirm, never invented.
type SubscriptionLimitChange struct {
	// EffectiveOn is the date the change took effect (ISO-8601).
	EffectiveOn string
	// FiveHourLimitMultiplier is the factor applied to the 5-hour rate limits.
	FiveHourLimitMultiplier float64
	// PeakHoursReductionRemoved is true when the change removed the peak-hours
	// limit reduction.
	PeakHoursReductionRemoved bool
	// Plans are the seat plans the change applies to.
	Plans []string
	// AsOf stamps when the record was recorded.
	AsOf string
}

// subscriptionLimitChanges is the published subscription limit-change log.
// Source: anthropic.com/news/higher-limits-spacex (2026-05-06): Claude Code 5-hour
// rate limits DOUBLED (x2) for Pro/Max/Team/Enterprise seat plans, and the
// peak-hours limit reduction was removed.
var subscriptionLimitChanges = []SubscriptionLimitChange{
	{
		EffectiveOn:               "2026-05-06",
		FiveHourLimitMultiplier:   2.0,
		PeakHoursReductionRemoved: true,
		Plans:                     []string{"pro", "max", "team", "enterprise_seat"},
		AsOf:                      limitsAsOf,
	},
}

// APIUsageTiers returns the published API spend-tier ladder in declared order (a
// copy, so a caller cannot mutate package state).
func APIUsageTiers() []APIUsageTier {
	return append([]APIUsageTier(nil), apiUsageTiers...)
}

// ModelClassRateLimits returns the published per-model-class default rate-limit
// table in declared order (a copy, so a caller cannot mutate package state).
func ModelClassRateLimits() []ModelClassRateLimit {
	return append([]ModelClassRateLimit(nil), modelClassRateLimits...)
}

// SubscriptionLimitChanges returns the published subscription limit-change log (a
// deep copy — the Plans slices are cloned — so a caller cannot mutate package state).
func SubscriptionLimitChanges() []SubscriptionLimitChange {
	out := make([]SubscriptionLimitChange, len(subscriptionLimitChanges))
	for i, c := range subscriptionLimitChanges {
		c.Plans = append([]string(nil), c.Plans...)
		out[i] = c
	}
	return out
}
