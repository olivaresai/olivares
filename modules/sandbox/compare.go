// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sandbox

import (
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// The pre/post-deploy verdicts. inconclusive is the honest answer when neither a
// score nor a deterministic output difference can decide (e.g. unscored runs with
// identical synthetic outputs cannot prove an improvement).
const (
	verdictImproved     = "improved"
	verdictRegressed    = "regressed"
	verdictUnchanged    = "unchanged"
	verdictInconclusive = "inconclusive"
)

type compareRequest struct {
	ScenarioRef      string `json:"scenario_ref,omitempty"`
	SessionRef       string `json:"session_ref,omitempty"`
	BaselineVariant  string `json:"baseline_variant"`
	CandidateVariant string `json:"candidate_variant"`
	SuiteRef         string `json:"suite_ref,omitempty"`
}

// handleCompare runs the SAME scenario/steps against two variants, scores both (if a
// Scorer is wired) or compares their deterministic outputs_hash, derives a verdict +
// delta, and persists two runs plus a sandbox_comparison (append-only deploy-decision
// evidence). It is the link to (deploy gating). Admin-tier + self-audited.
func (m *Module) handleCompare(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	var req compareRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	baseVar := clamp(strings.TrimSpace(req.BaselineVariant), maxNameLen)
	candVar := clamp(strings.TrimSpace(req.CandidateVariant), maxNameLen)
	if baseVar == "" || candVar == "" {
		writeJSON(w, http.StatusBadRequest, errorBody("baseline_variant and candidate_variant are required"))
		return
	}
	scenarioRef := strings.TrimSpace(req.ScenarioRef)
	sessionRef := clamp(strings.TrimSpace(req.SessionRef), maxRefLen)
	if scenarioRef == "" && sessionRef == "" {
		writeJSON(w, http.StatusBadRequest, errorBody("scenario_ref or session_ref is required"))
		return
	}

	// 1) Resolve the spec + subject (read-only). A scenario_ref loads the persisted
	// fixture; a session_ref reconstructs the timeline (degraded ⇒ zero steps).
	var spec RunSpec
	var subjectKind, subjectRef string
	var scenID model.ID
	if scenarioRef != "" {
		id, ok := idParam(scenarioRef)
		if !ok {
			writeJSON(w, http.StatusBadRequest, errorBody("invalid scenario_ref"))
			return
		}
		scenID = id
		exists := false
		var scen scenarioDTO
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
		subjectKind, subjectRef = scen.SubjectKind, scen.ID
	} else {
		timeline, err := m.history.Timeline(r.Context(), mc.Tenant, sessionRef)
		if err != nil {
			m.debugf("sandbox: history source error", "err", err)
		}
		spec.Steps = make([]Step, 0, len(timeline))
		for _, t := range timeline {
			spec.Steps = append(spec.Steps, Step{Key: clamp(t.Key, maxNameLen), Input: clamp(t.Input, maxStepLen)})
		}
		subjectKind, subjectRef = "session", sessionRef
	}

	suiteRef := strings.TrimSpace(req.SuiteRef)

	// 2) Execute the SAME spec for each variant. The variant is recorded on the run;
	// the in-proc runner is deterministic, so two identical specs+variants yield equal
	// outputs (the honest "unchanged" path when nothing distinguishes them).
	startBase := m.clock.Now()
	baseRes := m.executeSpec(r.Context(), mc.Tenant, spec)
	m.score(r.Context(), mc.Tenant, &baseRes, suiteRef, subjectKind, subjectRef, baseVar)
	endBase := m.clock.Now()

	startCand := m.clock.Now()
	candRes := m.executeSpec(r.Context(), mc.Tenant, spec)
	m.score(r.Context(), mc.Tenant, &candRes, suiteRef, subjectKind, subjectRef, candVar)
	endCand := m.clock.Now()

	// A session whose timeline yielded no steps (none recorded, or the source
	// refused — e.g. a partial-replay refusal) is honestly DEGRADED on both
	// runs, the same rule handleReplay applies: never two confident zero-step
	// "completed" runs backing a deploy decision.
	if scenarioRef == "" && len(spec.Steps) == 0 {
		if baseRes.status == "completed" {
			baseRes.status = "degraded"
		}
		if candRes.status == "completed" {
			candRes.status = "degraded"
		}
	}

	verdict, baseScore, candScore, delta := decideVerdict(baseRes, candRes)

	// 3) Persist both runs, the comparison evidence and the decision self-audit,
	// atomically.
	var out comparisonDTO
	if err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		baseDTO, perr := m.persistRun(r.Context(), sc, mc, runKindCompare, scenarioRef, subjectRef, baseVar, baseRes, startBase, endBase)
		if perr != nil {
			return perr
		}
		candDTO, perr := m.persistRun(r.Context(), sc, mc, runKindCompare, scenarioRef, subjectRef, candVar, candRes, startCand, endCand)
		if perr != nil {
			return perr
		}
		cmpRepo, err := sc.Ext(comparisonKind)
		if err != nil {
			return err
		}
		rec := model.Record{
			colBaselineRun: baseDTO.ID, colCandidateRun: candDTO.ID, colSubjectRef: clamp(subjectRef, maxRefLen),
			colVerdict: verdict, colBaselineScore: baseScore, colCandScore: candScore, colDelta: delta,
			colDecidedBy: mc.Principal.Actor(), colOccurredAt: endCand.String(),
		}
		if scenarioRef != "" {
			rec[colScenarioRef] = scenarioRef
		}
		if suiteRef != "" {
			rec[colSuiteRef] = suiteRef
		}
		created, err := cmpRepo.Create(r.Context(), rec)
		if err != nil {
			return err
		}
		out = toComparisonDTO(created)
		return auditEvent(r.Context(), sc, mc, "sandbox.compare.decide", comparisonKind,
			model.ID(created.String(model.ColID)),
			map[string]any{
				"scenario_ref": scenID.String(), "baseline_variant": baseVar, "candidate_variant": candVar,
				"verdict": verdict, "delta": delta, "suite_ref": suiteRef,
			})
	}); err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, out)
}

// decideVerdict derives the comparison verdict. When BOTH runs are scored the verdict
// follows the score delta (candidate − baseline). When neither is scored it falls back
// to the deterministic outputs_hash: equal hashes ⇒ unchanged, differing hashes ⇒
// inconclusive (a hash difference proves they differ but not which is better — only a
// scorer can rank them). A score is only trusted when present on both sides. Two runs
// that executed NOTHING prove nothing: equal empty hashes are not equivalence
// evidence, so the zero-step unscored case is inconclusive, never unchanged.
func decideVerdict(base, cand runResult) (verdict string, baseScore, candScore, delta float64) {
	if base.scored && cand.scored {
		baseScore, candScore = base.score, cand.score
		delta = candScore - baseScore
		switch {
		case delta > 0:
			return verdictImproved, baseScore, candScore, delta
		case delta < 0:
			return verdictRegressed, baseScore, candScore, delta
		default:
			return verdictUnchanged, baseScore, candScore, delta
		}
	}
	// Unscored: compare deterministic output hashes — meaningful only when
	// something actually ran.
	if base.stepsTotal == 0 && cand.stepsTotal == 0 {
		return verdictInconclusive, 0, 0, 0
	}
	if base.outputsHash == cand.outputsHash {
		return verdictUnchanged, 0, 0, 0
	}
	return verdictInconclusive, 0, 0, 0
}

// ---- comparison reads ------------------------------------------------------------

type comparisonDTO struct {
	ID             string  `json:"id"`
	ScenarioRef    string  `json:"scenario_ref,omitempty"`
	BaselineRunRef string  `json:"baseline_run_ref"`
	CandidateRun   string  `json:"candidate_run_ref"`
	SubjectRef     string  `json:"subject_ref,omitempty"`
	SuiteRef       string  `json:"suite_ref,omitempty"`
	Verdict        string  `json:"verdict"`
	BaselineScore  float64 `json:"baseline_score"`
	CandidateScore float64 `json:"candidate_score"`
	Delta          float64 `json:"delta"`
	DecidedBy      string  `json:"decided_by,omitempty"`
	OccurredAt     string  `json:"occurred_at"`
}

func toComparisonDTO(rec model.Record) comparisonDTO {
	return comparisonDTO{
		ID: rec.String(model.ColID), ScenarioRef: rec.String(colScenarioRef), BaselineRunRef: rec.String(colBaselineRun),
		CandidateRun: rec.String(colCandidateRun), SubjectRef: rec.String(colSubjectRef), SuiteRef: rec.String(colSuiteRef),
		Verdict: rec.String(colVerdict), BaselineScore: rec.Float(colBaselineScore), CandidateScore: rec.Float(colCandScore),
		Delta: rec.Float(colDelta), DecidedBy: rec.String(colDecidedBy), OccurredAt: rec.String(colOccurredAt),
	}
}

// handleListComparisons lists the tenant's pre/post-deploy comparisons.
func (m *Module) handleListComparisons(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	q := listQuery(r)
	if v := strings.TrimSpace(r.URL.Query().Get("scenario_ref")); v != "" {
		q.Filters = append(q.Filters, eq(colScenarioRef, v))
	}
	if v := strings.TrimSpace(r.URL.Query().Get("verdict")); v != "" {
		q.Filters = append(q.Filters, eq(colVerdict, v))
	}
	out := listResponse[comparisonDTO]{Items: []comparisonDTO{}}
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(comparisonKind)
		if err != nil {
			return err
		}
		recs, page, err := repo.List(r.Context(), q)
		if err != nil {
			return err
		}
		for _, rec := range recs {
			out.Items = append(out.Items, toComparisonDTO(rec))
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

// handleGetComparison returns one comparison.
func (m *Module) handleGetComparison(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id, ok := idParam(chi.URLParam(r, "id"))
	if !ok {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid id"))
		return
	}
	var out comparisonDTO
	found := false
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(comparisonKind)
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
		found, out = true, toComparisonDTO(rec)
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
