// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package models

import (
	"strings"

	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

// This file models Claude-on-AWS-Bedrock inference profiles and the regional
// burndown reference (CLA-11). It is DECLARED reference data, AsOf-stamped, with an
// explicit honesty status per fact (ARCHITECTURE.md) — nothing here is
// fabricated; the unverified Bedrock numerics are marked to-confirm, not invented.
//
// HONESTY STATUS (verified jun-2026 against AWS / Anthropic primary docs):
//   - Inference-profile ID FORMAT — CONFIRMED. A cross-region (CRIS / legacy
//     InvokeModel) profile id is "<geo>.anthropic.<model>", geo ∈ {us, eu, apac,
//     global} (AWS geographic-cross-region-inference docs). The current Bedrock
//     _Mantle_ surface uses the BARE "anthropic.<model>" id (ANT2-01).
//     (A specific per-model CRIS id may not be individually listed on the AWS page;
//     the FORMAT is confirmed, a specific id is to-confirm per model.)
//   - US-only inference burndown 1.1× — CONFIRMED (Anthropic service-tiers;
//     inference_geo="us", Opus 4.6 / Sonnet 4.6+). Modeled on the reference table
//     (USInferenceBurndownMult) as a fact.
//   - Global-vs-Regional ±10% — TO-CONFIRM, and the spec's hypothesized DIRECTION is
//     likely INVERTED: AWS docs read that REGIONAL endpoints carry the ~10% premium
//     over GLOBAL (global is the cheaper baseline), not "global +10%". We therefore do
//     NOT model a multiplier — only record the documented direction as to-confirm.
const (
	// BedrockMantleIDPrefix is the bare id prefix of the current Bedrock Mantle surface.
	BedrockMantleIDPrefix = "anthropic."

	// GlobalRegionalPremiumStatus marks the Bedrock global/regional price delta as
	// unverified. Do not derive a multiplier from it; verify against AWS before use.
	GlobalRegionalPremiumStatus = "to-confirm"

	// GlobalRegionalPremiumNote records what the docs appear to say (direction only).
	GlobalRegionalPremiumNote = "AWS docs indicate REGIONAL inference profiles carry a ~10% premium over GLOBAL (global is the cheaper baseline) — opposite of the 'global +10%' hypothesis; exact multiplier unverified (to-confirm)."

	// USInferenceBurndownMult is the CONFIRMED US-only inference burndown (1.1×),
	// already applied per-family on the reference table; mirrored here as the named
	// constant for the Bedrock/deploy docs.
	USInferenceBurndownMult = 1.1
)

// bedrockGeoPrefixes are the AWS geographic-region prefixes of a cross-region
// inference profile id (verified format).
var bedrockGeoPrefixes = []string{"us", "eu", "apac", "global"}

// MantleModelID returns the Bedrock _Mantle_ (current surface) id for a bare Claude
// model id, e.g. "claude-opus-4-8" -> "anthropic.claude-opus-4-8". An id that already
// carries the prefix is returned unchanged.
func MantleModelID(claudeModelID string) string {
	id := strings.TrimSpace(claudeModelID)
	if id == "" || strings.HasPrefix(id, BedrockMantleIDPrefix) || hasBedrockGeoPrefix(id) {
		return id
	}
	return BedrockMantleIDPrefix + id
}

// CRISModelID returns the legacy cross-region inference-profile id for a Claude model
// in a geographic region, e.g. ("us","claude-opus-4-8") ->
// "us.anthropic.claude-opus-4-8". It rebases ANY input form (bare, Mantle, or an
// already-geo-prefixed CRIS id) to the requested geo — so re-regioning a profile id
// is correct, not double-prefixed. An unknown geo returns "" (never a guessed id).
func CRISModelID(geo, claudeModelID string) string {
	geo = strings.ToLower(strings.TrimSpace(geo))
	id := strings.TrimSpace(claudeModelID)
	if id == "" || !isBedrockGeo(geo) {
		return ""
	}
	return geo + "." + BedrockMantleIDPrefix + bareClaudeModelID(id)
}

// bareClaudeModelID strips any surface prefix (a "<geo>.anthropic." CRIS prefix or the
// bare "anthropic." Mantle prefix) to recover the underlying model id.
func bareClaudeModelID(id string) string {
	for _, g := range bedrockGeoPrefixes {
		if p := g + "." + BedrockMantleIDPrefix; strings.HasPrefix(id, p) {
			return strings.TrimPrefix(id, p)
		}
	}
	return strings.TrimPrefix(id, BedrockMantleIDPrefix)
}

// SurfaceForBedrockID classifies a Bedrock model/inference-profile id to its surface:
// a geo-prefixed CRIS id is bedrock-legacy (observe-only/deprecated), a bare
// anthropic.* id is the current bedrock-mantle. A non-Bedrock id returns direct.
func SurfaceForBedrockID(modelID string) sdkmodel.Gateway {
	if hasBedrockGeoPrefix(modelID) {
		return sdkmodel.GatewayBedrockLegacy
	}
	if strings.HasPrefix(modelID, BedrockMantleIDPrefix) {
		return sdkmodel.GatewayBedrockMantle
	}
	return sdkmodel.GatewayDirect
}

// hasBedrockGeoPrefix reports whether id starts with "<geo>.anthropic.".
func hasBedrockGeoPrefix(id string) bool {
	for _, g := range bedrockGeoPrefixes {
		if strings.HasPrefix(id, g+"."+BedrockMantleIDPrefix) {
			return true
		}
	}
	return false
}

// isBedrockGeo reports whether g is a recognized geographic-region prefix.
func isBedrockGeo(g string) bool {
	for _, p := range bedrockGeoPrefixes {
		if g == p {
			return true
		}
	}
	return false
}
