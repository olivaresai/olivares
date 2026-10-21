// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package evals

import (
	"fmt"
	"net/http"
	"reflect"
	"testing"

	"github.com/olivaresai/olivares/core/model"
)

// regressionIntervalFixture seeds a suite of n exact-match cases with the given
// regression threshold and one baseline run that passes all of them, so a candidate
// that gets `wrong` cases wrong has a pass-rate (and score) drop of wrong/n.
func regressionIntervalFixture(t *testing.T, h *harness, admin string, tenant model.TenantID, n int, threshold float64) (suiteID string) {
	t.Helper()
	suiteID = h.createSuite(admin, tenant, map[string]any{
		"name": fmt.Sprintf("interval-%d", n), "subject_kind": "model", "scorer": scorerExact,
		"pass_threshold": 0.5, "regression_threshold": threshold,
	})
	for i := 0; i < n; i++ {
		h.addCase(admin, tenant, suiteID, map[string]any{"case_key": fmt.Sprintf("c%03d", i), "input": "x", "expected": "a"})
	}
	base := h.do("POST", "/v1/m/evals/runs", admin, map[string]any{"suite_ref": suiteID, "subject_ref": "m1", "outputs": candidateOutputs(n, 0)}, tenantHdr(tenant))
	if base.code != http.StatusCreated || base.body["regressed"] != false {
		t.Fatalf("baseline run = %d regressed=%v: %s", base.code, base.body["regressed"], base.raw)
	}
	return suiteID
}

// candidateOutputs answers n cases, getting the first `wrong` of them wrong.
func candidateOutputs(n, wrong int) map[string]any {
	outputs := map[string]any{}
	for i := 0; i < n; i++ {
		answer := "a"
		if i < wrong {
			answer = "wrong"
		}
		outputs[fmt.Sprintf("c%03d", i)] = answer
	}
	return outputs
}

func launchRun(t *testing.T, h *harness, admin string, tenant model.TenantID, suiteID string, outputs map[string]any) resp {
	t.Helper()
	r := h.do("POST", "/v1/m/evals/runs", admin, map[string]any{"suite_ref": suiteID, "subject_ref": "m1", "outputs": outputs}, tenantHdr(tenant))
	if r.code != http.StatusCreated {
		t.Fatalf("run = %d %s", r.code, r.raw)
	}
	return r
}

func launchGate(t *testing.T, h *harness, admin string, tenant model.TenantID, suiteID string, outputs map[string]any) resp {
	t.Helper()
	r := h.do("POST", "/v1/m/evals/gate", admin, map[string]any{"suite_ref": suiteID, "subject_ref": "m1", "outputs": outputs}, tenantHdr(tenant))
	if r.code != http.StatusCreated {
		t.Fatalf("gate = %d %s", r.code, r.raw)
	}
	return r
}

func comparisonOf(r resp) map[string]any {
	cmp, _ := r.body["comparison"].(map[string]any)
	return cmp
}

// passRateDiffOf returns the comparison's pass_rate_diff as counts and interval.
func passRateDiffOf(t *testing.T, cmp map[string]any) (baselinePassed, baselineN, passed, n int, drop, lo, hi float64) {
	t.Helper()
	diff, _ := cmp["pass_rate_diff"].(map[string]any)
	if diff == nil {
		t.Fatalf("comparison has no pass_rate_diff: %v", cmp)
	}
	ci, _ := diff["drop_ci"].(map[string]any)
	return intOf(diff["baseline_passed"]), intOf(diff["baseline_n"]), intOf(diff["passed"]), intOf(diff["n"]),
		floatOf(diff["drop"]), floatOf(ci["lo"]), floatOf(ci["hi"])
}

// TestRegressionNeedsIntervalExcludingZero is the #2151 contract. The same
// 10-point drop (score 1.0 → 0.9, over a 0.05 threshold) is noise on 10 cases and a
// finding on 100: eval_regression fires only when the Newcombe interval of the
// pass-rate difference excludes zero.
func TestRegressionNeedsIntervalExcludingZero(t *testing.T) {
	t.Run("small n: a 10 point drop is inconclusive_noise, no finding", func(t *testing.T) {
		h := newHarness(t, nil)
		admin := h.adminLogin()
		tenant := h.createOrg(admin, "acme")
		suiteID := regressionIntervalFixture(t, h, admin, tenant, 10, 0.05)

		cand := launchRun(t, h, admin, tenant, suiteID, candidateOutputs(10, 1))
		if d := floatOf(cand.body["drift"]); d < 0.09 || d > 0.11 {
			t.Fatalf("drift = %v, want ~0.1 (the drop is over the 0.05 threshold)", d)
		}
		if cand.body["regressed"] != false {
			t.Fatalf("regressed = %v, want false: 9/10 vs 10/10 is inside the noise", cand.body["regressed"])
		}
		cmp := comparisonOf(cand)
		if cmp["status"] != "comparable" || cmp["reason"] != "inconclusive_noise" {
			t.Fatalf("comparison status/reason = %v/%v, want comparable/inconclusive_noise", cmp["status"], cmp["reason"])
		}
		bp, bn, p, n, drop, lo, hi := passRateDiffOf(t, cmp)
		if bp != 10 || bn != 10 || p != 9 || n != 10 {
			t.Fatalf("denominators = %d/%d baseline, %d/%d candidate, want 10/10 and 9/10", bp, bn, p, n)
		}
		if lo > 0 || hi <= drop {
			t.Fatalf("drop_ci = [%v,%v] around drop %v, want an interval that includes zero", lo, hi, drop)
		}
		if finds := h.coreFindings(tenant, findingKindRegression); len(finds) != 0 {
			t.Fatalf("eval_regression findings = %d, want 0 for a noise-level drop", len(finds))
		}
	})

	t.Run("large n: the same 10 point drop is a finding", func(t *testing.T) {
		h := newHarness(t, nil)
		admin := h.adminLogin()
		tenant := h.createOrg(admin, "acme")
		suiteID := regressionIntervalFixture(t, h, admin, tenant, 100, 0.05)

		cand := launchRun(t, h, admin, tenant, suiteID, candidateOutputs(100, 10))
		if cand.body["regressed"] != true {
			t.Fatalf("regressed = %v, want true: 90/100 vs 100/100 excludes zero", cand.body["regressed"])
		}
		cmp := comparisonOf(cand)
		if cmp["reason"] != "comparison_valid" {
			t.Fatalf("reason = %v, want comparison_valid on a real regression", cmp["reason"])
		}
		bp, bn, p, n, _, lo, _ := passRateDiffOf(t, cmp)
		if bp != 100 || bn != 100 || p != 90 || n != 100 || lo <= 0 {
			t.Fatalf("pass_rate_diff = %d/%d vs %d/%d lo=%v, want 100/100 vs 90/100 with drop_ci.lo > 0", bp, bn, p, n, lo)
		}
		if finds := h.coreFindings(tenant, findingKindRegression); len(finds) != 1 {
			t.Fatalf("eval_regression findings = %d, want 1", len(finds))
		}
	})

	t.Run("gate: noise passes, notes it, and reads back with the interval", func(t *testing.T) {
		h := newHarness(t, nil)
		admin := h.adminLogin()
		tenant := h.createOrg(admin, "acme")
		suiteID := regressionIntervalFixture(t, h, admin, tenant, 10, 0.05)

		r := launchGate(t, h, admin, tenant, suiteID, candidateOutputs(10, 1))
		if r.body["verdict"] != verdictPass || r.body["effective_verdict"] != verdictPass {
			t.Fatalf("verdict = %v/%v, want pass/pass: a noise-level drop must not block CI", r.body["verdict"], r.body["effective_verdict"])
		}
		if got, want := r.body["reasons"], []any{"inconclusive_noise"}; !reflect.DeepEqual(got, want) {
			t.Fatalf("reasons = %v, want exactly %v", got, want)
		}
		if _, _, _, _, _, lo, _ := passRateDiffOf(t, comparisonOf(r)); lo > 0 {
			t.Fatalf("gate drop_ci.lo = %v, want an interval that includes zero", lo)
		}
		// The retained gate keeps the same note and interval.
		again := h.do("GET", "/v1/m/evals/gate/"+r.body["id"].(string), admin, nil, tenantHdr(tenant))
		if again.code != http.StatusOK || !hasReason(again.body, "inconclusive_noise") || comparisonOf(again)["pass_rate_diff"] == nil {
			t.Fatalf("retained gate = %d %s, want the note and pass_rate_diff", again.code, again.raw)
		}
	})
}

// TestRegressionIntervalBoundary pins which side of zero decides: with a 0.03
// threshold at n=100, 100/100 → 96/100 has lo ≈ -0.004 (noise) and 100/100 →
// 95/100 has lo ≈ +0.003 (regression).
func TestRegressionIntervalBoundary(t *testing.T) {
	for _, tc := range []struct {
		name      string
		wrong     int
		regressed bool
	}{
		{"4 points wrong: interval includes zero", 4, false},
		{"5 points wrong: interval excludes zero", 5, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, nil)
			admin := h.adminLogin()
			tenant := h.createOrg(admin, "acme")
			suiteID := regressionIntervalFixture(t, h, admin, tenant, 100, 0.03)
			cand := launchRun(t, h, admin, tenant, suiteID, candidateOutputs(100, tc.wrong))
			if cand.body["regressed"] != tc.regressed {
				t.Fatalf("regressed = %v, want %v: %v", cand.body["regressed"], tc.regressed, comparisonOf(cand)["pass_rate_diff"])
			}
		})
	}
}

// TestRegressionControlsAreNotNoise guards the other half of the rule: a run that
// did not drop over the threshold is not "noise", whatever its interval says.
func TestRegressionControlsAreNotNoise(t *testing.T) {
	for _, tc := range []struct {
		name  string
		wrong int
	}{
		{"identical run", 0},
		{"drop under the threshold with an interval that excludes zero", 10},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, nil)
			admin := h.adminLogin()
			tenant := h.createOrg(admin, "acme")
			suiteID := regressionIntervalFixture(t, h, admin, tenant, 100, 0.2)
			r := launchGate(t, h, admin, tenant, suiteID, candidateOutputs(100, tc.wrong))
			run, _ := r.body["run"].(map[string]any)
			if r.body["verdict"] != verdictPass || run["regressed"] != false {
				t.Fatalf("verdict=%v regressed=%v, want pass and not regressed: %s", r.body["verdict"], run["regressed"], r.raw)
			}
			if reason := comparisonOf(r)["reason"]; reason != "comparison_valid" || hasReason(r.body, "inconclusive_noise") {
				t.Fatalf("reason = %v reasons = %v, want comparison_valid and no noise note", reason, r.body["reasons"])
			}
		})
	}
}

// TestNoiseRunIsNotTheNextBaseline: 10/10 → 9/10 is noise, and 9/10 → 6/10 is noise
// too (lo ≈ -0.08), but 10/10 → 6/10 is not (lo ≈ +0.04). The slide must be caught
// because the noise run did not replace the baseline.
func TestNoiseRunIsNotTheNextBaseline(t *testing.T) {
	h := newHarness(t, nil)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	suiteID := regressionIntervalFixture(t, h, admin, tenant, 10, 0.05)

	second := launchRun(t, h, admin, tenant, suiteID, candidateOutputs(10, 1))
	if second.body["regressed"] != false || comparisonOf(second)["reason"] != "inconclusive_noise" {
		t.Fatalf("second run = regressed %v reason %v, want a noise-level drop", second.body["regressed"], comparisonOf(second)["reason"])
	}
	third := launchRun(t, h, admin, tenant, suiteID, candidateOutputs(10, 4))
	bp, bn, p, n, _, lo, _ := passRateDiffOf(t, comparisonOf(third))
	if bp != 10 || bn != 10 || p != 6 || n != 10 || lo <= 0 {
		t.Fatalf("third run compared %d/%d baseline vs %d/%d (lo %v), want the 10/10 run as baseline and lo > 0", bp, bn, p, n, lo)
	}
	if third.body["regressed"] != true {
		t.Fatalf("third run regressed = %v, want true: the slide 10/10 → 9/10 → 6/10 must be caught", third.body["regressed"])
	}
}

// completeFixture runs comparisonPlan.complete over n selected cases, `passed` of
// which passed with mean score `score`, against a baseline receipt.
func completeFixture(t *testing.T, n, passed int, score float64, baseline ComparisonEvidence, threshold float64) ComparisonEvidence {
	t.Helper()
	selected := make([]caseDTO, 0, n)
	results := make([]caseResult, 0, n)
	for i := 0; i < n; i++ {
		key := fmt.Sprintf("k%03d", i)
		selected = append(selected, caseDTO{CaseKey: key, Input: "x", Expected: "a", Weight: 1})
		outcome := outcomeFail
		if i < passed {
			outcome = outcomePass
		}
		results = append(results, caseResult{caseKey: key, outcome: outcome})
	}
	pop, err := population(selected)
	if err != nil {
		t.Fatal(err)
	}
	plan := &comparisonPlan{
		evidence: ComparisonEvidence{Version: 1, Status: ComparisonComparable, Reason: ComparisonValid, Mode: "same_candidate", Known: true,
			Identity: comparisonIdentity{Selected: pop, RegressionThreshold: threshold}},
		baseline: &baseline,
		selected: selected,
	}
	got, err := plan.complete(runAggregate{total: n, passed: passed, failed: n - passed, score: score, passRate: float64(passed) / float64(n), cases: results})
	if err != nil {
		t.Fatal(err)
	}
	return got
}

// TestCompleteRegressionRule pins the rule where the HTTP fixtures cannot reach: a
// score drop with an unchanged pass rate, a one-case suite, and a baseline receipt
// that carries no usable pass rate or denominator.
func TestCompleteRegressionRule(t *testing.T) {
	baseline := func(n int, rate, score float64) ComparisonEvidence {
		return ComparisonEvidence{Version: 1, Status: ComparisonComparable, Known: true, Complete: true,
			Scored: populationIdentity{Count: n}, PassRate: rate, Score: score}
	}
	for _, tc := range []struct {
		name      string
		n, passed int
		score     float64
		baseline  ComparisonEvidence
		regressed bool
		status    ComparisonStatus
		reason    ComparisonReason
	}{
		// A judge scorer can lower the mean score without changing a single pass/fail
		// outcome. The rule is the issue's literal one (threshold AND pass-rate
		// interval): that drop is not confirmed, at any n, and the reason says so.
		{"score drop with unchanged pass rate is not confirmed", 500, 450, 0.6, baseline(500, 0.9, 0.9), false, ComparisonComparable, ComparisonInconclusiveNoise},
		{"one case cannot show a regression", 1, 0, 0, baseline(1, 1, 1), false, ComparisonComparable, ComparisonInconclusiveNoise},
		{"two cases can", 2, 0, 0, baseline(2, 1, 1), true, ComparisonComparable, ComparisonValid},
		{"baseline pass rate above 1 is unknown evidence, not noise", 100, 50, 0.5, baseline(100, 2, 1), false, ComparisonUnknown, ComparisonBaselineUnknown},
		{"baseline pass rate below 0 is unknown evidence, not noise", 100, 50, 0.5, baseline(100, -0.5, 1), false, ComparisonUnknown, ComparisonBaselineUnknown},
		{"baseline without a denominator is unknown evidence, not noise", 100, 50, 0.5, baseline(0, 1, 1), false, ComparisonUnknown, ComparisonBaselineUnknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := completeFixture(t, tc.n, tc.passed, tc.score, tc.baseline, 0.05)
			if got.Regressed != tc.regressed || got.Status != tc.status || got.Reason != tc.reason {
				t.Fatalf("regressed=%v status=%s reason=%s, want %v %s %s (pass_rate_diff %+v)", got.Regressed, got.Status, got.Reason, tc.regressed, tc.status, tc.reason, got.PassRateDiff)
			}
		})
	}
}
