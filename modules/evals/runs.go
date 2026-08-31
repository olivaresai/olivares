// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package evals

import (
	"context"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

// This file owns the RUN executor — the core reusable scoring path shared by POST
// /runs, POST /ab and ScoreOutputs (the seam XVII calls). A run scores caller-
// supplied outputs against a suite's cases, aggregates a score + pass_rate, persists
// append-only per-case evidence + a mutable run aggregate + ONE core EvalResult, and
// — if it regressed vs a baseline — a core Finding plus a best-effort bus signal.
//
// The minimal-data invariant (docs/SECURITY-HARDENING.md): the raw candidate output is NEVER
// persisted; a case result stores only hashHex(output|expected|reason) + a clamped
// label. A case with no supplied output is SKIPPED (excluded from the denominator,
// exactly like redteam).

// findingKindRegression is the persisted Finding.Kind for an eval regression (ARCHITECTURE.md
// §3). busEvalRegression is the FindingReport.Kind routing key deliver.
const (
	findingKindRegression = "eval_regression"
	busEvalRegression     = "eval_regression"
)

// caseResult is one scored case within a run (the in-memory aggregate before
// persistence). detailHash is the one-way hash of output|expected|reason; label is a
// short clamped+scrubbed UI hint — never the raw output.
type caseResult struct {
	caseKey    string
	scorer     string
	outcome    string
	score      float64
	passed     bool
	detailHash string
	label      string
}

// runAggregate is the computed outcome of a run before it is persisted.
type runAggregate struct {
	total, passed, failed, errors, skipped int
	score, passRate                        float64
	status                                 string
	cases                                  []caseResult
}

// executeRun is the CORE reusable scoring function. Given a suite, its cases, a map
// of case_key→candidate output, the resolved scorer and the run options, it scores
// each case and returns the aggregate. It performs NO I/O beyond the scorer (the
// llm_judge scorer may call the Judge port); persistence is the caller's job so the
// same function backs the handlers and ScoreOutputs.
func (m *Module) executeRun(ctx context.Context, tenant model.TenantID, suite suiteDTO, cases []caseDTO, outputs map[string]string, scorer Scorer) runAggregate {
	agg := runAggregate{cases: make([]caseResult, 0, len(cases)), status: "completed"}
	var scoreSum float64
	for _, c := range cases {
		agg.total++
		output, present := outputs[c.CaseKey]
		if !present {
			// No candidate output for this case: SKIPPED, excluded from the denominator
			// (never counted as a pass) — the redteam rule.
			agg.skipped++
			agg.cases = append(agg.cases, caseResult{
				caseKey: c.CaseKey, scorer: scorer.ID(), outcome: outcomeSkipped, score: 0, passed: false,
				detailHash: hashHex("|" + c.Expected + "|no output supplied"), label: "no output supplied",
			})
			continue
		}
		res := scorer.Score(ctx, ScoreInput{
			Tenant: tenant, Input: c.Input, Output: output, Expected: c.Expected, Criterion: suite.Criterion,
			ModelRef: suite.JudgeModel, Config: c.Metadata,
		})
		cr := caseResult{
			caseKey: c.CaseKey, scorer: scorer.ID(), outcome: res.Outcome, score: res.Score, passed: res.Passed,
			detailHash: hashHex(output + "|" + c.Expected + "|" + res.Reason), label: clamp(scrub(res.Reason), maxLabelLen),
		}
		switch res.Outcome {
		case outcomePass:
			agg.passed++
			scoreSum += res.Score
		case outcomeFail:
			agg.failed++
			scoreSum += res.Score
		case outcomeSkipped:
			agg.skipped++
		default: // error
			agg.errors++
		}
		agg.cases = append(agg.cases, cr)
	}
	if scored := agg.passed + agg.failed; scored > 0 {
		agg.score = scoreSum / float64(scored)
		agg.passRate = float64(agg.passed) / float64(scored)
	}
	agg.status = runStatus(agg)
	return agg
}

// runStatus derives the terminal status: error when every case errored, degraded when
// nothing was actually scored (all skipped, or a skipped+error mix), else completed.
func runStatus(agg runAggregate) string {
	switch {
	case agg.total == 0:
		return "completed"
	case agg.errors == agg.total:
		return "error"
	case agg.passed+agg.failed == 0:
		return "degraded"
	default:
		return "completed"
	}
}

// runSubject identifies what a run evaluated.
type runSubject struct {
	suiteRef    string
	suiteVer    int64
	subjectKind string
	subjectRef  string
	modelRef    string
	variant     string
	baselineRef string
	launchedBy  string
}

// persistRun writes one run's evidence atomically inside an open Mutate: the per-case
// results (append-only), the mutable run aggregate (created already-terminal), the
// canonical core EvalResult, and — on a detected regression — a core Finding. It
// returns the persisted run DTO and the regression info for the best-effort bus emit.
func (m *Module) persistRun(ctx context.Context, sc store.Scope, suite suiteDTO, subj runSubject, agg runAggregate) (runDTO, regressionInfo, error) {
	now := m.clock.Now()

	// Resolve the baseline and compute drift BEFORE writing the run, so the run row
	// records its own baseline_ref/regressed/drift.
	reg, err := m.resolveRegression(ctx, sc, suite, subj, agg)
	if err != nil {
		return runDTO{}, regressionInfo{}, err
	}

	runRepo, err := sc.Ext(runKind)
	if err != nil {
		return runDTO{}, regressionInfo{}, err
	}
	rec := model.Record{
		colSuiteRef: subj.suiteRef, colSuiteVer: subj.suiteVer, colSubjKind: subj.subjectKind,
		colSubjectRef: clamp(subj.subjectRef, maxRefLen), colModelRef: clamp(subj.modelRef, maxRefLen),
		colVariant: clamp(subj.variant, maxNameLen), colScorer: suite.Scorer, colRunStatus: agg.status,
		colTotal: int64(agg.total), colPassed: int64(agg.passed), colFailed: int64(agg.failed),
		colErrors: int64(agg.errors), colSkipped: int64(agg.skipped), colScore: agg.score, colPassRate: agg.passRate,
		colRegressed: reg.regressed, colDrift: reg.drift,
		colStartedAt: now.String(), colFinishedAt: now.String(), colLaunchedBy: subj.launchedBy,
	}
	if reg.baselineRef != "" {
		rec[colBaselineRef] = reg.baselineRef
	}
	runRec, err := runRepo.Create(ctx, rec)
	if err != nil {
		return runDTO{}, regressionInfo{}, err
	}
	runID := model.ID(runRec.String(model.ColID))

	resRepo, err := sc.Ext(resultKind)
	if err != nil {
		return runDTO{}, regressionInfo{}, err
	}
	for _, cr := range agg.cases {
		if _, err := resRepo.Create(ctx, model.Record{
			colRunRef: runID.String(), colCaseKey: cr.caseKey, colResScorer: cr.scorer, colOutcome: cr.outcome,
			colResScore: cr.score, colPassedFlag: cr.passed, colDetailHash: cr.detailHash, colLabel: cr.label,
			colOccurredAt: now.String(),
		}); err != nil {
			return runDTO{}, regressionInfo{}, err
		}
	}

	// The canonical cross-module artifact: ONE core EvalResult (sc.Evals().Create).
	passed := agg.passRate >= suite.PassThreshold && (agg.passed+agg.failed) > 0
	if _, err := sc.Evals().Create(ctx, model.EvalResult{
		Suite: suite.Name, SubjectKind: subj.subjectKind, SubjectID: parseIDOrZero(subj.subjectRef),
		Score: agg.score, Passed: passed, OccurredAt: now,
		Metrics: map[string]any{
			"total": agg.total, "passed": agg.passed, "failed": agg.failed, "errors": agg.errors,
			"skipped": agg.skipped, "pass_rate": agg.passRate, "status": agg.status,
		},
		Metadata: map[string]any{
			"run_ref": runID.String(), "subject_ref": clamp(subj.subjectRef, maxRefLen),
			"suite_version": subj.suiteVer, "variant": clamp(subj.variant, maxNameLen),
		},
	}); err != nil {
		return runDTO{}, regressionInfo{}, err
	}

	reg.runRef = runID.String()
	reg.subjectRef = subj.subjectRef
	if reg.regressed {
		sev := regressionSeverity(reg.drift)
		detail := suite.Name + "|" + subj.subjectRef + "|drift=" + strconv.FormatFloat(reg.drift, 'f', 4, 64)
		if _, err := sc.Findings().Create(ctx, model.Finding{
			Kind: findingKindRegression, Severity: sevToCore(sev), Status: model.FindingOpen,
			Source: suite.Name, SubjectKind: subj.subjectKind, SubjectID: parseIDOrZero(subj.subjectRef),
			Title:      clamp("Eval regression: "+suite.Name, maxNameLen),
			DetailHash: hashBytes(detail), OccurredAt: now,
			Metadata: map[string]any{
				"subject_ref": clamp(subj.subjectRef, maxRefLen), "run_ref": runID.String(),
				"baseline_ref": reg.baselineRef, "drift": reg.drift,
			},
		}); err != nil {
			return runDTO{}, regressionInfo{}, err
		}
		reg.severity = sev
	}

	out := toRunDTO(runRec)
	out.Cases = caseDTOsOf(agg.cases)
	out.ScoreCI = scoreCIOf(agg.cases) // per-case scores are in hand here
	return out, reg, nil
}

// scrub removes obvious inline credentials/markers from a reason before it is stored
// as a UI label (defense in depth — a scorer reason is already meant to be
// non-sensitive, docs/SECURITY-HARDENING.md).
func scrub(s string) string {
	for _, frag := range []string{"password", "secret", "token", "api_key", "apikey", "authorization", "bearer"} {
		if strings.Contains(strings.ToLower(s), frag) {
			return "[redacted]"
		}
	}
	return s
}

// ---- regression resolution -------------------------------------------------------

// regressionInfo is the resolved baseline comparison for a run.
type regressionInfo struct {
	baselineRef   string
	baselineScore float64
	drift         float64 // baseline.score - run.score
	regressed     bool
	severity      sdkmodel.Severity
	runRef        string
	subjectRef    string
}

// resolveRegression resolves the baseline (explicit ref → pinned baseline → latest
// prior completed run for the same suite+subject[+variant]) and reports whether the
// run regressed beyond the suite's regression_threshold. A degraded/all-skipped run
// is never flagged (it was not actually scored).
func (m *Module) resolveRegression(ctx context.Context, sc store.Scope, suite suiteDTO, subj runSubject, agg runAggregate) (regressionInfo, error) {
	if agg.passed+agg.failed == 0 || suite.RegThreshold <= 0 {
		return regressionInfo{baselineRef: subj.baselineRef}, nil
	}
	baselineRef, baselineScore, found, err := m.baselineScore(ctx, sc, suite.ID, subj)
	if err != nil {
		return regressionInfo{}, err
	}
	if !found {
		return regressionInfo{baselineRef: baselineRef}, nil
	}
	drift := baselineScore - agg.score
	return regressionInfo{
		baselineRef: baselineRef, baselineScore: baselineScore, drift: drift,
		regressed: drift > suite.RegThreshold,
	}, nil
}

// baselineScore resolves the baseline run's score for a subject. Precedence: an
// explicit baseline_ref on the run → a pinned evals_baseline → the latest prior
// completed evals_run for the same (suite, subject_ref[, variant]).
func (m *Module) baselineScore(ctx context.Context, sc store.Scope, suiteID string, subj runSubject) (ref string, score float64, found bool, err error) {
	runRepo, err := sc.Ext(runKind)
	if err != nil {
		return "", 0, false, err
	}
	// 1) Explicit baseline_ref on the run.
	if subj.baselineRef != "" {
		if id, ok := idParam(subj.baselineRef); ok {
			rec, gerr := runRepo.Get(ctx, id)
			if gerr != nil {
				if isNotFound(gerr) {
					return subj.baselineRef, 0, false, nil
				}
				return "", 0, false, gerr
			}
			return rec.String(model.ColID), rec.Float(colScore), true, nil
		}
	}
	// 2) A pinned baseline for (suite, subject).
	baseRepo, err := sc.Ext(baseKind)
	if err != nil {
		return "", 0, false, err
	}
	pins, err := listAll(ctx, baseRepo, eq(colSuiteRef, suiteID), eq(colSubjectRef, subj.subjectRef))
	if err != nil {
		return "", 0, false, err
	}
	if len(pins) > 0 {
		pinnedRun := pins[0].String(colBaseRunRef)
		if id, ok := idParam(pinnedRun); ok {
			rec, gerr := runRepo.Get(ctx, id)
			if gerr == nil {
				return rec.String(model.ColID), rec.Float(colScore), true, nil
			}
			if !isNotFound(gerr) {
				return "", 0, false, gerr
			}
		}
	}
	// 3) The latest prior completed run for (suite, subject[, variant]).
	filters := []model.Filter{eq(colSuiteRef, suiteID), eq(colSubjectRef, subj.subjectRef), eq(colRunStatus, "completed")}
	if subj.variant != "" {
		filters = append(filters, eq(colVariant, subj.variant))
	}
	prior, err := listAll(ctx, runRepo, filters...)
	if err != nil {
		return "", 0, false, err
	}
	var latest model.Record
	for _, rec := range prior {
		if latest == nil || rec.String(colStartedAt) > latest.String(colStartedAt) {
			latest = rec
		}
	}
	if latest == nil {
		return "", 0, false, nil
	}
	return latest.String(model.ColID), latest.Float(colScore), true, nil
}

// regressionSeverity grades a regression by the magnitude of the drop.
func regressionSeverity(drift float64) sdkmodel.Severity {
	switch {
	case drift >= 0.5:
		return sdkmodel.SeverityCritical
	case drift >= 0.25:
		return sdkmodel.SeverityHigh
	case drift >= 0.1:
		return sdkmodel.SeverityMedium
	default:
		return sdkmodel.SeverityLow
	}
}

// emitRegression publishes a minimal-data FindingReport for a detected regression on
// the bus (best-effort; a publish failure is logged, not fatal) so deliver it.
func (m *Module) emitRegression(ctx context.Context, tenant model.TenantID, suite suiteDTO, reg regressionInfo) {
	if m.host == nil || !reg.regressed {
		return
	}
	detail := suite.Name + "|" + reg.subjectRef + "|drift=" + strconv.FormatFloat(reg.drift, 'f', 4, 64)
	report := sdkmodel.FindingReport{
		Kind: busEvalRegression, Severity: reg.severity, SubjectKind: suite.SubjectKind,
		SubjectRef: clamp(reg.subjectRef, maxRefLen), Title: clamp("Eval regression: "+suite.Name, maxNameLen),
		DetailHash: hashHex(detail), OccurredAt: m.clock.Now().Time(),
	}
	if err := m.host.Publish(ctx, evalEvent(tenant, report)); err != nil {
		m.debugf("evals: publish regression finding failed", "err", err)
	}
}

// ---- run DTOs --------------------------------------------------------------------

// ciDTO is a 95% confidence interval (docs/EVAL-METHODOLOGY.md §4). It is COMPUTED
// ON READ from persisted counts/scores — never stored (module tables are frozen;
// derived statistics don't belong in the ledger anyway).
type ciDTO struct {
	Lo float64 `json:"lo"`
	Hi float64 `json:"hi"`
}

type runDTO struct {
	ID           string  `json:"id"`
	SuiteRef     string  `json:"suite_ref"`
	SuiteVersion int64   `json:"suite_version"`
	SubjectKind  string  `json:"subject_kind"`
	SubjectRef   string  `json:"subject_ref"`
	ModelRef     string  `json:"model_ref,omitempty"`
	Variant      string  `json:"prompt_variant,omitempty"`
	Scorer       string  `json:"scorer"`
	Status       string  `json:"status"`
	Total        int64   `json:"total"`
	Passed       int64   `json:"passed"`
	Failed       int64   `json:"failed"`
	Errors       int64   `json:"errors"`
	Skipped      int64   `json:"skipped"`
	Score        float64 `json:"score"`
	PassRate     float64 `json:"pass_rate"`
	// NScored is the denominator (passed+failed) behind score/pass_rate — reported
	// so a reader can weigh the aggregate (n=2 and n=200 are different claims).
	NScored int64 `json:"n_scored"`
	// PassRateCI is the 95% Wilson interval for pass_rate (nil when nothing was
	// scored). ScoreCI is the 95% t-interval for the mean score; it needs the
	// per-case scores, so it rides only the responses that load them (launch, get,
	// gate) and is nil for n<2 — absent, never fabricated.
	PassRateCI  *ciDTO         `json:"pass_rate_ci,omitempty"`
	ScoreCI     *ciDTO         `json:"score_ci,omitempty"`
	BaselineRef string         `json:"baseline_ref,omitempty"`
	Regressed   bool           `json:"regressed"`
	Drift       float64        `json:"drift"`
	StartedAt   string         `json:"started_at"`
	FinishedAt  string         `json:"finished_at,omitempty"`
	LaunchedBy  string         `json:"launched_by,omitempty"`
	Cases       []caseScoreDTO `json:"cases,omitempty"`
}

func toRunDTO(rec model.Record) runDTO {
	dto := runDTO{
		ID: rec.String(model.ColID), SuiteRef: rec.String(colSuiteRef), SuiteVersion: rec.Int(colSuiteVer),
		SubjectKind: rec.String(colSubjKind), SubjectRef: rec.String(colSubjectRef), ModelRef: rec.String(colModelRef),
		Variant: rec.String(colVariant), Scorer: rec.String(colScorer), Status: rec.String(colRunStatus),
		Total: rec.Int(colTotal), Passed: rec.Int(colPassed), Failed: rec.Int(colFailed), Errors: rec.Int(colErrors),
		Skipped: rec.Int(colSkipped), Score: rec.Float(colScore), PassRate: rec.Float(colPassRate),
		BaselineRef: rec.String(colBaselineRef), Regressed: rec.Bool(colRegressed), Drift: rec.Float(colDrift),
		StartedAt: rec.String(colStartedAt), FinishedAt: rec.String(colFinishedAt), LaunchedBy: rec.String(colLaunchedBy),
	}
	dto.NScored = dto.Passed + dto.Failed
	dto.PassRateCI = passRateCI(int(dto.Passed), int(dto.NScored))
	return dto
}

// passRateCI returns the 95% Wilson interval for passed/n, or nil when nothing was
// scored (no information — no interval).
func passRateCI(passed, n int) *ciDTO {
	if n <= 0 {
		return nil
	}
	lo, hi := wilsonInterval(passed, n)
	return &ciDTO{Lo: lo, Hi: hi}
}

// scoreCIOf returns the 95% t-interval for the mean of the SCORED case scores
// (pass/fail outcomes only — skipped/error cases carry no score), or nil when fewer
// than two cases were scored.
func scoreCIOf(cases []caseResult) *ciDTO {
	var scores []float64
	for _, c := range cases {
		if c.outcome == outcomePass || c.outcome == outcomeFail {
			scores = append(scores, c.score)
		}
	}
	lo, hi, ok := meanInterval(scores)
	if !ok {
		return nil
	}
	return &ciDTO{Lo: lo, Hi: hi}
}

// scoreCIOfRecords is scoreCIOf over persisted result rows (the on-read twin).
func scoreCIOfRecords(recs []model.Record) *ciDTO {
	var scores []float64
	for _, rec := range recs {
		if o := rec.String(colOutcome); o == outcomePass || o == outcomeFail {
			scores = append(scores, rec.Float(colResScore))
		}
	}
	lo, hi, ok := meanInterval(scores)
	if !ok {
		return nil
	}
	return &ciDTO{Lo: lo, Hi: hi}
}

type caseScoreDTO struct {
	CaseKey    string  `json:"case_key"`
	Scorer     string  `json:"scorer"`
	Outcome    string  `json:"outcome"`
	Score      float64 `json:"score"`
	Passed     bool    `json:"passed"`
	DetailHash string  `json:"detail_hash,omitempty"`
	Label      string  `json:"label,omitempty"`
}

func caseDTOsOf(cases []caseResult) []caseScoreDTO {
	out := make([]caseScoreDTO, 0, len(cases))
	for _, c := range cases {
		out = append(out, caseScoreDTO{
			CaseKey: c.caseKey, Scorer: c.scorer, Outcome: c.outcome, Score: c.score, Passed: c.passed,
			DetailHash: c.detailHash, Label: c.label,
		})
	}
	return out
}

// ---- launch handler --------------------------------------------------------------

type launchRunRequest struct {
	SuiteRef    string            `json:"suite_ref"`
	SubjectKind string            `json:"subject_kind,omitempty"`
	SubjectRef  string            `json:"subject_ref,omitempty"`
	ModelRef    string            `json:"model_ref,omitempty"`
	Variant     string            `json:"prompt_variant,omitempty"`
	BaselineRef string            `json:"baseline_ref,omitempty"`
	Outputs     map[string]string `json:"outputs"`
}

// handleLaunchRun scores a set of candidate outputs against a suite SYNCHRONOUSLY
// (the redteam pattern — no module goroutines) and returns 201 with the completed run
// + per-case. Write-tier + self-audited. The regression finding is emitted on the bus
// after the transaction commits (best-effort).
//
// Two-phase: the suite + cases load in a READ transaction, the scoring —
// which for llm_judge is network I/O to the judge model — runs OUTSIDE any
// transaction, and only the persistence opens the write transaction. Holding a
// write tx across model calls would block the whole store for the duration of a
// judged suite (SQLite serializes writers). A case appended between the phases is
// simply not part of this run — cases are append-only, a suite is never deleted.
func (m *Module) handleLaunchRun(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	var req launchRunRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	suiteID, ok := idParam(req.SuiteRef)
	if !ok {
		writeJSON(w, http.StatusBadRequest, errorBody("suite_ref is required"))
		return
	}
	if req.Outputs == nil {
		writeJSON(w, http.StatusBadRequest, errorBody("outputs is required (case_key → output)"))
		return
	}

	var suite suiteDTO
	var cases []caseDTO
	found := false
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		s, cs, ok, lerr := loadSuiteAndCases(r.Context(), sc, suiteID)
		suite, cases, found = s, cs, ok
		return lerr
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if !found {
		writeJSON(w, http.StatusNotFound, errorBody("suite not found"))
		return
	}

	subj := runSubject{
		suiteRef: suiteID.String(), suiteVer: suite.SuiteVersion,
		subjectKind: firstNonEmpty(req.SubjectKind, suite.SubjectKind), subjectRef: req.SubjectRef,
		modelRef: req.ModelRef, variant: req.Variant, baselineRef: req.BaselineRef, launchedBy: mc.Principal.Actor(),
	}
	agg := m.executeRun(r.Context(), mc.Tenant, suite, cases, clampOutputs(req.Outputs), m.scorerByID(suite.Scorer))

	var out runDTO
	var reg regressionInfo
	err = mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		dto, ri, perr := m.persistRun(r.Context(), sc, suite, subj, agg)
		if perr != nil {
			return perr
		}
		out, reg = dto, ri
		return auditEvent(r.Context(), sc, mc, "evals.run.launch", runKind, model.ID(dto.ID), map[string]any{
			"suite_ref": suiteID.String(), "subject_ref": clamp(req.SubjectRef, maxRefLen),
			"score": agg.score, "pass_rate": agg.passRate, "status": agg.status, "regressed": ri.regressed,
		})
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	m.emitRegression(r.Context(), mc.Tenant, suite, reg)
	writeJSON(w, http.StatusCreated, out)
}

// loadSuiteAndCases loads a suite + its cases. The bool is false when the suite is
// absent.
func loadSuiteAndCases(ctx context.Context, sc store.Scope, suiteID model.ID) (suiteDTO, []caseDTO, bool, error) {
	suiteRepo, err := sc.Ext(suiteKind)
	if err != nil {
		return suiteDTO{}, nil, false, err
	}
	rec, err := suiteRepo.Get(ctx, suiteID)
	if err != nil {
		if isNotFound(err) {
			return suiteDTO{}, nil, false, nil
		}
		return suiteDTO{}, nil, false, err
	}
	suite := toSuiteDTO(rec)
	caseRepo, err := sc.Ext(caseKind)
	if err != nil {
		return suiteDTO{}, nil, false, err
	}
	recs, err := listAll(ctx, caseRepo, eq(colSuiteRef, suiteID.String()))
	if err != nil {
		return suiteDTO{}, nil, false, err
	}
	cases := make([]caseDTO, 0, len(recs))
	for _, c := range recs {
		cases = append(cases, toCaseDTO(c))
	}
	sort.Slice(cases, func(i, j int) bool { return cases[i].CaseKey < cases[j].CaseKey })
	return suite, cases, true, nil
}

// clampOutputs bounds every candidate output before it is scored/hashed (the value is
// never persisted, but bounding caps the work and the hashed preimage).
func clampOutputs(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[clamp(k, maxNameLen)] = clamp(v, maxFixtureLen)
	}
	return out
}

func firstNonEmpty(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}

// ---- run + result reads ----------------------------------------------------------

// handleListRuns lists the tenant's runs (filterable by suite_ref/subject_ref).
func (m *Module) handleListRuns(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	q := listQuery(r)
	if v := strings.TrimSpace(r.URL.Query().Get("suite_ref")); v != "" {
		q.Filters = append(q.Filters, eq(colSuiteRef, v))
	}
	if v := strings.TrimSpace(r.URL.Query().Get("subject_ref")); v != "" {
		q.Filters = append(q.Filters, eq(colSubjectRef, v))
	}
	out := listResponse[runDTO]{Items: []runDTO{}}
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(runKind)
		if err != nil {
			return err
		}
		recs, page, err := repo.List(r.Context(), q)
		if err != nil {
			return err
		}
		for _, rec := range recs {
			out.Items = append(out.Items, toRunDTO(rec))
		}
		out.Cursor, out.HasMore = page.Cursor, page.HasMore
		return nil
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// handleGetRun returns one run with its per-case results attached.
func (m *Module) handleGetRun(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id, ok := idParam(chi.URLParam(r, "id"))
	if !ok {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid run_id"))
		return
	}
	var out runDTO
	found := false
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		runRepo, err := sc.Ext(runKind)
		if err != nil {
			return err
		}
		rec, err := runRepo.Get(r.Context(), id)
		if err != nil {
			if isNotFound(err) {
				return nil
			}
			return err
		}
		out = toRunDTO(rec)
		found = true
		resRepo, err := sc.Ext(resultKind)
		if err != nil {
			return err
		}
		recs, err := listAll(r.Context(), resRepo, eq(colRunRef, id.String()))
		if err != nil {
			return err
		}
		out.Cases = resultsToCaseScores(recs)
		out.ScoreCI = scoreCIOfRecords(recs) // per-case scores are loaded here
		return nil
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if !found {
		writeJSON(w, http.StatusNotFound, errorBody("not found"))
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// handleListResults lists one run's per-case results.
func (m *Module) handleListResults(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id, ok := idParam(chi.URLParam(r, "id"))
	if !ok {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid run_id"))
		return
	}
	out := listResponse[caseScoreDTO]{Items: []caseScoreDTO{}}
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(resultKind)
		if err != nil {
			return err
		}
		recs, err := listAll(r.Context(), repo, eq(colRunRef, id.String()))
		if err != nil {
			return err
		}
		out.Items = resultsToCaseScores(recs)
		return nil
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// resultsToCaseScores maps stored result rows to case-score DTOs, ordered by case_key.
func resultsToCaseScores(recs []model.Record) []caseScoreDTO {
	out := make([]caseScoreDTO, 0, len(recs))
	for _, rec := range recs {
		out = append(out, caseScoreDTO{
			CaseKey: rec.String(colCaseKey), Scorer: rec.String(colResScorer), Outcome: rec.String(colOutcome),
			Score: rec.Float(colResScore), Passed: rec.Bool(colPassedFlag), DetailHash: rec.String(colDetailHash),
			Label: rec.String(colLabel),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CaseKey < out[j].CaseKey })
	return out
}
