// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package modelprovider

import (
	"math"
	"time"

	"github.com/olivaresai/olivares/sdk/model"
)

// PricingSource records where a price came from, so module XI can show whether a
// cost figure is a billed amount, a published list price, or an operator estimate
// — the product never fakes certainty (ARCHITECTURE.md).
type PricingSource string

const (
	// PricingList is the provider's published list price (declared, maintainable).
	PricingList PricingSource = "list"
	// PricingAPI is a monetary amount the provider's own cost API returned.
	PricingAPI PricingSource = "api"
	// PricingOperator is an operator-declared override (e.g. a negotiated rate, or
	// a $/MTok rate assigned to local inference).
	PricingOperator PricingSource = "operator"
)

// ModelPricing is the declared list price for one model, expressed in USD per
// million tokens — the unit providers publish, so the table reads like a pricing
// page and stays maintainable. It is NOT fabricated telemetry: it is a declared
// price stamped with the date it was declared (AsOf) and its provenance (Source).
// It is used to DERIVE cost when a usage API returns token counts only; when a
// provider's cost API returns a monetary amount, that authoritative figure is used
// instead (ToCostSampleWithCost) and pricing is bypassed.
//
// Operators override the declared table (negotiated rates, local-inference
// $/MTok) and should verify list prices against each provider's pricing page; the
// declared values are a maintainable default, not a guarantee.
type ModelPricing struct {
	// InputPerMTokUSD is the price per million standard (uncached) input tokens.
	InputPerMTokUSD float64
	// OutputPerMTokUSD is the price per million output tokens.
	OutputPerMTokUSD float64
	// CacheWritePerMTokUSD is the price per million cache-write input tokens at the
	// SHORT-TTL (5-minute) rate — the standard cache-write tier (~1.25× base input).
	// It is 0 when the model has no cache-write tier. It is also the rate applied to
	// untiered cache-write tokens (Usage.CacheWriteTokens) from providers that do not
	// split cache writes by TTL.
	CacheWritePerMTokUSD float64
	// CacheWrite1hPerMTokUSD is the price per million 1-hour-TTL cache-write tokens
	// (~2.0× base input — distinct from, and higher than, the 5m rate). 0 means "use
	// CacheWritePerMTokUSD" (the conservative fallback when a 1h rate is undeclared),
	// not "free": pricing a 1h write at 0 would silently drop the dimension.
	CacheWrite1hPerMTokUSD float64
	// CacheReadPerMTokUSD is the price per million cache-read input tokens
	// (0 when the model has no cache-read tier).
	CacheReadPerMTokUSD float64
	// Currency is the ISO-4217 code; "USD" for v1 (CostSample is micro-USD).
	Currency string
	// AsOf is the date the price was declared, YYYY-MM-DD.
	AsOf string
	// Source is the price provenance.
	Source PricingSource
}

// Usage is one normalized token-usage bucket a connector extracted from a
// provider's usage report (or a local-inference probe). It is minimal-data: token
// COUNTS and references only — never prompt or completion content (docs/SECURITY-HARDENING.md).
type Usage struct {
	// ProviderRef and ModelRef are the natural references (see the Provider*
	// constants). ProviderRef is the cost stream's provenance discriminator.
	ProviderRef string
	// ModelRef names the model the tokens were spent on.
	ModelRef string
	// SessionRef optionally ties the usage to an agent session (empty if unknown).
	SessionRef string
	// InputTokens is the standard (uncached) input token count.
	InputTokens int64
	// OutputTokens is the output token count.
	OutputTokens int64
	// CacheWriteTokens is the UNTIERED cache-creation input token count, for providers
	// that do not split cache writes by TTL (priced at CacheWritePerMTokUSD). Anthropic
	// splits by TTL — use CacheCreation1hTokens/CacheCreation5mTokens instead and leave
	// this 0 to avoid double counting.
	CacheWriteTokens int64
	// CacheCreation1hTokens / CacheCreation5mTokens are cache-write tokens by TTL,
	// priced distinctly (1h ~2.0× base, 5m ~1.25× base). 0 when not reported.
	CacheCreation1hTokens int64
	CacheCreation5mTokens int64
	// CacheReadTokens is the cache-hit input token count (0 if none).
	CacheReadTokens int64
	// OccurredAt is when the usage happened (the bucket time, UTC).
	OccurredAt time.Time

	// Attribution + surface dimensions (provider-neutral; carried verbatim onto the
	// CostSample). Empty/zero = not reported.
	WorkspaceRef  string
	APIKeyRef     string
	Actor         string
	ServiceTier   string
	ContextWindow string
	InferenceGeo  string
	Gateway       model.Gateway
	Provenance    model.CostProvenance
	CostType      string
}

// TotalInputTokens is the full input volume: standard (uncached) input plus every
// cache tier (untiered write, 1h write, 5m write, read). It is what the CostSample's
// InputTokens field receives; the cache breakdown is carried additionally in the
// CostSample cache fields, and the per-tier price differences are preserved in the
// derived CostMicroUSD.
func (u Usage) TotalInputTokens() int64 {
	return u.InputTokens + u.CacheWriteTokens + u.CacheCreation1hTokens + u.CacheCreation5mTokens + u.CacheReadTokens
}

// cacheWrite1hRate returns the 1-hour cache-write rate, falling back to the 5m rate
// when no distinct 1h rate is declared (conservative — never prices a 1h write at 0).
func (p ModelPricing) cacheWrite1hRate() float64 {
	if p.CacheWrite1hPerMTokUSD > 0 {
		return p.CacheWrite1hPerMTokUSD
	}
	return p.CacheWritePerMTokUSD
}

// DeriveCostMicroUSD returns the derived cost of u under p, in integer micro-USD.
//
// Arithmetic: a price of $P per million tokens is exactly P micro-USD per token
// ($P / 1e6 tokens = P·1e6 micro-USD / 1e6 tokens = P micro-USD/token), so the
// per-tier cost is (token count) × (per-MTok USD price) with no scaling, summed
// across tiers and rounded to the nearest micro-USD. Money never uses float in the
// wire type (CostSample is int64 micro-USD); the float here is a derivation
// intermediate, rounded once.
func (p ModelPricing) DeriveCostMicroUSD(u Usage) int64 {
	cost := float64(u.InputTokens)*p.InputPerMTokUSD +
		float64(u.OutputTokens)*p.OutputPerMTokUSD +
		float64(u.CacheWriteTokens)*p.CacheWritePerMTokUSD +
		float64(u.CacheCreation5mTokens)*p.CacheWritePerMTokUSD +
		float64(u.CacheCreation1hTokens)*p.cacheWrite1hRate() +
		float64(u.CacheReadTokens)*p.CacheReadPerMTokUSD
	if cost < 0 {
		return 0
	}
	return int64(math.Round(cost))
}

// ToCostSample builds the SDK CostSample for u, deriving the monetary amount from
// p. Token counts and refs are carried verbatim; InputTokens receives the total
// input volume (u.TotalInputTokens). Use this when the provider's usage API
// returns token counts but no money.
func ToCostSample(u Usage, p ModelPricing) model.CostSample {
	return costSample(u, p.DeriveCostMicroUSD(u))
}

// ToCostSampleWithCost builds the CostSample using an authoritative cost the
// provider's API already reported (costMicroUSD), bypassing derivation. Use this
// when the API returns a billed monetary amount (Anthropic cost_report, OpenAI
// costs) so the product shows the real figure rather than an estimate. A negative
// costMicroUSD is treated as "unknown" and clamped to 0.
func ToCostSampleWithCost(u Usage, costMicroUSD int64) model.CostSample {
	if costMicroUSD < 0 {
		costMicroUSD = 0
	}
	return costSample(u, costMicroUSD)
}

// costSample assembles the CostSample with a settled cost. InputTokens receives the
// full input volume (Usage.TotalInputTokens) — its v1 meaning — and the cache tiers
// are ALSO carried as the additive cache breakdown (a subset of InputTokens, not
// extra), so module XI can measure cache savings while existing InputTokens math is
// unchanged. The per-tier price differences are already settled in costMicroUSD.
func costSample(u Usage, costMicroUSD int64) model.CostSample {
	return model.CostSample{
		ProviderRef:           u.ProviderRef,
		ModelRef:              u.ModelRef,
		SessionRef:            u.SessionRef,
		InputTokens:           u.TotalInputTokens(),
		OutputTokens:          u.OutputTokens,
		CostMicroUSD:          costMicroUSD,
		OccurredAt:            u.OccurredAt,
		CacheReadTokens:       u.CacheReadTokens,
		CacheCreation1hTokens: u.CacheCreation1hTokens,
		CacheCreation5mTokens: u.CacheCreation5mTokens,
		WorkspaceRef:          u.WorkspaceRef,
		APIKeyRef:             u.APIKeyRef,
		Actor:                 u.Actor,
		ServiceTier:           u.ServiceTier,
		ContextWindow:         u.ContextWindow,
		InferenceGeo:          u.InferenceGeo,
		Gateway:               u.Gateway,
		Provenance:            u.Provenance,
		CostType:              u.CostType,
	}
}
