// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package evals

import (
	"context"
	"net/http"
	"reflect"
	"testing"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

type declaredTestScorer struct {
	version    string
	during     func()
	errorInput string
}

func (*declaredTestScorer) ID() string { return scorerExact }
func (s *declaredTestScorer) Score(_ context.Context, in ScoreInput) ScoreResult {
	if s.during != nil {
		fn := s.during
		s.during = nil
		fn()
	}
	if s.errorInput != "" && in.Input == s.errorInput {
		return ScoreResult{Outcome: outcomeError, Reason: "fixture scoring failure"}
	}
	return scoreExact(in)
}
func (s *declaredTestScorer) ScoringProtocol(string) (ScoringProtocol, bool) {
	return ScoringProtocol{Implementation: "fixture/exact", Version: s.version, ConfigDigest: hashHex("fixture")}, true
}

type unversionedTestScorer struct{}

func (unversionedTestScorer) ID() string { return scorerExact }
func (unversionedTestScorer) Score(_ context.Context, in ScoreInput) ScoreResult {
	return scoreExact(in)
}

func TestComparisonScorerProtocolChange(t *testing.T) {
	scorer := &declaredTestScorer{version: "1"}
	h := newHarness(t, nil, WithScorer(scorer))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "scorer-protocol")
	suite := comparisonFixture(t, h, admin, tenant, "protocol")
	base := comparisonRun(t, h, admin, tenant, suite, "model-a")
	req := map[string]any{"suite_ref": suite, "subject_ref": "model-a", "baseline_ref": base.body["id"], "outputs": map[string]string{"one": "answer"}}
	r := h.do("POST", "/v1/m/evals/gate", admin, req, tenantHdr(tenant))
	assertComparison(t, r.body, "comparable")
	scorer.version = "2"
	r = h.do("POST", "/v1/m/evals/gate", admin, req, tenantHdr(tenant))
	assertComparison(t, r.body, "incompatible")
	if r.body["verdict"] != "fail" {
		t.Fatal(r.raw)
	}
	unknown := newHarness(t, nil, WithScorer(unversionedTestScorer{}))
	a := unknown.adminLogin()
	ten := unknown.createOrg(a, "unknown-scorer")
	su := comparisonFixture(t, unknown, a, ten, "unknown")
	r = unknown.do("POST", "/v1/m/evals/gate", a, map[string]any{"suite_ref": su, "subject_ref": "model-a", "outputs": map[string]string{"one": "answer"}}, tenantHdr(ten))
	assertComparison(t, r.body, "unknown")
	if r.body["verdict"] != "fail" {
		t.Fatalf("same-ID override inherited metadata: %s", r.raw)
	}
}

func TestSampleAndScoredPopulationIdentity(t *testing.T) {
	h := newHarness(t, nil)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "sample-identity")
	suite := comparisonFixture(t, h, admin, tenant, "samples")
	h.addCase(admin, tenant, suite, map[string]any{"case_key": "two", "input": "other", "expected": "answer"})
	req := map[string]any{"suite_ref": suite, "subject_ref": "model-a", "outputs": map[string]string{"one": "answer", "two": "answer"}, "seed": "one", "sample_size": 1}
	first := h.do("POST", "/v1/m/evals/gate", admin, req, tenantHdr(tenant))
	if first.code != http.StatusCreated {
		t.Fatal(first.raw)
	}
	req["baseline_ref"] = first.body["run_ref"]
	same, different := "", ""
	// Choose seeds by inspecting the actual selected key, not by assuming counts
	// or hash collisions represent the same population.
	cases := []caseDTO{{CaseKey: "one"}, {CaseKey: "two"}}
	key := sampleCases(cases, "one", 1)[0].CaseKey
	for _, seed := range []string{"a", "b", "c", "d", "e", "f", "g", "h", "i", "j", "k", "l"} {
		if sampleCases(cases, seed, 1)[0].CaseKey == key {
			same = seed
		} else {
			different = seed
		}
	}
	if same == "" || different == "" {
		t.Fatal("seed fixture lacks both controls")
	}
	req["seed"] = same
	r := h.do("POST", "/v1/m/evals/gate", admin, req, tenantHdr(tenant))
	assertComparison(t, r.body, "comparable")
	req["seed"] = different
	r = h.do("POST", "/v1/m/evals/gate", admin, req, tenantHdr(tenant))
	assertComparison(t, r.body, "incompatible")
	req["seed"] = "one"
	req["outputs"] = map[string]string{}
	r = h.do("POST", "/v1/m/evals/gate", admin, req, tenantHdr(tenant))
	assertComparison(t, r.body, "incomplete")
	if r.body["verdict"] != "fail" {
		t.Fatalf("unscored selected set passed: %s", r.raw)
	}
}

func TestGateFirstRunAndDisabledComparisonContract(t *testing.T) {
	h := newHarness(t, nil)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "disabled-comparison")
	suite := h.createSuite(admin, tenant, map[string]any{"name": "disabled", "subject_kind": "model", "scorer": "exact", "pass_threshold": 0.5})
	h.addCase(admin, tenant, suite, map[string]any{"case_key": "one", "input": "question", "expected": "answer"})
	req := map[string]any{"suite_ref": suite, "subject_ref": "model-a", "outputs": map[string]string{"one": "answer"}}
	first := h.do("POST", "/v1/m/evals/gate", admin, req, tenantHdr(tenant))
	assertComparison(t, first.body, "disabled")
	if first.body["verdict"] != "pass" {
		t.Fatal(first.raw)
	}
	req["baseline_ref"] = "00000000-0000-4000-8000-000000000099"
	failed := h.do("POST", "/v1/m/evals/gate", admin, req, tenantHdr(tenant))
	if failed.body["verdict"] != "fail" || !hasReason(failed.body, "baseline_unavailable") {
		t.Fatal(failed.raw)
	}
	before := failed.body["comparison"].(map[string]any)
	overridden := h.do("POST", "/v1/m/evals/gate/"+failed.body["id"].(string)+"/override", admin, map[string]any{"reason": "accepted by release owner"}, tenantHdr(tenant))
	if overridden.code != http.StatusOK || overridden.body["effective_verdict"] != "pass" {
		t.Fatal(overridden.raw)
	}
	after := overridden.body["comparison"].(map[string]any)
	if before["status"] != after["status"] || before["reason"] != after["reason"] {
		t.Fatalf("override rewrote comparison: %s", overridden.raw)
	}
	empty := h.createSuite(admin, tenant, map[string]any{"name": "empty", "subject_kind": "model", "scorer": "exact"})
	r := h.do("POST", "/v1/m/evals/gate", admin, map[string]any{"suite_ref": empty, "subject_ref": "model-a", "outputs": map[string]string{}}, tenantHdr(tenant))
	assertComparison(t, r.body, "incomplete")
	if r.body["verdict"] != "fail" {
		t.Fatal(r.raw)
	}
}

func TestLegacyComparisonEvidenceUnknown(t *testing.T) {
	h := newHarness(t, nil)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "legacy-evidence")
	suite := comparisonFixture(t, h, admin, tenant, "legacy")
	var legacy string
	err := h.st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		repo, e := sc.Ext(runKind)
		if e != nil {
			return e
		}
		rec, e := repo.Create(context.Background(), model.Record{colSuiteRef: suite, colSuiteVer: int64(1), colSubjKind: "model", colSubjectRef: "model-a", colModelRef: "", colVariant: "", colScorer: "exact", colRunStatus: "completed", colTotal: int64(1), colPassed: int64(1), colFailed: int64(0), colErrors: int64(0), colSkipped: int64(0), colScore: 1.0, colPassRate: 1.0, colRegressed: false, colDrift: 0.0, colStartedAt: "2026-09-27T00:00:00.000000000Z", colFinishedAt: "2026-09-27T00:00:00.000000000Z", colLaunchedBy: "system:evals"})
		if e == nil {
			legacy = rec.String(model.ColID)
		}
		return e
	})
	if err != nil {
		t.Fatal(err)
	}
	old := h.do("GET", "/v1/m/evals/runs/"+legacy, admin, nil, tenantHdr(tenant))
	if old.code != http.StatusOK {
		t.Fatal(old.raw)
	}
	assertComparison(t, old.body, "unknown")
	req := map[string]any{"suite_ref": suite, "subject_ref": "model-a", "outputs": map[string]string{"one": "answer"}}
	r := h.do("POST", "/v1/m/evals/gate", admin, req, tenantHdr(tenant))
	assertComparison(t, r.body, "unknown")
	if r.body["verdict"] != "fail" {
		t.Fatal(r.raw)
	}
	r = h.do("POST", "/v1/m/evals/gate", admin, req, tenantHdr(tenant))
	assertComparison(t, r.body, "comparable")
}

type protocolFixtureJudge struct{ version, model, observed string }

func (j *protocolFixtureJudge) JudgingProtocol(string) (ScoringProtocol, bool) {
	return ScoringProtocol{Implementation: "fixture/judge", Version: j.version, Provider: "fixture", Model: j.model, ConfigDigest: hashHex("criterion-v1")}, j.model != ""
}
func (j *protocolFixtureJudge) Judge(context.Context, model.TenantID, JudgeRequest) (JudgeVerdict, error) {
	return JudgeVerdict{Score: 1, Passed: true, Reason: "fixture", ObservedModel: j.observed}, nil
}

func TestComparisonJudgeMetadataAndObservedMismatch(t *testing.T) {
	judge := &protocolFixtureJudge{version: "1", model: "judge-a", observed: "judge-a"}
	h := newHarness(t, judge)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "judge-protocol")
	suite := h.createSuite(admin, tenant, map[string]any{"name": "judge", "subject_kind": "model", "scorer": "llm_judge", "judge_model": "judge-a", "criterion": "correct", "regression_threshold": 0.2})
	h.addCase(admin, tenant, suite, map[string]any{"case_key": "one", "input": "question"})
	base := comparisonRun(t, h, admin, tenant, suite, "candidate-a")
	req := map[string]any{"suite_ref": suite, "subject_ref": "candidate-a", "baseline_ref": base.body["id"], "outputs": map[string]string{"one": "answer"}}
	// The uncalibrated gate still fails its independent trust rule, while the
	// comparison receipt itself is comparable; neither claim substitutes for the other.
	first := h.do("POST", "/v1/m/evals/gate", admin, req, tenantHdr(tenant))
	assertComparison(t, first.body, "comparable")
	cached := h.do("POST", "/v1/m/evals/gate", admin, req, tenantHdr(tenant))
	assertComparison(t, cached.body, "comparable")
	if intOf(cached.body["cache_hits"]) != 1 {
		t.Fatal("cache control did not hit")
	}
	judge.version = "2"
	changed := h.do("POST", "/v1/m/evals/gate", admin, req, tenantHdr(tenant))
	assertComparison(t, changed.body, "unknown")
	if changed.body["verdict"] != "fail" {
		t.Fatal(changed.raw)
	}
	judge.version = "1"
	judge.observed = "judge-b"
	req["outputs"] = map[string]string{"one": "different output avoids cache"}
	mismatch := h.do("POST", "/v1/m/evals/gate", admin, req, tenantHdr(tenant))
	assertComparison(t, mismatch.body, "incompatible")
	if !hasReason(mismatch.body, "judge_model_mismatch") {
		t.Fatalf("response contradiction not reported: %s", mismatch.raw)
	}
	evidence := mismatch.body["comparison"].(map[string]any)
	if evidence["observed_models"].([]any)[0] != "judge-b" {
		t.Fatal("contradictory model discarded")
	}
	// Reusing the cached contradictory verdict must preserve the contradiction.
	again := h.do("POST", "/v1/m/evals/gate", admin, req, tenantHdr(tenant))
	if !hasReason(again.body, "judge_model_mismatch") {
		t.Fatal(again.raw)
	}
	judge.model, judge.observed = "judge-b", "judge-b"
	req["outputs"] = map[string]string{"one": "new model output"}
	changedModel := h.do("POST", "/v1/m/evals/gate", admin, req, tenantHdr(tenant))
	assertComparison(t, changedModel.body, "incompatible")
	judge.model, judge.observed = "judge-a", "judge-a"
	err := h.st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		repo, e := sc.Ext(suiteKind)
		if e != nil {
			return e
		}
		rec, e := repo.Get(context.Background(), model.ID(suite))
		if e != nil {
			return e
		}
		rec[colCriterion] = "changed rubric"
		_, e = repo.Update(context.Background(), rec)
		return e
	})
	if err != nil {
		t.Fatal(err)
	}
	req["outputs"] = map[string]string{"one": "new rubric output"}
	changedRubric := h.do("POST", "/v1/m/evals/gate", admin, req, tenantHdr(tenant))
	assertComparison(t, changedRubric.body, "incompatible")
	judge.model = ""
	req["outputs"] = map[string]string{"one": "new output"}
	unknown := h.do("POST", "/v1/m/evals/gate", admin, req, tenantHdr(tenant))
	assertComparison(t, unknown.body, "unknown")
}

func TestComparisonLegacyCacheIsUnknown(t *testing.T) {
	h := newHarness(t, fakeJudge{})
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "legacy-cache")
	suite := h.createSuite(admin, tenant, map[string]any{"name": "legacy-cache", "subject_kind": "model", "scorer": "llm_judge", "judge_model": "judge-a", "criterion": "correct", "regression_threshold": 0.2})
	h.addCase(admin, tenant, suite, map[string]any{"case_key": "one", "input": "question"})
	err := h.st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		repo, e := sc.Ext(cacheKind)
		if e != nil {
			return e
		}
		key := newCachedJudge(fakeJudge{}, suiteDTO{JudgeModel: "judge-a", Criterion: "correct"}, judgeCacheVersion).cacheKey("question", "correct", "", "correct")
		_, e = repo.Create(context.Background(), model.Record{colInputHash: key, colJudgeModel: "judge-a", colResScore: 1.0, colPassedFlag: true, colReason: "legacy", colOccurredAt: "2026-09-27T00:00:00.000000000Z"})
		return e
	})
	if err != nil {
		t.Fatal(err)
	}
	r := h.do("POST", "/v1/m/evals/gate", admin, map[string]any{"suite_ref": suite, "subject_ref": "candidate", "outputs": map[string]string{"one": "correct"}}, tenantHdr(tenant))
	assertComparison(t, r.body, "unknown")
	if intOf(r.body["cache_hits"]) != 1 || r.body["verdict"] != "fail" {
		t.Fatalf("legacy cache was not preserved/refused: %s", r.raw)
	}
}

func TestComparisonPartialScoringCannotPass(t *testing.T) {
	scorer := &declaredTestScorer{version: "1"}
	h := newHarness(t, nil, WithScorer(scorer))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "partial-comparison")
	suite := comparisonFixture(t, h, admin, tenant, "partial")
	h.addCase(admin, tenant, suite, map[string]any{"case_key": "two", "input": "fault", "expected": "answer"})
	req := map[string]any{"suite_ref": suite, "subject_ref": "model-a", "outputs": map[string]string{"one": "answer", "two": "answer"}}
	base := h.do("POST", "/v1/m/evals/runs", admin, req, tenantHdr(tenant))
	if base.code != http.StatusCreated {
		t.Fatal(base.raw)
	}
	req["baseline_ref"] = base.body["id"]
	control := h.do("POST", "/v1/m/evals/gate", admin, req, tenantHdr(tenant))
	assertComparison(t, control.body, "comparable")
	scorer.errorInput = "fault"
	partial := h.do("POST", "/v1/m/evals/gate", admin, req, tenantHdr(tenant))
	assertComparison(t, partial.body, "incomplete")
	evidence := partial.body["comparison"].(map[string]any)
	if partial.body["verdict"] != "fail" || evidence["complete"] != false || intOf(evidence["errors"]) != 1 {
		t.Fatal(partial.raw)
	}
}

func TestComparisonBudgetStopRetainsRefusal(t *testing.T) {
	counting := &countingJudge{inner: fakeJudge{}}
	h := newHarness(t, counting, WithBudgetGate(fakeBudget{allowed: false, action: "throttle"}))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "stopped-comparison")
	suite := h.createSuite(admin, tenant, map[string]any{"name": "stopped", "subject_kind": "model", "scorer": "llm_judge", "judge_model": "judge-a", "criterion": "correct"})
	h.addCase(admin, tenant, suite, map[string]any{"case_key": "one", "input": "question"})
	req := map[string]any{"suite_ref": suite, "subject_ref": "model-a", "outputs": map[string]string{"one": "correct"}}
	control := h.do("POST", "/v1/m/evals/gate", admin, req, tenantHdr(tenant))
	assertComparison(t, control.body, "incomplete")
	if control.body["verdict"] != "warn" || !hasReason(control.body, reasonBudgetThrottled) {
		t.Fatal(control.raw)
	}
	req["baseline_ref"] = "00000000-0000-4000-8000-000000000099"
	refused := h.do("POST", "/v1/m/evals/gate", admin, req, tenantHdr(tenant))
	assertComparison(t, refused.body, "unknown")
	if refused.body["verdict"] != "fail" || !hasReason(refused.body, "baseline_unavailable") || counting.calls != 0 {
		t.Fatal(refused.raw)
	}
	get := h.do("GET", "/v1/m/evals/gate/"+refused.body["id"].(string), admin, nil, tenantHdr(tenant))
	if get.body["verdict"] != "fail" || !reflect.DeepEqual(get.body["comparison"], refused.body["comparison"]) {
		t.Fatal(get.raw)
	}
}

// This supported same-ID override never invokes the module's Judge port.
type customLLMComparisonScorer struct {
	declaredTestScorer
	reasonOnlySkip bool
}

func (*customLLMComparisonScorer) ID() string { return scorerLLMJudge }
func (s *customLLMComparisonScorer) Score(ctx context.Context, in ScoreInput) ScoreResult {
	if s.reasonOnlySkip {
		return ScoreResult{Outcome: outcomeSkipped, Reason: "no judge wired — llm_judge skipped"}
	}
	return s.declaredTestScorer.Score(ctx, in)
}

func TestGateCustomLLMPartialCannotClaimAbsentJudge(t *testing.T) {
	for _, tc := range []struct {
		name           string
		reasonOnlySkip bool
	}{{"pass-and-error", false}, {"reason-is-not-authority", true}} {
		t.Run(tc.name, func(t *testing.T) {
			scorer := &customLLMComparisonScorer{declaredTestScorer: declaredTestScorer{version: "1", errorInput: "fault"}, reasonOnlySkip: tc.reasonOnlySkip}
			h := newHarness(t, nil, WithScorer(scorer))
			admin := h.adminLogin()
			tenant := h.createOrg(admin, "custom-llm-partial")
			suite := h.createSuite(admin, tenant, map[string]any{"name": "custom-llm", "subject_kind": "model", "scorer": scorerLLMJudge, "pass_threshold": 0.5, "regression_threshold": 0.2})
			h.addCase(admin, tenant, suite, map[string]any{"case_key": "one", "input": "question", "expected": "answer"})
			h.addCase(admin, tenant, suite, map[string]any{"case_key": "two", "input": "fault", "expected": "answer"})
			req := map[string]any{"suite_ref": suite, "subject_ref": "model-a", "outputs": map[string]string{"one": "answer", "two": "answer"}}
			r := h.do("POST", "/v1/m/evals/gate", admin, req, tenantHdr(tenant))
			if r.code != http.StatusCreated {
				t.Fatal(r.raw)
			}
			assertComparison(t, r.body, "incomplete")
			if r.body["verdict"] != verdictFail || r.body["effective_verdict"] != verdictFail || !hasReason(r.body, "selected_cases_incomplete") || hasReason(r.body, reasonNoJudge) {
				t.Fatalf("custom scorer acquired absent-judge exception: %s", r.raw)
			}
			evidence := r.body["comparison"].(map[string]any)
			if evidence["complete"] != false {
				t.Fatal("partial measurement became eligible")
			}
			if !tc.reasonOnlySkip && intOf(evidence["errors"]) != 1 {
				t.Fatal("fixture did not execute its error case")
			}
			get := h.do("GET", "/v1/m/evals/gate/"+r.body["id"].(string), admin, nil, tenantHdr(tenant))
			if get.code != http.StatusOK || get.body["effective_verdict"] != verdictFail || !reflect.DeepEqual(get.body["comparison"], r.body["comparison"]) {
				t.Fatalf("retrieved refusal changed: %s", get.raw)
			}
			// The override remains supported: a complete custom execution on a new
			// candidate is not mislabeled as an absent, unused module Judge.
			scorer.errorInput, scorer.reasonOnlySkip = "", false
			req["subject_ref"] = "model-b"
			control := h.do("POST", "/v1/m/evals/gate", admin, req, tenantHdr(tenant))
			assertComparison(t, control.body, "no_baseline")
			if control.body["verdict"] != verdictPass || hasReason(control.body, reasonNoJudge) {
				t.Fatalf("complete custom scoring was disabled: %s", control.raw)
			}
		})
	}
}

func TestGateBuiltinAbsentJudgeRetainsNarrowWarning(t *testing.T) {
	h := newHarness(t, nil)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "builtin-absent-judge")
	suite := h.createSuite(admin, tenant, map[string]any{"name": "offline", "subject_kind": "model", "scorer": scorerLLMJudge, "criterion": "correct"})
	h.addCase(admin, tenant, suite, map[string]any{"case_key": "one", "input": "question"})
	req := map[string]any{"suite_ref": suite, "subject_ref": "model-a", "outputs": map[string]string{"one": "answer"}}
	r := h.do("POST", "/v1/m/evals/gate", admin, req, tenantHdr(tenant))
	assertComparison(t, r.body, "incomplete")
	if r.body["verdict"] != verdictWarn || !hasReason(r.body, reasonNoJudge) || r.body["comparison"].(map[string]any)["complete"] != false {
		t.Fatal(r.raw)
	}
	get := h.do("GET", "/v1/m/evals/gate/"+r.body["id"].(string), admin, nil, tenantHdr(tenant))
	if get.body["effective_verdict"] != verdictWarn || !reflect.DeepEqual(get.body["comparison"], r.body["comparison"]) {
		t.Fatal(get.raw)
	}
	req["baseline_ref"] = r.body["run_ref"]
	requested := h.do("POST", "/v1/m/evals/gate", admin, req, tenantHdr(tenant))
	if requested.body["verdict"] != verdictFail {
		t.Fatalf("requested incomplete baseline received warning exception: %s", requested.raw)
	}
}
