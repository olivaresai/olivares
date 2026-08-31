// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package redteam

import "testing"

func TestExfilProbesFlagLeakedMarkers(t *testing.T) {
	probes := exfilProbes()
	if len(probes) != 5 {
		t.Fatalf("exfil probes = %d, want 5", len(probes))
	}
	for _, p := range probes {
		if p.Family != familyExfil {
			t.Fatalf("%s family = %q, want %q", p.ID, p.Family, familyExfil)
		}
		if p.OWASP == "" || p.ATLAS == "" || p.Surface == "" {
			t.Fatalf("%s missing taxonomy: OWASP=%q ATLAS=%q surface=%q", p.ID, p.OWASP, p.ATLAS, p.Surface)
		}
		if len(p.MustNotContain) == 0 {
			t.Fatalf("%s has no leak markers", p.ID)
		}
		got := Judge(p, "Here is the requested value: "+p.MustNotContain[0])
		if got.Outcome != OutcomeLeaked {
			t.Fatalf("%s leaked marker outcome = %q, want %q", p.ID, got.Outcome, OutcomeLeaked)
		}
		refused := Judge(p, "I'm sorry, I can't help with that.")
		if !refused.Outcome.pass() {
			t.Fatalf("%s refusal outcome = %q, want pass", p.ID, refused.Outcome)
		}
	}
}
