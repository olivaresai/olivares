// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package compliance

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

func TestWriteStoreErrorAuditSpoolFull(t *testing.T) {
	w := httptest.NewRecorder()
	writeStoreError(w, fmt.Errorf("compliance audit: %w", store.ErrAuditSpoolFull))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", w.Code)
	}
	if !strings.Contains(w.Body.String(), `"message":"audit spool full"`) {
		t.Fatalf("body = %s, want audit spool full message", w.Body.String())
	}
}

// ctrlStatuses extracts control_id -> status from a /status assessment response.
func ctrlStatuses(r resp) map[string]string {
	out := map[string]string{}
	asmt, _ := r.body["assessment"].(map[string]any)
	ctrls, _ := asmt["controls"].([]any)
	for _, c := range ctrls {
		m, _ := c.(map[string]any)
		id, _ := m["control_id"].(string)
		st, _ := m["status"].(string)
		out[id] = st
	}
	return out
}

func (h *harness) statuses(token string, tenant model.TenantID, fw string) map[string]string {
	r := h.do("GET", "/v1/m/compliance/frameworks/"+fw+"/status", token, nil, tenantHdr(tenant))
	if r.code != http.StatusOK {
		h.t.Fatalf("status %s = %d %s", fw, r.code, r.raw)
	}
	return ctrlStatuses(r)
}

// TestCatalogAndDisclaimer verifies the in-repo catalog is served and that the module
// designs-for-audit — it never claims certification.
func TestCatalogAndDisclaimer(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	tok := h.roleToken(admin, tenant, "v@x.io", "viewer")

	r := h.do("GET", "/v1/m/compliance/frameworks", tok, nil, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("list frameworks = %d %s", r.code, r.raw)
	}
	items, _ := r.body["items"].([]any)
	// 17 base frameworks (through) + adds 7 (4 US state AI laws:
	// tx_traiga, ca_sb53, il_hb3773, co_sb26_189 + 3 sector overlays:
	// hipaa_clinical_ai, pci_dss_401_ai, finra_genai) = 24 + nis2 = 25
	// + ferpa (FERPA education-records overlay) = 26.
	if len(items) != len(catalog) || len(catalog) != 26 {
		t.Fatalf("want 26 frameworks, got %d (catalog %d)", len(items), len(catalog))
	}
	if !strings.Contains(strings.ToLower(r.raw), "not a certification") {
		t.Errorf("disclaimer must state NOT a certification; got %s", r.raw)
	}

	if r := h.do("GET", "/v1/m/compliance/frameworks/eu_ai_act", tok, nil, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Fatalf("get framework = %d %s", r.code, r.raw)
	}
	if r := h.do("GET", "/v1/m/compliance/frameworks/nope", tok, nil, tenantHdr(tenant)); r.code != http.StatusNotFound {
		t.Fatalf("unknown framework = %d, want 404", r.code)
	}
}

// TestHonestyEmptyTenant is the core honesty test (docs/SECURITY-HARDENING.md): on a tenant with only
// its genesis audit chain, an architectural-only control is BY_DESIGN (never
// satisfied), an operational-only control with no data is a GAP/PARTIAL, a control
// with no capabilities is UNMAPPED — and an audit-backed control IS satisfied because
// the verified ledger is real evidence.
func TestHonestyEmptyTenant(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	tok := h.roleToken(admin, tenant, "v@x.io", "viewer")

	eu := h.statuses(tok, tenant, "eu_ai_act")
	if got := eu["art_5"]; got == string(StatusSatisfied) {
		t.Errorf("art_5 must NOT be satisfied without a risk classification; got %s", got)
	}

	nist := h.statuses(tok, tenant, "nist_ai_rmf")
	if got := nist["GOVERN-4.1"]; got != string(StatusByDesign) {
		t.Errorf("GOVERN-4.1 (architectural-only) must be by_design, never satisfied; got %s", got)
	}

	gdpr := h.statuses(tok, tenant, "gdpr")
	// Mapped art_17 to the OPERATIONAL rtbf_erasure capability: with zero
	// erasure receipts the control is an honest GAP (it stopped being unmapped the
	// day the workflow shipped, and becomes satisfied only when a real erasure was
	// fulfilled — the claim-vs-evidence line).
	if got := gdpr["art_17"]; got != string(StatusGap) {
		t.Errorf("art_17 (mapped, no erasure fulfilled yet) must be gap; got %s", got)
	}
	// art_5_2 (accountability) is backed by the append-only ledger that verifies —
	// genuine operational evidence — so it IS satisfied even on a quiet tenant.
	if got := gdpr["art_5_2"]; got != string(StatusSatisfied) {
		t.Errorf("art_5_2 must be satisfied on the verified audit ledger; got %s", got)
	}
}

// TestRiskClassifyFeedsMappingAndIsGoverned verifies that classifying an agent from
// observed signals produces a governed suggestion, that it is audited, and that the
// resulting classification turns the risk_classification capability present (so a
// control mapped to it can become satisfied).
func TestRiskClassifyFeedsMappingAndIsGoverned(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	tok := h.roleToken(admin, tenant, "e@x.io", "editor")
	adminTok := h.roleToken(admin, tenant, "a@x.io", "admin")

	agent := h.seedAgent(tenant, "bot")
	h.seedEdge(tenant, agent, sdkmodel.ModeReadWrite, false, true)
	h.seedFinding(tenant, agent, "guardrail", model.SeverityHigh)

	r := h.do("POST", "/v1/m/compliance/risk/classify", tok, map[string]any{"subject_ref": agent.String()}, tenantHdr(tenant))
	if r.code != http.StatusCreated {
		t.Fatalf("classify = %d %s", r.code, r.raw)
	}
	if got := r.body["suggested_tier"]; got != string(TierHigh) {
		t.Errorf("agent with high finding + write access should suggest high; got %v", got)
	}
	if got := r.body["state"]; got != string(RiskSuggested) {
		t.Errorf("a fresh classification must be 'suggested', got %v", got)
	}
	id := r.body["id"].(string)

	// risk_classification capability is now present → eu art_5 becomes satisfied.
	if got := h.statuses(tok, tenant, "eu_ai_act")["art_5"]; got != string(StatusSatisfied) {
		t.Errorf("art_5 should be satisfied once a classification exists; got %s", got)
	}

	// Review is the admin-tier decision surface; an editor cannot.
	if r := h.do("POST", "/v1/m/compliance/risk/"+id+"/review", tok, map[string]any{"tier": "high"}, tenantHdr(tenant)); r.code != http.StatusForbidden {
		t.Fatalf("editor review = %d, want 403", r.code)
	}
	r = h.do("POST", "/v1/m/compliance/risk/"+id+"/review", adminTok, map[string]any{"tier": "high"}, tenantHdr(tenant))
	if r.code != http.StatusOK || r.body["state"] != string(RiskApproved) {
		t.Fatalf("admin approve = %d state=%v %s", r.code, r.body["state"], r.raw)
	}

	actions := strings.Join(h.auditActions(tenant), ",")
	if !strings.Contains(actions, "compliance.risk.classify") || !strings.Contains(actions, "compliance.risk.review") {
		t.Errorf("classify and review must be self-audited; actions=%s", actions)
	}
}

// TestRiskNeverAutoUnacceptable proves the heuristic never asserts the prohibited tier
// (a legal determination): only a reviewer may set unacceptable.
func TestRiskNeverAutoUnacceptable(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	tok := h.roleToken(admin, tenant, "e@x.io", "editor")
	adminTok := h.roleToken(admin, tenant, "a@x.io", "admin")

	// A benign agent (no signals) → minimal.
	benign := h.seedAgent(tenant, "benign")
	r := h.do("POST", "/v1/m/compliance/risk/classify", tok, map[string]any{"subject_ref": benign.String()}, tenantHdr(tenant))
	if got := r.body["suggested_tier"]; got != string(TierMinimal) {
		t.Errorf("benign agent should be minimal; got %v", got)
	}

	// A heavily-writing agent with critical findings → high, NEVER unacceptable.
	risky := h.seedAgent(tenant, "risky")
	for i := 0; i < 6; i++ {
		h.seedEdge(tenant, risky, sdkmodel.ModeReadWrite, false, true)
	}
	h.seedFinding(tenant, risky, "anomaly", model.SeverityCritical)
	r = h.do("POST", "/v1/m/compliance/risk/classify", tok, map[string]any{"subject_ref": risky.String()}, tenantHdr(tenant))
	if got := r.body["suggested_tier"]; got != string(TierHigh) {
		t.Errorf("risky agent should suggest high; got %v", got)
	}
	if got := r.body["tier"]; got == string(TierUnacceptable) {
		t.Errorf("heuristic must NEVER assign unacceptable; got %v", got)
	}
	id := r.body["id"].(string)

	// Only a reviewer can set unacceptable.
	r = h.do("POST", "/v1/m/compliance/risk/"+id+"/review", adminTok, map[string]any{"tier": "unacceptable", "note": "prohibited use"}, tenantHdr(tenant))
	if r.code != http.StatusOK || r.body["tier"] != string(TierUnacceptable) || r.body["state"] != string(RiskOverridden) {
		t.Fatalf("admin override to unacceptable = %d tier=%v state=%v", r.code, r.body["tier"], r.body["state"])
	}
}

// TestEvidencePackageIntegrityAndDeterminism seals an evidence package, checks the
// ledger integrity proof, and verifies the body manifest hash is deterministic across
// re-runs with unchanged evidence (the chain advances, the assessment hash does not).
func TestEvidencePackageIntegrityAndDeterminism(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "e@x.io", "editor")

	// Produce some audited activity so the chain is non-trivial.
	agent := h.seedAgent(tenant, "bot")
	h.do("POST", "/v1/m/compliance/risk/classify", editor, map[string]any{"subject_ref": agent.String()}, tenantHdr(tenant))

	// Catalog control count for eu_ai_act.
	fwResp := h.do("GET", "/v1/m/compliance/frameworks/eu_ai_act", editor, nil, tenantHdr(tenant))
	fw, _ := fwResp.body["framework"].(map[string]any)
	wantTotal := len(fw["controls"].([]any))

	r1 := h.do("POST", "/v1/m/compliance/frameworks/eu_ai_act/evidence", editor, map[string]any{"scope_note": "Q2 audit"}, tenantHdr(tenant))
	if r1.code != http.StatusCreated {
		t.Fatalf("seal = %d %s", r1.code, r1.raw)
	}
	if r1.body["integrity_ok"] != true {
		t.Errorf("clean chain must verify integrity_ok=true; got %v", r1.body["integrity_ok"])
	}
	if intOf(r1.body["ledger_seq"]) <= 0 {
		t.Errorf("package must anchor to a ledger head seq > 0; got %v", r1.body["ledger_seq"])
	}
	summary := r1.body["summary"].(map[string]any)
	if intOf(summary["total"]) != wantTotal {
		t.Errorf("package total %v != catalog controls %d", summary["total"], wantTotal)
	}
	if r1.body["manifest_hash"] == "" || r1.body["manifest_hash"] == nil {
		t.Errorf("package must carry a manifest hash")
	}
	if !strings.Contains(strings.ToLower(r1.raw), "not a certification") {
		t.Errorf("package must carry the no-certification disclaimer")
	}

	// Seal again with no new evidence: the chain advanced (the seal self-audited), but
	// the assessment body hash is identical (deterministic, tamper-evident).
	r2 := h.do("POST", "/v1/m/compliance/frameworks/eu_ai_act/evidence", editor, nil, tenantHdr(tenant))
	if r2.code != http.StatusCreated {
		t.Fatalf("seal2 = %d %s", r2.code, r2.raw)
	}
	if r1.body["manifest_hash"] != r2.body["manifest_hash"] {
		t.Errorf("manifest hash must be deterministic for unchanged evidence: %v != %v", r1.body["manifest_hash"], r2.body["manifest_hash"])
	}
	if intOf(r2.body["ledger_seq"]) <= intOf(r1.body["ledger_seq"]) {
		t.Errorf("second seal must anchor to a later head (the first seal was audited): %v !> %v", r2.body["ledger_seq"], r1.body["ledger_seq"])
	}
}

// TestEvidenceReadExportSelfAudits checks the sealed-package read and export are
// self-audited sensitive reads, and that the CSV export is well-formed.
func TestEvidenceReadExportSelfAudits(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "e@x.io", "editor")

	r := h.do("POST", "/v1/m/compliance/frameworks/soc2_tsc/evidence", editor, nil, tenantHdr(tenant))
	if r.code != http.StatusCreated {
		t.Fatalf("seal = %d %s", r.code, r.raw)
	}
	id := r.body["id"].(string)

	if r := h.do("GET", "/v1/m/compliance/evidence/"+id, editor, nil, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Fatalf("get evidence = %d %s", r.code, r.raw)
	}
	exp := h.do("GET", "/v1/m/compliance/evidence/"+id+"/export?format=csv", editor, nil, tenantHdr(tenant))
	if exp.code != http.StatusOK || !strings.Contains(exp.raw, "control_id,status") {
		t.Fatalf("csv export = %d, body=%s", exp.code, exp.raw)
	}

	actions := strings.Join(h.auditActions(tenant), ",")
	if !strings.Contains(actions, "compliance.evidence.seal") ||
		!strings.Contains(actions, "compliance.evidence.read") ||
		!strings.Contains(actions, "compliance.evidence.export") {
		t.Errorf("seal/read/export must each self-audit; actions=%s", actions)
	}
}

// TestResidencyAttestScanEmitsFinding attests residency, scans existing egress signals
// (read inline from the lineage stand-in) and verifies a violation Finding + bus signal.
func TestResidencyAttestScanEmitsFinding(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "e@x.io", "editor")
	viewer := h.roleToken(admin, tenant, "v@x.io", "viewer")

	if r := h.do("POST", "/v1/m/compliance/residency", editor, map[string]any{"region": "eu-west", "self_hosted": true}, tenantHdr(tenant)); r.code != http.StatusCreated {
		t.Fatalf("attest = %d %s", r.code, r.raw)
	}
	// HONESTY: attestation alone is a CLAIM, not evidence of no-egress. Until a scan
	// actually looks, data_residency is an honest gap (absent), never present.
	if got := capState2(h, viewer, tenant)["data_residency"]; got != "absent" {
		t.Errorf("data_residency must be absent until scanned; got %s", got)
	}

	// A clean scan (no egress signals yet) turns it present — now there IS evidence.
	if r := h.do("POST", "/v1/m/compliance/residency/scan", editor, nil, tenantHdr(tenant)); r.code != http.StatusOK || intOf(r.body["violations"]) != 0 {
		t.Fatalf("clean scan = %d %s", r.code, r.raw)
	}
	if got := capState2(h, viewer, tenant)["data_residency"]; got != "present" {
		t.Errorf("data_residency should be present after a clean scan; got %s", got)
	}

	// Two egress signals exist (the model VIII stand-in): the scan must flag them.
	h.seedLineageEgress(tenant, 2)
	scan := h.do("POST", "/v1/m/compliance/residency/scan", editor, nil, tenantHdr(tenant))
	if scan.code != http.StatusOK {
		t.Fatalf("scan = %d %s", scan.code, scan.raw)
	}
	if intOf(scan.body["egress_signals"]) != 2 || intOf(scan.body["violations"]) != 2 || intOf(scan.body["findings_emitted"]) < 1 {
		t.Fatalf("scan should observe 2 egress, 2 violations, >=1 finding; got %s", scan.raw)
	}

	h.waitFindings()
	found := false
	for _, f := range h.deliveredFindings() {
		if f.Kind == busResidencyViolation {
			found = true
		}
	}
	if !found {
		t.Errorf("a residency-violation finding must be delivered to the bus for")
	}

	// With a violation observed, data_residency is now an honest gap (absent).
	if got := capState2(h, viewer, tenant)["data_residency"]; got != "absent" {
		t.Errorf("data_residency must be absent once a violation is observed; got %s", got)
	}
}

// TestResidencyScanViaSeam verifies the injected LineageSource branch: a wired seam
// supplies egress signals instead of the inline knowledge.lineage read.
func TestResidencyScanViaSeam(t *testing.T) {
	h := newHarness(t, WithLineageSource(fakeLineage{n: 3}))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "e@x.io", "editor")

	if r := h.do("POST", "/v1/m/compliance/residency", editor, map[string]any{"region": "eu", "self_hosted": true}, tenantHdr(tenant)); r.code != http.StatusCreated {
		t.Fatalf("attest = %d %s", r.code, r.raw)
	}
	scan := h.do("POST", "/v1/m/compliance/residency/scan", editor, nil, tenantHdr(tenant))
	if scan.code != http.StatusOK || intOf(scan.body["egress_signals"]) != 3 || intOf(scan.body["violations"]) != 3 {
		t.Fatalf("seam scan should observe 3 egress/violations; got %s", scan.raw)
	}
}

// TestResidencyMultiRegionViolationCount locks the fix that egress signals are
// tenant-global: with 2 self-hosted regions and 3 egress events the violation count is
// 3 (distinct events), NOT 6 (regions × events), though each region is flagged.
func TestResidencyMultiRegionViolationCount(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "e@x.io", "editor")

	for _, region := range []string{"eu-west", "eu-central"} {
		if r := h.do("POST", "/v1/m/compliance/residency", editor, map[string]any{"region": region, "self_hosted": true}, tenantHdr(tenant)); r.code != http.StatusCreated {
			t.Fatalf("attest %s = %d %s", region, r.code, r.raw)
		}
	}
	h.seedLineageEgress(tenant, 3)
	scan := h.do("POST", "/v1/m/compliance/residency/scan", editor, nil, tenantHdr(tenant))
	if scan.code != http.StatusOK {
		t.Fatalf("scan = %d %s", scan.code, scan.raw)
	}
	if intOf(scan.body["regions_checked"]) != 2 {
		t.Errorf("want 2 regions checked; got %v", scan.body["regions_checked"])
	}
	if got := intOf(scan.body["violations"]); got != 3 {
		t.Errorf("violations must be the 3 distinct egress events, not regions×events; got %d", got)
	}
	if got := intOf(scan.body["findings_emitted"]); got != 2 {
		t.Errorf("each self-hosted region is flagged: want 2 findings; got %d", got)
	}
}

// TestResidencyPinInferenceCoherence verifies the scan flags inference that
// crosses a tenant's control-plane region pin, deduped per distinct geo, and does NOT
// flag an attestation gap when the pin has a matching self-hosted attestation.
func TestResidencyPinInferenceCoherence(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "e@x.io", "editor")

	h.pinOrg(tenant, "eu")
	if r := h.do("POST", "/v1/m/compliance/residency", editor, map[string]any{"region": "eu", "self_hosted": true}, tenantHdr(tenant)); r.code != http.StatusCreated {
		t.Fatalf("attest = %d %s", r.code, r.raw)
	}
	// Observed inference: one in-region (eu, fine) and two out-of-region (us, one
	// distinct violation after dedup).
	h.seedCostSampleGeo(tenant, "eu")
	h.seedCostSampleGeo(tenant, "us")
	h.seedCostSampleGeo(tenant, "us")

	scan := h.do("POST", "/v1/m/compliance/residency/scan", editor, nil, tenantHdr(tenant))
	if scan.code != http.StatusOK {
		t.Fatalf("scan = %d %s", scan.code, scan.raw)
	}
	if got, _ := scan.body["pinned_region"].(string); got != "eu" {
		t.Errorf("pinned_region = %q, want eu", got)
	}
	if got := intOf(scan.body["inference_violations"]); got != 1 {
		t.Errorf("inference_violations = %d, want 1 (us, deduped); body=%s", got, scan.raw)
	}
	if gap, _ := scan.body["attestation_gap"].(bool); gap {
		t.Error("attestation_gap must be false when an eu self-hosted attestation exists")
	}
}

// TestResidencyPinAttestationGap verifies the scan flags a pin that has no
// backing self-hosted attestation for its region.
func TestResidencyPinAttestationGap(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "e@x.io", "editor")

	h.pinOrg(tenant, "eu") // pinned eu, but no eu attestation
	scan := h.do("POST", "/v1/m/compliance/residency/scan", editor, nil, tenantHdr(tenant))
	if scan.code != http.StatusOK {
		t.Fatalf("scan = %d %s", scan.code, scan.raw)
	}
	if gap, _ := scan.body["attestation_gap"].(bool); !gap {
		t.Errorf("attestation_gap must be true when pinned eu has no eu self-hosted attestation; body=%s", scan.raw)
	}
}

// TestResidencyWorkspaceGeoDrift verifies the workspace-geo drift branch:
// PERMITTED (models.workspace_residency allowed_geos) vs OBSERVED (the per-workspace
// inference geos on finops cost samples) — membership, not pin equality — deduped per
// (workspace, geo), with unattributed samples (empty workspace_ref, the default
// workspace) skipped, and the drift reusing the residency_violation Finding + bus signal.
func TestResidencyWorkspaceGeoDrift(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "e@x.io", "editor")

	h.seedWorkspaceResidency(tenant, "wrkspc_main", "us")
	// Observed: one permitted (us), two outside the allowed set (global → ONE distinct
	// violation after dedup) and one unattributed sample the branch must skip.
	h.seedCostSampleGeoWS(tenant, "wrkspc_main", "us")
	h.seedCostSampleGeoWS(tenant, "wrkspc_main", "global")
	h.seedCostSampleGeoWS(tenant, "wrkspc_main", "global")
	h.seedCostSampleGeoWS(tenant, "", "global")

	scan := h.do("POST", "/v1/m/compliance/residency/scan", editor, nil, tenantHdr(tenant))
	if scan.code != http.StatusOK {
		t.Fatalf("scan = %d %s", scan.code, scan.raw)
	}
	if got := intOf(scan.body["workspace_geo_violations"]); got != 1 {
		t.Errorf("workspace_geo_violations = %d, want 1 (global deduped; empty-workspace sample ignored); body=%s", got, scan.raw)
	}
	if got := intOf(scan.body["findings_emitted"]); got != 1 {
		t.Errorf("findings_emitted = %d, want exactly the 1 drift finding; body=%s", got, scan.raw)
	}

	h.waitFindings()
	var drift []sdkmodel.FindingReport
	for _, f := range h.deliveredFindings() {
		if f.Kind == busResidencyViolation {
			drift = append(drift, f)
		}
	}
	if len(drift) != 1 {
		t.Fatalf("want 1 %s bus signal, got %d", busResidencyViolation, len(drift))
	}
	if drift[0].Severity != sdkmodel.SeverityHigh {
		t.Errorf("drift severity = %s, want high", drift[0].Severity)
	}
	if !strings.Contains(drift[0].Title, "wrkspc_main") {
		t.Errorf("drift title must mention the workspace ref; got %q", drift[0].Title)
	}
}

// TestResidencyWorkspaceGeoUnrestricted verifies a workspace with EMPTY
// allowed_geos is unrestricted/unreported: no permitted set to drift from, never a
// violation regardless of the observed geos.
func TestResidencyWorkspaceGeoUnrestricted(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "e@x.io", "editor")

	h.seedWorkspaceResidency(tenant, "wrkspc_open", "")
	h.seedCostSampleGeoWS(tenant, "wrkspc_open", "global")
	h.seedCostSampleGeoWS(tenant, "wrkspc_open", "not_available")

	scan := h.do("POST", "/v1/m/compliance/residency/scan", editor, nil, tenantHdr(tenant))
	if scan.code != http.StatusOK {
		t.Fatalf("scan = %d %s", scan.code, scan.raw)
	}
	if got := intOf(scan.body["workspace_geo_violations"]); got != 0 {
		t.Errorf("workspace_geo_violations = %d, want 0 (empty allowed_geos = unrestricted); body=%s", got, scan.raw)
	}
	if got := intOf(scan.body["findings_emitted"]); got != 0 {
		t.Errorf("findings_emitted = %d, want 0; body=%s", got, scan.raw)
	}
}

// TestResidencyWorkspaceGeoNotAvailable locks the deny-closed semantics: when a
// workspace DOES declare allowed geos, an observed "not_available" geo (pre-Feb-2026
// models report it) is drift — residency cannot be proven, so it is not compliant.
func TestResidencyWorkspaceGeoNotAvailable(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "e@x.io", "editor")

	h.seedWorkspaceResidency(tenant, "wrkspc_strict", "us")
	h.seedCostSampleGeoWS(tenant, "wrkspc_strict", "not_available")

	scan := h.do("POST", "/v1/m/compliance/residency/scan", editor, nil, tenantHdr(tenant))
	if scan.code != http.StatusOK {
		t.Fatalf("scan = %d %s", scan.code, scan.raw)
	}
	if got := intOf(scan.body["workspace_geo_violations"]); got != 1 {
		t.Errorf("workspace_geo_violations = %d, want 1 (not_available cannot prove residency, deny-closed); body=%s", got, scan.raw)
	}
}

// capState2 reads the live capability evidence map as key -> state.
func capState2(h *harness, token string, tenant model.TenantID) map[string]string {
	r := h.do("GET", "/v1/m/compliance/capabilities", token, nil, tenantHdr(tenant))
	if r.code != http.StatusOK {
		h.t.Fatalf("capabilities = %d %s", r.code, r.raw)
	}
	out := map[string]string{}
	items, _ := r.body["capabilities"].([]any)
	for _, it := range items {
		m, _ := it.(map[string]any)
		key, _ := m["key"].(string)
		st, _ := m["state"].(string)
		out[key] = st
	}
	return out
}

// TestCapabilitiesEvidenceMap checks the evidence map distinguishes architectural
// (present by design) from operational (absent without data).
func TestCapabilitiesEvidenceMap(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	viewer := h.roleToken(admin, tenant, "v@x.io", "viewer")

	r := h.do("GET", "/v1/m/compliance/capabilities", viewer, nil, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("capabilities = %d %s", r.code, r.raw)
	}
	items, _ := r.body["capabilities"].([]any)
	if len(items) != len(capabilityCatalog) {
		t.Fatalf("want %d capabilities, got %d", len(capabilityCatalog), len(items))
	}
	byKey := map[string]map[string]any{}
	for _, it := range items {
		m := it.(map[string]any)
		byKey[m["key"].(string)] = m
	}
	if byKey["audit_immutability"]["state"] != "present" || byKey["audit_immutability"]["class"] != string(ClassArchitectural) {
		t.Errorf("audit_immutability must be present + architectural; got %v", byKey["audit_immutability"])
	}
	if byKey["risk_classification"]["state"] != "absent" {
		t.Errorf("risk_classification must be absent with no classifications; got %v", byKey["risk_classification"]["state"])
	}
}

// TestLeastPrivilegeDriftEvidenceIsReconciled proves the compliance evidence engine
// consumes module III's RECONCILED drift, not the raw store path (C2). A
// single fully-permitted cross-origin access must contribute 0 to the
// least_privilege_drift count — the raw path would inflate it to 2 (a false unexpected
// access + a false unused grant), shipping false positives into the compliance report.
func TestLeastPrivilegeDriftEvidenceIsReconciled(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	viewer := h.roleToken(admin, tenant, "v@x.io", "viewer")

	h.seedCrossOriginAccess(tenant)

	r := h.do("GET", "/v1/m/compliance/capabilities", viewer, nil, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("capabilities = %d %s", r.code, r.raw)
	}
	items, _ := r.body["capabilities"].([]any)
	var drift map[string]any
	for _, it := range items {
		m, _ := it.(map[string]any)
		if m["key"] == "least_privilege_drift" {
			drift = m
		}
	}
	if drift == nil {
		t.Fatal("least_privilege_drift capability missing from evidence map")
	}
	// Edges exist, so the capability is present (drift is computable)…
	if drift["state"] != "present" {
		t.Errorf("least_privilege_drift state = %v, want present (access edges exist)", drift["state"])
	}
	// …but the reconciled count is 0 for this permitted cross-origin access. `count`
	// is omitempty, so a missing key means 0; a present non-zero value is the bug.
	if c, ok := drift["count"].(float64); ok && c != 0 {
		t.Errorf("least_privilege_drift count = %v, want 0 (reconciled); the raw path would report 2 cross-origin false positives", c)
	}
}

// TestAuthzTiers verifies module permission tiers: read for catalog/status, write to
// seal/classify, admin to review.
func TestAuthzTiers(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	viewer := h.roleToken(admin, tenant, "v@x.io", "viewer")
	editor := h.roleToken(admin, tenant, "e@x.io", "editor")

	if r := h.do("GET", "/v1/m/compliance/frameworks/gdpr/status", viewer, nil, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Fatalf("viewer status = %d, want 200", r.code)
	}
	if r := h.do("POST", "/v1/m/compliance/frameworks/gdpr/evidence", viewer, nil, tenantHdr(tenant)); r.code != http.StatusForbidden {
		t.Fatalf("viewer seal = %d, want 403", r.code)
	}
	if r := h.do("POST", "/v1/m/compliance/frameworks/gdpr/evidence", editor, nil, tenantHdr(tenant)); r.code != http.StatusCreated {
		t.Fatalf("editor seal = %d, want 201 (%s)", r.code, r.raw)
	}
}

// TestOperationalCapabilitiesBecomePresent seeds real core/ext evidence and verifies
// the operational capability probes flip from absent to present — and that a control
// then becomes satisfied (live evidence, not design alone).
func TestOperationalCapabilitiesBecomePresent(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	viewer := h.roleToken(admin, tenant, "v@x.io", "viewer")

	before := capState2(h, viewer, tenant)
	for _, k := range []string{"access_observability", "least_privilege_drift", "identity_governance", "quality_evaluation", "change_management", "data_lineage"} {
		if before[k] != "absent" {
			t.Errorf("capability %s should start absent; got %s", k, before[k])
		}
	}

	agent := h.seedAgent(tenant, "bot")
	h.seedEdge(tenant, agent, sdkmodel.ModeReadWrite, true, true)
	h.seedFinding(tenant, agent, "guardrail", model.SeverityMedium)
	h.seedFinding(tenant, agent, "redteam", model.SeverityHigh)
	h.seedEval(tenant, agent)
	h.seedDeployment(tenant)
	h.seedIdentityPolicy(tenant)
	h.seedLineageEgress(tenant, 1)

	after := capState2(h, viewer, tenant)
	for _, k := range []string{
		"access_observability", "least_privilege_drift", "identity_governance",
		"threat_detection", "adversarial_testing", "quality_evaluation", "change_management", "data_lineage",
	} {
		if after[k] != "present" {
			t.Errorf("capability %s should be present after seeding; got %s", k, after[k])
		}
	}
	// encryption_at_rest stays absent — it is opt-in and was never attested (honest gap).
	if after["encryption_at_rest"] != "absent" {
		t.Errorf("encryption_at_rest must stay absent until attested; got %s", after["encryption_at_rest"])
	}
}

// TestGapAnalysisExport checks the gap analysis flags partial/gap/unmapped controls
// with their missing capabilities, and the CSV export works.
func TestGapAnalysisExport(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	viewer := h.roleToken(admin, tenant, "v@x.io", "viewer")

	r := h.do("GET", "/v1/m/compliance/frameworks/eu_ai_act/gaps", viewer, nil, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("gaps = %d %s", r.code, r.raw)
	}
	gaps, _ := r.body["gaps"].([]any)
	if len(gaps) == 0 {
		t.Errorf("a quiet tenant must show gaps, not full coverage")
	}
	csv := h.do("GET", "/v1/m/compliance/frameworks/eu_ai_act/gaps?format=csv", viewer, nil, tenantHdr(tenant))
	if csv.code != http.StatusOK || !strings.Contains(csv.raw, "control_id,status,title,missing_capabilities") {
		t.Fatalf("gaps csv = %d body=%s", csv.code, csv.raw)
	}
}

func TestHIPAATechnicalSafeguardsGapReport(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "hipaa")
	viewer := h.roleToken(admin, tenant, "v@x.io", "viewer")

	agent := h.seedAgent(tenant, "clinical-ai")
	h.seedEdge(tenant, agent, sdkmodel.ModeRead, true, true)
	h.seedIdentityPolicy(tenant)

	r := h.do("GET", "/v1/m/compliance/hipaa/gap-report", viewer, nil, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("hipaa gap report = %d %s", r.code, r.raw)
	}
	if !strings.Contains(strings.ToLower(r.raw), "not a hipaa compliance certification") {
		t.Fatalf("HIPAA report disclaimer missing certification caveat: %s", r.raw)
	}
	controls, _ := r.body["controls"].([]any)
	if len(controls) != 5 {
		t.Fatalf("HIPAA technical controls = %d, want 5 (%s)", len(controls), r.raw)
	}
	byID := map[string]map[string]any{}
	for _, c := range controls {
		row := c.(map[string]any)
		byID[row["control_id"].(string)] = row
	}
	for _, id := range []string{"164.312(a)", "164.312(b)", "164.312(c)", "164.312(d)", "164.312(e)"} {
		if byID[id] == nil {
			t.Fatalf("missing HIPAA control %s in %s", id, r.raw)
		}
	}
	if byID["164.312(a)"]["status"] != string(StatusSatisfied) {
		t.Fatalf("access control status = %v, want satisfied (%s)", byID["164.312(a)"]["status"], r.raw)
	}
	if byID["164.312(c)"]["status"] == string(StatusSatisfied) {
		t.Fatalf("integrity must not be satisfied without change-management evidence: %s", r.raw)
	}
	missing, _ := byID["164.312(c)"]["missing_capabilities"].([]any)
	if len(missing) == 0 {
		t.Fatalf("integrity row must list missing capabilities: %s", r.raw)
	}
}

// TestObserveRiskSignalsHighBeyondFirstPage (sweep, D-03 analog) reproduces the
// same enforcement-path truncation in the EU AI Act risk classifier: an agent with
// more than one page (listCap) of findings whose single HIGH-severity finding sorts
// onto a LATER page. Before the keyset-drain fix, observeRiskSignals read only the
// first page, missed the high finding, and suggested a LOWER tier — silently
// under-classifying the AI system's risk.
func TestObserveRiskSignalsHighBeyondFirstPage(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	agent := h.seedAgent(tenant, "bot")

	// listCap low findings FIRST (earlier ids ⇒ page 1), the single HIGH finding LAST.
	h.mutate(tenant, func(sc store.Scope) error {
		for i := 0; i < listCap; i++ {
			if _, err := sc.Findings().Create(context.Background(), model.Finding{
				Kind: "guardrail", Severity: model.SeverityLow, Status: model.FindingOpen, Source: "test",
				SubjectKind: "agent", SubjectID: agent, Title: "noise", OccurredAt: h.mod.clock.Now(),
			}); err != nil {
				return err
			}
		}
		_, err := sc.Findings().Create(context.Background(), model.Finding{
			Kind: "guardrail", Severity: model.SeverityHigh, Status: model.FindingOpen, Source: "test",
			SubjectKind: "agent", SubjectID: agent, Title: "the high one", OccurredAt: h.mod.clock.Now(),
		})
		return err
	})

	var sig riskSignals
	if err := h.st.View(context.Background(), tenant, func(sc store.Scope) error {
		var e error
		sig, e = h.mod.observeRiskSignals(context.Background(), sc, tenant, agent, agent.String())
		return e
	}); err != nil {
		t.Fatal(err)
	}
	if sig.High == 0 {
		t.Fatalf("high finding beyond the first page was truncated: High=%d", sig.High)
	}
	if tier, _ := suggestTier(sig); tier != TierHigh {
		t.Fatalf("suggestTier = %q, want high (a high finding on a later page must still classify high)", tier)
	}
}

// TestComplianceSuggestTierTruncatedFailsSafe locks the fail-safe: a truncated scan
// must never yield a tier below TierHigh (the highest heuristic tier; unacceptable is
// human-only), so an unseen high/critical finding is never classified away.
func TestComplianceSuggestTierTruncatedFailsSafe(t *testing.T) {
	if tier, _ := suggestTier(riskSignals{TotalEdges: 1, Truncated: true}); tier != TierHigh {
		t.Fatalf("suggestTier(truncated) = %q, want high (fail-safe: never lower on truncation)", tier)
	}
}
