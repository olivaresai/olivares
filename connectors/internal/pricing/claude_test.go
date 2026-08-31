// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package pricing

import "testing"

func TestClaudePricingForKnownModel(t *testing.T) {
	p, ok := ClaudePricingFor("claude-sonnet-4-20250514")
	if !ok {
		t.Fatal("expected pricing for claude-sonnet-4-20250514")
	}
	if p.InputPerMTokUSD != 3.00 {
		t.Errorf("InputPerMTokUSD = %v, want 3.00", p.InputPerMTokUSD)
	}
	if p.OutputPerMTokUSD != 15.00 {
		t.Errorf("OutputPerMTokUSD = %v, want 15.00", p.OutputPerMTokUSD)
	}
}

func TestClaudePricingForUnknownModel(t *testing.T) {
	_, ok := ClaudePricingFor("unknown-model-v99")
	if ok {
		t.Fatal("expected no pricing for unknown model")
	}
}

func TestClaudeTableHasExpectedModels(t *testing.T) {
	want := []string{
		"claude-sonnet-4-20250514",
		"claude-opus-4-20250514",
		"claude-haiku-3-5-20241022",
	}
	for _, m := range want {
		if _, ok := Claude[m]; !ok {
			t.Errorf("missing expected model %q in Claude pricing table", m)
		}
	}
}
