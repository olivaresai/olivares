// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

const (
	channelKind                  model.Kind = "sessions.channel"
	channelGrantKind             model.Kind = "sessions.channel_grant"
	channelSubscriptionKind      model.Kind = "sessions.channel_subscription"
	channelLabelDefinitionKind   model.Kind = "sessions.channel_label_definition"
	channelRouteKind             model.Kind = "sessions.channel_route"
	communicationEndpointKind    model.Kind = "sessions.communication_endpoint"
	messageKind                  model.Kind = "sessions.message"
	messageAudienceKind          model.Kind = "sessions.message_audience"
	messageAudienceRecipientKind model.Kind = "sessions.message_audience_recipient"
	messageDeliveryKind          model.Kind = "sessions.message_delivery"
	inboxCursorKind              model.Kind = "sessions.inbox_cursor"
	inboxCursorBarrierKind       model.Kind = "sessions.inbox_cursor_barrier"
	messageAckKind               model.Kind = "sessions.message_ack"
	communicationGuardKind       model.Kind = "sessions.communication_guard"
	decisionRequestKind          model.Kind = "sessions.decision_request"
	decisionResponseKind         model.Kind = "sessions.decision_response"
	handoffKind                  model.Kind = "sessions.handoff"
	deliveryDispatchKind         model.Kind = "sessions.delivery_dispatch"
	deliveryAttemptKind          model.Kind = "sessions.delivery_attempt"
	communicationCommandKind     model.Kind = "sessions.communication_command"
)

const (
	channelTable                  = "sessions_channel"
	channelGrantTable             = "sessions_channel_grant"
	channelSubscriptionTable      = "sessions_channel_subscription"
	channelLabelDefinitionTable   = "sessions_channel_label_definition"
	channelRouteTable             = "sessions_channel_route"
	communicationEndpointTable    = "sessions_communication_endpoint"
	messageTable                  = "sessions_message"
	messageAudienceTable          = "sessions_message_audience"
	messageAudienceRecipientTable = "sessions_message_audience_recipient"
	messageDeliveryTable          = "sessions_message_delivery"
	inboxCursorTable              = "sessions_inbox_cursor"
	inboxCursorBarrierTable       = "sessions_inbox_cursor_barrier"
	messageAckTable               = "sessions_message_ack"
	communicationGuardTable       = "sessions_communication_guard"
	decisionRequestTable          = "sessions_decision_request"
	decisionResponseTable         = "sessions_decision_response"
	handoffTable                  = "sessions_work_handoff"
	deliveryDispatchTable         = "sessions_delivery_dispatch"
	deliveryAttemptTable          = "sessions_delivery_attempt"
	communicationCommandTable     = "sessions_communication_command"
)

const (
	colCommSlug                 = "slug"
	colCommName                 = "name"
	colCommDescription          = "description"
	colCommKind                 = "kind"
	colCommState                = "state"
	colCommSensitivity          = "sensitivity"
	colCommContentProtection    = "content_protection"
	colCommProtectionGeneration = "protection_generation"
	colCommDefaultAckPolicy     = "default_ack_policy"
	colCommDefaultAckTimeoutMS  = "default_ack_timeout_ms"
	colCommDefaultWake          = "default_wake"
	colCommRetentionPolicyRef   = "retention_policy_ref"
	colCommMaxFanout            = "max_fanout"
	colCommMaxAutomationDepth   = "max_automation_depth"
	colCommACLRevision          = "acl_revision"
	colCommChannelACLRevision   = "channel_acl_revision"
	colCommRouteRevision        = "route_revision"
	colCommSubscriptionRevision = "subscription_revision"
)

const (
	colCommChannelID            = "channel_id"
	colCommSubjectKind          = "subject_kind"
	colCommSubjectRef           = "subject_ref"
	colCommGeneration           = "generation"
	colCommCanRead              = "can_read"
	colCommCanWrite             = "can_write"
	colCommCanAdmin             = "can_admin"
	colCommGrantedByKind        = "granted_by_kind"
	colCommGrantedByRef         = "granted_by_ref"
	colCommRevokedByKind        = "revoked_by_kind"
	colCommRevokedByRef         = "revoked_by_ref"
	colCommExpiresAt            = "expires_at"
	colCommSupersedesID         = "supersedes_id"
	colCommSubscriberKind       = "subscriber_kind"
	colCommSubscriberRef        = "subscriber_ref"
	colCommMode                 = "mode"
	colCommWake                 = "wake"
	colCommRequiredForCritical  = "required_for_critical"
	colCommFilterJSON           = "filter_json"
	colCommFilterHash           = "filter_hash"
	colCommLabelKey             = "key"
	colCommAllowedValuesJSON    = "allowed_values_json"
	colCommValuesHash           = "values_hash"
	colCommClassification       = "classification"
	colCommRouteKey             = "route_key"
	colCommPriority             = "priority"
	colCommSourceKind           = "source_kind"
	colCommEventType            = "event_type"
	colCommMessageKind          = "message_kind"
	colCommMinimumUrgency       = "minimum_urgency"
	colCommLabelMatchJSON       = "label_match_json"
	colCommTargetChannelID      = "target_channel_id"
	colCommAudienceKind         = "audience_kind"
	colCommAudienceRef          = "audience_ref"
	colCommAckPolicy            = "ack_policy"
	colCommWakePolicy           = "wake_policy"
	colCommCatchAll             = "catch_all"
	colCommOwnerKind            = "owner_kind"
	colCommOwnerRef             = "owner_ref"
	colCommProviderKey          = "provider_key"
	colCommEndpointRef          = "endpoint_ref"
	colCommSessionSID           = "session_sid"
	colCommCapabilitiesJSON     = "capabilities_json"
	colCommTransportFingerprint = "transport_fingerprint"
	colCommSupportLevel         = "support_level"
	colCommHeartbeatExpiresAt   = "heartbeat_expires_at"
	colCommSecretRef            = "secret_ref"
)

const (
	colCommThreadID        = "thread_id"
	colCommSenderKind      = "sender_kind"
	colCommSenderRef       = "sender_ref"
	colCommLabelsJSON      = "labels_json"
	colCommLabelsHash      = "labels_hash"
	colCommUrgency         = "urgency"
	colCommAckQuorum       = "ack_quorum"
	colCommAvailableAt     = "available_at"
	colCommAckDueAt        = "ack_due_at"
	colCommReplyToID       = "reply_to_id"
	colCommOriginEventID   = "origin_event_id"
	colCommAutomationDepth = "automation_depth"
	colCommPublishedAt     = "published_at"
	colCommTerminalAt      = "terminal_at"
	colCommTerminalCode    = "terminal_code"
	colCommAudienceHash    = "audience_hash"
	colCommLastEventSeq    = "last_event_seq"
)

const (
	colCommMessageID              = "message_id"
	colCommOrdinal                = "ordinal"
	colCommSelectorKind           = "selector_kind"
	colCommSelectorRef            = "selector_ref"
	colCommSelectorRequired       = "selector_required"
	colCommSelectorWakePolicy     = "selector_wake_policy"
	colCommRouteRuleID            = "route_rule_id"
	colCommDirectoryEpoch         = "directory_epoch"
	colCommDirectorySnapshotAt    = "directory_snapshot_at"
	colCommResolvedCount          = "resolved_count"
	colCommSelectorHash           = "selector_hash"
	colCommResolvedHash           = "resolved_hash"
	colCommMessageAudienceID      = "message_audience_id"
	colCommMessageDeliveryID      = "message_delivery_id"
	colCommRecipientKind          = "recipient_kind"
	colCommRecipientRef           = "recipient_ref"
	colCommRecipientEpoch         = "recipient_epoch"
	colCommRequired               = "required"
	colCommRouteReasonsJSON       = "route_reasons_json"
	colCommCausalKind             = "causal_kind"
	colCommCausalRef              = "causal_ref"
	colCommCausalFactKind         = "causal_fact_kind"
	colCommCausalFactID           = "causal_fact_id"
	colCommCausalFactVersion      = "causal_fact_version"
	colCommObservedSessionSID     = "observed_session_sid"
	colCommObservedClaimFence     = "observed_claim_fence"
	colCommOriginalSubscriberKind = "original_subscriber_kind"
	colCommOriginalSubscriberRef  = "original_subscriber_ref"
	colCommSubscriptionID         = "subscription_id"
	colCommSubscriptionGeneration = "subscription_generation"
	colCommRouteRuleGeneration    = "route_rule_generation"
	colCommCausalArcHash          = "causal_arc_hash"
)

const (
	colCommDeliverySeq                = "delivery_seq"
	colCommFirstSeenAt                = "first_seen_at"
	colCommAckID                      = "ack_id"
	colCommAcknowledgedAt             = "acknowledged_at"
	colCommLastWakeVerdict            = "last_wake_verdict"
	colCommLastWakeCode               = "last_wake_code"
	colCommLastWakeAt                 = "last_wake_at"
	colCommRetirementTombstoneKind    = "retirement_tombstone_kind"
	colCommRetirementTombstoneID      = "retirement_tombstone_id"
	colCommRetirementTombstoneVersion = "retirement_tombstone_version"
	colCommRetirementEpoch            = "retirement_epoch"
	colCommUndeliverableAt            = "undeliverable_at"
	colCommUndeliverableCode          = "undeliverable_code"
	colCommReaderKind                 = "reader_kind"
	colCommReaderRef                  = "reader_ref"
	colCommMailboxKind                = "mailbox_kind"
	colCommMailboxRef                 = "mailbox_ref"
	colCommLastSeenSeq                = "last_seen_seq"
	colCommLastSeenAt                 = "last_seen_at"
	colCommBarrierSeq                 = "barrier_seq"
	colCommCause                      = "cause"
	colCommResolvedAt                 = "resolved_at"
	colCommReasonCode                 = "reason_code"
	colCommAckKind                    = "ack_kind"
	colCommActorKind                  = "actor_kind"
	colCommActorRef                   = "actor_ref"
	colCommOnBehalfOfKind             = "on_behalf_of_kind"
	colCommOnBehalfOfRef              = "on_behalf_of_ref"
	colCommLate                       = "late"
	colCommGuardKind                  = "guard_kind"
	colCommNextSeq                    = "next_seq"
	colCommLastDBTime                 = "last_db_time"
)

const (
	colCommDecisionKey          = "decision_key"
	colCommRequesterKind        = "requester_kind"
	colCommRequesterRef         = "requester_ref"
	colCommAcceptedDeliveryID   = "accepted_delivery_id"
	colCommAuthorityRequirement = "authority_requirement"
	colCommDueAt                = "due_at"
	colCommAcceptedAt           = "accepted_at"
	colCommBlockedCode          = "blocked_code"
	colCommResolvedDecisionID   = "resolved_decision_id"
	colCommLastResponseSeq      = "last_response_seq"
	colCommRequestID            = "request_id"
	colCommResponseSeq          = "response_seq"
	colCommFromState            = "from_state"
	colCommToState              = "to_state"
	colCommBlockerWorkItemID    = "blocker_work_item_id"
	colCommWorkDecisionID       = "work_decision_id"
	colCommRespondedAt          = "responded_at"
	colCommDeliveryID           = "delivery_id"
	colCommFromKind             = "from_kind"
	colCommFromRef              = "from_ref"
	colCommFromOwnerEpoch       = "from_owner_epoch"
	colCommToKind               = "to_kind"
	colCommToRef                = "to_ref"
	colCommOfferedLeaseFence    = "offered_lease_fence"
	colCommContextEventSeq      = "context_event_seq"
	colCommContextHash          = "context_hash"
	colCommAckDeadline          = "ack_deadline"
	colCommRejectedAt           = "rejected_at"
	colCommWithdrawnAt          = "withdrawn_at"
	colCommExpiredAt            = "expired_at"
	colCommResultingLeaseFence  = "resulting_lease_fence"
)

const (
	colCommRootDispatchID               = "root_dispatch_id"
	colCommPredecessorID                = "predecessor_id"
	colCommEndpointID                   = "endpoint_id"
	colCommEndpointGeneration           = "endpoint_generation"
	colCommDispatchGeneration           = "dispatch_generation"
	colCommRerouteRung                  = "reroute_rung"
	colCommPolicyGeneration             = "policy_generation"
	colCommAttemptCount                 = "attempt_count"
	colCommNextAttemptAt                = "next_attempt_at"
	colCommClaimOwner                   = "claim_owner"
	colCommClaimUntil                   = "claim_until"
	colCommIdempotencyKeyHash           = "idempotency_key_hash"
	colCommLastVerdict                  = "last_verdict"
	colCommLastCode                     = "last_code"
	colCommResolutionDeadlineAt         = "resolution_deadline_at"
	colCommResolutionCode               = "resolution_code"
	colCommReconciledAttemptID          = "reconciled_attempt_id"
	colCommReconciledEndpointID         = "reconciled_endpoint_id"
	colCommReconciledEndpointGeneration = "reconciled_endpoint_generation"
	colCommReconciliationVerdict        = "reconciliation_verdict"
	colCommReconciliationCode           = "reconciliation_code"
	colCommReconciliationEvidenceRef    = "reconciliation_evidence_ref"
	colCommReconciliationObservedAt     = "reconciliation_observed_at"
	colCommProviderAcceptanceHash       = "provider_acceptance_hash"
	colCommSettledAt                    = "settled_at"
	colCommDispatchID                   = "dispatch_id"
	colCommAttemptSeq                   = "attempt_seq"
	colCommStartedAt                    = "started_at"
	colCommTransmitBoundary             = "transmit_boundary"
	colCommFinishedAt                   = "finished_at"
	colCommVerdict                      = "verdict"
	colCommCode                         = "code"
	colCommProviderReceiptHash          = "provider_receipt_hash"
	colCommRequestHash                  = "request_hash"
)

const (
	colCommCommandID              = "command_id"
	colCommActorFingerprint       = "actor_fingerprint"
	colCommCommandScope           = "command_scope"
	colCommRequestDigest          = "request_digest"
	colCommSealKeyVersion         = "seal_key_version"
	colCommDigestKeyVersion       = "digest_key_version"
	colCommPlanHash               = "plan_hash"
	colCommResultKind             = "result_kind"
	colCommResultID               = "result_id"
	colCommHTTPStatus             = "http_status"
	colCommResponseProjectionJSON = "response_projection_json"
	colCommResponseDigest         = "response_digest"
	colCommAuditSeq               = "audit_seq"
	colCommAuditHash              = "audit_hash"
	colCommCompletedAt            = "completed_at"
)

func communicationFields(extra ...model.FieldSpec) []model.FieldSpec {
	return append([]model.FieldSpec{{Name: colWorkWorkspaceID, Kind: model.KindUUID}}, extra...)
}

func communicationFieldGroups(groups ...[]model.FieldSpec) []model.FieldSpec {
	fields := []model.FieldSpec{{Name: colWorkWorkspaceID, Kind: model.KindUUID}}
	for _, group := range groups {
		fields = append(fields, group...)
	}
	return fields
}

func communicationIndexes(name string, extra ...model.IndexSpec) []model.IndexSpec {
	base := model.IndexSpec{Name: name, Columns: []string{
		model.ColTenantID, colWorkWorkspaceID, model.ColID,
	}}
	return append([]model.IndexSpec{base}, extra...)
}

// protectedPayloadFields flattens one ProtectedPayload while preserving the
// authenticated sealed envelope as canonical JSON. Variant columns and key
// versions are nullable even for required carriers because plain_json and
// sealed_v1 deliberately have different shapes. When optional is true, the
// metadata columns are nullable as one all-or-none group as well.
func protectedPayloadFields(prefix string, optional bool) []model.FieldSpec {
	return []model.FieldSpec{
		{Name: prefix + "_encoding", Kind: model.KindText, Nullable: optional},
		{Name: prefix + "_plain_json", Kind: model.KindJSON, Nullable: true},
		{Name: prefix + "_sealed_json", Kind: model.KindJSON, Nullable: true},
		{Name: prefix + "_schema", Kind: model.KindText, Nullable: optional},
		{Name: prefix + "_digest", Kind: model.KindBytes, Nullable: optional},
		{Name: prefix + "_seal_key_version", Kind: model.KindText, Nullable: true},
		{Name: prefix + "_digest_key_version", Kind: model.KindText, Nullable: true},
		{Name: prefix + "_protection_generation", Kind: model.KindInt, Nullable: optional},
	}
}

func (m *Module) registerCommunicationSchema(reg store.ExtensionRegistry) error {
	descriptors := []model.EntityDescriptor{
		{
			Kind: channelKind, Table: channelTable, RetainOnTenantDrop: true,
			WorkspaceLineage: hiddenWorkspaceLineage,
			Fields: communicationFields(
				model.FieldSpec{Name: colCommSlug, Kind: model.KindText},
				model.FieldSpec{Name: colCommName, Kind: model.KindText},
				model.FieldSpec{Name: colCommDescription, Kind: model.KindText, Nullable: true},
				model.FieldSpec{Name: colCommKind, Kind: model.KindText},
				model.FieldSpec{Name: colCommState, Kind: model.KindText},
				model.FieldSpec{Name: colCommSensitivity, Kind: model.KindText},
				model.FieldSpec{Name: colCommContentProtection, Kind: model.KindText},
				model.FieldSpec{Name: colCommProtectionGeneration, Kind: model.KindInt},
				model.FieldSpec{Name: colCommDefaultAckPolicy, Kind: model.KindText},
				model.FieldSpec{Name: colCommDefaultAckTimeoutMS, Kind: model.KindInt},
				model.FieldSpec{Name: colCommDefaultWake, Kind: model.KindText},
				model.FieldSpec{Name: colCommRetentionPolicyRef, Kind: model.KindText, Nullable: true},
				model.FieldSpec{Name: colCommMaxFanout, Kind: model.KindInt},
				model.FieldSpec{Name: colCommMaxAutomationDepth, Kind: model.KindInt},
				model.FieldSpec{Name: colCommACLRevision, Kind: model.KindInt},
				model.FieldSpec{Name: colCommRouteRevision, Kind: model.KindInt},
				model.FieldSpec{Name: colCommSubscriptionRevision, Kind: model.KindInt},
			),
			Indexes: communicationIndexes("sessions_channel_workspace",
				model.IndexSpec{Name: "sessions_channel_slug_uniq", Columns: []string{model.ColTenantID, colWorkWorkspaceID, colCommSlug}, Unique: true},
				model.IndexSpec{Name: "sessions_channel_state", Columns: []string{model.ColTenantID, colWorkWorkspaceID, colCommState, model.ColID}},
				model.IndexSpec{Name: "sessions_channel_sensitivity", Columns: []string{model.ColTenantID, colWorkWorkspaceID, colCommSensitivity, model.ColID}},
				model.IndexSpec{Name: "sessions_channel_guard_route", Columns: []string{model.ColTenantID, colWorkWorkspaceID, colCommRouteRevision, model.ColID}},
				model.IndexSpec{Name: "sessions_channel_guard_time", Columns: []string{model.ColTenantID, colWorkWorkspaceID, model.ColUpdatedAt, model.ColID}},
			),
		},
		{
			Kind: channelGrantKind, Table: channelGrantTable, RetainOnTenantDrop: true,
			WorkspaceLineage: hiddenWorkspaceLineage,
			Fields: communicationFields(
				model.FieldSpec{Name: colCommChannelID, Kind: model.KindUUID},
				model.FieldSpec{Name: colCommSubjectKind, Kind: model.KindText},
				model.FieldSpec{Name: colCommSubjectRef, Kind: model.KindText},
				model.FieldSpec{Name: colCommGeneration, Kind: model.KindInt},
				model.FieldSpec{Name: colCommCanRead, Kind: model.KindBool},
				model.FieldSpec{Name: colCommCanWrite, Kind: model.KindBool},
				model.FieldSpec{Name: colCommCanAdmin, Kind: model.KindBool},
				model.FieldSpec{Name: colCommState, Kind: model.KindText},
				model.FieldSpec{Name: colCommGrantedByKind, Kind: model.KindText},
				model.FieldSpec{Name: colCommGrantedByRef, Kind: model.KindText},
				model.FieldSpec{Name: colCommRevokedByKind, Kind: model.KindText, Nullable: true},
				model.FieldSpec{Name: colCommRevokedByRef, Kind: model.KindText, Nullable: true},
				model.FieldSpec{Name: colCommExpiresAt, Kind: model.KindTimestamp, Nullable: true},
				model.FieldSpec{Name: colCommSupersedesID, Kind: model.KindUUID, Nullable: true},
			),
			Indexes: communicationIndexes("sessions_channel_grant_workspace",
				model.IndexSpec{Name: "sessions_channel_grant_uniq", Columns: []string{model.ColTenantID, colCommChannelID, colCommSubjectKind, colCommSubjectRef, colCommGeneration}, Unique: true},
				model.IndexSpec{Name: "sessions_channel_grant_predecessor_uniq", Columns: []string{model.ColTenantID, colCommSupersedesID}, Unique: true},
				model.IndexSpec{Name: "sessions_channel_grant_subject", Columns: []string{model.ColTenantID, colWorkWorkspaceID, colCommSubjectKind, colCommSubjectRef, colCommState, model.ColID}},
				model.IndexSpec{Name: "sessions_channel_grant_channel", Columns: []string{model.ColTenantID, colCommChannelID, colCommState, model.ColID}},
			),
		},
		{
			Kind: channelSubscriptionKind, Table: channelSubscriptionTable, RetainOnTenantDrop: true,
			WorkspaceLineage: hiddenWorkspaceLineage,
			Fields: communicationFields(
				model.FieldSpec{Name: colCommChannelID, Kind: model.KindUUID},
				model.FieldSpec{Name: colCommSubscriberKind, Kind: model.KindText},
				model.FieldSpec{Name: colCommSubscriberRef, Kind: model.KindText},
				model.FieldSpec{Name: colCommGeneration, Kind: model.KindInt},
				model.FieldSpec{Name: colCommMode, Kind: model.KindText},
				model.FieldSpec{Name: colCommWake, Kind: model.KindText},
				model.FieldSpec{Name: colCommRequiredForCritical, Kind: model.KindBool},
				model.FieldSpec{Name: colCommState, Kind: model.KindText},
				model.FieldSpec{Name: colCommFilterJSON, Kind: model.KindJSON, Nullable: true},
				model.FieldSpec{Name: colCommFilterHash, Kind: model.KindBytes, Nullable: true},
				model.FieldSpec{Name: colCommSupersedesID, Kind: model.KindUUID, Nullable: true},
			),
			Indexes: communicationIndexes("sessions_channel_subscription_workspace",
				model.IndexSpec{Name: "sessions_channel_subscription_uniq", Columns: []string{model.ColTenantID, colCommChannelID, colCommSubscriberKind, colCommSubscriberRef, colCommGeneration}, Unique: true},
				model.IndexSpec{Name: "sessions_channel_subscription_predecessor_uniq", Columns: []string{model.ColTenantID, colCommSupersedesID}, Unique: true},
				model.IndexSpec{Name: "sessions_channel_subscription_channel", Columns: []string{model.ColTenantID, colCommChannelID, colCommMode, colCommState, model.ColID}},
				model.IndexSpec{Name: "sessions_channel_subscription_subscriber", Columns: []string{model.ColTenantID, colWorkWorkspaceID, colCommSubscriberKind, colCommSubscriberRef, colCommState, model.ColID}},
			),
		},
		{
			Kind: channelLabelDefinitionKind, Table: channelLabelDefinitionTable, RetainOnTenantDrop: true,
			WorkspaceLineage: hiddenWorkspaceLineage,
			Fields: communicationFields(
				model.FieldSpec{Name: colCommChannelID, Kind: model.KindUUID},
				model.FieldSpec{Name: colCommLabelKey, Kind: model.KindText},
				model.FieldSpec{Name: colCommGeneration, Kind: model.KindInt},
				model.FieldSpec{Name: colCommAllowedValuesJSON, Kind: model.KindJSON},
				model.FieldSpec{Name: colCommValuesHash, Kind: model.KindBytes},
				model.FieldSpec{Name: colCommClassification, Kind: model.KindText},
				model.FieldSpec{Name: colCommState, Kind: model.KindText},
			),
			Indexes: communicationIndexes("sessions_channel_label_definition_workspace",
				model.IndexSpec{Name: "sessions_channel_label_definition_uniq", Columns: []string{model.ColTenantID, colCommChannelID, colCommLabelKey, colCommGeneration}, Unique: true},
				model.IndexSpec{Name: "sessions_channel_label_definition_state", Columns: []string{model.ColTenantID, colCommChannelID, colCommState, colCommLabelKey, model.ColID}},
			),
		},
		{
			Kind: channelRouteKind, Table: channelRouteTable, RetainOnTenantDrop: true,
			WorkspaceLineage: hiddenWorkspaceLineage,
			Fields: communicationFields(
				model.FieldSpec{Name: colCommRouteKey, Kind: model.KindText},
				model.FieldSpec{Name: colCommGeneration, Kind: model.KindInt},
				model.FieldSpec{Name: colCommPriority, Kind: model.KindInt},
				model.FieldSpec{Name: colCommSourceKind, Kind: model.KindText},
				model.FieldSpec{Name: colCommEventType, Kind: model.KindText, Nullable: true},
				model.FieldSpec{Name: colCommMessageKind, Kind: model.KindText, Nullable: true},
				model.FieldSpec{Name: colCommMinimumUrgency, Kind: model.KindText, Nullable: true},
				model.FieldSpec{Name: colCommLabelMatchJSON, Kind: model.KindJSON, Nullable: true},
				model.FieldSpec{Name: colCommTargetChannelID, Kind: model.KindUUID},
				model.FieldSpec{Name: colCommAudienceKind, Kind: model.KindText},
				model.FieldSpec{Name: colCommAudienceRef, Kind: model.KindUUID, Nullable: true},
				model.FieldSpec{Name: colCommAckPolicy, Kind: model.KindText},
				model.FieldSpec{Name: colCommWakePolicy, Kind: model.KindText},
				model.FieldSpec{Name: colCommCatchAll, Kind: model.KindBool},
				model.FieldSpec{Name: colCommState, Kind: model.KindText},
				model.FieldSpec{Name: colCommSupersedesID, Kind: model.KindUUID, Nullable: true},
			),
			Indexes: communicationIndexes("sessions_channel_route_workspace",
				model.IndexSpec{Name: "sessions_channel_route_uniq", Columns: []string{model.ColTenantID, colWorkWorkspaceID, colCommRouteKey, colCommGeneration}, Unique: true},
				model.IndexSpec{Name: "sessions_channel_route_predecessor_uniq", Columns: []string{model.ColTenantID, colCommSupersedesID}, Unique: true},
				model.IndexSpec{Name: "sessions_channel_route_order", Columns: []string{model.ColTenantID, colWorkWorkspaceID, colCommState, colCommPriority, model.ColID}},
				model.IndexSpec{Name: "sessions_channel_route_target", Columns: []string{model.ColTenantID, colCommTargetChannelID, colCommState, model.ColID}},
			),
		},
		{
			Kind: communicationEndpointKind, Table: communicationEndpointTable, RetainOnTenantDrop: true,
			WorkspaceLineage: hiddenWorkspaceLineage,
			Fields: communicationFields(
				model.FieldSpec{Name: colCommOwnerKind, Kind: model.KindText},
				model.FieldSpec{Name: colCommOwnerRef, Kind: model.KindText},
				model.FieldSpec{Name: colCommProviderKey, Kind: model.KindText},
				model.FieldSpec{Name: colTransport, Kind: model.KindText},
				model.FieldSpec{Name: colCommEndpointRef, Kind: model.KindText},
				model.FieldSpec{Name: colCommSessionSID, Kind: model.KindText, Nullable: true},
				model.FieldSpec{Name: colCommCapabilitiesJSON, Kind: model.KindJSON},
				model.FieldSpec{Name: colCommTransportFingerprint, Kind: model.KindText, Nullable: true},
				model.FieldSpec{Name: colCommSupportLevel, Kind: model.KindText},
				model.FieldSpec{Name: colCommPriority, Kind: model.KindInt},
				model.FieldSpec{Name: colCommState, Kind: model.KindText},
				model.FieldSpec{Name: colCommHeartbeatExpiresAt, Kind: model.KindTimestamp, Nullable: true},
				model.FieldSpec{Name: colCommGeneration, Kind: model.KindInt},
				model.FieldSpec{Name: colCommSecretRef, Kind: model.KindText, Nullable: true},
			),
			Indexes: communicationIndexes("sessions_communication_endpoint_workspace",
				model.IndexSpec{Name: "sessions_communication_endpoint_uniq", Columns: []string{model.ColTenantID, colCommProviderKey, colCommEndpointRef, colCommGeneration}, Unique: true},
				model.IndexSpec{Name: "sessions_communication_endpoint_owner", Columns: []string{model.ColTenantID, colWorkWorkspaceID, colCommOwnerKind, colCommOwnerRef, colCommState, colCommPriority, model.ColID}},
				model.IndexSpec{Name: "sessions_communication_endpoint_heartbeat", Columns: []string{model.ColTenantID, colCommState, colCommHeartbeatExpiresAt, model.ColID}},
			),
		},
		{
			Kind: messageKind, Table: messageTable, RetainOnTenantDrop: true,
			WorkspaceLineage: hiddenWorkspaceLineage,
			Fields: communicationFieldGroups(
				[]model.FieldSpec{
					{Name: colCommChannelID, Kind: model.KindUUID},
					{Name: colWorkItemID, Kind: model.KindUUID, Nullable: true},
					{Name: colCommThreadID, Kind: model.KindUUID},
					{Name: colCommKind, Kind: model.KindText},
					{Name: colCommState, Kind: model.KindText},
					{Name: colCommSenderKind, Kind: model.KindText},
					{Name: colCommSenderRef, Kind: model.KindText},
				},
				protectedPayloadFields("payload", false),
				[]model.FieldSpec{
					{Name: colCommLabelsJSON, Kind: model.KindJSON, Nullable: true},
					{Name: colCommLabelsHash, Kind: model.KindBytes, Nullable: true},
					{Name: colCommUrgency, Kind: model.KindText},
					{Name: colCommAckPolicy, Kind: model.KindText},
					{Name: colCommAckQuorum, Kind: model.KindInt},
					{Name: colCommAvailableAt, Kind: model.KindTimestamp},
					{Name: colCommAckDueAt, Kind: model.KindTimestamp, Nullable: true},
					{Name: colCommExpiresAt, Kind: model.KindTimestamp, Nullable: true},
					{Name: colCommReplyToID, Kind: model.KindUUID, Nullable: true},
					{Name: colCommSupersedesID, Kind: model.KindUUID, Nullable: true},
					{Name: colCommOriginEventID, Kind: model.KindUUID, Nullable: true},
					{Name: colCommAutomationDepth, Kind: model.KindInt},
					{Name: colCommPublishedAt, Kind: model.KindTimestamp, Nullable: true},
					{Name: colCommTerminalAt, Kind: model.KindTimestamp, Nullable: true},
					{Name: colCommTerminalCode, Kind: model.KindText, Nullable: true},
				},
				protectedPayloadFields("terminal_reason", true),
				[]model.FieldSpec{
					{Name: colCommAudienceHash, Kind: model.KindBytes, Nullable: true},
					{Name: colCommLastEventSeq, Kind: model.KindInt},
				},
			),
			Indexes: communicationIndexes("sessions_message_workspace",
				model.IndexSpec{Name: "sessions_message_thread", Columns: []string{model.ColTenantID, colWorkWorkspaceID, colCommChannelID, colCommThreadID, colCommPublishedAt, model.ColID}},
				model.IndexSpec{Name: "sessions_message_reply", Columns: []string{model.ColTenantID, colCommReplyToID, model.ColID}},
				model.IndexSpec{Name: "sessions_message_work_item", Columns: []string{model.ColTenantID, colWorkItemID, model.ColID}},
				model.IndexSpec{Name: "sessions_message_origin_event", Columns: []string{model.ColTenantID, colCommOriginEventID, model.ColID}},
				model.IndexSpec{Name: "sessions_message_ack_due", Columns: []string{model.ColTenantID, colCommAckDueAt, model.ColID}},
				model.IndexSpec{Name: "sessions_message_expiry", Columns: []string{model.ColTenantID, colCommExpiresAt, model.ColID}},
			),
		},
		{
			Kind: messageAudienceKind, Table: messageAudienceTable, AppendOnly: true,
			WorkspaceLineage: hiddenWorkspaceLineage,
			Fields: communicationFields(
				model.FieldSpec{Name: colCommMessageID, Kind: model.KindUUID},
				model.FieldSpec{Name: colCommOrdinal, Kind: model.KindInt},
				model.FieldSpec{Name: colCommSelectorKind, Kind: model.KindText},
				model.FieldSpec{Name: colCommSelectorRef, Kind: model.KindText, Nullable: true},
				model.FieldSpec{Name: colCommSelectorRequired, Kind: model.KindBool},
				model.FieldSpec{Name: colCommSelectorWakePolicy, Kind: model.KindText},
				model.FieldSpec{Name: colCommRouteRuleID, Kind: model.KindUUID, Nullable: true},
				model.FieldSpec{Name: colCommChannelACLRevision, Kind: model.KindInt},
				model.FieldSpec{Name: colCommRouteRevision, Kind: model.KindInt},
				model.FieldSpec{Name: colCommSubscriptionRevision, Kind: model.KindInt},
				model.FieldSpec{Name: colCommDirectoryEpoch, Kind: model.KindInt},
				model.FieldSpec{Name: colCommDirectorySnapshotAt, Kind: model.KindTimestamp},
				model.FieldSpec{Name: colCommResolvedCount, Kind: model.KindInt},
				model.FieldSpec{Name: colCommSelectorHash, Kind: model.KindBytes},
				model.FieldSpec{Name: colCommResolvedHash, Kind: model.KindBytes},
			),
			Indexes: communicationIndexes("sessions_message_audience_workspace",
				model.IndexSpec{Name: "sessions_message_audience_uniq", Columns: []string{model.ColTenantID, colCommMessageID, colCommOrdinal}, Unique: true},
				model.IndexSpec{Name: "sessions_message_audience_selector", Columns: []string{model.ColTenantID, colWorkWorkspaceID, colCommSelectorKind, colCommSelectorRef, colCommMessageID, colCommOrdinal}},
				model.IndexSpec{Name: "sessions_message_audience_route", Columns: []string{model.ColTenantID, colCommRouteRuleID, colCommMessageID}},
			),
		},
		{
			Kind: messageAudienceRecipientKind, Table: messageAudienceRecipientTable, AppendOnly: true,
			WorkspaceLineage: hiddenWorkspaceLineage,
			Fields: communicationFields(
				model.FieldSpec{Name: colCommMessageAudienceID, Kind: model.KindUUID},
				model.FieldSpec{Name: colCommMessageDeliveryID, Kind: model.KindUUID},
				model.FieldSpec{Name: colCommRecipientKind, Kind: model.KindText},
				model.FieldSpec{Name: colCommRecipientRef, Kind: model.KindText},
				model.FieldSpec{Name: colCommRecipientEpoch, Kind: model.KindInt},
				model.FieldSpec{Name: colCommRequired, Kind: model.KindBool},
				model.FieldSpec{Name: colCommWakePolicy, Kind: model.KindText},
				model.FieldSpec{Name: colCommRouteReasonsJSON, Kind: model.KindJSON},
				model.FieldSpec{Name: colCommSelectorKind, Kind: model.KindText},
				model.FieldSpec{Name: colCommSelectorRef, Kind: model.KindText, Nullable: true},
				model.FieldSpec{Name: colCommSelectorRequired, Kind: model.KindBool},
				model.FieldSpec{Name: colCommSelectorWakePolicy, Kind: model.KindText},
				model.FieldSpec{Name: colCommDirectoryEpoch, Kind: model.KindInt},
				model.FieldSpec{Name: colCommChannelACLRevision, Kind: model.KindInt},
				model.FieldSpec{Name: colCommRouteRevision, Kind: model.KindInt},
				model.FieldSpec{Name: colCommSubscriptionRevision, Kind: model.KindInt},
				model.FieldSpec{Name: colCommCausalKind, Kind: model.KindText},
				model.FieldSpec{Name: colCommCausalRef, Kind: model.KindText},
				model.FieldSpec{Name: colCommCausalFactKind, Kind: model.KindText, Nullable: true},
				model.FieldSpec{Name: colCommCausalFactID, Kind: model.KindUUID, Nullable: true},
				model.FieldSpec{Name: colCommCausalFactVersion, Kind: model.KindInt, Nullable: true},
				model.FieldSpec{Name: colCommObservedSessionSID, Kind: model.KindText, Nullable: true},
				model.FieldSpec{Name: colCommObservedClaimFence, Kind: model.KindInt, Nullable: true},
				model.FieldSpec{Name: colCommOriginalSubscriberKind, Kind: model.KindText, Nullable: true},
				model.FieldSpec{Name: colCommOriginalSubscriberRef, Kind: model.KindText, Nullable: true},
				model.FieldSpec{Name: colCommSubscriptionID, Kind: model.KindUUID, Nullable: true},
				model.FieldSpec{Name: colCommSubscriptionGeneration, Kind: model.KindInt, Nullable: true},
				model.FieldSpec{Name: colCommRouteRuleID, Kind: model.KindUUID, Nullable: true},
				model.FieldSpec{Name: colCommRouteRuleGeneration, Kind: model.KindInt, Nullable: true},
				model.FieldSpec{Name: colCommCausalArcHash, Kind: model.KindBytes},
			),
			Indexes: communicationIndexes("sessions_message_audience_recipient_workspace",
				model.IndexSpec{Name: "sessions_message_audience_recipient_arc_uniq", Columns: []string{model.ColTenantID, colCommMessageAudienceID, colCommCausalArcHash}, Unique: true},
				model.IndexSpec{Name: "sessions_message_audience_recipient_delivery", Columns: []string{model.ColTenantID, colCommMessageDeliveryID, model.ColID}},
				model.IndexSpec{Name: "sessions_message_audience_recipient_recipient", Columns: []string{model.ColTenantID, colWorkWorkspaceID, colCommRecipientKind, colCommRecipientRef, colCommMessageAudienceID, model.ColID}},
				model.IndexSpec{Name: "sessions_message_audience_recipient_fact", Columns: []string{model.ColTenantID, colCommCausalFactKind, colCommCausalFactID, model.ColID}},
			),
		},
		{
			Kind: messageDeliveryKind, Table: messageDeliveryTable, RetainOnTenantDrop: true,
			WorkspaceLineage: hiddenWorkspaceLineage,
			Fields: communicationFields(
				model.FieldSpec{Name: colCommMessageID, Kind: model.KindUUID},
				model.FieldSpec{Name: colCommRecipientKind, Kind: model.KindText},
				model.FieldSpec{Name: colCommRecipientRef, Kind: model.KindText},
				model.FieldSpec{Name: colCommRecipientEpoch, Kind: model.KindInt},
				model.FieldSpec{Name: colCommDeliverySeq, Kind: model.KindInt},
				model.FieldSpec{Name: colCommRequired, Kind: model.KindBool},
				model.FieldSpec{Name: colCommRouteReasonsJSON, Kind: model.KindJSON},
				model.FieldSpec{Name: colCommWakePolicy, Kind: model.KindText},
				model.FieldSpec{Name: colCommState, Kind: model.KindText},
				model.FieldSpec{Name: colCommAvailableAt, Kind: model.KindTimestamp},
				model.FieldSpec{Name: colCommFirstSeenAt, Kind: model.KindTimestamp, Nullable: true},
				model.FieldSpec{Name: colCommAckDueAt, Kind: model.KindTimestamp, Nullable: true},
				model.FieldSpec{Name: colCommExpiresAt, Kind: model.KindTimestamp, Nullable: true},
				model.FieldSpec{Name: colCommAckID, Kind: model.KindUUID, Nullable: true},
				model.FieldSpec{Name: colCommAcknowledgedAt, Kind: model.KindTimestamp, Nullable: true},
				model.FieldSpec{Name: colCommLastWakeVerdict, Kind: model.KindText, Nullable: true},
				model.FieldSpec{Name: colCommLastWakeCode, Kind: model.KindText, Nullable: true},
				model.FieldSpec{Name: colCommLastWakeAt, Kind: model.KindTimestamp, Nullable: true},
				model.FieldSpec{Name: colCommRetirementTombstoneKind, Kind: model.KindText, Nullable: true},
				model.FieldSpec{Name: colCommRetirementTombstoneID, Kind: model.KindUUID, Nullable: true},
				model.FieldSpec{Name: colCommRetirementTombstoneVersion, Kind: model.KindInt, Nullable: true},
				model.FieldSpec{Name: colCommRetirementEpoch, Kind: model.KindInt, Nullable: true},
				model.FieldSpec{Name: colCommUndeliverableAt, Kind: model.KindTimestamp, Nullable: true},
				model.FieldSpec{Name: colCommUndeliverableCode, Kind: model.KindText, Nullable: true},
			),
			Indexes: communicationIndexes("sessions_message_delivery_workspace",
				model.IndexSpec{Name: "sessions_message_delivery_recipient_uniq", Columns: []string{model.ColTenantID, colCommMessageID, colCommRecipientKind, colCommRecipientRef}, Unique: true},
				model.IndexSpec{Name: "sessions_message_delivery_seq_uniq", Columns: []string{model.ColTenantID, colWorkWorkspaceID, colCommDeliverySeq}, Unique: true},
				model.IndexSpec{Name: "sessions_message_delivery_inbox", Columns: []string{model.ColTenantID, colWorkWorkspaceID, colCommRecipientKind, colCommRecipientRef, colCommDeliverySeq}},
				model.IndexSpec{Name: "sessions_message_delivery_due", Columns: []string{model.ColTenantID, colCommState, colCommAckDueAt, model.ColID}},
				model.IndexSpec{Name: "sessions_message_delivery_message", Columns: []string{model.ColTenantID, colCommMessageID, colCommState, model.ColID}},
				model.IndexSpec{Name: "sessions_message_delivery_guard_time", Columns: []string{model.ColTenantID, colWorkWorkspaceID, model.ColUpdatedAt, model.ColID}},
			),
		},
		{
			Kind: inboxCursorKind, Table: inboxCursorTable, RetainOnTenantDrop: true,
			WorkspaceLineage: hiddenWorkspaceLineage,
			Fields: communicationFields(
				model.FieldSpec{Name: colCommReaderKind, Kind: model.KindText},
				model.FieldSpec{Name: colCommReaderRef, Kind: model.KindText},
				model.FieldSpec{Name: colCommMailboxKind, Kind: model.KindText},
				model.FieldSpec{Name: colCommMailboxRef, Kind: model.KindText},
				model.FieldSpec{Name: colCommLastSeenSeq, Kind: model.KindInt},
				model.FieldSpec{Name: colCommLastSeenAt, Kind: model.KindTimestamp},
				model.FieldSpec{Name: colCommFilterHash, Kind: model.KindBytes},
			),
			Indexes: communicationIndexes("sessions_inbox_cursor_workspace",
				model.IndexSpec{Name: "sessions_inbox_cursor_uniq", Columns: []string{model.ColTenantID, colWorkWorkspaceID, colCommReaderKind, colCommReaderRef, colCommMailboxKind, colCommMailboxRef, colCommFilterHash}, Unique: true},
			),
		},
		{
			Kind: inboxCursorBarrierKind, Table: inboxCursorBarrierTable, RetainOnTenantDrop: true,
			WorkspaceLineage: hiddenWorkspaceLineage,
			Fields: communicationFields(
				model.FieldSpec{Name: colCommReaderKind, Kind: model.KindText},
				model.FieldSpec{Name: colCommReaderRef, Kind: model.KindText},
				model.FieldSpec{Name: colCommMailboxKind, Kind: model.KindText},
				model.FieldSpec{Name: colCommMailboxRef, Kind: model.KindText},
				model.FieldSpec{Name: colCommFilterHash, Kind: model.KindBytes},
				model.FieldSpec{Name: colCommDeliveryID, Kind: model.KindUUID},
				model.FieldSpec{Name: colCommBarrierSeq, Kind: model.KindInt},
				model.FieldSpec{Name: colCommCause, Kind: model.KindText},
				model.FieldSpec{Name: colCommState, Kind: model.KindText},
				model.FieldSpec{Name: colCommResolvedAt, Kind: model.KindTimestamp, Nullable: true},
				model.FieldSpec{Name: colCommReasonCode, Kind: model.KindText},
			),
			Indexes: communicationIndexes("sessions_inbox_cursor_barrier_workspace",
				model.IndexSpec{Name: "sessions_inbox_cursor_barrier_active", Columns: []string{model.ColTenantID, colWorkWorkspaceID, colCommReaderKind, colCommReaderRef, colCommMailboxKind, colCommMailboxRef, colCommFilterHash, colCommState, colCommBarrierSeq, model.ColID}},
				model.IndexSpec{Name: "sessions_inbox_cursor_barrier_delivery", Columns: []string{model.ColTenantID, colCommDeliveryID, model.ColID}},
			),
		},
		{
			Kind: messageAckKind, Table: messageAckTable, AppendOnly: true,
			WorkspaceLineage: hiddenWorkspaceLineage,
			Fields: communicationFieldGroups(
				[]model.FieldSpec{
					{Name: colCommDeliveryID, Kind: model.KindUUID},
					{Name: colCommAckKind, Kind: model.KindText},
					{Name: colCommActorKind, Kind: model.KindText},
					{Name: colCommActorRef, Kind: model.KindText},
					{Name: colCommOnBehalfOfKind, Kind: model.KindText, Nullable: true},
					{Name: colCommOnBehalfOfRef, Kind: model.KindText, Nullable: true},
				},
				protectedPayloadFields("note", true),
				[]model.FieldSpec{
					{Name: colCommAcknowledgedAt, Kind: model.KindTimestamp},
					{Name: colCommLate, Kind: model.KindBool},
				},
			),
			Indexes: communicationIndexes("sessions_message_ack_workspace",
				model.IndexSpec{Name: "sessions_message_ack_delivery_uniq", Columns: []string{model.ColTenantID, colCommDeliveryID}, Unique: true},
				model.IndexSpec{Name: "sessions_message_ack_actor", Columns: []string{model.ColTenantID, colWorkWorkspaceID, colCommActorKind, colCommActorRef, colCommAcknowledgedAt, model.ColID}},
			),
		},
		{
			Kind: communicationGuardKind, Table: communicationGuardTable, RetainOnTenantDrop: true,
			WorkspaceLineage: hiddenWorkspaceLineage,
			Fields: communicationFields(
				model.FieldSpec{Name: colCommGuardKind, Kind: model.KindText},
				model.FieldSpec{Name: colCommNextSeq, Kind: model.KindInt},
				model.FieldSpec{Name: colCommLastDBTime, Kind: model.KindTimestamp},
			),
			Indexes: communicationIndexes("sessions_communication_guard_workspace",
				model.IndexSpec{Name: "sessions_communication_guard_uniq", Columns: []string{model.ColTenantID, colWorkWorkspaceID, colCommGuardKind}, Unique: true},
			),
		},
		{
			Kind: decisionRequestKind, Table: decisionRequestTable, RetainOnTenantDrop: true,
			WorkspaceLineage: hiddenWorkspaceLineage,
			Fields: communicationFieldGroups(
				[]model.FieldSpec{
					{Name: colCommMessageID, Kind: model.KindUUID},
					{Name: colWorkItemID, Kind: model.KindUUID},
					{Name: colCommDecisionKey, Kind: model.KindText},
					{Name: colCommRequesterKind, Kind: model.KindText},
					{Name: colCommRequesterRef, Kind: model.KindText},
					{Name: colCommOwnerKind, Kind: model.KindText},
					{Name: colCommOwnerRef, Kind: model.KindText},
					{Name: colCommAcceptedDeliveryID, Kind: model.KindUUID, Nullable: true},
					{Name: colCommState, Kind: model.KindText},
				},
				protectedPayloadFields("request", false),
				[]model.FieldSpec{
					{Name: colCommAuthorityRequirement, Kind: model.KindText},
					{Name: colCommDueAt, Kind: model.KindTimestamp},
					{Name: colCommAcceptedAt, Kind: model.KindTimestamp, Nullable: true},
					{Name: colCommBlockedCode, Kind: model.KindText, Nullable: true},
					{Name: colCommTerminalCode, Kind: model.KindText, Nullable: true},
					{Name: colCommResolvedDecisionID, Kind: model.KindUUID, Nullable: true},
					{Name: colCommLastResponseSeq, Kind: model.KindInt},
				},
			),
			Indexes: communicationIndexes("sessions_decision_request_workspace",
				model.IndexSpec{Name: "sessions_decision_request_message_uniq", Columns: []string{model.ColTenantID, colCommMessageID}, Unique: true},
				model.IndexSpec{Name: "sessions_decision_request_work", Columns: []string{model.ColTenantID, colWorkItemID, colCommDecisionKey, colCommState, model.ColID}},
				model.IndexSpec{Name: "sessions_decision_request_owner", Columns: []string{model.ColTenantID, colWorkWorkspaceID, colCommOwnerKind, colCommOwnerRef, colCommState, colCommDueAt, model.ColID}},
				model.IndexSpec{Name: "sessions_decision_request_due", Columns: []string{model.ColTenantID, colCommState, colCommDueAt, model.ColID}},
			),
		},
		{
			Kind: decisionResponseKind, Table: decisionResponseTable, AppendOnly: true,
			WorkspaceLineage: hiddenWorkspaceLineage,
			Fields: communicationFieldGroups(
				[]model.FieldSpec{
					{Name: colCommRequestID, Kind: model.KindUUID},
					{Name: colCommResponseSeq, Kind: model.KindInt},
					{Name: colCommFromState, Kind: model.KindText},
					{Name: colCommToState, Kind: model.KindText},
					{Name: colCommActorKind, Kind: model.KindText},
					{Name: colCommActorRef, Kind: model.KindText},
				},
				protectedPayloadFields("response", false),
				[]model.FieldSpec{
					{Name: colCommAcceptedDeliveryID, Kind: model.KindUUID, Nullable: true},
					{Name: colCommBlockerWorkItemID, Kind: model.KindUUID, Nullable: true},
					{Name: colCommWorkDecisionID, Kind: model.KindUUID, Nullable: true},
					{Name: colCommRespondedAt, Kind: model.KindTimestamp},
				},
			),
			Indexes: communicationIndexes("sessions_decision_response_workspace",
				model.IndexSpec{Name: "sessions_decision_response_uniq", Columns: []string{model.ColTenantID, colCommRequestID, colCommResponseSeq}, Unique: true},
				model.IndexSpec{Name: "sessions_decision_response_actor", Columns: []string{model.ColTenantID, colWorkWorkspaceID, colCommActorKind, colCommActorRef, colCommRespondedAt, model.ColID}},
			),
		},
		{
			Kind: handoffKind, Table: handoffTable, RetainOnTenantDrop: true,
			WorkspaceLineage: hiddenWorkspaceLineage,
			Fields: communicationFieldGroups(
				[]model.FieldSpec{
					{Name: colWorkItemID, Kind: model.KindUUID},
					{Name: colCommMessageID, Kind: model.KindUUID},
					{Name: colCommDeliveryID, Kind: model.KindUUID},
					{Name: colCommFromKind, Kind: model.KindText},
					{Name: colCommFromRef, Kind: model.KindText},
					{Name: colCommFromOwnerEpoch, Kind: model.KindInt},
					{Name: colCommToKind, Kind: model.KindText},
					{Name: colCommToRef, Kind: model.KindText},
					{Name: colCommOfferedLeaseFence, Kind: model.KindInt, Nullable: true},
					{Name: colCommContextEventSeq, Kind: model.KindInt},
					{Name: colCommContextHash, Kind: model.KindBytes},
				},
				protectedPayloadFields("handoff", false),
				[]model.FieldSpec{
					{Name: colCommState, Kind: model.KindText},
					{Name: colCommAckDeadline, Kind: model.KindTimestamp},
					{Name: colCommAckID, Kind: model.KindUUID, Nullable: true},
					{Name: colCommAcceptedAt, Kind: model.KindTimestamp, Nullable: true},
					{Name: colCommRejectedAt, Kind: model.KindTimestamp, Nullable: true},
					{Name: colCommWithdrawnAt, Kind: model.KindTimestamp, Nullable: true},
					{Name: colCommExpiredAt, Kind: model.KindTimestamp, Nullable: true},
					{Name: colCommTerminalCode, Kind: model.KindText, Nullable: true},
				},
				protectedPayloadFields("terminal_reason", true),
				[]model.FieldSpec{
					{Name: colCommResultingLeaseFence, Kind: model.KindInt, Nullable: true},
				},
			),
			Indexes: communicationIndexes("sessions_work_handoff_workspace",
				model.IndexSpec{Name: "sessions_work_handoff_message_uniq", Columns: []string{model.ColTenantID, colCommMessageID}, Unique: true},
				model.IndexSpec{Name: "sessions_work_handoff_delivery_uniq", Columns: []string{model.ColTenantID, colCommDeliveryID}, Unique: true},
				model.IndexSpec{Name: "sessions_work_handoff_work", Columns: []string{model.ColTenantID, colWorkItemID, colCommState, model.ColID}},
				model.IndexSpec{Name: "sessions_work_handoff_target", Columns: []string{model.ColTenantID, colWorkWorkspaceID, colCommToKind, colCommToRef, colCommState, colCommAckDeadline, model.ColID}},
				model.IndexSpec{Name: "sessions_work_handoff_due", Columns: []string{model.ColTenantID, colCommState, colCommAckDeadline, model.ColID}},
			),
		},
		{
			Kind: deliveryDispatchKind, Table: deliveryDispatchTable, RetainOnTenantDrop: true,
			WorkspaceLineage: hiddenWorkspaceLineage,
			Fields: communicationFields(
				model.FieldSpec{Name: colCommDeliveryID, Kind: model.KindUUID},
				model.FieldSpec{Name: colCommRootDispatchID, Kind: model.KindUUID},
				model.FieldSpec{Name: colCommPredecessorID, Kind: model.KindUUID, Nullable: true},
				model.FieldSpec{Name: colCommEndpointID, Kind: model.KindUUID},
				model.FieldSpec{Name: colCommEndpointGeneration, Kind: model.KindInt},
				model.FieldSpec{Name: colCommRouteRuleID, Kind: model.KindUUID, Nullable: true},
				model.FieldSpec{Name: colCommRouteRuleGeneration, Kind: model.KindInt, Nullable: true},
				model.FieldSpec{Name: colCommDispatchGeneration, Kind: model.KindInt},
				model.FieldSpec{Name: colCommRerouteRung, Kind: model.KindInt},
				model.FieldSpec{Name: colCommPolicyGeneration, Kind: model.KindInt},
				model.FieldSpec{Name: colCommState, Kind: model.KindText},
				model.FieldSpec{Name: colCommAttemptCount, Kind: model.KindInt},
				model.FieldSpec{Name: colCommNextAttemptAt, Kind: model.KindTimestamp, Nullable: true},
				model.FieldSpec{Name: colCommClaimOwner, Kind: model.KindText, Nullable: true},
				model.FieldSpec{Name: colCommClaimUntil, Kind: model.KindTimestamp, Nullable: true},
				model.FieldSpec{Name: colCommIdempotencyKeyHash, Kind: model.KindBytes},
				model.FieldSpec{Name: colCommLastVerdict, Kind: model.KindText, Nullable: true},
				model.FieldSpec{Name: colCommLastCode, Kind: model.KindText, Nullable: true},
				model.FieldSpec{Name: colCommResolutionDeadlineAt, Kind: model.KindTimestamp, Nullable: true},
				model.FieldSpec{Name: colCommResolutionCode, Kind: model.KindText, Nullable: true},
				model.FieldSpec{Name: colCommReconciledAttemptID, Kind: model.KindUUID, Nullable: true},
				model.FieldSpec{Name: colCommReconciledEndpointID, Kind: model.KindUUID, Nullable: true},
				model.FieldSpec{Name: colCommReconciledEndpointGeneration, Kind: model.KindInt, Nullable: true},
				model.FieldSpec{Name: colCommReconciliationVerdict, Kind: model.KindText, Nullable: true},
				model.FieldSpec{Name: colCommReconciliationCode, Kind: model.KindText, Nullable: true},
				model.FieldSpec{Name: colCommReconciliationEvidenceRef, Kind: model.KindText, Nullable: true},
				model.FieldSpec{Name: colCommReconciliationObservedAt, Kind: model.KindTimestamp, Nullable: true},
				model.FieldSpec{Name: colCommProviderAcceptanceHash, Kind: model.KindBytes, Nullable: true},
				model.FieldSpec{Name: colCommSettledAt, Kind: model.KindTimestamp, Nullable: true},
			),
			Indexes: communicationIndexes("sessions_delivery_dispatch_workspace",
				model.IndexSpec{Name: "sessions_delivery_dispatch_generation_uniq", Columns: []string{model.ColTenantID, colCommRootDispatchID, colCommDispatchGeneration}, Unique: true},
				model.IndexSpec{Name: "sessions_delivery_dispatch_predecessor_uniq", Columns: []string{model.ColTenantID, colCommPredecessorID}, Unique: true},
				model.IndexSpec{Name: "sessions_delivery_dispatch_idempotency_uniq", Columns: []string{model.ColTenantID, colCommRootDispatchID, colCommIdempotencyKeyHash}, Unique: true},
				model.IndexSpec{Name: "sessions_delivery_dispatch_due", Columns: []string{model.ColTenantID, colCommState, colCommNextAttemptAt, model.ColID}},
				model.IndexSpec{Name: "sessions_delivery_dispatch_resolution_due", Columns: []string{model.ColTenantID, colCommState, colCommResolutionDeadlineAt, model.ColID}},
				model.IndexSpec{Name: "sessions_delivery_dispatch_delivery", Columns: []string{model.ColTenantID, colCommDeliveryID, colCommRootDispatchID, colCommDispatchGeneration, model.ColID}},
				model.IndexSpec{Name: "sessions_delivery_dispatch_claim", Columns: []string{model.ColTenantID, colCommState, colCommClaimUntil, model.ColID}},
			),
		},
		{
			Kind: deliveryAttemptKind, Table: deliveryAttemptTable, RetainOnTenantDrop: true,
			WorkspaceLineage: hiddenWorkspaceLineage,
			Fields: communicationFields(
				model.FieldSpec{Name: colCommDispatchID, Kind: model.KindUUID},
				model.FieldSpec{Name: colCommAttemptSeq, Kind: model.KindInt},
				model.FieldSpec{Name: colCommState, Kind: model.KindText},
				model.FieldSpec{Name: colCommStartedAt, Kind: model.KindTimestamp},
				model.FieldSpec{Name: colCommTransmitBoundary, Kind: model.KindText},
				model.FieldSpec{Name: colCommFinishedAt, Kind: model.KindTimestamp, Nullable: true},
				model.FieldSpec{Name: colCommVerdict, Kind: model.KindText, Nullable: true},
				model.FieldSpec{Name: colCommCode, Kind: model.KindText, Nullable: true},
				model.FieldSpec{Name: colCommProviderReceiptHash, Kind: model.KindBytes, Nullable: true},
				model.FieldSpec{Name: colCommRequestHash, Kind: model.KindBytes},
			),
			Indexes: communicationIndexes("sessions_delivery_attempt_workspace",
				model.IndexSpec{Name: "sessions_delivery_attempt_dispatch_uniq", Columns: []string{model.ColTenantID, colCommDispatchID}, Unique: true},
				model.IndexSpec{Name: "sessions_delivery_attempt_verdict", Columns: []string{model.ColTenantID, colCommState, colCommVerdict, colCommStartedAt, model.ColID}},
			),
		},
		{
			Kind: communicationCommandKind, Table: communicationCommandTable, AppendOnly: true,
			WorkspaceLineage: hiddenWorkspaceLineage,
			Fields: communicationFields(
				model.FieldSpec{Name: colCommCommandID, Kind: model.KindUUID},
				model.FieldSpec{Name: colCommActorFingerprint, Kind: model.KindBytes},
				model.FieldSpec{Name: colCommCommandScope, Kind: model.KindText},
				model.FieldSpec{Name: colCommIdempotencyKeyHash, Kind: model.KindBytes},
				model.FieldSpec{Name: colCommRequestDigest, Kind: model.KindBytes},
				model.FieldSpec{Name: colCommSealKeyVersion, Kind: model.KindText, Nullable: true},
				model.FieldSpec{Name: colCommDigestKeyVersion, Kind: model.KindText, Nullable: true},
				model.FieldSpec{Name: colCommPlanHash, Kind: model.KindBytes},
				model.FieldSpec{Name: colCommResultKind, Kind: model.KindText},
				model.FieldSpec{Name: colCommResultID, Kind: model.KindUUID, Nullable: true},
				model.FieldSpec{Name: colCommHTTPStatus, Kind: model.KindInt},
				model.FieldSpec{Name: colCommResponseProjectionJSON, Kind: model.KindJSON},
				model.FieldSpec{Name: colCommResponseDigest, Kind: model.KindBytes},
				model.FieldSpec{Name: colEventID, Kind: model.KindUUID, Nullable: true},
				model.FieldSpec{Name: colCommAuditSeq, Kind: model.KindInt},
				model.FieldSpec{Name: colCommAuditHash, Kind: model.KindBytes, Nullable: true},
				model.FieldSpec{Name: colCommCompletedAt, Kind: model.KindTimestamp},
			),
			Indexes: communicationIndexes("sessions_communication_command_workspace",
				model.IndexSpec{Name: "sessions_communication_command_idem", Columns: []string{model.ColTenantID, colCommActorFingerprint, colCommCommandScope, colCommIdempotencyKeyHash}, Unique: true},
				model.IndexSpec{Name: "sessions_communication_command_id_uniq", Columns: []string{model.ColTenantID, colCommCommandID}, Unique: true},
				model.IndexSpec{Name: "sessions_communication_command_event", Columns: []string{model.ColTenantID, colEventID, model.ColID}},
			),
		},
	}

	for _, descriptor := range descriptors {
		if err := reg.Register(descriptor); err != nil {
			return err
		}
	}
	if err := m.registerProtocolBindingSchema(reg); err != nil {
		return err
	}
	if err := m.registerProtocolInterruptSchema(reg); err != nil {
		return err
	}
	if err := m.registerProtocolReplayGuardSchema(reg); err != nil {
		return err
	}
	if err := m.registerProtocolSubscriptionSchema(reg); err != nil {
		return err
	}
	return reg.WorkspaceInitializer(store.WorkspaceInitializer{
		Key: communicationGuardWorkspaceInitializerKey, Initialize: initializeCommunicationWorkspace,
	})
}

// communicationSchemaInvariants pins the live-catalog digests captured after the
// additive K3 migrations. Keeping this separate lets the namespace register
// migrations and invariants exactly once through the combiner below; it must not
// grow its own reg.Migrations or reg.SchemaInvariants call.
func communicationSchemaInvariants() map[store.Engine][]store.SchemaTrigger {
	definitions := []struct {
		table   string
		mutable bool
	}{
		{table: channelTable, mutable: true},
		{table: channelGrantTable, mutable: true},
		{table: channelSubscriptionTable, mutable: true},
		{table: channelLabelDefinitionTable, mutable: true},
		{table: channelRouteTable, mutable: true},
		{table: communicationEndpointTable, mutable: true},
		{table: messageTable, mutable: true},
		{table: messageAudienceTable},
		{table: messageAudienceRecipientTable},
		{table: messageDeliveryTable, mutable: true},
		{table: inboxCursorTable, mutable: true},
		{table: inboxCursorBarrierTable, mutable: true},
		{table: messageAckTable},
		{table: communicationGuardTable, mutable: true},
		{table: decisionRequestTable, mutable: true},
		{table: decisionResponseTable},
		{table: handoffTable, mutable: true},
		{table: deliveryDispatchTable, mutable: true},
		{table: deliveryAttemptTable, mutable: true},
		{table: communicationCommandTable},
	}
	postgresDigests := map[string]string{
		"sessions_channel_grant_guard":                "8654e3ec71c73c2b7e9d2935fdf95a99473906eca98de5c2648c682d7d2ba556",
		"sessions_channel_grant_no_delete":            "f79501411ffe58e128cf9bad569de64bb657e5792edd362f351abc222cfeab7e",
		"sessions_channel_guard":                      "c0931ebee9d585db68fcf7019d7f0ecd40234d09011efdcd216e25e22ea50290",
		"sessions_channel_label_definition_guard":     "556cf239310b0effa528246d76f4d8e196d85d359125ba9178581821dab28bc1",
		"sessions_channel_label_definition_no_delete": "57250c53e51e35c165c0cb913948011a512bbacedbdcdd270bd91596dc09db73",
		"sessions_channel_no_delete":                  "93bc37af2a3107ff44e2416262492722b400ccf9c3038d5192cbfde42853bbb3",
		"sessions_channel_route_guard":                "1e39b1d5f20ce3d453af669d63f355ceed87008b9f5f598ab2a98d498d9e9efb",
		"sessions_channel_route_no_delete":            "753505cd2f58b28bd3ef379363be91102ec48a26ca5f89b6f402bd0182853a49",
		"sessions_channel_subscription_guard":         "4fbec18f080bc2195103b396d109a484c7dcc40d1b897091302e5fbfc2be6088",
		"sessions_channel_subscription_no_delete":     "6e8459f6e0908b81d433ec73c0ac7afae729a9b94978dd2e29cba0db90a216c1",
		"sessions_communication_command_guard":        "fdef4326ded0968658859b969e82fd7fbfed5f5ae3501d8c9c7d7eea22f8d567",
		"sessions_communication_endpoint_guard":       "2d3d3322e286178b29e193f0a65e0564b2777ffc1c7c4efccc70627660750998",
		"sessions_communication_endpoint_no_delete":   "ab32af900ae0b990f2ed83001305dde42505689be528e48ff130147aefac6cef",
		"sessions_communication_guard_guard":          "aa625e70e658c1fde98135adefd8eb2636aaf5c3c2101425beab4f5c5015f059",
		"sessions_communication_guard_no_delete":      "408165651cf18f6ac9da71c5e498173863762c18a95273c5d7b2467d6ab6d23e",
		"sessions_decision_request_guard":             "a70af9db9dd5244f33876693c50b0e9b87cb80f8fa998715d7a07edeec4b7cab",
		"sessions_decision_request_no_delete":         "84419d157e55e126035bc54be0090d86480ae95a5304e33cbf3131fcadd87139",
		"sessions_decision_response_guard":            "ffb9a01c2c036afeef2ae139f6f2c89eb938b09093b6bcada7a80086af58081e",
		"sessions_delivery_attempt_guard":             "70b1ef42c95e65bb48ce45f784a523ae3f7d67587d77da8953579a80a4463581",
		"sessions_delivery_attempt_no_delete":         "784a04ef6cc9b73d4a35a239a2f55d419b184de97fa575ce85ceb5ab5082736a",
		"sessions_delivery_dispatch_guard":            "030c3b808d7e45d7bd095e5042c89f16d31140e191673c8ff8ee6e825e18d2a8",
		"sessions_delivery_dispatch_no_delete":        "918c149d09f89a987a1fa676b48bbc181c548aeaab12c1c2d6ea6c7850dc88e2",
		"sessions_inbox_cursor_barrier_guard":         "30e5c210a77800b25d2739a7fb23b0d5314d6b80aee3a28bebe9115dbcea577a",
		"sessions_inbox_cursor_barrier_no_delete":     "661f40fb543e2d4b2dfa2318e99a9c7276ec0bd411bf83c948c2917012366801",
		"sessions_inbox_cursor_guard":                 "722248409daf3896db594c837472da03fed15913d19c8c13482d076dabe4d330",
		"sessions_inbox_cursor_no_delete":             "ff530e5c80350aa96f7ed08e6557f73cc4172b6377d5bdac1718cafb55a41c37",
		"sessions_message_ack_guard":                  "30440089e804d9e51353c7f9646e8a7ce88c81e2f139630a2a96b87656ce65b6",
		"sessions_message_audience_guard":             "0044f19b50de17f0cade200ce2d5f3974e44c5b941b7febdc004c074872db2e5",
		"sessions_message_audience_recipient_guard":   "c0f4670998bfd7a81dd352394ab73ee98c4a7ad4e0db14ce3fc43360faa84490",
		"sessions_message_delivery_guard":             "628a1b409b317585689f7a46238ec14cf81267f9cc1f26eb4db2c86d789d6e6a",
		"sessions_message_delivery_no_delete":         "d7c9cedfa237b6ee91378615f489b031a17b6026d1a9fd10883125caa6da87c3",
		"sessions_message_guard":                      "8b8387002aa86ed1bacdfbe9ce413d5108a818af793a84be2f09f4a42a43c38e",
		"sessions_message_no_delete":                  "6dbe749a6f6303c8517ae2257b9005a05945a22f5ae7ed8460645fd9fb23b4ed",
		"sessions_work_event_guard":                   "c73cb52c4c398031b55605934931b26f62aabca9ffc8839afdaf82bde97d37d6",
		"sessions_work_handoff_guard":                 "516b6c0c369788e1276c201348f855b273480f927f7ddd88334398a6a7e4e840",
		"sessions_work_handoff_no_delete":             "c406b889147cffce2ac4c85fe311cc5cfc22164d0a54d79e27e46161256753f6",
	}
	sqliteDigests := map[string]string{
		"sessions_channel_grant_guard_ins":              "bcb1d2b9ec3661d572569df9f12d0dbf7aa069df668b8658e5663d6f8ba98b1a",
		"sessions_channel_grant_guard_upd":              "18b73281c0be5b1a1290119be2068da4022759a5ee84725b4ad93b32346a9933",
		"sessions_channel_grant_no_delete":              "9cc668d6ae2b425dff6b673ad89eb1269e2c89756a53af426cb32f17313e3865",
		"sessions_channel_guard_ins":                    "4a2a4df7f09fa83982f6df69b275cc0eac549092c216b693f8df97ea04a1cc8d",
		"sessions_channel_guard_upd":                    "a488d05fa9501a586f4ea39394b1a6d79b60cd7e7617fc89dca36fb17ca76f81",
		"sessions_channel_label_definition_guard_ins":   "d144b155a801af8cfceb03d02514c50501d161da8caecd850e7354a45fc3810e",
		"sessions_channel_label_definition_guard_upd":   "f5693f895acb893b14c883663bfd1115c073aae04e20e82aa886a1443134aa88",
		"sessions_channel_label_definition_no_delete":   "dc62aa6facd488ceadfb70a6d95e65390fa03a8921d60a6f7cc40873e37fc544",
		"sessions_channel_no_delete":                    "3701f6b3dec8317b4cb82c2684e445abf845c210460ca3d07ec2e4c6a6706bd4",
		"sessions_channel_route_guard_ins":              "2651f7c44bcdb1340580fdcf67095390f2e7606892d12f3a6932dc46b4bd9bdf",
		"sessions_channel_route_guard_upd":              "ba6c511ff92571ccd03e4ed50427cf1a7a44fb1eda0b01bbe009f64b0ad7375a",
		"sessions_channel_route_no_delete":              "2b61e03aa561289a8489fd2bea8181d3c4e3595abae5f41600cec1d76a0012a8",
		"sessions_channel_subscription_guard_ins":       "141d53133986de143cf71960fd96df00284bc1634d899b761943225ec9ff91f1",
		"sessions_channel_subscription_guard_upd":       "babfdb56cca131a80f4c4775b45a6b368314d9bc8b48776bfb110f44a7cac001",
		"sessions_channel_subscription_no_delete":       "d9b0f8905449dc626c80f688b9c64e62c568b9aec30ae1b707f44c5be1e439fa",
		"sessions_communication_command_guard_ins":      "7946feb2f27f444cdc85601690df5ce9e19248dd114db9490f1bb7cee4f583f2",
		"sessions_communication_endpoint_guard_ins":     "5f520c3d70304ab6e406f59e87fbd5c1364c8b9af0ea442e894ea89dda3aec9f",
		"sessions_communication_endpoint_guard_upd":     "650d34b494b122dffd4280d91c9939dbafe8ffc26227bb0e70cb801d3f31a3bf",
		"sessions_communication_endpoint_no_delete":     "3b099920cf6db47d113f3ac001217b39b563ae31eeeca1a434ac0894d272722c",
		"sessions_communication_guard_guard_ins":        "695c4b52db061956aed29b2fe409d5588a9ec154dcf5bd4d09c14a02a1252f75",
		"sessions_communication_guard_guard_upd":        "a2e61533f43bd17c3024b3cd20ddc2ac53e90744d23995e6988c1e3a3ba53fb8",
		"sessions_communication_guard_no_delete":        "e26cd4f0468940f4b4efde1c05381fb3dd089cfd1f17933fcd765a074f89abe0",
		"sessions_decision_request_guard_ins":           "66324605c36d198d0e86c081565568deedd9c1452521295df5d0fb1489efb90d",
		"sessions_decision_request_guard_upd":           "2689e70cce6bd54afc9e817b7d72d09934adb73ebf969b1da523a1d01b6ddbb9",
		"sessions_decision_request_no_delete":           "8d6543679e2fd1d3fdbb2c40c50ed977e325cce73f7da1a11f36b9140e893168",
		"sessions_decision_response_guard_ins":          "aed2ac2e1f576a0ef0630b255a86ff9e073f09b10539382a888a52163a90acef",
		"sessions_delivery_attempt_guard_ins":           "4a90ae8e25dc2cad82afdd806b7c6a6078f9f4fb8d9c60dbac38db166d8c24e7",
		"sessions_delivery_attempt_guard_upd":           "7eacd471ac022f65b18544dc3897b82521eeb8e6c1b0ea3c3f9a61562caae91c",
		"sessions_delivery_attempt_no_delete":           "0051db2398efa5394137a28e69aa8a071ab79669d39ca3f28d1e97c21b5472dc",
		"sessions_delivery_dispatch_guard_ins":          "8425ae58c337471b70b19a673e7223cbb3bc4937158876ffce569effb6aa75c4",
		"sessions_delivery_dispatch_guard_upd":          "5853d994ce17c636e1e27cec8ddd1e9e7874fc39ff152674b9c7cb79767b3e82",
		"sessions_delivery_dispatch_no_delete":          "e3d04f17a42a6db8d084e268c0d7bd84d401b8bec3426cccda3780689b57d7d3",
		"sessions_inbox_cursor_barrier_guard_ins":       "cbbe94177ccf925dbadd7f7fc029d452bf6952dd9282be107dbd4abb4f59f844",
		"sessions_inbox_cursor_barrier_guard_upd":       "90eb45c14447cfb22627b5d9336fc393985b81b68937909d87240938ccfd11eb",
		"sessions_inbox_cursor_barrier_no_delete":       "ee6f84aff380b660f5c6e5953b8a888bba383b48236d777208090faaf3d994fe",
		"sessions_inbox_cursor_guard_ins":               "f4c990cec118d748bdd56b2433745e4c3b4153be685c41c4da35ada92f7e0808",
		"sessions_inbox_cursor_guard_upd":               "a956704b8f553f5ad835274727e92008bfce3175b2c4ed3641470259c3bfe7ed",
		"sessions_inbox_cursor_no_delete":               "8b3fa8875262eb083aae0e3db06f98ddc36a48749531df118831e3ea5502dae1",
		"sessions_message_ack_guard_ins":                "55763053419278682cf49d2fe8bb9252eb1687917fd95520a88cc3c55d8ebbda",
		"sessions_message_audience_guard_ins":           "08422f7f9dc9b094cfb88874b9092059967d007cfb03721a17f83a68095ff115",
		"sessions_message_audience_recipient_guard_ins": "af3def1f9fd70cb71bf7b185ce66911f7ac0eae46cf7604564bee07dfa995a01",
		"sessions_message_delivery_guard_ins":           "d4a7df3b701c3e1d0e714fcff429ab546edfe857b6c38c09b9f9b9d513872d46",
		"sessions_message_delivery_guard_upd":           "cde67cf80256159358ff3fafb1bc5adf05a79fc9ec1afb42be2614d735e05842",
		"sessions_message_delivery_no_delete":           "f9dde89761f39324e585b0d71317b3c50f4f2938727ee679a628bab6ff8e10ce",
		"sessions_message_guard_ins":                    "3e54ccd0009b9864c7489b36d586baadef1ad2fc46fc2d33e4eb5cb10a3e3337",
		"sessions_message_guard_upd":                    "450bf53ee30b25ca0d35aa14cdf9a5f1113b6bf921c3c3be5d1783f55151fb53",
		"sessions_message_no_delete":                    "48ecbebde94ce17d52d729b5866c8692ca9aee2f7c3d790ace60c7e74061126d",
		"sessions_work_event_guard_ins":                 "8f7247fd7c0e8f2be3e75950d085f1199bfac643cdb17c610a8f119f982af334",
		"sessions_work_handoff_guard_ins":               "e4963f98519967bdf70719084c2aea92d4a6a8f75c4d0a44a8d22eac54890e61",
		"sessions_work_handoff_guard_upd":               "33d8411f8dbb0ea700633e5e0ecff54e85edd8745e21ffb3b0f70c331d7830a0",
		"sessions_work_handoff_no_delete":               "49016fd089d66a5a292ad9d5567179037e1f9a07742e630685e0cb46e9eef438",
	}

	postgres := make([]store.SchemaTrigger, 0, len(definitions)+16)
	sqlite := make([]store.SchemaTrigger, 0, len(definitions)*2+16)
	for _, definition := range definitions {
		postgresGuard := definition.table + "_guard"
		postgresTrigger := store.SchemaTrigger{
			Name: postgresGuard, Table: definition.table,
			DefinitionSHA256: postgresDigests[postgresGuard],
		}
		if postgresGuard == "sessions_communication_command_guard" {
			postgresTrigger.Transitions = []store.SchemaTriggerTransition{{
				MigrationVersion:         18,
				PreviousDefinitionSHA256: "93b8463fa70601b2c68318f3572c75e8341aae8753fa681d63cb4722f3bd396a",
				PostgresFunctionIdentity: &store.SchemaTriggerFunctionIdentityTransition{
					PreviousName: "olivares_sessions_communication_validate",
					NextName:     "olivares_sessions_communication_command_validate_v18",
				},
			}}
		}
		postgres = append(postgres, postgresTrigger)
		sqliteInsert := definition.table + "_guard_ins"
		sqliteTrigger := store.SchemaTrigger{
			Name: sqliteInsert, Table: definition.table,
			DefinitionSHA256: sqliteDigests[sqliteInsert],
		}
		if sqliteInsert == "sessions_communication_command_guard_ins" {
			sqliteTrigger.Transitions = []store.SchemaTriggerTransition{
				{
					MigrationVersion:         85,
					PreviousDefinitionSHA256: "f67652ec1ac04d9a0cc42178a450a5416059578ee13a46854235cca57f67a085",
				},
				{
					MigrationVersion:         86,
					PreviousDefinitionSHA256: "ba6bdd1a2e669b4b54287edf4b1c2423a4b740af317e70c0f9f7c85e26088f40",
				},
			}
		}
		sqlite = append(sqlite, sqliteTrigger)
		if definition.mutable {
			postgresNoDelete := definition.table + "_no_delete"
			postgres = append(postgres, store.SchemaTrigger{
				Name: postgresNoDelete, Table: definition.table,
				DefinitionSHA256: postgresDigests[postgresNoDelete],
			})
			sqliteUpdate := definition.table + "_guard_upd"
			sqliteNoDelete := definition.table + "_no_delete"
			sqlite = append(sqlite,
				store.SchemaTrigger{
					Name: sqliteUpdate, Table: definition.table,
					DefinitionSHA256: sqliteDigests[sqliteUpdate],
				},
				store.SchemaTrigger{
					Name: sqliteNoDelete, Table: definition.table,
					DefinitionSHA256: sqliteDigests[sqliteNoDelete],
				},
			)
		}
	}
	postgres = append(postgres, store.SchemaTrigger{
		Name: "sessions_work_event_guard", Table: workEventTable,
		DefinitionSHA256: postgresDigests["sessions_work_event_guard"],
	})
	sqlite = append(sqlite, store.SchemaTrigger{
		Name: "sessions_work_event_guard_ins", Table: workEventTable,
		DefinitionSHA256: sqliteDigests["sessions_work_event_guard_ins"],
	})
	return map[store.Engine][]store.SchemaTrigger{
		store.EnginePostgres: postgres,
		store.EngineSQLite:   sqlite,
	}
}

func sessionsSchemaInvariants() map[store.Engine][]store.SchemaTrigger {
	work := workSchemaInvariants()
	communication := communicationSchemaInvariants()
	protocolBinding := protocolBindingSchemaInvariants()
	combined := make(map[store.Engine][]store.SchemaTrigger, len(work))
	for _, engine := range store.SupportedEngines() {
		replacements := make(map[string]struct{})
		for _, trigger := range communication[engine] {
			if trigger.Table == workEventTable {
				replacements[trigger.Name] = struct{}{}
			}
		}
		for _, trigger := range work[engine] {
			if trigger.Table == workEventTable {
				if _, replaced := replacements[trigger.Name]; replaced {
					continue
				}
			}
			combined[engine] = append(combined[engine], trigger)
		}
		combined[engine] = append(combined[engine], communication[engine]...)
		combined[engine] = append(combined[engine], protocolBinding[engine]...)
	}
	return combined
}
