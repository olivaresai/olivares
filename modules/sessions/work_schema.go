// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"embed"
	"io/fs"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// The work kernel is deliberately part of sessions. A WorkItem is the durable
// unit coordinated across session lifetimes; it is not a second scheduler and
// it does not grant a session authority merely because it names that session.
const (
	workItemKind         model.Kind = "sessions.work_item"
	workDependencyKind   model.Kind = "sessions.work_dependency"
	workAcceptanceKind   model.Kind = "sessions.work_acceptance"
	workDecisionKind     model.Kind = "sessions.work_decision"
	workDecisionHeadKind model.Kind = "sessions.work_decision_head"
	workLeaseKind        model.Kind = "sessions.work_lease"
	workCommandKind      model.Kind = "sessions.work_command"
	workEventKind        model.Kind = "sessions.work_event"
	workOutboxKind       model.Kind = "sessions.work_outbox"
	workGuardKind        model.Kind = "sessions.work_guard"
)

const (
	workItemTable         = "sessions_work_item"
	workDependencyTable   = "sessions_work_dependency"
	workAcceptanceTable   = "sessions_work_acceptance"
	workDecisionTable     = "sessions_work_decision"
	workDecisionHeadTable = "sessions_work_decision_head"
	workLeaseTable        = "sessions_work_lease"
	workCommandTable      = "sessions_work_command"
	workEventTable        = "sessions_work_event"
	workOutboxTable       = "sessions_work_outbox"
	workGuardTable        = "sessions_work_guard"
)

const (
	colLeaseHolderSID      = "holder_sid"
	colLeaseHolderRunRef   = "holder_run_ref"
	colLeaseHolderAgentRef = "holder_agent_ref"
	colLeaseFence          = "fence"
	colLeaseState          = "state"
	colLeaseAcquiredAt     = "acquired_at"
	colLeaseRenewedAt      = "renewed_at"
	colLeaseExpiresAt      = "expires_at"
	colLeaseEndedAt        = "ended_at"
	colLeaseEndReason      = "end_reason"
	colLeaseRenewalCount   = "renewal_count"
)

const (
	colWorkWorkspaceID        = "workspace_id"
	colWorkItemID             = "work_item_id"
	colWorkKind               = "work_kind"
	colWorkTitle              = "title"
	colWorkBrief              = "brief_md"
	colWorkBriefHash          = "brief_hash"
	colWorkContextRefs        = "context_refs"
	colWorkStatus             = "status"
	colWorkPriority           = "priority"
	colWorkOwnerKind          = "owner_kind"
	colWorkOwnerRef           = "owner_ref"
	colWorkOwnerEpoch         = "owner_epoch"
	colWorkProvKind           = "provenance_kind"
	colWorkProvRef            = "provenance_ref"
	colWorkProvHash           = "provenance_hash"
	colWorkParentID           = "parent_id"
	colWorkSupersedesID       = "supersedes_id"
	colWorkAcceptanceRevision = "acceptance_revision"
	colWorkBlockedCode        = "blocked_code"
	colWorkBlockedReason      = "blocked_reason"
	colWorkTerminalCode       = "terminal_code"
	colWorkTerminalReason     = "terminal_reason"
	colWorkDueAt              = "due_at"
	colWorkReadyAt            = "ready_at"
	colWorkStartedAt          = "started_at"
	colWorkReviewAt           = "review_at"
	colWorkTerminalAt         = "terminal_at"
	colWorkArchivedAt         = "archived_at"
	colWorkLastEventSeq       = "last_event_seq"
)

const (
	colDepDependsOnID   = "depends_on_id"
	colDepRelation      = "relation"
	colDepActive        = "active"
	colDepAddedByKind   = "added_by_kind"
	colDepAddedByRef    = "added_by_ref"
	colDepRemovedByKind = "removed_by_kind"
	colDepRemovedByRef  = "removed_by_ref"
	colDepRemovedAt     = "removed_at"
)

const (
	colAccKey              = "criterion_key"
	colAccOrdinal          = "ordinal"
	colAccStatement        = "statement"
	colAccRequired         = "required"
	colAccState            = "state"
	colAccEvidenceRef      = "evidence_ref"
	colAccEvidenceHash     = "evidence_hash"
	colAccVerifiedByKind   = "verified_by_kind"
	colAccVerifiedByRef    = "verified_by_ref"
	colAccVerifiedAt       = "verified_at"
	colAccWaiverDecisionID = "waiver_decision_id"
)

const (
	colDecisionKey          = "decision_key"
	colDecisionSeq          = "decision_seq"
	colDecisionSubjectKind  = "subject_kind"
	colDecisionSubjectRef   = "subject_ref"
	colDecisionOperation    = "operation"
	colDecisionStatement    = "statement_md"
	colDecisionRationale    = "rationale_md"
	colDecisionByKind       = "decided_by_kind"
	colDecisionByRef        = "decided_by_ref"
	colDecisionAuthority    = "authority_ref"
	colDecisionSupersedesID = "supersedes_id"
	colDecisionRevokesID    = "revokes_id"
	colDecisionEffectiveAt  = "effective_at"
	colDecisionHash         = "decision_hash"
	colDecisionCurrentID    = "current_decision_id"
	colDecisionCurrentSeq   = "current_seq"
	colDecisionHeadState    = "state"
	colDecisionHeadHash     = "head_hash"
)

const (
	colCommandID           = "command_id"
	colCommandActorFP      = "actor_fingerprint"
	colCommandScope        = "command_scope"
	colCommandIdempotency  = "idempotency_key_hash"
	colCommandRequestHash  = "request_hash"
	colCommandPlanHash     = "plan_hash"
	colCommandResultKind   = "result_kind"
	colCommandResultID     = "result_id"
	colCommandHTTPStatus   = "http_status"
	colCommandResponse     = "response_json"
	colCommandResponseHash = "response_hash"
	colCommandAuditSeq     = "audit_seq"
	colCommandAuditHash    = "audit_hash"
	colCommandCompletedAt  = "completed_at"
)

const (
	colEventID            = "event_id"
	colEventAggregateKind = "aggregate_kind"
	colEventAggregateID   = "aggregate_id"
	colEventSeq           = "seq"
	colEventType          = "event_type"
	colEventActorKind     = "actor_kind"
	colEventActorRef      = "actor_ref"
	colEventOccurredAt    = "occurred_at"
	colEventPayload       = "payload_json"
	colEventPayloadHash   = "payload_hash"
	colEventCommandID     = "command_id"
	colEventAuditSeq      = "audit_seq"
	colEventAuditHash     = "audit_hash"
)

const (
	colOutboxEventID       = "event_id"
	colOutboxState         = "state"
	colOutboxAttempts      = "attempts"
	colOutboxNextAttemptAt = "next_attempt_at"
	colOutboxClaimOwner    = "claim_owner"
	colOutboxClaimUntil    = "claim_until"
	colOutboxPublishedAt   = "published_at"
	colOutboxLastOutcome   = "last_outcome"
	colGuardKind           = "guard_kind"
	colGuardEpoch          = "epoch"
	colGuardLastDBTime     = "last_db_time"
	colGuardRebaseDecision = "clock_rebase_decision_id"
	colGuardRebaseEvidence = "clock_rebase_evidence_ref"
)

//go:embed migrations/postgres/*.sql migrations/sqlite/*.sql
var sessionsMigrationsFS embed.FS

var hiddenWorkspaceLineage = model.WorkspaceLineageSpec{
	Column: colWorkWorkspaceID, Encoding: model.WorkspaceLineageID, Unset: model.WorkspaceUnsetHidden,
}

func workFields(extra ...model.FieldSpec) []model.FieldSpec {
	return append([]model.FieldSpec{{Name: colWorkWorkspaceID, Kind: model.KindUUID}}, extra...)
}

func workIndexes(name string, extra ...model.IndexSpec) []model.IndexSpec {
	base := model.IndexSpec{Name: name, Columns: []string{
		model.ColTenantID, colWorkWorkspaceID, model.ColID,
	}}
	return append([]model.IndexSpec{base}, extra...)
}

// registerWorkSchema declares the K1 durable-work entities plus K2's distinct
// WorkLease aggregate. Communication and protocol binding remain later cuts and
// cannot be inferred from these rows.
func (m *Module) registerWorkSchema(reg store.ExtensionRegistry) error {
	descriptors := []model.EntityDescriptor{
		{
			Kind: workItemKind, Table: workItemTable, RetainOnTenantDrop: true, WorkspaceLineage: hiddenWorkspaceLineage,
			Fields: workFields(
				model.FieldSpec{Name: colWorkKind, Kind: model.KindText},
				model.FieldSpec{Name: colWorkTitle, Kind: model.KindText},
				model.FieldSpec{Name: colWorkBrief, Kind: model.KindText},
				model.FieldSpec{Name: colWorkBriefHash, Kind: model.KindBytes},
				model.FieldSpec{Name: colWorkContextRefs, Kind: model.KindJSON},
				model.FieldSpec{Name: colWorkStatus, Kind: model.KindText},
				model.FieldSpec{Name: colWorkPriority, Kind: model.KindText},
				model.FieldSpec{Name: colWorkOwnerKind, Kind: model.KindText},
				model.FieldSpec{Name: colWorkOwnerRef, Kind: model.KindText},
				model.FieldSpec{Name: colWorkOwnerEpoch, Kind: model.KindInt},
				model.FieldSpec{Name: colWorkProvKind, Kind: model.KindText},
				model.FieldSpec{Name: colWorkProvRef, Kind: model.KindText},
				model.FieldSpec{Name: colWorkProvHash, Kind: model.KindBytes, Nullable: true},
				model.FieldSpec{Name: colWorkParentID, Kind: model.KindUUID, Nullable: true},
				model.FieldSpec{Name: colWorkSupersedesID, Kind: model.KindUUID, Nullable: true},
				model.FieldSpec{Name: colWorkAcceptanceRevision, Kind: model.KindInt},
				model.FieldSpec{Name: colWorkBlockedCode, Kind: model.KindText, Nullable: true},
				model.FieldSpec{Name: colWorkBlockedReason, Kind: model.KindText, Nullable: true},
				model.FieldSpec{Name: colWorkTerminalCode, Kind: model.KindText, Nullable: true},
				model.FieldSpec{Name: colWorkTerminalReason, Kind: model.KindText, Nullable: true},
				model.FieldSpec{Name: colWorkDueAt, Kind: model.KindTimestamp, Nullable: true},
				model.FieldSpec{Name: colWorkReadyAt, Kind: model.KindTimestamp, Nullable: true},
				model.FieldSpec{Name: colWorkStartedAt, Kind: model.KindTimestamp, Nullable: true},
				model.FieldSpec{Name: colWorkReviewAt, Kind: model.KindTimestamp, Nullable: true},
				model.FieldSpec{Name: colWorkTerminalAt, Kind: model.KindTimestamp, Nullable: true},
				model.FieldSpec{Name: colWorkArchivedAt, Kind: model.KindTimestamp, Nullable: true},
				model.FieldSpec{Name: colWorkLastEventSeq, Kind: model.KindInt},
			),
			Indexes: workIndexes("sessions_work_item_workspace",
				model.IndexSpec{Name: "sessions_work_item_state", Columns: []string{model.ColTenantID, colWorkStatus, colWorkPriority, model.ColID}},
				model.IndexSpec{Name: "sessions_work_item_owner", Columns: []string{model.ColTenantID, colWorkWorkspaceID, colWorkOwnerKind, colWorkOwnerRef, colWorkStatus, model.ColID}},
				model.IndexSpec{Name: "sessions_work_item_provenance", Columns: []string{model.ColTenantID, colWorkProvKind, colWorkProvRef, model.ColID}},
				model.IndexSpec{Name: "sessions_work_item_parent", Columns: []string{model.ColTenantID, colWorkParentID, model.ColID}},
				model.IndexSpec{Name: "sessions_work_item_blocked", Columns: []string{model.ColTenantID, colWorkStatus, colWorkBlockedCode, model.ColID}},
				model.IndexSpec{Name: "sessions_work_item_due", Columns: []string{model.ColTenantID, colWorkDueAt, model.ColID}},
				model.IndexSpec{Name: "sessions_work_item_archived", Columns: []string{model.ColTenantID, colWorkArchivedAt, model.ColID}},
			),
		},
		{
			Kind: workDependencyKind, Table: workDependencyTable, RetainOnTenantDrop: true, WorkspaceLineage: hiddenWorkspaceLineage,
			Fields: workFields(
				model.FieldSpec{Name: colWorkItemID, Kind: model.KindUUID},
				model.FieldSpec{Name: colDepDependsOnID, Kind: model.KindUUID},
				model.FieldSpec{Name: colDepRelation, Kind: model.KindText},
				model.FieldSpec{Name: colDepActive, Kind: model.KindBool},
				model.FieldSpec{Name: colDepAddedByKind, Kind: model.KindText},
				model.FieldSpec{Name: colDepAddedByRef, Kind: model.KindText},
				model.FieldSpec{Name: colDepRemovedByKind, Kind: model.KindText, Nullable: true},
				model.FieldSpec{Name: colDepRemovedByRef, Kind: model.KindText, Nullable: true},
				model.FieldSpec{Name: colDepRemovedAt, Kind: model.KindTimestamp, Nullable: true},
			),
			Indexes: workIndexes("sessions_work_dependency_workspace",
				model.IndexSpec{Name: "sessions_work_dependency_uniq", Columns: []string{model.ColTenantID, colWorkItemID, colDepDependsOnID, colDepRelation}, Unique: true},
				model.IndexSpec{Name: "sessions_work_dependency_from", Columns: []string{model.ColTenantID, colWorkWorkspaceID, colDepActive, colWorkItemID}},
				model.IndexSpec{Name: "sessions_work_dependency_to", Columns: []string{model.ColTenantID, colDepDependsOnID, colDepActive, model.ColID}},
			),
		},
		{
			Kind: workAcceptanceKind, Table: workAcceptanceTable, RetainOnTenantDrop: true, WorkspaceLineage: hiddenWorkspaceLineage,
			Fields: workFields(
				model.FieldSpec{Name: colWorkItemID, Kind: model.KindUUID},
				model.FieldSpec{Name: colAccKey, Kind: model.KindText},
				model.FieldSpec{Name: colAccOrdinal, Kind: model.KindInt},
				model.FieldSpec{Name: colAccStatement, Kind: model.KindText},
				model.FieldSpec{Name: colAccRequired, Kind: model.KindBool},
				model.FieldSpec{Name: colAccState, Kind: model.KindText},
				model.FieldSpec{Name: colAccEvidenceRef, Kind: model.KindText, Nullable: true},
				model.FieldSpec{Name: colAccEvidenceHash, Kind: model.KindBytes, Nullable: true},
				model.FieldSpec{Name: colAccVerifiedByKind, Kind: model.KindText, Nullable: true},
				model.FieldSpec{Name: colAccVerifiedByRef, Kind: model.KindText, Nullable: true},
				model.FieldSpec{Name: colAccVerifiedAt, Kind: model.KindTimestamp, Nullable: true},
				model.FieldSpec{Name: colAccWaiverDecisionID, Kind: model.KindUUID, Nullable: true},
			),
			Indexes: workIndexes("sessions_work_acceptance_workspace",
				model.IndexSpec{Name: "sessions_work_acceptance_uniq", Columns: []string{model.ColTenantID, colWorkItemID, colAccKey}, Unique: true},
				model.IndexSpec{Name: "sessions_work_acceptance_state", Columns: []string{model.ColTenantID, colWorkItemID, colAccState, model.ColID}},
				model.IndexSpec{Name: "sessions_work_acceptance_evidence", Columns: []string{model.ColTenantID, colAccEvidenceHash, model.ColID}},
			),
		},
		{
			Kind: workDecisionKind, Table: workDecisionTable, AppendOnly: true, WorkspaceLineage: hiddenWorkspaceLineage,
			Fields: workFields(
				model.FieldSpec{Name: colWorkItemID, Kind: model.KindUUID},
				model.FieldSpec{Name: colDecisionKey, Kind: model.KindText},
				model.FieldSpec{Name: colDecisionSeq, Kind: model.KindInt},
				model.FieldSpec{Name: colDecisionSubjectKind, Kind: model.KindText},
				model.FieldSpec{Name: colDecisionSubjectRef, Kind: model.KindText},
				model.FieldSpec{Name: colDecisionOperation, Kind: model.KindText},
				model.FieldSpec{Name: colDecisionStatement, Kind: model.KindText},
				model.FieldSpec{Name: colDecisionRationale, Kind: model.KindText},
				model.FieldSpec{Name: colDecisionByKind, Kind: model.KindText},
				model.FieldSpec{Name: colDecisionByRef, Kind: model.KindText},
				model.FieldSpec{Name: colDecisionAuthority, Kind: model.KindText},
				model.FieldSpec{Name: colDecisionSupersedesID, Kind: model.KindUUID, Nullable: true},
				model.FieldSpec{Name: colDecisionRevokesID, Kind: model.KindUUID, Nullable: true},
				model.FieldSpec{Name: colDecisionEffectiveAt, Kind: model.KindTimestamp},
				model.FieldSpec{Name: colDecisionHash, Kind: model.KindBytes},
			),
			Indexes: workIndexes("sessions_work_decision_workspace",
				model.IndexSpec{Name: "sessions_work_decision_uniq", Columns: []string{model.ColTenantID, colWorkItemID, colDecisionKey, colDecisionSeq}, Unique: true},
				model.IndexSpec{Name: "sessions_work_decision_subject", Columns: []string{model.ColTenantID, colDecisionSubjectKind, colDecisionSubjectRef, model.ColID}},
				model.IndexSpec{Name: "sessions_work_decision_actor", Columns: []string{model.ColTenantID, colDecisionByKind, colDecisionByRef, model.ColID}},
			),
		},
		{
			Kind: workDecisionHeadKind, Table: workDecisionHeadTable, RetainOnTenantDrop: true, WorkspaceLineage: hiddenWorkspaceLineage,
			Fields: workFields(
				model.FieldSpec{Name: colWorkItemID, Kind: model.KindUUID},
				model.FieldSpec{Name: colDecisionKey, Kind: model.KindText},
				model.FieldSpec{Name: colDecisionCurrentID, Kind: model.KindUUID},
				model.FieldSpec{Name: colDecisionCurrentSeq, Kind: model.KindInt},
				model.FieldSpec{Name: colDecisionHeadState, Kind: model.KindText},
				model.FieldSpec{Name: colDecisionHeadHash, Kind: model.KindBytes},
			),
			Indexes: workIndexes("sessions_work_decision_head_workspace", model.IndexSpec{Name: "sessions_work_decision_head_uniq", Columns: []string{model.ColTenantID, colWorkItemID, colDecisionKey}, Unique: true}),
		},
		{
			Kind: workLeaseKind, Table: workLeaseTable, RetainOnTenantDrop: true, WorkspaceLineage: hiddenWorkspaceLineage,
			Fields: workFields(
				model.FieldSpec{Name: colWorkItemID, Kind: model.KindUUID},
				model.FieldSpec{Name: colLeaseHolderSID, Kind: model.KindText, Nullable: true},
				model.FieldSpec{Name: colLeaseHolderRunRef, Kind: model.KindText, Nullable: true},
				model.FieldSpec{Name: colLeaseHolderAgentRef, Kind: model.KindText, Nullable: true},
				model.FieldSpec{Name: colLeaseFence, Kind: model.KindInt},
				model.FieldSpec{Name: colLeaseState, Kind: model.KindText},
				model.FieldSpec{Name: colLeaseAcquiredAt, Kind: model.KindTimestamp, Nullable: true},
				model.FieldSpec{Name: colLeaseRenewedAt, Kind: model.KindTimestamp, Nullable: true},
				model.FieldSpec{Name: colLeaseExpiresAt, Kind: model.KindTimestamp, Nullable: true},
				model.FieldSpec{Name: colLeaseEndedAt, Kind: model.KindTimestamp, Nullable: true},
				model.FieldSpec{Name: colLeaseEndReason, Kind: model.KindText, Nullable: true},
				model.FieldSpec{Name: colLeaseRenewalCount, Kind: model.KindInt},
			),
			Indexes: workIndexes("sessions_work_lease_workspace",
				model.IndexSpec{Name: "sessions_work_lease_item_uniq", Columns: []string{model.ColTenantID, colWorkItemID}, Unique: true},
				model.IndexSpec{Name: "sessions_work_lease_expiry", Columns: []string{model.ColTenantID, colLeaseState, colLeaseExpiresAt, model.ColID}},
				model.IndexSpec{Name: "sessions_work_lease_holder", Columns: []string{model.ColTenantID, colLeaseHolderSID, colLeaseState, model.ColID}},
			),
		},
		{
			Kind: workCommandKind, Table: workCommandTable, AppendOnly: true, WorkspaceLineage: hiddenWorkspaceLineage,
			Fields: workFields(
				model.FieldSpec{Name: colCommandID, Kind: model.KindUUID},
				model.FieldSpec{Name: colCommandActorFP, Kind: model.KindBytes},
				model.FieldSpec{Name: colCommandScope, Kind: model.KindText},
				model.FieldSpec{Name: colCommandIdempotency, Kind: model.KindBytes},
				model.FieldSpec{Name: colCommandRequestHash, Kind: model.KindBytes},
				model.FieldSpec{Name: colCommandPlanHash, Kind: model.KindBytes},
				model.FieldSpec{Name: colCommandResultKind, Kind: model.KindText},
				model.FieldSpec{Name: colCommandResultID, Kind: model.KindUUID, Nullable: true},
				model.FieldSpec{Name: colCommandHTTPStatus, Kind: model.KindInt},
				model.FieldSpec{Name: colCommandResponse, Kind: model.KindJSON},
				model.FieldSpec{Name: colCommandResponseHash, Kind: model.KindBytes},
				model.FieldSpec{Name: colCommandAuditSeq, Kind: model.KindInt},
				model.FieldSpec{Name: colCommandAuditHash, Kind: model.KindBytes},
				model.FieldSpec{Name: colCommandCompletedAt, Kind: model.KindTimestamp},
			),
			Indexes: workIndexes("sessions_work_command_workspace",
				model.IndexSpec{Name: "sessions_work_command_idem", Columns: []string{model.ColTenantID, colCommandActorFP, colCommandScope, colCommandIdempotency}, Unique: true},
				model.IndexSpec{Name: "sessions_work_command_id_uniq", Columns: []string{model.ColTenantID, colCommandID}, Unique: true},
			),
		},
		{
			Kind: workEventKind, Table: workEventTable, AppendOnly: true, WorkspaceLineage: hiddenWorkspaceLineage,
			Fields: workFields(
				model.FieldSpec{Name: colEventID, Kind: model.KindUUID},
				model.FieldSpec{Name: colEventAggregateKind, Kind: model.KindText},
				model.FieldSpec{Name: colEventAggregateID, Kind: model.KindUUID},
				model.FieldSpec{Name: colEventSeq, Kind: model.KindInt},
				model.FieldSpec{Name: colEventType, Kind: model.KindText},
				model.FieldSpec{Name: colEventActorKind, Kind: model.KindText},
				model.FieldSpec{Name: colEventActorRef, Kind: model.KindText},
				model.FieldSpec{Name: colEventOccurredAt, Kind: model.KindTimestamp},
				model.FieldSpec{Name: colEventPayload, Kind: model.KindJSON},
				model.FieldSpec{Name: colEventPayloadHash, Kind: model.KindBytes},
				model.FieldSpec{Name: colEventCommandID, Kind: model.KindUUID},
				model.FieldSpec{Name: colEventAuditSeq, Kind: model.KindInt},
				model.FieldSpec{Name: colEventAuditHash, Kind: model.KindBytes},
			),
			Indexes: workIndexes("sessions_work_event_workspace",
				model.IndexSpec{Name: "sessions_work_event_id_uniq", Columns: []string{model.ColTenantID, colEventID}, Unique: true},
				model.IndexSpec{Name: "sessions_work_event_seq_uniq", Columns: []string{model.ColTenantID, colEventAggregateKind, colEventAggregateID, colEventSeq}, Unique: true},
				model.IndexSpec{Name: "sessions_work_event_workspace_seq", Columns: []string{model.ColTenantID, colWorkWorkspaceID, colEventSeq, model.ColID}},
				model.IndexSpec{Name: "sessions_work_event_type", Columns: []string{model.ColTenantID, colEventType, model.ColID}},
			),
		},
		{
			Kind: workOutboxKind, Table: workOutboxTable, RetainOnTenantDrop: true, WorkspaceLineage: hiddenWorkspaceLineage,
			Fields: workFields(
				model.FieldSpec{Name: colOutboxEventID, Kind: model.KindUUID},
				model.FieldSpec{Name: colOutboxState, Kind: model.KindText},
				model.FieldSpec{Name: colOutboxAttempts, Kind: model.KindInt},
				model.FieldSpec{Name: colOutboxNextAttemptAt, Kind: model.KindTimestamp},
				model.FieldSpec{Name: colOutboxClaimOwner, Kind: model.KindText, Nullable: true},
				model.FieldSpec{Name: colOutboxClaimUntil, Kind: model.KindTimestamp, Nullable: true},
				model.FieldSpec{Name: colOutboxPublishedAt, Kind: model.KindTimestamp, Nullable: true},
				model.FieldSpec{Name: colOutboxLastOutcome, Kind: model.KindText, Nullable: true},
			),
			Indexes: workIndexes("sessions_work_outbox_workspace",
				model.IndexSpec{Name: "sessions_work_outbox_event_uniq", Columns: []string{model.ColTenantID, colOutboxEventID}, Unique: true},
				model.IndexSpec{Name: "sessions_work_outbox_due", Columns: []string{model.ColTenantID, colOutboxState, colOutboxNextAttemptAt, model.ColID}},
			),
		},
		{
			Kind: workGuardKind, Table: workGuardTable, RetainOnTenantDrop: true, WorkspaceLineage: hiddenWorkspaceLineage,
			Fields: workFields(
				model.FieldSpec{Name: colGuardKind, Kind: model.KindText},
				model.FieldSpec{Name: colGuardEpoch, Kind: model.KindInt},
				model.FieldSpec{Name: colGuardLastDBTime, Kind: model.KindTimestamp, Nullable: true},
				model.FieldSpec{Name: colGuardRebaseDecision, Kind: model.KindUUID, Nullable: true},
				model.FieldSpec{Name: colGuardRebaseEvidence, Kind: model.KindText, Nullable: true},
			),
			Indexes: workIndexes("sessions_work_guard_workspace", model.IndexSpec{Name: "sessions_work_guard_uniq", Columns: []string{model.ColTenantID, colWorkWorkspaceID, colGuardKind}, Unique: true}),
		},
	}
	for _, desc := range descriptors {
		if err := reg.Register(desc); err != nil {
			return err
		}
	}
	if err := m.registerCommunicationSchema(reg); err != nil {
		return err
	}

	sub, err := fs.Sub(sessionsMigrationsFS, "migrations")
	if err != nil {
		return err
	}
	if err := reg.Migrations(Namespace, sub); err != nil {
		return err
	}
	return reg.SchemaInvariants(Namespace, sessionsSchemaInvariants())
}

func workSchemaInvariants() map[store.Engine][]store.SchemaTrigger {
	// These digests are golden values captured from real freshly migrated
	// databases. They intentionally do not hash migration source: SQLite stores a
	// normalized CREATE TRIGGER statement, while PostgreSQL frames the catalog's
	// pg_get_triggerdef and pg_get_functiondef renderings. A body change requires a
	// new migration and a deliberately recaptured digest.
	definitions := []struct {
		name, table                    string
		postgres, sqliteIns, sqliteUpd string
	}{
		{
			name: "sessions_work_item_state_guard", table: workItemTable,
			postgres:  "5d3c0532694b9190eb40bae678e2994361c84062866e7f0cf5dd2b9918e15228",
			sqliteIns: "0ce57d9320b0a2e2f063f31f655496ceb1aebcfd20725fccb447165553cd5f90",
			sqliteUpd: "564bd16b9bc6f6c471403b08ed1e6f3e82266ccc3cec069fb3deabfd0a02f9e9",
		},
		{
			name: "sessions_work_dependency_guard", table: workDependencyTable,
			postgres:  "157eb681c89874caf4b905465a7db9e4f543803ee8803b6bbfe5d3d8f1c8fdb3",
			sqliteIns: "8ea0b1d361cfd2db9ca6d1f10187ddff2e1d41bf066e4bbf09e918c50bc28fbb",
			sqliteUpd: "c39831e1dc15e9f80d188794ed74020248d6ab1d2983c7172d640b68a54371b4",
		},
		{
			name: "sessions_work_acceptance_guard", table: workAcceptanceTable,
			postgres:  "baffef4a779da7a8c46de72439dd5709726fef67ba55b4bc6988a88df5da8820",
			sqliteIns: "603f96ccc117b0e42f5918897320b3ac680e6bac9d595b4475c057a38818fe68",
			sqliteUpd: "9b92a32bd5a84146e09b9e7415d025fcc346cdcc77e84fecea4b01ac6ac32af5",
		},
		{
			name: "sessions_work_decision_guard", table: workDecisionTable,
			postgres:  "771ebdf49d26ea6d452d74647c794b6cb517f86079bcd80c8a237d6924d485f4",
			sqliteIns: "aeb06140af1d3a850242dd63b83ee876951f7c84f50fb10d397179e78ad06bf3",
			sqliteUpd: "521b9476df111766149ae6ae6383bad11526530491e9898100b1c3fe4e55e5a5",
		},
		{
			name: "sessions_work_decision_head_guard", table: workDecisionHeadTable,
			postgres:  "728b1b9413d0721480c673c7b690bfe5961838912ca2b6008aaf53fb68308246",
			sqliteIns: "9a5531ceeca7f10a60506a7fa1cd02c47d36a2e4b803a03d2c981ed015944445",
			sqliteUpd: "b5d9dc5a014be880f6cf29912a2f81df7861b7d10bb95258f788ccd9d952f293",
		},
		{
			name: "sessions_work_command_guard", table: workCommandTable,
			postgres:  "2f6277fb342e2cdda9a9271b912b158908c7a0f412fba26cd272d8efce16de33",
			sqliteIns: "12229b12709a89ce10661def62f2af8138b59be76aa4b3323521b0cb7af0ba02",
			sqliteUpd: "3d78c10fc9b84adb5f38d92ba7c6ec8157538a14a9f6acaaa5e3fbcc0eae8230",
		},
		{
			name: "sessions_work_event_guard", table: workEventTable,
			postgres:  "cc3befb97b7a35a92abc3d6c5ded62b865bf44e1d1e803ef2a1d0df065e91127",
			sqliteIns: "1cfb4914780db893087113a12b0669e50f169cd806be3c3b2539532ea6ae8701",
			sqliteUpd: "f1d0623bab66af6b5f85dd12afa5340e6454e9537b3de48bdc36dbf262e97333",
		},
		{
			name: "sessions_work_outbox_guard", table: workOutboxTable,
			postgres:  "7c4697b19dd81b2c15ca84156945db9ce627b956c24fc9a00046508f6f5dc26d",
			sqliteIns: "c53ddbfcc5b67f86a807ed7ff57ecc2c34c93d520a32373031e07355a911cedf",
			sqliteUpd: "98ba6b167409a0de78821ad896b74e698bf405dc01914f4cd1b19d6050e19904",
		},
		{
			name: "sessions_work_guard_guard", table: workGuardTable,
			postgres:  "10fe1741526f689c2649162831b73856c51aaa963fad9f4342c495f8641c67c5",
			sqliteIns: "0c0071bb1156c7462908b0f5af7d72fc3a463c6daf8d95574713786486657e3b",
			sqliteUpd: "9c32c29fe98d805f3fbe4d76e8b98511d45a6c6836279618640e295417f840fb",
		},
	}
	pg := make([]store.SchemaTrigger, 0, len(definitions))
	sqlite := make([]store.SchemaTrigger, 0, len(definitions)*2)
	for _, definition := range definitions {
		pg = append(pg, store.SchemaTrigger{
			Name: definition.name, Table: definition.table,
			DefinitionSHA256: definition.postgres,
		})
		sqlite = append(sqlite,
			store.SchemaTrigger{
				Name: definition.name + "_ins", Table: definition.table,
				DefinitionSHA256: definition.sqliteIns,
			},
			store.SchemaTrigger{
				Name: definition.name + "_upd", Table: definition.table,
				DefinitionSHA256: definition.sqliteUpd,
			},
		)
	}
	// The mutable projections still prohibit hard delete. Their lifecycle is
	// represented by archive/tombstone/state fields; receipts, decisions and
	// events already receive the engine's append-only delete guards.
	for _, immutable := range []struct{ name, table, postgres, sqlite string }{
		{
			name: "sessions_work_item_no_delete", table: workItemTable,
			postgres: "fecb1938b056af4475bc51733ba9e68c470d6807a2e2823ff06cf8a772632336",
			sqlite:   "6d51518a373872b99aa86f2fd98eaf92ef97f6d56f8a833ad7947bf71a2fbc4c",
		},
		{
			name: "sessions_work_dependency_no_delete", table: workDependencyTable,
			postgres: "da2b48b903800c5dc5a050844d09914a352c208e891fff1135bad36617a9d7f6",
			sqlite:   "35a4a1558bd911985643b0831808bef646bccf464a39301a228ff4aa07aeee17",
		},
		{
			name: "sessions_work_acceptance_no_delete", table: workAcceptanceTable,
			postgres: "19cd1aa1b4787523f804609a00bb4eb7c1ea0d210ea6bd8ba5d480b2330dcdf0",
			sqlite:   "f75af2f855ee13eb15d954b1fedf25651a9880f547254424bcc2fe71bd099a2e",
		},
		{
			name: "sessions_work_decision_head_no_delete", table: workDecisionHeadTable,
			postgres: "6adccfd5db2bdbd2f4261dbad4960a45066acc3d0a837325bdce60e23651bf69",
			sqlite:   "cabc4e5cb325f2a5ec17d3876cac9d8ac6e38df40537e274b7df21550e9cb04c",
		},
		{
			name: "sessions_work_outbox_no_delete", table: workOutboxTable,
			postgres: "a0e0a633b4e5f8ea9c6e3cfb65f0f4ffc3b1f2439c281972b36793c4e8124b56",
			sqlite:   "104d0f85c639dcafe4685c32e7995cb7559a29a2bdedd69a0f63b7ed86baf0cc",
		},
		{
			name: "sessions_work_guard_no_delete", table: workGuardTable,
			postgres: "1cfb7a7abcae1d783320aa2b5526e1587d4a1ea13cf790d1035f1ac1a0226685",
			sqlite:   "686d3b346ffdad36c91a6dad0f46ab3107c42e412b15191b576760849480caa9",
		},
	} {
		pg = append(pg, store.SchemaTrigger{
			Name: immutable.name, Table: immutable.table, DefinitionSHA256: immutable.postgres,
		})
		sqlite = append(sqlite, store.SchemaTrigger{
			Name: immutable.name, Table: immutable.table, DefinitionSHA256: immutable.sqlite,
		})
	}
	// K2 adds one independently fenced aggregate and a second guard on the
	// existing WorkGuard table. Keep each exact catalog object in the boot
	// invariant set: presence by name alone would accept a replaced no-op body.
	pg = append(pg,
		store.SchemaTrigger{
			Name: "sessions_work_lease_guard", Table: workLeaseTable,
			DefinitionSHA256: "8431c7c2828eef007ad357ca22568513bd9abf21d59a1f62ed90958beeee2015",
		},
		store.SchemaTrigger{
			Name: "sessions_work_guard_clock_monotonic", Table: workGuardTable,
			DefinitionSHA256: "cdb27b42bf6f784e18d707df7d039430d02fbf2920e50cb1d36bbdfb53c8ab1f",
		},
		store.SchemaTrigger{
			Name: "sessions_work_lease_no_delete", Table: workLeaseTable,
			DefinitionSHA256: "4dbb7dcf94b8c749b685a2de63be426f873dfa0fb8f8fa5aa883dd30ecb4d02b",
		},
	)
	sqlite = append(sqlite,
		store.SchemaTrigger{
			Name: "sessions_work_lease_guard_ins", Table: workLeaseTable,
			DefinitionSHA256: "6f2441ad2ef638281e06bd7aff8f05798936c9a2dce9af75f014a0d9585d10eb",
		},
		store.SchemaTrigger{
			Name: "sessions_work_lease_guard_upd", Table: workLeaseTable,
			DefinitionSHA256: "2563aafcae1de7f0c7f116b412551a02db95f9ab7bb4f03b7c0534afcdfc92ea",
		},
		store.SchemaTrigger{
			Name: "sessions_work_guard_clock_monotonic", Table: workGuardTable,
			DefinitionSHA256: "d73f5ca4c10d010ba48e5696782b5e526d281fe97cf949ed87be3bde47989a30",
		},
		store.SchemaTrigger{
			Name: "sessions_work_lease_no_delete", Table: workLeaseTable,
			DefinitionSHA256: "97783ea5d97c3c66722770a4c249b339b63e150474eb198eac9c6c617d2295f4",
		},
	)
	return map[store.Engine][]store.SchemaTrigger{store.EnginePostgres: pg, store.EngineSQLite: sqlite}
}
