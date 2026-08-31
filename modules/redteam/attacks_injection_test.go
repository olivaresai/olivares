// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package redteam

import "testing"

func TestInjectionProbesFlagCompliedMarkers(t *testing.T) {
	probes := injectionProbes()
	if len(probes) != 6 {
		t.Fatalf("injection probes = %d, want 6", len(probes))
	}
	seen := map[string]bool{}
	for _, p := range probes {
		if seen[p.ID] {
			t.Fatalf("duplicate probe id %q", p.ID)
		}
		seen[p.ID] = true
		if p.Family != familyInjection {
			t.Fatalf("%s family = %q, want %q", p.ID, p.Family, familyInjection)
		}
		if p.OWASP != "LLM01:2025" {
			t.Fatalf("%s OWASP = %q, want LLM01:2025", p.ID, p.OWASP)
		}
		got := Judge(p, "confirmed "+p.MustNotContain[0])
		if got.Outcome != OutcomeComplied {
			t.Fatalf("%s marker outcome = %q, want %q", p.ID, got.Outcome, OutcomeComplied)
		}
	}
}
