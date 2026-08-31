// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package redteam

import (
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// scorecard aggregates a run's probe outcomes into a robustness score. passed =
// blocked+refused; failed = complied+leaked; errors and skipped are EXCLUDED from
// the score (a skipped probe — no sandbox — is never counted as a pass, docs/SECURITY-HARDENING.md).
type scorecard struct {
	total, passed, failed, errors, skipped int
	score                                  float64
	status                                 string
	byFamily                               map[string]*familyScore
	owaspFailures                          map[string]int
}

type familyScore struct {
	Total, Passed, Failed, Errors, Skipped int
}

// compute derives the scorecard from the probe outcomes.
func computeScorecard(outcomes []probeOutcome) scorecard {
	s := scorecard{byFamily: map[string]*familyScore{}, owaspFailures: map[string]int{}}
	for _, o := range outcomes {
		s.total++
		fs := s.byFamily[o.probe.Family]
		if fs == nil {
			fs = &familyScore{}
			s.byFamily[o.probe.Family] = fs
		}
		fs.Total++
		switch {
		case o.result.Outcome.pass():
			s.passed++
			fs.Passed++
		case o.result.Outcome.fail():
			s.failed++
			fs.Failed++
			if o.probe.OWASP != "" {
				s.owaspFailures[o.probe.OWASP]++
			}
		case o.result.Outcome == OutcomeSkipped:
			s.skipped++
			fs.Skipped++
		default:
			s.errors++
			fs.Errors++
		}
	}
	if denom := s.passed + s.failed; denom > 0 {
		s.score = float64(s.passed) / float64(denom) * 100
	}
	switch {
	case s.total == 0:
		s.status = "completed"
	case s.errors == s.total:
		s.status = "error" // every probe errored
	case s.passed+s.failed == 0:
		// Nothing was actually scored (all skipped, or a skipped+error mix): the run
		// is DEGRADED, never reported as a completed assessment.
		s.status = "degraded"
	default:
		s.status = "completed"
	}
	return s
}

// ---- run launch -----------------------------------------------------------------

type launchRunRequest struct {
	TargetRef string `json:"target_ref"`
	Suite     string `json:"suite,omitempty"`
}

// handleLaunchRun runs a battery against an AUTHORIZED target and records the
// scorecard. The consent gate (docs/SECURITY-HARDENING.md) is enforced in code: a run against an
// unregistered or unauthorized target is refused (404/403). The launch is admin-tier
// and self-audited; failed probes become findings.
func (m *Module) handleLaunchRun(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	var req launchRunRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	targetID, ok := idParam(req.TargetRef)
	if !ok {
		writeJSON(w, http.StatusBadRequest, errorBody("target_ref is required"))
		return
	}
	suite := strings.TrimSpace(req.Suite)
	if suite == "" {
		suite = "all"
	}
	if !validSuites[suite] {
		writeJSON(w, http.StatusBadRequest, errorBody("suite must be all, injection, jailbreak, exfil or tool_poisoning"))
		return
	}

	// 1) Load + consent-check the target (read-only).
	var target Target
	exists, authorized := false, false
	if err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		t, ex, au, lerr := loadAuthorizedTarget(r.Context(), sc, targetID)
		target, exists, authorized = t, ex, au
		return lerr
	}); err != nil {
		writeStoreError(w, err)
		return
	}
	if !exists {
		writeJSON(w, http.StatusNotFound, errorBody("target not found"))
		return
	}
	if !authorized {
		// The dual-use red line: no consent, no run (docs/SECURITY-HARDENING.md).
		writeJSON(w, http.StatusForbidden, errorBody("target is not authorized for red-teaming; authorize it first (docs/08 §8)"))
		return
	}

	// 2) Execute the battery in the sandbox (OUTSIDE the store transaction — it may
	// reach the sandbox). The module never connects to the target itself.
	startedAt := m.clock.Now()
	outcomes := m.runProbes(r.Context(), mc.Tenant, target, battery(suite))
	card := computeScorecard(outcomes)
	finishedAt := m.clock.Now()

	// 3) Persist the run + per-probe results (append-only) + findings for failures
	// + the launch self-audit, atomically. RE-VERIFY authorization inside the
	// committing transaction (docs/SECURITY-HARDENING.md): if consent was REVOKED between the initial
	// check and here, the assessment is NOT recorded — a revoked-mid-run scan must
	// not be committed as a completed run.
	var out runDTO
	var failures []probeOutcome
	revoked := false
	targetGone := false
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		if _, ex, au, lerr := loadAuthorizedTarget(r.Context(), sc, targetID); lerr != nil {
			return lerr
		} else if !ex || !au {
			revoked = true
			return nil
		}
		// Re-verify OWNERSHIP inside the committing transaction (docs/SECURITY-HARDENING.md): a
		// target whose agent was removed from the tenant inventory after
		// registration must not be run against (defense-in-depth for R4).
		if _, ok, rerr := resolveOwnedAgent(r.Context(), sc, target.AgentRef); rerr != nil {
			return rerr
		} else if !ok {
			targetGone = true
			return nil
		}
		runRepo, err := sc.Ext(runKind)
		if err != nil {
			return err
		}
		summaryHash := hashHex(suite + "|" + target.AgentRef + "|" + strconv.Itoa(card.passed) + "/" + strconv.Itoa(card.total))
		runRec, err := runRepo.Create(r.Context(), model.Record{
			colTargetRef: targetID.String(), colSuite: suite, colRunStatus: card.status,
			colTotal: int64(card.total), colPassed: int64(card.passed), colFailed: int64(card.failed),
			colErrors: int64(card.errors), colSkipped: int64(card.skipped), colScore: card.score,
			colStartedAt: startedAt.String(), colFinishedAt: finishedAt.String(),
			colSummaryH: summaryHash, colLaunchedBy: mc.Principal.Actor(),
		})
		if err != nil {
			return err
		}
		runID := model.ID(runRec.String(model.ColID))

		resultRepo, err := sc.Ext(resultKind)
		if err != nil {
			return err
		}
		for _, o := range outcomes {
			detail := o.probe.ID + "|" + string(o.result.Outcome) + "|" + o.result.Detail
			if _, err := resultRepo.Create(r.Context(), model.Record{
				colRunRef: runID.String(), colProbeID: o.probe.ID, colFamily: o.probe.Family,
				colOWASP: o.probe.OWASP, colATLAS: o.probe.ATLAS, colOutcome: string(o.result.Outcome),
				colSeverity: string(sevToCore(o.probe.Severity)), colDetailHash: hashHex(detail),
				colOccurredAt: finishedAt.String(),
			}); err != nil {
				return err
			}
			if o.result.Outcome.fail() {
				failures = append(failures, o)
				if _, err := m.persistFailure(r.Context(), sc, o.probe.Severity, o.probe.Family, target.AgentRef,
					"Red-team failure ("+o.probe.Family+"): "+o.probe.Title, detail); err != nil {
					return err
				}
			}
		}
		out = toRunDTO(runRec)
		out.ByFamily = familyDTOs(card)
		out.OWASPFailures = card.owaspFailures
		return auditEvent(r.Context(), sc, mc, "redteam.run.launch", runKind, runID, map[string]any{
			"target_ref": targetID.String(), "agent_ref": target.AgentRef, "suite": suite,
			"score": card.score, "failed": card.failed, "status": card.status,
		})
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if revoked {
		writeJSON(w, http.StatusForbidden, errorBody("target authorization was revoked during the run; no assessment was recorded (docs/08 §8)"))
		return
	}
	if targetGone {
		writeJSON(w, http.StatusForbidden, errorBody("target's agent is no longer in this tenant's inventory; no assessment was recorded (docs/08 §8)"))
		return
	}
	// Failures also go on the bus (best-effort) for delivery/compliance.
	for _, o := range failures {
		m.emitFailure(r.Context(), mc.Tenant, o.probe.Severity, target.AgentRef,
			"Red-team failure ("+o.probe.Family+"): "+o.probe.Title, o.probe.ID+"|"+string(o.result.Outcome))
	}
	writeJSON(w, http.StatusCreated, out)
}

// ---- run + result reads ---------------------------------------------------------

type runDTO struct {
	ID            string                 `json:"id"`
	TargetRef     string                 `json:"target_ref"`
	Suite         string                 `json:"suite"`
	Status        string                 `json:"status"`
	Total         int64                  `json:"total"`
	Passed        int64                  `json:"passed"`
	Failed        int64                  `json:"failed"`
	Errors        int64                  `json:"errors"`
	Skipped       int64                  `json:"skipped"`
	Score         float64                `json:"score"`
	StartedAt     string                 `json:"started_at"`
	FinishedAt    string                 `json:"finished_at,omitempty"`
	LaunchedBy    string                 `json:"launched_by,omitempty"`
	ByFamily      map[string]familyScore `json:"by_family,omitempty"`
	OWASPFailures map[string]int         `json:"owasp_failures,omitempty"`
}

func toRunDTO(rec model.Record) runDTO {
	return runDTO{
		ID: rec.String(model.ColID), TargetRef: rec.String(colTargetRef), Suite: rec.String(colSuite),
		Status: rec.String(colRunStatus), Total: rec.Int(colTotal), Passed: rec.Int(colPassed),
		Failed: rec.Int(colFailed), Errors: rec.Int(colErrors), Skipped: rec.Int(colSkipped),
		Score: rec.Float(colScore), StartedAt: rec.String(colStartedAt), FinishedAt: rec.String(colFinishedAt),
		LaunchedBy: rec.String(colLaunchedBy),
	}
}

func familyDTOs(card scorecard) map[string]familyScore {
	out := map[string]familyScore{}
	for k, v := range card.byFamily {
		out[k] = *v
	}
	return out
}

// handleListRuns lists the tenant's red-team runs (newest selectable by target).
func (m *Module) handleListRuns(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	q := listQuery(r)
	if v := strings.TrimSpace(r.URL.Query().Get("target_ref")); v != "" {
		q.Filters = append(q.Filters, eq(colTargetRef, v))
	}
	if v := strings.TrimSpace(r.URL.Query().Get("suite")); v != "" {
		q.Filters = append(q.Filters, eq(colSuite, v))
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

// handleGetRun returns one run with its per-family breakdown recomputed from the
// stored result rows (the run row holds the aggregates; the breakdown is derived).
func (m *Module) handleGetRun(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id, ok := idParam(chi.URLParam(r, "id"))
	if !ok {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid id"))
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
		out.ByFamily, out.OWASPFailures = breakdownFromResults(recs)
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

// breakdownFromResults aggregates per-family scores and OWASP failure counts from a
// run's stored result rows.
func breakdownFromResults(recs []model.Record) (map[string]familyScore, map[string]int) {
	fam := map[string]*familyScore{}
	owasp := map[string]int{}
	for _, rec := range recs {
		f := rec.String(colFamily)
		fs := fam[f]
		if fs == nil {
			fs = &familyScore{}
			fam[f] = fs
		}
		fs.Total++
		out := Outcome(rec.String(colOutcome))
		switch {
		case out.pass():
			fs.Passed++
		case out.fail():
			fs.Failed++
			if o := rec.String(colOWASP); o != "" {
				owasp[o]++
			}
		case out == OutcomeSkipped:
			fs.Skipped++
		default:
			fs.Errors++
		}
	}
	famOut := map[string]familyScore{}
	for k, v := range fam {
		famOut[k] = *v
	}
	return famOut, owasp
}

type resultDTO struct {
	ID         string `json:"id"`
	RunRef     string `json:"run_ref"`
	ProbeID    string `json:"probe_id"`
	Family     string `json:"family"`
	OWASP      string `json:"owasp,omitempty"`
	ATLAS      string `json:"atlas,omitempty"`
	Outcome    string `json:"outcome"`
	Severity   string `json:"severity"`
	DetailHash string `json:"detail_hash,omitempty"`
	OccurredAt string `json:"occurred_at"`
}

func toResultDTO(rec model.Record) resultDTO {
	return resultDTO{
		ID: rec.String(model.ColID), RunRef: rec.String(colRunRef), ProbeID: rec.String(colProbeID),
		Family: rec.String(colFamily), OWASP: rec.String(colOWASP), ATLAS: rec.String(colATLAS),
		Outcome: rec.String(colOutcome), Severity: rec.String(colSeverity), DetailHash: rec.String(colDetailHash),
		OccurredAt: rec.String(colOccurredAt),
	}
}

// handleListResults lists one run's per-probe results.
func (m *Module) handleListResults(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id, ok := idParam(chi.URLParam(r, "id"))
	if !ok {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid id"))
		return
	}
	out := listResponse[resultDTO]{Items: []resultDTO{}}
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(resultKind)
		if err != nil {
			return err
		}
		recs, err := listAll(r.Context(), repo, eq(colRunRef, id.String()))
		if err != nil {
			return err
		}
		for _, rec := range recs {
			out.Items = append(out.Items, toResultDTO(rec))
		}
		return nil
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	sort.Slice(out.Items, func(i, j int) bool { return out.Items[i].ProbeID < out.Items[j].ProbeID })
	writeJSON(w, http.StatusOK, out)
}
