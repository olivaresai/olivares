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
				model.FieldSpec{Name: colBindingKey, Kind: model.KindText},
				model.FieldSpec{Name: colCommGeneration, Kind: model.KindInt},
				model.FieldSpec{Name: colBindingProtocol, Kind: model.KindText},
				model.FieldSpec{Name: colBindingProtocolVersion, Kind: model.KindText},
				model.FieldSpec{Name: colBindingDirection, Kind: model.KindText},
				model.FieldSpec{Name: colBindingLocalKind, Kind: model.KindText},
				model.FieldSpec{Name: colBindingLocalSelectorJSON, Kind: model.KindJSON},
				model.FieldSpec{Name: colBindingPeerAuthority, Kind: model.KindText},
				model.FieldSpec{Name: colBindingRemoteResourceKind, Kind: model.KindText},
				model.FieldSpec{Name: colBindingRemoteResourceRef, Kind: model.KindText},
				model.FieldSpec{Name: colBindingMappingSchema, Kind: model.KindText},
				model.FieldSpec{Name: colBindingMappingJSON, Kind: model.KindJSON},
				model.FieldSpec{Name: colBindingMappingHash, Kind: model.KindBytes},
				model.FieldSpec{Name: colBindingKnownLossesJSON, Kind: model.KindJSON},
				model.FieldSpec{Name: colBindingLossesHash, Kind: model.KindBytes},
				model.FieldSpec{Name: colBindingRuleRefsJSON, Kind: model.KindJSON},
				model.FieldSpec{Name: colBindingPermissionProfileRef, Kind: model.KindText},
				model.FieldSpec{Name: colBindingCurrencyPolicy, Kind: model.KindText},
				model.FieldSpec{Name: colBindingValidationVerdict, Kind: model.KindText},
				model.FieldSpec{Name: colBindingValidationCode, Kind: model.KindText},
				model.FieldSpec{Name: colBindingValidatedAt, Kind: model.KindTimestamp, Nullable: true},
				model.FieldSpec{Name: colBindingState, Kind: model.KindText},
				model.FieldSpec{Name: colCommSupersedesID, Kind: model.KindUUID, Nullable: true},
				model.FieldSpec{Name: colBindingActiveSlot, Kind: model.KindText, Nullable: true},
				model.FieldSpec{Name: colBindingSpecHash, Kind: model.KindBytes},
				model.FieldSpec{Name: colBindingPlanHash, Kind: model.KindBytes},
				model.FieldSpec{Name: colBindingCommandKeyHash, Kind: model.KindBytes},
				model.FieldSpec{Name: colBindingRequestHash, Kind: model.KindBytes},
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
				model.FieldSpec{Name: colBindingSpecID, Kind: model.KindUUID},
				model.FieldSpec{Name: colBindingSpecGeneration, Kind: model.KindInt},
				model.FieldSpec{Name: colBindingPinnedSpecHash, Kind: model.KindBytes},
				model.FieldSpec{Name: colBindingPinnedMappingHash, Kind: model.KindBytes},
				model.FieldSpec{Name: colBindingPinnedLossesHash, Kind: model.KindBytes},
				model.FieldSpec{Name: colCommMessageID, Kind: model.KindUUID, Nullable: true},
				model.FieldSpec{Name: colCommDeliveryID, Kind: model.KindUUID, Nullable: true},
				model.FieldSpec{Name: colWorkItemID, Kind: model.KindUUID, Nullable: true},
				model.FieldSpec{Name: colBindingProtocol, Kind: model.KindText},
				model.FieldSpec{Name: colBindingProtocolVersion, Kind: model.KindText},
				model.FieldSpec{Name: colBindingDirection, Kind: model.KindText},
				model.FieldSpec{Name: colBindingPeerAuthority, Kind: model.KindText},
				model.FieldSpec{Name: colBindingRemoteResourceRef, Kind: model.KindText},
				model.FieldSpec{Name: colBindingAttemptID, Kind: model.KindUUID},
				model.FieldSpec{Name: colBindingDispatchKeyHash, Kind: model.KindBytes},
				model.FieldSpec{Name: colBindingReservationHash, Kind: model.KindBytes},
				model.FieldSpec{Name: colCommGeneration, Kind: model.KindInt},
				model.FieldSpec{Name: colBindingSyntheticSID, Kind: model.KindText},
				model.FieldSpec{Name: colCommOwnerKind, Kind: model.KindText, Nullable: true},
				model.FieldSpec{Name: colCommOwnerRef, Kind: model.KindText, Nullable: true},
				model.FieldSpec{Name: colBindingOwnerDigest, Kind: model.KindBytes, Nullable: true},
				model.FieldSpec{Name: colBindingOwnerEpoch, Kind: model.KindInt},
				model.FieldSpec{Name: colBindingLeaseFence, Kind: model.KindInt},
				model.FieldSpec{Name: colBindingExternalKind, Kind: model.KindText},
				model.FieldSpec{Name: colBindingExternalID, Kind: model.KindText, Nullable: true},
				model.FieldSpec{Name: colBindingContextID, Kind: model.KindText, Nullable: true},
				model.FieldSpec{Name: colBindingExternalMessageID, Kind: model.KindText, Nullable: true},
				model.FieldSpec{Name: colBindingLocalState, Kind: model.KindText},
				model.FieldSpec{Name: colBindingRemoteState, Kind: model.KindText},
				model.FieldSpec{Name: colBindingRemoteRevision, Kind: model.KindText, Nullable: true},
				model.FieldSpec{Name: colBindingObservationVerdict, Kind: model.KindText},
				model.FieldSpec{Name: colBindingObservationCode, Kind: model.KindText},
				model.FieldSpec{Name: colBindingLastObservedAt, Kind: model.KindTimestamp, Nullable: true},
				model.FieldSpec{Name: colBindingDetailHash, Kind: model.KindBytes, Nullable: true},
				model.FieldSpec{Name: colBindingCurrentTTLMs, Kind: model.KindInt, Nullable: true},
				model.FieldSpec{Name: colBindingCurrentPollMs, Kind: model.KindInt, Nullable: true},
				model.FieldSpec{Name: colBindingTerminal, Kind: model.KindBool},
				model.FieldSpec{Name: colBindingExternalActiveSlot, Kind: model.KindText, Nullable: true},
				model.FieldSpec{Name: colBindingLastUpdateHash, Kind: model.KindBytes, Nullable: true},
				model.FieldSpec{Name: colBindingCancelRequested, Kind: model.KindBool},
				model.FieldSpec{Name: colBindingCancelRequestedAt, Kind: model.KindTimestamp, Nullable: true},
				model.FieldSpec{Name: colBindingCancelReasonCode, Kind: model.KindText, Nullable: true},
				model.FieldSpec{Name: colBindingCancelKeyHash, Kind: model.KindBytes, Nullable: true},
				model.FieldSpec{Name: colBindingMCPTaskJSON, Kind: model.KindJSON, Nullable: true},
				model.FieldSpec{Name: colBindingMCPTaskHash, Kind: model.KindBytes, Nullable: true},
				model.FieldSpec{Name: colBindingLastCommandID, Kind: model.KindUUID},
				model.FieldSpec{Name: colBindingLastEventID, Kind: model.KindUUID},
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
