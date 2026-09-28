// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package redteam

import (
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// Owned entity kinds and tables. All within the 40-char module-table cap:
// the longest, redteam_result, is 14 chars. A TARGET is the consent record (mutable
// authorization lifecycle); a RUN and its RESULTS are APPEND-ONLY because a
// robustness assessment is tamper-evident evidence (docs/SECURITY-HARDENING.md) and a regression
// baseline a later run is compared against — it must not be silently rewritten.
const (
	targetKind  model.Kind = "redteam.target"
	targetTable            = "redteam_target"
	runKind     model.Kind = "redteam.run"
	runTable               = "redteam_run"
	resultKind  model.Kind = "redteam.result"
	resultTable            = "redteam_result"
)

// redteam_target columns — a client-governed agent registered for testing (consent).
const (
	colAgentRef      = "agent_ref"
	colName          = "name"
	colEndpoint      = "endpoint" // opaque handle the sandbox uses (never a secret)
	colScope         = "scope"    // the authorized scope of testing
	colAuthorized    = "authorized"
	colAuthorizedBy  = "authorized_by"
	colAuthorizedAt  = "authorized_at"
	colTargetStatus  = "status" // "registered" | "authorized" | "revoked"
	colTargetCreated = "created_by"
)

// redteam_run columns — one execution of a battery against a target (append-only).
const (
	colTargetRef  = "target_ref"
	colSuite      = "suite"
	colRunStatus  = "run_status" // "completed" | "degraded" | "error"
	colTotal      = "total"
	colPassed     = "passed"
	colFailed     = "failed"
	colErrors     = "errors"
	colSkipped    = "skipped"
	colScore      = "score" // 0..100 robustness
	colStartedAt  = "started_at"
	colFinishedAt = "finished_at"
	colSummaryH   = "summary_hash"
	colLaunchedBy = "launched_by"
)

// redteam_result columns — one probe outcome within a run (append-only).
const (
	colRunRef     = "run_ref"
	colProbeID    = "probe_id"
	colFamily     = "family"
	colOWASP      = "owasp"
	colATLAS      = "atlas"
	colOutcome    = "outcome"
	colSeverity   = "severity"
	colDetailHash = "detail_hash"
	colOccurredAt = "occurred_at"
)

// Principal declarations shared by more than one column below.
var (
	// pdeclNoneDigest is a one-way hex digest; nothing reads a principal back out.
	pdeclNoneDigest = model.None("a one-way SHA-256 hex digest: helpers.go:148-151, scorecard.go:169, scorecard.go:191")
	// pdeclNoneFrameworkRef is a framework reference id taken from the fixed probe catalog.
	pdeclNoneFrameworkRef = model.None("an OWASP or ATLAS reference id from the fixed probe catalog: ports.go:29-31, scorecard.go:190")
)

// RegisterSchema declares the module's three owned entities (the SchemaProvider
// seam). The engine creates the tables, injects base columns and attaches the
// tenant/audit/append-only guards; a module cannot opt out of isolation.
//
// Minimal data (docs/SECURITY-HARDENING.md): a target's endpoint is an opaque handle, never a
// credential; a result carries a hash of any sensitive detail, never the raw probe
// payload or the target's raw response.
func (m *Module) RegisterSchema(reg store.ExtensionRegistry) error {
	if err := reg.Register(model.EntityDescriptor{
		Kind:  targetKind,
		Table: targetTable,
		Fields: []model.FieldSpec{
			{Name: colAgentRef, Kind: model.KindText, Indexed: true, Principal: model.None("an agent's canonical id or source external id, resolved only against the tenant's agent inventory: ownership.go:26-47")},
			{Name: colName, Kind: model.KindText, Principal: model.None("a display label that defaults to the agent reference and is only rendered: consent.go:75-78, consent.go:41")},
			{Name: colEndpoint, Kind: model.KindText, Nullable: true, Principal: model.None("the network endpoint of the agent under test, parsed only into an egress host rule: cmd/olivares/sandboxrt.go:118-122")},
			{Name: colScope, Kind: model.KindText, Nullable: true, Principal: model.None("free-text testing scope, rendered only and never handed to the sandbox: consent.go:42, cmd/olivares/sandboxrt.go:115-133")},
			{Name: colAuthorized, Kind: model.KindBool},
			{Name: colAuthorizedBy, Kind: model.KindText, Nullable: true, Principal: model.Ref(model.EncodeUserRef, model.ClassEvidence)},
			{Name: colAuthorizedAt, Kind: model.KindTimestamp, Nullable: true},
			{Name: colTargetStatus, Kind: model.KindText, Indexed: true, Principal: model.None("a closed status set registered|authorized|revoked: consent.go:99, consent.go:161, consent.go:165")},
			{Name: colTargetCreated, Kind: model.KindText, Principal: model.Ref(model.EncodeUserRef, model.ClassEvidence)},
		},
		Indexes: []model.IndexSpec{{
			// One target per (tenant, agent_ref). Unique index leads with tenant_id.
			Name:    "redteam_target_uniq",
			Columns: []string{model.ColTenantID, colAgentRef},
			Unique:  true,
		}},
	}); err != nil {
		return err
	}

	if err := reg.Register(model.EntityDescriptor{
		Kind:       runKind,
		Table:      runTable,
		AppendOnly: true, // tamper-evident robustness evidence + regression baseline
		Fields: []model.FieldSpec{
			{Name: colTargetRef, Kind: model.KindUUID, Indexed: true, Principal: model.None("the id of a red-team target row: scorecard.go:98, scorecard.go:171")},
			{Name: colSuite, Kind: model.KindText, Indexed: true, Principal: model.None("a closed suite set: battery.go:26-27, scorecard.go:107")},
			{Name: colRunStatus, Kind: model.KindText, Indexed: true, Principal: model.None("a closed status set completed|degraded|error: scorecard.go:67-78")},
			{Name: colTotal, Kind: model.KindInt},
			{Name: colPassed, Kind: model.KindInt},
			{Name: colFailed, Kind: model.KindInt},
			{Name: colErrors, Kind: model.KindInt},
			{Name: colSkipped, Kind: model.KindInt},
			{Name: colScore, Kind: model.KindFloat},
			{Name: colStartedAt, Kind: model.KindTimestamp, Indexed: true},
			{Name: colFinishedAt, Kind: model.KindTimestamp, Nullable: true},
			{Name: colSummaryH, Kind: model.KindText, Nullable: true, Principal: pdeclNoneDigest},
			{Name: colLaunchedBy, Kind: model.KindText, Principal: model.Ref(model.EncodeUserRef, model.ClassEvidence)},
		},
	}); err != nil {
		return err
	}

	return reg.Register(model.EntityDescriptor{
		Kind:       resultKind,
		Table:      resultTable,
		AppendOnly: true, // immutable per-probe evidence
		Fields: []model.FieldSpec{
			{Name: colRunRef, Kind: model.KindUUID, Indexed: true, Principal: model.None("the id of a red-team run row: scorecard.go:180, scorecard.go:189")},
			{Name: colProbeID, Kind: model.KindText, Indexed: true, Principal: model.None("a probe id from the fixed probe catalog: attacks_injection.go:18, scorecard.go:189")},
			{Name: colFamily, Kind: model.KindText, Indexed: true, Principal: model.None("a closed probe family set: battery.go:18-23")},
			{Name: colOWASP, Kind: model.KindText, Nullable: true, Principal: pdeclNoneFrameworkRef},
			{Name: colATLAS, Kind: model.KindText, Nullable: true, Principal: pdeclNoneFrameworkRef},
			{Name: colOutcome, Kind: model.KindText, Indexed: true, Principal: model.None("a closed outcome set: ports.go:63-70, battery.go:67-68")},
			{Name: colSeverity, Kind: model.KindText, Principal: model.None("a closed severity set: helpers.go:173-183")},
			{Name: colDetailHash, Kind: model.KindText, Nullable: true, Principal: pdeclNoneDigest},
			{Name: colOccurredAt, Kind: model.KindTimestamp},
		},
	})
}
