// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package compliance

import (
	"net/http"
	"testing"
)

// TestSupplierGPAIPostureClaimVsVerified is the FIN-13 honesty line: a
// self-reported (claimed) GPAI posture is NOT evidence for ISO 42001 A.10.3; only
// an operator-VERIFIED posture backs the control. It mirrors the residency
// claim-vs-scan honesty.
func TestSupplierGPAIPostureClaimVsVerified(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	viewer := h.roleToken(admin, tenant, "v@x.io", "viewer")

	// No posture recorded → the control is an honest gap (absent), as A.10.3 was.
	if got := capState2(h, viewer, tenant)["supplier_gpai_posture"]; got != "absent" {
		t.Errorf("no posture: supplier_gpai_posture = %q, want absent", got)
	}

	// A CLAIMED (unverified) posture must NOT promote the capability to present.
	h.seedGPAIPosture(tenant, "anthropic", false)
	if got := capState2(h, viewer, tenant)["supplier_gpai_posture"]; got != "absent" {
		t.Errorf("claimed-only posture: supplier_gpai_posture = %q, want absent (claim != evidence)", got)
	}

	// An operator-VERIFIED posture backs the control.
	h.seedGPAIPosture(tenant, "openai", true)
	if got := capState2(h, viewer, tenant)["supplier_gpai_posture"]; got != "present" {
		t.Errorf("verified posture: supplier_gpai_posture = %q, want present", got)
	}

	// And A.10.3 must now report the capability as satisfied (it was a flat nil gap).
	r := h.do("GET", "/v1/m/compliance/frameworks/iso_42001", viewer, nil, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("get iso_42001 = %d %s", r.code, r.raw)
	}
}
