// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package evals

import (
	"encoding/json"
	"sort"
	"strings"
)

// ScoringProtocol describes the declared implementation and effective configuration.
// It is not proof that remote model weights or outputs are immutable. ConfigDigest
// covers prompt, schema, parser and all score-affecting settings, without secrets.
type ScoringProtocol struct {
	Implementation string `json:"implementation"`
	Version        string `json:"version"`
	Provider       string `json:"provider,omitempty"`
	Model          string `json:"model,omitempty"`
	ConfigDigest   string `json:"config_digest"`
}

// ScorerProtocol is optional for source compatibility. An override without this
// interface is unknown even when its ID matches a built-in scorer.
type ScorerProtocol interface {
	ScoringProtocol(modelRef string) (ScoringProtocol, bool)
}

// JudgeProtocol is the corresponding optional port for real judge adapters.
type JudgeProtocol interface {
	JudgingProtocol(modelRef string) (ScoringProtocol, bool)
}

func (s fnScorer) ScoringProtocol(string) (ScoringProtocol, bool) {
	return ScoringProtocol{Implementation: "evals/builtin/" + s.id, Version: "1", ConfigDigest: hashHex("builtin-v1")}, true
}
func (s *judgeScorer) ScoringProtocol(modelRef string) (ScoringProtocol, bool) {
	p, ok := s.judge().(JudgeProtocol)
	if !ok {
		return ScoringProtocol{}, false
	}
	d, ok := p.JudgingProtocol(modelRef)
	if !ok || d.Implementation == "" || d.Version == "" || d.ConfigDigest == "" || d.Provider == "" || d.Model == "" {
		return d, false
	}
	d.Implementation = "evals/llm_judge/v1/" + d.Implementation
	return d, d.Provider != "" && d.Model != ""
}

// ComparisonRequest v1 separates experiment dimensions from the scoring protocol.
type ComparisonRequest struct {
	Version int    `json:"version"`
	Mode    string `json:"mode"`
}

func normalizeComparison(request *ComparisonRequest, baseline string) (ComparisonRequest, bool) {
	r := ComparisonRequest{Version: 1, Mode: "same_candidate"}
	if request != nil {
		r = *request
	}
	if r.Version != 1 || (r.Mode != "same_candidate" && r.Mode != "candidate_change") {
		return r, false
	}
	if r.Mode == "candidate_change" && baseline == "" {
		return r, false
	}
	if baseline != "" {
		if _, ok := idParam(baseline); !ok {
			return r, false
		}
	}
	return r, true
}

type candidateIdentity struct {
	Subject     string `json:"subject"`
	Model       string `json:"model"`
	ModelSource string `json:"model_source"`
	Variant     string `json:"variant"`
}
type populationIdentity struct {
	Count  int    `json:"count"`
	Digest string `json:"digest"`
}
type comparisonIdentity struct {
	Tenant              string             `json:"tenant"`
	Suite               string             `json:"suite"`
	SuiteVersion        int64              `json:"suite_version"`
	SubjectKind         string             `json:"subject_kind"`
	Candidate           candidateIdentity  `json:"candidate"`
	Protocol            ScoringProtocol    `json:"protocol"`
	RubricDigest        string             `json:"rubric_digest"`
	Full                populationIdentity `json:"full"`
	Selected            populationIdentity `json:"selected"`
	Seed                string             `json:"seed,omitempty"`
	SampleSize          int                `json:"sample_size"`
	Sampling            string             `json:"sampling"`
	PassThreshold       float64            `json:"pass_threshold"`
	RegressionThreshold float64            `json:"regression_threshold"`
}

func digestJSON(v any) (string, error) {
	b, e := json.Marshal(v)
	if e != nil {
		return "", e
	}
	return hashHex(string(b)), nil
}
func population(cases []caseDTO) (populationIdentity, error) {
	// Avoid ids/timestamps: these exact inputs are what executeRun passes to scorers.
	type tuple struct {
		Key      string         `json:"key"`
		Input    string         `json:"input"`
		Expected string         `json:"expected"`
		Weight   float64        `json:"weight"`
		Metadata map[string]any `json:"metadata"`
	}
	tuples := make([]tuple, 0, len(cases))
	for _, c := range cases {
		tuples = append(tuples, tuple{c.CaseKey, c.Input, c.Expected, c.Weight, c.Metadata})
	}
	sort.Slice(tuples, func(i, j int) bool { return tuples[i].Key < tuples[j].Key })
	d, e := digestJSON(tuples)
	return populationIdentity{Count: len(tuples), Digest: d}, e
}
func captureIdentity(tenant string, suite suiteDTO, subj runSubject, full, selected []caseDTO, scorer Scorer, seed string, size int) (comparisonIdentity, bool, error) {
	i := comparisonIdentity{Tenant: tenant, Suite: suite.ID, SuiteVersion: suite.SuiteVersion, SubjectKind: subj.subjectKind,
		Candidate: candidateIdentity{Subject: subj.subjectRef, Model: subj.modelRef, Variant: subj.variant}, RubricDigest: hashHex(suite.Criterion),
		Seed: seed, SampleSize: size, Sampling: "all-v1", PassThreshold: suite.PassThreshold, RegressionThreshold: suite.RegThreshold}
	if seed != "" {
		i.Sampling = "lowest-sha256-seed-key-v1"
	}
	if strings.TrimSpace(i.Candidate.Model) != "" {
		i.Candidate.ModelSource = "model_ref"
	} else if subj.subjectKind == "model" && strings.TrimSpace(subj.subjectRef) != "" {
		i.Candidate.Model = subj.subjectRef
		i.Candidate.ModelSource = "subject_ref"
	}
	var err error
	if i.Full, err = population(full); err != nil {
		return i, false, err
	}
	if i.Selected, err = population(selected); err != nil {
		return i, false, err
	}
	p, ok := scorer.(ScorerProtocol)
	if !ok {
		return i, false, nil
	}
	d, known := p.ScoringProtocol(suite.JudgeModel)
	i.Protocol = d
	known = known && d.Implementation != "" && d.Version != "" && d.ConfigDigest != "" && strings.TrimSpace(i.Candidate.Model) != ""
	return i, known, nil
}

// Key excludes candidate source spelling, seed and decision thresholds. Equal
// selected content is required; a seed or count alone never proves equality.
func comparisonKey(i comparisonIdentity) (string, error) {
	return digestJSON(struct {
		Tenant, Suite, Kind, Subject, Model, Variant, Rubric string
		Version                                              int64
		Protocol                                             ScoringProtocol
		Full, Selected                                       populationIdentity
	}{
		i.Tenant, i.Suite, i.SubjectKind, i.Candidate.Subject, i.Candidate.Model, i.Candidate.Variant, i.RubricDigest, i.SuiteVersion, i.Protocol, i.Full, i.Selected})
}
func compatibleIdentity(a, b comparisonIdentity, mode string) bool {
	if mode == "candidate_change" {
		a.Candidate = b.Candidate
	}
	ka, ea := comparisonKey(a)
	kb, eb := comparisonKey(b)
	return ea == nil && eb == nil && ka == kb
}
