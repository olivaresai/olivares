// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package compliance

import (
	"strings"
	"testing"

	"github.com/olivaresai/olivares/modules/redteam"
)

// catalog completeness. Three NEW autonomous frameworks (CSA AICM, OWASP Top 10
// for LLM Applications 2025, MITRE ATLAS technique coverage) plus the OSCAL v1.2.2 bump
// and Control-Mapping document (the OSCAL assertions live in TestOSCALExport, s68_test.go).
// These tests lock the COUNTS (243/18, 10, 9), the HONEST statuses (gaps stay gaps; the
// LLM09 refutation; nil-capability estate domains), and the version pins — real coverage,
// not humo. The NIST GenAI Profile stays at its verified 12 risks and OWASP Agentic T&M
// at v1.0 (both brief premises for a 13th risk / a v1.1 were disproven against the
// primary sources), so there is deliberately no test here
// asserting a 13th risk or a T&M v1.1 — that would lock in fabricated data.

// TestAICMFramework: the CSA AI Controls Matrix is a NEW entry with all 18 domains
// (CCM v4's 17 + the AI-specific Model Security MDS), 247 objectives documented as data,
// STAR-for-AI Level 1 positioned as design-toward (not a certification), and honest nil gaps for
// the estate/organizational domains.
func TestAICMFramework(t *testing.T) {
	fw, ok := frameworkByID["csa_aicm"]
	if !ok {
		t.Fatal("csa_aicm missing from catalog")
	}
	if len(fw.Controls) != 18 {
		t.Fatalf("csa_aicm has %d domains, want 18 (CCM v4's 17 + Model Security)", len(fw.Controls))
	}
	byID := map[string]Control{}
	for _, c := range fw.Controls {
		byID[c.ID] = c
	}
	for _, id := range []string{"A&A", "AIS", "BCR", "CCC", "CEK", "DCS", "DSP", "GRC", "HRS", "IAM", "IPY", "IVS", "LOG", "MDS", "SEF", "STA", "TVM", "UEM"} {
		if _, ok := byID[id]; !ok {
			t.Errorf("csa_aicm missing domain %q", id)
		}
	}
	// The AI-specific domain (the one AICM adds beyond CCM v4) is Model Security and must
	// map the AI-specific capabilities.
	mds := byID["MDS"]
	if mds.Title != "Model Security" {
		t.Errorf("MDS must be the AI-specific Model Security domain; got title %q", mds.Title)
	}
	if !hasCap(mds, "signed_model_admission") || !hasCap(mds, "model_aibom") {
		t.Errorf("MDS (Model Security) must map signed_model_admission + model_aibom; got %v", mds.Capabilities)
	}
	// The counts live as auditor-facing DATA in the disclaimer (no prose decay), and the
	// STAR-for-AI assurance program is positioned as design-toward, never a certification.
	if !strings.Contains(fw.Disclaimer, "247") || !strings.Contains(fw.Disclaimer, "18 security domains") {
		t.Errorf("disclaimer must document 247 control objectives across 18 security domains; got %q", fw.Disclaimer)
	}
	if !strings.Contains(fw.Disclaimer, "STAR for AI") || !strings.Contains(strings.ToLower(fw.Disclaimer), "not a certification") {
		t.Errorf("disclaimer must position STAR-for-AI as design-toward and disclaim certification; got %q", fw.Disclaimer)
	}
	if fw.Pin.PublishedOn != "2026-06-22" || fw.Pin.Status != PinGuidance {
		t.Errorf("csa_aicm pin = {published %q status %q}, want 2026-06-22 / guidance", fw.Pin.PublishedOn, fw.Pin.Status)
	}

	// Honest statuses on an empty tenant: the estate/organizational domains are nil-cap
	// unmapped gaps; an all-architectural domain is by_design (never satisfied); a pure
	// operational domain with no data is a gap; and NO domain is laundered to satisfied.
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "aicm-org")
	tok := h.roleToken(admin, tenant, "v@x.io", "viewer")
	st := h.statuses(tok, tenant, "csa_aicm")
	for _, id := range []string{"BCR", "DCS", "HRS", "IPY", "UEM"} {
		if byID[id].Capabilities != nil {
			t.Errorf("%s must be an honest nil-capability gap (estate/org duty); got %v", id, byID[id].Capabilities)
		}
		if st[id] != string(StatusUnmapped) {
			t.Errorf("%s must be unmapped on an empty tenant; got %q", id, st[id])
		}
	}
	if st["IVS"] != string(StatusByDesign) {
		t.Errorf("IVS (architectural-only) must be by_design, never satisfied; got %q", st["IVS"])
	}
	if st["TVM"] != string(StatusGap) {
		t.Errorf("TVM (operational caps absent) must be a gap on an empty tenant; got %q", st["TVM"])
	}
	for id, status := range st {
		if status == string(StatusSatisfied) {
			t.Errorf("no AICM domain may be satisfied on an empty tenant (honest); %s was %q", id, status)
		}
	}
}

// TestLLMTop10Framework: the OWASP Top 10 for LLM Applications 2025 is a NEW entry with
// the ten official LLM01:2025–LLM10:2025 ids in order, and LLM09 Misinformation is the
// DELIBERATE honest gap (nil capabilities) — the plane does not detect misinformation in
// model output.
func TestLLMTop10Framework(t *testing.T) {
	fw, ok := frameworkByID["llm_top10"]
	if !ok {
		t.Fatal("llm_top10 missing from catalog")
	}
	want := []string{
		"LLM01:2025", "LLM02:2025", "LLM03:2025", "LLM04:2025", "LLM05:2025",
		"LLM06:2025", "LLM07:2025", "LLM08:2025", "LLM09:2025", "LLM10:2025",
	}
	if len(fw.Controls) != len(want) {
		t.Fatalf("llm_top10 has %d controls, want 10", len(fw.Controls))
	}
	byID := map[string]Control{}
	for i, c := range fw.Controls {
		if c.ID != want[i] {
			t.Errorf("control[%d] = %s, want %s", i, c.ID, want[i])
		}
		byID[c.ID] = c
	}
	// LLM09 Misinformation is the deliberate refutation: a nil-capability honest gap.
	if byID["LLM09:2025"].Title != "Misinformation" {
		t.Errorf("LLM09:2025 title = %q, want Misinformation", byID["LLM09:2025"].Title)
	}
	if byID["LLM09:2025"].Capabilities != nil {
		t.Errorf("LLM09:2025 Misinformation must be a nil-capability honest gap; got %v", byID["LLM09:2025"].Capabilities)
	}
	if !strings.Contains(fw.Disclaimer, "LLM09") {
		t.Error("disclaimer must call out the deliberate LLM09 honest gap")
	}
	// Every other risk maps real coverage (anchored to the guardrail / red-team probes).
	for _, c := range fw.Controls {
		if c.ID == "LLM09:2025" {
			continue
		}
		if len(c.Capabilities) == 0 {
			t.Errorf("%s must map at least one capability", c.ID)
		}
	}
	if fw.Pin.PublishedOn != "2024-11-18" || fw.Pin.Status != PinGuidance {
		t.Errorf("llm_top10 pin = {published %q status %q}, want 2024-11-18 / guidance", fw.Pin.PublishedOn, fw.Pin.Status)
	}

	// On an empty tenant LLM09 is unmapped and the injection-class risks are not laundered
	// to satisfied without live detection/probe evidence.
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "llm-org")
	tok := h.roleToken(admin, tenant, "v@x.io", "viewer")
	st := h.statuses(tok, tenant, "llm_top10")
	if st["LLM09:2025"] != string(StatusUnmapped) {
		t.Errorf("LLM09:2025 must be unmapped on any tenant; got %q", st["LLM09:2025"])
	}
	if st["LLM01:2025"] == string(StatusSatisfied) {
		t.Errorf("LLM01:2025 must not be satisfied without live detection/probe findings; got %q", st["LLM01:2025"])
	}
}

// TestATLASFramework: MITRE ATLAS is a NEW entry whose technique ids + VERBATIM titles
// are REUSED from the red-team battery's verified ATLAS snapshot (modules/redteam/
// atlas.go), pinned in LOCKSTEP to that release so the dated-snapshot guarantee cannot
// silently drift.
func TestATLASFramework(t *testing.T) {
	fw, ok := frameworkByID["mitre_atlas"]
	if !ok {
		t.Fatal("mitre_atlas missing from catalog")
	}
	want := map[string]string{
		"AML.T0024":     "Exfiltration via AI Inference API",
		"AML.T0051.000": "LLM Prompt Injection: Direct",
		"AML.T0051.001": "LLM Prompt Injection: Indirect",
		"AML.T0054":     "LLM Jailbreak",
		"AML.T0056":     "Extract LLM System Prompt",
		"AML.T0057":     "LLM Data Leakage",
		"AML.T0104":     "Publish Poisoned AI Agent Tool",
		"AML.T0105":     "Escape to Host",
		"AML.T0110":     "AI Agent Tool Poisoning",
	}
	if len(fw.Controls) != len(want) {
		t.Fatalf("mitre_atlas has %d techniques, want %d", len(fw.Controls), len(want))
	}
	for _, c := range fw.Controls {
		title, ok := want[c.ID]
		if !ok {
			t.Errorf("unexpected technique %q (not in the battery's verified ATLAS set)", c.ID)
			continue
		}
		if c.Title != title {
			t.Errorf("%s title = %q, want the verbatim atlas-data title %q", c.ID, c.Title, title)
		}
		// Each covered technique is evidenced by guardrail detection + red-team probes.
		if !hasCap(c, "threat_detection") || !hasCap(c, "adversarial_testing") {
			t.Errorf("%s must map threat_detection + adversarial_testing; got %v", c.ID, c.Capabilities)
		}
	}
	// LOCKSTEP with the battery's verified release — the dated-snapshot claim is only
	// honest if compliance and red-team cannot drift to different ATLAS versions.
	if !strings.Contains(fw.Pin.Document, redteam.ATLASVersion) {
		t.Errorf("atlas pin document %q must reference the battery's verified release %q", fw.Pin.Document, redteam.ATLASVersion)
	}
	if fw.Pin.PublishedOn != redteam.ATLASVersionAsOf {
		t.Errorf("atlas pin published_on = %q, want the battery's as-of date %q", fw.Pin.PublishedOn, redteam.ATLASVersionAsOf)
	}
	if fw.Pin.Status != PinGuidance {
		t.Errorf("atlas pin status = %q, want guidance", fw.Pin.Status)
	}
	// Honesty: a dated snapshot, never a continuous-parity / full-matrix / certification claim.
	if !strings.Contains(fw.Disclaimer, "DATED SNAPSHOT") || !strings.Contains(strings.ToLower(fw.Disclaimer), "not a certification") {
		t.Errorf("atlas disclaimer must be a dated-snapshot, no-certification claim; got %q", fw.Disclaimer)
	}
}

func hasCap(c Control, key CapabilityKey) bool {
	for _, k := range c.Capabilities {
		if k == key {
			return true
		}
	}
	return false
}
