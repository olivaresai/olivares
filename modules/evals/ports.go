// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package evals

import (
	"context"
	"errors"
	"time"

	"github.com/olivaresai/olivares/core/model"
)

// This file defines the SEAMS evals declares in its OWN terms (the pattern of
// redteam.Sandbox / orchestration.Dispatcher): each is a port with a fail-closed
// default, and the composition root injects the real adapter. evals never imports a
// sibling module; the only model invocation it makes is the Judge.

// errNoJudge is the offline-judge error. The llm_judge scorer maps it to a SKIPPED
// outcome — a degraded, honest result, NEVER a silent pass (contract §2.2/§2.3).
var errNoJudge = errors.New("no judge wired — llm_judge not executed")

// ---- Judge: model invocation for the llm_judge scorer ----------------------------

// JudgeRequest is what the llm_judge scorer asks the model to score. It carries the
// candidate output, the optional expected reference and the rubric/criterion — never
// a secret.
type JudgeRequest struct {
	// ModelRef is the judge model to invoke (suite.judge_model or run.model_ref).
	ModelRef string
	// Input is the optional original input the output answered.
	Input string
	// Output is the candidate output to judge.
	Output string
	// Expected is the optional reference answer.
	Expected string
	// Criterion is the rubric/criterion the model judges against.
	Criterion string
}

// JudgeVerdict is the model's judgement: a 0..1 score, a pass flag and a short,
// non-sensitive reason.
type JudgeVerdict struct {
	Score  float64
	Passed bool
	Reason string
}

// Judge invokes a model to score an output against a criterion (mapped to by
// a composition-root adapter). The redteam/orchestration port pattern: a default
// fail-closed implementation ships in this package; WithJudge wires the real one.
type Judge interface {
	// Judge returns the model's verdict, or an error (an execution fault — the
	// llm_judge scorer records it as an outcome, never as a pass).
	Judge(ctx context.Context, tenant model.TenantID, req JudgeRequest) (JudgeVerdict, error)
}

// offlineJudge is the default until is wired. It does NOT invoke any model;
// it returns errNoJudge so the llm_judge scorer degrades to SKIPPED rather than
// silently scoring a pass. Start() warns once.
type offlineJudge struct{}

func (offlineJudge) Judge(_ context.Context, _ model.TenantID, _ JudgeRequest) (JudgeVerdict, error) {
	return JudgeVerdict{}, errNoJudge
}

// ---- PairJudge: ordered pairwise comparison for the bias-mitigated A/B -----------

// Pairwise winner labels in the module's own terms (exported — an adapter must
// produce them). PairWinnerFirst/Second are PRESENTATION positions: the A/B handler
// calls the port twice with the candidates exchanged (order-swap) and declares a
// winner only when both orders agree — the position-bias mitigation lives HERE so
// every PairJudge implementation gets it (Zheng et al. 2023; docs/EVAL-METHODOLOGY.md §3).
const (
	PairWinnerFirst  = "first"
	PairWinnerSecond = "second"
	PairWinnerTie    = "tie"
)

// PairRequest is one ORDERED pairwise comparison: which of two candidate outputs
// better satisfies the criterion, as presented.
type PairRequest struct {
	// ModelRef is the judge model to invoke (suite.judge_model).
	ModelRef string
	// Input is the optional original input both outputs answered.
	Input string
	// Criterion is the rubric the comparison is judged against.
	Criterion string
	// OutputFirst/OutputSecond are the candidates IN PRESENTATION ORDER.
	OutputFirst  string
	OutputSecond string
}

// PairVerdict is the model's ordered pairwise verdict: the winning position
// (first|second|tie) and a short, non-sensitive reason.
type PairVerdict struct {
	Winner string
	Reason string
}

// PairJudge invokes a model for one ordered pairwise comparison. Same port pattern
// as Judge: a fail-closed offline default ships in this package; WithPairJudge wires
// the real adapter (the same Claude-backed adapter as the pointwise judge).
type PairJudge interface {
	JudgePair(ctx context.Context, tenant model.TenantID, req PairRequest) (PairVerdict, error)
}

// offlinePairJudge is the default until the adapter is wired: it returns errNoJudge
// so a requested pairwise comparison degrades to a DECLARED skip, never a fabricated
// winner.
type offlinePairJudge struct{}

func (offlinePairJudge) JudgePair(_ context.Context, _ model.TenantID, _ PairRequest) (PairVerdict, error) {
	return PairVerdict{}, errNoJudge
}

// ---- BudgetGate: pre-flight spend admission for the regression gate ---------------

// BudgetDims identifies the prospective judge spend of a gate run in provider-neutral
// terms (the seam convention: refs only, no amounts).
type BudgetDims struct {
	// JudgeModelRef is the judge model the gate would invoke.
	JudgeModelRef string
}

// BudgetDecision is the pre-flight admission: Allowed=false with Action
// "block"|"throttle" stops the gate from spending; Reason is money-free.
type BudgetDecision struct {
	Allowed bool
	Action  string
	Reason  string
}

// BudgetGate is the pre-flight budget admission the regression gate consults BEFORE
// invoking the judge over a sampled subset (decision: a budget per gate
// run, enforced by over the CI itself). The composition root always wires the
// FinOps adapter; the in-package default allows (and Start warns once) so a harness
// without FinOps still works — visibly, never silently.
type BudgetGate interface {
	Check(ctx context.Context, tenant model.TenantID, dims BudgetDims) (BudgetDecision, error)
}

// allowAllBudget is the unwired default: every gate run is admitted. Start() warns
// once that gate judge spend is unbudgeted.
type allowAllBudget struct{}

func (allowAllBudget) Check(_ context.Context, _ model.TenantID, _ BudgetDims) (BudgetDecision, error) {
	return BudgetDecision{Allowed: true}, nil
}

// ---- SessionSource: real-session sampling for the monitor ------------------------

// SampleQuery bounds a monitor sample: an optional subject filter and a row cap.
type SampleQuery struct {
	// SubjectKind/SubjectRef optionally narrow the sample (agent/model ref).
	SubjectKind string
	SubjectRef  string
	// Limit caps the number of samples (0 → a module default).
	Limit int
}

// SessionSample is the MINIMAL-DATA view of one real session the monitor scores. It
// is behavioral/outcome SIGNALS only — state, finding count, max severity, tokens/
// cost — NEVER the session's raw output text, which the platform never persists
// (docs/SECURITY-HARDENING.md, contract §2.3).
type SessionSample struct {
	SessionRef   string
	AgentRef     string
	ModelRef     string
	State        string
	MaxSeverity  string
	Findings     int
	InputTokens  int64
	OutputTokens int64
	CostMicroUSD int64
	OccurredAt   time.Time
}

// SessionSource samples real sessions for the monitor. The default reads core
// Session+Finding signals inline in the monitor handler; a richer adapter (the
// module-II timeline) is wired with WithSessionSource. A nil/zero default sampler is
// the signal that the handler should read core entities itself.
type SessionSource interface {
	// Sample returns minimal-data signals for recent sessions matching q.
	Sample(ctx context.Context, tenant model.TenantID, q SampleQuery) ([]SessionSample, error)
}

// coreSessionSource is the default sampler. It deliberately returns no samples on its
// own: the monitor handler holds the tenant-pinned Scope (this port does not), so the
// handler reads core Session+Finding rows inline when the wired source is the default
// (see handleMonitor). A composition-root adapter that DOES hold its own data handle
// can replace it via WithSessionSource.
type coreSessionSource struct{}

func (coreSessionSource) Sample(_ context.Context, _ model.TenantID, _ SampleQuery) ([]SessionSample, error) {
	return nil, nil
}

// isDefaultSource reports whether the wired session source is the built-in default
// (so the monitor handler reads core entities inline rather than calling out).
func (m *Module) isDefaultSource() bool {
	_, ok := m.sessions.(coreSessionSource)
	return ok
}
