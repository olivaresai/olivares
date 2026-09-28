// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package evals

import (
	"context"
	"sort"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

type ComparisonStatus string

const (
	ComparisonComparable   ComparisonStatus = "comparable"
	ComparisonNoBaseline   ComparisonStatus = "no_baseline"
	ComparisonIncompatible ComparisonStatus = "incompatible"
	ComparisonUnknown      ComparisonStatus = "unknown"
	ComparisonIncomplete   ComparisonStatus = "incomplete"
	ComparisonDisabled     ComparisonStatus = "disabled"
)

// ComparisonReason is a bounded diagnostic; consumers must not parse prose.
type ComparisonReason string

const (
	ComparisonValid              ComparisonReason = "comparison_valid"
	ComparisonAbsent             ComparisonReason = "no_baseline"
	ComparisonUnavailable        ComparisonReason = "baseline_unavailable"
	ComparisonBaselineUnknown    ComparisonReason = "baseline_evidence_unknown"
	ComparisonBaselineIncomplete ComparisonReason = "baseline_incomplete"
	ComparisonIdentityMismatch   ComparisonReason = "baseline_identity_mismatch"
	ComparisonHistoryUnknown     ComparisonReason = "history_evidence_unknown"
	ComparisonHistoryMismatch    ComparisonReason = "history_incompatible"
	ComparisonHistoryIncomplete  ComparisonReason = "history_incomplete"
	ComparisonModelMismatch      ComparisonReason = "judge_model_mismatch"
	ComparisonSelectedIncomplete ComparisonReason = "selected_cases_incomplete"
	ComparisonIdentityUnknown    ComparisonReason = "scoring_identity_unknown"
	ComparisonRegressionDisabled ComparisonReason = "regression_disabled"
	ComparisonLegacyUnknown      ComparisonReason = "legacy_evidence_unknown"
	ComparisonBudgetNotScored    ComparisonReason = "budget_not_scored"
)

// ComparisonEvidence is immutable. Metrics belong to this receipt, not to the
// mutable historical aggregate. No raw outputs or case bodies are stored here.
type ComparisonEvidence struct {
	Version             int                `json:"version"`
	Status              ComparisonStatus   `json:"status"`
	Reason              ComparisonReason   `json:"reason"`
	Mode                string             `json:"mode"`
	Selection           string             `json:"selection"`
	BaselineRef         string             `json:"baseline_ref,omitempty"`
	BaselineEvidenceRef string             `json:"baseline_evidence_ref,omitempty"`
	Identity            comparisonIdentity `json:"identity"`
	Known               bool               `json:"known"`
	FirstRun            bool               `json:"first_run"`
	Complete            bool               `json:"complete"`
	Scored              populationIdentity `json:"scored"`
	Score               float64            `json:"score"`
	PassRate            float64            `json:"pass_rate"`
	Errors              int                `json:"errors"`
	Skipped             int                `json:"skipped"`
	BaselineScore       float64            `json:"baseline_score,omitempty"`
	Drift               float64            `json:"drift"`
	Regressed           bool               `json:"regressed"`
	Changed             []string           `json:"changed,omitempty"`
	ObservedModels      []string           `json:"observed_models,omitempty"`
	CacheHits           int                `json:"cache_hits"`
	CacheBasis          string             `json:"cache_basis,omitempty"`
}
type comparisonPlan struct {
	evidence ComparisonEvidence
	baseline *ComparisonEvidence
	selected []caseDTO
	key      string
}

// prepareComparison is called only in the initial View. The returned plan owns a
// frozen copy of the selected baseline receipt, so later pins cannot change it.
func (m *Module) prepareComparison(ctx context.Context, sc store.Scope, suite suiteDTO, subj runSubject, full, selected []caseDTO, scorer Scorer, seed string, size int) (*comparisonPlan, error) {
	subj.subjectRef = clamp(subj.subjectRef, maxRefLen)
	subj.modelRef = clamp(subj.modelRef, maxRefLen)
	subj.variant = clamp(subj.variant, maxNameLen)
	identity, known, err := captureIdentity(sc.Tenant().String(), suite, subj, full, selected, scorer, seed, size)
	if err != nil {
		return nil, err
	}
	key, err := comparisonKey(identity)
	if err != nil {
		return nil, err
	}
	mode := subj.comparison.Mode
	if mode == "" {
		mode = "same_candidate"
	}
	p := &comparisonPlan{evidence: ComparisonEvidence{Version: 1, Status: ComparisonNoBaseline, Reason: "no_baseline", Mode: mode, Selection: "automatic", Identity: identity, Known: known, FirstRun: true}, selected: append([]caseDTO(nil), selected...), key: key}
	ref := subj.baselineRef
	if ref != "" {
		p.evidence.Selection = "explicit"
	} else {
		repo, e := sc.Ext(baseKind)
		if e != nil {
			return nil, e
		}
		pins, _, e := repo.List(ctx, model.Query{Filters: []model.Filter{eq(colSuiteRef, suite.ID), eq(colSubjectRef, subj.subjectRef)}, Limit: 1})
		if e != nil {
			return nil, e
		}
		if len(pins) > 0 {
			ref = pins[0].String(colBaseRunRef)
			p.evidence.Selection = "pin"
		}
	}
	if ref != "" || p.evidence.Selection == "pin" {
		p.evidence.FirstRun = false
		p.evidence.BaselineRef = ref
		id, ok := idParam(ref)
		if !ok {
			p.refuse(ComparisonUnknown, "baseline_unavailable")
			return p, nil
		}
		ref = id.String()
		p.evidence.BaselineRef = ref
		runs, e := sc.Ext(runKind)
		if e != nil {
			return nil, e
		}
		_, e = runs.Get(ctx, id)
		if e != nil {
			if isNotFound(e) {
				p.refuse(ComparisonUnknown, "baseline_unavailable")
				return p, nil
			}
			return nil, e
		}
		receipt, rid, e := loadComparison(ctx, sc, ref)
		if e != nil {
			return nil, e
		}
		if receipt == nil {
			p.refuse(ComparisonUnknown, "baseline_evidence_unknown")
			return p, nil
		}
		p.useBaseline(receipt, rid)
		return p, nil
	}
	repo, err := sc.Ext(comparisonKind)
	if err != nil {
		return nil, err
	}
	rows, _, err := repo.List(ctx, model.Query{Filters: []model.Filter{eq(colComparisonKey, key), eq(colEligible, true)}, Sort: []model.Sort{{Column: colFinishedAt, Desc: true}, {Column: model.ColID, Desc: true}}, Limit: 1})
	if err != nil {
		return nil, err
	}
	if len(rows) > 0 {
		receipt, e := decodeComparison(rows[0])
		if e != nil {
			return nil, e
		}
		p.evidence.BaselineRef = rows[0].String(colRunRef)
		p.evidence.FirstRun = false
		runs, e := sc.Ext(runKind)
		if e != nil {
			return nil, e
		}
		if _, e = runs.Get(ctx, model.ID(p.evidence.BaselineRef)); e != nil {
			if isNotFound(e) {
				p.refuse(ComparisonUnknown, ComparisonUnavailable)
				return p, nil
			}
			return nil, e
		}
		p.useBaseline(&receipt, rows[0].String(model.ColID))
		return p, nil
	}
	// One bounded probe: absence is a real first run; legacy/incompatible history
	// must not be silently relabelled as a first run.
	runs, err := sc.Ext(runKind)
	if err != nil {
		return nil, err
	}
	history, _, err := runs.List(ctx, model.Query{Filters: []model.Filter{eq(colSuiteRef, suite.ID), eq(colSubjectRef, subj.subjectRef), eq(colVariant, subj.variant)}, Sort: []model.Sort{{Column: colFinishedAt, Desc: true}, {Column: model.ColID, Desc: true}}, Limit: 1})
	if err != nil {
		return nil, err
	}
	if len(history) > 0 {
		p.evidence.FirstRun = false
		previous, _, e := loadComparison(ctx, sc, history[0].String(model.ColID))
		if e != nil {
			return nil, e
		}
		if previous == nil || !previous.Known {
			p.refuse(ComparisonUnknown, "history_evidence_unknown")
		} else if !previous.Complete {
			p.refuse(ComparisonIncomplete, "history_incomplete")
		} else {
			p.refuse(ComparisonIncompatible, "history_incompatible")
		}
	}
	return p, nil
}
func (p *comparisonPlan) refuse(status ComparisonStatus, reason ComparisonReason) {
	p.evidence.Status = status
	p.evidence.Reason = reason
}
func (p *comparisonPlan) useBaseline(b *ComparisonEvidence, id string) {
	p.baseline = b
	p.evidence.FirstRun = false
	p.evidence.BaselineEvidenceRef = id
	if !b.Known {
		p.refuse(ComparisonUnknown, "baseline_evidence_unknown")
		return
	}
	if !b.Complete {
		p.refuse(ComparisonIncomplete, "baseline_incomplete")
		return
	}
	if !compatibleIdentity(p.evidence.Identity, b.Identity, p.evidence.Mode) {
		p.refuse(ComparisonIncompatible, "baseline_identity_mismatch")
		return
	}
	p.refuse(ComparisonComparable, "comparison_valid")
}

// complete is pure and runs after all scores are in hand. Only successful scored
// cases enter the population, and any missing/error case makes it ineligible.
func (p *comparisonPlan) complete(agg runAggregate) (ComparisonEvidence, error) {
	c := p.evidence
	c.Score = agg.score
	c.PassRate = agg.passRate
	c.Errors = agg.errors
	c.Skipped = agg.skipped
	scoredKeys := map[string]bool{}
	models := map[string]bool{}
	for _, r := range agg.cases {
		if r.cached {
			c.CacheHits++
		}
		if r.outcome == outcomePass || r.outcome == outcomeFail {
			scoredKeys[r.caseKey] = true
		}
		if r.observedModel != "" {
			models[r.observedModel] = true
		}
		if r.protocolUnknown {
			c.Known = false
			c.CacheBasis = "cache-protocol-unverified"
		}
	}
	if c.CacheBasis == "" {
		switch {
		case c.CacheHits == 0:
			c.CacheBasis = "fresh-scoring"
		case c.CacheHits == len(agg.cases):
			c.CacheBasis = "retained-cache-protocol-v1"
		default:
			c.CacheBasis = "mixed-fresh-and-retained-cache-protocol-v1"
		}
	}
	scored := make([]caseDTO, 0, len(scoredKeys))
	for _, cs := range p.selected {
		if scoredKeys[cs.CaseKey] {
			scored = append(scored, cs)
		}
	}
	var err error
	c.Scored, err = population(scored)
	if err != nil {
		return c, err
	}
	c.Complete = c.Identity.Selected.Count > 0 && c.Scored == c.Identity.Selected && agg.errors == 0 && agg.skipped == 0
	for model := range models {
		c.ObservedModels = append(c.ObservedModels, model)
	}
	sort.Strings(c.ObservedModels)
	// An observed contradiction is retained even if another refusal already exists.
	mismatch := false
	for _, model := range c.ObservedModels {
		if c.Identity.Protocol.Model != "" && model != c.Identity.Protocol.Model {
			mismatch = true
			c.Known = false
		}
	}
	if c.Reason == "baseline_unavailable" {
		return c, nil
	}
	if mismatch {
		c.Status = ComparisonIncompatible
		c.Reason = "judge_model_mismatch"
		return c, nil
	}
	if !c.Complete {
		c.Status = ComparisonIncomplete
		c.Reason = "selected_cases_incomplete"
		return c, nil
	}
	if !c.Known {
		c.Status = ComparisonUnknown
		c.Reason = "scoring_identity_unknown"
		return c, nil
	}
	if c.Status == ComparisonComparable {
		c.BaselineScore = p.baseline.Score
		a, b := c.Identity.Candidate, p.baseline.Identity.Candidate
		if a.Subject != b.Subject {
			c.Changed = append(c.Changed, "subject_ref")
		}
		if a.Model != b.Model {
			c.Changed = append(c.Changed, "model_ref")
		}
		if a.Variant != b.Variant {
			c.Changed = append(c.Changed, "prompt_variant")
		}
	}
	if c.Identity.RegressionThreshold <= 0 && (c.Status == ComparisonComparable || c.Status == ComparisonNoBaseline) {
		c.Status = ComparisonDisabled
		c.Reason = "regression_disabled"
		return c, nil
	}
	if c.Status == ComparisonComparable {
		c.Drift = c.BaselineScore - c.Score
		c.Regressed = c.Drift > c.Identity.RegressionThreshold
	}
	return c, nil
}
