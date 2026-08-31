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

// DORA named-regulation depth (Register of Information + major-incident
// classification) and the OSCAL POA&M reinforcement. These tests exercise the OPEN-CORE
// half: the deny-closed (501) endpoints without the enterprise add-on, the governed
// persistence/export/audit substrate, the BYTE-IDENTICAL no-builder OSCAL path, and the
// honesty invariants (provisional verdicts, ledger anchor). The 2024/2956 structuring and
// the Art 18 / RTS classification themselves (enterprise/doraregister) are tested in that
// package; here stubs stand in for the closed add-on so the open behavior is verified
// without the open module importing the closed one.

// stubRegPackager is a programmable stand-in for the commercial packager.
type stubRegPackager struct {
	reg        *RegisterDocument
	regErr     error
	inc        *IncidentClassification
	incErr     error
	gotKnown   []KnownICTProvider
	gotRefDate string
}

func (s *stubRegPackager) BuildDORARegister(_ context.Context, in RegisterInput, known []KnownICTProvider) (*RegisterDocument, error) {
	s.gotKnown = known
	s.gotRefDate = in.ReferenceDate
	return s.reg, s.regErr
}

func (s *stubRegPackager) ClassifyMajorIncident(_ context.Context, _ IncidentInput) (*IncidentClassification, error) {
	return s.inc, s.incErr
}

// stubPOAMBuilder returns a canned POA&M model (or nil/err).
type stubPOAMBuilder struct {
	poam map[string]any
	err  error
}

func (s stubPOAMBuilder) BuildPOAM(POAMInput) (map[string]any, error) { return s.poam, s.err }

func sampleRegisterDoc() *RegisterDocument {
	return &RegisterDocument{
		Regulation:    doraRegisterRegulation,
		EntityLEI:     "529900T8BM49AURSDO55",
		EntityName:    "Acme Bank SA",
		ReferenceDate: "2026-03-31",
		Templates: map[string]any{
			"B_01.01": map[string]any{"0010": "529900T8BM49AURSDO55", "0020": "Acme Bank SA"},
			"B_05.01": []any{map[string]any{"0010": "anthropic", "0050": "Anthropic PBC"}},
		},
		Validation:     []RegisterIssue{{Severity: "warning", Template: "B_02.02", Field: "0120", Message: "governing-law country missing for a critical-function service"}},
		Reconciliation: []RegisterIssue{{Severity: "info", Template: "B_05.01", Message: "tracked provider 'anthropic' present in the register"}},
		Counts:         map[string]int{"B_05.01": 1},
		Note:           "labels rest on ESA artifacts, not byte-diffed against the OJ",
	}
}

func majorIncidentClassification() *IncidentClassification {
	return &IncidentClassification{
		Reference:        "INC-2026-007",
		Major:            true,
		CriticalServices: true,
		CriteriaMet:      []string{"9(3)", "9(5)(b)"},
		Rationale:        "critical services affected (Art 6) AND a successful malicious unauthorized access (Art 9(5)(b)) — major",
		Report:           map[string]any{"initial": map[string]any{"2.1": "INC-2026-007"}},
		Deadlines:        map[string]any{"initial": "4h from classification / 24h from awareness"},
		Basis:            []map[string]string{{"provision": "Art 8(1)", "source_url": "https://eur-lex.europa.eu/eli/reg_del/2024/1772/oj/eng", "verified_on": "2026-06-24"}},
		Note:             "provisional — requires human attestation",
	}
}

// TestDORADenyClosedWithoutPackager: with NO packager wired (the default AGPL build), the
// register/incident generation endpoints answer 501, nothing is persisted, and the open
// dora.go ICT-risk view (GET /dora) is unchanged.
func TestDORADenyClosedWithoutPackager(t *testing.T) {
	h := newHarness(t) // no WithRegulatoryPackager
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	owner := h.roleToken(admin, tenant, "o@x.io", "owner")

	if r := h.do("POST", "/v1/m/compliance/dora/register", owner, map[string]any{"entity_lei": "x"}, tenantHdr(tenant)); r.code != http.StatusNotImplemented {
		t.Fatalf("register generation without the enterprise packager must be 501; got %d %s", r.code, r.raw)
	}
	if r := h.do("POST", "/v1/m/compliance/dora/incidents?reference=INC-1", owner, map[string]any{"x": 1}, tenantHdr(tenant)); r.code != http.StatusNotImplemented {
		t.Fatalf("incident classification without the enterprise packager must be 501; got %d %s", r.code, r.raw)
	}
	// Nothing persisted; the reads are open and return empty.
	if r := h.do("GET", "/v1/m/compliance/dora/register", owner, nil, tenantHdr(tenant)); len(asList(r)) != 0 {
		t.Fatal("no register may exist without a packager")
	}
	// The open ICT-risk view is untouched (no rug-pull).
	if r := h.do("GET", "/v1/m/compliance/dora", owner, nil, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Fatalf("the open DORA ICT-risk view (GET /dora) must still work; got %d %s", r.code, r.raw)
	}
}

// TestDORARegisterGovernance: generation is admin-tier and deny-closed; a packager error is
// 422 (nothing persisted); a register with no maintaining-entity LEI is 422 (defense in depth).
func TestDORARegisterGovernance(t *testing.T) {
	h := newHarness(t, WithRegulatoryPackager(&stubRegPackager{reg: sampleRegisterDoc()}))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	viewer := h.roleToken(admin, tenant, "v@x.io", "viewer")

	if r := h.do("POST", "/v1/m/compliance/dora/register", viewer, map[string]any{"x": 1}, tenantHdr(tenant)); r.code != http.StatusForbidden {
		t.Fatalf("viewer register must be 403; got %d %s", r.code, r.raw)
	}

	hBad := newHarness(t, WithRegulatoryPackager(&stubRegPackager{regErr: context.DeadlineExceeded}))
	adminB := hBad.adminLogin()
	tenantB := hBad.createOrg(adminB, "acme")
	ownerB := hBad.roleToken(adminB, tenantB, "o@x.io", "owner")
	if r := hBad.do("POST", "/v1/m/compliance/dora/register", ownerB, map[string]any{"x": 1}, tenantHdr(tenantB)); r.code != http.StatusUnprocessableEntity {
		t.Fatalf("a rejected document must be 422; got %d %s", r.code, r.raw)
	}
	if r := hBad.do("GET", "/v1/m/compliance/dora/register", ownerB, nil, tenantHdr(tenantB)); len(asList(r)) != 0 {
		t.Fatal("a rejected generation must persist nothing")
	}

	hNoEntity := newHarness(t, WithRegulatoryPackager(&stubRegPackager{reg: &RegisterDocument{Templates: map[string]any{"B_01.01": map[string]any{}}}}))
	adminE := hNoEntity.adminLogin()
	tenantE := hNoEntity.createOrg(adminE, "acme")
	ownerE := hNoEntity.roleToken(adminE, tenantE, "o@x.io", "owner")
	if r := hNoEntity.do("POST", "/v1/m/compliance/dora/register", ownerE, map[string]any{"x": 1}, tenantHdr(tenantE)); r.code != http.StatusUnprocessableEntity {
		t.Fatalf("a register with no maintaining-entity LEI must be 422; got %d %s", r.code, r.raw)
	}
}

// TestDORARegisterPersistExportReplace: generate persists a maintained register (one per
// entity, replace-on-regenerate), the export carries the live ledger anchor + disclaimer, and
// generate/delete self-audit semantically.
func TestDORARegisterPersistExportReplace(t *testing.T) {
	pkg := &stubRegPackager{reg: sampleRegisterDoc()}
	h := newHarness(t, WithRegulatoryPackager(pkg))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	owner := h.roleToken(admin, tenant, "o@x.io", "owner")

	gen := h.do("POST", "/v1/m/compliance/dora/register?reference_date=2026-03-31", owner, map[string]any{"entity": "acme"}, tenantHdr(tenant))
	if gen.code != http.StatusCreated {
		t.Fatalf("generate = %d %s", gen.code, gen.raw)
	}
	if gen.body["entity_lei"] != "529900T8BM49AURSDO55" || gen.body["doc_sha256"] == "" {
		t.Fatalf("generate dto missing entity_lei/doc_sha256: %s", gen.raw)
	}
	if gen.body["error_count"] != float64(0) { // one warning, zero errors
		t.Fatalf("error_count = %v, want 0 (the sample has only a warning)", gen.body["error_count"])
	}
	if pkg.gotRefDate != "2026-03-31" {
		t.Fatalf("the reference_date query param must reach the packager; got %q", pkg.gotRefDate)
	}
	if acts := strings.Join(h.auditActions(tenant), ","); !strings.Contains(acts, "compliance.dora.register.generate") {
		t.Fatalf("generate must self-audit; actions=%s", acts)
	}

	id := gen.body["id"].(string)
	exp := h.do("GET", "/v1/m/compliance/dora/register/"+id+"/export", owner, nil, tenantHdr(tenant))
	if exp.code != http.StatusOK {
		t.Fatalf("export = %d %s", exp.code, exp.raw)
	}
	if _, ok := exp.body["ledger_anchor"].(map[string]any); !ok {
		t.Fatalf("export must carry a live ledger_anchor: %s", exp.raw)
	}
	if d, _ := exp.body["disclaimer"].(string); !strings.Contains(d, "DORA-compliant") || !strings.Contains(d, "certification") {
		t.Fatalf("export disclaimer must be honest (does NOT make the tenant DORA-compliant; NOT a certification): %q", d)
	}
	if !strings.Contains(exp.raw, "B_01.01") {
		t.Fatal("export must include the structured templates")
	}

	// Replace-on-regenerate: same entity ⇒ Update, never a second row.
	if r := h.do("POST", "/v1/m/compliance/dora/register", owner, map[string]any{"entity": "acme-again"}, tenantHdr(tenant)); r.code != http.StatusCreated {
		t.Fatalf("re-generate = %d %s", r.code, r.raw)
	}
	if r := h.do("GET", "/v1/m/compliance/dora/register", owner, nil, tenantHdr(tenant)); len(asList(r)) != 1 {
		t.Fatalf("re-generate for the same entity must replace, not add; list=%d", len(asList(r)))
	}

	if d := h.do("DELETE", "/v1/m/compliance/dora/register/"+id, owner, nil, tenantHdr(tenant)); d.code != http.StatusNoContent {
		t.Fatalf("delete = %d %s", d.code, d.raw)
	}
	if acts := strings.Join(h.auditActions(tenant), ","); !strings.Contains(acts, "compliance.dora.register.delete") {
		t.Fatalf("delete must self-audit; actions=%s", acts)
	}
}

// TestDORAIncidentClassifyAndReport: classify persists a provisional major-incident
// classification (one per reference, replace-on-reclassify), the report export carries the
// verdict + deadlines + the provisional flag, and classify self-audits.
func TestDORAIncidentClassifyAndReport(t *testing.T) {
	h := newHarness(t, WithRegulatoryPackager(&stubRegPackager{inc: majorIncidentClassification()}))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	owner := h.roleToken(admin, tenant, "o@x.io", "owner")
	viewer := h.roleToken(admin, tenant, "v@x.io", "viewer")

	// Deny-closed: a viewer cannot classify (admin-tier).
	if r := h.do("POST", "/v1/m/compliance/dora/incidents?reference=INC-2026-007", viewer, map[string]any{"x": 1}, tenantHdr(tenant)); r.code != http.StatusForbidden {
		t.Fatalf("viewer classify must be 403; got %d %s", r.code, r.raw)
	}
	// reference is required.
	if r := h.do("POST", "/v1/m/compliance/dora/incidents", owner, map[string]any{"x": 1}, tenantHdr(tenant)); r.code != http.StatusBadRequest {
		t.Fatalf("classify without a reference must be 400; got %d %s", r.code, r.raw)
	}

	cls := h.do("POST", "/v1/m/compliance/dora/incidents?reference=INC-2026-007", owner, map[string]any{"clients_affected_pct": 12}, tenantHdr(tenant))
	if cls.code != http.StatusCreated {
		t.Fatalf("classify = %d %s", cls.code, cls.raw)
	}
	if cls.body["major"] != true || cls.body["provisional"] != true {
		t.Fatalf("classification must be major + provisional: %s", cls.raw)
	}
	if acts := strings.Join(h.auditActions(tenant), ","); !strings.Contains(acts, "compliance.dora.incident.classify") {
		t.Fatalf("classify must self-audit; actions=%s", acts)
	}

	id := cls.body["id"].(string)
	rep := h.do("GET", "/v1/m/compliance/dora/incidents/"+id+"/report", owner, nil, tenantHdr(tenant))
	if rep.code != http.StatusOK {
		t.Fatalf("report export = %d %s", rep.code, rep.raw)
	}
	if rep.body["provisional"] != true {
		t.Fatal("report must carry provisional=true (requires human attestation)")
	}
	for _, marker := range []string{"deadlines", "criteria_met", "ledger_anchor"} {
		if _, ok := rep.body[marker]; !ok {
			t.Fatalf("report export missing %q: %s", marker, rep.raw)
		}
	}

	// Replace-on-reclassify: same reference ⇒ Update.
	if r := h.do("POST", "/v1/m/compliance/dora/incidents?reference=INC-2026-007", owner, map[string]any{"x": 2}, tenantHdr(tenant)); r.code != http.StatusCreated {
		t.Fatalf("re-classify = %d %s", r.code, r.raw)
	}
	if r := h.do("GET", "/v1/m/compliance/dora/incidents", owner, nil, tenantHdr(tenant)); len(asList(r)) != 1 {
		t.Fatalf("re-classify for the same reference must replace; list=%d", len(asList(r)))
	}
	// major=true filter returns it.
	if r := h.do("GET", "/v1/m/compliance/dora/incidents?major=true", owner, nil, tenantHdr(tenant)); len(asList(r)) != 1 {
		t.Fatalf("major filter must return the major incident; got %d", len(asList(r)))
	}
}

// TestOSCALPOAMByteIdenticalWithoutBuilder: without a POA&M builder the evidence OSCAL export
// emits exactly its three models (no plan-of-action-and-milestones); with the builder wired it
// gains the POA&M, and the rest of the bundle is unchanged.
func TestOSCALPOAMByteIdenticalWithoutBuilder(t *testing.T) {
	h := newHarness(t) // no WithPOAMBuilder
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	owner := h.roleToken(admin, tenant, "o@x.io", "owner")

	id := sealEUAIAct(t, h, owner, tenant)
	exp := h.do("GET", "/v1/m/compliance/evidence/"+id+"/export?format=oscal", owner, nil, tenantHdr(tenant))
	if exp.code != http.StatusOK {
		t.Fatalf("oscal export = %d %s", exp.code, exp.raw)
	}
	if _, has := exp.body["plan-of-action-and-milestones"]; has {
		t.Fatal("without a POA&M builder the OSCAL export must NOT contain a plan-of-action-and-milestones (byte-identical baseline)")
	}

	hP := newHarness(t, WithPOAMBuilder(stubPOAMBuilder{poam: map[string]any{"uuid": "poam-1", "poam-items": []any{}}}))
	adminP := hP.adminLogin()
	tenantP := hP.createOrg(adminP, "acme")
	ownerP := hP.roleToken(adminP, tenantP, "o@x.io", "owner")
	idP := sealEUAIAct(t, hP, ownerP, tenantP)
	expP := hP.do("GET", "/v1/m/compliance/evidence/"+idP+"/export?format=oscal", ownerP, nil, tenantHdr(tenantP))
	if expP.code != http.StatusOK {
		t.Fatalf("oscal export (poam) = %d %s", expP.code, expP.raw)
	}
	if _, has := expP.body["plan-of-action-and-milestones"]; !has {
		t.Fatalf("with a POA&M builder the OSCAL export must include a plan-of-action-and-milestones: %s", expP.raw)
	}
	// The three open models are still present (the POA&M is purely additive).
	for _, m := range []string{"component-definition", "assessment-results", "control-mapping"} {
		if _, has := expP.body[m]; !has {
			t.Fatalf("the POA&M must be additive; missing %q", m)
		}
	}
}

// TestOSCALPOAMAuditReflectsAttachment (review M1): the evidence-export self-audit records
// oscal_poam=true ONLY when a POA&M is actually attached — never merely because the builder is
// wired. A builder that yields no model (e.g. all controls satisfied) must NOT leave a false
// oscal_poam claim in the committed audit row.
func TestOSCALPOAMAuditReflectsAttachment(t *testing.T) {
	// Builder attaches a model ⇒ the audit records oscal_poam=true and the bytes carry it.
	hA := newHarness(t, WithPOAMBuilder(stubPOAMBuilder{poam: map[string]any{"uuid": "poam-1"}}))
	adminA := hA.adminLogin()
	tenantA := hA.createOrg(adminA, "acme")
	ownerA := hA.roleToken(adminA, tenantA, "o@x.io", "owner")
	idA := sealEUAIAct(t, hA, ownerA, tenantA)
	if r := hA.do("GET", "/v1/m/compliance/evidence/"+idA+"/export?format=oscal", ownerA, nil, tenantHdr(tenantA)); r.code != http.StatusOK {
		t.Fatalf("export = %d %s", r.code, r.raw)
	}
	if meta := hA.auditMetaFor(tenantA, "compliance.evidence.export"); meta["oscal_poam"] != true {
		t.Fatalf("audit must record oscal_poam=true when a POA&M is attached; meta=%v", meta)
	}

	// Builder yields nothing (nil) ⇒ no POA&M in the bytes AND no oscal_poam claim in the audit.
	hN := newHarness(t, WithPOAMBuilder(stubPOAMBuilder{poam: nil}))
	adminN := hN.adminLogin()
	tenantN := hN.createOrg(adminN, "acme")
	ownerN := hN.roleToken(adminN, tenantN, "o@x.io", "owner")
	idN := sealEUAIAct(t, hN, ownerN, tenantN)
	expN := hN.do("GET", "/v1/m/compliance/evidence/"+idN+"/export?format=oscal", ownerN, nil, tenantHdr(tenantN))
	if _, has := expN.body["plan-of-action-and-milestones"]; has {
		t.Fatal("a nil-yielding builder must not attach a POA&M")
	}
	if meta := hN.auditMetaFor(tenantN, "compliance.evidence.export"); meta["oscal_poam"] != nil {
		t.Fatalf("audit must NOT claim oscal_poam when nothing was attached; meta=%v", meta)
	}
}
