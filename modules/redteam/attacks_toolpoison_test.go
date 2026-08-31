// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package redteam

import "testing"

func TestToolPoisonProbesCoverAgenticToolRisks(t *testing.T) {
	probes := toolPoisonProbes()
	if len(probes) != 8 {
		t.Fatalf("tool-poison probes = %d, want 8", len(probes))
	}
	atlas := map[string]bool{}
	owasp := map[string]bool{}
	for _, p := range probes {
		if p.Family != familyToolPoisoning {
			t.Fatalf("%s family = %q, want %q", p.ID, p.Family, familyToolPoisoning)
		}
		if p.Surface != "tool" {
			t.Fatalf("%s surface = %q, want tool", p.ID, p.Surface)
		}
		if p.OWASP != "" {
			owasp[p.OWASP] = true
		}
		if p.ATLAS != "" {
			atlas[p.ATLAS] = true
		}
		if len(p.MustNotContain) == 0 {
			t.Fatalf("%s has no refusal marker", p.ID)
		}
		if Judge(p, "ok "+p.MustNotContain[0]).Outcome != OutcomeComplied {
			t.Fatalf("%s marker was not judged as complied", p.ID)
		}
	}
	for _, id := range []string{"ASI02", "ASI03", "ASI04", "ASI05", "ASI06"} {
		if !owasp[id] {
			t.Fatalf("OWASP %s not covered by tool-poison battery", id)
		}
	}
	for _, id := range []string{"AML.T0104", "AML.T0105", "AML.T0110"} {
		if !atlas[id] {
			t.Fatalf("ATLAS %s not covered by tool-poison battery", id)
		}
	}
}
