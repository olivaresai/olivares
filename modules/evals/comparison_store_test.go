// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package evals

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

type comparisonFixedClock struct{}

func (comparisonFixedClock) Now() model.Timestamp {
	return model.NewTimestamp(time.Date(2026, 9, 27, 0, 0, 0, 0, time.UTC))
}

type comparisonStoreProbe struct {
	kind      model.Kind
	operation string
	queries   []model.Query
	watch     bool
}
type comparisonTestStore struct {
	store.Store
	probe *comparisonStoreProbe
}

func (s comparisonTestStore) View(ctx context.Context, tenant model.TenantID, fn func(store.Scope) error) error {
	return s.Store.View(ctx, tenant, func(sc store.Scope) error { return fn(comparisonTestScope{Scope: sc, probe: s.probe}) })
}
func (s comparisonTestStore) Mutate(ctx context.Context, tenant model.TenantID, fn func(store.Scope) error) error {
	return s.Store.Mutate(ctx, tenant, func(sc store.Scope) error { return fn(comparisonTestScope{Scope: sc, probe: s.probe}) })
}

type comparisonTestScope struct {
	store.Scope
	probe *comparisonStoreProbe
}

func (s comparisonTestScope) Ext(kind model.Kind) (store.GenericRepo, error) {
	r, e := s.Scope.Ext(kind)
	if e != nil {
		return nil, e
	}
	return comparisonTestRepo{GenericRepo: r, kind: kind, probe: s.probe}, nil
}

type comparisonTestRepo struct {
	store.GenericRepo
	kind  model.Kind
	probe *comparisonStoreProbe
}

var errComparisonInjected = errors.New("comparison store fixture failure")

func (r comparisonTestRepo) Get(ctx context.Context, id model.ID) (model.Record, error) {
	if r.kind == r.probe.kind && r.probe.operation == "get" {
		return nil, errComparisonInjected
	}
	return r.GenericRepo.Get(ctx, id)
}
func (r comparisonTestRepo) List(ctx context.Context, q model.Query) ([]model.Record, model.Page, error) {
	if r.probe.watch && (r.kind == runKind || r.kind == baseKind || r.kind == comparisonKind) {
		r.probe.queries = append(r.probe.queries, q)
	}
	if r.kind == r.probe.kind && r.probe.operation == "list" {
		return nil, model.Page{}, errComparisonInjected
	}
	return r.GenericRepo.List(ctx, q)
}
func (r comparisonTestRepo) Create(ctx context.Context, rec model.Record) (model.Record, error) {
	if r.kind == r.probe.kind && r.probe.operation == "create" {
		return nil, errComparisonInjected
	}
	return r.GenericRepo.Create(ctx, rec)
}
func comparisonProbeDecorator(p *comparisonStoreProbe) func(store.Store) store.Store {
	return func(st store.Store) store.Store { return comparisonTestStore{Store: st, probe: p} }
}

func TestBaselineSelectionBoundedStableSnapshot(t *testing.T) {
	p := &comparisonStoreProbe{}
	scorer := &declaredTestScorer{version: "1"}
	h := newComparisonHarness(t, nil, comparisonProbeDecorator(p), WithScorer(scorer), WithClock(comparisonFixedClock{}))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "frozen-selection")
	suite := comparisonFixture(t, h, admin, tenant, "bounded")
	for i := 0; i < 16; i++ {
		comparisonRun(t, h, admin, tenant, suite, "model-a")
	}
	old := comparisonRun(t, h, admin, tenant, suite, "model-a")
	var newer resp
	// A complete later run commits while the outer scorer is executing. Because
	// prepare ran in View and released it, this neither deadlocks nor changes the
	// outer selection. The next request must observe the later receipt.
	scorer.during = func() { newer = comparisonRun(t, h, admin, tenant, suite, "model-a") }
	p.watch = true
	r := h.do("POST", "/v1/m/evals/gate", admin, map[string]any{"suite_ref": suite, "subject_ref": "model-a", "outputs": map[string]string{"one": "answer"}}, tenantHdr(tenant))
	p.watch = false
	assertComparison(t, r.body, "comparable")
	if r.body["baseline_ref"] != old.body["id"] {
		t.Fatalf("late run changed frozen baseline: %s", r.raw)
	}
	if newer.code != http.StatusCreated {
		t.Fatal("late control was not executed")
	}
	if len(p.queries) == 0 {
		t.Fatal("no captured selection queries")
	}
	for _, q := range p.queries {
		if q.Limit != 1 {
			t.Fatalf("unbounded historical query: %#v", q)
		}
		if len(q.Sort) > 0 && (len(q.Sort) != 2 || q.Sort[0].Column != colFinishedAt || q.Sort[1].Column != model.ColID) {
			t.Fatalf("unstable ordering: %#v", q.Sort)
		}
	}
	next := h.do("POST", "/v1/m/evals/gate", admin, map[string]any{"suite_ref": suite, "subject_ref": "model-a", "outputs": map[string]string{"one": "answer"}}, tenantHdr(tenant))
	if next.body["baseline_ref"] != r.body["run_ref"] {
		t.Fatalf("next invocation did not observe latest completion: %s", next.raw)
	}
	pin := func(ref any) {
		t.Helper()
		response := h.do("POST", "/v1/m/evals/baselines", admin, map[string]any{"suite_ref": suite, "subject_ref": "model-a", "run_ref": ref}, tenantHdr(tenant))
		if response.code != http.StatusCreated {
			t.Fatal(response.raw)
		}
	}
	pin(old.body["id"])
	scorer.during = func() { pin(newer.body["id"]) }
	frozen := h.do("POST", "/v1/m/evals/gate", admin, map[string]any{"suite_ref": suite, "subject_ref": "model-a", "outputs": map[string]string{"one": "answer"}}, tenantHdr(tenant))
	if frozen.body["baseline_ref"] != old.body["id"] {
		t.Fatalf("late repin changed captured receipt: %s", frozen.raw)
	}
	repinned := h.do("POST", "/v1/m/evals/gate", admin, map[string]any{"suite_ref": suite, "subject_ref": "model-a", "outputs": map[string]string{"one": "answer"}}, tenantHdr(tenant))
	if repinned.body["baseline_ref"] != newer.body["id"] {
		t.Fatalf("next request ignored committed repin: %s", repinned.raw)
	}
}

func TestComparisonStorageFailureRollsBack(t *testing.T) {
	p := &comparisonStoreProbe{}
	h := newComparisonHarness(t, nil, comparisonProbeDecorator(p))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "comparison-rollback")
	suite := comparisonFixture(t, h, admin, tenant, "rollback")
	base := comparisonRun(t, h, admin, tenant, suite, "model-a")
	req := map[string]any{"suite_ref": suite, "subject_ref": "model-a", "baseline_ref": base.body["id"], "outputs": map[string]string{"one": "answer"}}
	counts := func() map[model.Kind]int {
		t.Helper()
		out := map[model.Kind]int{}
		err := h.st.View(context.Background(), tenant, func(sc store.Scope) error {
			for _, kind := range []model.Kind{runKind, resultKind, comparisonKind, gateKind} {
				repo, e := sc.Ext(kind)
				if e != nil {
					return e
				}
				rows, e := listAll(context.Background(), repo)
				if e != nil {
					return e
				}
				out[kind] = len(rows)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	before := counts()
	for _, fault := range []struct {
		kind model.Kind
		op   string
	}{{runKind, "get"}, {baseKind, "list"}, {comparisonKind, "list"}, {comparisonKind, "create"}, {gateKind, "create"}} {
		p.kind, p.operation = fault.kind, fault.op
		if fault.kind == baseKind {
			delete(req, "baseline_ref")
		} else {
			req["baseline_ref"] = base.body["id"]
		}
		r := h.do("POST", "/v1/m/evals/gate", admin, req, tenantHdr(tenant))
		p.operation = ""
		if r.code < 500 {
			t.Fatalf("storage failure became a verdict: %d %s", r.code, r.raw)
		}
		for kind, n := range counts() {
			if n != before[kind] {
				t.Fatalf("partial %s rows after %s/%s: %d vs %d", kind, fault.kind, fault.op, n, before[kind])
			}
		}
		if len(h.coreEvalResults(tenant, "rollback")) != 1 {
			t.Fatal("core result escaped rollback")
		}
	}
	module := New()
	module.UseData(api.NewModuleData(h.st))
	p.kind, p.operation = comparisonKind, "create"
	_, err := module.ScoreOutputs(context.Background(), tenant, ScoreOutputsRequest{SuiteRef: suite, SubjectKind: "model", SubjectRef: "model-a", Outputs: map[string]string{"one": "answer"}})
	p.operation = ""
	if !errors.Is(err, errComparisonInjected) {
		t.Fatalf("ScoreOutputs lost storage fault: %v", err)
	}
	for kind, n := range counts() {
		if n != before[kind] {
			t.Fatalf("ScoreOutputs partial %s", kind)
		}
	}
	control := h.do("POST", "/v1/m/evals/gate", admin, req, tenantHdr(tenant))
	assertComparison(t, control.body, "comparable")
}

func TestComparisonScoreOutputsAndABShareEvidence(t *testing.T) {
	h := newHarness(t, nil)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "shared-comparison")
	suite := comparisonFixture(t, h, admin, tenant, "shared")
	module := New()
	module.UseData(api.NewModuleData(h.st))
	req := ScoreOutputsRequest{SuiteRef: suite, SubjectKind: "model", SubjectRef: "model-a", Outputs: map[string]string{"one": "answer"}}
	first, err := module.ScoreOutputs(context.Background(), tenant, req)
	if err != nil {
		t.Fatal(err)
	}
	second, err := module.ScoreOutputs(context.Background(), tenant, req)
	if err != nil {
		t.Fatal(err)
	}
	if second.Comparison.Status != ComparisonComparable || second.Comparison.BaselineRef != first.RunRef {
		t.Fatalf("ScoreOutputs comparison: %+v", second.Comparison)
	}
	r := h.do("POST", "/v1/m/evals/ab", admin, map[string]any{"suite_ref": suite, "subject_ref": "model-a", "a": map[string]any{"label": "A", "outputs": map[string]string{"one": "answer"}}, "b": map[string]any{"label": "B", "outputs": map[string]string{"one": "wrong"}}}, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatal(r.raw)
	}
	for _, variant := range r.body["variants"].([]any) {
		v := variant.(map[string]any)
		if v["comparison"] == nil {
			t.Fatal("AB omitted comparison")
		}
		get := h.do("GET", "/v1/m/evals/runs/"+v["run_ref"].(string), admin, nil, tenantHdr(tenant))
		assertComparison(t, get.body, "no_baseline")
	}
}
