// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package redteam

import (
	"strings"
	"testing"
)

// TestATLASCoverageDated verifies: the battery exercises the verified 2026
// agent techniques, the coverage view is stamped with the verified matrix version,
// every covered id resolves to a verified title, and — the binding honesty check —
// the view never asserts a monthly update cadence.
func TestATLASCoverageDated(t *testing.T) {
	covered := map[string]int{}
	for _, p := range battery("all") {
		if p.ATLAS != "" {
			covered[p.ATLAS]++
		}
	}

	// The new 2026 agent techniques are genuinely exercised by a probe.
	for _, id := range []string{"AML.T0104", "AML.T0105", "AML.T0110"} {
		if covered[id] == 0 {
			t.Fatalf("battery does not exercise verified 2026 technique %s", id)
		}
	}
	// AML.T0057 (already mapped pre) must not be lost.
	if covered["AML.T0057"] == 0 {
		t.Fatal("AML.T0057 (LLM Data Leakage) coverage regressed")
	}

	view := atlasCoverage(covered)
	if view.Version != "2026.05" || view.AsOf != "2026-05-27" || view.DataFormat != "v6.0.0" {
		t.Fatalf("ATLAS version stamp wrong: %+v", view)
	}
	// Every covered id must resolve to a verified title — no un-reconciled id rides along.
	for _, tch := range view.Techniques {
		if tch.Title == "" {
			t.Fatalf("ATLAS id %s in coverage has no verified title (un-reconciled / possibly invented)", tch.ID)
		}
	}
	// HONESTY: the view must NOT claim a monthly cadence.
	if strings.Contains(strings.ToLower(view.Note), "monthly") {
		t.Fatalf("coverage note must not assert a monthly cadence: %q", view.Note)
	}
}
