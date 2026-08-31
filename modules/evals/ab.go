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
)

// POST /ab scores TWO output sets (variant A and variant B) against the SAME suite,
// producing two runs and a comparison ordered by score (contract §2.5). It is the
// prompt-variant comparison renders. Write-tier + self-audited.
//
// Adds the OPT-IN bias-mitigated judged comparison ("pairwise": true): each
// case present in both variants is judged head-to-head TWICE with the candidates'
// presentation order swapped, and a win is declared only when both orders agree
// (Zheng et al. 2023 — position-bias mitigation). An order-inconsistent dual counts
// as a tie AND is reported as inconsistent, so the response carries the measured
// position-consistency rate instead of hiding the bias. Without a wired PairJudge
// the block is a DECLARED skip, never a fabricated winner.

type abVariantInput struct {
	Label   string            `json:"label"`
	Outputs map[string]string `json:"outputs"`
}

type abRequest struct {
	SuiteRef    string         `json:"suite_ref"`
	SubjectKind string         `json:"subject_kind,omitempty"`
	SubjectRef  string         `json:"subject_ref,omitempty"`
	A           abVariantInput `json:"a"`
	B           abVariantInput `json:"b"`
	// Pairwise opts into the order-swapped judged comparison (two judge calls per
	// shared case — billable; the suite must carry a criterion for the judge).
	Pairwise bool `json:"pairwise,omitempty"`
}

type abVariantResult struct {
	Label    string  `json:"label"`
	RunRef   string  `json:"run_ref"`
	Score    float64 `json:"score"`
	PassRate float64 `json:"pass_rate"`
}

// abPairwiseDTO is the judged-comparison block. Mode is "judged" or "skipped"
// (skip_reason says why — no pair judge wired, or no shared cases). Winner is the
// variant label with more order-consistent wins ("" on a tie). The counts expose the
// raw tallies; position_consistency is the fraction of judged duals where both
// presentation orders agreed — the measured position-bias signal.
type abPairwiseDTO struct {
	Mode                string        `json:"mode"`
	SkipReason          string        `json:"skip_reason,omitempty"`
	Winner              string        `json:"winner,omitempty"`
	Compared            int           `json:"compared"`
	AWins               int           `json:"a_wins"`
	BWins               int           `json:"b_wins"`
	Ties                int           `json:"ties"`
	Inconsistent        int           `json:"inconsistent"`
	Errors              int           `json:"errors"`
	PositionConsistency *measuredRate `json:"position_consistency,omitempty"`
}

type abResponse struct {
	Variants []abVariantResult `json:"variants"`
	Winner   string            `json:"winner"`
	Delta    float64           `json:"delta"`
	Tie      bool              `json:"tie"`
	Pairwise *abPairwiseDTO    `json:"pairwise,omitempty"`
}

// handleAB loads the suite, scores both variants OUTSIDE the write transaction
// (two-phase — judge I/O never holds the store), optionally runs the
// order-swapped pairwise comparison, then persists both runs atomically.
func (m *Module) handleAB(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	var req abRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	suiteID, ok := idParam(req.SuiteRef)
	if !ok {
		writeJSON(w, http.StatusBadRequest, errorBody("suite_ref is required"))
		return
	}
	if req.A.Outputs == nil || req.B.Outputs == nil {
		writeJSON(w, http.StatusBadRequest, errorBody("both a.outputs and b.outputs are required"))
		return
	}
	labelA := firstNonEmpty(clamp(strings.TrimSpace(req.A.Label), maxNameLen), "A")
	labelB := firstNonEmpty(clamp(strings.TrimSpace(req.B.Label), maxNameLen), "B")

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

	outsA, outsB := clampOutputs(req.A.Outputs), clampOutputs(req.B.Outputs)
	scorer := m.scorerByID(suite.Scorer)
	aggA := m.executeRun(r.Context(), mc.Tenant, suite, cases, outsA, scorer)
	aggB := m.executeRun(r.Context(), mc.Tenant, suite, cases, outsB, scorer)

	var pairwise *abPairwiseDTO
	if req.Pairwise {
		pw := m.judgePairwise(r.Context(), mc.Tenant, suite, cases, labelA, outsA, labelB, outsB)
		pairwise = &pw
	}

	var resA, resB abVariantResult
	var regA, regB regressionInfo
	err = mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		var rerr error
		resA, regA, rerr = m.persistVariant(r.Context(), mc, sc, suite, suiteID, req.SubjectKind, req.SubjectRef, labelA, aggA)
		if rerr != nil {
			return rerr
		}
		resB, regB, rerr = m.persistVariant(r.Context(), mc, sc, suite, suiteID, req.SubjectKind, req.SubjectRef, labelB, aggB)
		return rerr
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	// A repeated A/B run with the same label can regress vs its prior run; emit the
	// best-effort bus signal for each variant AFTER the commit, like handleLaunchRun
	// (the core Finding was already persisted inside persistRun).
	m.emitRegression(r.Context(), mc.Tenant, suite, regA)
	m.emitRegression(r.Context(), mc.Tenant, suite, regB)

	out := abResponse{Variants: []abVariantResult{resA, resB}, Pairwise: pairwise}
	// Order variants by score descending; the winner is the higher-scoring variant.
	if resB.Score > resA.Score {
		out.Variants = []abVariantResult{resB, resA}
	}
	out.Delta = out.Variants[0].Score - out.Variants[1].Score
	if out.Delta == 0 {
		out.Tie = true
		out.Winner = ""
	} else {
		out.Winner = out.Variants[0].Label
	}
	writeJSON(w, http.StatusOK, out)
}

// persistVariant persists one already-scored variant run (in the open transaction)
// and self-audits the launch.
func (m *Module) persistVariant(ctx context.Context, mc api.ModuleContext, sc store.Scope, suite suiteDTO, suiteID model.ID, subjectKind, subjectRef, label string, agg runAggregate) (abVariantResult, regressionInfo, error) {
	subj := runSubject{
		suiteRef: suiteID.String(), suiteVer: suite.SuiteVersion,
		subjectKind: firstNonEmpty(subjectKind, suite.SubjectKind), subjectRef: subjectRef,
		variant: label, launchedBy: mc.Principal.Actor(),
	}
	dto, reg, err := m.persistRun(ctx, sc, suite, subj, agg)
	if err != nil {
		return abVariantResult{}, regressionInfo{}, err
	}
	if err := auditEvent(ctx, sc, mc, "evals.ab.score", runKind, model.ID(dto.ID), map[string]any{
		"suite_ref": suiteID.String(), "variant": label, "score": agg.score, "pass_rate": agg.passRate,
	}); err != nil {
		return abVariantResult{}, regressionInfo{}, err
	}
	return abVariantResult{Label: label, RunRef: dto.ID, Score: agg.score, PassRate: agg.passRate}, reg, nil
}

// judgePairwise runs the order-swapped judged comparison over every case both
// variants answered. Per case it asks the PairJudge twice — (A,B) then (B,A) — and
// declares a per-case winner ONLY when both orders agree; a disagreement counts as a
// tie and increments the inconsistency tally (the measured position bias). The judge
// reasons are consumed in flight and dropped: only counts leave this function.
func (m *Module) judgePairwise(ctx context.Context, tenant model.TenantID, suite suiteDTO, cases []caseDTO, labelA string, outsA map[string]string, labelB string, outsB map[string]string) abPairwiseDTO {
	if _, off := m.pairJudge.(offlinePairJudge); off {
		return abPairwiseDTO{Mode: "skipped", SkipReason: "no pair judge wired — pairwise comparison not executed"}
	}
	out := abPairwiseDTO{Mode: "judged"}
	consistent := 0
	for _, c := range cases {
		oa, okA := outsA[c.CaseKey]
		ob, okB := outsB[c.CaseKey]
		if !okA || !okB {
			continue // only cases both variants answered are comparable
		}
		out.Compared++
		// Dual 1: A presented first. Dual 2: order swapped.
		v1, err1 := m.pairJudge.JudgePair(ctx, tenant, PairRequest{
			ModelRef: suite.JudgeModel, Input: c.Input, Criterion: suite.Criterion,
			OutputFirst: oa, OutputSecond: ob,
		})
		v2, err2 := m.pairJudge.JudgePair(ctx, tenant, PairRequest{
			ModelRef: suite.JudgeModel, Input: c.Input, Criterion: suite.Criterion,
			OutputFirst: ob, OutputSecond: oa,
		})
		if errors.Is(err1, errNoJudge) || errors.Is(err2, errNoJudge) {
			return abPairwiseDTO{Mode: "skipped", SkipReason: "no pair judge wired — pairwise comparison not executed"}
		}
		if err1 != nil || err2 != nil {
			out.Errors++
			continue
		}
		// Map positions back to variant labels.
		w1 := pairPosToLabel(v1.Winner, labelA, labelB) // A was first
		w2 := pairPosToLabel(v2.Winner, labelB, labelA) // B was first
		if w1 != w2 {
			// Order-dependent verdict: position bias surfaced. Tie, counted.
			out.Inconsistent++
			out.Ties++
			continue
		}
		consistent++
		switch w1 {
		case labelA:
			out.AWins++
		case labelB:
			out.BWins++
		default:
			out.Ties++
		}
	}
	if judged := out.Compared - out.Errors; judged > 0 {
		rate := float64(consistent) / float64(judged)
		lo, hi := wilsonInterval(consistent, judged)
		out.PositionConsistency = &measuredRate{Rate: rate, N: judged, CI: ciDTO{Lo: lo, Hi: hi}}
	}
	switch {
	case out.AWins > out.BWins:
		out.Winner = labelA
	case out.BWins > out.AWins:
		out.Winner = labelB
	}
	return out
}

// pairPosToLabel maps a positional pair verdict (first|second|tie) onto the variant
// labels given which variant was presented first/second in that dual.
func pairPosToLabel(winner, firstLabel, secondLabel string) string {
	switch winner {
	case PairWinnerFirst:
		return firstLabel
	case PairWinnerSecond:
		return secondLabel
	default:
		return ""
	}
}

// measuredRate is a rate reported WITH its denominator and 95% Wilson interval —
// the reporting convention (a rate without an n is not a defensible claim).
type measuredRate struct {
	Rate float64 `json:"rate"`
	N    int     `json:"n"`
	CI   ciDTO   `json:"ci"`
}
