// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

const (
	protocolBindingSpecKind model.Kind = "sessions.communication_binding_spec"
	protocolBindingKind     model.Kind = "sessions.communication_binding"

	protocolBindingSpecTable = "sessions_communication_binding_spec"
	protocolBindingTable     = "sessions_communication_binding"
)

const (
	colBindingKey                  = "binding_key"
	colBindingProtocol             = "protocol"
	colBindingProtocolVersion      = "protocol_version"
	colBindingDirection            = "direction"
	colBindingLocalKind            = "local_kind"
	colBindingLocalSelectorJSON    = "local_selector_json"
	colBindingPeerAuthority        = "peer_authority"
	colBindingRemoteResourceKind   = "remote_resource_kind"
	colBindingRemoteResourceRef    = "remote_resource_ref"
	colBindingMappingSchema        = "mapping_schema"
	colBindingMappingJSON          = "mapping_json"
	colBindingMappingHash          = "mapping_hash"
	colBindingKnownLossesJSON      = "known_losses_json"
	colBindingLossesHash           = "losses_hash"
	colBindingRuleRefsJSON         = "rule_refs_json"
	colBindingPermissionProfileRef = "permission_profile_ref"
	colBindingCurrencyPolicy       = "currency_policy"
	colBindingValidationVerdict    = "validation_verdict"
	colBindingValidationCode       = "validation_code"
	colBindingValidatedAt          = "validated_at"
	colBindingState                = "state"
	colBindingActiveSlot           = "active_slot"
	colBindingSpecHash             = "spec_hash"
	colBindingPlanHash             = "plan_hash"
	colBindingCommandKeyHash       = "command_key_hash"
	colBindingRequestHash          = "request_hash"
)

const (
	colBindingSpecID             = "binding_spec_id"
	colBindingSpecGeneration     = "binding_spec_generation"
	colBindingPinnedSpecHash     = "pinned_spec_hash"
	colBindingPinnedMappingHash  = "pinned_mapping_hash"
	colBindingPinnedLossesHash   = "pinned_losses_hash"
	colBindingAttemptID          = "attempt_id"
	colBindingDispatchKeyHash    = "dispatch_key_hash"
	colBindingReservationHash    = "reservation_hash"
	colBindingSyntheticSID       = "synthetic_sid"
	colBindingOwnerEpoch         = "owner_epoch"
	colBindingLeaseFence         = "lease_fence"
	colBindingOwnerDigest        = "owner_digest"
	colBindingExternalKind       = "external_kind"
	colBindingExternalID         = "external_id"
	colBindingExternalMessageID  = "external_message_id"
	colBindingContextID          = "context_id"
	colBindingLocalState         = "local_state"
	colBindingRemoteState        = "remote_state"
	colBindingRemoteRevision     = "remote_revision"
	colBindingObservationVerdict = "observation_verdict"
	colBindingObservationCode    = "observation_code"
	colBindingLastObservedAt     = "last_observed_at"
	colBindingDetailHash         = "detail_hash"
	colBindingCurrentTTLMs       = "current_ttl_ms"
	colBindingCurrentPollMs      = "current_poll_interval_ms"
	colBindingTerminal           = "terminal"
	colBindingExternalActiveSlot = "external_active_slot"
	colBindingLastUpdateHash     = "last_update_hash"
	colBindingCancelRequested    = "cancel_requested"
	colBindingCancelRequestedAt  = "cancel_requested_at"
	colBindingCancelReasonCode   = "cancel_reason_code"
	colBindingCancelKeyHash      = "cancel_key_hash"
	colBindingMCPTaskJSON        = "mcp_task_json"
	colBindingMCPTaskHash        = "mcp_task_hash"
	colBindingLastCommandID      = "last_command_id"
	colBindingLastEventID        = "last_event_id"
	colBindingLastEventSeq       = "last_event_seq"
)

// registerProtocolBindingSchema is an additive K5 expansion. The descriptors
// create two new tables; no prior migration or K3 table is rewritten.
func (m *Module) registerProtocolBindingSchema(reg store.ExtensionRegistry) error {
	descriptors := []model.EntityDescriptor{
		{
			Kind: protocolBindingSpecKind, Table: protocolBindingSpecTable,
			RetainOnTenantDrop: true, WorkspaceLineage: hiddenWorkspaceLineage,
			Fields: communicationFields(
				model.FieldSpec{Name: colBindingKey, Kind: model.KindText, Principal: model.None("a binding key, a bounded vocabulary token: communication_binding_codec.go:169")},
				model.FieldSpec{Name: colCommGeneration, Kind: model.KindInt},
				model.FieldSpec{Name: colBindingProtocol, Kind: model.KindText, Principal: pdeclNoneBindingProtocol},
				model.FieldSpec{Name: colBindingProtocolVersion, Kind: model.KindText, Principal: model.None("a pinned protocol version, an opaque ref that may not float: communication_binding_codec.go:171-173")},
				model.FieldSpec{Name: colBindingDirection, Kind: model.KindText, Principal: pdeclNoneBindingDirection},
				model.FieldSpec{Name: colBindingLocalKind, Kind: model.KindText, Principal: model.None("a local resource kind, a closed set with no account kind: communication_binding_codec.go:49-52, communication_binding_codec.go:170")},
				model.FieldSpec{Name: colBindingLocalSelectorJSON, Kind: model.KindJSON, Principal: model.None("a canonical JSON object selecting the local work item, agent, model or channel by id, never an account: communication_binding_codec.go:129-138, communication_binding_local_resource.go:52-73")},
				model.FieldSpec{Name: colBindingPeerAuthority, Kind: model.KindText, Principal: pdeclNonePeerAuthority},
				model.FieldSpec{Name: colBindingRemoteResourceKind, Kind: model.KindText, Principal: model.None("a remote resource kind, a bounded vocabulary token: communication_binding_codec.go:174")},
				model.FieldSpec{Name: colBindingRemoteResourceRef, Kind: model.KindText, Principal: pdeclNoneRemoteResourceRef},
				model.FieldSpec{Name: colBindingMappingSchema, Kind: model.KindText, Principal: model.None("an opaque mapping schema name, checked only for shape: communication_binding_codec.go:175-176")},
				model.FieldSpec{Name: colBindingMappingJSON, Kind: model.KindJSON, Principal: pdeclBindingMapping},
				model.FieldSpec{Name: colBindingMappingHash, Kind: model.KindBytes, Principal: model.None("the SHA-256 of the canonical mapping: communication_binding_codec.go:268-282")},
				model.FieldSpec{Name: colBindingKnownLossesJSON, Kind: model.KindJSON, Principal: pdeclBindingLosses},
				model.FieldSpec{Name: colBindingLossesHash, Kind: model.KindBytes, Principal: model.None("the SHA-256 of the canonical known losses: communication_binding_codec.go:268-286")},
				model.FieldSpec{Name: colBindingRuleRefsJSON, Kind: model.KindJSON, Principal: pdeclBindingRuleRefs},
				model.FieldSpec{Name: colBindingPermissionProfileRef, Kind: model.KindText, Principal: model.None("an opaque permission profile reference, checked only for shape: communication_binding_codec.go:177")},
				model.FieldSpec{Name: colBindingCurrencyPolicy, Kind: model.KindText, Principal: model.None("a currency policy, a closed set of one: communication_binding_codec.go:177")},
				model.FieldSpec{Name: colBindingValidationVerdict, Kind: model.KindText, Principal: pdeclNoneObservationVerdict},
				model.FieldSpec{Name: colBindingValidationCode, Kind: model.KindText, Principal: model.None("a bounded validation code: communication_binding_codec.go:178")},
				model.FieldSpec{Name: colBindingValidatedAt, Kind: model.KindTimestamp, Nullable: true},
				model.FieldSpec{Name: colBindingState, Kind: model.KindText, Principal: model.None("a binding spec state, a closed set: communication_binding_codec.go:54-57")},
				model.FieldSpec{Name: colCommSupersedesID, Kind: model.KindUUID, Nullable: true, Principal: model.None("the id of the spec generation this one supersedes: communication_binding_codec.go:256")},
				model.FieldSpec{Name: colBindingActiveSlot, Kind: model.KindText, Nullable: true, Principal: model.None("the active-generation slot, the binding key while the generation is active: communication_binding_codec.go:351-355")},
				model.FieldSpec{Name: colBindingSpecHash, Kind: model.KindBytes, Principal: model.None("the SHA-256 of the canonical spec: communication_binding_codec.go:277-312, communication_binding_codec.go:356")},
				model.FieldSpec{Name: colBindingPlanHash, Kind: model.KindBytes, Principal: model.None("the hash of the spec command plan: communication_binding_codec.go:357")},
				model.FieldSpec{Name: colBindingCommandKeyHash, Kind: model.KindBytes, Principal: model.None("a digest of the spec command's idempotency key: communication_binding_codec.go:358")},
				model.FieldSpec{Name: colBindingRequestHash, Kind: model.KindBytes, Principal: model.None("a digest of the spec command request: communication_binding_codec.go:359")},
			),
			Indexes: communicationIndexes("sessions_communication_binding_spec_workspace",
				model.IndexSpec{Name: "sessions_communication_binding_spec_generation_uniq", Columns: []string{model.ColTenantID, colWorkWorkspaceID, colBindingKey, colCommGeneration}, Unique: true},
				model.IndexSpec{Name: "sessions_communication_binding_spec_active_uniq", Columns: []string{model.ColTenantID, colWorkWorkspaceID, colBindingActiveSlot}, Unique: true},
				model.IndexSpec{Name: "sessions_communication_binding_spec_command_uniq", Columns: []string{model.ColTenantID, colWorkWorkspaceID, colBindingCommandKeyHash}, Unique: true},
				model.IndexSpec{Name: "sessions_communication_binding_spec_supersedes_uniq", Columns: []string{model.ColTenantID, colCommSupersedesID}, Unique: true},
				model.IndexSpec{Name: "sessions_communication_binding_spec_protocol", Columns: []string{model.ColTenantID, colWorkWorkspaceID, colBindingProtocol, colBindingState, model.ColID}},
				model.IndexSpec{Name: "sessions_communication_binding_spec_peer", Columns: []string{model.ColTenantID, colWorkWorkspaceID, colBindingPeerAuthority, colBindingRemoteResourceKind, colBindingRemoteResourceRef, model.ColID}},
				model.IndexSpec{Name: "sessions_communication_binding_spec_local", Columns: []string{model.ColTenantID, colWorkWorkspaceID, colBindingLocalKind, colBindingState, model.ColID}},
			),
		},
		{
			Kind: protocolBindingKind, Table: protocolBindingTable,
			RetainOnTenantDrop: true, WorkspaceLineage: hiddenWorkspaceLineage,
			Fields: communicationFields(
				model.FieldSpec{Name: colBindingSpecID, Kind: model.KindUUID, Principal: model.None("the id of the binding spec generation the binding pins: communication_binding_codec.go:428, communication_binding_service.go:581")},
				model.FieldSpec{Name: colBindingSpecGeneration, Kind: model.KindInt},
				model.FieldSpec{Name: colBindingPinnedSpecHash, Kind: model.KindBytes, Principal: pdeclNonePinnedSpecHash},
				model.FieldSpec{Name: colBindingPinnedMappingHash, Kind: model.KindBytes, Principal: pdeclNonePinnedSpecHash},
				model.FieldSpec{Name: colBindingPinnedLossesHash, Kind: model.KindBytes, Principal: pdeclNonePinnedSpecHash},
				model.FieldSpec{Name: colCommMessageID, Kind: model.KindUUID, Nullable: true, Principal: pdeclNoneMessageID},
				model.FieldSpec{Name: colCommDeliveryID, Kind: model.KindUUID, Nullable: true, Principal: pdeclNoneDeliveryID},
				model.FieldSpec{Name: colWorkItemID, Kind: model.KindUUID, Nullable: true, Principal: pdeclNoneWorkItemID},
				model.FieldSpec{Name: colBindingProtocol, Kind: model.KindText, Principal: pdeclNoneBindingProtocol},
				model.FieldSpec{Name: colBindingProtocolVersion, Kind: model.KindText, Principal: model.None("the pinned protocol version copied from the spec: communication_binding_codec.go:171-173, communication_binding_codec.go:437")},
				model.FieldSpec{Name: colBindingDirection, Kind: model.KindText, Principal: pdeclNoneBindingDirection},
				model.FieldSpec{Name: colBindingPeerAuthority, Kind: model.KindText, Principal: pdeclNonePeerAuthority},
				model.FieldSpec{Name: colBindingRemoteResourceRef, Kind: model.KindText, Principal: pdeclNoneRemoteResourceRef},
				model.FieldSpec{Name: colBindingAttemptID, Kind: model.KindUUID, Principal: model.None("the id of the attempt the binding records: communication_binding_service.go:586, communication_binding_codec.go:441")},
				model.FieldSpec{Name: colBindingDispatchKeyHash, Kind: model.KindBytes, Principal: model.None("a SHA-256 of the binding's dispatch key: communication_binding_service.go:57, communication_binding_codec.go:442")},
				model.FieldSpec{Name: colBindingReservationHash, Kind: model.KindBytes, Principal: model.None("a digest of the reservation request: communication_binding_service.go:602-606, communication_binding_codec.go:443")},
				model.FieldSpec{Name: colCommGeneration, Kind: model.KindInt},
				model.FieldSpec{Name: colBindingSyntheticSID, Kind: model.KindText, Principal: pdeclNoneSID},
				model.FieldSpec{Name: colCommOwnerKind, Kind: model.KindText, Nullable: true, Principal: model.None("a copy of the work item owner kind, a closed set: work_state.go:29, communication_binding_service.go:993-995")},
				model.FieldSpec{Name: colCommOwnerRef, Kind: model.KindText, Nullable: true, Principal: pdeclBindingOwnerRef},
				model.FieldSpec{Name: colBindingOwnerDigest, Kind: model.KindBytes, Nullable: true, Principal: model.None("a SHA-256 commitment to the MCP task owner tuple, compared byte for byte: communication_binding_service.go:594, cmd/olivares/mcpdurabletasks.go:1420-1432, cmd/olivares/mcpdurabletasks.go:1441")},
				model.FieldSpec{Name: colBindingOwnerEpoch, Kind: model.KindInt},
				model.FieldSpec{Name: colBindingLeaseFence, Kind: model.KindInt},
				model.FieldSpec{Name: colBindingExternalKind, Kind: model.KindText, Principal: model.None("a protocol result kind, a closed set: communication_binding_codec.go:69-75")},
				model.FieldSpec{Name: colBindingExternalID, Kind: model.KindText, Nullable: true, Principal: pdeclNoneProtocolExternalRef},
				model.FieldSpec{Name: colBindingContextID, Kind: model.KindText, Nullable: true, Principal: pdeclNoneProtocolExternalRef},
				model.FieldSpec{Name: colBindingExternalMessageID, Kind: model.KindText, Nullable: true, Principal: pdeclNoneProtocolExternalRef},
				model.FieldSpec{Name: colBindingLocalState, Kind: model.KindText, Principal: model.None("a local work state token, checked only for shape and mapped to a work state: communication_binding_service.go:709-710, communication_binding_service.go:1352-1361")},
				model.FieldSpec{Name: colBindingRemoteState, Kind: model.KindText, Principal: model.None("an opaque remote state token from the peer, checked only for shape: communication_binding_service.go:710")},
				model.FieldSpec{Name: colBindingRemoteRevision, Kind: model.KindText, Nullable: true, Principal: model.None("an opaque remote revision from the peer, checked only for shape: communication_binding_service.go:711")},
				model.FieldSpec{Name: colBindingObservationVerdict, Kind: model.KindText, Principal: pdeclNoneObservationVerdict},
				model.FieldSpec{Name: colBindingObservationCode, Kind: model.KindText, Principal: model.None("a bounded observation code: communication_binding_service.go:757-767")},
				model.FieldSpec{Name: colBindingLastObservedAt, Kind: model.KindTimestamp, Nullable: true},
				model.FieldSpec{Name: colBindingDetailHash, Kind: model.KindBytes, Nullable: true, Principal: model.None("a SHA-256 of the observation detail: communication_binding_service.go:712")},
				model.FieldSpec{Name: colBindingCurrentTTLMs, Kind: model.KindInt, Nullable: true},
				model.FieldSpec{Name: colBindingCurrentPollMs, Kind: model.KindInt, Nullable: true},
				model.FieldSpec{Name: colBindingTerminal, Kind: model.KindBool},
				model.FieldSpec{Name: colBindingExternalActiveSlot, Kind: model.KindText, Nullable: true, Principal: model.None("the non-terminal external id slot, the external id while the binding is open: communication_binding_codec.go:419-424")},
				model.FieldSpec{Name: colBindingLastUpdateHash, Kind: model.KindBytes, Nullable: true, Principal: model.None("a digest of the last settlement or observation: communication_binding_service.go:724, communication_binding_service.go:753")},
				model.FieldSpec{Name: colBindingCancelRequested, Kind: model.KindBool},
				model.FieldSpec{Name: colBindingCancelRequestedAt, Kind: model.KindTimestamp, Nullable: true},
				model.FieldSpec{Name: colBindingCancelReasonCode, Kind: model.KindText, Nullable: true, Principal: model.None("a bounded cancel reason code: communication_binding_service.go:779-782")},
				model.FieldSpec{Name: colBindingCancelKeyHash, Kind: model.KindBytes, Nullable: true, Principal: model.None("a digest of the cancel intent: communication_binding_service.go:785, communication_binding_service.go:413")},
				model.FieldSpec{Name: colBindingMCPTaskJSON, Kind: model.KindJSON, Nullable: true, Principal: pdeclBindingMCPTask},
				model.FieldSpec{Name: colBindingMCPTaskHash, Kind: model.KindBytes, Nullable: true, Principal: model.None("a SHA-256 of the canonical MCP task projection, compared on read: communication_binding_codec.go:527")},
				model.FieldSpec{Name: colBindingLastCommandID, Kind: model.KindUUID, Principal: model.None("the id of the last command that changed the binding, or of the command of a workflow Message on its WorkItem that advanced it: communication_binding_codec.go:478")},
				model.FieldSpec{Name: colBindingLastEventID, Kind: model.KindUUID, Principal: model.None("the id of the last work event the binding appended, or of a workflow Message's event on its WorkItem that advanced it: communication_binding_codec.go:479")},
				model.FieldSpec{Name: colBindingLastEventSeq, Kind: model.KindInt},
			),
			Indexes: communicationIndexes("sessions_communication_binding_workspace",
				model.IndexSpec{Name: "sessions_communication_binding_attempt_uniq", Columns: []string{model.ColTenantID, colBindingAttemptID}, Unique: true},
				model.IndexSpec{Name: "sessions_communication_binding_dispatch_uniq", Columns: []string{model.ColTenantID, colWorkWorkspaceID, colBindingDispatchKeyHash}, Unique: true},
				model.IndexSpec{Name: "sessions_communication_binding_external_uniq", Columns: []string{model.ColTenantID, colWorkWorkspaceID, colBindingProtocol, colBindingPeerAuthority, colBindingExternalKind, colBindingExternalID, colCommGeneration}, Unique: true},
				model.IndexSpec{Name: "sessions_communication_binding_external_active_uniq", Columns: []string{model.ColTenantID, colWorkWorkspaceID, colBindingProtocol, colBindingPeerAuthority, colBindingExternalKind, colBindingExternalActiveSlot}, Unique: true},
				model.IndexSpec{Name: "sessions_communication_binding_spec_ref", Columns: []string{model.ColTenantID, colBindingSpecID, colBindingSpecGeneration, model.ColID}},
				model.IndexSpec{Name: "sessions_communication_binding_work", Columns: []string{model.ColTenantID, colWorkWorkspaceID, colWorkItemID, model.ColID}},
				model.IndexSpec{Name: "sessions_communication_binding_message", Columns: []string{model.ColTenantID, colCommMessageID, model.ColID}},
				model.IndexSpec{Name: "sessions_communication_binding_delivery", Columns: []string{model.ColTenantID, colCommDeliveryID, model.ColID}},
				model.IndexSpec{Name: "sessions_communication_binding_owner", Columns: []string{model.ColTenantID, colWorkWorkspaceID, colCommOwnerKind, colCommOwnerRef, colBindingTerminal, model.ColID}},
				model.IndexSpec{Name: "sessions_communication_binding_verdict", Columns: []string{model.ColTenantID, colWorkWorkspaceID, colBindingObservationVerdict, colBindingLastObservedAt, model.ColID}},
			),
		},
	}
	for _, descriptor := range descriptors {
		if err := reg.Register(descriptor); err != nil {
			return err
		}
	}
	return nil
}

// Principal declarations of the protocol-binding descriptors. A binding spec
// is declarative configuration and a binding row records one protocol
// exchange; the principal-bearing values are the work owner copied at
// reservation and the MCP task owner of the task projection.
var (
	pdeclNoneBindingProtocol     = model.None("a binding protocol, a closed set: communication_binding_codec.go:41-43")
	pdeclNoneBindingDirection    = model.None("a binding direction, a closed set: communication_binding_codec.go:45-47")
	pdeclNonePeerAuthority       = model.None("a protocol peer authority, a URL without userinfo or an opaque ref, never an account: communication_binding_codec.go:107-127")
	pdeclNoneRemoteResourceRef   = model.None("an opaque remote resource reference on the peer, checked only for shape: communication_binding_codec.go:175")
	pdeclNoneObservationVerdict  = model.None("a protocol observation verdict, a closed set: communication_binding_codec.go:64-67")
	pdeclNonePinnedSpecHash      = model.None("a spec hash the binding pinned from the active spec generation: communication_binding_codec.go:430-432, communication_binding_codec.go:277-312")
	pdeclNoneProtocolExternalRef = model.None("an opaque protocol-side id (a task, context or message id) the peer issued, checked only for shape: communication_binding_service.go:769-773")

	// pdeclBindingMapping is the declarative mapping the spec writer marshals
	// (communication_binding_codec.go:329) and the reader decodes
	// (communication_binding_codec.go:382).
	pdeclBindingMapping = model.Nested([]ProtocolMappingRule{}, model.ClassEvidence,
		model.Leaf("[].source", model.None("a declarative source field path, checked only for shape: communication_binding_codec.go:187, communication_binding_codec.go:191")),
		model.Leaf("[].target", model.None("a declarative target field path, checked only for shape: communication_binding_codec.go:188, communication_binding_codec.go:191")),
		model.Leaf("[].cardinality", model.None("a mapping cardinality, a closed set: communication_binding_codec.go:77-80, communication_binding_codec.go:192")),
		model.Leaf("[].transform", model.None("a mapping transform, a closed set with no executable form: communication_binding_codec.go:82-86, communication_binding_codec.go:192")),
	)
	// pdeclBindingLosses is the known-loss list the spec writer marshals
	// (communication_binding_codec.go:333) and the reader decodes
	// (communication_binding_codec.go:383).
	pdeclBindingLosses = model.Nested([]ProtocolBindingLoss{}, model.ClassEvidence,
		model.Leaf("[].field", model.None("the field path of a known loss, checked only for shape: communication_binding_codec.go:219, communication_binding_codec.go:222")),
		model.Leaf("[].reason_code", model.None("a bounded loss reason code: communication_binding_codec.go:222")),
		model.Leaf("[].acceptance_ref", model.None("an opaque reference to the acceptance of a loss, checked only for shape and never resolved to an account: communication_binding_codec.go:222-224")),
	)
	// pdeclBindingRuleRefs is the rule reference list the spec writer marshals
	// (communication_binding_codec.go:337) and the reader decodes
	// (communication_binding_codec.go:384).
	pdeclBindingRuleRefs = model.Nested([]string{}, model.ClassEvidence,
		model.Leaf("[]", model.None("an opaque rule reference, checked only for shape: communication_binding_codec.go:240-255")),
	)

	// pdeclBindingOwnerRef is the work item owner (kind "user" with a bare
	// account id, work_state.go:317) copied onto the binding at reservation and
	// compared with the item on every settlement
	// (communication_binding_service.go:993-995). The work item row keeps the
	// duty; this copy is evidence of the owner the exchange ran under.
	pdeclBindingOwnerRef = model.KindRef(colCommOwnerKind, model.ClassEvidence)

	// pdeclBindingMCPTask is the MCP task projection the binding writer
	// marshals (communication_binding_codec.go:475) and the reader decodes
	// (communication_binding_codec.go:514-517). Its owner tuple is what the MCP
	// adapter compares, byte for byte, with the caller's validated token before
	// any task method may operate the task (cmd/olivares/mcpdurabletasks.go:1436-1446,
	// connectors/mcp/taskledger.go:474-479), so the row lets that owner operate
	// the task: AUTHORITY. The subject is the token's subject claim, which the
	// plane's own authorization grant mints as the bare account id
	// (core/auth/ema.go:257), and act_as is the on-behalf-of account id of a
	// delegated token (connectors/mcp/tokenvalidate.go:436-464); both are
	// declared as bare account ids. A subject minted by another issuer does not
	// parse as an account id and resolves to nothing.
	pdeclBindingMCPTask = model.Nested(ProtocolMCPTaskProjection{}, model.ClassAuthority,
		model.Leaf("owner.subject", model.Ref(model.EncodeUserID, model.ClassAuthority)),
		model.Leaf("owner.act_as", model.Ref(model.EncodeUserID, model.ClassAuthority)),
		model.Leaf("owner.issuer", model.None("the token issuer, compared byte for byte as part of the owner tuple and never resolved to an account: communication_binding_service.go:657, cmd/olivares/mcpdurabletasks.go:1445")),
		model.Leaf("owner.client_id", model.None("the OAuth client identity of the token, compared byte for byte as part of the owner tuple: communication_binding_service.go:658, cmd/olivares/mcpdurabletasks.go:1446")),
		model.Leaf("tool", model.None("the MCP tool name, checked only for shape and compared with the task intent: communication_binding_service.go:661, cmd/olivares/mcpdurabletasks.go:1461")),
		model.Leaf("required_scope", model.None("an OAuth scope the task requires: communication_binding_service.go:662, cmd/olivares/mcpdurabletasks.go:1462")),
		model.Leaf("initial_status", model.None("the task's initial status, a bounded token: communication_binding_service.go:663")),
		model.Leaf("initial_status_reason", model.None("an opaque initial status reason, checked only for shape: communication_binding_service.go:664")),
		model.Leaf("upstream_descriptor", model.None("an opaque upstream descriptor, checked only for shape: communication_binding_service.go:665")),
		model.Leaf("protocol_revision", model.None("an opaque protocol revision, checked only for shape: communication_binding_service.go:665")),
		model.Leaf("origin_operation_id", model.None("the id of the operation that created the task: communication_binding_service.go:666, cmd/olivares/mcpdurabletasks.go:1450-1451")),
		model.Leaf("origin_effect_digest", model.None("a digest of the operation's effect: communication_binding_service.go:666, cmd/olivares/mcpdurabletasks.go:1450-1451")),
		model.Leaf("initial_input_requests[].key_digest", model.None("a connector-computed SHA-256 of an input request key: communication_protocol_interrupt.go:45-52, communication_protocol_interrupt.go:175")),
		model.Leaf("initial_input_requests[].content_digest", model.None("a connector-computed SHA-256 of an input request value: communication_protocol_interrupt.go:45-52, communication_protocol_interrupt.go:178")),
	)
)
