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

// TestResourceAccountingEvidence verifies FIN-12: the resource_accounting capability
// is ABSENT on a tenant with no cost samples and PRESENT once FinOps cost-sample rows
// exist — and it is what lifts EU AI Act art_11/art_12 and ISO 42001 A.4.5.
func TestResourceAccountingEvidence(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	tok := h.roleToken(admin, tenant, "v@x.io", "viewer")

	caps := h.capabilityStates(tok, tenant)
	if caps["resource_accounting"] == string(EvidencePresent) {
		t.Fatal("resource_accounting must be ABSENT with no cost samples (honest gap)")
	}

	h.seedCostSample(tenant, 3)
	caps = h.capabilityStates(tok, tenant)
	if caps["resource_accounting"] != string(EvidencePresent) {
		t.Fatalf("resource_accounting must be PRESENT after seeding cost samples; got %q", caps["resource_accounting"])
	}
}

// capabilityStates returns the capability key→state map from /capabilities.
func (h *harness) capabilityStates(token string, tenant model.TenantID) map[string]string {
	h.t.Helper()
	r := h.do("GET", "/v1/m/compliance/capabilities", token, nil, tenantHdr(tenant))
	if r.code != http.StatusOK {
		h.t.Fatalf("capabilities = %d %s", r.code, r.raw)
	}
	out := map[string]string{}
	items, _ := r.body["capabilities"].([]any)
	for _, it := range items {
		m, _ := it.(map[string]any)
		key, _ := m["key"].(string)
		state, _ := m["state"].(string)
		out[key] = state
	}
	return out
}

// TestExternalActivityDoesNotInflateThreatDetection verifies the CLA-06 honesty line:
// an external_activity evidence finding (from the Compliance Activity Feed) counts for
// the external_activity capability but is classified APART from security findings, so it
// never inflates threat_detection — evidence must not masquerade as a security alert.
func TestExternalActivityDoesNotInflateThreatDetection(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	tok := h.roleToken(admin, tenant, "v@x.io", "viewer")

	subj := h.seedAgent(tenant, "a1")
	h.seedFinding(tenant, subj, "external_activity", model.SeverityLow)

	caps := h.capabilityStates(tok, tenant)
	if caps["external_activity"] != string(EvidencePresent) {
		t.Fatalf("external_activity must be present after an activity finding; got %q", caps["external_activity"])
	}
	if caps["threat_detection"] == string(EvidencePresent) {
		t.Fatal("an external_activity finding must NOT inflate threat_detection (must stay absent)")
	}
}

// TestNISTAI600Honesty verifies: the GenAI Profile is the 7th framework, its
// honest gaps are unmapped (CBRN, IP, bias have no capability), and — the binding
// invariant — on a tenant with only its genesis chain NO control is satisfied without
// operational evidence (by_design != satisfied, docs/SECURITY-HARDENING.md).
func TestNISTAI600Honesty(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	tok := h.roleToken(admin, tenant, "v@x.io", "viewer")

	st := h.statuses(tok, tenant, "nist_ai_600_1")
	if len(st) == 0 {
		t.Fatal("nist_ai_600_1 framework not exposed")
	}
	// Honest gaps: risks the control plane cannot evidence are unmapped (no capability).
	for _, id := range []string{"cbrn_information_or_capabilities", "harmful_bias_or_homogenization", "intellectual_property"} {
		if st[id] != string(StatusUnmapped) {
			t.Errorf("%s must be unmapped (honest gap); got %q", id, st[id])
		}
	}
	// Operational-only controls with no tenant data are an honest gap, NOT satisfied and
	// NOT by_design (confabulation needs evals; environmental_impacts needs cost samples).
	for _, id := range []string{"confabulation", "environmental_impacts"} {
		if st[id] != string(StatusGap) {
			t.Errorf("%s must be a gap with no operational data; got %q", id, st[id])
		}
	}
	// information_integrity maps to the audit ledger, which genuinely exists and verifies
	// on every tenant — so it IS honestly satisfied (real operational evidence, proving the
	// mapping is not decorative). This is the docs/SECURITY-HARDENING.md line working the OTHER way: a
	// control IS satisfied precisely because live evidence backs it.
	if st["information_integrity"] != string(StatusSatisfied) {
		t.Errorf("information_integrity should be satisfied (audit ledger is real evidence); got %q", st["information_integrity"])
	}
}

// TestOSCALExport verifies FIN-10: a sealed package exports as OSCAL v1.2.2 with a
// component-definition, assessment-results AND a control-mapping, the ledger
// anchor (manifest_hash) rides in props, the finding status enum is the OSCAL
// {satisfied,not-satisfied} only, and a by_design/gap control is NEVER laundered to
// OSCAL "satisfied".
func TestOSCALExport(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "e@x.io", "editor")

	seal := h.do("POST", "/v1/m/compliance/frameworks/eu_ai_act/evidence", editor, map[string]any{"scope_note": "oscal test"}, tenantHdr(tenant))
	if seal.code != http.StatusCreated {
		t.Fatalf("seal = %d %s", seal.code, seal.raw)
	}
	id := seal.body["id"].(string)

	exp := h.do("GET", "/v1/m/compliance/evidence/"+id+"/export?format=oscal", editor, nil, tenantHdr(tenant))
	if exp.code != http.StatusOK {
		t.Fatalf("oscal export = %d %s", exp.code, exp.raw)
	}

	// Structural checks against the documented OSCAL v1.2.2 outline.
	cd, ok := exp.body["component-definition"].(map[string]any)
	if !ok {
		t.Fatal("missing component-definition")
	}
	if md, _ := cd["metadata"].(map[string]any); md["oscal-version"] != "1.2.2" {
		t.Fatalf("component-definition oscal-version = %v, want 1.2.2", md["oscal-version"])
	}
	ar, ok := exp.body["assessment-results"].(map[string]any)
	if !ok {
		t.Fatal("missing assessment-results")
	}
	if _, ok := ar["import-ap"].(map[string]any); !ok {
		t.Fatal("assessment-results missing required import-ap")
	}

	// The ledger anchor must be present (tamper-evidence) and the manifest_hash a prop.
	if !strings.Contains(exp.raw, "manifest_hash") || !strings.Contains(exp.raw, "olivares.ai/ns/oscal") {
		t.Fatal("OSCAL export missing manifest_hash prop / custom ns anchor")
	}

	// Every finding status.state must be one of the OSCAL enum, and any control whose
	// real status is NOT satisfied must be OSCAL not-satisfied (no laundering by_design).
	results, _ := ar["results"].([]any)
	if len(results) == 0 {
		t.Fatal("assessment-results has no results")
	}
	res0, _ := results[0].(map[string]any)
	findings, _ := res0["findings"].([]any)
	if len(findings) == 0 {
		t.Fatal("no findings in assessment-results")
	}
	sawNonSatisfied := false
	for _, f := range findings {
		fm, _ := f.(map[string]any)
		tgt, _ := fm["target"].(map[string]any)
		stt, _ := tgt["status"].(map[string]any)
		state, _ := stt["state"].(string)
		reason, _ := stt["reason"].(string)
		if state != "satisfied" && state != "not-satisfied" {
			t.Fatalf("OSCAL status.state = %q, not in enum {satisfied,not-satisfied}", state)
		}
		// The real product status is preserved in reason; if it is by_design it MUST map
		// to not-satisfied, never satisfied.
		if reason == string(StatusByDesign) && state == "satisfied" {
			t.Fatal("a by_design control was laundered into OSCAL 'satisfied'")
		}
		if state == "not-satisfied" {
			sawNonSatisfied = true
		}
	}
	if !sawNonSatisfied {
		t.Fatal("expected at least one not-satisfied control on a fresh tenant (honest)")
	}

	// the bundle also carries an OSCAL Control Mapping model (the framework→capability
	// crosswalk). It must be well-formed (mapping-collection → mappings → maps) and it must
	// NEVER assert conformance — every relationship is "intersects-with", never satisfied/
	// equivalent-to (the only satisfaction assertion lives in assessment-results).
	cm, ok := exp.body["control-mapping"].(map[string]any)
	if !ok {
		t.Fatal("missing control-mapping")
	}
	if md, _ := cm["metadata"].(map[string]any); md["oscal-version"] != "1.2.2" {
		t.Fatalf("control-mapping oscal-version = %v, want 1.2.2", md["oscal-version"])
	}
	mappings, _ := cm["mappings"].([]any)
	if len(mappings) == 0 {
		t.Fatal("control-mapping has no mappings")
	}
	mapping0, _ := mappings[0].(map[string]any)
	if _, ok := mapping0["source-resource"].(map[string]any); !ok {
		t.Fatal("mapping missing source-resource")
	}
	if _, ok := mapping0["target-resource"].(map[string]any); !ok {
		t.Fatal("mapping missing target-resource")
	}
	maps, _ := mapping0["maps"].([]any)
	if len(maps) == 0 {
		t.Fatal("mapping has no maps (eu_ai_act controls all map to capabilities)")
	}
	for _, mm := range maps {
		m, _ := mm.(map[string]any)
		rel, _ := m["relationship"].(string)
		if rel != "intersects-with" {
			t.Fatalf("control-mapping relationship = %q, want intersects-with (never a conformance/equivalence claim)", rel)
		}
		if srcs, _ := m["sources"].([]any); len(srcs) == 0 {
			t.Fatal("map missing sources")
		}
		if tgts, _ := m["targets"].([]any); len(tgts) == 0 {
			t.Fatal("map missing targets (a mapped control must reference its capabilities)")
		}
	}
}

// TestDesignTowardCrosswalksNeverClaimConformance verifies/IDN-10:
// the crosswalk frameworks carry the not-a-certification disclaimer, and the non-final
// standards (COSAiS, AIIM, CISA guidance) are explicitly design-toward / no conformance.
func TestDesignTowardCrosswalksNeverClaimConformance(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	tok := h.roleToken(admin, tenant, "v@x.io", "viewer")

	for _, fwID := range []string{"csa_maestro", "owasp_agentic_tm", "cisa_agentic_adoption", "nist_cosais"} {
		r := h.do("GET", "/v1/m/compliance/frameworks/"+fwID, tok, nil, tenantHdr(tenant))
		if r.code != http.StatusOK {
			t.Fatalf("get %s = %d %s", fwID, r.code, r.raw)
		}
		low := strings.ToLower(r.raw)
		if !strings.Contains(low, "not a certification") && !strings.Contains(low, "no conformance claim") {
			t.Errorf("%s must disclaim certification/conformance; got %s", fwID, r.raw)
		}
	}
	// The truly non-final standards must be explicitly design-toward / in development.
	cosais := h.do("GET", "/v1/m/compliance/frameworks/nist_cosais", tok, nil, tenantHdr(tenant))
	if !strings.Contains(strings.ToLower(cosais.raw), "in development") && !strings.Contains(strings.ToLower(cosais.raw), "design-toward") {
		t.Errorf("nist_cosais must be labeled in-development/design-toward; got %s", cosais.raw)
	}
}
