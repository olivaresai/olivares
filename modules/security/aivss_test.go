// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package security

import (
	"math"
	"testing"
)

func TestAIVSSWorkedExamples(t *testing.T) {
	for _, ref := range aivssReferenceScores {
		t.Run(ref.slug, func(t *testing.T) {
			gotAARS := aivssAARS(ref.cvssBase, ref.factorSum, aivssThreatMultiplierProofOfConcept)
			if math.Abs(gotAARS-ref.aars) > 1e-9 {
				t.Fatalf("AARS = %.12f, want %.12f", gotAARS, ref.aars)
			}

			_, got := aivssScore(ref.cvssBase, ref.factorSum, aivssThreatMultiplierProofOfConcept, aivssMitigationNoWeak)
			if got != ref.aivss {
				t.Fatalf("AIVSS = %.1f, want %.1f", got, ref.aivss)
			}
			if sev := aivssSeverity(got); sev != ref.severity {
				t.Fatalf("severity = %q, want %q", sev, ref.severity)
			}
		})
	}
}

func TestAIVSSRoundHalfUp1(t *testing.T) {
	cases := []struct {
		in   float64
		want float64
	}{
		{in: 8.64, want: 8.6},
		{in: 8.65, want: 8.7},
		{in: 9.9529, want: 10.0},
		{in: 7.08095, want: 7.1},
		{in: 9.15525, want: 9.2},
	}
	for _, c := range cases {
		if got := aivssRoundHalfUp1(c.in); got != c.want {
			t.Fatalf("aivssRoundHalfUp1(%v) = %.1f, want %.1f", c.in, got, c.want)
		}
	}
}

func TestAIVSSSeverityBandEdges(t *testing.T) {
	cases := []struct {
		score float64
		want  string
	}{
		{score: 8.9, want: "High"},
		{score: 9.0, want: "Critical"},
		{score: 6.9, want: "Medium"},
		{score: 7.0, want: "High"},
		{score: 3.9, want: "Low"},
		{score: 4.0, want: "Medium"},
		{score: 0.0, want: "none"},
	}
	for _, c := range cases {
		if got := aivssSeverity(c.score); got != c.want {
			t.Fatalf("aivssSeverity(%.1f) = %q, want %q", c.score, got, c.want)
		}
	}
}

func TestAIVSSFactorValidation(t *testing.T) {
	invalid := aivssFactors{executionAutonomy: 0.3}
	if err := invalid.validate(); err == nil {
		t.Fatal("0.3 factor was accepted")
	}

	valid := aivssFactors{
		executionAutonomy:          0,
		externalToolControlSurface: 0.5,
		naturalLanguageInterface:   1.0,
		contextualAwareness:        0,
		behavioralNonDeterminism:   0.5,
		opacityReflexivity:         1.0,
		persistentStateRetention:   0,
		dynamicIdentity:            0.5,
		multiAgentInteractions:     1.0,
		selfModification:           0,
	}
	if err := valid.validate(); err != nil {
		t.Fatalf("valid factors rejected: %v", err)
	}

	allOnes := aivssFactors{
		executionAutonomy:          1.0,
		externalToolControlSurface: 1.0,
		naturalLanguageInterface:   1.0,
		contextualAwareness:        1.0,
		behavioralNonDeterminism:   1.0,
		opacityReflexivity:         1.0,
		persistentStateRetention:   1.0,
		dynamicIdentity:            1.0,
		multiAgentInteractions:     1.0,
		selfModification:           1.0,
	}
	if got := allOnes.sum(); got != 10 {
		t.Fatalf("all-1.0 factor sum = %.1f, want 10", got)
	}
}

func TestAIVSSMappingCompleteness(t *testing.T) {
	if len(asiToAIVSSCoreRisk) != 10 {
		t.Fatalf("ASI mapping rows = %d, want 10", len(asiToAIVSSCoreRisk))
	}

	refs := aivssReferencesBySlug()
	for i := 1; i <= 10; i++ {
		id := "ASI0" + string(rune('0'+i))
		if i == 10 {
			id = "ASI10"
		}
		slug, ok := asiToAIVSSCoreRisk[id]
		if !ok {
			t.Fatalf("ASI mapping missing %s", id)
		}
		if _, ok := refs[slug]; !ok {
			t.Fatalf("ASI mapping %s references unknown slug %q", id, slug)
		}
	}

	if len(aivssReferenceScores) != 10 {
		t.Fatalf("reference rows = %d, want 10", len(aivssReferenceScores))
	}
	seen := map[string]bool{}
	for _, ref := range aivssReferenceScores {
		if seen[ref.slug] {
			t.Fatalf("duplicate reference slug %q", ref.slug)
		}
		seen[ref.slug] = true
	}
}

func TestAIVSSReferenceInternalConsistency(t *testing.T) {
	for _, ref := range aivssReferenceScores {
		t.Run(ref.slug, func(t *testing.T) {
			raw, rounded := aivssScore(ref.cvssBase, ref.factorSum, aivssThreatMultiplierProofOfConcept, aivssMitigationNoWeak)
			if math.Abs((raw-ref.cvssBase)-ref.aars) > 1e-9 {
				t.Fatalf("computed AARS = %.12f, want %.12f", raw-ref.cvssBase, ref.aars)
			}
			if rounded != ref.aivss {
				t.Fatalf("rounded AIVSS = %.1f, want %.1f", rounded, ref.aivss)
			}
			if sev := aivssSeverity(rounded); sev != ref.severity {
				t.Fatalf("severity = %q, want %q", sev, ref.severity)
			}
		})
	}
}

func TestAIVSSMetaSingleASI(t *testing.T) {
	d := Detection{OWASPASI: []string{"ASI02"}}
	meta := aivssMeta(d, map[string]any{"rule": "asi02-rm-rf"})

	ref, ok := meta[aivssMetadataKey].(map[string]any)
	if !ok {
		t.Fatalf("missing %s metadata", aivssMetadataKey)
	}
	if experimental, _ := ref["experimental"].(bool); !experimental {
		t.Fatal("experimental = false, want true")
	}
	scores := ref["scores"].([]map[string]any)
	if len(scores) != 1 {
		t.Fatalf("scores = %d, want 1", len(scores))
	}
	if got := scores[0]["core_risk"]; got != "tool_misuse" {
		t.Fatalf("core_risk = %v, want tool_misuse", got)
	}
	if got := scores[0]["aivss"]; got != 9.9 {
		t.Fatalf("aivss = %v, want 9.9", got)
	}
	if got := scores[0]["severity"]; got != "Critical" {
		t.Fatalf("severity = %v, want Critical", got)
	}
}

func TestAIVSSMetaMultipleASIOrder(t *testing.T) {
	d := Detection{OWASPASI: []string{"ASI03", "ASI10"}}
	meta := aivssMeta(d, map[string]any{})
	ref := meta[aivssMetadataKey].(map[string]any)
	scores := ref["scores"].([]map[string]any)
	if len(scores) != 2 {
		t.Fatalf("scores = %d, want 2", len(scores))
	}
	for i, wantASI := range []string{"ASI03", "ASI10"} {
		if got := scores[i]["asi"]; got != wantASI {
			t.Fatalf("scores[%d].asi = %v, want %s", i, got, wantASI)
		}
		if got := scores[i]["core_risk"]; got != "access_control_violation" {
			t.Fatalf("scores[%d].core_risk = %v, want access_control_violation", i, got)
		}
		if got := scores[i]["aivss"]; got != 9.7 {
			t.Fatalf("scores[%d].aivss = %v, want 9.7", i, got)
		}
	}
}

func TestAIVSSMetaEmptyASILeavesBaseUnchanged(t *testing.T) {
	base := map[string]any{"rule": "llm01"}
	got := aivssMeta(Detection{OWASPLLM: []string{"LLM01:2025"}}, base)
	if _, ok := got[aivssMetadataKey]; ok {
		t.Fatalf("empty ASI axis emitted %s", aivssMetadataKey)
	}
	if got["rule"] != "llm01" || len(got) != 1 {
		t.Fatalf("base changed: %v", got)
	}
	got["after"] = true
	if _, ok := base["after"]; !ok {
		t.Fatal("aivssMeta did not return the base map")
	}
}

func TestAIVSSMetaDoesNotChangeDetail(t *testing.T) {
	dets := scan(classAgentic, "sudo rm -rf /", agenticShapes)
	if len(dets) == 0 {
		t.Fatal("expected agentic detector to fire")
	}
	d := dets[0]
	before := d.detail()
	base := map[string]any{"rule": d.Rule}
	meta := aivssMeta(d, base)
	after := d.detail()
	if after != before {
		t.Fatalf("detail changed after aivssMeta: before %q, after %q", before, after)
	}
	if _, ok := base[aivssMetadataKey]; !ok {
		t.Fatalf("base map missing %s after aivssMeta", aivssMetadataKey)
	}
	meta["probe"] = true
	if _, ok := base["probe"]; !ok {
		t.Fatal("aivssMeta did not return the same base map")
	}
	if len(meta) != 3 {
		t.Fatalf("meta keys = %d, want 3 (rule, %s, probe)", len(meta), aivssMetadataKey)
	}
}
