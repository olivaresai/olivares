// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sandbox

import (
	"context"
	"crypto/ed25519"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/audit"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/eventbus"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/runtime"
	"github.com/olivaresai/olivares/core/secure"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/evals"
	"github.com/olivaresai/olivares/sdk"
)

// This file proves the XII (evals) ↔ XVII (sandbox) integration end-to-end with the
// REAL evals scorers, not a fake. It is the ONLY place that imports both modules, and
// it is TEST-ONLY — exactly the convention the repo already uses for a cross-module
// test (e.g. capabilities's harness imports inventory). Production code never imports a
// sibling (contract §4): the bridge is this tiny adapter, which in production lives in
// the composition root (cmd/olivares/boot.go).

// evalsScorerAdapter implements the sandbox Scorer seam by delegating to the evals
// module's public ScoreOutputs method and translating the evals-owned Scorecard into
// the sandbox-owned ScoreVerdict. This is the composition-root adapter, exercised here.
type evalsScorerAdapter struct{ ev *evals.Module }

func (a evalsScorerAdapter) Score(ctx context.Context, tenant model.TenantID, req ScoreRequest) (ScoreVerdict, error) {
	card, err := a.ev.ScoreOutputs(ctx, tenant, evals.ScoreOutputsRequest{
		SuiteRef:    req.SuiteRef,
		SubjectKind: req.SubjectKind,
		SubjectRef:  req.SubjectRef,
		Variant:     req.Variant,
		Actor:       "module:olivares.sandbox",
		Outputs:     req.Outputs,
	})
	if err != nil {
		return ScoreVerdict{}, err
	}
	return ScoreVerdict{
		Score:    card.Score,
		PassRate: card.PassRate,
		// Passed = every scored case passed and at least one was actually scored. A
		// degraded/all-skipped scorecard is never a pass.
		Passed:  card.Status == "completed" && card.Failed == 0 && (card.Passed+card.Failed) > 0,
		Total:   card.Total,
		PassedN: card.Passed,
		FailedN: card.Failed,
	}, nil
}

// dualHarness wires evals + sandbox into ONE api.Server over one store, with the
// sandbox scored by the real evals module via the adapter. It reuses the sandbox
// harness's HTTP helpers (do/adminLogin/createOrg/roleToken) by returning *harness.
func newDualHarness(t *testing.T) (*harness, *evals.Module) {
	t.Helper()
	ctx := context.Background()
	h := &harness{t: t}

	ev := evals.New()
	sb := New(WithScorer(evalsScorerAdapter{ev: ev}))

	// One store, both modules' schemas registered through one register hook.
	register := func(reg store.ExtensionRegistry) error {
		if err := ev.RegisterSchema(reg); err != nil {
			return err
		}
		return sb.RegisterSchema(reg)
	}
	st, err := engine.Open(ctx, store.Config{Engine: store.EngineSQLite, DSN: ":memory:", Debug: true}, register)
	if err != nil {
		t.Fatal(err)
	}
	h.st = st
	t.Cleanup(func() { _ = st.Close() })
	if err := st.System(ctx, func(sys store.SystemScope) error { _, e := sys.EnsureSystemTenant(ctx); return e }); err != nil {
		t.Fatal(err)
	}
	ev.UseData(api.NewModuleData(st))
	sb.UseData(api.NewModuleData(st))

	bus := eventbus.NewInProc(eventbus.Options{})
	rt := runtime.New(runtime.Options{Bus: bus})
	if err := rt.AddModule(ev, sdk.Config{}); err != nil {
		t.Fatal(err)
	}
	if err := rt.AddModule(sb, sdk.Config{}); err != nil {
		t.Fatal(err)
	}
	if err := rt.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rt.Stop(ctx); _ = bus.Close() })

	_, priv, _ := ed25519.GenerateKey(nil)
	signer, _ := audit.NewSigner(priv)
	tok := secure.NewSetupToken(filepath.Join(t.TempDir(), "setup.token"))
	plaintext, _, err := tok.Ensure()
	if err != nil {
		t.Fatal(err)
	}
	srv, err := api.New(api.Options{
		Store: st, Authenticator: auth.NewAuthenticator(st, nil), Authorizer: auth.NewAuthorizer(nil),
		Signer: signer, SetupToken: tok, Version: "test",
		Modules: []api.Module{ev, sb},
	})
	if err != nil {
		t.Fatal(err)
	}
	h.srv, h.setupTok = srv, plaintext
	return h, ev
}

// TestEvalsScoreSandboxOutputs is the XII↔XVII contract: a sandbox scenario runs in the
// isolated runner, and its per-step outputs are scored by the REAL evals deterministic
// scorers against a golden suite — producing a scored sandbox run AND a recorded evals
// run + canonical core EvalResult. It then proves the scoring discriminates (a wrong
// output drops the score and the pass).
func TestEvalsScoreSandboxOutputs(t *testing.T) {
	h, _ := newDualHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	owner := h.roleToken(admin, tenant, "owner@acme.io", "owner")
	hdr := tenantHdr(tenant)

	// 1) A golden suite scored by the deterministic `exact` scorer (default
	// pass_threshold 1.0 ⇒ every scored case must match).
	rs := h.do("POST", "/v1/m/evals/suites", owner, map[string]any{
		"name": "greetings", "subject_kind": "sandbox_run", "scorer": "exact",
	}, hdr)
	if rs.code != http.StatusCreated {
		t.Fatalf("create suite = %d %s", rs.code, rs.raw)
	}
	suiteID := rs.body["id"].(string)
	for _, c := range []struct{ key, expected string }{{"c1", "hello"}, {"c2", "world"}} {
		if r := h.do("POST", "/v1/m/evals/suites/"+suiteID+"/cases", owner,
			map[string]any{"case_key": c.key, "input": "n/a", "expected": c.expected}, hdr); r.code != http.StatusCreated {
			t.Fatalf("add case %s = %d %s", c.key, r.code, r.raw)
		}
	}

	// 2) A scenario whose step KEYS match the case keys and whose mocked resources
	// return the expected outputs. The runner is isolated/deterministic.
	allCorrect := h.createScenario(owner, tenant, "greet-ok",
		[]map[string]any{{"key": "c1", "input": "r1"}, {"key": "c2", "input": "r2"}},
		[]map[string]any{{"resource": "r1", "response": "hello"}, {"resource": "r2", "response": "world"}})

	run := h.do("POST", "/v1/m/sandbox/scenarios/"+allCorrect+"/run", owner, map[string]any{"suite_ref": suiteID}, hdr)
	if run.code != http.StatusCreated {
		t.Fatalf("run scenario = %d %s", run.code, run.raw)
	}
	if run.body["runner"] != "inproc-mock" || run.body["isolated"] != true || run.body["destroyed"] != true {
		t.Fatalf("run not isolated/ephemeral: %s", run.raw)
	}
	if got := run.body["score"]; got != float64(1) {
		t.Fatalf("expected score 1.0 from real evals scoring, got %v (%s)", got, run.raw)
	}
	if run.body["passed"] != true {
		t.Fatalf("expected passed=true, got %s", run.raw)
	}
	if run.body["suite_ref"] != suiteID {
		t.Fatalf("expected suite_ref echoed, got %s", run.raw)
	}

	// 3) The evals side recorded a real run for this subject (the scenario), proving the
	// scoring flowed through XII — and the canonical core EvalResult was written.
	er := h.do("GET", "/v1/m/evals/runs?subject_ref="+allCorrect, owner, nil, hdr)
	if er.code != http.StatusOK {
		t.Fatalf("list evals runs = %d %s", er.code, er.raw)
	}
	items, _ := er.body["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("expected 1 evals run for the sandbox subject, got %d (%s)", len(items), er.raw)
	}
	evalsResults := h.coreEvalResults(tenant)
	if len(evalsResults) == 0 {
		t.Fatal("expected a canonical core EvalResult written by the scored sandbox run")
	}
	found := false
	for _, r := range evalsResults {
		if r.Suite == "greetings" && r.Passed {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected a passing core EvalResult for suite 'greetings', got %+v", evalsResults)
	}

	// 4) Discrimination: a scenario where one output is WRONG drops the score below the
	// threshold and fails — proving real scoring, not a rubber stamp.
	oneWrong := h.createScenario(owner, tenant, "greet-bad",
		[]map[string]any{{"key": "c1", "input": "r1"}, {"key": "c2", "input": "r2"}},
		[]map[string]any{{"resource": "r1", "response": "hello"}, {"resource": "r2", "response": "WRONG"}})
	bad := h.do("POST", "/v1/m/sandbox/scenarios/"+oneWrong+"/run", owner, map[string]any{"suite_ref": suiteID}, hdr)
	if bad.code != http.StatusCreated {
		t.Fatalf("run bad scenario = %d %s", bad.code, bad.raw)
	}
	if got := bad.body["score"]; got != float64(0.5) {
		t.Fatalf("expected score 0.5 (one of two correct), got %v (%s)", got, bad.raw)
	}
	if bad.body["passed"] != false {
		t.Fatalf("expected passed=false below threshold, got %s", bad.raw)
	}
}

// coreEvalResults reads the canonical core EvalResult rows of a tenant directly from the
// store (the cross-module artifact a scored sandbox run must produce).
func (h *harness) coreEvalResults(tenant model.TenantID) []model.EvalResult {
	h.t.Helper()
	var out []model.EvalResult
	if err := h.st.View(context.Background(), tenant, func(sc store.Scope) error {
		rs, _, err := sc.Evals().List(context.Background(), model.Query{Limit: 100})
		out = rs
		return err
	}); err != nil {
		h.t.Fatalf("read eval results: %v", err)
	}
	return out
}
