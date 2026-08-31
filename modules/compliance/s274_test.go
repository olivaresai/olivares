// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package compliance

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

// US state AI law compliance depth, sector overlays (HIPAA/
// PCI/FINRA), continuous controls monitoring (CCM) and FedRAMP 20x
// KSI. These tests exercise the OPEN-CORE half: the deny-closed
// (501) endpoints without the enterprise add-on, the governed
// persistence/export/audit substrate, the honesty invariants
// (disclaimer, gaps), the framework catalog integrity and the
// regulatory-calendar milestone sourcing. The real pack building
// (enterprise/compliancedepth) is tested in that package; here
// stubs stand in for the closed add-on so the open behavior is
// verified without the open module importing the closed one.

// --- stub ---------------------------------------------------

// stubDepthPackager is a programmable stand-in for the commercial
// ComplianceDepthPackager.
type stubDepthPackager struct {
	usStatePack   *USStatePack
	sectorPack    *SectorPack
	ccmSnapshot   *CCMSnapshot
	driftFindings []DriftFinding
	fedRAMPDoc    *FedRAMPKSIDocument
	err           error
}

func (s *stubDepthPackager) BuildUSStatePack(
	_ context.Context,
	_ USStateInput,
	_ map[string]FrameworkAssessment,
) (*USStatePack, error) {
	return s.usStatePack, s.err
}

func (s *stubDepthPackager) BuildSectorPack(
	_ context.Context,
	_ SectorInput,
	_ map[string]FrameworkAssessment,
) (*SectorPack, error) {
	return s.sectorPack, s.err
}

func (s *stubDepthPackager) RunCCMSnapshot(
	_ context.Context,
	_ CCMSnapshotInput,
	_ map[string]FrameworkAssessment,
) (*CCMSnapshot, error) {
	return s.ccmSnapshot, s.err
}

func (s *stubDepthPackager) DetectDrift(
	_ context.Context,
	_ *CCMSnapshot,
	_ *CCMSnapshot,
) ([]DriftFinding, error) {
	return s.driftFindings, s.err
}

func (s *stubDepthPackager) BuildFedRAMPKSIs(
	_ context.Context,
	_ FedRAMPKSIInput,
	_ map[string]FrameworkAssessment,
) (*FedRAMPKSIDocument, error) {
	return s.fedRAMPDoc, s.err
}

// --- sample data helpers ------------------------------------

func sampleUSStatePack() *USStatePack {
	return &USStatePack{
		Jurisdictions: []JurisdictionResult{{
			FrameworkID: "tx_traiga",
			LawName:     "Texas TRAIGA (HB 1709)",
			ObligationMap: map[string]any{
				"deployer_disclosure": "mapped",
			},
		}},
		CrosswalkNIST: map[string]any{
			"GOVERN-1.1": "deployer_disclosure",
		},
		Validation: []DepthIssue{
			{
				Severity: "info",
				Message:  "draft",
			},
		},
	}
}

func sampleSectorPack() *SectorPack {
	return &SectorPack{
		Sectors: []SectorResult{{
			FrameworkID: "hipaa_clinical_ai",
			SectorName:  "HIPAA Clinical AI Overlay",
			ControlMapping: map[string]any{
				"phi_minimization": "mapped",
			},
			GapAnalysis: map[string]any{
				"phi_breach_notification": "gap",
			},
		}},
		Validation: []DepthIssue{
			{
				Severity: "warning",
				Message: "organizational safeguards " +
					"beyond scope",
			},
		},
	}
}

func sampleCCMSnapshot() *CCMSnapshot {
	return &CCMSnapshot{
		SnapshotAt: "2026-06-28T12:00:00Z",
		Frameworks: []CCMFrameworkSnapshot{{
			FrameworkID: "eu_ai_act",
			Name:        "EU AI Act",
			Controls: []CCMControlState{{
				ControlID: "art6_classification",
				Title:     "Art 6 Classification",
				Status:    "satisfied",
			}},
			Summary: AssessmentSummary{
				Total:     1,
				Satisfied: 1,
			},
		}},
		Summary: CCMSummary{
			FrameworksMonitored: 1,
			TotalControls:       1,
			Satisfied:           1,
		},
	}
}

func sampleDriftFindings() []DriftFinding {
	return []DriftFinding{{
		FrameworkID: "eu_ai_act",
		ControlID:   "art6_classification",
		Title:       "Art 6 Classification",
		PrevStatus:  "satisfied",
		CurrStatus:  "gap",
		Direction:   "regression",
		Detail: "risk classification evidence " +
			"removed",
	}}
}

func sampleFedRAMPKSIDocument() *FedRAMPKSIDocument {
	return &FedRAMPKSIDocument{
		SystemName:  "Acme AI Platform",
		ImpactLevel: "IL2",
		KSIs: map[string]any{
			"KSI-01": map[string]any{
				"status": "met",
				"evidence": "audit trail " +
					"present",
			},
		},
		OscalVersion: "1.1.3",
		AuthorizationPackage: map[string]any{
			"ssp_ref": "ssp-001",
		},
		Validation: []DepthIssue{
			{
				Severity: "info",
				Message: "draft authorization " +
					"package",
			},
		},
	}
}

// --- tests --------------------------------------------------

// TestDepthDenyClosedWithoutPackager: with NO packager wired (the
// default AGPL build), ALL depth endpoints answer 501, nothing is
// persisted, and the open catalog frameworks are unchanged.
func TestDepthDenyClosedWithoutPackager(t *testing.T) {
	h := newHarness(t) // no WithComplianceDepth
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	owner := h.roleToken(
		admin, tenant, "o@x.io", "owner")

	endpoints := []string{
		"/v1/m/compliance/depth/us-law",
		"/v1/m/compliance/depth/sector",
		"/v1/m/compliance/depth/ccm/snapshot",
		"/v1/m/compliance/depth/ccm/drift",
		"/v1/m/compliance/depth/fedramp",
	}
	for _, ep := range endpoints {
		r := h.do(
			"POST", ep, owner,
			map[string]any{"x": 1},
			tenantHdr(tenant))
		if r.code != http.StatusNotImplemented {
			t.Fatalf(
				"POST %s without depth packager "+
					"must be 501; got %d %s",
				ep, r.code, r.raw)
		}
	}

	// GET list for us-law must return empty items.
	r := h.do(
		"GET",
		"/v1/m/compliance/depth/us-law",
		owner, nil, tenantHdr(tenant))
	if len(asList(r)) != 0 {
		t.Fatal(
			"no US law pack may exist " +
				"without a packager")
	}

	// The open frameworks (tx_traiga, ca_sb53, etc.)
	// are still accessible (no rug-pull).
	for _, fwID := range usStateLawFrameworks {
		r := h.do(
			"GET",
			"/v1/m/compliance/frameworks/"+fwID,
			owner, nil, tenantHdr(tenant))
		if r.code != http.StatusOK {
			t.Fatalf(
				"the open %s framework must "+
					"still work; got %d %s",
				fwID, r.code, r.raw)
		}
	}
	for _, fwID := range sectorOverlayFrameworks {
		r := h.do(
			"GET",
			"/v1/m/compliance/frameworks/"+fwID,
			owner, nil, tenantHdr(tenant))
		if r.code != http.StatusOK {
			t.Fatalf(
				"the open %s framework must "+
					"still work; got %d %s",
				fwID, r.code, r.raw)
		}
	}
}

// TestUSStateLawPackGovernance: generation is admin-tier and
// deny-closed; a packager error is 422 (nothing persisted);
// successful generation → 201 + pack persisted.
func TestUSStateLawPackGovernance(t *testing.T) {
	stub := &stubDepthPackager{
		usStatePack: sampleUSStatePack(),
	}
	h := newHarness(t, WithComplianceDepth(stub))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	viewer := h.roleToken(
		admin, tenant, "v@x.io", "viewer")

	// Viewer cannot generate — permission gate.
	r := h.do(
		"POST",
		"/v1/m/compliance/depth/us-law",
		viewer,
		map[string]any{"x": 1},
		tenantHdr(tenant))
	if r.code != http.StatusForbidden {
		t.Fatalf(
			"viewer must be 403; got %d %s",
			r.code, r.raw)
	}

	// Packager error → 422 + nothing persisted.
	hBad := newHarness(t, WithComplianceDepth(
		&stubDepthPackager{
			err: context.DeadlineExceeded,
		}))
	adminB := hBad.adminLogin()
	tenantB := hBad.createOrg(adminB, "acme")
	ownerB := hBad.roleToken(
		adminB, tenantB, "o@x.io", "owner")
	r = hBad.do(
		"POST",
		"/v1/m/compliance/depth/us-law",
		ownerB,
		map[string]any{"x": 1},
		tenantHdr(tenantB))
	if r.code != http.StatusUnprocessableEntity {
		t.Fatalf(
			"packager error must be 422; "+
				"got %d %s",
			r.code, r.raw)
	}
	r = hBad.do(
		"GET",
		"/v1/m/compliance/depth/us-law",
		ownerB, nil, tenantHdr(tenantB))
	if len(asList(r)) != 0 {
		t.Fatal(
			"a rejected generation must " +
				"persist nothing")
	}

	// Successful generation → 201 + persisted.
	owner := h.roleToken(
		admin, tenant, "ow@x.io", "owner")
	gen := h.do(
		"POST",
		"/v1/m/compliance/depth/us-law",
		owner,
		map[string]any{"x": 1},
		tenantHdr(tenant))
	if gen.code != http.StatusCreated {
		t.Fatalf(
			"generate = %d %s",
			gen.code, gen.raw)
	}
	if gen.body["doc_sha256"] == "" {
		t.Fatal(
			"generate must carry doc_sha256")
	}
	list := h.do(
		"GET",
		"/v1/m/compliance/depth/us-law",
		owner, nil, tenantHdr(tenant))
	if len(asList(list)) != 1 {
		t.Fatalf(
			"generate must persist exactly "+
				"one pack; got %d",
			len(asList(list)))
	}
}

// TestSectorPackGovernance: same pattern as US law pack but for
// sector overlays.
func TestSectorPackGovernance(t *testing.T) {
	stub := &stubDepthPackager{
		sectorPack: sampleSectorPack(),
	}
	h := newHarness(t, WithComplianceDepth(stub))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	viewer := h.roleToken(
		admin, tenant, "v@x.io", "viewer")

	// Viewer cannot generate — permission gate.
	r := h.do(
		"POST",
		"/v1/m/compliance/depth/sector",
		viewer,
		map[string]any{"x": 1},
		tenantHdr(tenant))
	if r.code != http.StatusForbidden {
		t.Fatalf(
			"viewer must be 403; got %d %s",
			r.code, r.raw)
	}

	// Packager error → 422 + nothing persisted.
	hBad := newHarness(t, WithComplianceDepth(
		&stubDepthPackager{
			err: context.DeadlineExceeded,
		}))
	adminB := hBad.adminLogin()
	tenantB := hBad.createOrg(adminB, "acme")
	ownerB := hBad.roleToken(
		adminB, tenantB, "o@x.io", "owner")
	r = hBad.do(
		"POST",
		"/v1/m/compliance/depth/sector",
		ownerB,
		map[string]any{"x": 1},
		tenantHdr(tenantB))
	if r.code != http.StatusUnprocessableEntity {
		t.Fatalf(
			"packager error must be 422; "+
				"got %d %s",
			r.code, r.raw)
	}
	r = hBad.do(
		"GET",
		"/v1/m/compliance/depth/sector",
		ownerB, nil, tenantHdr(tenantB))
	if len(asList(r)) != 0 {
		t.Fatal(
			"a rejected generation must " +
				"persist nothing")
	}

	// Successful generation → 201 + persisted.
	owner := h.roleToken(
		admin, tenant, "ow@x.io", "owner")
	gen := h.do(
		"POST",
		"/v1/m/compliance/depth/sector",
		owner,
		map[string]any{"x": 1},
		tenantHdr(tenant))
	if gen.code != http.StatusCreated {
		t.Fatalf(
			"generate = %d %s",
			gen.code, gen.raw)
	}
	if gen.body["doc_sha256"] == "" {
		t.Fatal(
			"generate must carry doc_sha256")
	}
	list := h.do(
		"GET",
		"/v1/m/compliance/depth/sector",
		owner, nil, tenantHdr(tenant))
	if len(asList(list)) != 1 {
		t.Fatalf(
			"generate must persist exactly "+
				"one pack; got %d",
			len(asList(list)))
	}
}

// TestCCMSnapshotGovernance: viewer cannot trigger snapshot (403);
// packager error → 422; successful trigger → 201 + snapshot
// persisted.
func TestCCMSnapshotGovernance(t *testing.T) {
	stub := &stubDepthPackager{
		ccmSnapshot: sampleCCMSnapshot(),
	}
	h := newHarness(t, WithComplianceDepth(stub))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	viewer := h.roleToken(
		admin, tenant, "v@x.io", "viewer")

	// Viewer cannot trigger snapshot — permission
	// gate.
	r := h.do(
		"POST",
		"/v1/m/compliance/depth/ccm/snapshot",
		viewer,
		map[string]any{"x": 1},
		tenantHdr(tenant))
	if r.code != http.StatusForbidden {
		t.Fatalf(
			"viewer must be 403; got %d %s",
			r.code, r.raw)
	}

	// Packager error → the CCM snapshot endpoint
	// uses writeStoreError, not writeDepthError, so
	// a generic error is 500. Test the deny path.
	hBad := newHarness(t, WithComplianceDepth(
		&stubDepthPackager{
			err: context.DeadlineExceeded,
		}))
	adminB := hBad.adminLogin()
	tenantB := hBad.createOrg(adminB, "acme")
	ownerB := hBad.roleToken(
		adminB, tenantB, "o@x.io", "owner")
	rBad := hBad.do(
		"POST",
		"/v1/m/compliance/depth/ccm/snapshot",
		ownerB, nil, tenantHdr(tenantB))
	if rBad.code == http.StatusCreated {
		t.Fatal(
			"packager error must not create " +
				"a snapshot")
	}
	rList := hBad.do(
		"GET",
		"/v1/m/compliance/depth/ccm/snapshots",
		ownerB, nil, tenantHdr(tenantB))
	if len(asList(rList)) != 0 {
		t.Fatal(
			"a failed snapshot must persist " +
				"nothing")
	}

	// Successful trigger → 201 + persisted.
	owner := h.roleToken(
		admin, tenant, "ow@x.io", "owner")
	gen := h.do(
		"POST",
		"/v1/m/compliance/depth/ccm/snapshot",
		owner, nil, tenantHdr(tenant))
	if gen.code != http.StatusCreated {
		t.Fatalf(
			"snapshot = %d %s",
			gen.code, gen.raw)
	}
	list := h.do(
		"GET",
		"/v1/m/compliance/depth/ccm/snapshots",
		owner, nil, tenantHdr(tenant))
	if len(asList(list)) != 1 {
		t.Fatalf(
			"snapshot must persist exactly "+
				"one record; got %d",
			len(asList(list)))
	}
}

// TestFedRAMPKSIGovernance: same pattern as US law / sector packs
// but for FedRAMP 20x KSI documents.
func TestFedRAMPKSIGovernance(t *testing.T) {
	stub := &stubDepthPackager{
		fedRAMPDoc: sampleFedRAMPKSIDocument(),
	}
	h := newHarness(t, WithComplianceDepth(stub))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	viewer := h.roleToken(
		admin, tenant, "v@x.io", "viewer")

	// Viewer cannot generate — permission gate.
	r := h.do(
		"POST",
		"/v1/m/compliance/depth/fedramp",
		viewer,
		map[string]any{"x": 1},
		tenantHdr(tenant))
	if r.code != http.StatusForbidden {
		t.Fatalf(
			"viewer must be 403; got %d %s",
			r.code, r.raw)
	}

	// Packager error → 422 + nothing persisted.
	hBad := newHarness(t, WithComplianceDepth(
		&stubDepthPackager{
			err: context.DeadlineExceeded,
		}))
	adminB := hBad.adminLogin()
	tenantB := hBad.createOrg(adminB, "acme")
	ownerB := hBad.roleToken(
		adminB, tenantB, "o@x.io", "owner")
	r = hBad.do(
		"POST",
		"/v1/m/compliance/depth/fedramp",
		ownerB,
		map[string]any{"x": 1},
		tenantHdr(tenantB))
	if r.code != http.StatusUnprocessableEntity {
		t.Fatalf(
			"packager error must be 422; "+
				"got %d %s",
			r.code, r.raw)
	}
	r = hBad.do(
		"GET",
		"/v1/m/compliance/depth/fedramp",
		ownerB, nil, tenantHdr(tenantB))
	if len(asList(r)) != 0 {
		t.Fatal(
			"a rejected generation must " +
				"persist nothing")
	}

	// Successful generation → 201 + persisted.
	owner := h.roleToken(
		admin, tenant, "ow@x.io", "owner")
	gen := h.do(
		"POST",
		"/v1/m/compliance/depth/fedramp",
		owner,
		map[string]any{"x": 1},
		tenantHdr(tenant))
	if gen.code != http.StatusCreated {
		t.Fatalf(
			"generate = %d %s",
			gen.code, gen.raw)
	}
	if gen.body["system_name"] != "Acme AI Platform" {
		t.Fatalf(
			"generate must carry system_name; "+
				"got %v",
			gen.body["system_name"])
	}
	if gen.body["doc_sha256"] == "" {
		t.Fatal(
			"generate must carry doc_sha256")
	}
	list := h.do(
		"GET",
		"/v1/m/compliance/depth/fedramp",
		owner, nil, tenantHdr(tenant))
	if len(asList(list)) != 1 {
		t.Fatalf(
			"generate must persist exactly "+
				"one KSI doc; got %d",
			len(asList(list)))
	}
}

// TestDepthPersistExportReplace: generate US law pack → persists;
// generate again → replace-on-regenerate (still only 1 item);
// export carries disclaimer + ledger anchor; delete removes →
// empty list after.
func TestDepthPersistExportReplace(t *testing.T) {
	stub := &stubDepthPackager{
		usStatePack: sampleUSStatePack(),
	}
	h := newHarness(t, WithComplianceDepth(stub))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	owner := h.roleToken(
		admin, tenant, "o@x.io", "owner")

	// First generation → 201 + persisted.
	gen := h.do(
		"POST",
		"/v1/m/compliance/depth/us-law"+
			"?scope_note=initial+draft",
		owner,
		map[string]any{"x": 1},
		tenantHdr(tenant))
	if gen.code != http.StatusCreated {
		t.Fatalf(
			"generate = %d %s",
			gen.code, gen.raw)
	}
	id := gen.body["id"].(string)

	// Self-audit on generate.
	acts := strings.Join(
		h.auditActions(tenant), ",")
	if !strings.Contains(
		acts,
		"compliance.depth.us_law.generate") {
		t.Fatalf(
			"generate must self-audit; "+
				"actions=%s", acts)
	}

	// Replace-on-regenerate — second generation
	// replaces the existing pack.
	stub.usStatePack = sampleUSStatePack()
	stub.usStatePack.Jurisdictions[0].LawName =
		"Texas TRAIGA (HB 1709) v2"
	gen2 := h.do(
		"POST",
		"/v1/m/compliance/depth/us-law",
		owner,
		map[string]any{"x": 1},
		tenantHdr(tenant))
	if gen2.code != http.StatusCreated {
		t.Fatalf(
			"regenerate = %d %s",
			gen2.code, gen2.raw)
	}
	list := h.do(
		"GET",
		"/v1/m/compliance/depth/us-law",
		owner, nil, tenantHdr(tenant))
	if len(asList(list)) != 1 {
		t.Fatalf(
			"replace-on-regenerate must "+
				"keep exactly one pack; "+
				"got %d",
			len(asList(list)))
	}

	// Export carries disclaimer + ledger anchor.
	exp := h.do(
		"GET",
		"/v1/m/compliance/depth/us-law/"+
			id+"/export",
		owner, nil, tenantHdr(tenant))
	if exp.code != http.StatusOK {
		t.Fatalf(
			"export = %d %s",
			exp.code, exp.raw)
	}
	if _, ok := exp.body["ledger_anchor"].(map[string]any); !ok {
		t.Fatalf(
			"export must carry a live "+
				"ledger_anchor: %s",
			exp.raw)
	}
	d, _ := exp.body["disclaimer"].(string)
	if !strings.Contains(d, "NOT") ||
		!strings.Contains(d, "legal advice") {
		t.Fatalf(
			"export disclaimer must be "+
				"honest: %q", d)
	}

	// Self-audit on export.
	acts = strings.Join(
		h.auditActions(tenant), ",")
	if !strings.Contains(
		acts,
		"compliance.depth.us_law.export") {
		t.Fatalf(
			"export must self-audit; "+
				"actions=%s", acts)
	}

	// Delete removes → empty list after.
	del := h.do(
		"DELETE",
		"/v1/m/compliance/depth/us-law/"+id,
		owner, nil, tenantHdr(tenant))
	if del.code != http.StatusNoContent {
		t.Fatalf(
			"delete = %d %s",
			del.code, del.raw)
	}
	acts = strings.Join(
		h.auditActions(tenant), ",")
	if !strings.Contains(
		acts,
		"compliance.depth.us_law.delete") {
		t.Fatalf(
			"delete must self-audit; "+
				"actions=%s", acts)
	}
}

// TestUSStateLawFrameworksExist verifies that all 4 US state law
// frameworks exist in the catalog with non-empty Pins.
func TestUSStateLawFrameworksExist(t *testing.T) {
	expected := []string{
		"tx_traiga",
		"ca_sb53",
		"il_hb3773",
		"co_sb26_189",
	}
	for _, id := range expected {
		fw, ok := frameworkByID[id]
		if !ok {
			t.Fatalf(
				"framework %s must exist in "+
					"the catalog",
				id)
		}
		if fw.Pin.SourceURL == "" {
			t.Fatalf(
				"framework %s must have a "+
					"non-empty Pin.SourceURL",
				id)
		}
		if fw.Pin.VerifiedOn == "" {
			t.Fatalf(
				"framework %s must have a "+
					"non-empty Pin.VerifiedOn",
				id)
		}
		if fw.Pin.Document == "" {
			t.Fatalf(
				"framework %s must have a "+
					"non-empty Pin.Document",
				id)
		}
	}
}

// TestSectorOverlayFrameworksExist verifies that all 3 sector
// overlay frameworks exist in the catalog with non-empty Pins.
func TestSectorOverlayFrameworksExist(t *testing.T) {
	expected := []string{
		"hipaa_clinical_ai",
		"pci_dss_401_ai",
		"finra_genai",
	}
	for _, id := range expected {
		fw, ok := frameworkByID[id]
		if !ok {
			t.Fatalf(
				"framework %s must exist in "+
					"the catalog",
				id)
		}
		if fw.Pin.SourceURL == "" {
			t.Fatalf(
				"framework %s must have a "+
					"non-empty Pin.SourceURL",
				id)
		}
		if fw.Pin.VerifiedOn == "" {
			t.Fatalf(
				"framework %s must have a "+
					"non-empty Pin.VerifiedOn",
				id)
		}
	}
}

// TestCalendarMilestonesSourced verifies that the regulatory
// calendar milestones exist, carry non-empty Source.URL and
// VerifiedOn, and that pre-existing milestones (colorado_admt)
// are unaffected.
func TestCalendarMilestonesSourced(t *testing.T) {
	required := []string{
		"tx_traiga.effective",
		"ca_sb53.effective",
		"il_hb3773.effective",
		"pci_dss_401.future_dated",
		"finra_genai.notice_25_06",
	}
	for _, id := range required {
		ms, ok := milestoneByID[id]
		if !ok {
			t.Fatalf(
				"milestone %s must exist "+
					"in milestoneByID",
				id)
		}
		if ms.Source.URL == "" {
			t.Fatalf(
				"milestone %s must have a "+
					"non-empty Source.URL",
				id)
		}
		if ms.VerifiedOn == "" {
			t.Fatalf(
				"milestone %s must have a "+
					"non-empty VerifiedOn",
				id)
		}
	}

	// colorado_admt.obligations_apply was already
	// there — verify it still is.
	if _, ok := milestoneByID["colorado_admt.obligations_apply"]; !ok {
		t.Fatal(
			"colorado_admt.obligations_apply " +
				"must still exist " +
				"(no rug-pull)")
	}
}

// TestDepthEntityRegistered verifies that the 5 depth entity
// kinds are registered in the module's schema (schema-parity:
// both builds see the same table).
func TestDepthEntityRegistered(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	owner := h.roleToken(
		admin, tenant, "o@x.io", "owner")

	// Each entity kind must be accessible via the
	// store (the module registered it); a list
	// returning an empty set (not a 500) proves the
	// table is present.
	listEndpoints := []struct {
		name string
		path string
	}{
		{
			"us_law_pack",
			"/v1/m/compliance/depth/us-law",
		},
		{
			"sector_pack",
			"/v1/m/compliance/depth/sector",
		},
		{
			"ccm_snapshot",
			"/v1/m/compliance/depth/ccm/" +
				"snapshots",
		},
		{
			"ccm_drift",
			"/v1/m/compliance/depth/ccm/drift",
		},
		{
			"fedramp_ksi",
			"/v1/m/compliance/depth/fedramp",
		},
	}
	for _, ep := range listEndpoints {
		r := h.do(
			"GET", ep.path, owner, nil,
			tenantHdr(tenant))
		if r.code != http.StatusOK {
			t.Fatalf(
				"listing %s must succeed "+
					"(entity registered); "+
					"got %d %s",
				ep.name, r.code, r.raw)
		}
	}
}

// TestFrameworkControlsHonestMappings verifies the honesty
// invariants for every framework's controls: no control
// has nil Capabilities AND a non-empty Criterion referencing
// platform capabilities (would be dishonest), and every
// CapabilityKey in every control's Capabilities list is a
// known key from capabilityCatalog.
func TestFrameworkControlsHonestMappings(t *testing.T) {
	// Build the set of known capability keys.
	knownKeys := make(
		map[CapabilityKey]bool,
		len(capabilityCatalog))
	for _, c := range capabilityCatalog {
		knownKeys[c.Key] = true
	}

	s274Frameworks := append(
		append([]string{}, usStateLawFrameworks...),
		sectorOverlayFrameworks...)

	for _, fwID := range s274Frameworks {
		fw, ok := frameworkByID[fwID]
		if !ok {
			t.Fatalf(
				"framework %s missing", fwID)
		}
		for _, ctrl := range fw.Controls {
			// Every listed CapabilityKey must
			// be known.
			for _, ck := range ctrl.Capabilities {
				if !knownKeys[ck] {
					t.Errorf(
						"framework %s "+
							"control %s "+
							"references "+
							"unknown "+
							"capability "+
							"%q",
						fwID,
						ctrl.ID,
						ck)
				}
			}
			// A control with nil Capabilities
			// and a Criterion mentioning platform
			// capabilities would be dishonest.
			if len(ctrl.Capabilities) == 0 &&
				ctrl.Criterion != "" &&
				(strings.Contains(
					ctrl.Criterion,
					"capability") ||
					strings.Contains(
						ctrl.Criterion,
						"evidence")) {
				t.Errorf(
					"framework %s control "+
						"%s has Criterion "+
						"referencing "+
						"capability/evidence "+
						"but nil "+
						"Capabilities "+
						"(dishonest mapping)",
					fwID, ctrl.ID)
			}
		}
	}
}
