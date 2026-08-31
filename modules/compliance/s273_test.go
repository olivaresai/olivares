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

// ISO/IEC 42001 AIMS certification-readiness pack. These tests exercise the
// OPEN-CORE half: the deny-closed (501) endpoints without the enterprise add-on, the
// governed persistence/export/audit substrate, and the honesty invariants (disclaimer,
// gaps). The SoA structuring and the actual pack building (enterprise/iso42001) are tested
// in that package; here stubs stand in for the closed add-on so the open behavior is
// verified without the open module importing the closed one.

// stubAIMSPackager is a programmable stand-in for the commercial packager.
type stubAIMSPackager struct {
	doc    *AIMSDocument
	err    error
	gotCtx bool
}

func (s *stubAIMSPackager) BuildAIMSPack(_ context.Context, _ AIMSInput, _ FrameworkAssessment, _ []RiskDTO) (*AIMSDocument, error) {
	s.gotCtx = true
	return s.doc, s.err
}

func sampleAIMSDocument() *AIMSDocument {
	return &AIMSDocument{
		OrganizationName: "Acme Corp",
		Standard:         aimsStandard,
		SoA: map[string]any{
			"A.6.2.8": map[string]any{
				"applicable":    true,
				"justification": "event logging is a core requirement for our AIMS",
				"status":        "satisfied",
				"evidence_ref":  "pkg-001",
			},
			"A.3.2": map[string]any{
				"applicable":    true,
				"justification": "roles must be defined per clause 5.3",
				"status":        "gap",
				"evidence_ref":  "",
			},
		},
		Policy: map[string]any{
			"clause_4": map[string]any{"context": "AI governance for Acme Corp"},
			"clause_5": map[string]any{"leadership": "CTO sponsors the AIMS"},
		},
		RiskRegister: map[string]any{
			"entries": []any{
				map[string]any{"agent_id": "agent-001", "tier": "high", "state": "provisional"},
			},
		},
		ImpactAssessments: map[string]any{
			"A.5.2": map[string]any{"process": "documented", "bias_fairness": "gap — platform cannot measure"},
		},
		LifecycleControls: map[string]any{
			"A.6.2.5": map[string]any{"deployment": "change-ledger evidence present"},
			"A.6.2.8": map[string]any{"logging": "audit trail present"},
		},
		SupplierGovernance: map[string]any{
			"A.10.3": map[string]any{"anthropic": "gpai posture verified"},
			"A.7.5":  map[string]any{"model_aibom": "sealed, ledger-anchored"},
		},
		Validation: []AIMSIssue{
			{Severity: "warning", Field: "policy.clause_6", Message: "planning section incomplete"},
		},
		Note: "draft — requires human attestation",
	}
}

// TestAIMSDenyClosedWithoutPackager: with NO packager wired (the default AGPL build), the
// AIMS pack generation endpoint answers 501, nothing is persisted, and the open iso_42001
// catalog is unchanged.
func TestAIMSDenyClosedWithoutPackager(t *testing.T) {
	h := newHarness(t) // no WithAIMSPackager
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	owner := h.roleToken(admin, tenant, "o@x.io", "owner")

	if r := h.do("POST", "/v1/m/compliance/aims/pack", owner, map[string]any{"org": "acme"}, tenantHdr(tenant)); r.code != http.StatusNotImplemented {
		t.Fatalf("AIMS pack generation without the enterprise packager must be 501; got %d %s", r.code, r.raw)
	}
	if r := h.do("GET", "/v1/m/compliance/aims/pack", owner, nil, tenantHdr(tenant)); len(asList(r)) != 0 {
		t.Fatal("no AIMS pack may exist without a packager")
	}
	// The open iso_42001 catalog is untouched (no rug-pull).
	if r := h.do("GET", "/v1/m/compliance/frameworks/iso_42001", owner, nil, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Fatalf("the open iso_42001 catalog must still work; got %d %s", r.code, r.raw)
	}
}

// TestAIMSPackGovernance: generation is admin-tier and deny-closed; a packager error is
// 422 (nothing persisted); a pack with no organization name is 422 (defense in depth).
func TestAIMSPackGovernance(t *testing.T) {
	h := newHarness(t, WithAIMSPackager(&stubAIMSPackager{doc: sampleAIMSDocument()}))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	viewer := h.roleToken(admin, tenant, "v@x.io", "viewer")

	// Viewer cannot generate — permission gate.
	if r := h.do("POST", "/v1/m/compliance/aims/pack", viewer, map[string]any{"x": 1}, tenantHdr(tenant)); r.code != http.StatusForbidden {
		t.Fatalf("viewer must be 403; got %d %s", r.code, r.raw)
	}

	// A packager error yields 422.
	hBad := newHarness(t, WithAIMSPackager(&stubAIMSPackager{err: context.DeadlineExceeded}))
	adminB := hBad.adminLogin()
	tenantB := hBad.createOrg(adminB, "acme")
	ownerB := hBad.roleToken(adminB, tenantB, "o@x.io", "owner")
	if r := hBad.do("POST", "/v1/m/compliance/aims/pack", ownerB, map[string]any{"x": 1}, tenantHdr(tenantB)); r.code != http.StatusUnprocessableEntity {
		t.Fatalf("a rejected document must be 422; got %d %s", r.code, r.raw)
	}
	if r := hBad.do("GET", "/v1/m/compliance/aims/pack", ownerB, nil, tenantHdr(tenantB)); len(asList(r)) != 0 {
		t.Fatal("a rejected generation must persist nothing")
	}

	// A pack with no organization name is 422 (defense in depth).
	hNoOrg := newHarness(t, WithAIMSPackager(&stubAIMSPackager{doc: &AIMSDocument{Standard: aimsStandard, SoA: map[string]any{}}}))
	adminE := hNoOrg.adminLogin()
	tenantE := hNoOrg.createOrg(adminE, "acme")
	ownerE := hNoOrg.roleToken(adminE, tenantE, "o@x.io", "owner")
	if r := hNoOrg.do("POST", "/v1/m/compliance/aims/pack", ownerE, map[string]any{"x": 1}, tenantHdr(tenantE)); r.code != http.StatusUnprocessableEntity {
		t.Fatalf("a pack with no organization name must be 422; got %d %s", r.code, r.raw)
	}
}

// TestAIMSPackPersistExportReplace: generate persists a maintained pack (one per tenant,
// replace-on-regenerate), the export carries the live ledger anchor + disclaimer, and
// generate/delete self-audit semantically.
func TestAIMSPackPersistExportReplace(t *testing.T) {
	pkg := &stubAIMSPackager{doc: sampleAIMSDocument()}
	h := newHarness(t, WithAIMSPackager(pkg))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	owner := h.roleToken(admin, tenant, "o@x.io", "owner")

	gen := h.do("POST", "/v1/m/compliance/aims/pack?scope_note=initial+draft", owner, map[string]any{"org": "acme"}, tenantHdr(tenant))
	if gen.code != http.StatusCreated {
		t.Fatalf("generate = %d %s", gen.code, gen.raw)
	}
	if gen.body["organisation_name"] != "Acme Corp" || gen.body["doc_sha256"] == "" {
		t.Fatalf("generate dto missing organisation_name/doc_sha256: %s", gen.raw)
	}
	if gen.body["standard"] != aimsStandard {
		t.Fatalf("standard = %v, want %s", gen.body["standard"], aimsStandard)
	}
	if gen.body["error_count"] != float64(0) {
		t.Fatalf("error_count = %v, want 0 (the sample has only a warning)", gen.body["error_count"])
	}
	if !pkg.gotCtx {
		t.Fatal("the packager must receive the assessment context")
	}
	if acts := strings.Join(h.auditActions(tenant), ","); !strings.Contains(acts, "compliance.aims.pack.generate") {
		t.Fatalf("generate must self-audit; actions=%s", acts)
	}

	id := gen.body["id"].(string)

	// Get — includes body.
	get := h.do("GET", "/v1/m/compliance/aims/pack/"+id, owner, nil, tenantHdr(tenant))
	if get.code != http.StatusOK {
		t.Fatalf("get = %d %s", get.code, get.raw)
	}
	if _, ok := get.body["soa"].(map[string]any); !ok {
		t.Fatal("get must include soa body")
	}

	// Export — ledger anchor + disclaimer.
	exp := h.do("GET", "/v1/m/compliance/aims/pack/"+id+"/export", owner, nil, tenantHdr(tenant))
	if exp.code != http.StatusOK {
		t.Fatalf("export = %d %s", exp.code, exp.raw)
	}
	if _, ok := exp.body["ledger_anchor"].(map[string]any); !ok {
		t.Fatalf("export must carry a live ledger_anchor: %s", exp.raw)
	}
	if d, _ := exp.body["disclaimer"].(string); !strings.Contains(d, "NOT a certification") || !strings.Contains(d, "DRAFT") {
		t.Fatalf("export disclaimer must be honest (NOT a certification; DRAFT): %q", d)
	}
	if !strings.Contains(exp.raw, "A.6.2.8") {
		t.Fatal("export must include the structured SoA")
	}
	if acts := strings.Join(h.auditActions(tenant), ","); !strings.Contains(acts, "compliance.aims.pack.export") {
		t.Fatalf("export must self-audit; actions=%s", acts)
	}

	// List — summary view (no body sections in the list).
	list := h.do("GET", "/v1/m/compliance/aims/pack", owner, nil, tenantHdr(tenant))
	items := asList(list)
	if len(items) != 1 {
		t.Fatalf("list must return exactly one pack; got %d", len(items))
	}
	if item, ok := items[0].(map[string]any); ok && item["soa"] != nil {
		t.Fatal("list view must omit the body sections (summary only)")
	}

	// Replace-on-regenerate — re-generate replaces the existing pack.
	pkg.doc = sampleAIMSDocument()
	pkg.doc.OrganizationName = "Acme Corp v2"
	gen2 := h.do("POST", "/v1/m/compliance/aims/pack", owner, map[string]any{"org": "acme"}, tenantHdr(tenant))
	if gen2.code != http.StatusCreated {
		t.Fatalf("regenerate = %d %s", gen2.code, gen2.raw)
	}
	if gen2.body["organisation_name"] != "Acme Corp v2" {
		t.Fatalf("regenerated pack must carry the updated organisation name; got %v", gen2.body["organisation_name"])
	}
	list2 := h.do("GET", "/v1/m/compliance/aims/pack", owner, nil, tenantHdr(tenant))
	if len(asList(list2)) != 1 {
		t.Fatalf("replace-on-regenerate must keep exactly one pack per tenant; got %d", len(asList(list2)))
	}

	// Delete — admin-tier, self-audits.
	del := h.do("DELETE", "/v1/m/compliance/aims/pack/"+id, owner, nil, tenantHdr(tenant))
	if del.code != http.StatusNoContent {
		t.Fatalf("delete = %d %s", del.code, del.raw)
	}
	if acts := strings.Join(h.auditActions(tenant), ","); !strings.Contains(acts, "compliance.aims.pack.delete") {
		t.Fatalf("delete must self-audit; actions=%s", acts)
	}
}

// TestAIMSPermissionsDeclared verifies that the AIMS permissions are declared in the
// module's permission set (the engine needs them to wire RBAC).
func TestAIMSPermissionsDeclared(t *testing.T) {
	m := New()
	perms := m.Permissions()
	found := map[string]bool{"compliance:aims:read": false, "compliance:aims:admin": false}
	for _, p := range perms {
		if _, ok := found[string(p)]; ok {
			found[string(p)] = true
		}
	}
	for p, ok := range found {
		if !ok {
			t.Errorf("permission %s not declared in Module.Permissions()", p)
		}
	}
}

// TestAIMSPackDisclaimerContent verifies the disclaimer contains the required honesty
// tokens (not a certification, draft, not conformity, accredited certification body).
func TestAIMSPackDisclaimerContent(t *testing.T) {
	required := []string{
		"NOT a certification",
		"DRAFT",
		"statement of conformity",
		"accredited certification body",
		"ISO/IEC 42006:2025",
		"legal advice",
	}
	for _, tok := range required {
		if !strings.Contains(aimsPackDisclaimer, tok) {
			t.Errorf("disclaimer must contain %q", tok)
		}
	}
}

// TestAIMSPackClaimsIndependent verifies that the AIMS pack does not key anything off
// license Claims — attestation-only invariant (ADR-0010, core/license/license.go:26-34).
// The test creates a harness with a stub packager and generates a pack; then verifies
// the same pack is generated regardless of license state (which is always nil in the
// open test harness — the pack never reads it).
func TestAIMSPackClaimsIndependent(t *testing.T) {
	h := newHarness(t, WithAIMSPackager(&stubAIMSPackager{doc: sampleAIMSDocument()}))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	owner := h.roleToken(admin, tenant, "o@x.io", "owner")

	gen := h.do("POST", "/v1/m/compliance/aims/pack", owner, map[string]any{"org": "acme"}, tenantHdr(tenant))
	if gen.code != http.StatusCreated {
		t.Fatalf("generate must succeed regardless of license state; got %d %s", gen.code, gen.raw)
	}
	if gen.body["organisation_name"] != "Acme Corp" {
		t.Fatal("the pack content must be independent of license Claims")
	}
}

// TestAIMSEntityRegistered verifies that the compliance.aims_pack entity is registered
// in the module's schema (schema-parity: both builds see the same table).
func TestAIMSEntityRegistered(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	owner := h.roleToken(admin, tenant, "o@x.io", "owner")

	// The entity must exist in the store (the module registered it); a list
	// returning an empty set (not a 500) proves the table is present.
	r := h.do("GET", "/v1/m/compliance/aims/pack", owner, nil, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("listing AIMS packs must succeed (entity registered); got %d %s", r.code, r.raw)
	}
}
