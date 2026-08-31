// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"testing"

	"github.com/olivaresai/olivares/modules/security"
)

// classifierSeededText seeds a few real shapes (one rule twice, to carry the
// occurrence count across the seam) so the adapter mapping is exercised on a
// non-trivial result, not an empty one.
const classifierSeededText = `Reach the auditor at a@b.example or c@d.example today.
The SSN on file is 123-45-6789 per intake.
A deploy credential AKIAIOSFODNN7EXAMPLE leaked in the log.`

// TestKnowledgeClassifierMapsCatalogOneToOne is the composition-root proof:
// the securitySensitivityClassifier adapter (the only place that speaks both
// modules' types) returns EXACTLY what security.ClassifySensitivity returns —
// same classes, rules, counts, severities, same order — with no error. The seam
// adds nothing and loses nothing; the detectors stay single-owner in the security
// module.
func TestKnowledgeClassifierMapsCatalogOneToOne(t *testing.T) {
	adapter := securitySensitivityClassifier{}

	got, err := adapter.Classify(classifierSeededText)
	if err != nil {
		t.Fatalf("Classify: %v (the deterministic catalog never errors)", err)
	}
	want := security.ClassifySensitivity(classifierSeededText)
	if len(want) == 0 {
		t.Fatal("security.ClassifySensitivity found nothing in the seeded text — the mapping check would be vacuous")
	}
	if len(got) != len(want) {
		t.Fatalf("adapter returned %d hits, catalog returned %d:\n  adapter = %+v\n  catalog = %+v", len(got), len(want), got, want)
	}
	for i := range want {
		if got[i].Class != want[i].Class || got[i].Rule != want[i].Rule ||
			got[i].Count != want[i].Count || got[i].Severity != want[i].Severity {
			t.Errorf("hit %d: adapter = %+v, want catalog hit %+v (1:1 field mapping)", i, got[i], want[i])
		}
	}
	// The occurrence count crosses the seam (email seeded twice above).
	var emailCount int
	for _, h := range got {
		if h.Rule == "email" {
			emailCount = h.Count
		}
	}
	if emailCount != 2 {
		t.Errorf("email Count = %d through the adapter, want 2 (counts must survive the mapping)", emailCount)
	}

	// An empty text classifies to no hits, still without an error.
	empty, err := adapter.Classify("")
	if err != nil {
		t.Fatalf("Classify(\"\"): %v", err)
	}
	if len(empty) != 0 {
		t.Errorf("Classify(\"\") returned %d hits, want 0", len(empty))
	}
}

// TestKnowledgeClassifierVersionMatchesCatalog: the adapter reports the security
// module's catalog version verbatim — the version recorded on scan evidence is the
// detector owner's, so a catalog bump invalidates stale labels everywhere at once.
func TestKnowledgeClassifierVersionMatchesCatalog(t *testing.T) {
	v := securitySensitivityClassifier{}.Version()
	if v == "" {
		t.Fatal("adapter Version() is empty — scan evidence would be irreproducible")
	}
	if v != security.SensitivityCatalogVersion {
		t.Errorf("adapter Version() = %q, want security.SensitivityCatalogVersion %q", v, security.SensitivityCatalogVersion)
	}
}

// TestRedactionPlaceholderStillMatchesCatalog pins the HAZARD premise behind the
// knowledge module's marker neutralization: the catalog's key=value secret rule
// re-matches the redaction placeholder itself ("api_key=[REDACTED]" reads as a
// secret value), so classifying stored text WITHOUT neutralizing markers would
// re-label already-minimized content secret.credential and deny its egress for
// no risk. If this test ever fails, the catalog stopped matching placeholders
// and knowledge's neutralizeMarkers rationale must be re-reviewed.
func TestRedactionPlaceholderStillMatchesCatalog(t *testing.T) {
	hits := security.ClassifySensitivity("Config api_key=[REDACTED] for the deploy.")
	found := false
	for _, h := range hits {
		if h.Class == security.SensSecretCredential && h.Rule == "key-value-secret" {
			found = true
		}
	}
	if !found {
		t.Fatal("the catalog no longer flags \"api_key=[REDACTED]\" as key-value-secret — re-review knowledge.neutralizeMarkers (it may be unnecessary or need a different sentinel)")
	}
}
