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

// NIS 2 Directive mapping + significant-incident classification depth +
// ISO/IEC 42001 AIMS certification-readiness wizard. These tests exercise the OPEN-CORE
// half: the NIS2 catalog + calendar, the deny-closed (501) NIS2 incident endpoints without
// the enterprise add-on, governed persistence/phase-evolution when a stub is wired, and
// the honesty invariants (provisional verdicts, disclaimers, gaps).

// --- NIS2 catalog + calendar invariants ------------------------------------------

func TestNIS2CatalogExists(t *testing.T) {
	fw, ok := frameworkByID["nis2"]
	if !ok {
		t.Fatal("nis2 framework must exist in the catalog")
	}
	if fw.Name != "NIS 2 Directive" {
		t.Fatalf("nis2 name = %q, want NIS 2 Directive", fw.Name)
	}
	if fw.Version != "Directive (EU) 2022/2555" {
		t.Fatalf("nis2 version = %q", fw.Version)
	}
	if fw.Pin.Status != PinInForce {
		t.Fatalf("nis2 pin status = %q, want in_force", fw.Pin.Status)
	}
	if fw.Pin.SourceURL == "" {
		t.Fatal("nis2 pin must have a source URL")
	}
	if fw.Pin.VerifiedOn == "" {
		t.Fatal("nis2 pin must have a verified_on date")
	}
}

func TestNIS2ControlsValid(t *testing.T) {
	fw := frameworkByID["nis2"]
	if len(fw.Controls) < 13 {
		t.Fatalf("nis2 must have at least 13 controls (Art 20 + 10 Art 21(2) sub-measures + Art 23 + Art 29); got %d", len(fw.Controls))
	}
	capKeys := make(map[CapabilityKey]bool)
	for _, c := range capabilityCatalog {
		capKeys[c.Key] = true
	}
	for _, c := range fw.Controls {
		if c.ID == "" {
			t.Fatal("every control must have an ID")
		}
		if c.Title == "" {
			t.Fatalf("control %s has no title", c.ID)
		}
		if c.Requirement == "" {
			t.Fatalf("control %s has no requirement", c.ID)
		}
		if c.Criterion == "" {
			t.Fatalf("control %s has no criterion", c.ID)
		}
		if len(c.Capabilities) == 0 {
			t.Fatalf("control %s has no capabilities mapped — an empty list should have a Note explaining the gap", c.ID)
		}
		for _, cap := range c.Capabilities {
			if !capKeys[cap] {
				t.Fatalf("control %s references unknown capability %q", c.ID, cap)
			}
		}
	}
}

func TestNIS2CalendarMilestones(t *testing.T) {
	nis2Milestones := []string{
		"nis2.entry_into_force",
		"nis2.transposition_deadline",
		"nis2.essential_important_register",
	}
	for _, id := range nis2Milestones {
		found := false
		for _, m := range regulatoryCalendar {
			if m.ID == id {
				found = true
				if m.Date == "" {
					t.Fatalf("milestone %s has no date", id)
				}
				if m.Source.URL == "" {
					t.Fatalf("milestone %s has no source URL", id)
				}
				if m.VerifiedOn == "" {
					t.Fatalf("milestone %s has no verified_on", id)
				}
				if m.FrameworkID != "nis2" {
					t.Fatalf("milestone %s framework_id = %q, want nis2", id, m.FrameworkID)
				}
				break
			}
		}
		if !found {
			t.Fatalf("milestone %s not found in the regulatory calendar", id)
		}
	}
}

func TestNIS2FrameworkAssessable(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	owner := h.roleToken(admin, tenant, "o@x.io", "owner")

	r := h.do("GET", "/v1/m/compliance/frameworks/nis2", owner, nil, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("GET /frameworks/nis2 = %d %s", r.code, r.raw)
	}
	fw, _ := r.body["framework"].(map[string]any)
	if fw == nil || fw["id"] != "nis2" {
		t.Fatalf("framework response missing or wrong id: %v", r.body)
	}

	r = h.do("GET", "/v1/m/compliance/frameworks/nis2/status", owner, nil, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("GET /frameworks/nis2/status = %d %s", r.code, r.raw)
	}
}

// --- NIS2 incident: deny-closed without packager ---------------------------------

// stubNIS2Packager is a programmable stand-in for the commercial NIS2 classifier.
type stubNIS2Packager struct {
	result *NIS2IncidentResult
	err    error
}

func (s *stubNIS2Packager) ClassifySignificantIncident(_ context.Context, _ NIS2IncidentInput) (*NIS2IncidentResult, error) {
	return s.result, s.err
}

func sampleNIS2Classification() *NIS2IncidentResult {
	return &NIS2IncidentResult{
		Significant:    true,
		CrossBorder:    true,
		SuspectedCrime: false,
		CriteriaMet:    []string{"23(3)(a)", "23(3)(b)"},
		Rationale:      "severe operational disruption AND persons affected — significant",
		Deadlines:      map[string]any{"early_warning_due": "2026-06-05T12:00:00Z", "notification_due": "2026-06-07T12:00:00Z"},
		ReportDrafts:   map[string]any{"early_warning": map[string]any{"reference": "INC-001"}},
		Basis:          []map[string]string{{"provision": "Art 23(3)", "source_url": "https://eur-lex.europa.eu/eli/dir/2022/2555/oj"}},
		Note:           "provisional — requires human attestation",
	}
}

func TestNIS2IncidentDenyClosedWithoutPackager(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	owner := h.roleToken(admin, tenant, "o@x.io", "owner")

	r := h.do("POST", "/v1/m/compliance/nis2/incidents/classify?reference=INC-001", owner, map[string]any{"x": 1}, tenantHdr(tenant))
	if r.code != http.StatusNotImplemented {
		t.Fatalf("classify without packager must be 501; got %d %s", r.code, r.raw)
	}

	r = h.do("GET", "/v1/m/compliance/nis2/incidents", owner, nil, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("list must work; got %d %s", r.code, r.raw)
	}
}

func TestNIS2IncidentPersistAndExport(t *testing.T) {
	pkg := &stubNIS2Packager{result: sampleNIS2Classification()}
	h := newHarness(t, WithNIS2IncidentPackager(pkg))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	owner := h.roleToken(admin, tenant, "o@x.io", "owner")

	gen := h.do("POST", "/v1/m/compliance/nis2/incidents/classify?reference=INC-001", owner, map[string]any{"x": 1}, tenantHdr(tenant))
	if gen.code != http.StatusCreated {
		t.Fatalf("classify = %d %s", gen.code, gen.raw)
	}
	if gen.body["significant"] != true {
		t.Fatalf("significant = %v", gen.body["significant"])
	}
	if gen.body["cross_border"] != true {
		t.Fatalf("cross_border = %v", gen.body["cross_border"])
	}
	if gen.body["phase"] != "early_warning" {
		t.Fatalf("initial phase = %v, want early_warning", gen.body["phase"])
	}
	if gen.body["doc_sha256"] == "" {
		t.Fatal("doc_sha256 must be populated")
	}
	if d, _ := gen.body["disclaimer"].(string); !strings.Contains(d, "DECISION SUPPORT") {
		t.Fatalf("disclaimer must be honest: %q", d)
	}
	if acts := strings.Join(h.auditActions(tenant), ","); !strings.Contains(acts, "compliance.nis2.incident.classify") {
		t.Fatalf("classify must self-audit; actions=%s", acts)
	}

	id := gen.body["id"].(string)

	get := h.do("GET", "/v1/m/compliance/nis2/incidents/"+id, owner, nil, tenantHdr(tenant))
	if get.code != http.StatusOK {
		t.Fatalf("get = %d %s", get.code, get.raw)
	}

	exp := h.do("GET", "/v1/m/compliance/nis2/incidents/"+id+"/export", owner, nil, tenantHdr(tenant))
	if exp.code != http.StatusOK {
		t.Fatalf("export = %d %s", exp.code, exp.raw)
	}
	if _, ok := exp.body["ledger_anchor"].(map[string]any); !ok {
		t.Fatal("export must carry a ledger_anchor")
	}

	list := h.do("GET", "/v1/m/compliance/nis2/incidents", owner, nil, tenantHdr(tenant))
	items := asList(list)
	if len(items) != 1 {
		t.Fatalf("list should have 1 item; got %d", len(items))
	}
}

func TestNIS2IncidentPhaseEvolution(t *testing.T) {
	pkg := &stubNIS2Packager{result: sampleNIS2Classification()}
	h := newHarness(t, WithNIS2IncidentPackager(pkg))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	owner := h.roleToken(admin, tenant, "o@x.io", "owner")

	gen := h.do("POST", "/v1/m/compliance/nis2/incidents/classify?reference=INC-002", owner, map[string]any{"x": 1}, tenantHdr(tenant))
	if gen.code != http.StatusCreated {
		t.Fatalf("classify = %d %s", gen.code, gen.raw)
	}
	id := gen.body["id"].(string)

	upd := h.do("PUT", "/v1/m/compliance/nis2/incidents/"+id, owner, map[string]any{"phase": "notification"}, tenantHdr(tenant))
	if upd.code != http.StatusOK {
		t.Fatalf("update to notification = %d %s", upd.code, upd.raw)
	}
	if upd.body["phase"] != "notification" {
		t.Fatalf("phase = %v, want notification", upd.body["phase"])
	}

	backward := h.do("PUT", "/v1/m/compliance/nis2/incidents/"+id, owner, map[string]any{"phase": "early_warning"}, tenantHdr(tenant))
	if backward.code != http.StatusConflict {
		t.Fatalf("backward phase transition must be 409; got %d %s", backward.code, backward.raw)
	}

	upd2 := h.do("PUT", "/v1/m/compliance/nis2/incidents/"+id, owner, map[string]any{"phase": "final"}, tenantHdr(tenant))
	if upd2.code != http.StatusOK {
		t.Fatalf("update to final = %d %s", upd2.code, upd2.raw)
	}
	if upd2.body["phase"] != "final" {
		t.Fatalf("phase = %v, want final", upd2.body["phase"])
	}
}

func TestNIS2IncidentReclassify(t *testing.T) {
	pkg := &stubNIS2Packager{result: sampleNIS2Classification()}
	h := newHarness(t, WithNIS2IncidentPackager(pkg))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	owner := h.roleToken(admin, tenant, "o@x.io", "owner")

	gen1 := h.do("POST", "/v1/m/compliance/nis2/incidents/classify?reference=INC-003", owner, map[string]any{"v": 1}, tenantHdr(tenant))
	if gen1.code != http.StatusCreated {
		t.Fatalf("classify 1 = %d %s", gen1.code, gen1.raw)
	}
	gen2 := h.do("POST", "/v1/m/compliance/nis2/incidents/classify?reference=INC-003", owner, map[string]any{"v": 2}, tenantHdr(tenant))
	if gen2.code != http.StatusCreated {
		t.Fatalf("classify 2 (re-classify) = %d %s", gen2.code, gen2.raw)
	}

	list := h.do("GET", "/v1/m/compliance/nis2/incidents", owner, nil, tenantHdr(tenant))
	items := asList(list)
	if len(items) != 1 {
		t.Fatalf("re-classify must update, not duplicate; got %d items", len(items))
	}
}

func TestNIS2IncidentDelete(t *testing.T) {
	pkg := &stubNIS2Packager{result: sampleNIS2Classification()}
	h := newHarness(t, WithNIS2IncidentPackager(pkg))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	owner := h.roleToken(admin, tenant, "o@x.io", "owner")

	gen := h.do("POST", "/v1/m/compliance/nis2/incidents/classify?reference=INC-DEL", owner, map[string]any{"x": 1}, tenantHdr(tenant))
	if gen.code != http.StatusCreated {
		t.Fatalf("classify = %d %s", gen.code, gen.raw)
	}
	id := gen.body["id"].(string)

	del := h.do("DELETE", "/v1/m/compliance/nis2/incidents/"+id, owner, nil, tenantHdr(tenant))
	if del.code != http.StatusOK {
		t.Fatalf("delete = %d %s", del.code, del.raw)
	}

	list := h.do("GET", "/v1/m/compliance/nis2/incidents", owner, nil, tenantHdr(tenant))
	if len(asList(list)) != 0 {
		t.Fatal("delete must remove the classification")
	}
}

func TestNIS2IncidentGovernance(t *testing.T) {
	h := newHarness(t, WithNIS2IncidentPackager(&stubNIS2Packager{result: sampleNIS2Classification()}))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	viewer := h.roleToken(admin, tenant, "v@x.io", "viewer")

	r := h.do("POST", "/v1/m/compliance/nis2/incidents/classify?reference=INC-X", viewer, map[string]any{"x": 1}, tenantHdr(tenant))
	if r.code != http.StatusForbidden {
		t.Fatalf("viewer must be 403; got %d %s", r.code, r.raw)
	}
}

func TestNIS2IncidentRejected(t *testing.T) {
	h := newHarness(t, WithNIS2IncidentPackager(&stubNIS2Packager{err: context.DeadlineExceeded}))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	owner := h.roleToken(admin, tenant, "o@x.io", "owner")

	r := h.do("POST", "/v1/m/compliance/nis2/incidents/classify?reference=INC-BAD", owner, map[string]any{"x": 1}, tenantHdr(tenant))
	if r.code != http.StatusUnprocessableEntity {
		t.Fatalf("rejected document must be 422; got %d %s", r.code, r.raw)
	}
}
