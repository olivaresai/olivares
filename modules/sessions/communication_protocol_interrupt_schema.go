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
			model.FieldSpec{Name: colInterruptBindingID, Kind: model.KindUUID, Principal: model.None("the id of the protocol binding the interrupt belongs to: communication_protocol_interrupt.go:777, communication_protocol_interrupt.go:822")},
			model.FieldSpec{Name: colInterruptBindingGeneration, Kind: model.KindInt},
			model.FieldSpec{Name: colWorkItemID, Kind: model.KindUUID, Principal: pdeclNoneWorkItemID},
			model.FieldSpec{Name: colInterruptProtocol, Kind: model.KindText, Principal: pdeclNoneBindingProtocol},
			model.FieldSpec{Name: colInterruptRemoteState, Kind: model.KindText, Principal: model.None("an interrupt remote state, a closed set of two: communication_protocol_interrupt.go:252-253")},
			model.FieldSpec{Name: colInterruptKeyHash, Kind: model.KindBytes, Principal: model.None("a connector-computed SHA-256 of the external request key: communication_protocol_interrupt.go:148-157, communication_protocol_interrupt.go:344")},
			model.FieldSpec{Name: colInterruptContentHash, Kind: model.KindBytes, Principal: model.None("a connector-computed SHA-256 of the external request value: communication_protocol_interrupt.go:148-157, communication_protocol_interrupt.go:345")},
			model.FieldSpec{Name: colInterruptRouteHash, Kind: model.KindBytes, Principal: model.None("the SHA-256 of the canonical interrupt route: communication_protocol_interrupt.go:140-145")},
			model.FieldSpec{Name: colCommChannelID, Kind: model.KindUUID, Principal: pdeclNoneChannelID},
			model.FieldSpec{Name: colInterruptSenderUserID, Kind: model.KindUUID, Principal: pdeclInterruptSender},
			model.FieldSpec{Name: colInterruptRecipientUserID, Kind: model.KindUUID, Principal: pdeclInterruptRecipient},
			model.FieldSpec{Name: colInterruptMessageID, Kind: model.KindUUID, Principal: model.None("the id of the actionable message the interrupt published: communication_protocol_interrupt.go:788")},
			model.FieldSpec{Name: colInterruptDeliveryID, Kind: model.KindUUID, Principal: model.None("the id of the recipient's delivery of that message: communication_protocol_interrupt.go:789")},
			model.FieldSpec{Name: colInterruptState, Kind: model.KindText, Principal: model.None("an interrupt state, a closed set: communication_protocol_interrupt.go:23-25, communication_protocol_interrupt.go:581, communication_protocol_interrupt.go:738")},
			model.FieldSpec{Name: colInterruptResponseHash, Kind: model.KindBytes, Nullable: true, Principal: pdeclNoneInterruptHash},
			model.FieldSpec{Name: colInterruptOperationHash, Kind: model.KindBytes, Nullable: true, Principal: pdeclNoneInterruptHash},
			model.FieldSpec{Name: colInterruptEffectHash, Kind: model.KindBytes, Nullable: true, Principal: pdeclNoneInterruptHash},
			model.FieldSpec{Name: colInterruptAckID, Kind: model.KindUUID, Nullable: true, Principal: model.None("the id of the acknowledgement that answered the interrupt: communication_protocol_interrupt.go:740, communication_protocol_interrupt.go:850")},
			model.FieldSpec{Name: colInterruptResponseMessageID, Kind: model.KindUUID, Nullable: true, Principal: model.None("the id of the response message the answer published: communication_protocol_interrupt.go:741, communication_protocol_interrupt.go:854")},
			model.FieldSpec{Name: colInterruptResponseDeliveryID, Kind: model.KindUUID, Nullable: true, Principal: model.None("the id of the delivery of the response message: communication_protocol_interrupt.go:742, communication_protocol_interrupt.go:858")},
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

// Principal declarations of the protocol-interrupt link. Its route names two
// distinct accounts as bare ids (communication_protocol_interrupt.go:126-138):
// the recipient who must answer the interrupt and the sender it was relayed as.
var (
	pdeclInterruptRecipient = model.Ref(model.EncodeUserID, model.ClassObligation)
	pdeclInterruptSender    = model.Ref(model.EncodeUserID, model.ClassEvidence)
	pdeclNoneInterruptHash  = model.None("a digest of the interrupt response, of its operation or of its effect: communication_protocol_interrupt.go:582-584, communication_protocol_interrupt.go:739")
)
