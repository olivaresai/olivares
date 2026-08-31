// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package knowledge

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestNIHControlledAccessPreset proves the shipped NIH/NSF policy pack operationalizes
// NIH NOT-OD-25-081: content labeled controlled-access is denied reaching a public
// generative-AI surface on BOTH deny-closed enforcement axes — the DLP egress gate and
// the classification clearance ladder. The DLP preset asserted here is the SAME file the
// docs pack ships, so the operator guide and the enforced policy cannot drift.
func TestNIHControlledAccessPreset(t *testing.T) {
	// (1) DLP egress gate: load the shipped preset and confirm it denies the
	// controlled-access class and unscanned content (deny-closed).
	presetPath := filepath.Join("..", "..", "docs", "edu-research", "presets", "nih-controlled-access.dlp.json")
	raw, err := os.ReadFile(presetPath)
	if err != nil {
		t.Fatalf("read NIH DLP preset %s: %v", presetPath, err)
	}
	var reqs []dlpRuleRequest
	if err := json.Unmarshal(raw, &reqs); err != nil {
		t.Fatalf("parse NIH DLP preset: %v", err)
	}
	if len(reqs) == 0 {
		t.Fatal("NIH DLP preset is empty")
	}
	rules := make(map[string]string, len(reqs))
	for _, r := range reqs {
		if r.Class == "" || r.Action == "" {
			t.Fatalf("preset rule missing class/action: %+v", r)
		}
		rules[r.Class] = r.Action
	}
	p := dlpPolicy{rules: rules}
	if !p.enabled() {
		t.Fatal("NIH preset must enable the DLP gate")
	}
	if denied := p.decide([]string{"controlled-access"}); len(denied) != 1 || denied[0] != "controlled-access" {
		t.Fatalf("NIH preset must DENY controlled-access egress; decide = %v", denied)
	}
	if !p.unscannedDenied() {
		t.Error("NIH preset must deny unscanned content (deny-closed, NOT-OD-25-081)")
	}

	// (2) Classification clearance ladder: a controlled-access dataset classified at a
	// restricted clearance is NOT visible to a public generative-AI surface.
	if classificationAllowed(classSecret, classPublic) {
		t.Error("a secret-classified (controlled-access) chunk must not be visible to a public surface")
	}
	if classificationAllowed("restricted", classPublic) {
		t.Error("a restricted-classified chunk must not be visible to a public surface")
	}
	// Monotonicity sanity: a lower-rank chunk IS visible to a higher clearance.
	if !classificationAllowed(classInternal, classSecret) {
		t.Error("classification ladder must admit lower-rank chunks to higher clearances")
	}
}
