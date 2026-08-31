// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package evals

import (
	"context"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// This file is the CROSS-MODULE seam (contract §4): evals exposes a PUBLIC method on
// its concrete type with evals-OWNED DTOs. The sandbox (module XVII) calls it through
// a tiny adapter that lives in the composition root — evals NEVER imports the sandbox
// (that would be the repo's first sibling production import, prohibited). Default
// without a scorer wired ⇒ a real, honest scorecard ("executed, then scored"), never
// a silent pass.

// ScoreOutputsRequest is the evals-owned input the sandbox adapter translates into.
type ScoreOutputsRequest struct {
	// SuiteRef is the suite to score against.
	SuiteRef string
	// SubjectKind/SubjectRef identify what produced the outputs (defaults to the
	// suite's subject_kind when SubjectKind is empty).
	SubjectKind string
	SubjectRef  string
	// Variant is an optional A/B label.
	Variant string
	// Actor attributes the launch in the audit (the caller's principal string).
	Actor string
	// Outputs maps case_key → candidate output (never persisted raw).
	Outputs map[string]string
}

// Scorecard is the evals-owned result the seam returns: the run aggregate plus its
// per-case scores. It carries no internal table shape so a caller (the sandbox) is
// decoupled from evals' schema.
type Scorecard struct {
	RunRef   string      `json:"run_ref"`
	Total    int         `json:"total"`
	Passed   int         `json:"passed"`
	Failed   int         `json:"failed"`
	Errors   int         `json:"errors"`
	Skipped  int         `json:"skipped"`
	Score    float64     `json:"score"`
	PassRate float64     `json:"pass_rate"`
	Status   string      `json:"status"`
	Cases    []CaseScore `json:"cases"`
}

// CaseScore is one scored case in a Scorecard (evals-owned).
type CaseScore struct {
	CaseKey string  `json:"case_key"`
	Outcome string  `json:"outcome"`
	Score   float64 `json:"score"`
	Passed  bool    `json:"passed"`
}

// ScoreOutputs runs a full eval (execute + persist + regression) over caller-supplied
// outputs and returns the scorecard. It is the seam the sandbox calls via a
// composition-root adapter. It uses m.data View/Mutate so it works for an
// event-driven caller that holds no request Scope; like the run handler it is
// two-phase — the judge's network I/O never runs inside the write
// transaction. The regression bus signal is emitted after the commit (best-effort).
func (m *Module) ScoreOutputs(ctx context.Context, tenant model.TenantID, req ScoreOutputsRequest) (Scorecard, error) {
	suiteID, ok := idParam(req.SuiteRef)
	if !ok {
		return Scorecard{}, store.ErrNotFound
	}
	actor := req.Actor
	if actor == "" {
		actor = "system:evals"
	}
	var suite suiteDTO
	var cases []caseDTO
	found := false
	if err := m.data.View(ctx, tenant, func(sc store.Scope) error {
		s, cs, ok, lerr := loadSuiteAndCases(ctx, sc, suiteID)
		suite, cases, found = s, cs, ok
		return lerr
	}); err != nil {
		return Scorecard{}, err
	}
	if !found {
		return Scorecard{}, store.ErrNotFound
	}

	subj := runSubject{
		suiteRef: suiteID.String(), suiteVer: suite.SuiteVersion,
		subjectKind: firstNonEmpty(req.SubjectKind, suite.SubjectKind), subjectRef: req.SubjectRef,
		variant: req.Variant, launchedBy: actor,
	}
	agg := m.executeRun(ctx, tenant, suite, cases, clampOutputs(req.Outputs), m.scorerByID(suite.Scorer))

	var card Scorecard
	var reg regressionInfo
	if err := m.data.Mutate(ctx, tenant, func(sc store.Scope) error {
		dto, ri, perr := m.persistRun(ctx, sc, suite, subj, agg)
		if perr != nil {
			return perr
		}
		reg = ri
		card = toScorecard(dto, agg)
		return nil
	}); err != nil {
		return Scorecard{}, err
	}
	m.emitRegression(ctx, tenant, suite, reg)
	return card, nil
}

// toScorecard projects a persisted run + its aggregate to the seam DTO.
func toScorecard(dto runDTO, agg runAggregate) Scorecard {
	cases := make([]CaseScore, 0, len(agg.cases))
	for _, c := range agg.cases {
		cases = append(cases, CaseScore{CaseKey: c.caseKey, Outcome: c.outcome, Score: c.score, Passed: c.passed})
	}
	return Scorecard{
		RunRef: dto.ID, Total: agg.total, Passed: agg.passed, Failed: agg.failed, Errors: agg.errors,
		Skipped: agg.skipped, Score: agg.score, PassRate: agg.passRate, Status: agg.status, Cases: cases,
	}
}
