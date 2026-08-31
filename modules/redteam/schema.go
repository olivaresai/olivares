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
			{Name: colAgentRef, Kind: model.KindText, Indexed: true},
			{Name: colName, Kind: model.KindText},
			{Name: colEndpoint, Kind: model.KindText, Nullable: true},
			{Name: colScope, Kind: model.KindText, Nullable: true},
			{Name: colAuthorized, Kind: model.KindBool},
			{Name: colAuthorizedBy, Kind: model.KindText, Nullable: true},
			{Name: colAuthorizedAt, Kind: model.KindTimestamp, Nullable: true},
			{Name: colTargetStatus, Kind: model.KindText, Indexed: true},
			{Name: colTargetCreated, Kind: model.KindText},
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
			{Name: colTargetRef, Kind: model.KindUUID, Indexed: true},
			{Name: colSuite, Kind: model.KindText, Indexed: true},
			{Name: colRunStatus, Kind: model.KindText, Indexed: true},
			{Name: colTotal, Kind: model.KindInt},
			{Name: colPassed, Kind: model.KindInt},
			{Name: colFailed, Kind: model.KindInt},
			{Name: colErrors, Kind: model.KindInt},
			{Name: colSkipped, Kind: model.KindInt},
			{Name: colScore, Kind: model.KindFloat},
			{Name: colStartedAt, Kind: model.KindTimestamp, Indexed: true},
			{Name: colFinishedAt, Kind: model.KindTimestamp, Nullable: true},
			{Name: colSummaryH, Kind: model.KindText, Nullable: true},
			{Name: colLaunchedBy, Kind: model.KindText},
		},
	}); err != nil {
		return err
	}

	return reg.Register(model.EntityDescriptor{
		Kind:       resultKind,
		Table:      resultTable,
		AppendOnly: true, // immutable per-probe evidence
		Fields: []model.FieldSpec{
			{Name: colRunRef, Kind: model.KindUUID, Indexed: true},
			{Name: colProbeID, Kind: model.KindText, Indexed: true},
			{Name: colFamily, Kind: model.KindText, Indexed: true},
			{Name: colOWASP, Kind: model.KindText, Nullable: true},
			{Name: colATLAS, Kind: model.KindText, Nullable: true},
			{Name: colOutcome, Kind: model.KindText, Indexed: true},
			{Name: colSeverity, Kind: model.KindText},
			{Name: colDetailHash, Kind: model.KindText, Nullable: true},
			{Name: colOccurredAt, Kind: model.KindTimestamp},
		},
	})
}
