// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package redteam

import (
	"net/http"
	"testing"
)

func TestComputeScorecardExcludesSkippedAndErrorsFromScore(t *testing.T) {
	outcomes := []probeOutcome{
		{probe: Probe{ID: "p1", Family: familyInjection, OWASP: "LLM01"}, result: ProbeResult{Outcome: OutcomeBlocked}},
		{probe: Probe{ID: "p2", Family: familyInjection, OWASP: "LLM01"}, result: ProbeResult{Outcome: OutcomeRefused}},
		{probe: Probe{ID: "p3", Family: familyInjection, OWASP: "LLM01"}, result: ProbeResult{Outcome: OutcomeComplied}},
		{probe: Probe{ID: "p4", Family: familyExfil, OWASP: "LLM02"}, result: ProbeResult{Outcome: OutcomeLeaked}},
		{probe: Probe{ID: "p5", Family: familyExfil, OWASP: "LLM02"}, result: ProbeResult{Outcome: OutcomeSkipped}},
		{probe: Probe{ID: "p6", Family: familyExfil, OWASP: "LLM02"}, result: ProbeResult{Outcome: OutcomeError}},
	}
	card := computeScorecard(outcomes)
	if card.total != 6 || card.passed != 2 || card.failed != 2 || card.skipped != 1 || card.errors != 1 {
		t.Fatalf("scorecard counts = %+v", card)
	}
	if card.score != 50 || card.status != "completed" {
		t.Fatalf("score/status = %.1f/%s, want 50/completed", card.score, card.status)
	}
	if card.owaspFailures["LLM01"] != 1 || card.owaspFailures["LLM02"] != 1 {
		t.Fatalf("owasp failures = %+v", card.owaspFailures)
	}
	if card.byFamily[familyExfil].Skipped != 1 || card.byFamily[familyExfil].Errors != 1 {
		t.Fatalf("exfil family score = %+v", card.byFamily[familyExfil])
	}

	if got := computeScorecard([]probeOutcome{{result: ProbeResult{Outcome: OutcomeSkipped}}}); got.status != "degraded" || got.score != 0 {
		t.Fatalf("all skipped = %+v, want degraded zero score", got)
	}
	if got := computeScorecard([]probeOutcome{{result: ProbeResult{Outcome: OutcomeError}}}); got.status != "error" {
		t.Fatalf("all error status = %s, want error", got.status)
	}
	if got := computeScorecard(nil); got.status != "completed" || got.total != 0 {
		t.Fatalf("empty scorecard = %+v, want completed empty", got)
	}
}

func TestRunListAndGetExposeStoredBreakdown(t *testing.T) {
	h := newHarness(t, fakeSandbox{comply: map[string]bool{familyExfil: true}})
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	targetID := h.registerAuthorizedTarget(admin, tenant, "agent-scorecard")

	launch := h.do("POST", "/v1/m/redteam/runs", admin, map[string]any{
		"target_ref": targetID,
		"suite":      familyExfil,
	}, tenantHdr(tenant))
	if launch.code != http.StatusCreated {
		t.Fatalf("launch = %d %s", launch.code, launch.raw)
	}
	runID := launch.body["id"].(string)

	list := h.do("GET", "/v1/m/redteam/runs?suite="+familyExfil+"&target_ref="+targetID, admin, nil, tenantHdr(tenant))
	if list.code != http.StatusOK {
		t.Fatalf("list runs = %d %s", list.code, list.raw)
	}
	items := list.body["items"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["id"] != runID {
		t.Fatalf("list items = %+v, want run %s", items, runID)
	}

	get := h.do("GET", "/v1/m/redteam/runs/"+runID, admin, nil, tenantHdr(tenant))
	if get.code != http.StatusOK {
		t.Fatalf("get run = %d %s", get.code, get.raw)
	}
	byFamily := get.body["by_family"].(map[string]any)
	exfil := byFamily[familyExfil].(map[string]any)
	if intOf(exfil["Failed"]) != len(exfilProbes()) || intOf(exfil["Passed"]) != 0 {
		t.Fatalf("exfil breakdown = %+v, want all failed", exfil)
	}
	owasp := get.body["owasp_failures"].(map[string]any)
	if len(owasp) == 0 {
		t.Fatal("expected OWASP failure counts on get run")
	}
}
