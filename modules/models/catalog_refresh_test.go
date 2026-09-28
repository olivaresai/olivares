// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package models

import (
	"testing"

	mp "github.com/olivaresai/olivares/connectors/modelprovider"
)

func TestL4B2ClaudeReference(t *testing.T) {
	fable, ok := lookupReference("claude-fable-5-1")
	if !ok || fable.Family != "claude-fable" || fable.Pricing == nil ||
		fable.Pricing.InputPerMTokUSD != 10 || fable.Pricing.OutputPerMTokUSD != 50 ||
		fable.Pricing.CacheReadPerMTokUSD != 0.25 || fable.Pricing.AsOf != "2026-09-27" ||
		fable.ContextWindow != 1_000_000 || fable.MaxOutputTokens != 128_000 {
		t.Fatalf("claude-fable-5-1 reference = %+v ok %v price %+v", fable, ok, fable.Pricing)
	}
	old, ok := lookupReference("claude-fable-5")
	if !ok || old.Family != "claude-fable" || old.Pricing == nil || old.Pricing.CacheReadPerMTokUSD != 1 {
		t.Fatalf("claude-fable-5 reference = %+v ok %v", old, ok)
	}
	opus, ok := lookupReference("claude-opus-5-5")
	if !ok || opus.Family != "claude-opus-5" || opus.Pricing == nil ||
		opus.Pricing.InputPerMTokUSD != 4 || opus.Pricing.OutputPerMTokUSD != 20 ||
		opus.Pricing.CacheReadPerMTokUSD != 0.20 || opus.ContextWindow != 1_000_000 {
		t.Fatalf("claude-opus-5-5 reference = %+v ok %v price %+v", opus, ok, opus.Pricing)
	}
	prior, ok := lookupReference("claude-opus-5")
	if !ok || prior.Family != "claude-opus-5" || prior.Pricing == nil || prior.Pricing.InputPerMTokUSD != 5 {
		t.Fatalf("claude-opus-5 reference = %+v ok %v", prior, ok)
	}
	haiku, ok := lookupReference("claude-haiku-4-5-20251001")
	if !ok || haiku.Family != "claude-haiku" || haiku.Pricing == nil ||
		haiku.Pricing.InputPerMTokUSD != 1 || haiku.Pricing.AsOf != "2026-09-27" ||
		haiku.ContextWindow != 200_000 || haiku.MaxOutputTokens != 64_000 {
		t.Fatalf("dated haiku reference = %+v ok %v", haiku, ok)
	}
}

// TestL4B2FamilyForbidCoversCurrentIDs is the access-governance check: a saved
// family forbid on the label these IDs already had still covers the current ID.
func TestL4B2FamilyForbidCoversCurrentIDs(t *testing.T) {
	cases := []struct {
		family string
		id     string
	}{
		{"claude-fable", "claude-fable-5-1"},
		{"claude-opus-5", "claude-opus-5-5"},
		{"claude-haiku", "claude-haiku-4-5-20251001"},
	}
	for _, c := range cases {
		if !groupContains(modelGroupDef{families: []string{c.family}}, c.id) {
			t.Errorf("family %q does not cover %q", c.family, c.id)
		}
	}
	if groupContains(modelGroupDef{families: []string{"claude-sonnet"}}, "claude-opus-5-5") {
		t.Fatal("a sonnet family forbid covered claude-opus-5-5")
	}
}

func TestL4B2ClaudeResidency(t *testing.T) {
	for _, id := range []string{"claude-fable-5-1", "claude-opus-5-5"} {
		row, ok := lookupReference(id)
		if !ok {
			t.Fatalf("lookupReference(%s) missing", id)
		}
		if len(row.DataResidency) != len(claudeDataResidency) {
			t.Fatalf("%s residency = %v, want %v", id, row.DataResidency, claudeDataResidency)
		}
		for i := range claudeDataResidency {
			if row.DataResidency[i] != claudeDataResidency[i] {
				t.Fatalf("%s residency = %v, want %v", id, row.DataResidency, claudeDataResidency)
			}
		}
		if row.USInferenceBurndownMult != 1.1 {
			t.Fatalf("%s US multiplier = %v, want 1.1", id, row.USInferenceBurndownMult)
		}
		if len(row.ServiceTierEligibility) != 0 || row.RetentionClass != "" {
			t.Fatalf("%s service tiers or retention must stay unknown: %v %q", id, row.ServiceTierEligibility, row.RetentionClass)
		}
	}
}

func TestL4B2NonDatedNeighborIsUnknown(t *testing.T) {
	if _, ok := lookupReference("gpt-6-sol-pro"); ok {
		t.Fatal("gpt-6-sol-pro resolved to a GPT-6 row")
	}
	sol, ok := lookupReference("gpt-6-sol-20260927")
	if !ok || sol.Family != "gpt-6-sol" || sol.Pricing == nil || sol.Pricing.InputPerMTokUSD != 2 {
		t.Fatalf("gpt-6-sol-20260927 = %+v ok %v", sol, ok)
	}
	iso, ok := lookupReference("gpt-6-sol-2026-09-27")
	if !ok || iso.Family != "gpt-6-sol" {
		t.Fatalf("gpt-6-sol-2026-09-27 = %+v ok %v", iso, ok)
	}
	if _, ok := lookupReference("glm-5.3-pro"); ok {
		t.Fatal("glm-5.3-pro resolved to a GLM row")
	}
	dated, ok := lookupReference("glm-5.3-20260708")
	if !ok || dated.Prefix != "glm-5.3" || dated.Pricing == nil || dated.Pricing.InputPerMTokUSD != 1.40 {
		t.Fatalf("glm-5.3-20260708 = %+v ok %v", dated, ok)
	}
	if _, ok := lookupReference("claude-opus-5-5-pro"); ok {
		t.Fatal("claude-opus-5-5-pro resolved to a Claude row")
	}
	opus, ok := lookupReference("claude-opus-5-5-20260927")
	if !ok || opus.Family != "claude-opus-5" || opus.Pricing == nil || opus.Pricing.InputPerMTokUSD != 4 {
		t.Fatalf("claude-opus-5-5-20260927 = %+v ok %v", opus, ok)
	}
}

func TestL4B2OpenAIReference(t *testing.T) {
	astra, ok := lookupReference("gpt-6-astra")
	if !ok || astra.Family != "gpt-6-astra" || astra.Pricing == nil ||
		astra.Pricing.InputPerMTokUSD != 10 || astra.Pricing.OutputPerMTokUSD != 50 ||
		astra.Pricing.CacheReadPerMTokUSD != 1 || astra.Pricing.CacheWritePerMTokUSD != 12.5 ||
		astra.ContextWindow != 1_050_000 || astra.MaxOutputTokens != 128_000 {
		t.Fatalf("gpt-6-astra reference = %+v ok %v", astra, ok)
	}
	sol, ok := lookupReference("gpt-6-sol")
	if !ok || sol.Pricing == nil || sol.Pricing.InputPerMTokUSD != 2 || sol.Pricing.OutputPerMTokUSD != 10 ||
		sol.Pricing.CacheReadPerMTokUSD != 0.2 {
		t.Fatalf("gpt-6-sol reference = %+v ok %v", sol, ok)
	}
	luna, ok := lookupReference("gpt-6-luna")
	if !ok || luna.Pricing == nil || luna.Pricing.InputPerMTokUSD != 0.10 || luna.Pricing.OutputPerMTokUSD != 0.50 ||
		luna.Pricing.CacheReadPerMTokUSD != 0.01 {
		t.Fatalf("gpt-6-luna reference = %+v ok %v", luna, ok)
	}
	if _, ok := lookupReference("gpt-6-nope"); ok {
		t.Fatal("unknown gpt-6 id resolved to a family row")
	}
}

func TestL4B2XAIReference(t *testing.T) {
	g, ok := lookupReference("grok-4.7")
	if !ok || g.Family != "grok-4.7" || g.Pricing == nil ||
		g.Pricing.InputPerMTokUSD != 2 || g.Pricing.OutputPerMTokUSD != 6 ||
		g.Pricing.CacheReadPerMTokUSD != 0.50 || g.ContextWindow != 500_000 ||
		!mp.Has(g.Capabilities, mp.CapVision) {
		t.Fatalf("grok-4.7 reference = %+v ok %v", g, ok)
	}
	if _, ok := lookupReference("grok-4.7-fast"); ok {
		t.Fatal("grok-4.7-fast must not inherit grok-4.7")
	}
}

func TestL4B2GLMReference(t *testing.T) {
	flash, ok := lookupReference("glm-5.3-flash")
	if !ok || flash.Family != "glm-5.3-flash" || flash.Pricing == nil ||
		flash.Pricing.InputPerMTokUSD != 0.15 || flash.Pricing.OutputPerMTokUSD != 0.50 ||
		flash.Pricing.CacheReadPerMTokUSD != 0.03 || flash.Modality != "vision" ||
		!mp.Has(flash.Capabilities, mp.CapVision) {
		t.Fatalf("glm-5.3-flash reference = %+v ok %v", flash, ok)
	}
	text, ok := lookupReference("glm-5.3")
	if !ok || text.Pricing == nil || text.Pricing.InputPerMTokUSD != 1.40 || text.Modality != "text" ||
		mp.Has(text.Capabilities, mp.CapVision) || text.ContextWindow != 1_000_000 || text.MaxOutputTokens != 128_000 {
		t.Fatalf("glm-5.3 reference = %+v ok %v", text, ok)
	}
	older, ok := lookupReference("glm-5.2")
	if !ok || older.Family != "glm-5.2" || older.Pricing == nil || older.Pricing.InputPerMTokUSD != 1.40 ||
		older.Pricing.AsOf != "2026-08-27" {
		t.Fatalf("glm-5.2 reference changed: %+v ok %v", older, ok)
	}
}

func TestL4B2DeepSeekReference(t *testing.T) {
	flash, ok := lookupReference("deepseek-flash")
	if !ok || flash.Family != "deepseek-flash" || flash.Pricing != nil ||
		flash.ContextWindow != 1_000_000 || flash.MaxOutputTokens != 384_000 ||
		!mp.Has(flash.Capabilities, mp.CapVision) {
		t.Fatalf("deepseek-flash reference = %+v ok %v", flash, ok)
	}
	pro, ok := lookupReference("deepseek-v4-pro")
	if !ok || pro.Pricing != nil || mp.Has(pro.Capabilities, mp.CapVision) || pro.ContextWindow != 1_000_000 {
		t.Fatalf("deepseek-v4-pro reference = %+v ok %v", pro, ok)
	}
	if _, ok := lookupReference("deepseek-v4-flash-20260704"); ok {
		t.Fatal("a dated deepseek neighbor must stay unknown in the reference table")
	}
}

func TestL4B2KimiReference(t *testing.T) {
	k3, ok := lookupReference("kimi-k3")
	if !ok || k3.Family != "kimi-k3" || k3.Pricing == nil ||
		k3.Pricing.InputPerMTokUSD != 3 || k3.Pricing.OutputPerMTokUSD != 15 ||
		k3.Pricing.CacheWritePerMTokUSD != 3 || k3.Pricing.CacheWrite1hPerMTokUSD != 6 ||
		k3.Pricing.CacheReadPerMTokUSD != 0.30 || k3.Pricing.AsOf != "2026-09-27" ||
		k3.ContextWindow != 1_048_576 || k3.MaxOutputTokens != 1_048_576 ||
		mp.Has(k3.Capabilities, mp.CapToolUse) || mp.Has(k3.Capabilities, mp.CapVision) ||
		mp.Has(k3.Capabilities, mp.CapStructuredOutputs) || mp.Has(k3.Capabilities, mp.CapExtendedThinking) {
		t.Fatalf("kimi-k3 reference = %+v ok %v caps %v", k3, ok, k3.Capabilities)
	}
	code, ok := lookupReference("k3")
	if !ok || code.Pricing != nil || code.ContextWindow != 1_048_576 || code.Family != "k3" ||
		mp.Has(code.Capabilities, mp.CapExtendedThinking) {
		t.Fatalf("k3 reference = %+v ok %v", code, ok)
	}
	short, ok := lookupReference("k3-256k")
	if !ok || short.Family != "k3-256k" || short.Pricing != nil || short.ContextWindow != 262_144 ||
		mp.Has(short.Capabilities, mp.CapExtendedThinking) {
		t.Fatalf("k3-256k reference = %+v ok %v", short, ok)
	}
	coding, ok := lookupReference("kimi-for-coding")
	if !ok || coding.Family != "kimi-for-coding" || coding.Pricing != nil || coding.ContextWindow != 1_048_576 ||
		mp.Has(coding.Capabilities, mp.CapExtendedThinking) {
		t.Fatalf("kimi-for-coding reference = %+v ok %v", coding, ok)
	}
	fast, ok := lookupReference("kimi-for-coding-highspeed")
	if !ok || fast.Family != "kimi-for-coding-highspeed" || fast.Pricing != nil || fast.ContextWindow != 262_144 ||
		mp.Has(fast.Capabilities, mp.CapExtendedThinking) {
		t.Fatalf("kimi-for-coding-highspeed reference = %+v ok %v", fast, ok)
	}
	if _, ok := lookupReference("kimi-k3-extra"); ok {
		t.Fatal("unknown kimi id resolved to a family row")
	}
	if _, ok := lookupReference("kcode"); ok {
		t.Fatal("kcode is out of scope and must stay unknown")
	}
}

func TestL4B2SavedReferenceDefaults(t *testing.T) {
	opus, ok := lookupReference("claude-opus-4-8")
	if !ok || opus.Family != "claude-opus" || opus.Pricing == nil ||
		opus.Pricing.InputPerMTokUSD != 5 || opus.Pricing.OutputPerMTokUSD != 25 ||
		opus.Pricing.AsOf != referencePricingAsOf {
		t.Fatalf("claude-opus-4-8 reference changed: %+v ok %v", opus, ok)
	}
	sonnet, ok := lookupReference("claude-sonnet-5")
	if !ok || sonnet.Family != "claude-sonnet-5" || sonnet.Pricing == nil ||
		sonnet.Pricing.InputPerMTokUSD != 2 || sonnet.Pricing.OutputPerMTokUSD != 10 ||
		sonnet.Pricing.AsOf != "2026-08-27" {
		t.Fatalf("claude-sonnet-5 reference changed: %+v ok %v", sonnet, ok)
	}
	if _, ok := lookupReference("llama-3.1-70b"); ok {
		t.Fatal("unknown reference id resolved to a family row")
	}
}
