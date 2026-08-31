// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package compliance

import (
	"net/http"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/model"
)

// capabilityDetail fetches one capability's full evidence object from /capabilities
// (capState2/capabilityStates only expose the state).
func capabilityDetail(h *harness, token string, tenant model.TenantID, key string) map[string]any {
	h.t.Helper()
	r := h.do("GET", "/v1/m/compliance/capabilities", token, nil, tenantHdr(tenant))
	if r.code != http.StatusOK {
		h.t.Fatalf("capabilities = %d %s", r.code, r.raw)
	}
	items, _ := r.body["capabilities"].([]any)
	for _, it := range items {
		m, _ := it.(map[string]any)
		if m["key"] == key {
			return m
		}
	}
	h.t.Fatalf("capability %q missing from evidence map", key)
	return nil
}

// TestPIIDiscoveryAndDLPEnforcementEvidence is the evidence progression (the
// docs/SECURITY-HARDENING.md honesty model): on an empty tenant both capabilities are absent and the
// mapped controls are honest gaps (never by_design-laundered); a real pii_scan row
// turns pii_discovery present (and the ISO classification/labeling controls
// satisfied) while dlp_enforcement stays absent until a rule exists; rules + scans
// arm the gate — with ZERO enforcement events still present, because an estate with
// no violations is not a gap — and dlp_event rows enrich the evidence note.
func TestPIIDiscoveryAndDLPEnforcementEvidence(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	viewer := h.roleToken(admin, tenant, "v@x.io", "viewer")

	// (a) Empty tenant: both capabilities absent; mapped controls honestly gap.
	caps := h.capabilityStates(viewer, tenant)
	if caps["pii_discovery"] != string(EvidenceAbsent) || caps["dlp_enforcement"] != string(EvidenceAbsent) {
		t.Fatalf("empty tenant: want both absent; got pii_discovery=%q dlp_enforcement=%q",
			caps["pii_discovery"], caps["dlp_enforcement"])
	}
	iso := h.statuses(viewer, tenant, "iso_27001_2022")
	for _, id := range []string{"A.5.12", "A.5.13", "A.8.12"} {
		if iso[id] != string(StatusGap) {
			t.Errorf("empty tenant: ISO %s = %q, want gap (single operational capability, no data)", id, iso[id])
		}
	}
	// art_25 / art_32_1_a now carry an OPERATIONAL probe next to the architectural
	// guarantees: with no data they are PARTIAL, never by_design-laundered.
	gdpr := h.statuses(viewer, tenant, "gdpr")
	if gdpr["art_25"] != string(StatusPartial) {
		t.Errorf("empty tenant: gdpr art_25 = %q, want partial (pii_discovery absent, no by_design laundering)", gdpr["art_25"])
	}
	if gdpr["art_32_1_a"] != string(StatusPartial) {
		t.Errorf("empty tenant: gdpr art_32_1_a = %q, want partial (dlp_enforcement absent)", gdpr["art_32_1_a"])
	}

	// (b) A real discovery scan row: pii_discovery present, dlp_enforcement STILL
	// absent (no rule — the gate is inert).
	h.seedPIIScan(tenant, 2)
	caps = h.capabilityStates(viewer, tenant)
	if caps["pii_discovery"] != string(EvidencePresent) {
		t.Fatalf("after pii_scan row: pii_discovery = %q, want present", caps["pii_discovery"])
	}
	if caps["dlp_enforcement"] != string(EvidenceAbsent) {
		t.Fatalf("after pii_scan row only: dlp_enforcement = %q, want absent (no rule, gate inert)", caps["dlp_enforcement"])
	}
	iso = h.statuses(viewer, tenant, "iso_27001_2022")
	for _, id := range []string{"A.5.12", "A.5.13"} {
		if iso[id] != string(StatusSatisfied) {
			t.Errorf("after scan: ISO %s = %q, want satisfied", id, iso[id])
		}
	}
	if iso["A.8.12"] != string(StatusGap) {
		t.Errorf("after scan only: ISO A.8.12 = %q, want gap (no DLP rule)", iso["A.8.12"])
	}
	if got := h.statuses(viewer, tenant, "gdpr")["art_25"]; got != string(StatusSatisfied) {
		t.Errorf("after scan: gdpr art_25 = %q, want satisfied (operational discovery evidence)", got)
	}

	// (c) A DLP rule arms the deny-closed gate: present with ZERO enforcement events.
	h.seedDLPRule(tenant, "pii.financial", "deny")
	dlp := capabilityDetail(h, viewer, tenant, "dlp_enforcement")
	if dlp["state"] != string(EvidencePresent) {
		t.Fatalf("rules+scans: dlp_enforcement = %v, want present (zero events is not a gap)", dlp["state"])
	}
	if detail, _ := dlp["detail"].(string); !strings.Contains(detail, "0 enforcement event(s)") {
		t.Errorf("dlp_enforcement detail should record 0 enforcement events; got %q", detail)
	}
	iso = h.statuses(viewer, tenant, "iso_27001_2022")
	if iso["A.8.12"] != string(StatusSatisfied) {
		t.Errorf("rules+scans: ISO A.8.12 = %q, want satisfied", iso["A.8.12"])
	}
	gdpr = h.statuses(viewer, tenant, "gdpr")
	if gdpr["art_32_1_a"] != string(StatusSatisfied) {
		t.Errorf("rules+scans: gdpr art_32_1_a = %q, want satisfied", gdpr["art_32_1_a"])
	}
	// SOC2 CC6.7 maps dlp_enforcement but also needs lineage/residency evidence: with
	// only the DLP side present it is PARTIAL, not satisfied (no over-claiming).
	if got := h.statuses(viewer, tenant, "soc2_tsc")["CC6.7"]; got != string(StatusPartial) {
		t.Errorf("soc2 CC6.7 = %q, want partial (lineage/residency still absent)", got)
	}

	// (d) An enforcement event enriches the evidence note (the gate provably fired).
	h.seedDLPEvent(tenant, "filtered", 3)
	dlp = capabilityDetail(h, viewer, tenant, "dlp_enforcement")
	if detail, _ := dlp["detail"].(string); !strings.Contains(detail, "1 enforcement event(s)") {
		t.Errorf("dlp_enforcement detail should count the enforcement event; got %q", detail)
	}
}

// TestDLPRuleWithoutScanIsClaimNotEvidence locks the honesty inversion (the
// residency attested-but-unscanned line applied to DLP): a DLP policy recorded on a
// tenant that never ran a discovery scan has no labels to key on — a recorded claim,
// NOT enforceable evidence — so dlp_enforcement stays absent until a scan exists.
func TestDLPRuleWithoutScanIsClaimNotEvidence(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	viewer := h.roleToken(admin, tenant, "v@x.io", "viewer")

	h.seedDLPRule(tenant, "*", "deny")
	dlp := capabilityDetail(h, viewer, tenant, "dlp_enforcement")
	if dlp["state"] != string(EvidenceAbsent) {
		t.Fatalf("rule without scan: dlp_enforcement = %v, want absent (a claim, not evidence)", dlp["state"])
	}
	if detail, _ := dlp["detail"].(string); !strings.Contains(detail, "claim, not enforceable evidence") {
		t.Errorf("rule-without-scan detail must name the claim-vs-evidence line; got %q", detail)
	}
	if got := h.statuses(viewer, tenant, "iso_27001_2022")["A.8.12"]; got != string(StatusGap) {
		t.Errorf("rule without scan: ISO A.8.12 = %q, want gap", got)
	}

	// The first discovery scan turns the recorded policy into enforceable evidence.
	h.seedPIIScan(tenant, 0)
	if got := h.capabilityStates(viewer, tenant)["dlp_enforcement"]; got != string(EvidencePresent) {
		t.Errorf("rule+scan: dlp_enforcement = %q, want present", got)
	}
}
