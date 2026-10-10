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
