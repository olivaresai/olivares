// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package evals

import (
	"net/http"
	"testing"
)

// calItem builds one labeling-request item.
func calItem(key, output string, humanPassed bool) map[string]any {
	return map[string]any{
		"case_key": key, "output": output, "criterion": "GOOD", "human_passed": humanPassed,
	}
}

// TestCalibrationItemsUpsert proves labeling is an upsert: a new (set, case_key)
// creates, a re-label of the same key UPDATES in place (the audited correction).
func TestCalibrationItemsUpsert(t *testing.T) {
	h := newHarness(t, fakeJudge{})
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")

	r := h.do("POST", "/v1/m/evals/calibration/items", admin, map[string]any{
		"set_name": "ref", "items": []any{calItem("k1", "GOOD a", true), calItem("k2", "meh", false)},
	}, tenantHdr(tenant))
	if r.code != http.StatusCreated {
		t.Fatalf("add items = %d %s", r.code, r.raw)
	}
	if intOf(r.body["created"]) != 2 || intOf(r.body["updated"]) != 0 {
		t.Fatalf("created/updated = %v/%v, want 2/0", r.body["created"], r.body["updated"])
	}

	// Re-label k1 (flip the human verdict): an update, not a duplicate.
	r = h.do("POST", "/v1/m/evals/calibration/items", admin, map[string]any{
		"set_name": "ref", "items": []any{calItem("k1", "GOOD a", false)},
	}, tenantHdr(tenant))
	if r.code != http.StatusCreated {
		t.Fatalf("relabel = %d %s", r.code, r.raw)
	}
	if intOf(r.body["created"]) != 0 || intOf(r.body["updated"]) != 1 {
		t.Fatalf("relabel created/updated = %v/%v, want 0/1", r.body["created"], r.body["updated"])
	}

	list := h.do("GET", "/v1/m/evals/calibration/items?set=ref", admin, nil, tenantHdr(tenant))
	items, _ := list.body["items"].([]any)
	if len(items) != 2 {
		t.Fatalf("items = %d, want 2 (upsert, no duplicate)", len(items))
	}
}

// TestCalibrationMeasuresPerfectAgreement runs a calibration where the fake judge
// agrees with every human label on a BALANCED set: agreement 1.0, kappa 1.0
// (defined), sensitivity/specificity 1.0 with their denominators, meets_target, a
// core EvalResult as evidence, and no finding.
func TestCalibrationMeasuresPerfectAgreement(t *testing.T) {
	h := newHarness(t, fakeJudge{})
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")

	// fakeJudge passes iff the output contains the criterion ("GOOD").
	h.do("POST", "/v1/m/evals/calibration/items", admin, map[string]any{
		"set_name": "ref", "items": []any{
			calItem("k1", "GOOD a", true), calItem("k2", "meh", false),
			calItem("k3", "GOOD b", true), calItem("k4", "nope", false),
		},
	}, tenantHdr(tenant))

	r := h.do("POST", "/v1/m/evals/calibration/run", admin, map[string]any{
		"set_name": "ref", "judge_model": "claude-x",
	}, tenantHdr(tenant))
	if r.code != http.StatusCreated {
		t.Fatalf("calibration run = %d %s", r.code, r.raw)
	}
	if floatOf(r.body["agreement"]) != 1.0 {
		t.Errorf("agreement = %v, want 1.0", r.body["agreement"])
	}
	if floatOf(r.body["kappa"]) != 1.0 || r.body["kappa_defined"] != true {
		t.Errorf("kappa = %v defined=%v, want 1.0 true", r.body["kappa"], r.body["kappa_defined"])
	}
	if floatOf(r.body["sensitivity"]) != 1.0 || intOf(r.body["sensitivity_n"]) != 2 {
		t.Errorf("sens = %v n=%v, want 1.0 n=2", r.body["sensitivity"], r.body["sensitivity_n"])
	}
	if floatOf(r.body["specificity"]) != 1.0 || intOf(r.body["specificity_n"]) != 2 {
		t.Errorf("spec = %v n=%v, want 1.0 n=2", r.body["specificity"], r.body["specificity_n"])
	}
	if r.body["meets_target"] != true {
		t.Errorf("meets_target = %v, want true", r.body["meets_target"])
	}
	ci, _ := r.body["agreement_ci"].(map[string]any)
	if ci == nil || floatOf(ci["hi"]) != 1.0 || floatOf(ci["lo"]) <= 0 {
		t.Errorf("agreement_ci = %v, want a real Wilson interval", r.body["agreement_ci"])
	}

	// The canonical evidence stream.
	results := h.coreEvalResults(tenant, calibrationSuite)
	if len(results) != 1 || !results[0].Passed {
		t.Fatalf("core judge-calibration EvalResults = %d (passed=%v), want 1 passed", len(results), results)
	}
	if finds := h.coreFindings(tenant, findingKindCalibration); len(finds) != 0 {
		t.Fatalf("findings = %d, want 0 (calibration met target)", len(finds))
	}
}

// TestCalibrationBelowTargetEmitsFinding flips two human labels so the judge
// disagrees on half the set: agreement 0.5, meets_target=false, a persisted
// judge_calibration Finding and a bus FindingReport.
func TestCalibrationBelowTargetEmitsFinding(t *testing.T) {
	h := newHarness(t, fakeJudge{})
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")

	h.do("POST", "/v1/m/evals/calibration/items", admin, map[string]any{
		"set_name": "ref", "items": []any{
			calItem("k1", "GOOD a", false), // judge passes, human says fail → FP
			calItem("k2", "meh", true),     // judge fails, human says pass → FN
			calItem("k3", "GOOD b", true),  // agree
			calItem("k4", "nope", false),   // agree
		},
	}, tenantHdr(tenant))

	r := h.do("POST", "/v1/m/evals/calibration/run", admin, map[string]any{"set_name": "ref"}, tenantHdr(tenant))
	if r.code != http.StatusCreated {
		t.Fatalf("calibration run = %d %s", r.code, r.raw)
	}
	if floatOf(r.body["agreement"]) != 0.5 || r.body["meets_target"] != false {
		t.Fatalf("agreement = %v meets=%v, want 0.5 false", r.body["agreement"], r.body["meets_target"])
	}

	finds := h.coreFindings(tenant, findingKindCalibration)
	if len(finds) != 1 {
		t.Fatalf("judge_calibration findings = %d, want 1", len(finds))
	}
	h.waitFindings()
	delivered := h.deliveredFindings()
	if len(delivered) != 1 || delivered[0].Kind != busJudgeCalibration {
		t.Fatalf("bus findings = %+v, want one judge_calibration", delivered)
	}
}

// TestCalibrationImbalancedSetCannotCertify proves the honesty rule: an all-pass
// human reference yields perfect agreement but an UNDEFINED kappa, so it can never
// certify the judge (meets_target=false) — the set must contain both label kinds.
func TestCalibrationImbalancedSetCannotCertify(t *testing.T) {
	h := newHarness(t, fakeJudge{})
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")

	h.do("POST", "/v1/m/evals/calibration/items", admin, map[string]any{
		"set_name": "allpass", "items": []any{
			calItem("k1", "GOOD a", true), calItem("k2", "GOOD b", true), calItem("k3", "GOOD c", true),
		},
	}, tenantHdr(tenant))

	r := h.do("POST", "/v1/m/evals/calibration/run", admin, map[string]any{"set_name": "allpass"}, tenantHdr(tenant))
	if r.code != http.StatusCreated {
		t.Fatalf("calibration run = %d %s", r.code, r.raw)
	}
	if floatOf(r.body["agreement"]) != 1.0 {
		t.Errorf("agreement = %v, want 1.0", r.body["agreement"])
	}
	if r.body["kappa_defined"] != false {
		t.Errorf("kappa_defined = %v, want false (both raters constant)", r.body["kappa_defined"])
	}
	if r.body["meets_target"] != false {
		t.Errorf("meets_target = %v, want false — an imbalanced set certifies nothing", r.body["meets_target"])
	}
}

// TestCalibrationFloorsCannotBeWeakened proves a request below the committed
// defaults is clamped UP (target 0.85 / kappa floor 0.6 are the project's bar).
func TestCalibrationFloorsCannotBeWeakened(t *testing.T) {
	h := newHarness(t, fakeJudge{})
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	h.do("POST", "/v1/m/evals/calibration/items", admin, map[string]any{
		"set_name": "ref", "items": []any{calItem("k1", "GOOD a", true), calItem("k2", "meh", false)},
	}, tenantHdr(tenant))

	r := h.do("POST", "/v1/m/evals/calibration/run", admin, map[string]any{
		"set_name": "ref", "target": 0.10, "kappa_floor": 0.05,
	}, tenantHdr(tenant))
	if r.code != http.StatusCreated {
		t.Fatalf("calibration run = %d %s", r.code, r.raw)
	}
	if floatOf(r.body["target"]) != defaultAgreementTarget {
		t.Errorf("target = %v, want clamped to %v", r.body["target"], defaultAgreementTarget)
	}
	if floatOf(r.body["kappa_floor"]) != defaultKappaFloor {
		t.Errorf("kappa_floor = %v, want clamped to %v", r.body["kappa_floor"], defaultKappaFloor)
	}
}

// TestCalibrationWithoutJudge412 proves a calibration cannot be simulated: with no
// judge wired the endpoint is an honest 412, never a fabricated report.
func TestCalibrationWithoutJudge412(t *testing.T) {
	h := newHarness(t, nil) // offline judge
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	if r := h.do("POST", "/v1/m/evals/calibration/run", admin, map[string]any{}, tenantHdr(tenant)); r.code != http.StatusPreconditionFailed {
		t.Fatalf("calibration without judge = %d, want 412", r.code)
	}
}
