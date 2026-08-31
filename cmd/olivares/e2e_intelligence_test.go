// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

// Intelligence layer (modules XII, XIII, XVII, XVIII): evals, sandbox, compliance
// and red-team. The suite asserts the HONEST outcome of each — including the
// seams that are deny-closed/degraded by design in the composition root (the
// offline red-team sandbox), which must report degraded, never a faked pass.

import (
	"net/http"
	"testing"

	"github.com/olivaresai/olivares/cmd/olivares/seed"
)

func TestE2E_Evals_DeterministicScorerCompletes(t *testing.T) {
	h := newHarness(t)

	var suite struct {
		ID string `json:"id"`
	}
	if code := h.reqInto("POST", "/v1/m/evals/suites", h.adminToken, h.tenantA, map[string]any{
		"name": "greeting", "subject_kind": "agent", "scorer": "contains", "pass_threshold": 1,
	}, &suite); code != http.StatusCreated || suite.ID == "" {
		t.Fatalf("create suite = %d", code)
	}
	if code, raw := h.req("POST", "/v1/m/evals/suites/"+suite.ID+"/cases", h.adminToken, h.tenantA, map[string]any{
		"case_key": "c1", "input": "say hi", "expected": "hello",
	}); code != http.StatusCreated {
		t.Fatalf("add case = %d: %s", code, raw)
	}
	var run struct {
		Status string  `json:"status"`
		Passed float64 `json:"passed"`
	}
	if code := h.reqInto("POST", "/v1/m/evals/runs", h.adminToken, h.tenantA, map[string]any{
		"suite_ref": suite.ID, "subject_ref": "agent-x", "outputs": map[string]any{"c1": "hello world"},
	}, &run); code != http.StatusCreated && code != http.StatusOK {
		t.Fatalf("run = %d", code)
	}
	assertEq(t, "eval.status", run.Status, "completed")
	if run.Passed < 1 {
		t.Errorf("eval passed = %v, want >=1 (contains scorer, deterministic)", run.Passed)
	}
}

func TestE2E_Sandbox_IsolatedDeterministicRun(t *testing.T) {
	h := newHarness(t)

	var scn struct {
		ID string `json:"id"`
	}
	if code := h.reqInto("POST", "/v1/m/sandbox/scenarios", h.adminToken, h.tenantA, map[string]any{
		"name": "ping-pong", "subject_kind": "agent",
		"steps": []map[string]any{{"key": "s1", "input": "ping"}},
		"mocks": []map[string]any{{"resource": "ping", "response": "pong"}},
	}, &scn); code != http.StatusCreated || scn.ID == "" {
		t.Fatalf("create scenario = %d", code)
	}
	var run struct {
		Runner    string  `json:"runner"`
		Isolated  bool    `json:"isolated"`
		Destroyed bool    `json:"destroyed"`
		StepsOK   float64 `json:"steps_ok"`
	}
	if code := h.reqInto("POST", "/v1/m/sandbox/scenarios/"+scn.ID+"/run", h.adminToken, h.tenantA, map[string]any{}, &run); code != http.StatusCreated && code != http.StatusOK {
		t.Fatalf("run scenario = %d", code)
	}
	assertEq(t, "sandbox.runner", run.Runner, "inproc-mock")
	assertEq(t, "sandbox.isolated", run.Isolated, true)
	assertEq(t, "sandbox.destroyed", run.Destroyed, true)
	if run.StepsOK < 1 {
		t.Errorf("sandbox steps_ok = %v, want >=1", run.StepsOK)
	}
}

func TestE2E_Redteam_DegradedHonestPath(t *testing.T) {
	h := newHarness(t)

	// A run against an UNauthorized target is refused.
	var tgt struct {
		ID string `json:"id"`
	}
	if code := h.reqInto("POST", "/v1/m/redteam/targets", h.adminToken, h.tenantA, map[string]any{
		"agent_ref": seed.AgentCoder, "name": "coder-target",
	}, &tgt); code != http.StatusCreated || tgt.ID == "" {
		t.Fatalf("create target = %d", code)
	}
	if code, _ := h.req("POST", "/v1/m/redteam/runs", h.adminToken, h.tenantA, map[string]any{
		"target_ref": tgt.ID, "suite": "injection",
	}); code != http.StatusForbidden {
		t.Errorf("run against unauthorized target = %d, want 403 (consent gate)", code)
	}

	// After explicit authorization, the run executes but is HONESTLY degraded —
	// no OS-level sandbox is wired, so every probe is skipped, never a faked pass.
	if code, raw := h.req("POST", "/v1/m/redteam/targets/"+tgt.ID+"/authorize", h.adminToken, h.tenantA, map[string]any{
		"authorized": true,
	}); code != http.StatusOK && code != http.StatusCreated {
		t.Fatalf("authorize = %d: %s", code, raw)
	}
	var run struct {
		Status  string  `json:"status"`
		Total   float64 `json:"total"`
		Skipped float64 `json:"skipped"`
		Score   float64 `json:"score"`
	}
	if code := h.reqInto("POST", "/v1/m/redteam/runs", h.adminToken, h.tenantA, map[string]any{
		"target_ref": tgt.ID, "suite": "injection",
	}, &run); code != http.StatusCreated && code != http.StatusOK {
		t.Fatalf("authorized run = %d", code)
	}
	assertEq(t, "redteam.status", run.Status, "degraded")
	assertEq(t, "redteam.score", run.Score, float64(0))
	if run.Total == 0 || run.Skipped != run.Total {
		t.Errorf("redteam skipped=%v total=%v, want skipped==total (offline sandbox)", run.Skipped, run.Total)
	}
}

func TestE2E_Compliance_EvidenceFromLedger(t *testing.T) {
	h := newHarness(t)

	frameworks := h.getJSON(h.adminToken, h.tenantA, "/v1/m/compliance/frameworks")
	fws := items(frameworks)
	if len(fws) == 0 {
		t.Fatal("no compliance frameworks cataloged")
	}
	fwID, _ := fws[0]["id"].(string)

	// Capabilities are derived live from real tenant rows (audit head, findings, …)
	// — the seeded estate makes several present rather than all-gap.
	caps := h.getJSON(h.adminToken, h.tenantA, "/v1/m/compliance/capabilities")
	present := 0
	for _, c := range items2(caps, "capabilities") {
		if c["state"] == "present" {
			present++
		}
	}
	if present == 0 {
		t.Error("no compliance capability present despite a populated estate")
	}

	// Sealing an evidence package proves the audit chain live (integrity_ok).
	var pkg struct {
		IntegrityOK bool `json:"integrity_ok"`
	}
	if code := h.reqInto("POST", "/v1/m/compliance/frameworks/"+fwID+"/evidence", h.adminToken, h.tenantA, map[string]any{
		"scope_note": "e2e",
	}, &pkg); code != http.StatusCreated && code != http.StatusOK {
		t.Fatalf("seal evidence = %d", code)
	}
	assertEq(t, "evidence.integrity_ok", pkg.IntegrityOK, true)
}
