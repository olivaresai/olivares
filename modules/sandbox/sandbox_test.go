// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sandbox

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/model"
)

// TestRunScenarioIsolatedAndDestroyed runs a scenario whose first step hits a mock and
// whose second has NO mock: the run is completed, isolated, destroyed; the mocked step
// yields the mock response; the un-mocked step yields the deterministic mock-miss
// marker and NEVER an error reaching a real resource.
func TestRunScenarioIsolatedAndDestroyed(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")

	scenID := h.createScenario(admin, tenant, "checkout",
		[]map[string]any{
			{"key": "s1", "input": "mcp:weather"},
			{"key": "s2", "input": "mcp:secret-db"}, // no mock for this one
		},
		[]map[string]any{
			{"resource": "mcp:weather", "response": "sunny, 24C"},
		})

	r := h.do("POST", "/v1/m/sandbox/scenarios/"+scenID+"/run", admin, map[string]any{}, tenantHdr(tenant))
	if r.code != http.StatusCreated {
		t.Fatalf("run = %d %s", r.code, r.raw)
	}
	if r.body["runner"] != "inproc-mock" {
		t.Fatalf("runner = %v, want inproc-mock", r.body["runner"])
	}
	if r.body["isolated"] != true {
		t.Fatalf("isolated = %v, want true", r.body["isolated"])
	}
	if r.body["destroyed"] != true {
		t.Fatalf("destroyed = %v, want true", r.body["destroyed"])
	}
	if r.body["status"] != "completed" {
		t.Fatalf("status = %v, want completed", r.body["status"])
	}
	if got := intOf(r.body["steps_total"]); got != 2 {
		t.Fatalf("steps_total = %d, want 2", got)
	}
	if got := intOf(r.body["steps_ok"]); got != 1 {
		t.Fatalf("steps_ok = %d, want 1 (one mock hit)", got)
	}
	if got := intOf(r.body["steps_error"]); got != 1 {
		t.Fatalf("steps_error = %d, want 1 (one mock-miss)", got)
	}

	runID := r.body["id"].(string)
	out := h.do("GET", "/v1/m/sandbox/runs/"+runID+"/outputs", admin, nil, tenantHdr(tenant))
	if out.code != http.StatusOK {
		t.Fatalf("outputs = %d %s", out.code, out.raw)
	}
	items, _ := out.body["items"].([]any)
	if len(items) != 2 {
		t.Fatalf("outputs = %d, want 2", len(items))
	}
	byKey := map[string]map[string]any{}
	for _, it := range items {
		m := it.(map[string]any)
		byKey[m["step_key"].(string)] = m
	}
	if byKey["s1"]["mock_hit"] != true || byKey["s1"]["output"] != "sunny, 24C" {
		t.Fatalf("s1 = %#v, want mock hit with the mock response", byKey["s1"])
	}
	// The mock-miss step: a deterministic synthetic marker, never a real resource.
	if byKey["s2"]["mock_hit"] != false {
		t.Fatalf("s2 mock_hit = %v, want false", byKey["s2"]["mock_hit"])
	}
	wantMiss := "[[mock-miss:mcp:secret-db]]"
	if byKey["s2"]["output"] != wantMiss {
		t.Fatalf("s2 output = %v, want %q", byKey["s2"]["output"], wantMiss)
	}
}

// TestRunNotScoredWithoutScorer verifies that a run with a suite_ref but no Scorer
// wired is recorded executed-not-scored (status degraded, no score/passed), never a
// silent pass.
func TestRunNotScoredWithoutScorer(t *testing.T) {
	h := newHarness(t) // default unscored scorer
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	scenID := h.createScenario(admin, tenant, "s",
		[]map[string]any{{"key": "s1", "input": "r1"}},
		[]map[string]any{{"resource": "r1", "response": "ok"}})

	r := h.do("POST", "/v1/m/sandbox/scenarios/"+scenID+"/run", admin,
		map[string]any{"suite_ref": "11111111-1111-1111-1111-111111111111"}, tenantHdr(tenant))
	if r.code != http.StatusCreated {
		t.Fatalf("run = %d %s", r.code, r.raw)
	}
	if r.body["status"] != "degraded" {
		t.Fatalf("status = %v, want degraded (executed, not scored)", r.body["status"])
	}
	if _, ok := r.body["score"]; ok {
		t.Fatalf("score must be absent when not scored; got %v", r.body["score"])
	}
	if _, ok := r.body["passed"]; ok {
		t.Fatalf("passed must be absent when not scored; got %v", r.body["passed"])
	}
	if r.body["suite_ref"] != "11111111-1111-1111-1111-111111111111" {
		t.Fatalf("suite_ref = %v, want the requested suite recorded", r.body["suite_ref"])
	}
}

// TestRunScoredWithFakeScorer wires a LOCAL fakeScorer (the test-only stand-in for the
// XII adapter) and verifies a scored run records score + passed.
func TestRunScoredWithFakeScorer(t *testing.T) {
	h := newHarness(t, WithScorer(fakeScorer{want: "GOOD"}))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	scenID := h.createScenario(admin, tenant, "s",
		[]map[string]any{{"key": "s1", "input": "r1"}, {"key": "s2", "input": "r2"}},
		[]map[string]any{{"resource": "r1", "response": "GOOD answer"}, {"resource": "r2", "response": "GOOD again"}})

	r := h.do("POST", "/v1/m/sandbox/scenarios/"+scenID+"/run", admin,
		map[string]any{"suite_ref": "22222222-2222-2222-2222-222222222222"}, tenantHdr(tenant))
	if r.code != http.StatusCreated {
		t.Fatalf("run = %d %s", r.code, r.raw)
	}
	if r.body["status"] != "completed" {
		t.Fatalf("status = %v, want completed", r.body["status"])
	}
	if r.body["passed"] != true {
		t.Fatalf("passed = %v, want true (both outputs contain GOOD)", r.body["passed"])
	}
	if s, ok := r.body["score"].(float64); !ok || s != 1.0 {
		t.Fatalf("score = %v, want 1.0", r.body["score"])
	}
}

// TestReplayDeterminism wires a fakeHistory timeline and runs the same replay twice:
// the two runs must produce IDENTICAL output sets (same session_ref + mocks ⇒ same
// outputs — the runner is pure).
func TestReplayDeterminism(t *testing.T) {
	h := newHarness(t, WithHistorySource(fakeHistory{steps: []ReplayStep{
		{Key: "t1", Input: "tool:a"},
		{Key: "t2", Input: "tool:b"},
	}}))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")

	body := map[string]any{
		"session_ref": "sess-123",
		"mocks": []map[string]any{
			{"resource": "tool:a", "response": "A-out"},
			{"resource": "tool:b", "response": "B-out"},
		},
	}
	first := h.replay(admin, tenant, body)
	second := h.replay(admin, tenant, body)

	if first["t1"] != "A-out" || first["t2"] != "B-out" {
		t.Fatalf("first replay outputs = %#v, want A-out/B-out", first)
	}
	if first["t1"] != second["t1"] || first["t2"] != second["t2"] {
		t.Fatalf("replay not deterministic: %#v vs %#v", first, second)
	}
}

// TestReplayDegradedWithoutTimeline verifies the default core history yields zero
// steps ⇒ a degraded replay, never fabricated input.
func TestReplayDegradedWithoutTimeline(t *testing.T) {
	h := newHarness(t) // default core history → zero steps
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")

	r := h.do("POST", "/v1/m/sandbox/replay", admin, map[string]any{"session_ref": "sess-x"}, tenantHdr(tenant))
	if r.code != http.StatusCreated {
		t.Fatalf("replay = %d %s", r.code, r.raw)
	}
	if r.body["status"] != "degraded" {
		t.Fatalf("status = %v, want degraded (no reconstructable timeline)", r.body["status"])
	}
	if intOf(r.body["steps_total"]) != 0 {
		t.Fatalf("steps_total = %v, want 0 (never fabricated)", r.body["steps_total"])
	}
}

// TestCompareDifferentiatesVariants scores two variants with a fakeScorer where the
// candidate's expected marker matches and the baseline's does not, so the verdict is
// improved with a positive delta. (The runner is deterministic, so the differentiation
// comes from the scorer, exactly as in production where XII ranks the variants.)
func TestCompareDifferentiatesVariants(t *testing.T) {
	h := newHarness(t, WithScorer(variantScorer{}))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	scenID := h.createScenario(admin, tenant, "deploy-check",
		[]map[string]any{{"key": "s1", "input": "r1"}},
		[]map[string]any{{"resource": "r1", "response": "answer"}})

	r := h.do("POST", "/v1/m/sandbox/compare", admin, map[string]any{
		"scenario_ref": scenID, "baseline_variant": "v1", "candidate_variant": "v2",
		"suite_ref": "33333333-3333-3333-3333-333333333333",
	}, tenantHdr(tenant))
	if r.code != http.StatusCreated {
		t.Fatalf("compare = %d %s", r.code, r.raw)
	}
	if r.body["verdict"] != verdictImproved {
		t.Fatalf("verdict = %v, want improved", r.body["verdict"])
	}
	delta, _ := r.body["delta"].(float64)
	if delta <= 0 {
		t.Fatalf("delta = %v, want > 0", delta)
	}
	if bs, _ := r.body["baseline_score"].(float64); bs != 0 {
		t.Fatalf("baseline_score = %v, want 0", bs)
	}
	if cs, _ := r.body["candidate_score"].(float64); cs != 1 {
		t.Fatalf("candidate_score = %v, want 1", cs)
	}

	// It is recorded as append-only comparison evidence.
	list := h.do("GET", "/v1/m/sandbox/comparisons", admin, nil, tenantHdr(tenant))
	if list.code != http.StatusOK {
		t.Fatalf("comparisons = %d %s", list.code, list.raw)
	}
	items, _ := list.body["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("comparisons = %d, want 1", len(items))
	}
}

// variantScorer passes the candidate variant ("v2") and fails the baseline ("v1"), so
// compare can differentiate two variants over the SAME deterministic outputs.
type variantScorer struct{}

func (variantScorer) Score(_ context.Context, _ model.TenantID, req ScoreRequest) (ScoreVerdict, error) {
	v := ScoreVerdict{Total: len(req.Outputs)}
	if req.Variant == "v2" {
		v.PassedN, v.PassRate, v.Score, v.Passed = v.Total, 1, 1, v.Total > 0
	} else {
		v.FailedN = v.Total
	}
	return v, nil
}

// TestSyntheticDataAbsent asserts the POST-v1 extension point ships NO generator: the
// default produces ZERO samples + errSyntheticDataPostV1, there is no WithSynthetic*
// option, and no route generates data.
func TestSyntheticDataAbsent(t *testing.T) {
	var gen SyntheticDataGenerator = noSyntheticData{}
	samples, err := gen.Generate(context.Background(), model.TenantID("t"), GenSpec{SubjectKind: "agent", Count: 10})
	if len(samples) != 0 {
		t.Fatalf("default generator produced %d samples, want 0", len(samples))
	}
	if err != errSyntheticDataPostV1 {
		t.Fatalf("err = %v, want errSyntheticDataPostV1", err)
	}

	// No route on the module generates synthetic data: probe the obvious candidates.
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	for _, p := range []string{"/v1/m/sandbox/generate", "/v1/m/sandbox/synthetic", "/v1/m/sandbox/data/generate"} {
		r := h.do("POST", p, admin, map[string]any{}, tenantHdr(tenant))
		if r.code != http.StatusNotFound {
			t.Fatalf("POST %s = %d, want 404 (no synthetic-data route)", p, r.code)
		}
	}
}

// TestMultiTenantIsolation verifies one tenant never sees another's scenarios/runs.
func TestMultiTenantIsolation(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	acme := h.createOrg(admin, "acme")
	other := h.createOrg(admin, "other")

	scenID := h.createScenario(admin, acme, "only-acme",
		[]map[string]any{{"key": "s1", "input": "r1"}},
		[]map[string]any{{"resource": "r1", "response": "ok"}})
	if r := h.do("POST", "/v1/m/sandbox/scenarios/"+scenID+"/run", admin, map[string]any{}, tenantHdr(acme)); r.code != http.StatusCreated {
		t.Fatalf("run in acme = %d %s", r.code, r.raw)
	}

	// The other tenant cannot see acme's scenario nor its runs.
	if r := h.do("GET", "/v1/m/sandbox/scenarios/"+scenID, admin, nil, tenantHdr(other)); r.code != http.StatusNotFound {
		t.Fatalf("cross-tenant scenario read = %d, want 404", r.code)
	}
	runs := h.do("GET", "/v1/m/sandbox/runs", admin, nil, tenantHdr(other))
	if runs.code != http.StatusOK {
		t.Fatalf("other runs = %d %s", runs.code, runs.raw)
	}
	if items, _ := runs.body["items"].([]any); len(items) != 0 {
		t.Fatalf("other tenant sees %d runs, want 0", len(items))
	}
	// And acme does see its own.
	mine := h.do("GET", "/v1/m/sandbox/scenarios", admin, nil, tenantHdr(acme))
	if items, _ := mine.body["items"].([]any); len(items) != 1 {
		t.Fatalf("acme sees %d scenarios, want 1", len(items))
	}
}

// TestStreamReplaysCompletedRun verifies the SSE stream of a completed run yields the
// per-step output frames, then a summary frame, then done.
func TestStreamReplaysCompletedRun(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	scenID := h.createScenario(admin, tenant, "s",
		[]map[string]any{{"key": "s1", "input": "r1"}, {"key": "s2", "input": "r2"}},
		[]map[string]any{{"resource": "r1", "response": "one"}, {"resource": "r2", "response": "two"}})
	run := h.do("POST", "/v1/m/sandbox/scenarios/"+scenID+"/run", admin, map[string]any{}, tenantHdr(tenant))
	if run.code != http.StatusCreated {
		t.Fatalf("run = %d %s", run.code, run.raw)
	}
	runID := run.body["id"].(string)

	r := h.do("GET", "/v1/m/sandbox/runs/"+runID+"/stream", admin, nil, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("stream = %d %s", r.code, r.raw)
	}
	body := r.raw
	if strings.Count(body, "event: output") != 2 {
		t.Fatalf("want 2 output frames, body:\n%s", body)
	}
	if !strings.Contains(body, "event: summary") {
		t.Fatalf("missing summary frame, body:\n%s", body)
	}
	if !strings.Contains(body, "event: done") {
		t.Fatalf("missing done frame, body:\n%s", body)
	}
	if !strings.Contains(body, "\"runner\":\"inproc-mock\"") {
		t.Fatalf("summary missing runner, body:\n%s", body)
	}
}

// TestPrivilegedActionsAreTiered verifies the verb tiers: a viewer can read but cannot
// create a scenario, launch a run/replay, or compare.
func TestPrivilegedActionsAreTiered(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	viewer := h.roleToken(admin, tenant, "v@x.io", "viewer")
	editor := h.roleToken(admin, tenant, "e@x.io", "editor")

	// Viewer cannot create a scenario.
	if r := h.do("POST", "/v1/m/sandbox/scenarios", viewer, map[string]any{"name": "x"}, tenantHdr(tenant)); r.code != http.StatusForbidden {
		t.Fatalf("viewer create = %d, want 403", r.code)
	}
	// Editor (write-tier) can.
	scenID := h.createScenario(editor, tenant, "x",
		[]map[string]any{{"key": "s1", "input": "r1"}}, nil)

	// Viewer cannot launch a run; editor can.
	if r := h.do("POST", "/v1/m/sandbox/scenarios/"+scenID+"/run", viewer, map[string]any{}, tenantHdr(tenant)); r.code != http.StatusForbidden {
		t.Fatalf("viewer run = %d, want 403", r.code)
	}
	if r := h.do("POST", "/v1/m/sandbox/scenarios/"+scenID+"/run", editor, map[string]any{}, tenantHdr(tenant)); r.code != http.StatusCreated {
		t.Fatalf("editor run = %d %s", r.code, r.raw)
	}
	// Compare is admin-tier: editor is refused, admin allowed.
	cmpBody := map[string]any{"scenario_ref": scenID, "baseline_variant": "a", "candidate_variant": "b"}
	if r := h.do("POST", "/v1/m/sandbox/compare", editor, cmpBody, tenantHdr(tenant)); r.code != http.StatusForbidden {
		t.Fatalf("editor compare = %d, want 403", r.code)
	}
	if r := h.do("POST", "/v1/m/sandbox/compare", admin, cmpBody, tenantHdr(tenant)); r.code != http.StatusCreated {
		t.Fatalf("admin compare = %d %s", r.code, r.raw)
	}
	// Viewer can still read.
	if r := h.do("GET", "/v1/m/sandbox/scenarios", viewer, nil, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Fatalf("viewer list = %d, want 200", r.code)
	}
}

// replay POSTs a replay and returns the resulting step_key→output map.
func (h *harness) replay(token string, tenant model.TenantID, body map[string]any) map[string]string {
	h.t.Helper()
	r := h.do("POST", "/v1/m/sandbox/replay", token, body, tenantHdr(tenant))
	if r.code != http.StatusCreated {
		h.t.Fatalf("replay = %d %s", r.code, r.raw)
	}
	runID := r.body["id"].(string)
	out := h.do("GET", "/v1/m/sandbox/runs/"+runID+"/outputs", token, nil, tenantHdr(tenant))
	if out.code != http.StatusOK {
		h.t.Fatalf("outputs = %d %s", out.code, out.raw)
	}
	res := map[string]string{}
	items, _ := out.body["items"].([]any)
	for _, it := range items {
		m := it.(map[string]any)
		res[m["step_key"].(string)] = m["output"].(string)
	}
	return res
}
