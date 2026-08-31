// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package evals

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
)

// TestRunScoresAndWritesCoreEvalResult creates a suite + cases, runs it with inline
// outputs scored by a deterministic scorer, and asserts the score and that a CANONICAL
// core EvalResult was written.
func TestRunScoresAndWritesCoreEvalResult(t *testing.T) {
	h := newHarness(t, nil)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")

	suiteID := h.createSuite(admin, tenant, map[string]any{
		"name": "math", "subject_kind": "model", "scorer": scorerExact, "pass_threshold": 0.5,
	})
	h.addCase(admin, tenant, suiteID, map[string]any{"case_key": "c1", "input": "2+2", "expected": "4"})
	h.addCase(admin, tenant, suiteID, map[string]any{"case_key": "c2", "input": "3+3", "expected": "6"})
	h.addCase(admin, tenant, suiteID, map[string]any{"case_key": "c3", "input": "5+5", "expected": "10"})

	// c1, c2 correct; c3 wrong ⇒ 2/3 pass.
	r := h.do("POST", "/v1/m/evals/runs", admin, map[string]any{
		"suite_ref": suiteID, "subject_kind": "model", "subject_ref": "gpt-x",
		"outputs": map[string]any{"c1": "4", "c2": "6", "c3": "11"},
	}, tenantHdr(tenant))
	if r.code != http.StatusCreated {
		t.Fatalf("launch run = %d %s", r.code, r.raw)
	}
	if r.body["status"] != "completed" {
		t.Fatalf("status = %v, want completed", r.body["status"])
	}
	if intOf(r.body["total"]) != 3 || intOf(r.body["passed"]) != 2 || intOf(r.body["failed"]) != 1 {
		t.Fatalf("aggregate wrong: total=%v passed=%v failed=%v", r.body["total"], r.body["passed"], r.body["failed"])
	}
	if pr := floatOf(r.body["pass_rate"]); pr < 0.66 || pr > 0.67 {
		t.Fatalf("pass_rate = %v, want ~0.667", pr)
	}
	if sc := floatOf(r.body["score"]); sc < 0.66 || sc > 0.67 {
		t.Fatalf("score = %v, want ~0.667 (mean of scored)", sc)
	}

	// The canonical cross-module artifact exists with the right score.
	results := h.coreEvalResults(tenant, "math")
	if len(results) != 1 {
		t.Fatalf("core EvalResults = %d, want 1", len(results))
	}
	er := results[0]
	if !er.Passed {
		t.Fatalf("EvalResult.Passed = false, want true (pass_rate 0.667 >= threshold 0.5)")
	}
	if er.Score < 0.66 || er.Score > 0.67 {
		t.Fatalf("EvalResult.Score = %v, want ~0.667", er.Score)
	}
	if got := er.Metrics["total"]; intOf(got) != 3 {
		t.Fatalf("EvalResult.Metrics[total] = %v, want 3", got)
	}
}

// TestSkippedCaseExcludedFromDenominator verifies a case with no supplied output is
// SKIPPED and excluded from the pass-rate denominator (the redteam rule), never a pass.
func TestSkippedCaseExcludedFromDenominator(t *testing.T) {
	h := newHarness(t, nil)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	suiteID := h.createSuite(admin, tenant, map[string]any{"name": "s", "subject_kind": "model", "scorer": scorerExact})
	h.addCase(admin, tenant, suiteID, map[string]any{"case_key": "c1", "input": "x", "expected": "y"})
	h.addCase(admin, tenant, suiteID, map[string]any{"case_key": "c2", "input": "x", "expected": "y"})

	r := h.do("POST", "/v1/m/evals/runs", admin, map[string]any{
		"suite_ref": suiteID, "subject_ref": "m", "outputs": map[string]any{"c1": "y"}, // c2 absent
	}, tenantHdr(tenant))
	if r.code != http.StatusCreated {
		t.Fatalf("run = %d %s", r.code, r.raw)
	}
	if intOf(r.body["skipped"]) != 1 {
		t.Fatalf("skipped = %v, want 1", r.body["skipped"])
	}
	if pr := floatOf(r.body["pass_rate"]); pr != 1.0 {
		t.Fatalf("pass_rate = %v, want 1.0 (skipped excluded from denominator)", pr)
	}
}

// TestDeterministicScorers exercises each deterministic built-in via a one-case suite.
func TestDeterministicScorers(t *testing.T) {
	h := newHarness(t, nil)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")

	cases := []struct {
		scorer, expected, output, wantOutcome string
	}{
		{scorerContains, "lo", "hello", outcomePass},
		{scorerNotContains, "zz", "hello", outcomePass},
		{scorerNotContains, "ell", "hello", outcomeFail},
		{scorerRegex, "^h.*o$", "hello", outcomePass},
		{scorerRegex, "(", "hello", outcomeError}, // bad pattern
		{scorerJSONValid, "", `{"a":1}`, outcomePass},
		{scorerJSONValid, "", `{bad`, outcomeFail},
		{scorerJSONEqual, `{"a":1,"b":2}`, `{"b":2,"a":1}`, outcomePass},
		{scorerNumericRange, "1..10", "5", outcomePass},
		{scorerNumericRange, "1..10", "50", outcomeFail},
		{scorerNumericRange, "bad", "5", outcomeError},
	}
	for i, tc := range cases {
		suiteID := h.createSuite(admin, tenant, map[string]any{
			"name": "sc" + string(rune('a'+i)), "subject_kind": "model", "scorer": tc.scorer,
		})
		h.addCase(admin, tenant, suiteID, map[string]any{"case_key": "c1", "input": "x", "expected": tc.expected})
		r := h.do("POST", "/v1/m/evals/runs", admin, map[string]any{
			"suite_ref": suiteID, "subject_ref": "m", "outputs": map[string]any{"c1": tc.output},
		}, tenantHdr(tenant))
		if r.code != http.StatusCreated {
			t.Fatalf("[%s] run = %d %s", tc.scorer, r.code, r.raw)
		}
		items, _ := r.body["cases"].([]any)
		if len(items) != 1 {
			t.Fatalf("[%s] cases = %d, want 1", tc.scorer, len(items))
		}
		got := items[0].(map[string]any)["outcome"]
		if got != tc.wantOutcome {
			t.Fatalf("[%s] expected=%q output=%q outcome = %v, want %v", tc.scorer, tc.expected, tc.output, got, tc.wantOutcome)
		}
	}
}

// TestLLMJudgeScoredWithFakeJudge verifies the llm_judge scorer produces a real score
// when a Judge is wired.
func TestLLMJudgeScoredWithFakeJudge(t *testing.T) {
	h := newHarness(t, fakeJudge{})
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")

	suiteID := h.createSuite(admin, tenant, map[string]any{
		"name": "judged", "subject_kind": "model", "scorer": scorerLLMJudge,
		"criterion": "PARIS", "judge_model": "claude-x", "pass_threshold": 0.5,
	})
	h.addCase(admin, tenant, suiteID, map[string]any{"case_key": "c1", "input": "capital of France?"})
	h.addCase(admin, tenant, suiteID, map[string]any{"case_key": "c2", "input": "capital of France?"})

	r := h.do("POST", "/v1/m/evals/runs", admin, map[string]any{
		"suite_ref": suiteID, "subject_ref": "m", "model_ref": "claude-x",
		"outputs": map[string]any{"c1": "It is PARIS.", "c2": "It is London."},
	}, tenantHdr(tenant))
	if r.code != http.StatusCreated {
		t.Fatalf("judge run = %d %s", r.code, r.raw)
	}
	if intOf(r.body["passed"]) != 1 || intOf(r.body["failed"]) != 1 {
		t.Fatalf("judge aggregate: passed=%v failed=%v, want 1/1", r.body["passed"], r.body["failed"])
	}
	if r.body["status"] != "completed" {
		t.Fatalf("status = %v, want completed", r.body["status"])
	}
}

// TestLLMJudgeSkippedWithoutJudge verifies an un-wired judge degrades llm_judge to
// SKIPPED (never a silent pass), and the run is degraded.
func TestLLMJudgeSkippedWithoutJudge(t *testing.T) {
	h := newHarness(t, nil) // offline judge
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	suiteID := h.createSuite(admin, tenant, map[string]any{
		"name": "judged", "subject_kind": "model", "scorer": scorerLLMJudge, "criterion": "x",
	})
	h.addCase(admin, tenant, suiteID, map[string]any{"case_key": "c1", "input": "q"})

	r := h.do("POST", "/v1/m/evals/runs", admin, map[string]any{
		"suite_ref": suiteID, "subject_ref": "m", "outputs": map[string]any{"c1": "anything"},
	}, tenantHdr(tenant))
	if r.code != http.StatusCreated {
		t.Fatalf("run = %d %s", r.code, r.raw)
	}
	if intOf(r.body["skipped"]) != 1 || intOf(r.body["passed"]) != 0 {
		t.Fatalf("expected skipped=1 passed=0, got skipped=%v passed=%v", r.body["skipped"], r.body["passed"])
	}
	if r.body["status"] != "degraded" {
		t.Fatalf("status = %v, want degraded (nothing scored)", r.body["status"])
	}
}

// TestRegressionDetectedAndEmitted runs a strong baseline then a worse candidate for
// the same suite+subject: the candidate is flagged regressed, a core eval_regression
// Finding is persisted, and a FindingReport is delivered on the bus.
func TestRegressionDetectedAndEmitted(t *testing.T) {
	h := newHarness(t, nil)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")

	suiteID := h.createSuite(admin, tenant, map[string]any{
		"name": "reg", "subject_kind": "model", "scorer": scorerExact,
		"pass_threshold": 0.5, "regression_threshold": 0.2,
	})
	h.addCase(admin, tenant, suiteID, map[string]any{"case_key": "c1", "input": "x", "expected": "a"})
	h.addCase(admin, tenant, suiteID, map[string]any{"case_key": "c2", "input": "y", "expected": "b"})

	// Baseline: both correct ⇒ score 1.0.
	base := h.do("POST", "/v1/m/evals/runs", admin, map[string]any{
		"suite_ref": suiteID, "subject_ref": "m1", "outputs": map[string]any{"c1": "a", "c2": "b"},
	}, tenantHdr(tenant))
	if base.code != http.StatusCreated || base.body["regressed"] != false {
		t.Fatalf("baseline run = %d regressed=%v (want false)", base.code, base.body["regressed"])
	}

	// Candidate: both wrong ⇒ score 0.0, drift 1.0 > 0.2.
	cand := h.do("POST", "/v1/m/evals/runs", admin, map[string]any{
		"suite_ref": suiteID, "subject_ref": "m1", "outputs": map[string]any{"c1": "wrong", "c2": "wrong"},
	}, tenantHdr(tenant))
	if cand.code != http.StatusCreated {
		t.Fatalf("candidate run = %d %s", cand.code, cand.raw)
	}
	if cand.body["regressed"] != true {
		t.Fatalf("candidate regressed = %v, want true", cand.body["regressed"])
	}
	if d := floatOf(cand.body["drift"]); d < 0.99 {
		t.Fatalf("drift = %v, want ~1.0", d)
	}

	// A core eval_regression Finding was persisted.
	finds := h.coreFindings(tenant, findingKindRegression)
	if len(finds) != 1 {
		t.Fatalf("persisted eval_regression findings = %d, want 1", len(finds))
	}
	if len(finds[0].DetailHash) == 0 {
		t.Fatalf("regression finding missing detail_hash")
	}

	// A FindingReport was delivered on the bus.
	h.waitFindings()
	delivered := h.deliveredFindings()
	if len(delivered) != 1 || delivered[0].Kind != busEvalRegression {
		t.Fatalf("bus findings = %d, kinds wrong: %+v", len(delivered), delivered)
	}
}

// TestABOrdersByScore runs two variants against the same suite and asserts the higher
// scorer wins.
func TestABOrdersByScore(t *testing.T) {
	h := newHarness(t, nil)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	suiteID := h.createSuite(admin, tenant, map[string]any{"name": "ab", "subject_kind": "prompt", "scorer": scorerExact})
	h.addCase(admin, tenant, suiteID, map[string]any{"case_key": "c1", "input": "x", "expected": "a"})
	h.addCase(admin, tenant, suiteID, map[string]any{"case_key": "c2", "input": "y", "expected": "b"})

	r := h.do("POST", "/v1/m/evals/ab", admin, map[string]any{
		"suite_ref": suiteID, "subject_ref": "p",
		"a": map[string]any{"label": "v1", "outputs": map[string]any{"c1": "a", "c2": "wrong"}}, // 0.5
		"b": map[string]any{"label": "v2", "outputs": map[string]any{"c1": "a", "c2": "b"}},     // 1.0
	}, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("ab = %d %s", r.code, r.raw)
	}
	if r.body["winner"] != "v2" {
		t.Fatalf("winner = %v, want v2", r.body["winner"])
	}
	variants, _ := r.body["variants"].([]any)
	if len(variants) != 2 {
		t.Fatalf("variants = %d, want 2", len(variants))
	}
	first := variants[0].(map[string]any)
	if first["label"] != "v2" || floatOf(first["score"]) != 1.0 {
		t.Fatalf("first variant = %+v, want v2 score 1.0", first)
	}
	if d := floatOf(r.body["delta"]); d != 0.5 {
		t.Fatalf("delta = %v, want 0.5", d)
	}
}

// TestABRegressionEmittedOnBus proves the /ab path delivers the best-effort regression
// FindingReport on the bus (the code-review fix): a worse A/B run reusing the same
// labels+subject regresses each variant vs its prior run, and the signal reaches.
func TestABRegressionEmittedOnBus(t *testing.T) {
	h := newHarness(t, nil)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	suiteID := h.createSuite(admin, tenant, map[string]any{
		"name": "abreg", "subject_kind": "prompt", "scorer": scorerExact,
		"pass_threshold": 0.5, "regression_threshold": 0.2,
	})
	h.addCase(admin, tenant, suiteID, map[string]any{"case_key": "c1", "input": "x", "expected": "a"})
	h.addCase(admin, tenant, suiteID, map[string]any{"case_key": "c2", "input": "y", "expected": "b"})

	// Baseline A/B: both variants correct (1.0); no prior ⇒ no regression delivered.
	base := h.do("POST", "/v1/m/evals/ab", admin, map[string]any{
		"suite_ref": suiteID, "subject_ref": "p",
		"a": map[string]any{"label": "v1", "outputs": map[string]any{"c1": "a", "c2": "b"}},
		"b": map[string]any{"label": "v2", "outputs": map[string]any{"c1": "a", "c2": "b"}},
	}, tenantHdr(tenant))
	if base.code != http.StatusOK {
		t.Fatalf("baseline ab = %d %s", base.code, base.raw)
	}
	h.waitFindings()
	if got := len(h.deliveredFindings()); got != 0 {
		t.Fatalf("baseline ab delivered %d findings, want 0", got)
	}

	// Worse A/B, same labels+subject ⇒ each variant regresses vs its prior run.
	cand := h.do("POST", "/v1/m/evals/ab", admin, map[string]any{
		"suite_ref": suiteID, "subject_ref": "p",
		"a": map[string]any{"label": "v1", "outputs": map[string]any{"c1": "wrong", "c2": "wrong"}},
		"b": map[string]any{"label": "v2", "outputs": map[string]any{"c1": "wrong", "c2": "wrong"}},
	}, tenantHdr(tenant))
	if cand.code != http.StatusOK {
		t.Fatalf("candidate ab = %d %s", cand.code, cand.raw)
	}
	h.waitFindings()
	delivered := h.deliveredFindings()
	if len(delivered) == 0 {
		t.Fatal("A/B regression was not delivered on the bus (the /ab emit fix regressed)")
	}
	for _, f := range delivered {
		if f.Kind != busEvalRegression {
			t.Fatalf("unexpected bus finding kind %q, want %q", f.Kind, busEvalRegression)
		}
	}
}

// TestMonitorScoresSessionSignals seeds core Sessions+Findings, runs the monitor and
// asserts a core EvalResult per sample with the expected signal scoring.
func TestMonitorScoresSessionSignals(t *testing.T) {
	h := newHarness(t, nil)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")

	h.seedSession(tenant, model.SessionCompleted, "")                     // clean ⇒ 1.0 pass
	h.seedSession(tenant, model.SessionFailed, "")                        // failed ⇒ 0.0 fail
	h.seedSession(tenant, model.SessionCompleted, model.SeverityCritical) // critical finding ⇒ 0.0 fail

	r := h.do("POST", "/v1/m/evals/monitor", admin, map[string]any{"suite": "live"}, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("monitor = %d %s", r.code, r.raw)
	}
	if intOf(r.body["total"]) != 3 {
		t.Fatalf("monitor total = %v, want 3", r.body["total"])
	}
	if intOf(r.body["passed"]) != 1 || intOf(r.body["failed"]) != 2 {
		t.Fatalf("monitor passed/failed = %v/%v, want 1/2", r.body["passed"], r.body["failed"])
	}
	results := h.coreEvalResults(tenant, "live")
	if len(results) != 3 {
		t.Fatalf("monitor core EvalResults = %d, want 3", len(results))
	}
}

// fakeSessionSource is a deterministic wired SessionSource: it returns fixed
// module-II-shaped samples and records the query it was asked.
type fakeSessionSource struct {
	samples []SessionSample
	got     *SampleQuery
}

func (f fakeSessionSource) Sample(_ context.Context, _ model.TenantID, q SampleQuery) ([]SessionSample, error) {
	if f.got != nil {
		*f.got = q
	}
	return f.samples, nil
}

// TestMonitorSamplesWiredSessionSource proves the seam end-to-end on the evals
// side: a wired source REPLACES inline core sampling (the seeded core session is not
// scored), the monitor forwards the subject filter + limit, and module II's cc_state
// vocabulary scores honestly — silent_evasion is never a pass.
func TestMonitorSamplesWiredSessionSource(t *testing.T) {
	var got SampleQuery
	src := fakeSessionSource{
		samples: []SessionSample{
			{SessionRef: "11111111-1111-4111-8111-111111111111", AgentRef: "agent-x", State: "ended",
				InputTokens: 10, OutputTokens: 5, CostMicroUSD: 42, OccurredAt: time.Now()}, // clean ⇒ 1.0 pass
			{SessionRef: "sess-active", State: "active"},                                    // in flight ⇒ 0.5 fail
			{SessionRef: "sess-evade", State: "silent_evasion"},                             // evasion ⇒ 0.0 fail
			{SessionRef: "sess-crit", State: "ended", Findings: 1, MaxSeverity: "critical"}, // critical ⇒ 0.0 fail
		},
		got: &got,
	}
	h := newHarness(t, nil, WithSessionSource(src))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")

	// A core session that must NOT be sampled: the wired source replaces the
	// default inline core read entirely.
	h.seedSession(tenant, model.SessionCompleted, "")

	r := h.do("POST", "/v1/m/evals/monitor", admin,
		map[string]any{"suite": "live-sessions", "subject_kind": "agent", "subject_ref": "agent-x", "limit": 50},
		tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("monitor = %d %s", r.code, r.raw)
	}
	if got.SubjectKind != "agent" || got.SubjectRef != "agent-x" || got.Limit != 50 {
		t.Errorf("forwarded query = %+v, want agent/agent-x/50", got)
	}
	if intOf(r.body["total"]) != 4 {
		t.Fatalf("total = %v, want 4 (the wired source's samples, not the core session)", r.body["total"])
	}
	if intOf(r.body["passed"]) != 1 || intOf(r.body["failed"]) != 3 {
		t.Fatalf("passed/failed = %v/%v, want 1/3", r.body["passed"], r.body["failed"])
	}
	results := h.coreEvalResults(tenant, "live-sessions")
	if len(results) != 4 {
		t.Fatalf("core EvalResults = %d, want 4", len(results))
	}
	states := map[string]float64{}
	for _, res := range results {
		state, _ := res.Metrics["state"].(string)
		states[state] = res.Score
	}
	if states["silent_evasion"] != 0.0 {
		t.Errorf("silent_evasion score = %v, want 0.0 (never a pass)", states["silent_evasion"])
	}
	if states["active"] != 0.5 {
		t.Errorf("active score = %v, want 0.5", states["active"])
	}
	// One UUID session ref must survive as the EvalResult subject id; the rest keep
	// the ref in metadata only (the existing parseIDOrZero contract).
	linked := 0
	for _, res := range results {
		if res.SubjectID != "" {
			linked++
		}
	}
	if linked != 1 {
		t.Errorf("EvalResults with a linked subject id = %d, want 1 (the UUID ref)", linked)
	}
}

// TestScorecardsTrend runs the same suite+subject twice and asserts the scorecard
// aggregates both runs into a trend with the latest score last.
func TestScorecardsTrend(t *testing.T) {
	h := newHarness(t, nil)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	suiteID := h.createSuite(admin, tenant, map[string]any{"name": "tr", "subject_kind": "model", "scorer": scorerExact})
	h.addCase(admin, tenant, suiteID, map[string]any{"case_key": "c1", "input": "x", "expected": "a"})

	h.do("POST", "/v1/m/evals/runs", admin, map[string]any{
		"suite_ref": suiteID, "subject_ref": "m", "outputs": map[string]any{"c1": "wrong"},
	}, tenantHdr(tenant)) // score 0
	h.do("POST", "/v1/m/evals/runs", admin, map[string]any{
		"suite_ref": suiteID, "subject_ref": "m", "outputs": map[string]any{"c1": "a"},
	}, tenantHdr(tenant)) // score 1

	r := h.do("GET", "/v1/m/evals/scorecards?suite_ref="+suiteID, admin, nil, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("scorecards = %d %s", r.code, r.raw)
	}
	items, _ := r.body["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("scorecards = %d, want 1", len(items))
	}
	card := items[0].(map[string]any)
	if intOf(card["runs"]) != 2 {
		t.Fatalf("runs = %v, want 2", card["runs"])
	}
	trend, _ := card["trend"].([]any)
	if len(trend) != 2 {
		t.Fatalf("trend = %d, want 2", len(trend))
	}
	if floatOf(card["last_score"]) != 1.0 {
		t.Fatalf("last_score = %v, want 1.0", card["last_score"])
	}
	// The subject's KIND must cross the wire. It did not until 2026-08-01: the
	// DTO had no such field, so the console — whose typed contract declares it
	// REQUIRED — received undefined, built the i18n key `subjectKind.undefined`
	// and rendered that key raw, because its fallback was the same missing
	// value. A subject_ref alone does not identify a subject either: the same
	// free-form ref can name an agent in one suite and a model in another.
	// "model" is what this suite declares, so an absent field and a wrong one
	// both fail here, and they fail differently.
	if got, ok := card["subject_kind"].(string); !ok {
		t.Fatalf("subject_kind absent from the scorecard (%v) — the console renders the raw i18n key when this is missing", card)
	} else if got != "model" {
		t.Fatalf("subject_kind = %q, want %q (the suite's declared kind)", got, "model")
	}

	// CSV export.
	csv := h.do("GET", "/v1/m/evals/scorecards?suite_ref="+suiteID+"&format=csv", admin, nil, tenantHdr(tenant))
	if csv.code != http.StatusOK {
		t.Fatalf("csv scorecards = %d", csv.code)
	}
	if !strings.HasPrefix(csv.raw, "suite_ref,subject_ref,subject_kind,prompt_variant,runs,") {
		t.Fatalf("csv header wrong: %q", csv.raw[:min(60, len(csv.raw))])
	}
}

// TestStreamReplaysCompletedRun verifies the SSE replay emits case + summary + done.
func TestStreamReplaysCompletedRun(t *testing.T) {
	h := newHarness(t, nil)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	suiteID := h.createSuite(admin, tenant, map[string]any{"name": "st", "subject_kind": "model", "scorer": scorerExact})
	h.addCase(admin, tenant, suiteID, map[string]any{"case_key": "c1", "input": "x", "expected": "a"})
	h.addCase(admin, tenant, suiteID, map[string]any{"case_key": "c2", "input": "y", "expected": "b"})

	run := h.do("POST", "/v1/m/evals/runs", admin, map[string]any{
		"suite_ref": suiteID, "subject_ref": "m", "outputs": map[string]any{"c1": "a", "c2": "b"},
	}, tenantHdr(tenant))
	runID := run.body["id"].(string)

	r := h.do("GET", "/v1/m/evals/runs/"+runID+"/stream", admin, nil, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("stream = %d %s", r.code, r.raw)
	}
	if strings.Count(r.raw, "event: case") != 2 {
		t.Fatalf("expected 2 case frames, got: %q", r.raw)
	}
	if !strings.Contains(r.raw, "event: summary") {
		t.Fatalf("missing summary frame: %q", r.raw)
	}
	if !strings.Contains(r.raw, "event: done") {
		t.Fatalf("missing done frame: %q", r.raw)
	}
}

// TestPrivilegedActionsTier verifies the verb tiers: a viewer cannot create a suite or
// launch a run but can read; an editor can launch a run but cannot pin a baseline.
func TestPrivilegedActionsTier(t *testing.T) {
	h := newHarness(t, nil)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	viewer := h.roleToken(admin, tenant, "v@x.io", "viewer")
	editor := h.roleToken(admin, tenant, "e@x.io", "editor")

	if r := h.do("POST", "/v1/m/evals/suites", viewer, map[string]any{"name": "x", "subject_kind": "model", "scorer": scorerExact}, tenantHdr(tenant)); r.code != http.StatusForbidden {
		t.Fatalf("viewer create suite = %d, want 403", r.code)
	}
	suiteID := h.createSuite(admin, tenant, map[string]any{"name": "x", "subject_kind": "model", "scorer": scorerExact})
	h.addCase(admin, tenant, suiteID, map[string]any{"case_key": "c1", "input": "i", "expected": "o"})

	if r := h.do("GET", "/v1/m/evals/suites", viewer, nil, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Fatalf("viewer list suites = %d, want 200", r.code)
	}
	// Editor can launch a run (write-tier).
	run := h.do("POST", "/v1/m/evals/runs", editor, map[string]any{
		"suite_ref": suiteID, "subject_ref": "m", "outputs": map[string]any{"c1": "o"},
	}, tenantHdr(tenant))
	if run.code != http.StatusCreated {
		t.Fatalf("editor launch run = %d %s", run.code, run.raw)
	}
	// Editor CANNOT pin a baseline (admin-tier).
	runID := run.body["id"].(string)
	if r := h.do("POST", "/v1/m/evals/baselines", editor, map[string]any{
		"suite_ref": suiteID, "subject_ref": "m", "run_ref": runID,
	}, tenantHdr(tenant)); r.code != http.StatusForbidden {
		t.Fatalf("editor pin baseline = %d, want 403", r.code)
	}
	// Admin can.
	if r := h.do("POST", "/v1/m/evals/baselines", admin, map[string]any{
		"suite_ref": suiteID, "subject_ref": "m", "run_ref": runID,
	}, tenantHdr(tenant)); r.code != http.StatusCreated {
		t.Fatalf("admin pin baseline = %d %s", r.code, r.raw)
	}
}

// TestMultiTenantIsolation verifies tenant B cannot see tenant A's runs.
func TestMultiTenantIsolation(t *testing.T) {
	h := newHarness(t, nil)
	admin := h.adminLogin()
	a := h.createOrg(admin, "tenant-a")
	b := h.createOrg(admin, "tenant-b")

	suiteA := h.createSuite(admin, a, map[string]any{"name": "s", "subject_kind": "model", "scorer": scorerExact})
	h.addCase(admin, a, suiteA, map[string]any{"case_key": "c1", "input": "x", "expected": "y"})
	run := h.do("POST", "/v1/m/evals/runs", admin, map[string]any{
		"suite_ref": suiteA, "subject_ref": "m", "outputs": map[string]any{"c1": "y"},
	}, tenantHdr(a))
	if run.code != http.StatusCreated {
		t.Fatalf("tenant A run = %d %s", run.code, run.raw)
	}
	runID := run.body["id"].(string)

	// Tenant B cannot list A's runs.
	rb := h.do("GET", "/v1/m/evals/runs", admin, nil, tenantHdr(b))
	if rb.code != http.StatusOK {
		t.Fatalf("tenant B list = %d", rb.code)
	}
	if items, _ := rb.body["items"].([]any); len(items) != 0 {
		t.Fatalf("tenant B sees %d runs, want 0", len(items))
	}
	// Tenant B cannot fetch A's run by id.
	if r := h.do("GET", "/v1/m/evals/runs/"+runID, admin, nil, tenantHdr(b)); r.code != http.StatusNotFound {
		t.Fatalf("tenant B get A's run = %d, want 404", r.code)
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// An unscored run has no score and no pass rate to contribute, and the doc
// comment on aggregateScorecards already said so — "only scored runs feed the
// means". The code did not check, so such a run folded a zero into both means:
// a subject that passed everything it was actually measured on was reported at
// 50% for having one run that measured nothing. runs and the trend still count
// it, because it did happen.
func TestScorecardMeansSkipUnscoredRuns(t *testing.T) {
	h := newHarness(t, nil)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	suiteID := h.createSuite(admin, tenant, map[string]any{"name": "un", "subject_kind": "model", "scorer": scorerExact})
	h.addCase(admin, tenant, suiteID, map[string]any{"case_key": "c1", "input": "x", "expected": "a"})

	// Scored, and it passes: passed=1, failed=0.
	h.do("POST", "/v1/m/evals/runs", admin, map[string]any{
		"suite_ref": suiteID, "subject_ref": "m", "outputs": map[string]any{"c1": "a"},
	}, tenantHdr(tenant))
	// Scores nothing: passed=0, failed=0. Not a failure — an absence.
	h.do("POST", "/v1/m/evals/runs", admin, map[string]any{
		"suite_ref": suiteID, "subject_ref": "m", "outputs": map[string]any{},
	}, tenantHdr(tenant))

	r := h.do("GET", "/v1/m/evals/scorecards?suite_ref="+suiteID, admin, nil, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("scorecards = %d %s", r.code, r.raw)
	}
	items, _ := r.body["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("scorecards = %d, want 1", len(items))
	}
	card := items[0].(map[string]any)

	// Every run is counted and shown: the card's "Runs" means runs.
	if intOf(card["runs"]) != 2 {
		t.Errorf("runs = %v, want 2 (the unscored run still happened)", card["runs"])
	}
	// The means divide by the runs that produced a denominator, so the zero of a
	// run that measured nothing does not halve them.
	if got := floatOf(card["pass_rate"]); got != 1.0 {
		t.Errorf("pass_rate = %v, want 1 (the unscored run must not enter the mean)", got)
	}
	if got := floatOf(card["mean_score"]); got != 1.0 {
		t.Errorf("mean_score = %v, want 1 (same reason)", got)
	}
	// Control on the case-weighted view, which was already right: one case scored,
	// one passed. If this moved, the fix reached further than the means.
	pooled, ok := card["pooled_pass_rate"].(map[string]any)
	if !ok {
		t.Fatalf("pooled_pass_rate absent from %v", card)
	}
	if floatOf(pooled["rate"]) != 1.0 || intOf(pooled["n"]) != 1 {
		t.Errorf("pooled_pass_rate = %v, want rate 1 over n 1", pooled)
	}
}
