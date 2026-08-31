// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// The OPERATE entities adds to module II. They live in the SAME "sessions"
// namespace as the observe overlay (chose to extend module II), so their
// dotted Kind keeps the namespace hyphen-free and SQL-safe.
const (
	// runKind is one operated Claude Code session (mutable lifecycle row).
	runKind model.Kind = "sessions.run"
	// runEventKind is one immutable lifecycle-ledger event (per-session,
	// queryable, anchored in PayloadHash via the core audit chain).
	runEventKind model.Kind = "sessions.run_event"
)

// Physical tables (namespace_snake, ≤40 chars).
const (
	runTable      = "sessions_run"
	runEventTable = "sessions_run_event"
)

// sessions.run columns. NO column holds a secret, an env value, a prompt, a
// transcript, or process arguments (minimal-data, docs/SECURITY-HARDENING.md). The inference
// token is injected in-memory and discarded; only its non-sensitive id lands.
const (
	colRunRef          = "run_ref"
	colRunName         = "name"
	colTransport       = "transport"
	colPermissionMode  = "permission_mode"
	colEffort          = "effort"
	colRunModelRef     = "model_ref"
	colWorkspaceRef    = "workspace_ref"
	colTemplateID      = "template_id"       // the workspace template whose terms govern this run
	colTemplateVersion = "template_version"  // the REVISION of it this run was launched from
	colTemplateCeiling = "max_duration_secs" // the session-duration ceiling this run was launched under
	colIsolation       = "isolation"
	colState           = "state"
	colClaudeSessionID = "claude_session_id"
	colPID             = "pid"
	colCredentialID    = "credential_id"
	colExitCode        = "exit_code"
	colReason          = "reason"
	colLastEventSeq    = "last_event_seq"
	colStartedAt       = "started_at"
	colLastActivityAt  = "last_activity_at"
	colStoppedAt       = "stopped_at"
	// Governance facts: the non-sensitive launch-decision posture persisted
	// on the run so the portal renders the per-session governance panel without the
	// secrets that decided it. References and flags only — never a token, env value,
	// prompt or transcript (minimal-data, docs/SECURITY-HARDENING.md). The column name "agent_ref"
	// matches the observe entity's column but lives on a different table; the Go const is
	// distinct (colAgentRef is the observe one).
	colRunAgentRef    = "agent_ref"       // the agent NHI dimension the kill-switch/budget scope on
	colPEPProvisioned = "pep_provisioned" // the managed PreToolUse PEP env was injected (tool-calls governed in line)
	colRecordIO       = "record_io"       // the bridged I/O is anchored as governed ledger evidence
	colApprovalRef    = "approval_ref"    // the HITL approval opened for a CRITICAL launch (empty otherwise)
	colCritical       = "critical"        // a privileged launch (drove the HITL + mandatory recording floor)

	// SG-02-b admission stamp: the claim under which this run was last launched. A
	// reference and a counter, never a secret.
	colClaimHolder = "claim_holder"
	colClaimFence  = "claim_fence"
	colRunClaimSID = "claim_sid"

	// G/K3 dual runtime credentials. The bearer values never enter this table:
	// only the two revocation handles, their expiries, and the exact server-owned
	// binding needed to revoke them after a restart are durable.
	colCommunicationWorkspaceID  = "communication_workspace_id"
	colWorkCredentialID          = "work_credential_id"
	colWorkCredentialExpiresAt   = "work_credential_expires_at"
	colCommunicationCredentialID = "communication_credential_id"
	colCommunicationExpiresAt    = "communication_credential_expires_at"
	colRuntimeLaunchID           = "runtime_launch_id"

	// K2 work-kernel binding. These four columns are one nullable stamp: legacy
	// runs have all four NULL, while a work-launched run records the exact
	// WorkItem generation and lease authority under which it was dispatched.
	// The Go names are run-specific because work_schema.go already owns the
	// entity-level work_item_id and owner_epoch constants.
	colRunWorkItemID      = "work_item_id"
	colRunWorkLeaseFence  = "work_lease_fence"
	colRunWorkDispatchKey = "work_dispatch_key"
	colRunWorkOwnerEpoch  = "work_owner_epoch"
	// colRunWorkLaunchSpecHash binds an idempotent K4 dispatch reservation to
	// the complete semantic launch request. The dispatch key deliberately names
	// the WorkItem generation, not every runtime choice; this digest is what
	// makes reusing the same key with a different profile a conflict.
	colRunWorkLaunchSpecHash = "work_launch_spec_hash"
)

// sessions.run_event columns (append-only ledger; per-session hash anchor).
const (
	colEvRunRef      = "run_ref"
	colEvSeq         = "seq"
	colEvAt          = "at"
	colEvEvent       = "event"
	colEvFromState   = "from_state"
	colEvToState     = "to_state"
	colEvDetail      = "detail"
	colEvActor       = "actor"
	colEvActorKind   = "actor_kind"
	colEvPayloadHash = "payload_hash"
	colEvAuditSeq    = "audit_seq"
	colEvWorkItemID  = "work_item_id"
	colEvWorkSID     = "work_holder_sid"
	colEvWorkFence   = "work_lease_fence"
)

// registerRuntimeSchema declares the two operate entities. The engine creates
// the tables, injects the base columns (id/tenant_id/created_at/updated_at/
// version) and attaches the tenant + append-only guards. Called from
// RegisterSchema (schema.go) alongside the observe entities.
func (m *Module) registerRuntimeSchema(reg store.ExtensionRegistry) error {
	if err := reg.Register(model.EntityDescriptor{
		Kind:  runKind,
		Table: runTable,
		Fields: []model.FieldSpec{
			{Name: colRunRef, Kind: model.KindText},
			{Name: colRunName, Kind: model.KindText, Nullable: true},
			{Name: colTransport, Kind: model.KindText},
			{Name: colPermissionMode, Kind: model.KindText},
			{Name: colEffort, Kind: model.KindText, Nullable: true},
			{Name: colRunModelRef, Kind: model.KindText, Nullable: true},
			{Name: colWorkspaceRef, Kind: model.KindText, Nullable: true},
			// the template this run was last launched under. A reference, never the
			// template's body — the terms are re-resolved from the template row on every
			// launch and resume, so a tightened template governs the next relaunch rather
			// than a snapshot nobody can see going stale. Nullable: an untemplated run.
			{Name: colTemplateID, Kind: model.KindText, Nullable: true},
			// The template's store version at launch. A template is MUTABLE, so its id alone
			// does not say what a running child was started under; with the revision an
			// operator can tell an edited template from the terms actually applied, and the
			// launch gate can bind a human approval to the revision it approved.
			{Name: colTemplateVersion, Kind: model.KindInt, Nullable: true},
			// The duration ceiling this run was launched under, in seconds. The timer that
			// ENFORCES it is in-process and does not survive a restart (expireRun says so);
			// persisting the value is what lets a later boot reconcile a child that outlived
			// its ceiling, and lets the panel state the limit rather than imply one.
			{Name: colTemplateCeiling, Kind: model.KindInt, Nullable: true},
			{Name: colIsolation, Kind: model.KindText},
			{Name: colState, Kind: model.KindText, Indexed: true},
			{Name: colClaudeSessionID, Kind: model.KindText, Nullable: true},
			{Name: colPID, Kind: model.KindInt, Nullable: true},
			{Name: colCredentialID, Kind: model.KindText, Nullable: true},
			{Name: colExitCode, Kind: model.KindInt, Nullable: true},
			{Name: colReason, Kind: model.KindText, Nullable: true},
			{Name: colLastEventSeq, Kind: model.KindInt},
			{Name: colStartedAt, Kind: model.KindTimestamp, Nullable: true},
			{Name: colLastActivityAt, Kind: model.KindTimestamp, Nullable: true, Indexed: true},
			{Name: colStoppedAt, Kind: model.KindTimestamp, Nullable: true},
			// Governance facts, all nullable so a row that predates them reads
			// as the safe default (Record.Bool ⇒ false; missing text ⇒ "").
			{Name: colRunAgentRef, Kind: model.KindText, Nullable: true},
			{Name: colPEPProvisioned, Kind: model.KindBool, Nullable: true},
			{Name: colRecordIO, Kind: model.KindBool, Nullable: true},
			{Name: colApprovalRef, Kind: model.KindText, Nullable: true},
			{Name: colCritical, Kind: model.KindBool, Nullable: true},
			// SG-02-b: the admission stamp. The claim this run is operating under, written
			// under that claim's own authority at launch, so a later governed write has a
			// DURABLE thing to compare the live claim against instead of re-reading the
			// current fence and comparing it with itself. Nullable: NULL means a run that
			// predates the control, which the next launch adopts and stamps once.
			{Name: colClaimHolder, Kind: model.KindText, Nullable: true},
			{Name: colClaimFence, Kind: model.KindInt, Nullable: true},
			{Name: colRunClaimSID, Kind: model.KindText, Nullable: true},
			{Name: colCommunicationWorkspaceID, Kind: model.KindUUID, Nullable: true},
			{Name: colWorkCredentialID, Kind: model.KindUUID, Nullable: true},
			{Name: colWorkCredentialExpiresAt, Kind: model.KindTimestamp, Nullable: true},
			{Name: colCommunicationCredentialID, Kind: model.KindUUID, Nullable: true},
			{Name: colCommunicationExpiresAt, Kind: model.KindTimestamp, Nullable: true},
			{Name: colRuntimeLaunchID, Kind: model.KindUUID, Nullable: true},
			// K2: nullable is an expand-contract requirement for historical runs.
			{Name: colRunWorkItemID, Kind: model.KindUUID, Nullable: true},
			{Name: colRunWorkLeaseFence, Kind: model.KindInt, Nullable: true},
			{Name: colRunWorkDispatchKey, Kind: model.KindBytes, Nullable: true},
			{Name: colRunWorkOwnerEpoch, Kind: model.KindInt, Nullable: true},
			{Name: colRunWorkLaunchSpecHash, Kind: model.KindBytes, Nullable: true},
		},
		Indexes: []model.IndexSpec{
			{
				Name:    "sessions_run_ref_uniq",
				Columns: []string{model.ColTenantID, colRunRef},
				Unique:  true,
			},
			{
				Name:    "sessions_run_dispatch_key_uniq",
				Columns: []string{model.ColTenantID, colRunWorkDispatchKey},
				Unique:  true,
			},
		},
	}); err != nil {
		return err
	}
	return reg.Register(model.EntityDescriptor{
		Kind:       runEventKind,
		Table:      runEventTable,
		AppendOnly: true, // immutability: no UPDATE/DELETE (engine triggers/grants)
		Fields: []model.FieldSpec{
			{Name: colEvRunRef, Kind: model.KindText, Indexed: true},
			{Name: colEvSeq, Kind: model.KindInt},
			{Name: colEvAt, Kind: model.KindTimestamp},
			{Name: colEvEvent, Kind: model.KindText},
			{Name: colEvFromState, Kind: model.KindText, Nullable: true},
			{Name: colEvToState, Kind: model.KindText, Nullable: true},
			{Name: colEvDetail, Kind: model.KindText, Nullable: true},
			{Name: colEvActor, Kind: model.KindText, Nullable: true},
			{Name: colEvActorKind, Kind: model.KindText, Nullable: true},
			{Name: colEvPayloadHash, Kind: model.KindText},
			{Name: colEvAuditSeq, Kind: model.KindInt},
			// K2: complete generation under which a fenced runtime action was
			// settled. Nullable keeps historical/non-work events compatible.
			{Name: colEvWorkItemID, Kind: model.KindUUID, Nullable: true},
			{Name: colEvWorkSID, Kind: model.KindText, Nullable: true},
			{Name: colEvWorkFence, Kind: model.KindInt, Nullable: true},
		},
	})
}
