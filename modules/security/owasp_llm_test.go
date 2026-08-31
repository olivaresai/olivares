// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package security

import "testing"

// TestLLMTop10CatalogComplete asserts the crosswalk covers LLM01..LLM10 exactly
// once, with the verified verbatim 2025 titles. A wrong id/title is the kind of detail
// a security reviewer reads as amateur, so it is guarded here (mirrors the MCP test).
func TestLLMTop10CatalogComplete(t *testing.T) {
	want := map[string]string{
		"LLM01:2025": "Prompt Injection",
		"LLM02:2025": "Sensitive Information Disclosure",
		"LLM03:2025": "Supply Chain",
		"LLM04:2025": "Data and Model Poisoning",
		"LLM05:2025": "Improper Output Handling",
		"LLM06:2025": "Excessive Agency",
		"LLM07:2025": "System Prompt Leakage",
		"LLM08:2025": "Vector and Embedding Weaknesses",
		"LLM09:2025": "Misinformation",
		"LLM10:2025": "Unbounded Consumption",
	}
	got := LLMTop10()
	if len(got) != 10 {
		t.Fatalf("expected 10 LLM controls, got %d", len(got))
	}
	seen := map[string]bool{}
	for _, c := range got {
		wantTitle, ok := want[c.ID]
		if !ok {
			t.Errorf("unexpected control id %q", c.ID)
			continue
		}
		if c.Title != wantTitle {
			t.Errorf("%s title = %q, want %q", c.ID, c.Title, wantTitle)
		}
		if c.ProductControl == "" || len(c.Evidence) == 0 {
			t.Errorf("%s must map to a product control with evidence: %+v", c.ID, c)
		}
		seen[c.ID] = true
	}
	for id := range want {
		if !seen[id] {
			t.Errorf("missing control %q", id)
		}
	}
}

// TestLLMTop10DetectorTags guards the DELIBERATE decisions in the crosswalk: which
// risks a guardrail detector tags directly (LLM01/02/05/07) versus which are evidenced
// by another module or — for LLM09 Misinformation — left intentionally untagged
//. If a future edit accidentally flips LLM09 to detector-tagged, or breaks
// the LLM06/08/10 crosswalk grading, this test fails loudly rather than silently.
func TestLLMTop10DetectorTags(t *testing.T) {
	wantTagged := map[string]bool{
		"LLM01:2025": true, "LLM02:2025": true, "LLM05:2025": true, "LLM07:2025": true,
		"LLM03:2025": false, "LLM04:2025": false, "LLM06:2025": false,
		"LLM08:2025": false, "LLM09:2025": false, "LLM10:2025": false,
	}
	for _, c := range LLMTop10() {
		if c.DetectorTagged != wantTagged[c.ID] {
			t.Errorf("%s DetectorTagged = %v, want %v", c.ID, c.DetectorTagged, wantTagged[c.ID])
		}
		// LLM09 is the binding refutation: it must NEVER be detector-tagged (the content
		// filter is a different concern; contentfilter.go owasp:'').
		if c.ID == "LLM09:2025" && c.DetectorTagged {
			t.Fatal("LLM09 Misinformation must stay deliberately untagged")
		}
	}
}

// TestLLMTop10Immutable confirms the accessor returns a defensive copy.
func TestLLMTop10Immutable(t *testing.T) {
	a := LLMTop10()
	a[0].Title = "MUTATED"
	if LLMTop10()[0].Title == "MUTATED" {
		t.Error("LLMTop10() must return a defensive copy")
	}
}
