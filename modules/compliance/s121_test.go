// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package compliance

import (
	"net/http"
	"testing"
)

// TestSignedModelAdmissionClaimVsVerified is the honesty line (mirrors FIN-13):
// a recorded-but-unverified model admission is a CLAIM, not evidence; only a verified
// admission backs the signed_model_admission capability. A sealed AIBOM backs
// model_aibom and CLOSES the ISO 42001 A.7.5 data-provenance gap.
func TestSignedModelAdmissionClaimVsVerified(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	viewer := h.roleToken(admin, tenant, "v@x.io", "viewer")

	// No admission / AIBOM → both capabilities absent.
	caps := capState2(h, viewer, tenant)
	if caps["signed_model_admission"] != "absent" || caps["model_aibom"] != "absent" {
		t.Fatalf("with no data both capabilities must be absent; got admission=%q aibom=%q", caps["signed_model_admission"], caps["model_aibom"])
	}

	// A recorded-but-UNVERIFIED admission must NOT promote the capability (claim != evidence).
	h.seedModelAdmission(tenant, "ver-unsigned", false)
	if got := capState2(h, viewer, tenant)["signed_model_admission"]; got != "absent" {
		t.Errorf("unverified admission: signed_model_admission = %q, want absent (claim != evidence)", got)
	}

	// A VERIFIED admission backs the capability.
	h.seedModelAdmission(tenant, "ver-signed", true)
	if got := capState2(h, viewer, tenant)["signed_model_admission"]; got != "present" {
		t.Errorf("verified admission: signed_model_admission = %q, want present", got)
	}

	// A.7.5 (Data provenance) was a flat nil gap; a SEALED AIBOM closes it.
	if got := h.statuses(viewer, tenant, "iso_42001")["A.7.5"]; got == string(StatusSatisfied) {
		t.Errorf("A.7.5 must NOT be satisfied before any AIBOM; got %q", got)
	}
	h.seedAIBOMSeal(tenant, "owned-1")
	if got := capState2(h, viewer, tenant)["model_aibom"]; got != "present" {
		t.Errorf("model_aibom = %q, want present after sealing an AIBOM", got)
	}
	if got := h.statuses(viewer, tenant, "iso_42001")["A.7.5"]; got != string(StatusSatisfied) {
		t.Errorf("A.7.5 (Data provenance) must be satisfied once an AIBOM is sealed; got %q", got)
	}

	// The framework GET still serves (200) with the new mappings in place.
	if r := h.do("GET", "/v1/m/compliance/frameworks/nist_ai_600_1", viewer, nil, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Fatalf("get nist_ai_600_1 = %d %s", r.code, r.raw)
	}
}
