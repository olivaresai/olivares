// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package evals

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/model"
)

// countingJudge wraps a Judge and counts the model calls — the cache assertions
// depend on it.
type countingJudge struct {
	inner Judge
	calls int
}

func (c *countingJudge) Judge(ctx context.Context, tenant model.TenantID, req JudgeRequest) (JudgeVerdict, error) {
	c.calls++
	return c.inner.Judge(ctx, tenant, req)
}

// fakeBudget is a deterministic BudgetGate.
type fakeBudget struct {
	allowed bool
	action  string
}

func (f fakeBudget) Check(_ context.Context, _ model.TenantID, _ BudgetDims) (BudgetDecision, error) {
	return BudgetDecision{Allowed: f.allowed, Action: f.action, Reason: "test budget " + f.action}, nil
}

func hasReason(body map[string]any, want string) bool {
	reasons, _ := body["reasons"].([]any)
	for _, r := range reasons {
		if r == want {
			return true
		}
	}
	return false
}

// TestGateBlocksRegression proves the BLOCKING semantics (decision 1): a candidate
// below the baseline fails the gate with the regression + threshold reasons, and the
// gate evaluation persists a normal run plus the gate row.
func TestGateBlocksRegression(t *testing.T) {
	h := newHarness(t, nil)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	suiteID := h.createSuite(admin, tenant, map[string]any{
		"name": "g", "subject_kind": "model", "scorer": scorerExact,
		"pass_threshold": 0.5, "regression_threshold": 0.2,
	})
	h.addCase(admin, tenant, suiteID, map[string]any{"case_key": "c1", "input": "x", "expected": "a"})
	h.addCase(admin, tenant, suiteID, map[string]any{"case_key": "c2", "input": "y", "expected": "b"})

	// Baseline: a perfect prior run.
	h.do("POST", "/v1/m/evals/runs", admin, map[string]any{
		"suite_ref": suiteID, "subject_ref": "m1", "outputs": map[string]any{"c1": "a", "c2": "b"},
	}, tenantHdr(tenant))

	// Candidate: everything wrong → drift 1.0 → BLOCK.
	r := h.do("POST", "/v1/m/evals/gate", admin, map[string]any{
		"suite_ref": suiteID, "subject_ref": "m1", "outputs": map[string]any{"c1": "w", "c2": "w"},
	}, tenantHdr(tenant))
	if r.code != http.StatusCreated {
		t.Fatalf("gate = %d %s", r.code, r.raw)
	}
	if r.body["verdict"] != verdictFail || r.body["effective_verdict"] != verdictFail {
		t.Fatalf("verdict = %v/%v, want fail/fail", r.body["verdict"], r.body["effective_verdict"])
	}
	if !hasReason(r.body, reasonRegression) || !hasReason(r.body, reasonBelowThreshold) {
		t.Fatalf("reasons = %v, want regression_vs_baseline + below_pass_threshold", r.body["reasons"])
	}
	if r.body["run_ref"] == "" || r.body["run_ref"] == nil {
		t.Fatal("gate did not persist its run")
	}
	run, _ := r.body["run"].(map[string]any)
	if run == nil || run["regressed"] != true {
		t.Fatalf("gate run = %v, want regressed", run)
	}
	// The run carries its CIs (n_scored=2 → Wilson always, t-interval defined).
	if run["pass_rate_ci"] == nil || intOf(run["n_scored"]) != 2 {
		t.Fatalf("run CI surface missing: %v", run)
	}
}

// TestGateFirstRunPasses proves a fresh suite gates green (nothing to regress
// against) and DECLARES the missing baseline.
func TestGateFirstRunPasses(t *testing.T) {
	h := newHarness(t, nil)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	suiteID := h.createSuite(admin, tenant, map[string]any{
		"name": "g", "subject_kind": "model", "scorer": scorerExact, "pass_threshold": 0.5, "regression_threshold": 0.2,
	})
	h.addCase(admin, tenant, suiteID, map[string]any{"case_key": "c1", "input": "x", "expected": "a"})

	r := h.do("POST", "/v1/m/evals/gate", admin, map[string]any{
		"suite_ref": suiteID, "subject_ref": "m1", "outputs": map[string]any{"c1": "a"},
	}, tenantHdr(tenant))
	if r.code != http.StatusCreated || r.body["verdict"] != verdictPass {
		t.Fatalf("gate = %d verdict=%v, want 201 pass", r.code, r.body["verdict"])
	}
	if !hasReason(r.body, reasonNoBaseline) {
		t.Fatalf("reasons = %v, want the declared no_baseline note", r.body["reasons"])
	}
}

// TestGateWarnsWithoutJudgeCredential is decision 3's honest degradation: an
// llm_judge suite with no judge wired WARNS (declared), it neither blocks nor
// silently passes as if something had been judged.
func TestGateWarnsWithoutJudgeCredential(t *testing.T) {
	h := newHarness(t, nil) // offline judge
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	suiteID := h.createSuite(admin, tenant, map[string]any{
		"name": "gj", "subject_kind": "model", "scorer": scorerLLMJudge, "criterion": "PARIS",
	})
	h.addCase(admin, tenant, suiteID, map[string]any{"case_key": "c1", "input": "q"})

	r := h.do("POST", "/v1/m/evals/gate", admin, map[string]any{
		"suite_ref": suiteID, "subject_ref": "m", "outputs": map[string]any{"c1": "It is PARIS."},
	}, tenantHdr(tenant))
	if r.code != http.StatusCreated || r.body["verdict"] != verdictWarn {
		t.Fatalf("gate = %d verdict=%v, want 201 warn", r.code, r.body["verdict"])
	}
	if !hasReason(r.body, reasonNoJudge) {
		t.Fatalf("reasons = %v, want no_judge_credential", r.body["reasons"])
	}
}

// TestGateFailsUncalibratedJudge is decision 1's trust rule: a WIRED judge whose
// calibration was never measured cannot gate — fail, not warn.
func TestGateFailsUncalibratedJudge(t *testing.T) {
	h := newHarness(t, fakeJudge{})
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	suiteID := h.createSuite(admin, tenant, map[string]any{
		"name": "gj", "subject_kind": "model", "scorer": scorerLLMJudge,
		"criterion": "PARIS", "judge_model": "claude-x", "pass_threshold": 0.5,
	})
	h.addCase(admin, tenant, suiteID, map[string]any{"case_key": "c1", "input": "q"})

	r := h.do("POST", "/v1/m/evals/gate", admin, map[string]any{
		"suite_ref": suiteID, "subject_ref": "m", "outputs": map[string]any{"c1": "It is PARIS."},
	}, tenantHdr(tenant))
	if r.code != http.StatusCreated || r.body["verdict"] != verdictFail {
		t.Fatalf("gate = %d verdict=%v, want 201 fail", r.code, r.body["verdict"])
	}
	if !hasReason(r.body, reasonUncalibrated) {
		t.Fatalf("reasons = %v, want judge_uncalibrated", r.body["reasons"])
	}
}

// TestGateCalibratedJudgePassesAndCaches is the full happy path: calibrate the
// judge (meets target), gate once (verdicts measured, corrected pass-rate from the
// calibration's sensitivity/specificity), gate again with identical inputs — every
// verdict comes from the cache, zero new model calls (decision 3).
func TestGateCalibratedJudgePassesAndCaches(t *testing.T) {
	counting := &countingJudge{inner: fakeJudge{}}
	h := newHarness(t, counting)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	suiteID := h.createSuite(admin, tenant, map[string]any{
		"name": "gj", "subject_kind": "model", "scorer": scorerLLMJudge,
		"criterion": "PARIS", "judge_model": "claude-x", "pass_threshold": 0.5,
	})
	h.addCase(admin, tenant, suiteID, map[string]any{"case_key": "c1", "input": "q1"})
	h.addCase(admin, tenant, suiteID, map[string]any{"case_key": "c2", "input": "q2"})

	// Calibrate the pinned judge model on a balanced set the fake judge nails.
	cr := h.do("POST", "/v1/m/evals/calibration/items", admin, map[string]any{
		"set_name": "ref", "items": []any{
			map[string]any{"case_key": "k1", "output": "PARIS yes", "criterion": "PARIS", "human_passed": true},
			map[string]any{"case_key": "k2", "output": "London", "criterion": "PARIS", "human_passed": false},
		},
	}, tenantHdr(tenant))
	if cr.code != http.StatusCreated {
		t.Fatalf("items = %d %s", cr.code, cr.raw)
	}
	cal := h.do("POST", "/v1/m/evals/calibration/run", admin, map[string]any{
		"set_name": "ref", "judge_model": "claude-x",
	}, tenantHdr(tenant))
	if cal.code != http.StatusCreated || cal.body["meets_target"] != true {
		t.Fatalf("calibration = %d meets=%v %s", cal.code, cal.body["meets_target"], cal.raw)
	}
	callsAfterCalibration := counting.calls

	outputs := map[string]any{"c1": "It is PARIS.", "c2": "PARIS again."}
	g1 := h.do("POST", "/v1/m/evals/gate", admin, map[string]any{
		"suite_ref": suiteID, "subject_ref": "m", "outputs": outputs,
	}, tenantHdr(tenant))
	if g1.code != http.StatusCreated || g1.body["verdict"] != verdictPass {
		t.Fatalf("gate1 = %d verdict=%v %s", g1.code, g1.body["verdict"], g1.raw)
	}
	calBlock, _ := g1.body["calibration"].(map[string]any)
	if calBlock == nil || calBlock["meets_target"] != true {
		t.Fatalf("gate1 calibration block = %v, want the trusted report", g1.body["calibration"])
	}
	corrected, _ := g1.body["corrected_pass_rate"].(map[string]any)
	if corrected == nil || floatOf(corrected["pass_rate"]) != 1.0 {
		t.Fatalf("corrected_pass_rate = %v, want 1.0 (perfect judge ⇒ no shift)", g1.body["corrected_pass_rate"])
	}
	if counting.calls != callsAfterCalibration+2 {
		t.Fatalf("gate1 model calls = %d, want %d (one per case)", counting.calls, callsAfterCalibration+2)
	}

	// Identical rerun: every verdict from the cache, ZERO new model calls.
	g2 := h.do("POST", "/v1/m/evals/gate", admin, map[string]any{
		"suite_ref": suiteID, "subject_ref": "m", "outputs": outputs,
	}, tenantHdr(tenant))
	if g2.code != http.StatusCreated || g2.body["verdict"] != verdictPass {
		t.Fatalf("gate2 = %d verdict=%v %s", g2.code, g2.body["verdict"], g2.raw)
	}
	if intOf(g2.body["cache_hits"]) != 2 {
		t.Fatalf("gate2 cache_hits = %v, want 2", g2.body["cache_hits"])
	}
	if counting.calls != callsAfterCalibration+2 {
		t.Fatalf("gate2 made %d new model calls, want 0 (verdict cache)", counting.calls-callsAfterCalibration-2)
	}
}

// TestGateBudgetEnforcement proves the pre-flight: a blocked budget fails the
// gate WITHOUT spending a single judge call; a throttled one warns without spending.
func TestGateBudgetEnforcement(t *testing.T) {
	for _, tc := range []struct {
		action  string
		verdict string
		reason  string
	}{
		{"block", verdictFail, reasonBudgetBlocked},
		{"throttle", verdictWarn, reasonBudgetThrottled},
	} {
		counting := &countingJudge{inner: fakeJudge{}}
		h := newHarness(t, counting, WithBudgetGate(fakeBudget{allowed: false, action: tc.action}))
		admin := h.adminLogin()
		tenant := h.createOrg(admin, "acme")
		suiteID := h.createSuite(admin, tenant, map[string]any{
			"name": "gb", "subject_kind": "model", "scorer": scorerLLMJudge,
			"criterion": "PARIS", "judge_model": "claude-x",
		})
		h.addCase(admin, tenant, suiteID, map[string]any{"case_key": "c1", "input": "q"})

		r := h.do("POST", "/v1/m/evals/gate", admin, map[string]any{
			"suite_ref": suiteID, "subject_ref": "m", "outputs": map[string]any{"c1": "PARIS"},
		}, tenantHdr(tenant))
		if r.code != http.StatusCreated || r.body["verdict"] != tc.verdict {
			t.Fatalf("[%s] gate = %d verdict=%v, want %s", tc.action, r.code, r.body["verdict"], tc.verdict)
		}
		if !hasReason(r.body, tc.reason) {
			t.Fatalf("[%s] reasons = %v, want %s", tc.action, r.body["reasons"], tc.reason)
		}
		if counting.calls != 0 {
			t.Fatalf("[%s] judge was called %d times despite the budget refusal", tc.action, counting.calls)
		}
		if r.body["run_ref"] != nil && r.body["run_ref"] != "" {
			t.Fatalf("[%s] a run was persisted despite the budget refusal", tc.action)
		}
	}
}

// TestGateOverrideGoverned exercises the governed escape hatch: admin-only, a
// written reason required, audited, idempotence guarded, and a passing gate is not
// overridable.
func TestGateOverrideGoverned(t *testing.T) {
	h := newHarness(t, nil)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "e@x.io", "editor")
	suiteID := h.createSuite(admin, tenant, map[string]any{
		"name": "g", "subject_kind": "model", "scorer": scorerExact, "pass_threshold": 1.0,
	})
	h.addCase(admin, tenant, suiteID, map[string]any{"case_key": "c1", "input": "x", "expected": "a"})

	failGate := h.do("POST", "/v1/m/evals/gate", admin, map[string]any{
		"suite_ref": suiteID, "subject_ref": "m", "outputs": map[string]any{"c1": "wrong"},
	}, tenantHdr(tenant))
	if failGate.body["verdict"] != verdictFail {
		t.Fatalf("setup gate verdict = %v, want fail", failGate.body["verdict"])
	}
	gateID := failGate.body["id"].(string)

	// Editor (write-tier) cannot override — the override is admin-tier.
	if r := h.do("POST", "/v1/m/evals/gate/"+gateID+"/override", editor, map[string]any{"reason": "x"}, tenantHdr(tenant)); r.code != http.StatusForbidden {
		t.Fatalf("editor override = %d, want 403", r.code)
	}
	// A reason is mandatory.
	if r := h.do("POST", "/v1/m/evals/gate/"+gateID+"/override", admin, map[string]any{"reason": "  "}, tenantHdr(tenant)); r.code != http.StatusBadRequest {
		t.Fatalf("override without reason = %d, want 400", r.code)
	}
	// The governed override flips the EFFECTIVE verdict, not the recorded one.
	r := h.do("POST", "/v1/m/evals/gate/"+gateID+"/override", admin, map[string]any{"reason": "hotfix: incident 42, quality drop accepted by an approver"}, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("override = %d %s", r.code, r.raw)
	}
	if r.body["verdict"] != verdictFail || r.body["effective_verdict"] != verdictPass || r.body["overridden"] != true {
		t.Fatalf("override result = %v/%v/%v, want fail/pass/true", r.body["verdict"], r.body["effective_verdict"], r.body["overridden"])
	}
	// CI re-checks the gate and sees the effective pass.
	got := h.do("GET", "/v1/m/evals/gate/"+gateID, admin, nil, tenantHdr(tenant))
	if got.body["effective_verdict"] != verdictPass || got.body["override_reason"] == "" {
		t.Fatalf("get after override = %v", got.body)
	}
	// A second override is a conflict; so is overriding a passing gate.
	if r := h.do("POST", "/v1/m/evals/gate/"+gateID+"/override", admin, map[string]any{"reason": "again"}, tenantHdr(tenant)); r.code != http.StatusConflict {
		t.Fatalf("double override = %d, want 409", r.code)
	}
	passGate := h.do("POST", "/v1/m/evals/gate", admin, map[string]any{
		"suite_ref": suiteID, "subject_ref": "m2", "outputs": map[string]any{"c1": "a"},
	}, tenantHdr(tenant))
	passID := passGate.body["id"].(string)
	if r := h.do("POST", "/v1/m/evals/gate/"+passID+"/override", admin, map[string]any{"reason": "x"}, tenantHdr(tenant)); r.code != http.StatusConflict {
		t.Fatalf("override of passing gate = %d, want 409", r.code)
	}
}

// TestGateDeterministicSample proves the seeded subset is FIXED: same seed → the
// same cases scored across reruns (the property the verdict cache builds on).
func TestGateDeterministicSample(t *testing.T) {
	h := newHarness(t, nil)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	suiteID := h.createSuite(admin, tenant, map[string]any{
		"name": "gs", "subject_kind": "model", "scorer": scorerExact, "pass_threshold": 0.5,
	})
	outputs := map[string]any{}
	for _, k := range []string{"c1", "c2", "c3", "c4", "c5"} {
		h.addCase(admin, tenant, suiteID, map[string]any{"case_key": k, "input": "x", "expected": "a"})
		outputs[k] = "a"
	}

	sampledKeys := func(body map[string]any) []string {
		run, _ := body["run"].(map[string]any)
		cases, _ := run["cases"].([]any)
		keys := make([]string, 0, len(cases))
		for _, c := range cases {
			keys = append(keys, c.(map[string]any)["case_key"].(string))
		}
		return keys
	}

	g1 := h.do("POST", "/v1/m/evals/gate", admin, map[string]any{
		"suite_ref": suiteID, "subject_ref": "m", "outputs": outputs, "seed": "ci-seed", "sample_size": 2,
	}, tenantHdr(tenant))
	if g1.code != http.StatusCreated || intOf(g1.body["sampled"]) != 2 || intOf(g1.body["total_cases"]) != 5 {
		t.Fatalf("gate1 = %d sampled=%v/%v %s", g1.code, g1.body["sampled"], g1.body["total_cases"], g1.raw)
	}
	g2 := h.do("POST", "/v1/m/evals/gate", admin, map[string]any{
		"suite_ref": suiteID, "subject_ref": "m", "outputs": outputs, "seed": "ci-seed", "sample_size": 2,
	}, tenantHdr(tenant))
	k1, k2 := sampledKeys(g1.body), sampledKeys(g2.body)
	if len(k1) != 2 || strings.Join(k1, ",") != strings.Join(k2, ",") {
		t.Fatalf("seeded sample diverged across reruns: %v vs %v", k1, k2)
	}
}
