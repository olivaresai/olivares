// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package models

import (
	"testing"

	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

func TestMantleAndCRISModelID(t *testing.T) {
	if got := MantleModelID("claude-opus-4-8"); got != "anthropic.claude-opus-4-8" {
		t.Errorf("MantleModelID = %q", got)
	}
	// Idempotent on an already-prefixed id.
	if got := MantleModelID("anthropic.claude-opus-4-8"); got != "anthropic.claude-opus-4-8" {
		t.Errorf("MantleModelID idempotent = %q", got)
	}
	if got := CRISModelID("us", "claude-opus-4-8"); got != "us.anthropic.claude-opus-4-8" {
		t.Errorf("CRISModelID = %q", got)
	}
	// Tolerates a bare-Mantle input and re-bases it.
	if got := CRISModelID("eu", "anthropic.claude-sonnet-4-6"); got != "eu.anthropic.claude-sonnet-4-6" {
		t.Errorf("CRISModelID rebased = %q", got)
	}
	// Unknown geo => no guessed id.
	if got := CRISModelID("antarctica", "claude-opus-4-8"); got != "" {
		t.Errorf("CRISModelID unknown geo = %q, want empty", got)
	}
	// Re-regioning an already-CRIS id rebases it (must NOT double-prefix).
	if got := CRISModelID("eu", "us.anthropic.claude-opus-4-8"); got != "eu.anthropic.claude-opus-4-8" {
		t.Errorf("CRISModelID re-region = %q, want eu.anthropic.claude-opus-4-8 (no double prefix)", got)
	}
}

func TestSurfaceForBedrockID(t *testing.T) {
	cases := map[string]sdkmodel.Gateway{
		"anthropic.claude-opus-4-8":     sdkmodel.GatewayBedrockMantle,
		"us.anthropic.claude-opus-4-8":  sdkmodel.GatewayBedrockLegacy,
		"global.anthropic.claude-haiku": sdkmodel.GatewayBedrockLegacy,
		"claude-opus-4-8":               sdkmodel.GatewayDirect,
	}
	for id, want := range cases {
		if got := SurfaceForBedrockID(id); got != want {
			t.Errorf("SurfaceForBedrockID(%q) = %q, want %q", id, got, want)
		}
	}
}

// TestBurndownReferenceConfirmed locks the confirmed US 1.1x burndown on the modeled
// Opus/Sonnet families and that pre-inference_geo Haiku has none (honesty).
func TestBurndownReferenceConfirmed(t *testing.T) {
	opus, _ := lookupReference("claude-opus-4-8")
	if opus.USInferenceBurndownMult != USInferenceBurndownMult {
		t.Errorf("opus burndown = %v, want %v", opus.USInferenceBurndownMult, USInferenceBurndownMult)
	}
	haiku, _ := lookupReference("claude-haiku-4-5")
	if haiku.USInferenceBurndownMult != 0 {
		t.Errorf("haiku 4.5 predates inference_geo; burndown must be 0, got %v", haiku.USInferenceBurndownMult)
	}
}

// TestGlobalRegionalPremiumIsToConfirm guards that we never accidentally model the
// unverified Bedrock global/regional premium as a fact.
func TestGlobalRegionalPremiumIsToConfirm(t *testing.T) {
	if GlobalRegionalPremiumStatus != "to-confirm" {
		t.Errorf("global/regional premium must stay to-confirm, got %q", GlobalRegionalPremiumStatus)
	}
}
