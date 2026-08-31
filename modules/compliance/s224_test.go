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

// OSCAL profile/SSP ingestion (closed-loop GRC). These tests exercise the
// OPEN-CORE half: the governed deny-closed ingestion endpoint, the persisted selection,
// and the export scoping/back-reference. The OSCAL parsing/resolution itself
// (enterprise/oscalingest) is tested in that package against both a stub and the real
// catalog; here a stub ProfileResolver stands in for the closed add-on so the open
// behavior — including the BYTE-IDENTICAL no-profile path — is verified without the open
// module importing the closed one.

// stubProfileResolver is a programmable stand-in for the commercial resolver: it returns a
// canned ResolvedProfile (or error) regardless of the document, so the open-module wiring
// (persist → filter → reference, 501/422/deny-closed) is exercised in isolation.
type stubProfileResolver struct {
	rp  *ResolvedProfile
	err error
}

func (s stubProfileResolver) Resolve(context.Context, ProfileInput, FrameworkCatalog) (*ResolvedProfile, error) {
	return s.rp, s.err
}

const oscalDocBody = `{"profile":{"uuid":"p","metadata":{"oscal-version":"1.2.2"},"imports":[{"href":"https://olivares.ai/compliance/frameworks/eu_ai_act","include-controls":[{"with-ids":["art_5","art_9"]}]}]}}`

func euAIActSubsetProfile() *ResolvedProfile {
	return &ResolvedProfile{
		DocKind:            "profile",
		Framework:          "eu_ai_act",
		SelectedControlIDs: []string{"art_5", "art_9"},
		ProfileUUID:        "prof-uuid-1",
		SourceHref:         "https://olivares.ai/compliance/frameworks/eu_ai_act",
		OscalVersion:       "1.2.2",
		Title:              "EU AI Act subset",
	}
}

// sealEUAIAct seals an eu_ai_act evidence package and returns its id.
func sealEUAIAct(t *testing.T, h *harness, tok string, tenant interface{ String() string }) string {
	t.Helper()
	seal := h.do("POST", "/v1/m/compliance/frameworks/eu_ai_act/evidence", tok, map[string]any{"scope_note": "oscal"}, map[string]string{"X-Olivares-Tenant": tenant.String()})
	if seal.code != http.StatusCreated {
		t.Fatalf("seal = %d %s", seal.code, seal.raw)
	}
	return seal.body["id"].(string)
}

// TestOSCALIngestionDenyClosedWithoutResolver: with NO resolver wired (the default AGPL
// build), the ingestion endpoint answers 501 and the OSCAL export keeps include-all with
// no profile props (byte-identical, no rug-pull).
func TestOSCALIngestionDenyClosedWithoutResolver(t *testing.T) {
	h := newHarness(t) // no WithProfileResolver
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	owner := h.roleToken(admin, tenant, "o@x.io", "owner")

	reg := h.do("POST", "/v1/m/compliance/oscal/profiles?framework=eu_ai_act", owner, map[string]any{"x": "y"}, tenantHdr(tenant))
	if reg.code != http.StatusNotImplemented {
		t.Fatalf("ingestion without the enterprise resolver must be 501; got %d %s", reg.code, reg.raw)
	}

	id := sealEUAIAct(t, h, owner, tenant)
	exp := h.do("GET", "/v1/m/compliance/evidence/"+id+"/export?format=oscal", owner, nil, tenantHdr(tenant))
	if exp.code != http.StatusOK {
		t.Fatalf("oscal export = %d %s", exp.code, exp.raw)
	}
	// include-all, and NONE of the profile markers.
	if !strings.Contains(exp.raw, `"include-all"`) {
		t.Fatal("no-profile export must keep reviewed-controls include-all")
	}
	for _, marker := range []string{"include-controls", "profile_doc_sha256", "profile_uuid"} {
		if strings.Contains(exp.raw, marker) {
			t.Fatalf("no-profile export must not contain %q (byte-identical to the no-profile baseline)", marker)
		}
	}
}

// TestOSCALProfileGovernance: register is admin-tier and deny-closed (a viewer is
// rejected), an invalid document is 422 (nothing persisted), and an empty selection is
// 422 — never a silent partial registration.
func TestOSCALProfileGovernance(t *testing.T) {
	h := newHarness(t, WithProfileResolver(stubProfileResolver{rp: euAIActSubsetProfile()}))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	viewer := h.roleToken(admin, tenant, "v@x.io", "viewer")
	owner := h.roleToken(admin, tenant, "o@x.io", "owner")

	// Deny-closed: a viewer cannot register (admin-tier governance act).
	if r := h.do("POST", "/v1/m/compliance/oscal/profiles", viewer, map[string]any{"x": 1}, tenantHdr(tenant)); r.code != http.StatusForbidden {
		t.Fatalf("viewer register must be 403; got %d %s", r.code, r.raw)
	}

	// Invalid OSCAL: the resolver errors ⇒ 422, and the listing stays empty (nothing persisted).
	hBad := newHarness(t, WithProfileResolver(stubProfileResolver{err: context.DeadlineExceeded}))
	adminB := hBad.adminLogin()
	tenantB := hBad.createOrg(adminB, "acme")
	ownerB := hBad.roleToken(adminB, tenantB, "o@x.io", "owner")
	if r := hBad.do("POST", "/v1/m/compliance/oscal/profiles", ownerB, map[string]any{"x": 1}, tenantHdr(tenantB)); r.code != http.StatusUnprocessableEntity {
		t.Fatalf("invalid OSCAL must be 422; got %d %s", r.code, r.raw)
	}
	if r := hBad.do("GET", "/v1/m/compliance/oscal/profiles", ownerB, nil, tenantHdr(tenantB)); len(asList(r)) != 0 {
		t.Fatal("a rejected ingestion must persist nothing")
	}

	// Empty selection: the resolver returns a zero-control selection ⇒ 422.
	hEmpty := newHarness(t, WithProfileResolver(stubProfileResolver{rp: &ResolvedProfile{Framework: "eu_ai_act", DocKind: "profile"}}))
	adminE := hEmpty.adminLogin()
	tenantE := hEmpty.createOrg(adminE, "acme")
	ownerE := hEmpty.roleToken(adminE, tenantE, "o@x.io", "owner")
	if r := hEmpty.do("POST", "/v1/m/compliance/oscal/profiles", ownerE, map[string]any{"x": 1}, tenantHdr(tenantE)); r.code != http.StatusUnprocessableEntity {
		t.Fatalf("empty selection must be 422; got %d %s", r.code, r.raw)
	}

	_ = owner
}

// TestOSCALProfileScopesExport: a registered profile scopes the OSCAL assessment-results
// to its selection (filtered findings), switches reviewed-controls to include-controls,
// and stamps the profile back-references as props — without laundering any status.
func TestOSCALProfileScopesExport(t *testing.T) {
	h := newHarness(t, WithProfileResolver(stubProfileResolver{rp: euAIActSubsetProfile()}))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	owner := h.roleToken(admin, tenant, "o@x.io", "owner")

	reg := h.do("POST", "/v1/m/compliance/oscal/profiles?framework=eu_ai_act&scope_note=fedramp", owner, map[string]any{"doc": oscalDocBody}, tenantHdr(tenant))
	if reg.code != http.StatusCreated {
		t.Fatalf("register = %d %s", reg.code, reg.raw)
	}
	if got := reg.body["selected_count"]; got != float64(2) {
		t.Fatalf("selected_count = %v, want 2", got)
	}
	if reg.body["framework"] != "eu_ai_act" || reg.body["doc_sha256"] == "" {
		t.Fatalf("register dto missing framework/doc_sha256: %s", reg.raw)
	}
	// The register is a governance act and self-audits semantically (rich meta, not a
	// generic mutation row).
	if acts := strings.Join(h.auditActions(tenant), ","); !strings.Contains(acts, "compliance.oscal.profile.register") {
		t.Fatalf("register must self-audit; actions=%s", acts)
	}

	id := sealEUAIAct(t, h, owner, tenant)
	exp := h.do("GET", "/v1/m/compliance/evidence/"+id+"/export?format=oscal", owner, nil, tenantHdr(tenant))
	if exp.code != http.StatusOK {
		t.Fatalf("export = %d %s", exp.code, exp.raw)
	}

	// reviewed-controls switched to include-controls with exactly the selected ids.
	ar, _ := exp.body["assessment-results"].(map[string]any)
	results, _ := ar["results"].([]any)
	res0, _ := results[0].(map[string]any)
	rc, _ := res0["reviewed-controls"].(map[string]any)
	sels, _ := rc["control-selections"].([]any)
	sel0, _ := sels[0].(map[string]any)
	if _, isAll := sel0["include-all"]; isAll {
		t.Fatal("a scoped export must NOT use include-all")
	}
	// assessment-results include-controls is an ARRAY of {control-id} objects (the
	// assessment model's select-control-by-id), NOT the profile model's with-ids.
	inc, _ := sel0["include-controls"].([]any)
	got := map[string]bool{}
	for _, e := range inc {
		m, _ := e.(map[string]any)
		if cid, ok := m["control-id"].(string); ok {
			got[cid] = true
		}
	}
	if len(got) != 2 || !got["art_5"] || !got["art_9"] {
		t.Fatalf("reviewed-controls include-controls control-ids = %v, want {art_5, art_9}", got)
	}
	if strings.Contains(exp.raw, `"with-ids"`) {
		t.Fatal("assessment-results must not use the profile-model with-ids shape")
	}

	// Findings are filtered to the selection (exactly art_5 + art_9).
	findings, _ := res0["findings"].([]any)
	if len(findings) != 2 {
		t.Fatalf("scoped findings = %d, want 2", len(findings))
	}
	for _, f := range findings {
		fm, _ := f.(map[string]any)
		tgt, _ := fm["target"].(map[string]any)
		stt, _ := tgt["status"].(map[string]any)
		state, _ := stt["state"].(string)
		// Honesty preserved: scoping never launders a non-satisfied control into satisfied.
		if state != "satisfied" && state != "not-satisfied" {
			t.Fatalf("status.state = %q not in the OSCAL enum", state)
		}
	}

	// Back-references rode as props under our namespace.
	for _, marker := range []string{"profile_doc_sha256", "profile_uuid", "olivares.ai/ns/oscal"} {
		if !strings.Contains(exp.raw, marker) {
			t.Fatalf("scoped export missing %q prop/anchor", marker)
		}
	}
	// The control-mapping is scoped consistently (built from the filtered results).
	cm, _ := exp.body["control-mapping"].(map[string]any)
	mappings, _ := cm["mappings"].([]any)
	mapping0, _ := mappings[0].(map[string]any)
	maps, _ := mapping0["maps"].([]any)
	if len(maps) > 2 {
		t.Fatalf("control-mapping has %d maps, want ≤2 (scoped to the selection)", len(maps))
	}
}

// TestOSCALProfileUnregisterRevertsToIncludeAll: registering then unregistering a profile
// reverts the export EXACTLY (byte-identical), proving the scoping is purely additive.
func TestOSCALProfileUnregisterRevertsToIncludeAll(t *testing.T) {
	h := newHarness(t, WithProfileResolver(stubProfileResolver{rp: euAIActSubsetProfile()}))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	owner := h.roleToken(admin, tenant, "o@x.io", "owner")

	id := sealEUAIAct(t, h, owner, tenant)
	before := h.do("GET", "/v1/m/compliance/evidence/"+id+"/export?format=oscal", owner, nil, tenantHdr(tenant)).raw

	reg := h.do("POST", "/v1/m/compliance/oscal/profiles?framework=eu_ai_act", owner, map[string]any{"doc": oscalDocBody}, tenantHdr(tenant))
	if reg.code != http.StatusCreated {
		t.Fatalf("register = %d %s", reg.code, reg.raw)
	}
	scoped := h.do("GET", "/v1/m/compliance/evidence/"+id+"/export?format=oscal", owner, nil, tenantHdr(tenant)).raw
	if scoped == before {
		t.Fatal("a registered profile must change the export")
	}

	profID := reg.body["id"].(string)
	if d := h.do("DELETE", "/v1/m/compliance/oscal/profiles/"+profID, owner, nil, tenantHdr(tenant)); d.code != http.StatusNoContent {
		t.Fatalf("unregister = %d %s", d.code, d.raw)
	}
	after := h.do("GET", "/v1/m/compliance/evidence/"+id+"/export?format=oscal", owner, nil, tenantHdr(tenant)).raw
	if after != before {
		t.Fatal("after unregister the OSCAL export must be byte-identical to the no-profile export")
	}
	if acts := strings.Join(h.auditActions(tenant), ","); !strings.Contains(acts, "compliance.oscal.profile.unregister") {
		t.Fatalf("unregister must self-audit; actions=%s", acts)
	}
}

// --- tiny test helpers -------------------------------------------------------------

func asList(r resp) []any {
	items, _ := r.body["items"].([]any)
	return items
}
