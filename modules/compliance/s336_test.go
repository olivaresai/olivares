// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package compliance

import (
	"strings"
	"testing"
)

// TestFERPAFramework verifies the FERPA education-records overlay is in the catalog,
// honestly disclaimed, correctly pinned, and that its controls map real capabilities —
// with the annual-notification institutional duty left as an honest nil-capability gap.
func TestFERPAFramework(t *testing.T) {
	fw, ok := frameworkByID["ferpa"]
	if !ok {
		t.Fatal("ferpa framework missing from catalog")
	}
	if fw.Pin.Status != PinInForce {
		t.Errorf("ferpa pin status = %q, want PinInForce", fw.Pin.Status)
	}
	if fw.Pin.PublishedOn != "1974-08-21" {
		t.Errorf("ferpa pin PublishedOn = %q, want 1974-08-21", fw.Pin.PublishedOn)
	}
	for _, want := range []string{"§1232g", "34 CFR Part 99", "not legal advice", "not a certification"} {
		if !strings.Contains(fw.Disclaimer, want) {
			t.Errorf("ferpa disclaimer missing %q; got %q", want, fw.Disclaimer)
		}
	}

	byID := make(map[string]Control, len(fw.Controls))
	for _, c := range fw.Controls {
		byID[c.ID] = c
	}
	hasCap := func(caps []CapabilityKey, want CapabilityKey) bool {
		for _, c := range caps {
			if c == want {
				return true
			}
		}
		return false
	}

	rk, ok := byID["disclosure_recordkeeping"]
	if !ok {
		t.Fatal("ferpa: disclosure_recordkeeping control missing")
	}
	if !hasCap(rk.Capabilities, "audit_trail") || !hasCap(rk.Capabilities, "audit_integrity") {
		t.Errorf("disclosure_recordkeeping must map audit_trail + audit_integrity; got %v", rk.Capabilities)
	}

	an, ok := byID["annual_notification"]
	if !ok {
		t.Fatal("ferpa: annual_notification control missing")
	}
	if len(an.Capabilities) != 0 {
		t.Errorf("annual_notification must be an honest nil-capability gap; got %v", an.Capabilities)
	}
}
