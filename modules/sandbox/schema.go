// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sandbox

import (
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// Owned entity kinds and tables. All within the 40-char module-table cap:
// the longest, sandbox_comparison, is 18 chars. A SCENARIO is the mutable fixture; a
// RUN is mutable (running→terminal — updated at completion); OUTPUTS and COMPARISONS
// are APPEND-ONLY because they are immutable evidence (a per-step output and a
// deploy decision, docs/SECURITY-HARDENING.md) a later run/decision is compared against.
const (
	scenarioKind    model.Kind = "sandbox.scenario"
	scenarioTable              = "sandbox_scenario"
	runKind         model.Kind = "sandbox.run"
	runTable                   = "sandbox_run"
	outputKind      model.Kind = "sandbox.output"
	outputTable                = "sandbox_output"
	comparisonKind  model.Kind = "sandbox.comparison"
	comparisonTable            = "sandbox_comparison"
)

// sandbox_scenario columns — a synthetic, operator-authored fixture (steps + mocks).
const (
	colName        = "name"
	colDescription = "description"
	colSubjectKind = "subject_kind"
	colSteps       = "steps" // json: synthetic input sequence
	colMocks       = "mocks" // json: simulated MCP/resource responses (no secrets)
	colSpecHash    = "spec_hash"
	colScenStatus  = "status" // "active" | "archived"
)

// sandbox_run columns — one execution's lifecycle aggregate (mutable: running→
// terminal). NOT append-only: it is updated when the run terminates.
const (
	colScenarioRef = "scenario_ref"
	colKind        = "kind" // "scenario" | "replay" | "compare"
	colSubjectRef  = "subject_ref"
	colVariant     = "variant"
	colRunner      = "runner" // "inproc-mock" | "container" | ...
	colIsolated    = "isolated"
	colRunStatus   = "run_status" // "running" | "completed" | "degraded" | "error"
	colStepsTotal  = "steps_total"
	colStepsOK     = "steps_ok"
	colStepsError  = "steps_error"
	colOutputsHash = "outputs_hash"
	colScore       = "score"
	colPassed      = "passed"
	colSuiteRef    = "suite_ref"
	colDestroyed   = "destroyed"
	colStartedAt   = "started_at"
	colFinishedAt  = "finished_at"
	colLaunchedBy  = "launched_by"
)

// sandbox_output columns — one per-step output (append-only evidence).
const (
	colRunRef     = "run_ref"
	colStepKey    = "step_key"
	colOutput     = "output" // synthetic from mocks; a real backend hashes/clamps/scrubs
	colMockHit    = "mock_hit"
	colOccurredAt = "occurred_at"
)

// sandbox_comparison columns — one pre/post-deploy verdict (append-only decision).
const (
	colBaselineRun   = "baseline_run_ref"
	colCandidateRun  = "candidate_run_ref"
	colVerdict       = "verdict" // "improved" | "regressed" | "unchanged" | "inconclusive"
	colBaselineScore = "baseline_score"
	colCandScore     = "candidate_score"
	colDelta         = "delta"
	colDecidedBy     = "decided_by"
)

// RegisterSchema declares the module's four owned entities (the SchemaProvider seam).
// The engine creates the tables, injects base columns and attaches the tenant/audit/
// append-only guards; a module cannot opt out of isolation.
//
// Minimal data (docs/SECURITY-HARDENING.md): a scenario's steps/mocks are synthetic, operator-authored
// fixtures (no secrets); an output stores the synthetic mock text with the in-proc
// runner, but an OS-level backend backing a real target hashes/clamps/scrubs before
// persisting — sandbox_output NEVER stores raw text of a real target.
func (m *Module) RegisterSchema(reg store.ExtensionRegistry) error {
	if err := reg.Register(model.EntityDescriptor{
		Kind:  scenarioKind,
		Table: scenarioTable,
		Fields: []model.FieldSpec{
			{Name: colName, Kind: model.KindText, Indexed: true},
			{Name: colDescription, Kind: model.KindText, Nullable: true},
			{Name: colSubjectKind, Kind: model.KindText, Nullable: true},
			{Name: colSteps, Kind: model.KindJSON, Nullable: true},
			{Name: colMocks, Kind: model.KindJSON, Nullable: true},
			{Name: colSpecHash, Kind: model.KindText, Nullable: true},
			{Name: colScenStatus, Kind: model.KindText, Indexed: true},
		},
		Indexes: []model.IndexSpec{{
			// One scenario per (tenant, name). Unique index leads with tenant_id.
			Name:    "sandbox_scenario_uniq",
			Columns: []string{model.ColTenantID, colName},
			Unique:  true,
		}},
	}); err != nil {
		return err
	}

	if err := reg.Register(model.EntityDescriptor{
		Kind:  runKind,
		Table: runTable, // mutable: a run is created running and updated to a terminal state
		Fields: []model.FieldSpec{
			{Name: colScenarioRef, Kind: model.KindUUID, Nullable: true, Indexed: true},
			{Name: colKind, Kind: model.KindText, Indexed: true},
			{Name: colSubjectRef, Kind: model.KindText, Indexed: true},
			{Name: colVariant, Kind: model.KindText, Nullable: true},
			{Name: colRunner, Kind: model.KindText, Indexed: true},
			{Name: colIsolated, Kind: model.KindBool},
			{Name: colRunStatus, Kind: model.KindText, Indexed: true},
			{Name: colStepsTotal, Kind: model.KindInt},
			{Name: colStepsOK, Kind: model.KindInt},
			{Name: colStepsError, Kind: model.KindInt},
			{Name: colOutputsHash, Kind: model.KindText, Nullable: true},
			{Name: colScore, Kind: model.KindFloat, Nullable: true},
			{Name: colPassed, Kind: model.KindBool, Nullable: true},
			{Name: colSuiteRef, Kind: model.KindUUID, Nullable: true},
			{Name: colDestroyed, Kind: model.KindBool},
			{Name: colStartedAt, Kind: model.KindTimestamp, Indexed: true},
			{Name: colFinishedAt, Kind: model.KindTimestamp, Nullable: true},
			{Name: colLaunchedBy, Kind: model.KindText},
		},
	}); err != nil {
		return err
	}

	if err := reg.Register(model.EntityDescriptor{
		Kind:       outputKind,
		Table:      outputTable,
		AppendOnly: true, // immutable per-step evidence
		Fields: []model.FieldSpec{
			{Name: colRunRef, Kind: model.KindUUID, Indexed: true},
			{Name: colStepKey, Kind: model.KindText, Indexed: true},
			{Name: colOutput, Kind: model.KindText, Nullable: true},
			{Name: colMockHit, Kind: model.KindBool},
			{Name: colOccurredAt, Kind: model.KindTimestamp},
		},
	}); err != nil {
		return err
	}

	return reg.Register(model.EntityDescriptor{
		Kind:       comparisonKind,
		Table:      comparisonTable,
		AppendOnly: true, // immutable deploy-decision evidence (docs/SECURITY-HARDENING.md)
		Fields: []model.FieldSpec{
			{Name: colScenarioRef, Kind: model.KindUUID, Nullable: true, Indexed: true},
			{Name: colBaselineRun, Kind: model.KindUUID},
			{Name: colCandidateRun, Kind: model.KindUUID},
			{Name: colSubjectRef, Kind: model.KindText, Nullable: true},
			{Name: colSuiteRef, Kind: model.KindUUID, Nullable: true},
			{Name: colVerdict, Kind: model.KindText, Indexed: true},
			{Name: colBaselineScore, Kind: model.KindFloat},
			{Name: colCandScore, Kind: model.KindFloat},
			{Name: colDelta, Kind: model.KindFloat},
			{Name: colDecidedBy, Kind: model.KindText},
			{Name: colOccurredAt, Kind: model.KindTimestamp},
		},
	})
}
