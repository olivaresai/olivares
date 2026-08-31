// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance

import "testing"

// TestEmergingStandardsRegistryWellFormed checks the IDN-12 design-toward registry
// is complete and honest: every entry names a seam + caveat + verified revision,
// and the registry reflects the CORRECTED revisions (not stale numbers).
func TestEmergingStandardsRegistryWellFormed(t *testing.T) {
	std := EmergingStandards()
	if len(std) < 6 {
		t.Fatalf("registry has %d standards, want >= 6", len(std))
	}
	byKey := map[string]EmergingStandard{}
	for _, s := range std {
		if s.Key == "" || s.Name == "" || s.Spec == "" || s.Revision == "" || s.Seam == "" || s.Caveat == "" || s.Authority == "" {
			t.Errorf("standard %q has an empty required field: %+v", s.Key, s)
		}
		if s.VerifiedAt != emergingVerifiedAt {
			t.Errorf("standard %q VerifiedAt = %q, want %q", s.Key, s.VerifiedAt, emergingVerifiedAt)
		}
		if s.Status != StatusDraft && s.Status != StatusStable {
			t.Errorf("standard %q has unknown status %q", s.Key, s.Status)
		}
		byKey[s.Key] = s
	}

	// Verified corrections to must be reflected.
	if got := byKey["oauth_identity_chaining"].Revision; got != "12" {
		t.Errorf("identity-chaining revision = %q, want 12 (corrects -08)", got)
	}
	if got := byKey["a2a_signed_agent_cards"].Status; got != StatusStable {
		t.Errorf("A2A status = %q, want stable (v1.0 GA, corrects RC)", got)
	}
	if _, ok := byKey["oauth_transaction_tokens"]; !ok {
		t.Error("transaction tokens (OAuth WG) must be tracked")
	}
	if _, ok := byKey["scim_device_model"]; !ok {
		t.Error("SCIM device model must be tracked as draft (the IDN-11 seam)")
	}
}

// TestEmergingStandardsReturnsCopy proves a caller cannot mutate package state.
func TestEmergingStandardsReturnsCopy(t *testing.T) {
	a := EmergingStandards()
	a[0].Revision = "MUTATED"
	if EmergingStandards()[0].Revision == "MUTATED" {
		t.Error("EmergingStandards must return a defensive copy")
	}
}
