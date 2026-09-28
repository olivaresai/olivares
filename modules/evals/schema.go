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
// Adds four tables: a CALIB_ITEM is
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
	colStoppedComparison = "stopped_comparison"
	colVerdict           = "verdict" // pass|fail|warn
	colReasons           = "reasons" // JSON array of reason codes
	colSampled           = "sampled"
	colSeed              = "seed"
	colCalibRef          = "calibration_ref" // nullable: the report the gate trusted
	colOverridden        = "overridden"
	colOverrideBy        = "override_by"
	colOverrideReason    = "override_reason"
)

// evals_judge_cache columns — one cached judge verdict (append-only). input_hash is
// hashHex(cache-version|judge-model|criterion|input|expected|output): the judge
// model PIN and the prompt version are part of the key, so a model or prompt change
// can never serve a stale verdict.
const (
	colCacheProtocol = "protocol_evidence"
	colInputHash     = "input_hash"
	colReason        = "reason"
)

// Principal declarations shared by several columns below. Each cited line holds
// for every column that uses the declaration.
var (
	// pdeclActorEvidence is the launching or deciding principal's audit actor
	// string ("user:<id>", "token:<id>", or a fixed module/system actor), recorded
	// as provenance and only rendered.
	pdeclActorEvidence  = model.Ref(model.EncodeUserRef, model.ClassEvidence)
	pdeclNoneSuiteRef   = model.None("a suite row id used only for case and pin selection: runs.go:539, baselines.go:114")
	pdeclNoneRunRef     = model.None("an evaluation run id used for result filters, baseline lookups and DTO projection: runs.go:638, comparison.go:131, gate.go:133")
	pdeclNoneSubjectRef = model.None("an evaluated-object ref grouped and projected into eval/finding records, never account resolution: scorecards.go:118, runs.go:208, runs.go:229")
	pdeclNoneScorer     = model.None("a scorer id resolved only in the registered scorer map: scorers.go:103, runs.go:87")
	pdeclNoneJudgeModel = model.None("a judge model ref passed to the Judge and hashed in a cache key: scorers.go:255, gate.go:501")
	pdeclNoneCaseKey    = model.None("a fixture case key clamped and matched as a calibration row key: suites.go:281, calibration.go:164")
	pdeclNoneFixture    = model.None("operator-authored fixture text clamped and passed to a scorer or judge: suites.go:311, runs.go:88, calibration.go:449")
	pdeclNoneSetName    = model.None("a calibration set name matched only as a filter: calibration.go:309")
	pdeclNoneRunStatus  = model.None("a terminal execution status projected into the run DTO and calibration aggregate: runs.go:356, calibration.go:480")
)

// RegisterSchema declares the module's ten owned entities (the SchemaProvider
// seam). The engine creates the tables, injects base columns and attaches the
// tenant/audit/append-only guards; a module cannot opt out of isolation.
//
// Minimal data (docs/SECURITY-HARDENING.md): the suite criterion and a case input/expected are
// operator-authorized, opt-in, NON-PRODUCTION fixtures (a new carve-out analogous to
// the auth partition, see contract §2.1), bounded by the handler before Create;
// a case result carries only a hash of the candidate output + a clamped label, never
// the raw output (from any source).
func (m *Module) RegisterSchema(reg store.ExtensionRegistry) error {
	if err := registerComparisonSchema(reg); err != nil {
		return err
	}
	if err := reg.Register(model.EntityDescriptor{
		Kind:    suiteKind,
		Table:   suiteTable,
		Audited: true, // suite lifecycle (create/archive) is auditable evidence
		Fields: []model.FieldSpec{
			{Name: colName, Kind: model.KindText, Indexed: true, Principal: model.None("a suite name clamped and projected to the DTO: suites.go:73, suites.go:48")},
			{Name: colDescr, Kind: model.KindText, Nullable: true, Principal: model.None("suite description prose clamped and projected to the DTO: suites.go:104, suites.go:48")},
			{Name: colSubjKind, Kind: model.KindText, Principal: model.None("an evaluated-subject kind validated against a closed set: suites.go:79")},
			{Name: colScorer, Kind: model.KindText, Indexed: true, Principal: pdeclNoneScorer},
			{Name: colCriterion, Kind: model.KindText, Nullable: true, Principal: pdeclNoneFixture},
			{Name: colPassThresh, Kind: model.KindFloat},
			{Name: colRegThresh, Kind: model.KindFloat},
			{Name: colJudgeModel, Kind: model.KindText, Nullable: true, Principal: pdeclNoneJudgeModel},
			{Name: colSuiteVer, Kind: model.KindInt},
			{Name: colSuiteStat, Kind: model.KindText, Indexed: true, Principal: model.None("a suite lifecycle status set to active or archived: suites.go:109, suites.go:221")},
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
			{Name: colSuiteRef, Kind: model.KindUUID, Indexed: true, Principal: pdeclNoneSuiteRef},
			{Name: colSuiteVer, Kind: model.KindInt, Indexed: true},
			{Name: colCaseKey, Kind: model.KindText, Indexed: true, Principal: pdeclNoneCaseKey},
			{Name: colInput, Kind: model.KindText, Principal: pdeclNoneFixture},
			{Name: colExpected, Kind: model.KindText, Nullable: true, Principal: pdeclNoneFixture},
			{Name: colWeight, Kind: model.KindFloat},
			{Name: colCaseMeta, Kind: model.KindJSON, Nullable: true, Principal: model.None("free-form scorer configuration passed to ScoreInput and projected to the case DTO: runs.go:89, suites.go:257")},
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
			{Name: colSuiteRef, Kind: model.KindUUID, Indexed: true, Principal: pdeclNoneSuiteRef},
			{Name: colSuiteVer, Kind: model.KindInt},
			{Name: colSubjKind, Kind: model.KindText, Principal: model.None("an evaluated-object kind projected into scorecards and run DTOs: runs.go:355, scorecards.go:156")},
			{Name: colSubjectRef, Kind: model.KindText, Indexed: true, Principal: pdeclNoneSubjectRef},
			{Name: colModelRef, Kind: model.KindText, Nullable: true, Principal: model.None("a declared candidate model clamped and projected to the run DTO: runs.go:172, runs.go:355")},
			{Name: colVariant, Kind: model.KindText, Nullable: true, Principal: model.None("an A/B variant label clamped and grouped in scorecards: runs.go:173, scorecards.go:118")},
			{Name: colScorer, Kind: model.KindText, Principal: pdeclNoneScorer},
			{Name: colRunStatus, Kind: model.KindText, Indexed: true, Principal: pdeclNoneRunStatus},
			{Name: colTotal, Kind: model.KindInt},
			{Name: colPassed, Kind: model.KindInt},
			{Name: colFailed, Kind: model.KindInt},
			{Name: colErrors, Kind: model.KindInt},
			{Name: colSkipped, Kind: model.KindInt},
			{Name: colScore, Kind: model.KindFloat},
			{Name: colPassRate, Kind: model.KindFloat},
			{Name: colBaselineRef, Kind: model.KindUUID, Nullable: true, Principal: pdeclNoneRunRef},
			{Name: colRegressed, Kind: model.KindBool},
			{Name: colDrift, Kind: model.KindFloat},
			{Name: colStartedAt, Kind: model.KindTimestamp, Indexed: true},
			{Name: colFinishedAt, Kind: model.KindTimestamp, Nullable: true},
			{Name: colLaunchedBy, Kind: model.KindText, Principal: pdeclActorEvidence},
		},
	}); err != nil {
		return err
	}

	if err := reg.Register(model.EntityDescriptor{
		Kind:       resultKind,
		Table:      resultTable,
		AppendOnly: true, // immutable per-case evidence (hash + label, never raw output)
		Fields: []model.FieldSpec{
			{Name: colRunRef, Kind: model.KindUUID, Indexed: true, Principal: pdeclNoneRunRef},
			{Name: colCaseKey, Kind: model.KindText, Indexed: true, Principal: pdeclNoneCaseKey},
			{Name: colResScorer, Kind: model.KindText, Principal: pdeclNoneScorer},
			{Name: colOutcome, Kind: model.KindText, Indexed: true, Principal: model.None("a scorer outcome used only to aggregate results: runs.go:96")},
			{Name: colResScore, Kind: model.KindFloat},
			{Name: colPassedFlag, Kind: model.KindBool},
			{Name: colDetailHash, Kind: model.KindText, Nullable: true, Principal: model.None("a result digest computed from output/expected/reason and projected to the result DTO: runs.go:94, runs.go:690")},
			{Name: colLabel, Kind: model.KindText, Nullable: true, Principal: model.None("a scorer reason scrubbed and projected as a result label: runs.go:94, runs.go:254, runs.go:691")},
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
			{Name: colSuiteRef, Kind: model.KindUUID, Indexed: true, Principal: pdeclNoneSuiteRef},
			{Name: colSubjectRef, Kind: model.KindText, Indexed: true, Principal: pdeclNoneSubjectRef},
			{Name: colBaseRunRef, Kind: model.KindUUID, Principal: pdeclNoneRunRef},
			{Name: colPinnedBy, Kind: model.KindText, Principal: pdeclActorEvidence},
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
			{Name: colSetName, Kind: model.KindText, Indexed: true, Principal: pdeclNoneSetName},
			{Name: colCaseKey, Kind: model.KindText, Indexed: true, Principal: pdeclNoneCaseKey},
			{Name: colInput, Kind: model.KindText, Nullable: true, Principal: pdeclNoneFixture},
			{Name: colOutput, Kind: model.KindText, Principal: pdeclNoneFixture},
			{Name: colExpected, Kind: model.KindText, Nullable: true, Principal: pdeclNoneFixture},
			{Name: colCriterion, Kind: model.KindText, Nullable: true, Principal: pdeclNoneFixture},
			{Name: colHumanPassed, Kind: model.KindBool},
			{Name: colHumanScore, Kind: model.KindFloat, Nullable: true},
			{Name: colLabeledBy, Kind: model.KindText, Principal: pdeclActorEvidence},
			{Name: colNotes, Kind: model.KindText, Nullable: true, Principal: model.None("labeler prose clamped and projected to the calibration item DTO: calibration.go:149, calibration.go:76")},
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
			{Name: colSetName, Kind: model.KindText, Indexed: true, Principal: pdeclNoneSetName},
			{Name: colJudgeModel, Kind: model.KindText, Indexed: true, Principal: pdeclNoneJudgeModel},
			{Name: colRunStatus, Kind: model.KindText, Principal: pdeclNoneRunStatus},
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
			{Name: colLaunchedBy, Kind: model.KindText, Principal: pdeclActorEvidence},
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
			{Name: colSuiteRef, Kind: model.KindUUID, Indexed: true, Principal: pdeclNoneSuiteRef},
			{Name: colSubjectRef, Kind: model.KindText, Indexed: true, Principal: pdeclNoneSubjectRef},
			{Name: colRunRef, Kind: model.KindUUID, Nullable: true, Principal: pdeclNoneRunRef},
			{Name: colBaselineRef, Kind: model.KindUUID, Nullable: true, Principal: pdeclNoneRunRef},
			{Name: colStoppedComparison, Kind: model.KindJSON, Nullable: true, Principal: comparisonDeclaration()},
			{Name: colVerdict, Kind: model.KindText, Indexed: true, Principal: model.None("a gate verdict mapped to an effective verdict and projected to the DTO: gate.go:143, gate.go:132")},
			{Name: colReasons, Kind: model.KindJSON, Nullable: true, Principal: model.Nested([]string{}, model.ClassEvidence,
				model.Leaf("[]", model.None("bounded gate reasons appended from comparison diagnostics and projected to the DTO: gate.go:394, gate.go:132")))},
			{Name: colSampled, Kind: model.KindInt},
			{Name: colTotal, Kind: model.KindInt},
			{Name: colSeed, Kind: model.KindText, Nullable: true, Principal: model.None("a sampling seed hashed only to order fixture cases: gate.go:445")},
			{Name: colJudgeModel, Kind: model.KindText, Nullable: true, Principal: pdeclNoneJudgeModel},
			{Name: colCalibRef, Kind: model.KindUUID, Nullable: true, Principal: model.None("a calibration report row id written as evidence and projected to the DTO: gate.go:341, gate.go:140")},
			{Name: colOverridden, Kind: model.KindBool},
			{Name: colOverrideBy, Kind: model.KindText, Nullable: true, Principal: pdeclActorEvidence},
			{Name: colOverrideReason, Kind: model.KindText, Nullable: true, Principal: model.None("operator override prose stored and projected to the gate DTO: gate.go:707, gate.go:136")},
			{Name: colLaunchedBy, Kind: model.KindText, Principal: pdeclActorEvidence},
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
			{Name: colCacheProtocol, Kind: model.KindJSON, Nullable: true, Principal: model.Nested(cacheProtocolEvidence{}, model.ClassEvidence,
				model.Leaf("protocol.implementation", model.None("cached scoring protocol compared structurally to the configured protocol: gate.go:532")),
				model.Leaf("protocol.version", model.None("cached scoring protocol compared structurally to the configured protocol: gate.go:532")),
				model.Leaf("protocol.provider", model.None("cached scoring protocol compared structurally to the configured protocol: gate.go:532")),
				model.Leaf("protocol.model", model.None("cached scoring protocol compared structurally to the configured protocol: gate.go:532")),
				model.Leaf("protocol.config_digest", model.None("cached scoring protocol compared structurally to the configured protocol: gate.go:532")),
				model.Leaf("observed_model", model.None("cached observed model retained then compared to the declared scoring model: gate.go:531, comparison.go:282")))},
			{Name: colInputHash, Kind: model.KindText, Principal: model.None("a judge-input digest used only for a cache lookup: gate.go:501, gate.go:517")},
			{Name: colJudgeModel, Kind: model.KindText, Principal: pdeclNoneJudgeModel},
			{Name: colResScore, Kind: model.KindFloat},
			{Name: colPassedFlag, Kind: model.KindBool},
			{Name: colReason, Kind: model.KindText, Nullable: true, Principal: model.None("a cached judge reason projected into a verdict and clamped on persistence: gate.go:525, gate.go:570")},
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
