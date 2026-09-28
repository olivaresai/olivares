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
	colLiveRef     = "live_ref"
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

// Principal declarations shared by more than one column below.
var (
	// pdeclNoneDigest is a one-way hex digest; nothing reads a principal back out.
	pdeclNoneDigest = model.None("a one-way SHA-256 hex digest: helpers.go:149-152, scenarios.go:79, runs.go:77")
	// pdeclNoneFixture is synthetic scenario text that only the isolated runner matches and echoes.
	pdeclNoneFixture = model.None("synthetic fixture text, matched and echoed only by the isolated runner: ports.go:111-123, cmd/olivares/sandboxrt.go:75-89")
	// pdeclNoneScenarioRef is the id of a sandbox scenario row.
	pdeclNoneScenarioRef = model.None("the id of a sandbox scenario row: runs.go:230, compare.go:72, runs.go:131-133")
	// pdeclNoneRunRef is the id of a sandbox run row.
	pdeclNoneRunRef = model.None("the id of a sandbox run row: runs.go:148, runs.go:156, compare.go:157")
	// pdeclNoneSubjectRef is a scenario id, a session external id or a live-row id.
	pdeclNoneSubjectRef = model.None("a scenario id, a session external id or a live-row id, never an account: runs.go:223, runs.go:287-289, compare.go:92, compare.go:102-104, modules/sessions/export.go:362")
	// pdeclNoneLiveRef is the id of a sessions live row.
	pdeclNoneLiveRef = model.None("the id of a sessions live row: history_target.go:26-31, modules/sessions/export.go:371")
	// pdeclNoneSuiteRef is the id of an evals suite row.
	pdeclNoneSuiteRef = model.None("the id of an evals suite row: modules/evals/scoreoutputs.go:68, modules/evals/scoreoutputs.go:80")
	// pdeclNoneStepKey is a step key, operator-chosen or generated from the step position.
	pdeclNoneStepKey = model.None("a step key, operator-chosen or generated from the step position: scenarios.go:259-262, cmd/olivares/sessionadapters.go:206-211")
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
			{Name: colName, Kind: model.KindText, Indexed: true, Principal: model.None("an operator-chosen scenario name, rendered only: scenarios.go:70, scenarios.go:46")},
			{Name: colDescription, Kind: model.KindText, Nullable: true, Principal: model.None("operator prose, rendered only: scenarios.go:89, scenarios.go:46")},
			{Name: colSubjectKind, Kind: model.KindText, Nullable: true, Principal: model.None("a subject-kind label, forwarded to the scorer and rendered, never resolved to an account: scenarios.go:90, scenarios.go:47, runs.go:224")},
			{Name: colSteps, Kind: model.KindJSON, Nullable: true, Principal: model.Nested([]stepDTO(nil), model.ClassEvidence,
				model.Leaf("[].key", pdeclNoneStepKey),
				model.Leaf("[].input", pdeclNoneFixture),
			)},
			{Name: colMocks, Kind: model.KindJSON, Nullable: true, Principal: model.Nested([]mockDTO(nil), model.ClassEvidence,
				model.Leaf("[].resource", pdeclNoneFixture),
				model.Leaf("[].response", pdeclNoneFixture),
			)},
			{Name: colSpecHash, Kind: model.KindText, Nullable: true, Principal: pdeclNoneDigest},
			{Name: colScenStatus, Kind: model.KindText, Indexed: true, Principal: model.None("a closed status set active|archived: scenarios.go:92, scenarios.go:203")},
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
			{Name: colScenarioRef, Kind: model.KindUUID, Nullable: true, Indexed: true, Principal: pdeclNoneScenarioRef},
			{Name: colKind, Kind: model.KindText, Indexed: true, Principal: model.None("a closed run-kind set: runs.go:21-25")},
			{Name: colSubjectRef, Kind: model.KindText, Indexed: true, Principal: pdeclNoneSubjectRef},
			{Name: colLiveRef, Kind: model.KindUUID, Nullable: true, Principal: pdeclNoneLiveRef},
			{Name: colVariant, Kind: model.KindText, Nullable: true, Principal: model.None("a variant label, recorded and rendered only: runs.go:134-136, runs.go:335")},
			{Name: colRunner, Kind: model.KindText, Indexed: true, Principal: model.None("the runner backend name: ports.go:68, runs.go:54")},
			{Name: colIsolated, Kind: model.KindBool},
			{Name: colRunStatus, Kind: model.KindText, Indexed: true, Principal: model.None("a closed status set completed|degraded|error: runs.go:54, runs.go:58, runs.go:101, runs.go:284")},
			{Name: colStepsTotal, Kind: model.KindInt},
			{Name: colStepsOK, Kind: model.KindInt},
			{Name: colStepsError, Kind: model.KindInt},
			{Name: colOutputsHash, Kind: model.KindText, Nullable: true, Principal: pdeclNoneDigest},
			{Name: colScore, Kind: model.KindFloat, Nullable: true},
			{Name: colPassed, Kind: model.KindBool, Nullable: true},
			{Name: colSuiteRef, Kind: model.KindUUID, Nullable: true, Principal: pdeclNoneSuiteRef},
			{Name: colDestroyed, Kind: model.KindBool},
			{Name: colStartedAt, Kind: model.KindTimestamp, Indexed: true},
			{Name: colFinishedAt, Kind: model.KindTimestamp, Nullable: true},
			{Name: colLaunchedBy, Kind: model.KindText, Principal: model.Ref(model.EncodeUserRef, model.ClassEvidence)},
		},
	}); err != nil {
		return err
	}

	if err := reg.Register(model.EntityDescriptor{
		Kind:       outputKind,
		Table:      outputTable,
		AppendOnly: true, // immutable per-step evidence
		Fields: []model.FieldSpec{
			{Name: colRunRef, Kind: model.KindUUID, Indexed: true, Principal: pdeclNoneRunRef},
			{Name: colStepKey, Kind: model.KindText, Indexed: true, Principal: pdeclNoneStepKey},
			{Name: colOutput, Kind: model.KindText, Nullable: true, Principal: model.None("runner output text, clamped, then rendered and scored only: runs.go:157, runs.go:91, runs.go:431")},
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
			{Name: colScenarioRef, Kind: model.KindUUID, Nullable: true, Indexed: true, Principal: pdeclNoneScenarioRef},
			{Name: colBaselineRun, Kind: model.KindUUID, Principal: pdeclNoneRunRef},
			{Name: colCandidateRun, Kind: model.KindUUID, Principal: pdeclNoneRunRef},
			{Name: colSubjectRef, Kind: model.KindText, Nullable: true, Principal: pdeclNoneSubjectRef},
			{Name: colLiveRef, Kind: model.KindUUID, Nullable: true, Principal: pdeclNoneLiveRef},
			{Name: colSuiteRef, Kind: model.KindUUID, Nullable: true, Principal: pdeclNoneSuiteRef},
			{Name: colVerdict, Kind: model.KindText, Indexed: true, Principal: model.None("a closed verdict set: compare.go:21-26")},
			{Name: colBaselineScore, Kind: model.KindFloat},
			{Name: colCandScore, Kind: model.KindFloat},
			{Name: colDelta, Kind: model.KindFloat},
			{Name: colDecidedBy, Kind: model.KindText, Principal: model.Ref(model.EncodeUserRef, model.ClassEvidence)},
			{Name: colOccurredAt, Kind: model.KindTimestamp},
		},
	})
}
