// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

const (
	protocolInterruptKind  model.Kind = "sessions.protocol_interrupt"
	protocolInterruptTable            = "sessions_protocol_interrupt"

	colInterruptBindingID          = "binding_id"
	colInterruptBindingGeneration  = "binding_generation"
	colInterruptProtocol           = "protocol"
	colInterruptRemoteState        = "remote_state"
	colInterruptKeyHash            = "request_key_hash"
	colInterruptContentHash        = "request_content_hash"
	colInterruptRouteHash          = "route_hash"
	colInterruptSenderUserID       = "sender_user_id"
	colInterruptRecipientUserID    = "recipient_user_id"
	colInterruptMessageID          = "interrupt_message_id"
	colInterruptDeliveryID         = "interrupt_delivery_id"
	colInterruptState              = "interrupt_state"
	colInterruptResponseHash       = "response_hash"
	colInterruptOperationHash      = "response_operation_hash"
	colInterruptEffectHash         = "response_effect_hash"
	colInterruptAckID              = "response_ack_id"
	colInterruptResponseMessageID  = "response_message_id"
	colInterruptResponseDeliveryID = "response_delivery_id"
)

func (m *Module) registerProtocolInterruptSchema(reg store.ExtensionRegistry) error {
	return reg.Register(model.EntityDescriptor{
		Kind: protocolInterruptKind, Table: protocolInterruptTable,
		WorkspaceLineage: hiddenWorkspaceLineage,
		Fields: communicationFields(
			model.FieldSpec{Name: colInterruptBindingID, Kind: model.KindUUID},
			model.FieldSpec{Name: colInterruptBindingGeneration, Kind: model.KindInt},
			model.FieldSpec{Name: colWorkItemID, Kind: model.KindUUID},
			model.FieldSpec{Name: colInterruptProtocol, Kind: model.KindText},
			model.FieldSpec{Name: colInterruptRemoteState, Kind: model.KindText},
			model.FieldSpec{Name: colInterruptKeyHash, Kind: model.KindBytes},
			model.FieldSpec{Name: colInterruptContentHash, Kind: model.KindBytes},
			model.FieldSpec{Name: colInterruptRouteHash, Kind: model.KindBytes},
			model.FieldSpec{Name: colCommChannelID, Kind: model.KindUUID},
			model.FieldSpec{Name: colInterruptSenderUserID, Kind: model.KindUUID},
			model.FieldSpec{Name: colInterruptRecipientUserID, Kind: model.KindUUID},
			model.FieldSpec{Name: colInterruptMessageID, Kind: model.KindUUID},
			model.FieldSpec{Name: colInterruptDeliveryID, Kind: model.KindUUID},
			model.FieldSpec{Name: colInterruptState, Kind: model.KindText},
			model.FieldSpec{Name: colInterruptResponseHash, Kind: model.KindBytes, Nullable: true},
			model.FieldSpec{Name: colInterruptOperationHash, Kind: model.KindBytes, Nullable: true},
			model.FieldSpec{Name: colInterruptEffectHash, Kind: model.KindBytes, Nullable: true},
			model.FieldSpec{Name: colInterruptAckID, Kind: model.KindUUID, Nullable: true},
			model.FieldSpec{Name: colInterruptResponseMessageID, Kind: model.KindUUID, Nullable: true},
			model.FieldSpec{Name: colInterruptResponseDeliveryID, Kind: model.KindUUID, Nullable: true},
		),
		Indexes: communicationIndexes(
			"sessions_protocol_interrupt_workspace",
			model.IndexSpec{
				Name: "sessions_protocol_interrupt_key_uniq",
				Columns: []string{
					model.ColTenantID, colInterruptBindingID,
					colInterruptBindingGeneration, colInterruptKeyHash,
				},
				Unique: true,
			},
			model.IndexSpec{
				Name: "sessions_protocol_interrupt_pending",
				Columns: []string{
					model.ColTenantID, colWorkWorkspaceID, colInterruptBindingID,
					colInterruptBindingGeneration, colInterruptState, model.ColID,
				},
			},
		),
	})
}
