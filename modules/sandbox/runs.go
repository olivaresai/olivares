// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sandbox

import (
	"context"
	"net/http"
	"sort"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// runKindScenario / runKindReplay / runKindCompare are the three run kinds.
const (
	runKindScenario = "scenario"
	runKindReplay   = "replay"
	runKindCompare  = "compare"
)

// runResult holds an executed-but-not-yet-persisted run: the runner's outcome plus
// the derived aggregates and the (optional) score verdict. It is what executeSpec
// returns and what persistRun writes.
type runResult struct {
	runner      string
	isolated    bool
	destroyed   bool
	steps       []StepOutput
	stepsTotal  int
	stepsOK     int // steps that resolved against a mock
	stepsError  int // steps that did not (mock-miss)
	outputsHash string
	status      string // "completed" | "degraded" | "error"

	scored   bool
	score    float64
	passed   bool
	suiteRef string
}

// executeSpec runs a spec against the isolated runner and derives the aggregates. It
// does NOT touch the store and does NOT score — scoring is layered on by the caller
// (it may need the suite ref + the Scorer seam). A runner fault yields status=error
// with no outputs; a run with every step a mock-miss is still "completed" (a mock-miss
// is a deterministic, expected outcome, not an execution error).
func (m *Module) executeSpec(ctx context.Context, tenant model.TenantID, spec RunSpec) runResult {
	res := runResult{runner: m.runner.Name(), isolated: m.runner.Isolated(), status: "completed"}
	outcome, err := m.runner.Run(ctx, tenant, spec)
	if err != nil {
		m.debugf("sandbox: runner error", "err", err)
		res.status = "error"
		res.destroyed = outcome.Destroyed
		return res
	}
	res.destroyed = outcome.Destroyed
	res.steps = outcome.Steps
	res.stepsTotal = len(outcome.Steps)
	var sb strings.Builder
	for _, so := range outcome.Steps {
		if so.MockHit {
			res.stepsOK++
		} else {
			res.stepsError++
		}
		sb.WriteString(so.Key)
		sb.WriteByte('=')
		sb.WriteString(so.Output)
		sb.WriteByte('\n')
	}
	res.outputsHash = hashHex(sb.String())
	return res
}

// score asks the wired Scorer to score the outputs against suiteRef. With no scorer
// (the unscored default) it leaves the run executed-not-scored (status degraded for
// the scoring dimension) — NEVER a silent pass. A suiteRef of "" means the caller did
// not request scoring, so the run stays unscored and completed.
func (m *Module) score(ctx context.Context, tenant model.TenantID, res *runResult, suiteRef, subjectKind, subjectRef, variant string) {
	if strings.TrimSpace(suiteRef) == "" {
		return
	}
	outputs := make(map[string]string, len(res.steps))
	for _, so := range res.steps {
		outputs[so.Key] = so.Output
	}
	verdict, err := m.scorer.Score(ctx, tenant, ScoreRequest{
		SuiteRef: suiteRef, SubjectKind: subjectKind, SubjectRef: subjectRef, Variant: variant, Outputs: outputs,
	})
	if err != nil {
		// Executed, not scored (honest): record the suite that was requested but mark
		// the run degraded for the scoring dimension; never a silent pass.
		res.suiteRef = suiteRef
		if res.status == "completed" {
			res.status = "degraded"
		}
		return
	}
	res.scored = true
	res.score = verdict.Score
	res.passed = verdict.Passed
	res.suiteRef = suiteRef
}

// persistRun writes the run row (mutable kind, created already-terminal) plus its
// per-step output rows (append-only) and the launch self-audit, atomically. Outputs
// are clamped to maxOutputLen before persistence; with the in-proc runner the text is
// synthetic (from mocks), but an OS-level backend backing a real target would
// hash/clamp/scrub before this point — sandbox_output never stores raw real text.
func (m *Module) persistRun(ctx context.Context, sc store.Scope, mc api.ModuleContext, kind, scenarioRef, subjectRef, variant string, res runResult, started, finished model.Timestamp) (runDTO, error) {
	runRepo, err := sc.Ext(runKind)
	if err != nil {
		return runDTO{}, err
	}
	rec := model.Record{
		colKind: kind, colSubjectRef: clamp(subjectRef, maxRefLen), colRunner: res.runner,
		colIsolated: res.isolated, colRunStatus: res.status,
		colStepsTotal: int64(res.stepsTotal), colStepsOK: int64(res.stepsOK), colStepsError: int64(res.stepsError),
		colOutputsHash: res.outputsHash, colDestroyed: res.destroyed,
		colStartedAt: started.String(), colFinishedAt: finished.String(), colLaunchedBy: mc.Principal.Actor(),
	}
	if s := strings.TrimSpace(scenarioRef); s != "" {
		rec[colScenarioRef] = s
	}
	if v := strings.TrimSpace(variant); v != "" {
		rec[colVariant] = clamp(v, maxNameLen)
	}
	if strings.TrimSpace(res.suiteRef) != "" {
		rec[colSuiteRef] = res.suiteRef
	}
	if res.scored {
		rec[colScore] = res.score
		rec[colPassed] = res.passed
	}
	created, err := runRepo.Create(ctx, rec)
	if err != nil {
		return runDTO{}, err
	}
	runID := model.ID(created.String(model.ColID))

	outRepo, err := sc.Ext(outputKind)
	if err != nil {
		return runDTO{}, err
	}
	for _, so := range res.steps {
		if _, err := outRepo.Create(ctx, model.Record{
			colRunRef: runID.String(), colStepKey: clamp(so.Key, maxNameLen),
			colOutput: clamp(so.Output, maxOutputLen), colMockHit: so.MockHit,
			colOccurredAt: finished.String(),
		}); err != nil {
			return runDTO{}, err
		}
	}

	meta := map[string]any{
		"kind": kind, "runner": res.runner, "isolated": res.isolated, "destroyed": res.destroyed,
		"steps_total": res.stepsTotal, "steps_ok": res.stepsOK, "steps_error": res.stepsError,
		"status": res.status,
	}
	if strings.TrimSpace(res.suiteRef) != "" {
		meta["suite_ref"] = res.suiteRef
		meta["scored"] = res.scored
	}
	if err := auditEvent(ctx, sc, mc, "sandbox.run.launch", runKind, runID, meta); err != nil {
		return runDTO{}, err
	}
	return toRunDTO(created), nil
}

// ---- scenario run ----------------------------------------------------------------

type runScenarioRequest struct {
	Variant  string `json:"variant,omitempty"`
	SuiteRef string `json:"suite_ref,omitempty"`
}

// handleRunScenario loads a scenario, runs its steps against the isolated runner,
// persists the run + per-step outputs, and (if a suite_ref is given AND a Scorer is
// wired) scores the outputs. Synchronous → 201 with the completed run. Write-tier +
// self-audited.
func (m *Module) handleRunScenario(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id, ok := idParam(chi.URLParam(r, "id"))
	if !ok {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid id"))
		return
	}
	var req runScenarioRequest
	if !decodeJSON(w, r, &req) {
		return
	}

	// 1) Load the scenario spec (read-only).
	var scen scenarioDTO
	var spec RunSpec
	exists := false
	if err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		s, sp, ex, lerr := loadScenarioSpec(r.Context(), sc, id)
		scen, spec, exists = s, sp, ex
		return lerr
	}); err != nil {
		writeStoreError(w, err)
		return
	}
	if !exists {
		writeJSON(w, http.StatusNotFound, errorBody("scenario not found"))
		return
	}

	// 2) Execute in the isolated runner (OUTSIDE the store transaction — an OS-level
	// backend may reach an ephemeral sandbox). The suite_ref may come from the request.
	started := m.clock.Now()
	res := m.executeSpec(r.Context(), mc.Tenant, spec)
	suiteRef := strings.TrimSpace(req.SuiteRef)
	subjectRef := scen.ID
	m.score(r.Context(), mc.Tenant, &res, suiteRef, scen.SubjectKind, subjectRef, req.Variant)
	finished := m.clock.Now()

	// 3) Persist the run + outputs + self-audit, atomically.
	var out runDTO
	if err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		dto, perr := m.persistRun(r.Context(), sc, mc, runKindScenario, scen.ID, subjectRef, req.Variant, res, started, finished)
		out = dto
		return perr
	}); err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, out)
}

// ---- replay ----------------------------------------------------------------------

type replayRequest struct {
	SessionRef string    `json:"session_ref"`
	Mocks      []mockDTO `json:"mocks,omitempty"`
	SuiteRef   string    `json:"suite_ref,omitempty"`
}

// handleReplay reconstructs a historical session's input timeline via HistorySource
// and re-executes it DETERMINISTICALLY against the supplied mocks. The same
// session_ref + mocks always yields identical outputs (the runner is pure). With no
// reconstructable timeline the replay is reported DEGRADED (zero steps), never
// fabricated. Write-tier + self-audited.
func (m *Module) handleReplay(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	var req replayRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	sessionRef := clamp(strings.TrimSpace(req.SessionRef), maxRefLen)
	if sessionRef == "" {
		writeJSON(w, http.StatusBadRequest, errorBody("session_ref is required"))
		return
	}

	// 1) Reconstruct the timeline (read-only). The default core source yields zero
	// steps (no per-message timeline) ⇒ degraded replay.
	timeline, err := m.history.Timeline(r.Context(), mc.Tenant, sessionRef)
	if err != nil {
		m.debugf("sandbox: history source error", "err", err)
	}
	spec := RunSpec{Steps: make([]Step, 0, len(timeline)), Mocks: make([]Mock, 0, len(req.Mocks))}
	for _, t := range timeline {
		spec.Steps = append(spec.Steps, Step{Key: clamp(t.Key, maxNameLen), Input: clamp(t.Input, maxStepLen)})
	}
	for _, mk := range clampMocks(req.Mocks) {
		spec.Mocks = append(spec.Mocks, Mock(mk))
	}

	// 2) Execute deterministically.
	started := m.clock.Now()
	res := m.executeSpec(r.Context(), mc.Tenant, spec)
	if len(spec.Steps) == 0 {
		// No reconstructable timeline ⇒ honestly degraded, never fabricated.
		res.status = "degraded"
	}
	m.score(r.Context(), mc.Tenant, &res, strings.TrimSpace(req.SuiteRef), "session", sessionRef, "")
	finished := m.clock.Now()

	// 3) Persist (no scenario ref; subject is the session).
	var out runDTO
	if err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		dto, perr := m.persistRun(r.Context(), sc, mc, runKindReplay, "", sessionRef, "", res, started, finished)
		out = dto
		return perr
	}); err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, out)
}

// ---- run + output reads ----------------------------------------------------------

type runDTO struct {
	ID          string   `json:"id"`
	ScenarioRef string   `json:"scenario_ref,omitempty"`
	Kind        string   `json:"kind"`
	SubjectRef  string   `json:"subject_ref,omitempty"`
	Variant     string   `json:"variant,omitempty"`
	Runner      string   `json:"runner"`
	Isolated    bool     `json:"isolated"`
	Status      string   `json:"status"`
	StepsTotal  int64    `json:"steps_total"`
	StepsOK     int64    `json:"steps_ok"`
	StepsError  int64    `json:"steps_error"`
	OutputsHash string   `json:"outputs_hash,omitempty"`
	Score       *float64 `json:"score,omitempty"`
	Passed      *bool    `json:"passed,omitempty"`
	SuiteRef    string   `json:"suite_ref,omitempty"`
	Destroyed   bool     `json:"destroyed"`
	StartedAt   string   `json:"started_at"`
	FinishedAt  string   `json:"finished_at,omitempty"`
	LaunchedBy  string   `json:"launched_by,omitempty"`
}

func toRunDTO(rec model.Record) runDTO {
	dto := runDTO{
		ID: rec.String(model.ColID), ScenarioRef: rec.String(colScenarioRef), Kind: rec.String(colKind),
		SubjectRef: rec.String(colSubjectRef), Variant: rec.String(colVariant), Runner: rec.String(colRunner),
		Isolated: rec.Bool(colIsolated), Status: rec.String(colRunStatus),
		StepsTotal: rec.Int(colStepsTotal), StepsOK: rec.Int(colStepsOK), StepsError: rec.Int(colStepsError),
		OutputsHash: rec.String(colOutputsHash), SuiteRef: rec.String(colSuiteRef), Destroyed: rec.Bool(colDestroyed),
		StartedAt: rec.String(colStartedAt), FinishedAt: rec.String(colFinishedAt), LaunchedBy: rec.String(colLaunchedBy),
	}
	if !rec.IsNull(colScore) {
		s := rec.Float(colScore)
		dto.Score = &s
	}
	if !rec.IsNull(colPassed) {
		p := rec.Bool(colPassed)
		dto.Passed = &p
	}
	return dto
}

// handleListRuns lists the tenant's runs (optionally filtered by kind / scenario).
func (m *Module) handleListRuns(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	q := listQuery(r)
	if v := strings.TrimSpace(r.URL.Query().Get("kind")); v != "" {
		q.Filters = append(q.Filters, eq(colKind, v))
	}
	if v := strings.TrimSpace(r.URL.Query().Get("scenario_ref")); v != "" {
		q.Filters = append(q.Filters, eq(colScenarioRef, v))
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

// handleGetRun returns one run.
func (m *Module) handleGetRun(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id, ok := idParam(chi.URLParam(r, "id"))
	if !ok {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid id"))
		return
	}
	var out runDTO
	found := false
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(runKind)
		if err != nil {
			return err
		}
		rec, err := repo.Get(r.Context(), id)
		if err != nil {
			if isNotFound(err) {
				return nil
			}
			return err
		}
		found, out = true, toRunDTO(rec)
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

type outputDTO struct {
	ID         string `json:"id"`
	RunRef     string `json:"run_ref"`
	StepKey    string `json:"step_key"`
	Output     string `json:"output"`
	MockHit    bool   `json:"mock_hit"`
	OccurredAt string `json:"occurred_at"`
}

func toOutputDTO(rec model.Record) outputDTO {
	return outputDTO{
		ID: rec.String(model.ColID), RunRef: rec.String(colRunRef), StepKey: rec.String(colStepKey),
		Output: rec.String(colOutput), MockHit: rec.Bool(colMockHit), OccurredAt: rec.String(colOccurredAt),
	}
}

// loadRunOutputs reads one run's per-step outputs, ordered by step key (stable, so
// the SSE replay and the JSON read agree).
func loadRunOutputs(ctx context.Context, sc store.Scope, runID model.ID) ([]outputDTO, error) {
	repo, err := sc.Ext(outputKind)
	if err != nil {
		return nil, err
	}
	recs, err := listAll(ctx, repo, eq(colRunRef, runID.String()))
	if err != nil {
		return nil, err
	}
	out := make([]outputDTO, 0, len(recs))
	for _, rec := range recs {
		out = append(out, toOutputDTO(rec))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].StepKey < out[j].StepKey })
	return out, nil
}

// handleListOutputs lists one run's per-step outputs.
func (m *Module) handleListOutputs(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id, ok := idParam(chi.URLParam(r, "id"))
	if !ok {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid id"))
		return
	}
	out := listResponse[outputDTO]{Items: []outputDTO{}}
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		items, lerr := loadRunOutputs(r.Context(), sc, id)
		if lerr != nil {
			return lerr
		}
		out.Items = items
		return nil
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}
