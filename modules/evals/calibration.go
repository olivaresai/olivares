// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package evals

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

// This file is the judge↔human CALIBRATION surface: the
// hybrid consensus is judge-for-scale + human-for-calibration, so before the
// llm_judge's verdicts gate anything, the judge is MEASURED against a human-labeled
// reference set and the measurement is immutable evidence.
//
//   - An ITEM is one human-labeled reference: (input, output[, expected], criterion)
//     plus the human pass/fail (and optional graded score). Item text is the same
//     operator-authorized NON-PRODUCTION fixture carve-out as a suite case (contract
//     §2.1); the labeling tool is `olivares evals label`.
//   - A calibration RUN invokes the wired Judge over every item of a set (outside
//     any transaction) and computes: percent agreement with its 95% Wilson interval,
//     Cohen's kappa (agreement alone is not defensible under class imbalance),
//     sensitivity/specificity vs the human reference (the Rogan–Gladen inputs the
//     gate uses to surface a bias-corrected pass-rate), mean absolute score error,
//     and the verbosity-bias correlation corr(len(output), judge−human score).
//   - The REPORT is append-only. meets_target requires agreement ≥ target AND a
//     DEFINED kappa ≥ kappa_floor: a set whose human labels are all-pass (or
//     all-fail) cannot measure chance-corrected agreement, so it cannot certify a
//     judge — label both kinds. Numbers are measured or absent, NEVER fabricated
//     (degenerate statistics carry *_defined=false / n=0).
//
// Defaults (decision 1 + docs/EVAL-METHODOLOGY.md §2): agreement target
// 0.85 (the 85–90% practitioner band, anchored on MT-Bench's 85% human-judge vs 81%
// human-human agreement), kappa floor 0.6 (Landis & Koch "substantial").
const (
	defaultAgreementTarget = 0.85
	defaultKappaFloor      = 0.60

	calibrationSetDefault = "default"
	// calibrationSuite names the core EvalResult evidence stream.
	calibrationSuite = "judge-calibration"

	findingKindCalibration = "judge_calibration"
	busJudgeCalibration    = "judge_calibration"
)

// ---- items -------------------------------------------------------------------------

type calibItemDTO struct {
	ID          string   `json:"id"`
	SetName     string   `json:"set_name"`
	CaseKey     string   `json:"case_key"`
	Input       string   `json:"input,omitempty"`
	Output      string   `json:"output"`
	Expected    string   `json:"expected,omitempty"`
	Criterion   string   `json:"criterion,omitempty"`
	HumanPassed bool     `json:"human_passed"`
	HumanScore  *float64 `json:"human_score,omitempty"`
	LabeledBy   string   `json:"labeled_by,omitempty"`
	Notes       string   `json:"notes,omitempty"`
}

func toCalibItemDTO(rec model.Record) calibItemDTO {
	dto := calibItemDTO{
		ID: rec.String(model.ColID), SetName: rec.String(colSetName), CaseKey: rec.String(colCaseKey),
		Input: rec.String(colInput), Output: rec.String(colOutput), Expected: rec.String(colExpected),
		Criterion: rec.String(colCriterion), HumanPassed: rec.Bool(colHumanPassed),
		LabeledBy: rec.String(colLabeledBy), Notes: rec.String(colNotes),
	}
	if _, ok := rec[colHumanScore]; ok && rec[colHumanScore] != nil {
		s := rec.Float(colHumanScore)
		dto.HumanScore = &s
	}
	return dto
}

type calibItemInput struct {
	CaseKey     string   `json:"case_key"`
	Input       string   `json:"input,omitempty"`
	Output      string   `json:"output"`
	Expected    string   `json:"expected,omitempty"`
	Criterion   string   `json:"criterion,omitempty"`
	HumanPassed bool     `json:"human_passed"`
	HumanScore  *float64 `json:"human_score,omitempty"`
	Notes       string   `json:"notes,omitempty"`
}

type addCalibItemsRequest struct {
	SetName string           `json:"set_name,omitempty"`
	Items   []calibItemInput `json:"items"`
}

type addCalibItemsResponse struct {
	SetName string                      `json:"set_name"`
	Created int                         `json:"created"`
	Updated int                         `json:"updated"`
	Items   api.JSONArray[calibItemDTO] `json:"items"`
}

// handleAddCalibItems creates or RE-labels reference items (upsert by set+case_key:
// a correction is an audited update, the history lives in the ledger). Write-tier +
// self-audited per batch.
func (m *Module) handleAddCalibItems(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	var req addCalibItemsRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	set := firstNonEmpty(clamp(strings.TrimSpace(req.SetName), maxNameLen), calibrationSetDefault)
	if len(req.Items) == 0 {
		writeJSON(w, http.StatusBadRequest, errorBody("items is required"))
		return
	}
	for _, it := range req.Items {
		if strings.TrimSpace(it.CaseKey) == "" {
			writeJSON(w, http.StatusBadRequest, errorBody("every item needs a case_key"))
			return
		}
		if strings.TrimSpace(it.Output) == "" {
			writeJSON(w, http.StatusBadRequest, errorBody("every item needs the candidate output the human judged"))
			return
		}
		if it.HumanScore != nil && (*it.HumanScore < 0 || *it.HumanScore > 1) {
			writeJSON(w, http.StatusBadRequest, errorBody("human_score must be within [0,1]"))
			return
		}
	}

	out := addCalibItemsResponse{SetName: set, Items: []calibItemDTO{}}
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(calItemKind)
		if err != nil {
			return err
		}
		for _, it := range req.Items {
			caseKey := clamp(strings.TrimSpace(it.CaseKey), maxNameLen)
			rec := model.Record{
				colSetName: set, colCaseKey: caseKey,
				colInput: clamp(it.Input, maxFixtureLen), colOutput: clamp(it.Output, maxFixtureLen),
				colExpected: clamp(it.Expected, maxFixtureLen), colCriterion: clamp(it.Criterion, maxFixtureLen),
				colHumanPassed: it.HumanPassed, colLabeledBy: mc.Principal.Actor(),
				colNotes: clamp(it.Notes, maxNameLen),
			}
			if it.HumanScore != nil {
				rec[colHumanScore] = *it.HumanScore
			}
			created, cerr := repo.Create(r.Context(), rec)
			if cerr == nil {
				out.Created++
				out.Items = append(out.Items, toCalibItemDTO(created))
				continue
			}
			if !isConflict(cerr) {
				return cerr
			}
			// Re-label: load the existing (set, case_key) item and update it in place.
			existing, lerr := listAll(r.Context(), repo, eq(colSetName, set), eq(colCaseKey, caseKey))
			if lerr != nil {
				return lerr
			}
			if len(existing) == 0 {
				return cerr // conflict but not found: surface the original error
			}
			upd := existing[0]
			for k, v := range rec {
				upd[k] = v
			}
			updated, uerr := repo.Update(r.Context(), upd)
			if uerr != nil {
				return uerr
			}
			out.Updated++
			out.Items = append(out.Items, toCalibItemDTO(updated))
		}
		return auditEvent(r.Context(), sc, mc, "evals.calibration.label", calItemKind, "", map[string]any{
			"set_name": set, "created": out.Created, "updated": out.Updated,
		})
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, out)
}

// handleListCalibItems lists a set's reference items (?set=, default all sets).
func (m *Module) handleListCalibItems(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	q := listQuery(r)
	if v := strings.TrimSpace(r.URL.Query().Get("set")); v != "" {
		q.Filters = append(q.Filters, eq(colSetName, v))
	}
	out := listResponse[calibItemDTO]{Items: []calibItemDTO{}}
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(calItemKind)
		if err != nil {
			return err
		}
		recs, page, err := repo.List(r.Context(), q)
		if err != nil {
			return err
		}
		for _, rec := range recs {
			out.Items = append(out.Items, toCalibItemDTO(rec))
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

// ---- calibration runs ----------------------------------------------------------------

type calibReportDTO struct {
	ID          string  `json:"id"`
	SetName     string  `json:"set_name"`
	JudgeModel  string  `json:"judge_model,omitempty"`
	Status      string  `json:"status"` // completed|degraded
	ItemsTotal  int64   `json:"items_total"`
	ItemsScored int64   `json:"items_scored"`
	ItemsError  int64   `json:"items_error"`
	Agreement   float64 `json:"agreement"`
	AgreementCI ciDTO   `json:"agreement_ci"`
	Kappa       float64 `json:"kappa"`
	KappaOK     bool    `json:"kappa_defined"`
	Sensitivity float64 `json:"sensitivity"`
	// SensitivityN/SpecificityN are the denominators (human-pass / human-fail item
	// counts); n=0 means the rate is UNMEASURED, not zero.
	SensitivityN int64   `json:"sensitivity_n"`
	Specificity  float64 `json:"specificity"`
	SpecificityN int64   `json:"specificity_n"`
	MeanAbsErr   float64 `json:"mean_abs_err"`
	VerbCorr     float64 `json:"verbosity_corr"`
	VerbCorrOK   bool    `json:"verbosity_corr_defined"`
	Target       float64 `json:"target"`
	KappaFloor   float64 `json:"kappa_floor"`
	MeetsTarget  bool    `json:"meets_target"`
	LaunchedBy   string  `json:"launched_by,omitempty"`
	OccurredAt   string  `json:"occurred_at"`
}

func toCalibReportDTO(rec model.Record) calibReportDTO {
	return calibReportDTO{
		ID: rec.String(model.ColID), SetName: rec.String(colSetName), JudgeModel: rec.String(colJudgeModel),
		Status: rec.String(colRunStatus), ItemsTotal: rec.Int(colItemsTotal), ItemsScored: rec.Int(colItemsScored),
		ItemsError: rec.Int(colItemsError), Agreement: rec.Float(colAgreement),
		AgreementCI: ciDTO{Lo: rec.Float(colAgreeLo), Hi: rec.Float(colAgreeHi)},
		Kappa:       rec.Float(colKappa), KappaOK: rec.Bool(colKappaOK),
		Sensitivity: rec.Float(colSens), SensitivityN: rec.Int(colSensN),
		Specificity: rec.Float(colSpec), SpecificityN: rec.Int(colSpecN),
		MeanAbsErr: rec.Float(colMeanAbsErr), VerbCorr: rec.Float(colVerbCorr), VerbCorrOK: rec.Bool(colVerbCorrOK),
		Target: rec.Float(colTarget), KappaFloor: rec.Float(colKappaFloor), MeetsTarget: rec.Bool(colMeets),
		LaunchedBy: rec.String(colLaunchedBy), OccurredAt: rec.String(colOccurredAt),
	}
}

type runCalibrationRequest struct {
	SetName string `json:"set_name,omitempty"`
	// JudgeModel pins the judge model under calibration. The pin is part of the
	// report identity: the gate trusts a report only for the SAME pin.
	JudgeModel string `json:"judge_model,omitempty"`
	// Target/KappaFloor override the defaults (0.85 / 0.6). They can only RAISE the
	// bar (a request below the default is clamped up): weakening the floor per-run
	// would let a caller calibrate around the commitment.
	Target     float64 `json:"target,omitempty"`
	KappaFloor float64 `json:"kappa_floor,omitempty"`
}

// handleRunCalibration measures the wired judge against a labeled set. The judge
// calls run OUTSIDE any transaction (two-phase); the report + core EvalResult (+ a
// Finding when below target) persist atomically. Write-tier + self-audited. With no
// judge wired it is an honest 412 — a calibration cannot be simulated.
func (m *Module) handleRunCalibration(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	var req runCalibrationRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if _, off := m.judge.(offlineJudge); off {
		writeJSON(w, http.StatusPreconditionFailed, errorBody("no judge wired — a judge calibration cannot run without the judge"))
		return
	}
	set := firstNonEmpty(clamp(strings.TrimSpace(req.SetName), maxNameLen), calibrationSetDefault)
	judgeModel := clamp(strings.TrimSpace(req.JudgeModel), maxRefLen)
	target := req.Target
	if target < defaultAgreementTarget {
		target = defaultAgreementTarget
	}
	kappaFloor := req.KappaFloor
	if kappaFloor < defaultKappaFloor {
		kappaFloor = defaultKappaFloor
	}

	var items []calibItemDTO
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(calItemKind)
		if err != nil {
			return err
		}
		recs, err := listAll(r.Context(), repo, eq(colSetName, set))
		if err != nil {
			return err
		}
		for _, rec := range recs {
			items = append(items, toCalibItemDTO(rec))
		}
		return nil
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if len(items) == 0 {
		writeJSON(w, http.StatusBadRequest, errorBody("calibration set has no items — label items first (olivares evals label)"))
		return
	}

	// Phase 2: judge every item (network I/O — outside any transaction).
	stats, judgeUnwired := m.measureCalibration(r.Context(), mc.Tenant, judgeModel, items)
	if judgeUnwired {
		writeJSON(w, http.StatusPreconditionFailed, errorBody("no judge wired — a judge calibration cannot run without the judge"))
		return
	}
	stats.target, stats.kappaFloor = target, kappaFloor
	stats.finish()

	// Phase 3: persist the immutable report + the canonical evidence atomically.
	now := m.clock.Now()
	var out calibReportDTO
	err = mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(calReportKind)
		if err != nil {
			return err
		}
		rec, err := repo.Create(r.Context(), model.Record{
			colSetName: set, colJudgeModel: judgeModel, colRunStatus: stats.status,
			colItemsTotal: int64(len(items)), colItemsScored: int64(stats.scored), colItemsError: int64(stats.errored),
			colAgreement: stats.agreement, colAgreeLo: stats.agreeLo, colAgreeHi: stats.agreeHi,
			colKappa: stats.kappa, colKappaOK: stats.kappaOK,
			colSens: stats.sens, colSensN: int64(stats.sensN), colSpec: stats.spec, colSpecN: int64(stats.specN),
			colMeanAbsErr: stats.meanAbsErr, colVerbCorr: stats.verbCorr, colVerbCorrOK: stats.verbCorrOK,
			colTarget: target, colKappaFloor: kappaFloor, colMeets: stats.meets,
			colLaunchedBy: mc.Principal.Actor(), colOccurredAt: now.String(),
		})
		if err != nil {
			return err
		}
		out = toCalibReportDTO(rec)

		// The canonical cross-module evidence (reads core EvalResults).
		if _, err := sc.Evals().Create(r.Context(), model.EvalResult{
			Suite: calibrationSuite, SubjectKind: "model", Score: stats.agreement, Passed: stats.meets, OccurredAt: now,
			Metrics: map[string]any{
				"agreement": stats.agreement, "agreement_lo": stats.agreeLo, "agreement_hi": stats.agreeHi,
				"kappa": stats.kappa, "kappa_defined": stats.kappaOK,
				"sensitivity": stats.sens, "sensitivity_n": stats.sensN,
				"specificity": stats.spec, "specificity_n": stats.specN,
				"items_total": len(items), "items_scored": stats.scored, "items_error": stats.errored,
				"target": target, "kappa_floor": kappaFloor, "status": stats.status,
			},
			Metadata: map[string]any{"set_name": set, "judge_model": judgeModel, "report_ref": out.ID},
		}); err != nil {
			return err
		}

		if !stats.meets {
			sev := sdkmodel.SeverityMedium
			if stats.scored == 0 || stats.agreement < target-0.10 {
				sev = sdkmodel.SeverityHigh
			}
			detail := set + "|" + judgeModel + "|agreement=" + formatFloat(stats.agreement) + "|kappa=" + formatFloat(stats.kappa)
			if _, err := sc.Findings().Create(r.Context(), model.Finding{
				Kind: findingKindCalibration, Severity: sevToCore(sev), Status: model.FindingOpen,
				Source: calibrationSuite, SubjectKind: "model",
				Title:      clamp("Judge calibration below target: "+set, maxNameLen),
				DetailHash: hashBytes(detail), OccurredAt: now,
				Metadata: map[string]any{
					"set_name": set, "judge_model": judgeModel, "report_ref": out.ID,
					"agreement": stats.agreement, "target": target, "kappa": stats.kappa, "kappa_floor": kappaFloor,
				},
			}); err != nil {
				return err
			}
			stats.failSeverity = sev
		}

		return auditEvent(r.Context(), sc, mc, "evals.calibration.run", calReportKind, model.ID(out.ID), map[string]any{
			"set_name": set, "judge_model": judgeModel, "items": len(items), "scored": stats.scored,
			"agreement": stats.agreement, "kappa": stats.kappa, "meets_target": stats.meets,
		})
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if !stats.meets && m.host != nil {
		report := sdkmodel.FindingReport{
			Kind: busJudgeCalibration, Severity: stats.failSeverity, SubjectKind: "model",
			SubjectRef: clamp(judgeModel, maxRefLen),
			Title:      clamp("Judge calibration below target: "+set, maxNameLen),
			DetailHash: hashHex(set + "|" + judgeModel + "|agreement=" + formatFloat(stats.agreement)),
			OccurredAt: m.clock.Now().Time(),
		}
		if perr := m.host.Publish(r.Context(), evalEvent(mc.Tenant, report)); perr != nil {
			m.debugf("evals: publish calibration finding failed", "err", perr)
		}
	}
	writeJSON(w, http.StatusCreated, out)
}

// calibStats accumulates one calibration measurement.
type calibStats struct {
	scored, errored        int
	judgePass, humanPass   []bool
	judgeScore, humanScore []float64
	outputLen              []float64

	agreement, agreeLo, agreeHi float64
	kappa                       float64
	kappaOK                     bool
	sens, spec                  float64
	sensN, specN                int
	meanAbsErr                  float64
	verbCorr                    float64
	verbCorrOK                  bool
	target, kappaFloor          float64
	status                      string
	meets                       bool
	failSeverity                sdkmodel.Severity
}

// measureCalibration judges every item and accumulates the raw vectors. The bool is
// true when the judge turns out to be unwired (the offline sentinel) — the caller
// answers 412, never a fabricated report.
func (m *Module) measureCalibration(ctx context.Context, tenant model.TenantID, judgeModel string, items []calibItemDTO) (calibStats, bool) {
	var st calibStats
	for _, it := range items {
		verdict, err := m.judge.Judge(ctx, tenant, JudgeRequest{
			ModelRef: judgeModel, Input: it.Input, Output: it.Output,
			Expected: it.Expected, Criterion: it.Criterion,
		})
		if errors.Is(err, errNoJudge) {
			return calibStats{}, true
		}
		if err != nil {
			st.errored++
			continue
		}
		st.scored++
		st.judgePass = append(st.judgePass, verdict.Passed)
		st.humanPass = append(st.humanPass, it.HumanPassed)
		hs := 0.0
		if it.HumanScore != nil {
			hs = *it.HumanScore
		} else if it.HumanPassed {
			hs = 1.0
		}
		st.judgeScore = append(st.judgeScore, verdict.Score)
		st.humanScore = append(st.humanScore, hs)
		st.outputLen = append(st.outputLen, float64(len(it.Output)))
	}
	return st, false
}

// finish derives the report statistics from the accumulated vectors. Degenerate
// statistics stay UNMEASURED (flags/zero-n), and a run that scored nothing is
// status=degraded and can never meet the target.
func (st *calibStats) finish() {
	st.status = "completed"
	if st.scored == 0 {
		st.status = "degraded"
		st.agreeLo, st.agreeHi = 0, 1
		return
	}
	agree := 0
	for i := range st.judgePass {
		if st.judgePass[i] == st.humanPass[i] {
			agree++
		}
	}
	st.agreement = float64(agree) / float64(st.scored)
	st.agreeLo, st.agreeHi = wilsonInterval(agree, st.scored)
	st.kappa, st.kappaOK = cohenKappa(st.judgePass, st.humanPass)
	st.sens, st.sensN, st.spec, st.specN = sensSpec(st.judgePass, st.humanPass)

	var absSum float64
	resid := make([]float64, len(st.judgeScore))
	for i := range st.judgeScore {
		d := st.judgeScore[i] - st.humanScore[i]
		resid[i] = d
		if d < 0 {
			d = -d
		}
		absSum += d
	}
	st.meanAbsErr = absSum / float64(st.scored)
	st.verbCorr, st.verbCorrOK = pearson(st.outputLen, resid)

	// meets_target demands a DEFINED kappa: an all-pass (or all-fail) human
	// reference cannot measure chance-corrected agreement, so it certifies nothing.
	st.meets = st.agreement >= st.target && st.kappaOK && st.kappa >= st.kappaFloor
}

// handleListCalibReports lists calibration reports (?set=, ?judge_model=), newest
// first per the occurred_at index order of the store's default listing.
func (m *Module) handleListCalibReports(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	q := listQuery(r)
	if v := strings.TrimSpace(r.URL.Query().Get("set")); v != "" {
		q.Filters = append(q.Filters, eq(colSetName, v))
	}
	if v := strings.TrimSpace(r.URL.Query().Get("judge_model")); v != "" {
		q.Filters = append(q.Filters, eq(colJudgeModel, v))
	}
	out := listResponse[calibReportDTO]{Items: []calibReportDTO{}}
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(calReportKind)
		if err != nil {
			return err
		}
		recs, page, err := repo.List(r.Context(), q)
		if err != nil {
			return err
		}
		for _, rec := range recs {
			out.Items = append(out.Items, toCalibReportDTO(rec))
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

// latestCalibration returns the newest calibration report for (set-agnostic)
// judge_model, or ok=false when none exists. The gate trusts only a model-pin match.
func latestCalibration(ctx context.Context, sc store.Scope, judgeModel string) (calibReportDTO, bool, error) {
	repo, err := sc.Ext(calReportKind)
	if err != nil {
		return calibReportDTO{}, false, err
	}
	recs, err := listAll(ctx, repo, eq(colJudgeModel, judgeModel))
	if err != nil {
		return calibReportDTO{}, false, err
	}
	var latest model.Record
	for _, rec := range recs {
		if latest == nil || rec.String(colOccurredAt) > latest.String(colOccurredAt) {
			latest = rec
		}
	}
	if latest == nil {
		return calibReportDTO{}, false, nil
	}
	return toCalibReportDTO(latest), true, nil
}
