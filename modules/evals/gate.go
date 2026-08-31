// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package evals

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// This file is the CI REGRESSION GATE: POST /gate scores
// candidate outputs against a suite and returns a BLOCKING verdict a CI pipeline
// maps to its exit code (`olivares evals gate`). The gate semantics implement
// the decisions verbatim:
//
//  1. BLOCKING, not advisory: a regression vs baseline, a pass-rate below the
//     suite's own threshold, or an UNCALIBRATED judge fails the gate. The only
//     escape is the governed override (admin-tier, reason required, audited).
//  2. Judge trust is EARNED: an llm_judge suite gates only on a judge whose latest
//     calibration report (same model pin) meets the agreement target — verdicts
//     from an unmeasured judge cannot block or unblock a merge.
//  3. Judge-in-CI cost control: a deterministic seeded SAMPLE of the cases (fixed
//     subset per seed — reruns re-judge the same cases), a verdict CACHE keyed by
//     (prompt-version | judge-model pin | case content) so a rerun is free, and an
//     Budget pre-flight that refuses to spend when the budget says block.
//  4. Honest degradation: with no judge credential the gate WARNS (declared in the
//     verdict + reasons), it never silently passes or silently judges nothing.
//
// A gate evaluation persists a normal eval run (it joins the suite's trend) plus an
// evals_gate row that records verdict, reasons, seed, sample and override state.

// judgeCacheVersion is baked into every cache key. Bump it when the judge's prompt
// or verdict behavior changes (see the note on judgeSystemPrompt in
// connectors/claude-api/judge.go) so a stale verdict can never be served.
const judgeCacheVersion = "s144-v1"

// Gate verdicts and reason codes (stable strings — CI scripts match on them).
const (
	verdictPass = "pass"
	verdictFail = "fail"
	verdictWarn = "warn"

	reasonRegression       = "regression_vs_baseline"
	reasonBelowThreshold   = "below_pass_threshold"
	reasonRunError         = "run_error"
	reasonUncalibrated     = "judge_uncalibrated"
	reasonCalibBelowTarget = "judge_calibration_below_target"
	reasonNoJudge          = "no_judge_credential"
	reasonBudgetBlocked    = "budget_blocked"
	reasonBudgetThrottled  = "budget_throttled"
	reasonNoBaseline       = "no_baseline"
)

type gateRequest struct {
	SuiteRef    string            `json:"suite_ref"`
	SubjectKind string            `json:"subject_kind,omitempty"`
	SubjectRef  string            `json:"subject_ref,omitempty"`
	BaselineRef string            `json:"baseline_ref,omitempty"`
	Outputs     map[string]string `json:"outputs"`
	// Seed fixes the deterministic case sample. Empty derives a stable default from
	// the suite identity, so re-runs of the same suite version judge the SAME subset
	// (and hit the verdict cache).
	Seed string `json:"seed,omitempty"`
	// SampleSize caps how many cases are scored (0 = all). The sample is the
	// SampleSize lowest hash(seed|case_key) cases — deterministic, not random.
	SampleSize int `json:"sample_size,omitempty"`
}

// gateCalibrationDTO summarizes the calibration report the gate trusted (or why it
// trusted nothing).
type gateCalibrationDTO struct {
	ReportRef   string  `json:"report_ref,omitempty"`
	Agreement   float64 `json:"agreement,omitempty"`
	Kappa       float64 `json:"kappa,omitempty"`
	MeetsTarget bool    `json:"meets_target"`
}

// gateCorrectedDTO is the Rogan–Gladen bias-corrected pass-rate estimate (Lee et
// al., ICML 2026): the raw judge-measured pass-rate corrected by the judge's
// measured sensitivity/specificity from the trusted calibration report. Surfaced
// alongside — never instead of — the raw rate.
type gateCorrectedDTO struct {
	PassRate    float64 `json:"pass_rate"`
	Sensitivity float64 `json:"sensitivity"`
	Specificity float64 `json:"specificity"`
}

type gateDTO struct {
	ID         string   `json:"id"`
	SuiteRef   string   `json:"suite_ref"`
	SubjectRef string   `json:"subject_ref,omitempty"`
	Verdict    string   `json:"verdict"`
	Reasons    []string `json:"reasons,omitempty"`
	// EffectiveVerdict is what CI must act on: the verdict, or "pass" after a
	// governed override.
	EffectiveVerdict  string              `json:"effective_verdict"`
	RunRef            string              `json:"run_ref,omitempty"`
	BaselineRef       string              `json:"baseline_ref,omitempty"`
	Sampled           int64               `json:"sampled"`
	TotalCases        int64               `json:"total_cases"`
	Seed              string              `json:"seed,omitempty"`
	JudgeModel        string              `json:"judge_model,omitempty"`
	Calibration       *gateCalibrationDTO `json:"calibration,omitempty"`
	CorrectedPassRate *gateCorrectedDTO   `json:"corrected_pass_rate,omitempty"`
	CacheHits         int                 `json:"cache_hits,omitempty"`
	Run               *runDTO             `json:"run,omitempty"`
	Overridden        bool                `json:"overridden"`
	OverrideBy        string              `json:"override_by,omitempty"`
	OverrideReason    string              `json:"override_reason,omitempty"`
	LaunchedBy        string              `json:"launched_by,omitempty"`
	OccurredAt        string              `json:"occurred_at"`
}

func toGateDTO(rec model.Record) gateDTO {
	dto := gateDTO{
		ID: rec.String(model.ColID), SuiteRef: rec.String(colSuiteRef), SubjectRef: rec.String(colSubjectRef),
		Verdict: rec.String(colVerdict), Reasons: decodeJSONStrings(rec.String(colReasons)),
		RunRef: rec.String(colRunRef), BaselineRef: rec.String(colBaselineRef),
		Sampled: rec.Int(colSampled), TotalCases: rec.Int(colTotal), Seed: rec.String(colSeed),
		JudgeModel: rec.String(colJudgeModel), Overridden: rec.Bool(colOverridden),
		OverrideBy: rec.String(colOverrideBy), OverrideReason: rec.String(colOverrideReason),
		LaunchedBy: rec.String(colLaunchedBy), OccurredAt: rec.String(colOccurredAt),
	}
	if ref := rec.String(colCalibRef); ref != "" {
		dto.Calibration = &gateCalibrationDTO{ReportRef: ref, MeetsTarget: true}
	}
	dto.EffectiveVerdict = effectiveVerdict(dto.Verdict, dto.Overridden)
	return dto
}

// effectiveVerdict is what CI acts on: an override turns fail/warn into pass.
func effectiveVerdict(verdict string, overridden bool) string {
	if overridden {
		return verdictPass
	}
	return verdict
}

// handleGate evaluates the gate. Three-phase like every judged path: read (suite,
// cases, calibration, cache), score outside any transaction, persist atomically.
// Write-tier + self-audited.
func (m *Module) handleGate(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	var req gateRequest
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

	// Phase 1: read everything the verdict depends on.
	var suite suiteDTO
	var cases []caseDTO
	var calib calibReportDTO
	calibFound := false
	found := false
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		s, cs, ok, lerr := loadSuiteAndCases(r.Context(), sc, suiteID)
		if lerr != nil {
			return lerr
		}
		suite, cases, found = s, cs, ok
		if !ok {
			return nil
		}
		if s.Scorer == scorerLLMJudge {
			c, cok, cerr := latestCalibration(r.Context(), sc, s.JudgeModel)
			if cerr != nil {
				return cerr
			}
			calib, calibFound = c, cok
		}
		return nil
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if !found {
		writeJSON(w, http.StatusNotFound, errorBody("suite not found"))
		return
	}

	seed := firstNonEmpty(clamp(strings.TrimSpace(req.Seed), maxNameLen), suiteID.String()+"@"+formatInt(suite.SuiteVersion))
	sampled := sampleCases(cases, seed, req.SampleSize)

	judged := suite.Scorer == scorerLLMJudge
	_, judgeOffline := m.judge.(offlineJudge)

	var reasons []string
	out := gateDTO{
		SuiteRef: suiteID.String(), SubjectRef: clamp(req.SubjectRef, maxRefLen),
		Sampled: int64(len(sampled)), TotalCases: int64(len(cases)), Seed: seed,
		JudgeModel: suite.JudgeModel, LaunchedBy: mc.Principal.Actor(),
	}

	// Budget pre-flight (decision 3): only a judged gate spends. A blocked budget
	// refuses to spend AND fails the gate (deny-closed); a throttle warns without
	// spending. A FinOps read error never decides (the adapter is fail-open).
	budgetStopped := false
	if judged && !judgeOffline {
		dec, berr := m.budget.Check(r.Context(), mc.Tenant, BudgetDims{JudgeModelRef: suite.JudgeModel})
		if berr != nil {
			m.debugf("evals: gate budget check failed (fail-open)", "err", berr)
		} else if !dec.Allowed {
			budgetStopped = true
			if dec.Action == "throttle" {
				reasons = append(reasons, reasonBudgetThrottled)
				out.Verdict = verdictWarn
			} else {
				reasons = append(reasons, reasonBudgetBlocked)
				out.Verdict = verdictFail
			}
		}
	}

	var agg runAggregate
	var cache *cachedJudge
	if !budgetStopped {
		// Phase 2: score the sampled subset outside any transaction. A judged gate
		// goes through the verdict cache (prefetched in a read tx).
		scorer := m.scorerByID(suite.Scorer)
		if judged && !judgeOffline {
			cache = newCachedJudge(m.judge, suite, judgeCacheVersion)
			if err := mc.Data.View(r.Context(), func(sc store.Scope) error {
				return cache.prefetch(r.Context(), sc, sampled, clampOutputs(req.Outputs))
			}); err != nil {
				writeStoreError(w, err)
				return
			}
			scorer = &judgeScorer{judge: func() Judge { return cache }}
		}
		agg = m.executeRun(r.Context(), mc.Tenant, suite, sampled, clampOutputs(req.Outputs), scorer)
		if cache != nil {
			out.CacheHits = cache.hits
		}
	}

	// Phase 3: persist the run (when one executed), the gate row and the audit
	// atomically; derive the verdict from what was MEASURED.
	now := m.clock.Now()
	var reg regressionInfo
	err = mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		gateRec := model.Record{
			colSuiteRef: suiteID.String(), colSubjectRef: out.SubjectRef,
			colSampled: int64(len(sampled)), colTotal: int64(len(cases)), colSeed: seed,
			colOverridden: false, colLaunchedBy: mc.Principal.Actor(), colOccurredAt: now.String(),
		}
		if suite.JudgeModel != "" {
			gateRec[colJudgeModel] = suite.JudgeModel
		}

		if !budgetStopped {
			subj := runSubject{
				suiteRef: suiteID.String(), suiteVer: suite.SuiteVersion,
				subjectKind: firstNonEmpty(req.SubjectKind, suite.SubjectKind), subjectRef: req.SubjectRef,
				baselineRef: req.BaselineRef, launchedBy: mc.Principal.Actor(),
			}
			dto, ri, perr := m.persistRun(r.Context(), sc, suite, subj, agg)
			if perr != nil {
				return perr
			}
			reg = ri
			out.Run = &dto
			out.RunRef = dto.ID
			out.BaselineRef = dto.BaselineRef
			gateRec[colRunRef] = dto.ID
			if dto.BaselineRef != "" {
				gateRec[colBaselineRef] = dto.BaselineRef
			}
			if cache != nil {
				if cerr := cache.persistMisses(r.Context(), sc, now.String()); cerr != nil {
					return cerr
				}
			}

			verdict, vreasons := gateVerdict(suite, agg, ri, judged, judgeOffline, calib, calibFound)
			reasons = append(reasons, vreasons...)
			out.Verdict = verdict

			if judged && calibFound && calib.MeetsTarget {
				out.Calibration = &gateCalibrationDTO{
					ReportRef: calib.ID, Agreement: calib.Agreement, Kappa: calib.Kappa, MeetsTarget: true,
				}
				gateRec[colCalibRef] = calib.ID
				if agg.passed+agg.failed > 0 {
					if theta, tok := roganGladen(agg.passRate, calib.Sensitivity, calib.Specificity); tok {
						out.CorrectedPassRate = &gateCorrectedDTO{
							PassRate: theta, Sensitivity: calib.Sensitivity, Specificity: calib.Specificity,
						}
					}
				}
			}
		}

		gateRec[colVerdict] = out.Verdict
		if enc := encodeJSONStrings(reasons); enc != nil {
			gateRec[colReasons] = enc
		}
		repo, err := sc.Ext(gateKind)
		if err != nil {
			return err
		}
		created, err := repo.Create(r.Context(), gateRec)
		if err != nil {
			return err
		}
		out.ID = created.String(model.ColID)
		out.OccurredAt = created.String(colOccurredAt)
		return auditEvent(r.Context(), sc, mc, "evals.gate.run", gateKind, model.ID(out.ID), map[string]any{
			"suite_ref": suiteID.String(), "subject_ref": out.SubjectRef, "verdict": out.Verdict,
			"reasons": reasons, "sampled": len(sampled), "total_cases": len(cases), "cache_hits": out.CacheHits,
		})
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	out.Reasons = reasons
	out.EffectiveVerdict = effectiveVerdict(out.Verdict, false)
	m.emitRegression(r.Context(), mc.Tenant, suite, reg)
	writeJSON(w, http.StatusCreated, out)
}

// gateVerdict derives the blocking decision from the measured run. Every applicable
// reason is reported (a CI log should show the whole picture, not the first hit).
func gateVerdict(suite suiteDTO, agg runAggregate, reg regressionInfo, judged, judgeOffline bool, calib calibReportDTO, calibFound bool) (string, []string) {
	var fails, warns, notes []string

	if judged && judgeOffline {
		// Decision 3: honest degradation — declared, never silent, never a block on
		// a verdict nobody rendered.
		warns = append(warns, reasonNoJudge)
	}
	if judged && !judgeOffline {
		switch {
		case !calibFound:
			fails = append(fails, reasonUncalibrated)
		case !calib.MeetsTarget:
			fails = append(fails, reasonCalibBelowTarget)
		}
	}
	if agg.status == "error" {
		fails = append(fails, reasonRunError)
	}
	if reg.regressed {
		fails = append(fails, reasonRegression)
	}
	if scored := agg.passed + agg.failed; scored > 0 && agg.passRate < suite.PassThreshold {
		fails = append(fails, reasonBelowThreshold)
	}
	if reg.baselineRef == "" {
		notes = append(notes, reasonNoBaseline)
	}

	switch {
	case len(fails) > 0:
		return verdictFail, append(fails, append(warns, notes...)...)
	case len(warns) > 0:
		return verdictWarn, append(warns, notes...)
	default:
		return verdictPass, notes
	}
}

// sampleCases returns the deterministic seeded subset: the k cases with the lowest
// hash(seed|case_key), in case_key order. k<=0 or k>=len returns all cases. The
// subset is FIXED for a given (seed, case set) — a rerun scores the same cases and
// hits the verdict cache.
func sampleCases(cases []caseDTO, seed string, k int) []caseDTO {
	if k <= 0 || k >= len(cases) {
		return cases
	}
	ranked := make([]caseDTO, len(cases))
	copy(ranked, cases)
	sort.Slice(ranked, func(i, j int) bool {
		return hashHex(seed+"|"+ranked[i].CaseKey) < hashHex(seed+"|"+ranked[j].CaseKey)
	})
	ranked = ranked[:k]
	sort.Slice(ranked, func(i, j int) bool { return ranked[i].CaseKey < ranked[j].CaseKey })
	return ranked
}

func formatInt(v int64) string {
	return strconv.FormatInt(v, 10)
}

// ---- verdict cache -----------------------------------------------------------------

// cachedJudge wraps the real Judge with the persisted verdict cache: a hit returns
// the stored verdict without a model call; a miss calls through and records the
// verdict for the final transaction to persist. Single-goroutine (executeRun is
// sequential), no locking.
type cachedJudge struct {
	inner      Judge
	version    string
	judgeModel string
	criterion  string // the suite-level rubric, part of every key
	prefetched map[string]JudgeVerdict
	misses     map[string]JudgeVerdict
	hits       int
}

func newCachedJudge(inner Judge, suite suiteDTO, version string) *cachedJudge {
	return &cachedJudge{
		inner: inner, version: version, judgeModel: suite.JudgeModel, criterion: suite.Criterion,
		prefetched: map[string]JudgeVerdict{}, misses: map[string]JudgeVerdict{},
	}
}

// cacheKey derives the verdict-cache key for one judging. The judge model PIN and
// the prompt version are inside the hash: a model or prompt change can never serve
// a stale verdict.
func (c *cachedJudge) cacheKey(input, output, expected, criterion string) string {
	return hashHex(c.version + "|" + c.judgeModel + "|" + criterion + "|" + input + "|" + expected + "|" + output)
}

// prefetch loads the cached verdicts for the sampled cases in one read transaction
// (per-key lookups on the unique index; gate samples are small by design).
func (c *cachedJudge) prefetch(ctx context.Context, sc store.Scope, cases []caseDTO, outputs map[string]string) error {
	repo, err := sc.Ext(cacheKind)
	if err != nil {
		return err
	}
	for _, cs := range cases {
		output, ok := outputs[cs.CaseKey]
		if !ok {
			continue
		}
		key := c.cacheKey(cs.Input, output, cs.Expected, c.criterion)
		recs, err := listAll(ctx, repo, eq(colInputHash, key))
		if err != nil {
			return err
		}
		if len(recs) == 0 {
			continue
		}
		rec := recs[0]
		c.prefetched[key] = JudgeVerdict{
			Score: rec.Float(colResScore), Passed: rec.Bool(colPassedFlag), Reason: rec.String(colReason),
		}
	}
	return nil
}

func (c *cachedJudge) Judge(ctx context.Context, tenant model.TenantID, req JudgeRequest) (JudgeVerdict, error) {
	key := c.cacheKey(req.Input, req.Output, req.Expected, req.Criterion)
	if v, ok := c.prefetched[key]; ok {
		c.hits++
		return v, nil
	}
	v, err := c.inner.Judge(ctx, tenant, req)
	if err != nil {
		return v, err // errors are never cached — a transient fault must retry
	}
	c.misses[key] = v
	return v, nil
}

// persistMisses writes the fresh verdicts inside the caller's transaction. A
// conflict (another gate cached the same key concurrently) is benign.
func (c *cachedJudge) persistMisses(ctx context.Context, sc store.Scope, occurredAt string) error {
	if len(c.misses) == 0 {
		return nil
	}
	repo, err := sc.Ext(cacheKind)
	if err != nil {
		return err
	}
	for key, v := range c.misses {
		if _, err := repo.Create(ctx, model.Record{
			colInputHash: key, colJudgeModel: c.judgeModel,
			colResScore: v.Score, colPassedFlag: v.Passed, colReason: clamp(v.Reason, maxLabelLen),
			colOccurredAt: occurredAt,
		}); err != nil && !isConflict(err) {
			return err
		}
	}
	return nil
}

// ---- gate reads + override -----------------------------------------------------------

// handleListGates lists gate evaluations (?suite_ref=, ?verdict=).
func (m *Module) handleListGates(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	q := listQuery(r)
	if v := strings.TrimSpace(r.URL.Query().Get("suite_ref")); v != "" {
		q.Filters = append(q.Filters, eq(colSuiteRef, v))
	}
	if v := strings.TrimSpace(r.URL.Query().Get("verdict")); v != "" {
		q.Filters = append(q.Filters, eq(colVerdict, v))
	}
	out := listResponse[gateDTO]{Items: []gateDTO{}}
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(gateKind)
		if err != nil {
			return err
		}
		recs, page, err := repo.List(r.Context(), q)
		if err != nil {
			return err
		}
		for _, rec := range recs {
			out.Items = append(out.Items, toGateDTO(rec))
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

// handleGetGate returns one gate evaluation (CI re-checks it after an override).
func (m *Module) handleGetGate(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id, ok := idParam(chi.URLParam(r, "id"))
	if !ok {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid gate_id"))
		return
	}
	var out gateDTO
	found := false
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(gateKind)
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
		found, out = true, toGateDTO(rec)
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

type overrideGateRequest struct {
	Reason string `json:"reason"`
}

// handleOverrideGate is the GOVERNED escape hatch: an admin overrides a failed (or
// warned) gate WITH a written reason; the override is recorded on the row and in
// the audit ledger with the original verdict. A passing gate cannot be "overridden"
// (nothing to escape) and an override is never undone via this surface — re-run the
// gate instead. Admin-tier.
func (m *Module) handleOverrideGate(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id, ok := idParam(chi.URLParam(r, "id"))
	if !ok {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid gate_id"))
		return
	}
	var req overrideGateRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	reason := clamp(strings.TrimSpace(req.Reason), maxNameLen)
	if reason == "" {
		writeJSON(w, http.StatusBadRequest, errorBody("a written reason is required to override a gate"))
		return
	}

	var out gateDTO
	notFound, conflict := false, ""
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(gateKind)
		if err != nil {
			return err
		}
		rec, err := repo.Get(r.Context(), id)
		if err != nil {
			if isNotFound(err) {
				notFound = true
				return nil
			}
			return err
		}
		switch {
		case rec.Bool(colOverridden):
			conflict = "gate is already overridden"
			return nil
		case rec.String(colVerdict) == verdictPass:
			conflict = "gate already passes — nothing to override"
			return nil
		}
		rec[colOverridden] = true
		rec[colOverrideBy] = mc.Principal.Actor()
		rec[colOverrideReason] = reason
		updated, err := repo.Update(r.Context(), rec)
		if err != nil {
			return err
		}
		out = toGateDTO(updated)
		return auditEvent(r.Context(), sc, mc, "evals.gate.override", gateKind, id, map[string]any{
			"suite_ref": out.SuiteRef, "original_verdict": out.Verdict, "reason": reason,
		})
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if notFound {
		writeJSON(w, http.StatusNotFound, errorBody("not found"))
		return
	}
	if conflict != "" {
		writeJSON(w, http.StatusConflict, errorBody(conflict))
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// ---- small JSON helpers --------------------------------------------------------------

// encodeJSONStrings encodes a string slice for a KindJSON column (nil when empty).
func encodeJSONStrings(v []string) any {
	if len(v) == 0 {
		return nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	return string(b)
}

// decodeJSONStrings decodes a KindJSON column back to a string slice (nil on empty).
func decodeJSONStrings(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	var out []string
	if err := json.Unmarshal([]byte(s), &out); err != nil {
		return nil
	}
	return out
}
