// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package evals

import (
	"context"
	"net/http"
	"testing"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

func TestGateExplicitMissingBaselineFails(t *testing.T) {
	h := newHarness(t, nil)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "missing-baseline")
	suite := h.createSuite(admin, tenant, map[string]any{
		"name": "comparison", "subject_kind": "model", "scorer": "exact",
		"pass_threshold": 0.5, "regression_threshold": 0.2,
	})
	h.addCase(admin, tenant, suite, map[string]any{"case_key": "one", "input": "question", "expected": "answer"})
	request := map[string]any{
		"suite_ref": suite, "subject_ref": "candidate", "outputs": map[string]string{"one": "answer"},
	}
	// An otherwise identical first run is permitted by the absolute threshold.
	control := h.do("POST", "/v1/m/evals/gate", admin, request, tenantHdr(tenant))
	if control.code != http.StatusCreated || control.body["verdict"] != "pass" || !hasReason(control.body, "no_baseline") {
		t.Fatalf("first-run control: %d %s", control.code, control.raw)
	}
	request["baseline_ref"] = "00000000-0000-4000-8000-000000000099"
	got := h.do("POST", "/v1/m/evals/gate", admin, request, tenantHdr(tenant))
	if got.code != http.StatusCreated || got.body["verdict"] != "fail" || got.body["effective_verdict"] != "fail" {
		t.Fatalf("missing requested baseline must fail despite passing outputs: %d %s", got.code, got.raw)
	}
	if !hasReason(got.body, "baseline_unavailable") {
		t.Fatalf("missing baseline reason: %s", got.raw)
	}
	id, ok := got.body["id"].(string)
	if !ok || id == "" {
		t.Fatalf("failed comparison must be retained: %s", got.raw)
	}
	retained := h.do("GET", "/v1/m/evals/gate/"+id, admin, nil, tenantHdr(tenant))
	if retained.code != http.StatusOK || retained.body["effective_verdict"] != "fail" || !hasReason(retained.body, "baseline_unavailable") {
		t.Fatalf("GET must preserve comparison refusal: %d %s", retained.code, retained.raw)
	}
}

func TestComparisonGateIdentityAndCandidateChange(t *testing.T) {
	h := newHarness(t, nil)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "comparison-identity")
	suite := h.createSuite(admin, tenant, map[string]any{"name": "identity", "subject_kind": "model", "scorer": "exact", "pass_threshold": 0.5, "regression_threshold": 0.2})
	h.addCase(admin, tenant, suite, map[string]any{"case_key": "one", "input": "question", "expected": "answer"})
	req := map[string]any{"suite_ref": suite, "subject_ref": "model-a", "outputs": map[string]string{"one": "answer"}}
	first := h.do("POST", "/v1/m/evals/gate", admin, req, tenantHdr(tenant))
	if first.code != http.StatusCreated || first.body["verdict"] != "pass" {
		t.Fatalf("first control: %s", first.raw)
	}
	base := first.body["run_ref"].(string)
	req["baseline_ref"] = base
	same := h.do("POST", "/v1/m/evals/gate", admin, req, tenantHdr(tenant))
	assertComparison(t, same.body, "comparable")
	if same.body["verdict"] != "pass" {
		t.Fatalf("same identity: %s", same.raw)
	}
	req["subject_ref"] = "model-b"
	different := h.do("POST", "/v1/m/evals/gate", admin, req, tenantHdr(tenant))
	assertComparison(t, different.body, "incompatible")
	if different.body["verdict"] != "fail" {
		t.Fatalf("undeclared candidate change: %s", different.raw)
	}
	req["comparison"] = map[string]any{"version": 1, "mode": "candidate_change"}
	changed := h.do("POST", "/v1/m/evals/gate", admin, req, tenantHdr(tenant))
	assertComparison(t, changed.body, "comparable")
	if changed.body["verdict"] != "pass" {
		t.Fatalf("declared candidate change: %s", changed.raw)
	}
	h.addCase(admin, tenant, suite, map[string]any{"case_key": "two", "input": "other", "expected": "answer"})
	req["outputs"] = map[string]string{"one": "answer", "two": "answer"}
	appended := h.do("POST", "/v1/m/evals/gate", admin, req, tenantHdr(tenant))
	assertComparison(t, appended.body, "incompatible")
	if appended.body["verdict"] != "fail" {
		t.Fatalf("changed cases at same suite version: %s", appended.raw)
	}
	old := h.do("GET", "/v1/m/evals/gate/"+same.body["id"].(string), admin, nil, tenantHdr(tenant))
	assertComparison(t, old.body, "comparable")
}

func assertComparison(t *testing.T, body map[string]any, status string) {
	t.Helper()
	c, ok := body["comparison"].(map[string]any)
	if !ok || c["status"] != status {
		t.Fatalf("comparison status want %s: %#v", status, body)
	}
}

func comparisonFixture(t *testing.T, h *harness, admin string, tenant model.TenantID, name string) string {
	t.Helper()
	suite := h.createSuite(admin, tenant, map[string]any{"name": name, "subject_kind": "model", "scorer": "exact", "pass_threshold": 0.5, "regression_threshold": 0.2})
	h.addCase(admin, tenant, suite, map[string]any{"case_key": "one", "input": "question", "expected": "answer"})
	return suite
}
func comparisonRun(t *testing.T, h *harness, admin string, tenant model.TenantID, suite, subject string) resp {
	t.Helper()
	r := h.do("POST", "/v1/m/evals/runs", admin, map[string]any{"suite_ref": suite, "subject_ref": subject, "outputs": map[string]string{"one": "answer"}}, tenantHdr(tenant))
	if r.code != http.StatusCreated {
		t.Fatalf("run: %d %s", r.code, r.raw)
	}
	return r
}
func TestBaselineTenantAndSubjectValidation(t *testing.T) {
	h := newHarness(t, nil)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "comparison-tenant")
	other := h.createOrg(admin, "comparison-other")
	suite := comparisonFixture(t, h, admin, tenant, "local")
	foreignSuite := comparisonFixture(t, h, admin, other, "foreign")
	local := comparisonRun(t, h, admin, tenant, suite, "model-a")
	foreign := comparisonRun(t, h, admin, other, foreignSuite, "model-a")
	for _, ref := range []string{foreign.body["id"].(string), "00000000-0000-4000-8000-000000000099"} {
		r := h.do("POST", "/v1/m/evals/gate", admin, map[string]any{"suite_ref": suite, "subject_ref": "model-a", "baseline_ref": ref, "outputs": map[string]string{"one": "answer"}}, tenantHdr(tenant))
		if r.code != http.StatusCreated || r.body["verdict"] != "fail" || !hasReason(r.body, "baseline_unavailable") {
			t.Fatalf("non-disclosing refusal: %s", r.raw)
		}
	}
	for _, subject := range []string{"other-subject", "model-a"} {
		r := h.do("POST", "/v1/m/evals/baselines", admin, map[string]any{"suite_ref": suite, "subject_ref": subject, "run_ref": local.body["id"]}, tenantHdr(tenant))
		want := http.StatusConflict
		if subject == "model-a" {
			want = http.StatusCreated
		}
		if r.code != want {
			t.Fatalf("pin %s: %d %s", subject, r.code, r.raw)
		}
	}
	otherLocalSuite := comparisonFixture(t, h, admin, tenant, "other-local")
	wrongSuite := h.do("POST", "/v1/m/evals/baselines", admin, map[string]any{"suite_ref": otherLocalSuite, "subject_ref": "model-a", "run_ref": local.body["id"]}, tenantHdr(tenant))
	if wrongSuite.code != http.StatusConflict {
		t.Fatalf("cross-suite pin accepted: %s", wrongSuite.raw)
	}
	bad := h.do("POST", "/v1/m/evals/gate", admin, map[string]any{"suite_ref": suite, "baseline_ref": "not-a-uuid", "outputs": map[string]string{"one": "answer"}}, tenantHdr(tenant))
	if bad.code != http.StatusBadRequest {
		t.Fatalf("malformed ref: %d %s", bad.code, bad.raw)
	}
}
func TestStalePinDoesNotFallBack(t *testing.T) {
	h := newHarness(t, nil)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "stale-pin")
	suite := comparisonFixture(t, h, admin, tenant, "stale")
	base := comparisonRun(t, h, admin, tenant, suite, "model-a")
	pin := h.do("POST", "/v1/m/evals/baselines", admin, map[string]any{"suite_ref": suite, "subject_ref": "model-a", "run_ref": base.body["id"]}, tenantHdr(tenant))
	if pin.code != http.StatusCreated {
		t.Fatal(pin.raw)
	}
	err := h.st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		repo, e := sc.Ext(baseKind)
		if e != nil {
			return e
		}
		rec, e := repo.Get(context.Background(), model.ID(pin.body["id"].(string)))
		if e != nil {
			return e
		}
		rec[colBaseRunRef] = "00000000-0000-4000-8000-000000000099"
		_, e = repo.Update(context.Background(), rec)
		return e
	})
	if err != nil {
		t.Fatal(err)
	}
	req := map[string]any{"suite_ref": suite, "subject_ref": "model-a", "outputs": map[string]string{"one": "answer"}}
	r := h.do("POST", "/v1/m/evals/gate", admin, req, tenantHdr(tenant))
	if r.body["verdict"] != "fail" || !hasReason(r.body, "baseline_unavailable") {
		t.Fatalf("stale pin fell through: %s", r.raw)
	}
	fixed := h.do("POST", "/v1/m/evals/baselines", admin, map[string]any{"suite_ref": suite, "subject_ref": "model-a", "run_ref": base.body["id"]}, tenantHdr(tenant))
	if fixed.code != http.StatusCreated {
		t.Fatal(fixed.raw)
	}
	r = h.do("POST", "/v1/m/evals/gate", admin, req, tenantHdr(tenant))
	assertComparison(t, r.body, "comparable")
}
func TestComparisonSameProtocolPositive(t *testing.T) {
	h := newHarness(t, nil)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "comparison-drift")
	suite := comparisonFixture(t, h, admin, tenant, "drift")
	base := comparisonRun(t, h, admin, tenant, suite, "model-a")
	// A mutable aggregate is not the immutable metric source used for comparison.
	if err := h.st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		repo, e := sc.Ext(runKind)
		if e != nil {
			return e
		}
		rec, e := repo.Get(context.Background(), model.ID(base.body["id"].(string)))
		if e != nil {
			return e
		}
		rec[colScore] = 0.0
		_, e = repo.Update(context.Background(), rec)
		return e
	}); err != nil {
		t.Fatal(err)
	}
	r := h.do("POST", "/v1/m/evals/gate", admin, map[string]any{"suite_ref": suite, "subject_ref": "model-a", "outputs": map[string]string{"one": "wrong"}}, tenantHdr(tenant))
	assertComparison(t, r.body, "comparable")
	if !hasReason(r.body, "regression_vs_baseline") {
		t.Fatalf("immutable baseline score was lost: %s", r.raw)
	}
}
func TestGateRejectsChangedSuiteOrCases(t *testing.T) {
	h := newHarness(t, nil)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "suite-identity")
	suite := comparisonFixture(t, h, admin, tenant, "versioned")
	base := comparisonRun(t, h, admin, tenant, suite, "model-a")
	v2 := h.createSuite(admin, tenant, map[string]any{"name": "versioned", "suite_version": 2, "subject_kind": "model", "scorer": "exact", "regression_threshold": 0.2})
	h.addCase(admin, tenant, v2, map[string]any{"case_key": "one", "input": "question", "expected": "answer"})
	// Automatic selection is already scoped to suite_ref; normal version creation
	// produces another ID. Preserve that behavior for the SAME declared candidate.
	first := h.do("POST", "/v1/m/evals/gate", admin, map[string]any{"suite_ref": v2, "subject_ref": "model-a", "outputs": map[string]string{"one": "answer"}}, tenantHdr(tenant))
	assertComparison(t, first.body, "no_baseline")
	r := h.do("POST", "/v1/m/evals/gate", admin, map[string]any{"suite_ref": v2, "subject_ref": "model-a", "baseline_ref": base.body["id"], "outputs": map[string]string{"one": "answer"}}, tenantHdr(tenant))
	assertComparison(t, r.body, "incompatible")
	if r.body["verdict"] != "fail" {
		t.Fatal(r.raw)
	}

}
func TestCandidateChangeRequiresExplicitExperiment(t *testing.T) {
	h := newHarness(t, nil)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "candidate-mode")
	suite := comparisonFixture(t, h, admin, tenant, "candidate")
	base := comparisonRun(t, h, admin, tenant, suite, "model-a")
	req := map[string]any{"suite_ref": suite, "subject_ref": "model-b", "model_ref": "model-b", "prompt_variant": "new", "outputs": map[string]string{"one": "answer"}, "comparison": map[string]any{"version": 1, "mode": "candidate_change"}}
	r := h.do("POST", "/v1/m/evals/gate", admin, req, tenantHdr(tenant))
	if r.code != http.StatusBadRequest {
		t.Fatalf("experiment without baseline: %s", r.raw)
	}
	req["baseline_ref"] = base.body["id"]
	r = h.do("POST", "/v1/m/evals/gate", admin, req, tenantHdr(tenant))
	assertComparison(t, r.body, "comparable")
	c := r.body["comparison"].(map[string]any)
	if len(c["changed"].([]any)) != 3 {
		t.Fatalf("changed dimensions absent: %s", r.raw)
	}
	req["subject_kind"] = "agent"
	delete(req, "model_ref")
	r = h.do("POST", "/v1/m/evals/gate", admin, req, tenantHdr(tenant))
	assertComparison(t, r.body, "unknown")
}

func TestComparisonExplicitVersionIsValidated(t *testing.T) {
	h := newHarness(t, nil)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "comparison-version")
	suite := comparisonFixture(t, h, admin, tenant, "version")
	req := map[string]any{"suite_ref": suite, "subject_ref": "model-a", "outputs": map[string]string{"one": "answer"}}
	for _, c := range []map[string]any{{}, {"version": 0, "mode": "same_candidate"}, {"version": 2, "mode": "same_candidate"}, {"version": 1, "mode": "unsupported"}} {
		req["comparison"] = c
		r := h.do("POST", "/v1/m/evals/gate", admin, req, tenantHdr(tenant))
		if r.code != http.StatusBadRequest {
			t.Fatalf("invalid explicit comparison %#v accepted: %d %s", c, r.code, r.raw)
		}
	}
	delete(req, "comparison")
	r := h.do("POST", "/v1/m/evals/gate", admin, req, tenantHdr(tenant))
	assertComparison(t, r.body, "no_baseline")
	if r.body["verdict"] != "pass" {
		t.Fatal(r.raw)
	}
}
