// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package evals

import (
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// Owned entity kinds and tables. All within the 40-char module-table cap:
// the longest, evals_calib_report, is 18 chars. A SUITE is the mutable definition; a
// CASE and a CASE_RESULT are APPEND-ONLY (immutable golden evidence per version — a
// fix is a new suite_version, not an edit); a RUN is MUTABLE (it transitions
// running→terminal, in practice created already-terminal); a BASELINE is mutable
// (re-pinnable, the change self-audited with old/new run refs).
//
// Adds four tables (NEW tables, never new columns — a module table is frozen
// after its first creation, core/internal/store/sqlstore/schema.go): a CALIB_ITEM is
// a human-labeled reference item (mutable — re-labeling is an audited correction); a
// CALIB_REPORT is one measured judge↔human calibration (append-only evidence); a
// GATE is one CI gate evaluation (mutable only for the governed override, audited);
// a JUDGE_CACHE row is one cached judge verdict keyed by input-hash + judge-model
// pin (append-only; the model pin and prompt version live inside the hash).
const (
	suiteKind     model.Kind = "evals.suite"
	suiteTable               = "evals_suite"
	caseKind      model.Kind = "evals.case"
	caseTable                = "evals_case"
	runKind       model.Kind = "evals.run"
	runTable                 = "evals_run"
	resultKind    model.Kind = "evals.case_result"
	resultTable              = "evals_case_result"
	baseKind      model.Kind = "evals.baseline"
	baseTable                = "evals_baseline"
	calItemKind   model.Kind = "evals.calib_item"
	calItemTable             = "evals_calib_item"
	calReportKind model.Kind = "evals.calib_report"
	calReportTbl             = "evals_calib_report"
	gateKind      model.Kind = "evals.gate"
	gateTable                = "evals_gate"
	cacheKind     model.Kind = "evals.judge_cache"
	cacheTable               = "evals_judge_cache"
)

// evals_suite columns — a versioned golden dataset definition (mutable). NOTE:
// "suite_version" NOT "version" — "version" is the engine's reserved optimistic-
// concurrency counter and Register would reject it (docs §2.1).
const (
	colName       = "name"
	colDescr      = "description"
	colSubjKind   = "subject_kind" // agent|model|prompt|session|sandbox_run
	colScorer     = "scorer"       // id of the default scorer
	colCriterion  = "criterion"    // rubric/criterion text (no PII/secrets)
	colPassThresh = "pass_threshold"
	colRegThresh  = "regression_threshold"
	colJudgeModel = "judge_model" // model for llm_judge (nullable)
	colSuiteVer   = "suite_version"
	colSuiteStat  = "status" // active|archived
)

// evals_case columns — one golden input→expected/criterion case (append-only).
const (
	colSuiteRef = "suite_ref"
	colCaseKey  = "case_key"
	colInput    = "input"    // bounded fixture text (no PII/secrets)
	colExpected = "expected" // bounded, nullable
	colWeight   = "weight"
	colCaseMeta = "metadata"
)

// evals_run columns — one execution of a suite against a subject's outputs
// (mutable: running→terminal).
const (
	colModelRef    = "model_ref"      // nullable
	colVariant     = "prompt_variant" // A/B label, nullable
	colRunStatus   = "status"         // running|completed|degraded|error
	colTotal       = "total"
	colPassed      = "passed"
	colFailed      = "failed"
	colErrors      = "errors"
	colSkipped     = "skipped"
	colScore       = "score"     // mean of scored cases, 0..1
	colPassRate    = "pass_rate" // passed/(passed+failed)
	colBaselineRef = "baseline_ref"
	colRegressed   = "regressed"
	colDrift       = "drift"
	colStartedAt   = "started_at"
	colFinishedAt  = "finished_at" // nullable
	colLaunchedBy  = "launched_by"
)

// evals_case_result columns — one case outcome within a run (append-only). The raw
// candidate output is NEVER stored: only a one-way detail hash + a clamped label.
const (
	colRunRef     = "run_ref"
	colResScorer  = "scorer"
	colOutcome    = "outcome" // pass|fail|error|skipped
	colResScore   = "score"
	colPassedFlag = "passed"
	colDetailHash = "detail_hash" // one-way hash of output|expected|reason
	colLabel      = "label"       // short, clamped+scrubbed, for UI
	colOccurredAt = "occurred_at"
)

// evals_baseline columns — a pinned baseline run per (suite, subject) (mutable).
const (
	colBaseRunRef = "run_ref"
	colSubjectRef = "subject_ref"
	colPinnedBy   = "pinned_by"
)

// evals_calib_item columns — one human-labeled reference item (mutable, audited).
// Like a suite case, the input/output/expected text is an operator-authorized,
// opt-in, NON-PRODUCTION fixture (the contract §2.1 carve-out), clamped before
// Create; the human label is the reference the judge is measured against.
const (
	colSetName     = "set_name"
	colOutput      = "output"
	colHumanPassed = "human_passed"
	colHumanScore  = "human_score" // optional graded label, nullable
	colLabeledBy   = "labeled_by"
	colNotes       = "notes"
)

// evals_calib_report columns — one measured judge↔human calibration (append-only
// evidence). agreement/kappa/sensitivity/specificity are MEASURED on the labeled
// set, never fabricated; *_defined flags mark degenerate statistics as unmeasured
// instead of reporting a fake zero.
const (
	colItemsTotal  = "items_total"
	colItemsScored = "items_scored"
	colItemsError  = "items_error"
	colAgreement   = "agreement"
	colAgreeLo     = "agreement_lo"
	colAgreeHi     = "agreement_hi"
	colKappa       = "kappa"
	colKappaOK     = "kappa_defined"
	colSens        = "sensitivity"
	colSensN       = "sensitivity_n"
	colSpec        = "specificity"
	colSpecN       = "specificity_n"
	colMeanAbsErr  = "mean_abs_err"
	colVerbCorr    = "verbosity_corr"
	colVerbCorrOK  = "verbosity_corr_defined"
	colTarget      = "target"
	colKappaFloor  = "kappa_floor"
	colMeets       = "meets_target"
)

// evals_gate columns — one CI gate evaluation (mutable ONLY for the governed
// override; everything else is written once). reasons is the verdict's structured
// explanation; seed/sampled record the deterministic subset so a re-run is
// reproducible.
const (
	colVerdict        = "verdict" // pass|fail|warn
	colReasons        = "reasons" // JSON array of reason codes
	colSampled        = "sampled"
	colSeed           = "seed"
	colCalibRef       = "calibration_ref" // nullable: the report the gate trusted
	colOverridden     = "overridden"
	colOverrideBy     = "override_by"
	colOverrideReason = "override_reason"
)

// evals_judge_cache columns — one cached judge verdict (append-only). input_hash is
// hashHex(cache-version|judge-model|criterion|input|expected|output): the judge
// model PIN and the prompt version are part of the key, so a model or prompt change
// can never serve a stale verdict.
const (
	colInputHash = "input_hash"
	colReason    = "reason"
)

// RegisterSchema declares the module's nine owned entities (the SchemaProvider
// seam). The engine creates the tables, injects base columns and attaches the
// tenant/audit/append-only guards; a module cannot opt out of isolation.
//
// Minimal data (docs/SECURITY-HARDENING.md): the suite criterion and a case input/expected are
// operator-authorized, opt-in, NON-PRODUCTION fixtures (a new carve-out analogous to
// the auth partition, see contract §2.1), bounded by the handler before Create;
// a case result carries only a hash of the candidate output + a clamped label, never
// the raw output (from any source).
func (m *Module) RegisterSchema(reg store.ExtensionRegistry) error {
	if err := reg.Register(model.EntityDescriptor{
		Kind:    suiteKind,
		Table:   suiteTable,
		Audited: true, // suite lifecycle (create/archive) is auditable evidence
		Fields: []model.FieldSpec{
			{Name: colName, Kind: model.KindText, Indexed: true},
			{Name: colDescr, Kind: model.KindText, Nullable: true},
			{Name: colSubjKind, Kind: model.KindText},
			{Name: colScorer, Kind: model.KindText, Indexed: true},
			{Name: colCriterion, Kind: model.KindText, Nullable: true},
			{Name: colPassThresh, Kind: model.KindFloat},
			{Name: colRegThresh, Kind: model.KindFloat},
			{Name: colJudgeModel, Kind: model.KindText, Nullable: true},
			{Name: colSuiteVer, Kind: model.KindInt},
			{Name: colSuiteStat, Kind: model.KindText, Indexed: true},
		},
		Indexes: []model.IndexSpec{{
			// One suite per (tenant, name, version). Unique index leads with tenant_id.
			Name:    "evals_suite_uniq",
			Columns: []string{model.ColTenantID, colName, colSuiteVer},
			Unique:  true,
		}},
	}); err != nil {
		return err
	}

	if err := reg.Register(model.EntityDescriptor{
		Kind:       caseKind,
		Table:      caseTable,
		AppendOnly: true, // immutable golden evidence per version
		Fields: []model.FieldSpec{
			{Name: colSuiteRef, Kind: model.KindUUID, Indexed: true},
			{Name: colSuiteVer, Kind: model.KindInt, Indexed: true},
			{Name: colCaseKey, Kind: model.KindText, Indexed: true},
			{Name: colInput, Kind: model.KindText},
			{Name: colExpected, Kind: model.KindText, Nullable: true},
			{Name: colWeight, Kind: model.KindFloat},
			{Name: colCaseMeta, Kind: model.KindJSON, Nullable: true},
		},
		Indexes: []model.IndexSpec{{
			// One case per (tenant, suite, case_key). Unique index leads with tenant_id.
			Name:    "evals_case_uniq",
			Columns: []string{model.ColTenantID, colSuiteRef, colCaseKey},
			Unique:  true,
		}},
	}); err != nil {
		return err
	}

	if err := reg.Register(model.EntityDescriptor{
		Kind:  runKind,
		Table: runTable,
		// MUTABLE (not append-only): a run transitions running→terminal; the canonical
		// immutable evidence is the per-case results + the core EvalResult.
		Fields: []model.FieldSpec{
			{Name: colSuiteRef, Kind: model.KindUUID, Indexed: true},
			{Name: colSuiteVer, Kind: model.KindInt},
			{Name: colSubjKind, Kind: model.KindText},
			{Name: colSubjectRef, Kind: model.KindText, Indexed: true},
			{Name: colModelRef, Kind: model.KindText, Nullable: true},
			{Name: colVariant, Kind: model.KindText, Nullable: true},
			{Name: colScorer, Kind: model.KindText},
			{Name: colRunStatus, Kind: model.KindText, Indexed: true},
			{Name: colTotal, Kind: model.KindInt},
			{Name: colPassed, Kind: model.KindInt},
			{Name: colFailed, Kind: model.KindInt},
			{Name: colErrors, Kind: model.KindInt},
			{Name: colSkipped, Kind: model.KindInt},
			{Name: colScore, Kind: model.KindFloat},
			{Name: colPassRate, Kind: model.KindFloat},
			{Name: colBaselineRef, Kind: model.KindUUID, Nullable: true},
			{Name: colRegressed, Kind: model.KindBool},
			{Name: colDrift, Kind: model.KindFloat},
			{Name: colStartedAt, Kind: model.KindTimestamp, Indexed: true},
			{Name: colFinishedAt, Kind: model.KindTimestamp, Nullable: true},
			{Name: colLaunchedBy, Kind: model.KindText},
		},
	}); err != nil {
		return err
	}

	if err := reg.Register(model.EntityDescriptor{
		Kind:       resultKind,
		Table:      resultTable,
		AppendOnly: true, // immutable per-case evidence (hash + label, never raw output)
		Fields: []model.FieldSpec{
			{Name: colRunRef, Kind: model.KindUUID, Indexed: true},
			{Name: colCaseKey, Kind: model.KindText, Indexed: true},
			{Name: colResScorer, Kind: model.KindText},
			{Name: colOutcome, Kind: model.KindText, Indexed: true},
			{Name: colResScore, Kind: model.KindFloat},
			{Name: colPassedFlag, Kind: model.KindBool},
			{Name: colDetailHash, Kind: model.KindText, Nullable: true},
			{Name: colLabel, Kind: model.KindText, Nullable: true},
			{Name: colOccurredAt, Kind: model.KindTimestamp},
		},
	}); err != nil {
		return err
	}

	if err := reg.Register(model.EntityDescriptor{
		Kind:    baseKind,
		Table:   baseTable,
		Audited: true, // a baseline pin is a decision; re-pinning is auditable
		Fields: []model.FieldSpec{
			{Name: colSuiteRef, Kind: model.KindUUID, Indexed: true},
			{Name: colSubjectRef, Kind: model.KindText, Indexed: true},
			{Name: colBaseRunRef, Kind: model.KindUUID},
			{Name: colPinnedBy, Kind: model.KindText},
		},
		Indexes: []model.IndexSpec{{
			// One pinned baseline per (tenant, suite, subject). Leads with tenant_id.
			Name:    "evals_baseline_uniq",
			Columns: []string{model.ColTenantID, colSuiteRef, colSubjectRef},
			Unique:  true,
		}},
	}); err != nil {
		return err
	}

	if err := reg.Register(model.EntityDescriptor{
		Kind:    calItemKind,
		Table:   calItemTable,
		Audited: true, // labeling and RE-labeling the human reference are decisions
		Fields: []model.FieldSpec{
			{Name: colSetName, Kind: model.KindText, Indexed: true},
			{Name: colCaseKey, Kind: model.KindText, Indexed: true},
			{Name: colInput, Kind: model.KindText, Nullable: true},
			{Name: colOutput, Kind: model.KindText},
			{Name: colExpected, Kind: model.KindText, Nullable: true},
			{Name: colCriterion, Kind: model.KindText, Nullable: true},
			{Name: colHumanPassed, Kind: model.KindBool},
			{Name: colHumanScore, Kind: model.KindFloat, Nullable: true},
			{Name: colLabeledBy, Kind: model.KindText},
			{Name: colNotes, Kind: model.KindText, Nullable: true},
		},
		Indexes: []model.IndexSpec{{
			// One item per (tenant, set, case_key); re-labeling updates in place.
			Name:    "evals_calib_item_uniq",
			Columns: []string{model.ColTenantID, colSetName, colCaseKey},
			Unique:  true,
		}},
	}); err != nil {
		return err
	}

	if err := reg.Register(model.EntityDescriptor{
		Kind:       calReportKind,
		Table:      calReportTbl,
		AppendOnly: true, // a measured calibration is immutable evidence
		Fields: []model.FieldSpec{
			{Name: colSetName, Kind: model.KindText, Indexed: true},
			{Name: colJudgeModel, Kind: model.KindText, Indexed: true},
			{Name: colRunStatus, Kind: model.KindText},
			{Name: colItemsTotal, Kind: model.KindInt},
			{Name: colItemsScored, Kind: model.KindInt},
			{Name: colItemsError, Kind: model.KindInt},
			{Name: colAgreement, Kind: model.KindFloat},
			{Name: colAgreeLo, Kind: model.KindFloat},
			{Name: colAgreeHi, Kind: model.KindFloat},
			{Name: colKappa, Kind: model.KindFloat},
			{Name: colKappaOK, Kind: model.KindBool},
			{Name: colSens, Kind: model.KindFloat},
			{Name: colSensN, Kind: model.KindInt},
			{Name: colSpec, Kind: model.KindFloat},
			{Name: colSpecN, Kind: model.KindInt},
			{Name: colMeanAbsErr, Kind: model.KindFloat},
			{Name: colVerbCorr, Kind: model.KindFloat},
			{Name: colVerbCorrOK, Kind: model.KindBool},
			{Name: colTarget, Kind: model.KindFloat},
			{Name: colKappaFloor, Kind: model.KindFloat},
			{Name: colMeets, Kind: model.KindBool},
			{Name: colLaunchedBy, Kind: model.KindText},
			{Name: colOccurredAt, Kind: model.KindTimestamp, Indexed: true},
		},
	}); err != nil {
		return err
	}

	if err := reg.Register(model.EntityDescriptor{
		Kind:    gateKind,
		Table:   gateTable,
		Audited: true, // a gate evaluation and (especially) its override are decisions
		Fields: []model.FieldSpec{
			{Name: colSuiteRef, Kind: model.KindUUID, Indexed: true},
			{Name: colSubjectRef, Kind: model.KindText, Indexed: true},
			{Name: colRunRef, Kind: model.KindUUID, Nullable: true},
			{Name: colBaselineRef, Kind: model.KindUUID, Nullable: true},
			{Name: colVerdict, Kind: model.KindText, Indexed: true},
			{Name: colReasons, Kind: model.KindJSON, Nullable: true},
			{Name: colSampled, Kind: model.KindInt},
			{Name: colTotal, Kind: model.KindInt},
			{Name: colSeed, Kind: model.KindText, Nullable: true},
			{Name: colJudgeModel, Kind: model.KindText, Nullable: true},
			{Name: colCalibRef, Kind: model.KindUUID, Nullable: true},
			{Name: colOverridden, Kind: model.KindBool},
			{Name: colOverrideBy, Kind: model.KindText, Nullable: true},
			{Name: colOverrideReason, Kind: model.KindText, Nullable: true},
			{Name: colLaunchedBy, Kind: model.KindText},
			{Name: colOccurredAt, Kind: model.KindTimestamp, Indexed: true},
		},
	}); err != nil {
		return err
	}

	return reg.Register(model.EntityDescriptor{
		Kind:       cacheKind,
		Table:      cacheTable,
		AppendOnly: true, // a verdict for a (hash, model-pin) never changes; new prompt ⇒ new hash
		Fields: []model.FieldSpec{
			{Name: colInputHash, Kind: model.KindText},
			{Name: colJudgeModel, Kind: model.KindText},
			{Name: colResScore, Kind: model.KindFloat},
			{Name: colPassedFlag, Kind: model.KindBool},
			{Name: colReason, Kind: model.KindText, Nullable: true},
			{Name: colOccurredAt, Kind: model.KindTimestamp},
		},
		Indexes: []model.IndexSpec{{
			// One cached verdict per (tenant, input_hash); the hash embeds the model
			// pin + prompt version. A duplicate Create is a benign conflict.
			Name:    "evals_judge_cache_uniq",
			Columns: []string{model.ColTenantID, colInputHash},
			Unique:  true,
		}},
	})
}
