// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package compliance

import (
	"context"
	"encoding/json"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// the 2026-H2 re-baseline: the regulatory calendar as verifiable data, the
// version pins, the new agentic frameworks (OWASP Agentic Top 10 2026; Five Eyes real
// categories; AI Data Security crosswalk), the PLD defense-evidence entry, the AILD
// purge, and the DORA export mode.

func isoDateOK(s string) bool {
	if len(s) == 4 { // year-only pin (e.g. "2022")
		_, err := time.Parse("2006", s)
		return err == nil
	}
	if len(s) == 7 { // year-month pin (e.g. "2023-12")
		_, err := time.Parse("2006-01", s)
		return err == nil
	}
	_, err := time.Parse("2006-01-02", s)
	return err == nil
}

// TestRegulatoryCalendarIsVerifiableData enforces the bar: every milestone and
// watchlist item carries a full date, a primary source (https URL + title + publisher)
// and a verified_on date; IDs are unique; framework links resolve; and every Digital
// Omnibus future date is labeled adopted_pending_oj — the calendar never pretends
// an adopted-but-unpublished amendment is in-force law.
func TestRegulatoryCalendarIsVerifiableData(t *testing.T) {
	if len(regulatoryCalendar) == 0 || len(regulatoryWatchlist) == 0 {
		t.Fatal("calendar and watchlist must not be empty")
	}
	seen := map[string]bool{}
	for _, ms := range regulatoryCalendar {
		if ms.ID == "" || seen[ms.ID] {
			t.Errorf("milestone %q: missing or duplicate id", ms.ID)
		}
		seen[ms.ID] = true
		if _, err := time.Parse("2006-01-02", ms.Date); err != nil {
			t.Errorf("milestone %s: date %q is not a full ISO date", ms.ID, ms.Date)
		}
		if ms.Regime == "" || ms.Title == "" || ms.Effect == "" {
			t.Errorf("milestone %s: regime/title/effect must be set", ms.ID)
		}
		switch ms.Status {
		case MilestoneInForce, MilestoneAppliesFrom, MilestoneProvisional, MilestoneAdoptedPendingOJ, MilestonePassed:
		default:
			t.Errorf("milestone %s: invalid status %q", ms.ID, ms.Status)
		}
		if !strings.HasPrefix(ms.Source.URL, "https://") || ms.Source.Title == "" || ms.Source.Publisher == "" {
			t.Errorf("milestone %s: source must carry https URL + title + publisher; got %+v", ms.ID, ms.Source)
		}
		if !isoDateOK(ms.VerifiedOn) || len(ms.VerifiedOn) != 10 {
			t.Errorf("milestone %s: verified_on %q must be a full ISO date", ms.ID, ms.VerifiedOn)
		}
		if ms.FrameworkID != "" {
			if _, ok := frameworkByID[ms.FrameworkID]; !ok {
				t.Errorf("milestone %s: framework_id %q does not resolve", ms.ID, ms.FrameworkID)
			}
		}
		// The honesty line for the omnibus: adopted, pending OJ publication ≠ in-force law.
		if strings.Contains(ms.Regime, "Digital Omnibus") && ms.ID != "eu_ai_act.omnibus_adopted" && ms.Status != MilestoneAdoptedPendingOJ {
			t.Errorf("milestone %s: Digital Omnibus future dates must be adopted_pending_oj, got %q", ms.ID, ms.Status)
		}
	}
	for _, wi := range regulatoryWatchlist {
		if wi.ID == "" || seen[wi.ID] {
			t.Errorf("watchlist %q: missing or duplicate id", wi.ID)
		}
		seen[wi.ID] = true
		if wi.Name == "" || wi.Status == "" {
			t.Errorf("watchlist %s: name/status must be set", wi.ID)
		}
		if !strings.HasPrefix(wi.Source.URL, "https://") || wi.Source.Title == "" || wi.Source.Publisher == "" {
			t.Errorf("watchlist %s: source must carry https URL + title + publisher; got %+v", wi.ID, wi.Source)
		}
		if !isoDateOK(wi.VerifiedOn) || len(wi.VerifiedOn) != 10 {
			t.Errorf("watchlist %s: verified_on %q must be a full ISO date", wi.ID, wi.VerifiedOn)
		}
		if wi.FrameworkID != "" {
			if _, ok := frameworkByID[wi.FrameworkID]; !ok {
				t.Errorf("watchlist %s: framework_id %q does not resolve", wi.ID, wi.FrameworkID)
			}
		}
	}
	// The re-baseline facts that triggered must be present with the right status.
	for id, want := range map[string]MilestoneStatus{
		"eu_ai_act.art50_transparency_applies": MilestoneAppliesFrom,
		"eu_ai_act.high_risk_annex3_omnibus":   MilestoneAdoptedPendingOJ,
		"eu_ai_act.high_risk_annex1_omnibus":   MilestoneAdoptedPendingOJ,
		"eu_ai_act.art50_marking_grace_ends":   MilestoneAdoptedPendingOJ,
		"eu_pld.transposition_deadline":        MilestoneAppliesFrom,
		"eu_aild.withdrawn":                    MilestonePassed,
		"fips_140_2.historical":                MilestoneAppliesFrom,
		"cnsa_2_0.new_nss_acquisitions":        MilestoneAppliesFrom,
		"colorado_admt.obligations_apply":      MilestoneAppliesFrom,
	} {
		ms, ok := milestoneByID[id]
		if !ok {
			t.Errorf("required milestone %s missing", id)
			continue
		}
		if ms.Status != want {
			t.Errorf("milestone %s: status %q, want %q", id, ms.Status, want)
		}
	}
}

// TestFrameworkPinsAreVerifiableData: every catalog framework carries a structured
// version pin (document + primary source + verified_on + lifecycle status) — the
// version-pin-everything deliverable.
func TestFrameworkPinsAreVerifiableData(t *testing.T) {
	for _, fw := range catalog {
		if fw.Pin.Document == "" {
			t.Errorf("%s: pin.document must be set", fw.ID)
		}
		if !strings.HasPrefix(fw.Pin.SourceURL, "https://") {
			t.Errorf("%s: pin.source_url must be a primary https URL; got %q", fw.ID, fw.Pin.SourceURL)
		}
		if !isoDateOK(fw.Pin.VerifiedOn) || len(fw.Pin.VerifiedOn) != 10 {
			t.Errorf("%s: pin.verified_on %q must be a full ISO date", fw.ID, fw.Pin.VerifiedOn)
		}
		switch fw.Pin.Status {
		case PinInForce, PinFinal, PinGuidance, PinInDevelopment:
		default:
			t.Errorf("%s: pin.status %q invalid", fw.ID, fw.Pin.Status)
		}
		if fw.Pin.PublishedOn != "" && !isoDateOK(fw.Pin.PublishedOn) {
			t.Errorf("%s: pin.published_on %q is not an ISO date", fw.ID, fw.Pin.PublishedOn)
		}
		// A document still in development must not carry a publication date pin.
		if fw.Pin.Status == PinInDevelopment && fw.Pin.PublishedOn != "" {
			t.Errorf("%s: in_development pin must not claim a publication date", fw.ID)
		}
	}
}

// TestMilestoneRefsResolveAndDatesLiveOnlyInCalendar: every Control.MilestoneIDs entry
// resolves; the EU AI Act controls whose timing the omnibus re-baselined are actually
// linked; and NO control text carries a literal application date — dates live only in
// the calendar (the "cero fechas muertas" bar).
func TestMilestoneRefsResolveAndDatesLiveOnlyInCalendar(t *testing.T) {
	dateRe := regexp.MustCompile(`\b20\d{2}-\d{2}-\d{2}\b`)
	for _, fw := range catalog {
		for _, c := range fw.Controls {
			for _, mid := range c.MilestoneIDs {
				if _, ok := milestoneByID[mid]; !ok {
					t.Errorf("%s/%s: milestone ref %q does not resolve", fw.ID, c.ID, mid)
				}
			}
			for field, text := range map[string]string{"title": c.Title, "requirement": c.Requirement, "criterion": c.Criterion, "note": c.Note} {
				if m := dateRe.FindString(text); m != "" {
					t.Errorf("%s/%s: %s carries a literal date %q — dates must live in the calendar", fw.ID, c.ID, field, m)
				}
			}
		}
		// Framework-level prose (name/authority/disclaimer) must not carry application
		// dates either; the Version string and the Pin are the only sanctioned spots.
		for field, text := range map[string]string{"name": fw.Name, "authority": fw.Authority} {
			if m := dateRe.FindString(text); m != "" {
				t.Errorf("%s: %s carries a literal date %q", fw.ID, field, m)
			}
		}
	}
	eu := frameworkByID["eu_ai_act"]
	linked := map[string]bool{}
	for _, c := range eu.Controls {
		if len(c.MilestoneIDs) > 0 {
			linked[c.ID] = true
		}
	}
	for _, id := range []string{"art_5", "art_6", "art_50", "art_9", "art_14", "art_72"} {
		if !linked[id] {
			t.Errorf("eu_ai_act/%s: must link its calendar milestones (the re-baseline)", id)
		}
	}
}

// TestAILDPurged: the catalog must not reference the withdrawn AI Liability Directive
// anywhere; the ONLY sanctioned trace is the calendar milestone recording the
// withdrawal itself (status passed, effect says withdrawn).
func TestAILDPurged(t *testing.T) {
	raw, err := json.Marshal(catalog)
	if err != nil {
		t.Fatal(err)
	}
	for _, needle := range []string{"AILD", "AI Liability"} {
		if strings.Contains(string(raw), needle) {
			t.Errorf("catalog must not reference %q (the AILD was withdrawn)", needle)
		}
	}
	ms, ok := milestoneByID["eu_aild.withdrawn"]
	if !ok {
		t.Fatal("the AILD withdrawal must be recorded as a calendar milestone")
	}
	if ms.Status != MilestonePassed || !strings.Contains(strings.ToLower(ms.Effect), "withdraw") {
		t.Errorf("eu_aild.withdrawn must be a passed milestone recording the withdrawal; got %+v", ms)
	}
}

// TestOWASPAgenticTop10Entry: the 2026 Top 10 is a NEW entry with the ten official
// ASI ids in order, distinct from (and alongside) the T1–T15 threat taxonomy.
func TestOWASPAgenticTop10Entry(t *testing.T) {
	fw, ok := frameworkByID["owasp_agentic_top10"]
	if !ok {
		t.Fatal("owasp_agentic_top10 missing from catalog")
	}
	want := []string{"ASI01", "ASI02", "ASI03", "ASI04", "ASI05", "ASI06", "ASI07", "ASI08", "ASI09", "ASI10"}
	if len(fw.Controls) != len(want) {
		t.Fatalf("owasp_agentic_top10 has %d controls, want %d", len(fw.Controls), len(want))
	}
	for i, c := range fw.Controls {
		if c.ID != want[i] {
			t.Errorf("control[%d] = %s, want %s", i, c.ID, want[i])
		}
		if len(c.Capabilities) == 0 {
			t.Errorf("%s: every Top 10 entry must map to at least one capability", c.ID)
		}
	}
	if fw.Pin.PublishedOn != "2025-12-09" {
		t.Errorf("owasp_agentic_top10 pin.published_on = %q, want 2025-12-09", fw.Pin.PublishedOn)
	}
	if _, ok := frameworkByID["owasp_agentic_tm"]; !ok {
		t.Error("owasp_agentic_tm (T1–T15) must remain alongside the Top 10 — the Top 10 complements, not supersedes")
	}
	if !strings.Contains(fw.Disclaimer, "COMPLEMENTS") {
		t.Error("disclaimer must state the Top 10 complements the threat taxonomy")
	}
}

// TestFiveEyesRealCategories: cisa_agentic_adoption carries the FIVE OFFICIAL risk
// categories of the 2026-05-01 joint guidance — not the earlier generic placeholders.
func TestFiveEyesRealCategories(t *testing.T) {
	fw, ok := frameworkByID["cisa_agentic_adoption"]
	if !ok {
		t.Fatal("cisa_agentic_adoption missing from catalog")
	}
	want := []string{"privilege_risks", "design_configuration_risks", "behaviour_risks", "structural_risks", "accountability_risks"}
	if len(fw.Controls) != len(want) {
		t.Fatalf("cisa_agentic_adoption has %d controls, want the 5 official categories", len(fw.Controls))
	}
	for i, c := range fw.Controls {
		if c.ID != want[i] {
			t.Errorf("control[%d] = %s, want %s", i, c.ID, want[i])
		}
		if len(c.Capabilities) == 0 {
			t.Errorf("%s: every category must map to capabilities", c.ID)
		}
	}
	if fw.Pin.PublishedOn != "2026-05-01" {
		t.Errorf("pin.published_on = %q, want 2026-05-01", fw.Pin.PublishedOn)
	}
	// The old generic placeholder ids must be gone.
	for _, c := range fw.Controls {
		switch c.ID {
		case "least_privilege", "agent_identity", "monitoring", "human_oversight", "supply_chain":
			t.Errorf("generic placeholder control %q must be replaced by the official categories", c.ID)
		}
	}
}

// TestAIDataSecurityCrosswalk: the CISA/NSA/FBI AI Data Security CSI is a separate
// pinned entry whose ten best practices map honestly — incl. the secure-deletion
// honest gap (unmapped) and the FIPS milestone link.
func TestAIDataSecurityCrosswalk(t *testing.T) {
	fw, ok := frameworkByID["cisa_ai_data_security"]
	if !ok {
		t.Fatal("cisa_ai_data_security missing from catalog")
	}
	if len(fw.Controls) != 10 {
		t.Fatalf("cisa_ai_data_security has %d controls, want the 10 best practices", len(fw.Controls))
	}
	if fw.Pin.PublishedOn != "2025-05-22" {
		t.Errorf("pin.published_on = %q, want 2025-05-22", fw.Pin.PublishedOn)
	}
	var deletion *Control
	for i := range fw.Controls {
		if fw.Controls[i].ID == "secure_deletion" {
			deletion = &fw.Controls[i]
		}
		if fw.Controls[i].ID == "secure_storage" {
			found := false
			for _, mid := range fw.Controls[i].MilestoneIDs {
				if mid == "fips_140_2.historical" {
					found = true
				}
			}
			if !found {
				t.Error("secure_storage must link the FIPS 140-2 sunset milestone")
			}
		}
	}
	if deletion == nil || deletion.Capabilities != nil {
		t.Error("secure_deletion must be an honest nil-capability gap (media sanitization is an estate duty)")
	}
}

// TestPLDDefenseEntryHonesty: the PLD defense-evidence crosswalk exists, links the
// transposition milestone, uses session_recording, and NEVER reads satisfied on an
// empty tenant (live evidence required).
func TestPLDDefenseEntryHonesty(t *testing.T) {
	fw, ok := frameworkByID["eu_pld"]
	if !ok {
		t.Fatal("eu_pld missing from catalog")
	}
	if len(fw.Controls) != 2 {
		t.Fatalf("eu_pld has %d controls, want 2 (Art 9 disclosure, Art 10 presumptions)", len(fw.Controls))
	}
	hasRecording := false
	for _, k := range fw.Controls[0].Capabilities {
		if k == "session_recording" {
			hasRecording = true
		}
	}
	if !hasRecording {
		t.Error("art_9_disclosure must map session_recording as disclosure-grade evidence")
	}

	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "pld-org")
	viewer := h.roleToken(admin, tenant, "v@x.io", auth.RoleViewer)
	r := h.do("GET", "/v1/m/compliance/frameworks/eu_pld/status", viewer, nil, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("status = %d %s", r.code, r.raw)
	}
	assessment := r.body["assessment"].(map[string]any)
	for _, c := range assessment["controls"].([]any) {
		cm := c.(map[string]any)
		if cm["status"] == string(StatusSatisfied) {
			t.Errorf("eu_pld %v must not be satisfied on an empty tenant", cm["control_id"])
		}
	}
}

// TestSessionRecordingCapability: absent without rows; present once a privileged
// session is recorded (probed via the recording.session stand-in, decoupled by kind).
func TestSessionRecordingCapability(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "rec-org")
	viewer := h.roleToken(admin, tenant, "v@x.io", auth.RoleViewer)

	state := func() string {
		r := h.do("GET", "/v1/m/compliance/capabilities", viewer, nil, tenantHdr(tenant))
		if r.code != http.StatusOK {
			t.Fatalf("capabilities = %d %s", r.code, r.raw)
		}
		for _, c := range r.body["capabilities"].([]any) {
			cm := c.(map[string]any)
			if cm["key"] == "session_recording" {
				return cm["state"].(string)
			}
		}
		t.Fatal("session_recording capability missing from the evidence map")
		return ""
	}
	if got := state(); got != string(EvidenceAbsent) {
		t.Errorf("session_recording on empty tenant = %s, want absent", got)
	}
	h.mutate(tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(recordingSessionStandInKind)
		if err != nil {
			return err
		}
		_, err = repo.Create(context.Background(), model.Record{"subject": "breakglass", "status": "closed"})
		return err
	})
	if got := state(); got != string(EvidencePresent) {
		t.Errorf("session_recording after a recorded session = %s, want present", got)
	}
}

// TestCalendarEndpoint: the calendar is served read-tier with milestones, watchlist
// and the dates-as-data disclaimer; ?framework= filters to one catalog entry.
func TestCalendarEndpoint(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "cal-org")
	viewer := h.roleToken(admin, tenant, "v@x.io", auth.RoleViewer)

	r := h.do("GET", "/v1/m/compliance/calendar", viewer, nil, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("calendar = %d %s", r.code, r.raw)
	}
	if len(r.body["milestones"].([]any)) != len(regulatoryCalendar) {
		t.Error("unfiltered calendar must return every milestone")
	}
	if len(r.body["watchlist"].([]any)) != len(regulatoryWatchlist) {
		t.Error("unfiltered calendar must return the watchlist")
	}
	if d, _ := r.body["disclaimer"].(string); !strings.Contains(d, "provisional_agreement") {
		t.Error("calendar disclaimer must spell out the provisional_agreement honesty line")
	}

	r = h.do("GET", "/v1/m/compliance/calendar?framework=eu_pld", viewer, nil, tenantHdr(tenant))
	for _, m := range r.body["milestones"].([]any) {
		if m.(map[string]any)["framework_id"] != "eu_pld" {
			t.Errorf("filtered calendar leaked milestone %v", m.(map[string]any)["id"])
		}
	}
	if len(r.body["milestones"].([]any)) == 0 {
		t.Error("eu_pld filter must return its milestones")
	}
}

// TestDORAExportMode: the export carries the corrected DORA anchors (no fictitious
// Art 9(10)), the tenant's risk register, the incident timeline, the third-party AI
// register and a ledger anchor — and self-audits as a sensitive evidence read.
func TestDORAExportMode(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "dora-org")
	editor := h.roleToken(admin, tenant, "e@x.io", auth.RoleEditor)

	agent := h.seedAgent(tenant, "fin-agent")
	h.seedFinding(tenant, agent, "guardrail", model.SeverityHigh)
	h.seedGPAIPosture(tenant, "anthropic", true)
	if r := h.do("POST", "/v1/m/compliance/risk/classify", editor, map[string]any{"subject_ref": agent.String()}, tenantHdr(tenant)); r.code != http.StatusCreated {
		t.Fatalf("classify = %d %s", r.code, r.raw)
	}

	r := h.do("GET", "/v1/m/compliance/dora", editor, nil, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("dora = %d %s", r.code, r.raw)
	}
	if n := len(r.body["risk_register"].([]any)); n != 1 {
		t.Errorf("risk_register has %d entries, want 1", n)
	}
	if n := len(r.body["incident_timeline"].([]any)); n == 0 {
		t.Error("incident_timeline must carry the seeded finding")
	}
	tp := r.body["third_party_register"].([]any)
	if len(tp) != 1 || tp[0].(map[string]any)["provider_ref"] != "anthropic" {
		t.Errorf("third_party_register must carry the GPAI posture row; got %v", tp)
	}
	anchor, _ := r.body["ledger_anchor"].(map[string]any)
	if anchor == nil || anchor["integrity_ok"] != true {
		t.Errorf("ledger anchor must verify; got %v", anchor)
	}
	basis, _ := json.Marshal(r.body["basis"])
	for _, want := range []string{"Art 6(1)", "Art 8(1)", "Art 10(1)", "Art 17(1)", "Art 28(3)"} {
		if !strings.Contains(string(basis), want) {
			t.Errorf("basis must cite %s", want)
		}
	}
	if strings.Contains(string(basis), "9(10)") {
		t.Error("basis must NOT cite the non-existent Art 9(10)")
	}
	// Auditor-facing citation data must carry DORA's FULL official title.
	if reg, _ := r.body["regulation"].(string); !strings.Contains(reg, "amending Regulations (EC) No 1060/2009") {
		t.Errorf("regulation must carry the full official title; got %q", reg)
	}
	if !strings.Contains(strings.Join(h.auditActions(tenant), ","), "compliance.dora.export") {
		t.Error("the DORA export must self-audit")
	}
}
