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
	return append([]model.FieldSpec{{Name: colWorkWorkspaceID, Kind: model.KindUUID, Principal: pdeclNoneWorkspaceID}}, extra...)
}

func communicationFieldGroups(groups ...[]model.FieldSpec) []model.FieldSpec {
	fields := []model.FieldSpec{{Name: colWorkWorkspaceID, Kind: model.KindUUID, Principal: pdeclNoneWorkspaceID}}
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
		{Name: prefix + "_encoding", Kind: model.KindText, Nullable: optional, Principal: pdeclNonePayloadEncoding},
		{Name: prefix + "_plain_json", Kind: model.KindJSON, Nullable: true, Principal: pdeclPayloadPlain[prefix]},
		{Name: prefix + "_sealed_json", Kind: model.KindJSON, Nullable: true, Principal: pdeclPayloadSealed},
		{Name: prefix + "_schema", Kind: model.KindText, Nullable: optional, Principal: pdeclNonePayloadSchema},
		{Name: prefix + "_digest", Kind: model.KindBytes, Nullable: optional, Principal: pdeclNonePayloadDigest},
		{Name: prefix + "_seal_key_version", Kind: model.KindText, Nullable: true, Principal: pdeclNonePayloadKeyVersion},
		{Name: prefix + "_digest_key_version", Kind: model.KindText, Nullable: true, Principal: pdeclNonePayloadKeyVersion},
		{Name: prefix + "_protection_generation", Kind: model.KindInt, Nullable: optional},
	}
}

func (m *Module) registerCommunicationSchema(reg store.ExtensionRegistry) error {
	descriptors := []model.EntityDescriptor{
		{
			Kind: channelKind, Table: channelTable, RetainOnTenantDrop: true,
			WorkspaceLineage: hiddenWorkspaceLineage,
			Fields: communicationFields(
				model.FieldSpec{Name: colCommSlug, Kind: model.KindText, Principal: model.None("a channel slug, a bounded vocabulary token: communication_state.go:1227")},
				model.FieldSpec{Name: colCommName, Kind: model.KindText, Principal: model.None("a channel display name, bounded prose: communication_state.go:1227")},
				model.FieldSpec{Name: colCommDescription, Kind: model.KindText, Nullable: true, Principal: model.None("channel description prose: communication_state.go:1228")},
				model.FieldSpec{Name: colCommKind, Kind: model.KindText, Principal: model.None("a channel kind, a closed set: communication_state.go:60-62, communication_state.go:1229")},
				model.FieldSpec{Name: colCommState, Kind: model.KindText, Principal: model.None("a channel state, a closed set: communication_state.go:64, communication_state.go:1229")},
				model.FieldSpec{Name: colCommSensitivity, Kind: model.KindText, Principal: model.None("a channel sensitivity, a closed set: communication_state.go:66-68, communication_state.go:1229")},
				model.FieldSpec{Name: colCommContentProtection, Kind: model.KindText, Principal: model.None("a content protection mode, a closed set: communication_state.go:70-72, communication_state.go:1230")},
				model.FieldSpec{Name: colCommProtectionGeneration, Kind: model.KindInt},
				model.FieldSpec{Name: colCommDefaultAckPolicy, Kind: model.KindText, Principal: pdeclNoneAckPolicy},
				model.FieldSpec{Name: colCommDefaultAckTimeoutMS, Kind: model.KindInt},
				model.FieldSpec{Name: colCommDefaultWake, Kind: model.KindText, Principal: pdeclNoneWakePolicy},
				model.FieldSpec{Name: colCommRetentionPolicyRef, Kind: model.KindText, Nullable: true, Principal: model.None("an opaque retention policy reference, checked only for shape: communication_state.go:1234")},
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
				model.FieldSpec{Name: colCommChannelID, Kind: model.KindUUID, Principal: pdeclNoneChannelID},
				model.FieldSpec{Name: colCommSubjectKind, Kind: model.KindText, Principal: pdeclNoneCommSubjectKind},
				model.FieldSpec{Name: colCommSubjectRef, Kind: model.KindText, Principal: model.KindRef(colCommSubjectKind, model.ClassAuthority)},
				model.FieldSpec{Name: colCommGeneration, Kind: model.KindInt},
				model.FieldSpec{Name: colCommCanRead, Kind: model.KindBool},
				model.FieldSpec{Name: colCommCanWrite, Kind: model.KindBool},
				model.FieldSpec{Name: colCommCanAdmin, Kind: model.KindBool},
				model.FieldSpec{Name: colCommState, Kind: model.KindText, Principal: model.None("a channel grant state, a closed set: communication_state.go:78-80, communication_state.go:1299")},
				model.FieldSpec{Name: colCommGrantedByKind, Kind: model.KindText, Principal: pdeclNoneCommActorKind},
				model.FieldSpec{Name: colCommGrantedByRef, Kind: model.KindText, Principal: model.KindRef(colCommGrantedByKind, model.ClassEvidence)},
				model.FieldSpec{Name: colCommRevokedByKind, Kind: model.KindText, Nullable: true, Principal: pdeclNoneCommActorKind},
				model.FieldSpec{Name: colCommRevokedByRef, Kind: model.KindText, Nullable: true, Principal: model.KindRef(colCommRevokedByKind, model.ClassEvidence)},
				model.FieldSpec{Name: colCommExpiresAt, Kind: model.KindTimestamp, Nullable: true},
				model.FieldSpec{Name: colCommSupersedesID, Kind: model.KindUUID, Nullable: true, Principal: model.None("the id of the grant generation this one supersedes: communication_state.go:1301-1303")},
			),
			Indexes: communicationIndexes("sessions_channel_grant_workspace",
				model.IndexSpec{Name: "sessions_channel_grant_uniq", Columns: []string{model.ColTenantID, colCommChannelID, colCommSubjectKind, colCommSubjectRef, colCommGeneration}, Unique: true},
				model.IndexSpec{Name: "sessions_channel_grant_predecessor_uniq", Columns: []string{model.ColTenantID, colCommSupersedesID}, Unique: true},
				model.IndexSpec{Name: "sessions_channel_grant_subject", Columns: []string{model.ColTenantID, colWorkWorkspaceID, colCommSubjectKind, colCommSubjectRef, colCommState, model.ColID}},
				model.IndexSpec{Name: "sessions_channel_grant_channel", Columns: []string{model.ColTenantID, colCommChannelID, colCommState, model.ColID}},
				// sessions_channel_grant_catalog is the grant-first catalog projection
				// index: for one closure subject it answers "which channel_id values
				// carry a current read grant" as an index range scan ordered by
				// channel_id, with the expiry column available in the index so the
				// unset-or-after predicate needs no row visit. It is declared here,
				// not in a migration, because the schema reconciler creates a newly
				// declared index on an existing table with CREATE INDEX IF NOT EXISTS
				// on both engines (core/internal/store/sqlstore/schema.go
				// reconcileIndexStmts); prior migrations are untouched.
				model.IndexSpec{Name: "sessions_channel_grant_catalog", Columns: []string{model.ColTenantID, colWorkWorkspaceID, colCommSubjectKind, colCommSubjectRef, colCommState, colCommCanRead, colCommChannelID, colCommExpiresAt}},
				// sessions_channel_grant_administration is the SAME measured shape for
				// the administrative catalog's projection, on the admin bit instead of
				// the read bit: for one closure subject it answers "which channel_id
				// values carry a current ADMIN grant" as an index range scan ordered by
				// channel_id, with the expiry column in the index so the unset-or-after
				// predicate needs no row visit. It is a separate index and not a widened
				// catalog one, because widening would move can_read behind can_admin and
				// deform the read catalog's own measured plan.
				model.IndexSpec{Name: "sessions_channel_grant_administration", Columns: []string{model.ColTenantID, colWorkWorkspaceID, colCommSubjectKind, colCommSubjectRef, colCommState, colCommCanAdmin, colCommChannelID, colCommExpiresAt}},
				// sessions_channel_grant_history serves the administrative sheet's
				// keyset page over one Channel's generations. The existing
				// sessions_channel_grant_channel index leads with state, so the `all`
				// selection could not range-scan it by id; this one leads with the
				// Channel and keeps id as the ordered keyset column, and the state
				// column trails so a state-filtered page is still index-covered.
				model.IndexSpec{Name: "sessions_channel_grant_history", Columns: []string{model.ColTenantID, colCommChannelID, model.ColID, colCommState}},
				// sessions_channel_grant_subject_history serves the same page with the
				// exact-subject filter, so inspecting one subject's generations never
				// walks the whole Channel history.
				model.IndexSpec{Name: "sessions_channel_grant_subject_history", Columns: []string{model.ColTenantID, colCommChannelID, colCommSubjectKind, colCommSubjectRef, model.ColID, colCommState}},
				// sessions_channel_grant_subject_generation serves the ADMIN WRITER's
				// one history question: the exact highest generation ONE subject
				// holds on ONE Channel, which fixes the successor's number and its
				// supersedes_id. Every equality the statement binds — the tenant and
				// the workspace lineage the store forces, then the Channel and the
				// subject — leads, and `generation` follows them with `id` as the
				// tiebreaker in the same direction, so the read is an exact reverse
				// index scan of two rows however long the history behind them is.
				// MEASURED, not assumed: without it SQLite serves the same statement
				// from sessions_channel_grant_catalog and adds
				// "USE TEMP B-TREE FOR ORDER BY", i.e. it sorts the subject's whole
				// history to answer a two-row question; dropping `id` from the tail
				// downgrades it to "USE TEMP B-TREE FOR LAST TERM OF ORDER BY", and
				// dropping the workspace column makes the planner prefer the catalog
				// index again. It is declared here, like the three above, because the
				// reconciler issues a newly declared index on an existing table with
				// CREATE INDEX IF NOT EXISTS on both engines; no migration is edited.
				model.IndexSpec{Name: "sessions_channel_grant_subject_generation", Columns: []string{model.ColTenantID, colWorkWorkspaceID, colCommChannelID, colCommSubjectKind, colCommSubjectRef, colCommGeneration, model.ColID}},
				// sessions_channel_grant_subject_current serves the ADMIN WRITER's
				// other current-row question, asked once per closure subject and once
				// more for the subject a grant serves: "which persisted-active
				// generation does this subject hold on this Channel?". It shares the
				// previous index's equality prefix, adds `state` to it, and then
				// orders by `generation` with `id` as the tiebreaker — the same
				// ordering the statement asks for. An ABSENT current row therefore
				// costs an empty range rather than a search for a row that is not
				// there.
				// MEASURED on PostgreSQL 16, twice, and both measurements are the
				// reason this index has this exact shape:
				//   · without channel_id — i.e. relying on the pre-existing
				//     sessions_channel_grant_subject — the planner answered the
				//     question from sessions_channel_grant_pkey with
				//     "Rows Removed by Filter: 2199" of 2200 rows;
				//   · WITH channel_id but ordered by `id`, it did the same thing for
				//     the one subject that owned most of the relation, because an
				//     ordered primary-key scan satisfies `ORDER BY id` and the row
				//     estimate for a most-common subject_ref made an early LIMIT hit
				//     look cheap. Ordering by `generation` removes that substitution:
				//     the primary key cannot serve it.
				// SQLite chose a bounded plan in every one of those variants. One
				// engine agreeing is not the measurement.
				model.IndexSpec{Name: "sessions_channel_grant_subject_current", Columns: []string{model.ColTenantID, colWorkWorkspaceID, colCommChannelID, colCommSubjectKind, colCommSubjectRef, colCommState, colCommGeneration, model.ColID}},
			),
		},
		{
			Kind: channelSubscriptionKind, Table: channelSubscriptionTable, RetainOnTenantDrop: true,
			WorkspaceLineage: hiddenWorkspaceLineage,
			Fields: communicationFields(
				model.FieldSpec{Name: colCommChannelID, Kind: model.KindUUID, Principal: pdeclNoneChannelID},
				model.FieldSpec{Name: colCommSubscriberKind, Kind: model.KindText, Principal: pdeclNoneCommSubjectKind},
				model.FieldSpec{Name: colCommSubscriberRef, Kind: model.KindText, Principal: model.KindRef(colCommSubscriberKind, model.ClassAuthority)},
				model.FieldSpec{Name: colCommGeneration, Kind: model.KindInt},
				model.FieldSpec{Name: colCommMode, Kind: model.KindText, Principal: model.None("a subscription mode, a closed set: communication_state.go:86-88, communication_state.go:1322")},
				model.FieldSpec{Name: colCommWake, Kind: model.KindText, Principal: pdeclNoneWakePolicy},
				model.FieldSpec{Name: colCommRequiredForCritical, Kind: model.KindBool},
				model.FieldSpec{Name: colCommState, Kind: model.KindText, Principal: model.None("a subscription state, a closed set: communication_state.go:92-94, communication_state.go:1323")},
				model.FieldSpec{Name: colCommFilterJSON, Kind: model.KindJSON, Nullable: true, Principal: model.None("an opaque canonical JSON filter checked only for canonical form and its digest; no reader interprets it: communication_state.go:1204-1219, communication_state.go:1329, communication_codec.go:732")},
				model.FieldSpec{Name: colCommFilterHash, Kind: model.KindBytes, Nullable: true, Principal: model.None("the SHA-256 of the subscription filter: communication_state.go:1216-1218, communication_state.go:1329")},
				model.FieldSpec{Name: colCommSupersedesID, Kind: model.KindUUID, Nullable: true, Principal: model.None("the id of the subscription generation this one supersedes: communication_state.go:1324-1326")},
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
				model.FieldSpec{Name: colCommChannelID, Kind: model.KindUUID, Principal: pdeclNoneChannelID},
				model.FieldSpec{Name: colCommLabelKey, Kind: model.KindText, Principal: model.None("a label key, a bounded vocabulary token: communication_state.go:1343")},
				model.FieldSpec{Name: colCommGeneration, Kind: model.KindInt},
				model.FieldSpec{Name: colCommAllowedValuesJSON, Kind: model.KindJSON, Principal: pdeclLabelVocabulary},
				model.FieldSpec{Name: colCommValuesHash, Kind: model.KindBytes, Principal: model.None("the SHA-256 of the label vocabulary: communication_state.go:1216-1218, communication_state.go:1347")},
				model.FieldSpec{Name: colCommClassification, Kind: model.KindText, Principal: model.None("a label classification, a closed set of one: communication_state.go:137, communication_state.go:1344")},
				model.FieldSpec{Name: colCommState, Kind: model.KindText, Principal: model.None("a label definition state, a closed set: communication_state.go:96, communication_state.go:1344")},
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
				model.FieldSpec{Name: colCommRouteKey, Kind: model.KindText, Principal: model.None("a route key, a bounded vocabulary token: communication_state.go:1375")},
				model.FieldSpec{Name: colCommGeneration, Kind: model.KindInt},
				model.FieldSpec{Name: colCommPriority, Kind: model.KindInt},
				model.FieldSpec{Name: colCommSourceKind, Kind: model.KindText, Principal: model.None("a route source kind, a closed set: communication_state.go:98-100, communication_state.go:1376")},
				model.FieldSpec{Name: colCommEventType, Kind: model.KindText, Nullable: true, Principal: model.None("a routed event type, a bounded vocabulary token: communication_state.go:1384")},
				model.FieldSpec{Name: colCommMessageKind, Kind: model.KindText, Nullable: true, Principal: pdeclNoneMessageKind},
				model.FieldSpec{Name: colCommMinimumUrgency, Kind: model.KindText, Nullable: true, Principal: pdeclNoneUrgency},
				model.FieldSpec{Name: colCommLabelMatchJSON, Kind: model.KindJSON, Nullable: true, Principal: pdeclLabelMap},
				model.FieldSpec{Name: colCommTargetChannelID, Kind: model.KindUUID, Principal: model.None("the id of the Channel the route targets: communication_state.go:1376")},
				model.FieldSpec{Name: colCommAudienceKind, Kind: model.KindText, Principal: model.None("a route audience kind, a closed set with no account kind: communication_state.go:102-105, communication_state.go:1377")},
				model.FieldSpec{Name: colCommAudienceRef, Kind: model.KindUUID, Nullable: true, Principal: model.None("the id of the user group or agent group a route addresses, required only for the two group audience kinds and never an account: communication_state.go:1411-1414")},
				model.FieldSpec{Name: colCommAckPolicy, Kind: model.KindText, Principal: pdeclNoneAckPolicy},
				model.FieldSpec{Name: colCommWakePolicy, Kind: model.KindText, Principal: pdeclNoneWakePolicy},
				model.FieldSpec{Name: colCommCatchAll, Kind: model.KindBool},
				model.FieldSpec{Name: colCommState, Kind: model.KindText, Principal: model.None("a route state, a closed set: communication_state.go:107, communication_state.go:1378")},
				model.FieldSpec{Name: colCommSupersedesID, Kind: model.KindUUID, Nullable: true, Principal: model.None("the id of the route generation this one supersedes: communication_state.go:1379-1381")},
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
				model.FieldSpec{Name: colCommOwnerKind, Kind: model.KindText, Principal: pdeclNoneCommRecipientKind},
				model.FieldSpec{Name: colCommOwnerRef, Kind: model.KindText, Principal: model.KindRef(colCommOwnerKind, model.ClassAuthority)},
				model.FieldSpec{Name: colCommProviderKey, Kind: model.KindText, Principal: model.None("an endpoint provider key from a closed list or a driver prefix: communication_state.go:1447, communication_state.go:1468-1475")},
				model.FieldSpec{Name: colTransport, Kind: model.KindText, Principal: model.None("an endpoint transport, a bounded vocabulary token: communication_state.go:1448")},
				model.FieldSpec{Name: colCommEndpointRef, Kind: model.KindText, Principal: model.None("an opaque endpoint reference checked only for shape: communication_state.go:1448")},
				model.FieldSpec{Name: colCommSessionSID, Kind: model.KindText, Nullable: true, Principal: pdeclNoneSID},
				model.FieldSpec{Name: colCommCapabilitiesJSON, Kind: model.KindJSON, Principal: model.None("an opaque canonical JSON capability document checked only for canonical form and size: communication_state.go:1461-1462, communication_codec.go:862")},
				model.FieldSpec{Name: colCommTransportFingerprint, Kind: model.KindText, Nullable: true, Principal: model.None("an opaque transport fingerprint checked only for shape: communication_state.go:1451")},
				model.FieldSpec{Name: colCommSupportLevel, Kind: model.KindText, Principal: model.None("an endpoint support level, a closed set: communication_state.go:109-111, communication_state.go:1449")},
				model.FieldSpec{Name: colCommPriority, Kind: model.KindInt},
				model.FieldSpec{Name: colCommState, Kind: model.KindText, Principal: model.None("an endpoint state, a closed set: communication_state.go:113-115, communication_state.go:1449")},
				model.FieldSpec{Name: colCommHeartbeatExpiresAt, Kind: model.KindTimestamp, Nullable: true},
				model.FieldSpec{Name: colCommGeneration, Kind: model.KindInt},
				model.FieldSpec{Name: colCommSecretRef, Kind: model.KindText, Nullable: true, Principal: model.None("an opaque secret locator, never a value or an account: communication_state.go:1452")},
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
					{Name: colCommChannelID, Kind: model.KindUUID, Principal: pdeclNoneChannelID},
					{Name: colWorkItemID, Kind: model.KindUUID, Nullable: true, Principal: pdeclNoneWorkItemID},
					{Name: colCommThreadID, Kind: model.KindUUID, Principal: model.None("the id of the root message of the thread: communication_state.go:1548, communication_state.go:1554-1556")},
					{Name: colCommKind, Kind: model.KindText, Principal: pdeclNoneMessageKind},
					{Name: colCommState, Kind: model.KindText, Principal: model.None("a message state, a closed set: communication_state.go:130-132, communication_state.go:1549")},
					{Name: colCommSenderKind, Kind: model.KindText, Principal: pdeclNoneCommActorKind},
					{Name: colCommSenderRef, Kind: model.KindText, Principal: model.KindRef(colCommSenderKind, model.ClassEvidence)},
				},
				protectedPayloadFields("payload", false),
				[]model.FieldSpec{
					{Name: colCommLabelsJSON, Kind: model.KindJSON, Nullable: true, Principal: pdeclLabelMap},
					{Name: colCommLabelsHash, Kind: model.KindBytes, Nullable: true, Principal: model.None("the SHA-256 of the canonical message labels: communication_state.go:1573-1579")},
					{Name: colCommUrgency, Kind: model.KindText, Principal: pdeclNoneUrgency},
					{Name: colCommAckPolicy, Kind: model.KindText, Principal: pdeclNoneAckPolicy},
					{Name: colCommAckQuorum, Kind: model.KindInt},
					{Name: colCommAvailableAt, Kind: model.KindTimestamp},
					{Name: colCommAckDueAt, Kind: model.KindTimestamp, Nullable: true},
					{Name: colCommExpiresAt, Kind: model.KindTimestamp, Nullable: true},
					{Name: colCommReplyToID, Kind: model.KindUUID, Nullable: true, Principal: model.None("the id of the message this one replies to: communication_state.go:1558")},
					{Name: colCommSupersedesID, Kind: model.KindUUID, Nullable: true, Principal: model.None("the id of the message this one supersedes: communication_state.go:1582-1588")},
					{Name: colCommOriginEventID, Kind: model.KindUUID, Nullable: true, Principal: model.None("the id of the event a message originated from: communication_state.go:1582-1588")},
					{Name: colCommAutomationDepth, Kind: model.KindInt},
					{Name: colCommPublishedAt, Kind: model.KindTimestamp, Nullable: true},
					{Name: colCommTerminalAt, Kind: model.KindTimestamp, Nullable: true},
					{Name: colCommTerminalCode, Kind: model.KindText, Nullable: true, Principal: model.None("a bounded terminal code: communication_state.go:1637")},
				},
				protectedPayloadFields("terminal_reason", true),
				[]model.FieldSpec{
					{Name: colCommAudienceHash, Kind: model.KindBytes, Nullable: true, Principal: model.None("the SHA-256 sealing the message's resolved audience: communication_state.go:1649-1651")},
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
				model.FieldSpec{Name: colCommMessageID, Kind: model.KindUUID, Principal: pdeclNoneMessageID},
				model.FieldSpec{Name: colCommOrdinal, Kind: model.KindInt},
				model.FieldSpec{Name: colCommSelectorKind, Kind: model.KindText, Principal: pdeclNoneSelectorKind},
				model.FieldSpec{Name: colCommSelectorRef, Kind: model.KindText, Nullable: true, Principal: pdeclSelectorRef},
				model.FieldSpec{Name: colCommSelectorRequired, Kind: model.KindBool},
				model.FieldSpec{Name: colCommSelectorWakePolicy, Kind: model.KindText, Principal: pdeclNoneWakePolicy},
				model.FieldSpec{Name: colCommRouteRuleID, Kind: model.KindUUID, Nullable: true, Principal: pdeclNoneRouteRuleID},
				model.FieldSpec{Name: colCommChannelACLRevision, Kind: model.KindInt},
				model.FieldSpec{Name: colCommRouteRevision, Kind: model.KindInt},
				model.FieldSpec{Name: colCommSubscriptionRevision, Kind: model.KindInt},
				model.FieldSpec{Name: colCommDirectoryEpoch, Kind: model.KindInt},
				model.FieldSpec{Name: colCommDirectorySnapshotAt, Kind: model.KindTimestamp},
				model.FieldSpec{Name: colCommResolvedCount, Kind: model.KindInt},
				model.FieldSpec{Name: colCommSelectorHash, Kind: model.KindBytes, Principal: model.None("the SHA-256 of the canonical audience selector: communication_state.go:2160, communication_state.go:2163-2170")},
				model.FieldSpec{Name: colCommResolvedHash, Kind: model.KindBytes, Principal: model.None("the SHA-256 of the resolved audience: communication_state.go:2160")},
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
				model.FieldSpec{Name: colCommMessageAudienceID, Kind: model.KindUUID, Principal: model.None("the id of the audience selector row the arc belongs to: communication_state.go:4125")},
				model.FieldSpec{Name: colCommMessageDeliveryID, Kind: model.KindUUID, Principal: model.None("the id of the delivery the arc resolved to: communication_state.go:4126")},
				model.FieldSpec{Name: colCommRecipientKind, Kind: model.KindText, Principal: pdeclNoneCommRecipientKind},
				model.FieldSpec{Name: colCommRecipientRef, Kind: model.KindText, Principal: pdeclRecipientRef},
				model.FieldSpec{Name: colCommRecipientEpoch, Kind: model.KindInt},
				model.FieldSpec{Name: colCommRequired, Kind: model.KindBool},
				model.FieldSpec{Name: colCommWakePolicy, Kind: model.KindText, Principal: pdeclNoneWakePolicy},
				model.FieldSpec{Name: colCommRouteReasonsJSON, Kind: model.KindJSON, Principal: pdeclRouteReasons},
				model.FieldSpec{Name: colCommSelectorKind, Kind: model.KindText, Principal: pdeclNoneSelectorKind},
				model.FieldSpec{Name: colCommSelectorRef, Kind: model.KindText, Nullable: true, Principal: pdeclSelectorRef},
				model.FieldSpec{Name: colCommSelectorRequired, Kind: model.KindBool},
				model.FieldSpec{Name: colCommSelectorWakePolicy, Kind: model.KindText, Principal: pdeclNoneWakePolicy},
				model.FieldSpec{Name: colCommDirectoryEpoch, Kind: model.KindInt},
				model.FieldSpec{Name: colCommChannelACLRevision, Kind: model.KindInt},
				model.FieldSpec{Name: colCommRouteRevision, Kind: model.KindInt},
				model.FieldSpec{Name: colCommSubscriptionRevision, Kind: model.KindInt},
				model.FieldSpec{Name: colCommCausalKind, Kind: model.KindText, Principal: model.None("an audience causal kind, a closed set with no account kind: communication_state.go:151-154, communication_state.go:4131")},
				model.FieldSpec{Name: colCommCausalRef, Kind: model.KindText, Principal: pdeclCausalRef},
				model.FieldSpec{Name: colCommCausalFactKind, Kind: model.KindText, Nullable: true, Principal: model.None("the entity kind of the directory fact that witnessed the arc: communication_state.go:3972-3980, communication_state.go:4058-4100")},
				model.FieldSpec{Name: colCommCausalFactID, Kind: model.KindUUID, Nullable: true, Principal: model.None("the id of the directory fact row that witnessed the arc, a membership or roster row and never an account id: communication_state.go:3972-3980, communication_state.go:4058-4100")},
				model.FieldSpec{Name: colCommCausalFactVersion, Kind: model.KindInt, Nullable: true},
				model.FieldSpec{Name: colCommObservedSessionSID, Kind: model.KindText, Nullable: true, Principal: pdeclNoneSID},
				model.FieldSpec{Name: colCommObservedClaimFence, Kind: model.KindInt, Nullable: true},
				model.FieldSpec{Name: colCommOriginalSubscriberKind, Kind: model.KindText, Nullable: true, Principal: pdeclNoneCommSubjectKind},
				model.FieldSpec{Name: colCommOriginalSubscriberRef, Kind: model.KindText, Nullable: true, Principal: model.KindRef(colCommOriginalSubscriberKind, model.ClassEvidence)},
				model.FieldSpec{Name: colCommSubscriptionID, Kind: model.KindUUID, Nullable: true, Principal: model.None("the id of the subscription that produced a subscriber arc: communication_state.go:4046-4050")},
				model.FieldSpec{Name: colCommSubscriptionGeneration, Kind: model.KindInt, Nullable: true},
				model.FieldSpec{Name: colCommRouteRuleID, Kind: model.KindUUID, Nullable: true, Principal: pdeclNoneRouteRuleID},
				model.FieldSpec{Name: colCommRouteRuleGeneration, Kind: model.KindInt, Nullable: true},
				model.FieldSpec{Name: colCommCausalArcHash, Kind: model.KindBytes, Principal: model.None("the SHA-256 of the canonical causal arc: communication_state.go:4155-4173, communication_state.go:4176-4188")},
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
				model.FieldSpec{Name: colCommMessageID, Kind: model.KindUUID, Principal: pdeclNoneMessageID},
				model.FieldSpec{Name: colCommRecipientKind, Kind: model.KindText, Principal: pdeclNoneCommRecipientKind},
				model.FieldSpec{Name: colCommRecipientRef, Kind: model.KindText, Principal: pdeclRecipientRef},
				model.FieldSpec{Name: colCommRecipientEpoch, Kind: model.KindInt},
				model.FieldSpec{Name: colCommDeliverySeq, Kind: model.KindInt},
				model.FieldSpec{Name: colCommRequired, Kind: model.KindBool},
				model.FieldSpec{Name: colCommRouteReasonsJSON, Kind: model.KindJSON, Principal: pdeclRouteReasons},
				model.FieldSpec{Name: colCommWakePolicy, Kind: model.KindText, Principal: pdeclNoneWakePolicy},
				model.FieldSpec{Name: colCommState, Kind: model.KindText, Principal: model.None("a delivery state, a closed set: communication_state.go:156-159, communication_state.go:1955")},
				model.FieldSpec{Name: colCommAvailableAt, Kind: model.KindTimestamp},
				model.FieldSpec{Name: colCommFirstSeenAt, Kind: model.KindTimestamp, Nullable: true},
				model.FieldSpec{Name: colCommAckDueAt, Kind: model.KindTimestamp, Nullable: true},
				model.FieldSpec{Name: colCommExpiresAt, Kind: model.KindTimestamp, Nullable: true},
				model.FieldSpec{Name: colCommAckID, Kind: model.KindUUID, Nullable: true, Principal: model.None("the id of the acknowledgement that settled the delivery: communication_state.go:1993")},
				model.FieldSpec{Name: colCommAcknowledgedAt, Kind: model.KindTimestamp, Nullable: true},
				model.FieldSpec{Name: colCommLastWakeVerdict, Kind: model.KindText, Nullable: true, Principal: pdeclNoneVerdict},
				model.FieldSpec{Name: colCommLastWakeCode, Kind: model.KindText, Nullable: true, Principal: model.None("a bounded wake outcome code: communication_state.go:1980")},
				model.FieldSpec{Name: colCommLastWakeAt, Kind: model.KindTimestamp, Nullable: true},
				model.FieldSpec{Name: colCommRetirementTombstoneKind, Kind: model.KindText, Nullable: true, Principal: model.None("the entity kind of the directory tombstone that proved the recipient retired: communication_state.go:2006-2012")},
				model.FieldSpec{Name: colCommRetirementTombstoneID, Kind: model.KindUUID, Nullable: true, Principal: model.None("the id of the directory tombstone row that proved the recipient retired, the tombstone and never the account id: communication_state.go:2006-2012, core/store/directory.go:142-148")},
				model.FieldSpec{Name: colCommRetirementTombstoneVersion, Kind: model.KindInt, Nullable: true},
				model.FieldSpec{Name: colCommRetirementEpoch, Kind: model.KindInt, Nullable: true},
				model.FieldSpec{Name: colCommUndeliverableAt, Kind: model.KindTimestamp, Nullable: true},
				model.FieldSpec{Name: colCommUndeliverableCode, Kind: model.KindText, Nullable: true, Principal: model.None("a bounded undeliverable code: communication_state.go:2015")},
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
				model.FieldSpec{Name: colCommReaderKind, Kind: model.KindText, Principal: pdeclNoneCommRecipientKind},
				model.FieldSpec{Name: colCommReaderRef, Kind: model.KindText, Principal: pdeclReaderRef},
				model.FieldSpec{Name: colCommMailboxKind, Kind: model.KindText, Principal: pdeclNoneMailboxKind},
				model.FieldSpec{Name: colCommMailboxRef, Kind: model.KindText, Principal: pdeclMailboxRef},
				model.FieldSpec{Name: colCommLastSeenSeq, Kind: model.KindInt},
				model.FieldSpec{Name: colCommLastSeenAt, Kind: model.KindTimestamp},
				model.FieldSpec{Name: colCommFilterHash, Kind: model.KindBytes, Principal: pdeclNoneCursorFilterHash},
			),
			Indexes: communicationIndexes("sessions_inbox_cursor_workspace",
				model.IndexSpec{Name: "sessions_inbox_cursor_uniq", Columns: []string{model.ColTenantID, colWorkWorkspaceID, colCommReaderKind, colCommReaderRef, colCommMailboxKind, colCommMailboxRef, colCommFilterHash}, Unique: true},
			),
		},
		{
			Kind: inboxCursorBarrierKind, Table: inboxCursorBarrierTable, RetainOnTenantDrop: true,
			WorkspaceLineage: hiddenWorkspaceLineage,
			Fields: communicationFields(
				model.FieldSpec{Name: colCommReaderKind, Kind: model.KindText, Principal: pdeclNoneCommRecipientKind},
				model.FieldSpec{Name: colCommReaderRef, Kind: model.KindText, Principal: pdeclReaderRef},
				model.FieldSpec{Name: colCommMailboxKind, Kind: model.KindText, Principal: pdeclNoneMailboxKind},
				model.FieldSpec{Name: colCommMailboxRef, Kind: model.KindText, Principal: pdeclMailboxRef},
				model.FieldSpec{Name: colCommFilterHash, Kind: model.KindBytes, Principal: pdeclNoneCursorFilterHash},
				model.FieldSpec{Name: colCommDeliveryID, Kind: model.KindUUID, Principal: pdeclNoneDeliveryID},
				model.FieldSpec{Name: colCommBarrierSeq, Kind: model.KindInt},
				model.FieldSpec{Name: colCommCause, Kind: model.KindText, Principal: model.None("a cursor barrier cause, a closed set: communication_state.go:163-165, communication_state.go:6887")},
				model.FieldSpec{Name: colCommState, Kind: model.KindText, Principal: model.None("a cursor barrier state, a closed set: communication_state.go:167-169, communication_state.go:6887")},
				model.FieldSpec{Name: colCommResolvedAt, Kind: model.KindTimestamp, Nullable: true},
				model.FieldSpec{Name: colCommReasonCode, Kind: model.KindText, Principal: model.None("a bounded barrier reason code: communication_state.go:6887")},
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
					{Name: colCommDeliveryID, Kind: model.KindUUID, Principal: pdeclNoneDeliveryID},
					{Name: colCommAckKind, Kind: model.KindText, Principal: model.None("an acknowledgement kind, a closed set of one: communication_state.go:171, communication_state.go:2187")},
					{Name: colCommActorKind, Kind: model.KindText, Principal: pdeclNoneCommActorKind},
					{Name: colCommActorRef, Kind: model.KindText, Principal: pdeclCommActorRef},
					{Name: colCommOnBehalfOfKind, Kind: model.KindText, Nullable: true, Principal: pdeclNoneCommRecipientKind},
					{Name: colCommOnBehalfOfRef, Kind: model.KindText, Nullable: true, Principal: model.KindRef(colCommOnBehalfOfKind, model.ClassEvidence)},
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
			WorkspaceLineage: hiddenWorkspaceLineage, Internal: true,
			Fields: communicationFields(
				model.FieldSpec{Name: colCommGuardKind, Kind: model.KindText, Principal: model.None("a communication guard kind, a closed set: communication_state.go:173-175, communication_state.go:2209")},
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
					{Name: colCommMessageID, Kind: model.KindUUID, Principal: pdeclNoneMessageID},
					{Name: colWorkItemID, Kind: model.KindUUID, Principal: pdeclNoneWorkItemID},
					{Name: colCommDecisionKey, Kind: model.KindText, Principal: model.None("a decision key, a bounded vocabulary token: communication_state.go:2803")},
					{Name: colCommRequesterKind, Kind: model.KindText, Principal: pdeclNoneCommActorKind},
					{Name: colCommRequesterRef, Kind: model.KindText, Principal: model.KindRef(colCommRequesterKind, model.ClassObligation)},
					{Name: colCommOwnerKind, Kind: model.KindText, Principal: pdeclNoneCommSubjectKind},
					{Name: colCommOwnerRef, Kind: model.KindText, Principal: model.KindRef(colCommOwnerKind, model.ClassAuthority)},
					{Name: colCommAcceptedDeliveryID, Kind: model.KindUUID, Nullable: true, Principal: model.None("the id of the delivery through which the request was accepted: communication_state.go:2813-2814")},
					{Name: colCommState, Kind: model.KindText, Principal: pdeclNoneDecisionRequestState},
				},
				protectedPayloadFields("request", false),
				[]model.FieldSpec{
					{Name: colCommAuthorityRequirement, Kind: model.KindText, Principal: model.None("a bounded authority requirement token: communication_state.go:2807")},
					{Name: colCommDueAt, Kind: model.KindTimestamp},
					{Name: colCommAcceptedAt, Kind: model.KindTimestamp, Nullable: true},
					{Name: colCommBlockedCode, Kind: model.KindText, Nullable: true, Principal: model.None("a bounded block code: communication_state.go:2833")},
					{Name: colCommTerminalCode, Kind: model.KindText, Nullable: true, Principal: model.None("a bounded terminal code: communication_state.go:2838, communication_state.go:2843")},
					{Name: colCommResolvedDecisionID, Kind: model.KindUUID, Nullable: true, Principal: model.None("the id of the work decision that resolved the request: communication_state.go:2839")},
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
					{Name: colCommRequestID, Kind: model.KindUUID, Principal: model.None("the id of the decision request the response belongs to: communication_state.go:3061")},
					{Name: colCommResponseSeq, Kind: model.KindInt},
					{Name: colCommFromState, Kind: model.KindText, Principal: pdeclNoneDecisionRequestState},
					{Name: colCommToState, Kind: model.KindText, Principal: pdeclNoneDecisionRequestState},
					{Name: colCommActorKind, Kind: model.KindText, Principal: pdeclNoneCommActorKind},
					{Name: colCommActorRef, Kind: model.KindText, Principal: pdeclCommActorRef},
				},
				protectedPayloadFields("response", false),
				[]model.FieldSpec{
					{Name: colCommAcceptedDeliveryID, Kind: model.KindUUID, Nullable: true, Principal: model.None("the id of the delivery through which the request was accepted: communication_state.go:3063-3064")},
					{Name: colCommBlockerWorkItemID, Kind: model.KindUUID, Nullable: true, Principal: model.None("the id of the work item a blocking response points to: communication_state.go:3082-3083")},
					{Name: colCommWorkDecisionID, Kind: model.KindUUID, Nullable: true, Principal: model.None("the id of the work decision a resolving response produced: communication_state.go:3080-3081")},
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
					{Name: colWorkItemID, Kind: model.KindUUID, Principal: pdeclNoneWorkItemID},
					{Name: colCommMessageID, Kind: model.KindUUID, Principal: pdeclNoneMessageID},
					{Name: colCommDeliveryID, Kind: model.KindUUID, Principal: pdeclNoneDeliveryID},
					{Name: colCommFromKind, Kind: model.KindText, Principal: pdeclNoneCommRecipientKind},
					{Name: colCommFromRef, Kind: model.KindText, Principal: model.KindRef(colCommFromKind, model.ClassObligation)},
					{Name: colCommFromOwnerEpoch, Kind: model.KindInt},
					{Name: colCommToKind, Kind: model.KindText, Principal: pdeclNoneCommRecipientKind},
					{Name: colCommToRef, Kind: model.KindText, Principal: model.KindRef(colCommToKind, model.ClassObligation)},
					{Name: colCommOfferedLeaseFence, Kind: model.KindInt, Nullable: true},
					{Name: colCommContextEventSeq, Kind: model.KindInt},
					{Name: colCommContextHash, Kind: model.KindBytes, Principal: model.None("the SHA-256 of the canonical handoff context: communication_state.go:3187, communication_state.go:3192-3194")},
				},
				protectedPayloadFields("handoff", false),
				[]model.FieldSpec{
					{Name: colCommState, Kind: model.KindText, Principal: model.None("a handoff state, a closed set: communication_state.go:182-184, communication_state.go:3189")},
					{Name: colCommAckDeadline, Kind: model.KindTimestamp},
					{Name: colCommAckID, Kind: model.KindUUID, Nullable: true, Principal: model.None("the id of the acknowledgement that accepted the handoff: communication_state.go:3217")},
					{Name: colCommAcceptedAt, Kind: model.KindTimestamp, Nullable: true},
					{Name: colCommRejectedAt, Kind: model.KindTimestamp, Nullable: true},
					{Name: colCommWithdrawnAt, Kind: model.KindTimestamp, Nullable: true},
					{Name: colCommExpiredAt, Kind: model.KindTimestamp, Nullable: true},
					{Name: colCommTerminalCode, Kind: model.KindText, Nullable: true, Principal: model.None("a bounded terminal code: communication_state.go:3230")},
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
				model.FieldSpec{Name: colCommDeliveryID, Kind: model.KindUUID, Principal: pdeclNoneDeliveryID},
				model.FieldSpec{Name: colCommRootDispatchID, Kind: model.KindUUID, Principal: model.None("the id of the first dispatch of the lineage: communication_state.go:7828, communication_state.go:7841-7846")},
				model.FieldSpec{Name: colCommPredecessorID, Kind: model.KindUUID, Nullable: true, Principal: model.None("the id of the dispatch this one succeeds: communication_state.go:7845")},
				model.FieldSpec{Name: colCommEndpointID, Kind: model.KindUUID, Principal: model.None("the id of the endpoint the dispatch targets: communication_state.go:7829")},
				model.FieldSpec{Name: colCommEndpointGeneration, Kind: model.KindInt},
				model.FieldSpec{Name: colCommRouteRuleID, Kind: model.KindUUID, Nullable: true, Principal: pdeclNoneRouteRuleID},
				model.FieldSpec{Name: colCommRouteRuleGeneration, Kind: model.KindInt, Nullable: true},
				model.FieldSpec{Name: colCommDispatchGeneration, Kind: model.KindInt},
				model.FieldSpec{Name: colCommRerouteRung, Kind: model.KindInt},
				model.FieldSpec{Name: colCommPolicyGeneration, Kind: model.KindInt},
				model.FieldSpec{Name: colCommState, Kind: model.KindText, Principal: model.None("a dispatch state, a closed set: communication_state.go:186-189, communication_state.go:7831")},
				model.FieldSpec{Name: colCommAttemptCount, Kind: model.KindInt},
				model.FieldSpec{Name: colCommNextAttemptAt, Kind: model.KindTimestamp, Nullable: true},
				model.FieldSpec{Name: colCommClaimOwner, Kind: model.KindText, Nullable: true, Principal: model.None("a dispatch worker's claim token, a bounded vocabulary token: communication_state.go:7863-7864")},
				model.FieldSpec{Name: colCommClaimUntil, Kind: model.KindTimestamp, Nullable: true},
				model.FieldSpec{Name: colCommIdempotencyKeyHash, Kind: model.KindBytes, Principal: model.None("a SHA-256 idempotency key of the dispatch: communication_state.go:7832")},
				model.FieldSpec{Name: colCommLastVerdict, Kind: model.KindText, Nullable: true, Principal: pdeclNoneVerdict},
				model.FieldSpec{Name: colCommLastCode, Kind: model.KindText, Nullable: true, Principal: model.None("a bounded attempt outcome code: communication_state.go:7885")},
				model.FieldSpec{Name: colCommResolutionDeadlineAt, Kind: model.KindTimestamp, Nullable: true},
				model.FieldSpec{Name: colCommResolutionCode, Kind: model.KindText, Nullable: true, Principal: model.None("a bounded resolution code: communication_state.go:7886")},
				model.FieldSpec{Name: colCommReconciledAttemptID, Kind: model.KindUUID, Nullable: true, Principal: model.None("the id of the attempt a reconciliation settled: communication_state.go:7796")},
				model.FieldSpec{Name: colCommReconciledEndpointID, Kind: model.KindUUID, Nullable: true, Principal: model.None("the id of the endpoint a reconciliation settled: communication_state.go:7797")},
				model.FieldSpec{Name: colCommReconciledEndpointGeneration, Kind: model.KindInt, Nullable: true},
				model.FieldSpec{Name: colCommReconciliationVerdict, Kind: model.KindText, Nullable: true, Principal: pdeclNoneVerdict},
				model.FieldSpec{Name: colCommReconciliationCode, Kind: model.KindText, Nullable: true, Principal: model.None("a bounded reconciliation code: communication_state.go:7800")},
				model.FieldSpec{Name: colCommReconciliationEvidenceRef, Kind: model.KindText, Nullable: true, Principal: model.None("an opaque reference to reconciliation evidence checked only for shape: communication_state.go:7801")},
				model.FieldSpec{Name: colCommReconciliationObservedAt, Kind: model.KindTimestamp, Nullable: true},
				model.FieldSpec{Name: colCommProviderAcceptanceHash, Kind: model.KindBytes, Nullable: true, Principal: model.None("a SHA-256 of the provider's acceptance evidence: communication_state.go:7805")},
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
				model.FieldSpec{Name: colCommDispatchID, Kind: model.KindUUID, Principal: model.None("the id of the dispatch the attempt belongs to: communication_state.go:7938")},
				model.FieldSpec{Name: colCommAttemptSeq, Kind: model.KindInt},
				model.FieldSpec{Name: colCommState, Kind: model.KindText, Principal: model.None("an attempt state, a closed set: communication_state.go:191-193, communication_state.go:7939")},
				model.FieldSpec{Name: colCommStartedAt, Kind: model.KindTimestamp},
				model.FieldSpec{Name: colCommTransmitBoundary, Kind: model.KindText, Principal: model.None("a transmit boundary, a closed set: communication_state.go:195-197, communication_state.go:7939")},
				model.FieldSpec{Name: colCommFinishedAt, Kind: model.KindTimestamp, Nullable: true},
				model.FieldSpec{Name: colCommVerdict, Kind: model.KindText, Nullable: true, Principal: pdeclNoneVerdict},
				model.FieldSpec{Name: colCommCode, Kind: model.KindText, Nullable: true, Principal: model.None("a bounded attempt outcome code: communication_state.go:7953")},
				model.FieldSpec{Name: colCommProviderReceiptHash, Kind: model.KindBytes, Nullable: true, Principal: model.None("a SHA-256 of the provider's receipt: communication_state.go:7941")},
				model.FieldSpec{Name: colCommRequestHash, Kind: model.KindBytes, Principal: model.None("a SHA-256 of the transmitted request: communication_state.go:7940")},
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
				model.FieldSpec{Name: colCommCommandID, Kind: model.KindUUID, Principal: model.None("the id of a communication command receipt: communication_state.go:2309")},
				model.FieldSpec{Name: colCommActorFingerprint, Kind: model.KindBytes, Principal: model.None("an unkeyed SHA-256 of the caller's canonical actor kind and ref, used only as an idempotency namespace key and compared for equality: communication_ack_service.go:609-616, communication_state.go:2309")},
				model.FieldSpec{Name: colCommCommandScope, Kind: model.KindText, Principal: model.None("an opaque command scope label: communication_state.go:2310")},
				model.FieldSpec{Name: colCommIdempotencyKeyHash, Kind: model.KindBytes, Principal: model.None("a SHA-256 of the caller's idempotency key: communication_state.go:2310")},
				model.FieldSpec{Name: colCommRequestDigest, Kind: model.KindBytes, Principal: model.None("a digest of the canonical request: communication_state.go:2311")},
				model.FieldSpec{Name: colCommSealKeyVersion, Kind: model.KindText, Nullable: true, Principal: model.None("a key version label chosen by the content sealer: communication_state.go:2317-2319")},
				model.FieldSpec{Name: colCommDigestKeyVersion, Kind: model.KindText, Nullable: true, Principal: model.None("a key version label chosen by the content sealer: communication_state.go:2317-2319")},
				model.FieldSpec{Name: colCommPlanHash, Kind: model.KindBytes, Principal: model.None("a SHA-256 of the command plan: communication_state.go:2311")},
				model.FieldSpec{Name: colCommResultKind, Kind: model.KindText, Principal: model.None("the entity kind of the command result: communication_state.go:2312")},
				model.FieldSpec{Name: colCommResultID, Kind: model.KindUUID, Nullable: true, Principal: model.None("the id of the command's result row: communication_state.go:2315")},
				model.FieldSpec{Name: colCommHTTPStatus, Kind: model.KindInt},
				model.FieldSpec{Name: colCommResponseProjectionJSON, Kind: model.KindJSON, Principal: pdeclCommandResponseProjection},
				model.FieldSpec{Name: colCommResponseDigest, Kind: model.KindBytes, Principal: model.None("a digest binding the closed response projection: communication_state.go:2313, communication_state.go:2369-2374")},
				model.FieldSpec{Name: colEventID, Kind: model.KindUUID, Nullable: true, Principal: model.None("the id of the work event the command appended: communication_state.go:2316")},
				model.FieldSpec{Name: colCommAuditSeq, Kind: model.KindInt},
				model.FieldSpec{Name: colCommAuditHash, Kind: model.KindBytes, Nullable: true, Principal: model.None("the hash of the audit event the command committed with: communication_state.go:2321-2322")},
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
		"sessions_channel_route_guard":                "509abdc9f8fbc3b7d22f128cd2d838fd562ace712067d1f94cfa766f2f4ffd7b",
		"sessions_channel_route_no_delete":            "753505cd2f58b28bd3ef379363be91102ec48a26ca5f89b6f402bd0182853a49",
		"sessions_channel_subscription_guard":         "4fbec18f080bc2195103b396d109a484c7dcc40d1b897091302e5fbfc2be6088",
		"sessions_channel_subscription_no_delete":     "6e8459f6e0908b81d433ec73c0ac7afae729a9b94978dd2e29cba0db90a216c1",
		"sessions_communication_command_guard":        "fdef4326ded0968658859b969e82fd7fbfed5f5ae3501d8c9c7d7eea22f8d567",
		"sessions_communication_endpoint_guard":       "2d3d3322e286178b29e193f0a65e0564b2777ffc1c7c4efccc70627660750998",
		"sessions_communication_endpoint_no_delete":   "ab32af900ae0b990f2ed83001305dde42505689be528e48ff130147aefac6cef",
		"sessions_communication_guard_guard":          "aa625e70e658c1fde98135adefd8eb2636aaf5c3c2101425beab4f5c5015f059",
		"sessions_communication_guard_no_delete":      "408165651cf18f6ac9da71c5e498173863762c18a95273c5d7b2467d6ab6d23e",
		"sessions_decision_request_guard":             "80d2f06ec67e7357537a73b3050df009ae48f8aee86a70c07c19e27d9873b848",
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
		"sessions_work_handoff_guard":                 "ddb234d47a9aaba46fc9d85d122ff8ed2d0c584b9dfffaaccc90a8a1ec4cc4b7",
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
		"sessions_work_handoff_guard_ins":               "5729768a24c1296b8cf82566dacb31cd6402b2fa7d098e84b7eccf2f8dda8ec1",
		"sessions_work_handoff_guard_upd":               "39cce761174b8660321bdd5740873031df0232a2d776375d3a78a78311df027b",
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
		// OT-V moves only the Handoff guard off the shared validator, for the
		// accepted-state lease-effect rule. Shared-function replacement is not
		// supported, so the transition names a freshly reserved identity; the other
		// EIGHTEEN triggers keep the immutable shared function and their digests.
		// Nineteen were attached at the assigned baseline, not the proposal's twenty:
		// migration 0018 had already detached the CommunicationCommand guard. The
		// invariant is the enumerated set above, never the count, but a count that
		// disagrees with the catalog is one a later reader will trust; this one was
		// captured from a real migrated database on both engines.
		if postgresGuard == "sessions_work_handoff_guard" {
			postgresTrigger.Transitions = []store.SchemaTriggerTransition{{
				MigrationVersion:         24,
				PreviousDefinitionSHA256: "516b6c0c369788e1276c201348f855b273480f927f7ddd88334398a6a7e4e840",
				PostgresFunctionIdentity: &store.SchemaTriggerFunctionIdentityTransition{
					PreviousName: "olivares_sessions_communication_validate",
					NextName:     "olivares_sessions_work_handoff_validate_v24",
				},
			}}
		}
		// Migration 25 repairs the ChannelRoute event_type and DecisionRequest
		// authority_requirement bounds: 0012 wrote both as {1,256}, above
		// PostgreSQL's regular-expression repetition limit of 255, so both guards
		// raised SQLSTATE 2201B on every row that reached them. Only these two
		// triggers move, to a reserved copy of the shared validator with those two
		// expressions rewritten; the other sixteen keep the shared function.
		if previous, ok := map[string]string{
			"sessions_channel_route_guard":    "1e39b1d5f20ce3d453af669d63f355ceed87008b9f5f598ab2a98d498d9e9efb",
			"sessions_decision_request_guard": "a70af9db9dd5244f33876693c50b0e9b87cb80f8fa998715d7a07edeec4b7cab",
		}[postgresGuard]; ok {
			postgresTrigger.Transitions = []store.SchemaTriggerTransition{{
				MigrationVersion:         25,
				PreviousDefinitionSHA256: previous,
				PostgresFunctionIdentity: &store.SchemaTriggerFunctionIdentityTransition{
					PreviousName: "olivares_sessions_communication_validate",
					NextName:     "olivares_sessions_communication_validate_v25",
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
		// SQLite has no separately addressable trigger function, so its OT-V
		// transitions carry no PostgresFunctionIdentity: 0096 replaces the INSERT
		// half and 0097 the UPDATE half of the same rule.
		if sqliteInsert == "sessions_work_handoff_guard_ins" {
			sqliteTrigger.Transitions = []store.SchemaTriggerTransition{{
				MigrationVersion:         96,
				PreviousDefinitionSHA256: "e4963f98519967bdf70719084c2aea92d4a6a8f75c4d0a44a8d22eac54890e61",
			}}
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
			sqliteUpdateTrigger := store.SchemaTrigger{
				Name: sqliteUpdate, Table: definition.table,
				DefinitionSHA256: sqliteDigests[sqliteUpdate],
			}
			if sqliteUpdate == "sessions_work_handoff_guard_upd" {
				sqliteUpdateTrigger.Transitions = []store.SchemaTriggerTransition{{
					MigrationVersion:         97,
					PreviousDefinitionSHA256: "33d8411f8dbb0ea700633e5e0ecff54e85edd8745e21ffb3b0f70c331d7830a0",
				}}
			}
			sqlite = append(sqlite,
				sqliteUpdateTrigger,
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

// Principal declarations of the communication descriptors. Every (kind, ref)
// pair here spells an account as kind "user" with a canonical account id and a
// session as an osn_ id (communication_state.go:276-327), which is the (kind,
// ref) encoding: only a "user" kind resolves to an account. A shared
// declaration cites lines that hold for every column using it.
var (
	pdeclNoneCommActorKind        = model.None("a communication actor kind, a closed set: communication_state.go:147-149, communication_state.go:308-311")
	pdeclNoneCommSubjectKind      = model.None("a communication subject kind, a closed set: communication_state.go:74-76, communication_state.go:276-279")
	pdeclNoneCommRecipientKind    = model.None("a recipient kind, a closed set: communication_state.go:145, communication_state.go:292-295")
	pdeclNoneSelectorKind         = model.None("an audience selector kind, a closed set: communication_state.go:140-143, communication_state.go:329-331")
	pdeclNoneWakePolicy           = model.None("a wake policy, a closed set: communication_state.go:90")
	pdeclNoneAckPolicy            = model.None("an acknowledgement policy, a closed set: communication_state.go:136-138")
	pdeclNoneUrgency              = model.None("a message urgency, a closed set: communication_state.go:134")
	pdeclNoneMessageKind          = model.None("a message kind, a closed set: communication_state.go:125-128")
	pdeclNoneMailboxKind          = model.None("a mailbox kind, a closed set: communication_state.go:161, communication_state.go:6853")
	pdeclNoneVerdict              = model.None("an assessment verdict, a closed set: work_model.go:33-35, communication_state.go:215-217")
	pdeclNoneCursorFilterHash     = model.None("the SHA-256 of an inbox cursor's canonical filter: communication_state.go:6854, communication_state.go:6885")
	pdeclNoneChannelID            = model.None("the id of the Channel the row belongs to, never an account: communication_state.go:1297, communication_state.go:1321, communication_state.go:1343, communication_state.go:1548, communication_protocol_interrupt.go:830")
	pdeclNoneMessageID            = model.None("the id of a Message, never an account: communication_state.go:1953, communication_state.go:2155, communication_state.go:2801, communication_state.go:3183, communication_binding_service.go:584")
	pdeclNoneDeliveryID           = model.None("the id of a message delivery, never an account: communication_state.go:2187, communication_state.go:3184, communication_state.go:6886, communication_state.go:7827, communication_binding_service.go:585")
	pdeclNoneRouteRuleID          = model.None("the id of a channel route rule, never an account: communication_state.go:2156, communication_state.go:4145-4148, communication_state.go:7721")
	pdeclNoneDecisionRequestState = model.None("a decision request state, a closed set: communication_state.go:177-180, communication_state.go:2804, communication_state.go:3062-3063")

	pdeclSelectorRef  = model.KindRef(colCommSelectorKind, model.ClassEvidence)
	pdeclReaderRef    = model.KindRef(colCommReaderKind, model.ClassEvidence)
	pdeclCommActorRef = model.KindRef(colCommActorKind, model.ClassEvidence)
	// pdeclRecipientRef makes the recipient a required party of the delivery
	// it names: it must see and, where the policy asks, acknowledge it. A publish
	// fans out to current directory members under the directory fact it already
	// locks (communication_publish.go:453), and a reader must still be one
	// (communication_ack_service.go:429), so the reference is member-bound.
	pdeclRecipientRef = model.KindRef(colCommRecipientKind, model.ClassObligation).BoundToMembers()

	// pdeclCausalRef is the ref of an audience arc's cause: the recipient itself
	// for a direct arc, the original subscriber for a subscriber arc, a group id
	// for a group arc or the workspace id for a workspace-member arc
	// (communication_state.go:4058-4100). The causal kind never says "user", so
	// no (kind, ref) pair fixes where an account id sits; it is matched against
	// every alias as evidence.
	pdeclCausalRef = model.Scan(model.ClassEvidence)
	// pdeclMailboxRef is an inbox cursor's mailbox: the reader's own ref for a
	// personal mailbox, which is an account id when the reader is a user, or a
	// Channel id (communication_state.go:6857-6858). The mailbox kind never
	// says "user", so it is matched against every alias as evidence.
	pdeclMailboxRef = model.Scan(model.ClassEvidence)

	// pdeclRouteReasons is the route reason list the delivery and audience
	// writers marshal as a JSON array of strings (communication_model.go:607,
	// communication_model.go:635).
	pdeclRouteReasons = model.Nested([]RouteReason{}, model.ClassEvidence,
		model.Leaf("[]", model.None("a route reason, a bounded vocabulary token: communication_state.go:4190-4201")),
	)
	// pdeclLabelMap is a canonical label map of bounded tokens, the message
	// labels or a route's label matcher (communication_state.go:1423-1441).
	pdeclLabelMap = model.Nested(map[string]string{}, model.ClassEvidence,
		model.Leaf("{key}", model.None("a label key, a bounded vocabulary token: communication_state.go:1434-1437")),
		model.Leaf("{}", model.None("a label value, a bounded vocabulary token: communication_state.go:1434-1437")),
	)
	// pdeclLabelVocabulary is a label definition's allowed values, a sorted JSON
	// array of bounded tokens (communication_state.go:1350-1367).
	pdeclLabelVocabulary = model.Nested([]string{}, model.ClassEvidence,
		model.Leaf("[]", model.None("a label vocabulary value, a bounded vocabulary token: communication_state.go:1350-1358")),
	)
	// pdeclCommandResponseProjection is the closed receipt projection, which
	// has no field for human content or a principal (communication_model.go:811-821).
	pdeclCommandResponseProjection = model.Nested(CommunicationCommandResponseProjection{}, model.ClassEvidence,
		model.Leaf("ids{key}", model.None("a projection id name from a closed set: communication_state.go:2330-2334, communication_state.go:2343-2344")),
		model.Leaf("ids{}", model.None("the canonical id of a communication row the command produced: communication_state.go:2343-2344")),
		model.Leaf("state", model.None("a resulting entity state from a closed set: communication_state.go:2255-2263, communication_state.go:2326-2327")),
		model.Leaf("counts{key}", model.None("a count name from a closed set: communication_state.go:2335-2338, communication_state.go:2348-2349")),
		model.Leaf("digests{key}", model.None("a digest name from a closed set: communication_state.go:2339-2342, communication_state.go:2353-2354")),
		model.Leaf("digests{}", model.None("a SHA-256 digest: communication_state.go:2353-2354")),
		model.Leaf("inbox_cursor.barrier_delivery_id", model.None("the id of the delivery a cursor barrier waits on: communication_state.go:2296-2297")),
		model.Leaf("inbox_cursor.barrier_reason", model.None("a cursor barrier cause, a closed set: communication_state.go:163-165, communication_state.go:2298")),
	)

	pdeclNonePayloadEncoding   = model.None("a protected-payload encoding, a closed set: communication_state.go:117, communication_state.go:888")
	pdeclNonePayloadSchema     = model.None("the schema name of a protected-payload slot, a closed set: communication_state.go:964-983, communication_state.go:1122-1123")
	pdeclNonePayloadDigest     = model.None("a digest of the protected content, compared and never resolved: communication_state.go:888-889, communication_state.go:902-904")
	pdeclNonePayloadKeyVersion = model.None("a key version label chosen by the content sealer: communication_state.go:909-910")
	// pdeclPayloadSealed is the sealed envelope the payload writer marshals
	// (communication_codec.go:449) and the reader decodes
	// (communication_codec.go:494-499).
	pdeclPayloadSealed = model.Nested(SealedPayload{}, model.ClassEvidence,
		model.Leaf("ciphertext", model.None("ciphertext only the content sealer opens: communication_state.go:906-912, communication_model.go:1082-1087")),
		model.Leaf("key_version", pdeclNonePayloadKeyVersion),
	)
	// pdeclContentReference classifies every content reference inside a
	// payload: an opaque kind, ref and hash checked only for shape and returned
	// to authorized readers; no reader resolves one to an account.
	pdeclContentReference = model.TypeLeaves(ContentReference{},
		model.Leaf("kind", model.None("an opaque content reference kind, checked only for shape: communication_state.go:985-991, communication_state.go:1522-1524")),
		model.Leaf("ref", model.None("an opaque content reference, checked only for shape: communication_state.go:985-991, communication_state.go:1522-1524")),
		model.Leaf("hash", model.None("an optional opaque content hash, checked only for shape: communication_state.go:985-991, communication_state.go:1522-1524")),
	)
	pdeclNoneContentProse = model.None("human prose of a protected payload, bounded and returned only to authorized readers: communication_state.go:993-1005, communication_state.go:1026-1066, communication_state.go:1503-1542")
	pdeclNoneContentToken = model.None("a bounded vocabulary token of a protected payload: communication_state.go:994, communication_state.go:1034, communication_state.go:1045, communication_state.go:1529")
	pdeclReasonContent    = model.Nested(CommunicationReasonContent{}, model.ClassEvidence,
		model.Leaf("code", pdeclNoneContentToken),
		model.Leaf("text", pdeclNoneContentProse),
		pdeclContentReference,
	)
	// pdeclPayloadPlain is the plain JSON of each protected-payload slot, keyed
	// by the column prefix protectedPayloadFields is called with; each is the
	// slot content type the validator decodes (communication_state.go:1073-1111).
	// A prefix missing here leaves its column undeclared, which the census
	// reports.
	pdeclPayloadPlain = map[string]*model.ColumnDecl{
		"payload": model.Nested(MessageContent{}, model.ClassEvidence,
			model.Leaf("subject", pdeclNoneContentProse),
			model.Leaf("blocks[].type", model.None("a content block type, a closed set: communication_state.go:119-121, communication_state.go:1510")),
			model.Leaf("blocks[].format", model.None("a text format, a closed set: communication_state.go:123, communication_state.go:1515")),
			model.Leaf("blocks[].text", pdeclNoneContentProse),
			model.Leaf("blocks[].code", pdeclNoneContentToken),
			pdeclContentReference,
		),
		"terminal_reason": pdeclReasonContent,
		"note":            pdeclReasonContent,
		"request": model.Nested(DecisionRequestContent{}, model.ClassEvidence,
			model.Leaf("question", pdeclNoneContentProse),
			model.Leaf("choices[].key", pdeclNoneContentToken),
			model.Leaf("choices[].label", pdeclNoneContentProse),
		),
		"response": model.Nested(DecisionResponseContent{}, model.ClassEvidence,
			model.Leaf("choice_key", pdeclNoneContentToken),
			model.Leaf("reason.code", pdeclNoneContentToken),
			model.Leaf("reason.text", pdeclNoneContentProse),
			pdeclContentReference,
		),
		"handoff": model.Nested(HandoffContent{}, model.ClassEvidence,
			model.Leaf("summary", pdeclNoneContentProse),
			model.Leaf("next_action", pdeclNoneContentProse),
			model.Leaf("risk", pdeclNoneContentProse),
			model.Leaf("branch", model.None("a git branch name the sender wrote, checked only for shape and never resolved to an account: communication_state.go:1079")),
			model.Leaf("sha", model.None("a full git object id the sender wrote, checked only for shape and never resolved to an account: communication_state.go:1080")),
			pdeclContentReference,
		),
	}
)
