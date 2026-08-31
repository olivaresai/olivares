// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

const (
	protocolSubscriptionCursorKind model.Kind = "sessions.protocol_subscription_cursor"
	protocolSubscriptionEventKind  model.Kind = "sessions.protocol_subscription_event"

	protocolSubscriptionCursorTable = "sessions_protocol_subscription_cursor"
	protocolSubscriptionEventTable  = "sessions_protocol_subscription_event"
)

const (
	colProtocolSubscriptionProtocol      = "protocol"
	colProtocolSubscriptionPeerAuthority = "peer_authority"
	colProtocolSubscriptionRouteHash     = "route_hash"
	colProtocolSubscriptionSubjectHash   = "subject_hash"
	colProtocolSubscriptionFilterHash    = "filter_hash"
	colProtocolSubscriptionLastEventID   = "last_event_id"
	colProtocolSubscriptionLastSeq       = "last_seq"
	colProtocolSubscriptionCursorID      = "cursor_id"
	colProtocolSubscriptionCursorSeq     = "cursor_seq"
	colProtocolSubscriptionHeadID        = "subscription_cursor_id"
	colProtocolSubscriptionMethod        = "method"
	colProtocolSubscriptionParamsJSON    = "params_json"
	colProtocolSubscriptionParamsHash    = "params_hash"
	colProtocolSubscriptionPreviousID    = "previous_event_id"
)

// registerProtocolSubscriptionSchema adds the durable MCP relay cursor and its
// append-only event log. The mutable head is only a CAS pointer; replay content
// lives in immutable event rows, so a restart cannot turn a partial head update
// into a fabricated gap.
func (m *Module) registerProtocolSubscriptionSchema(reg store.ExtensionRegistry) error {
	descriptors := []model.EntityDescriptor{
		{
			Kind: protocolSubscriptionCursorKind, Table: protocolSubscriptionCursorTable,
			WorkspaceLineage: hiddenWorkspaceLineage,
			Fields: communicationFields(
				model.FieldSpec{Name: colProtocolSubscriptionProtocol, Kind: model.KindText},
				model.FieldSpec{Name: colProtocolSubscriptionPeerAuthority, Kind: model.KindText},
				model.FieldSpec{Name: colProtocolSubscriptionRouteHash, Kind: model.KindBytes},
				model.FieldSpec{Name: colProtocolSubscriptionSubjectHash, Kind: model.KindBytes},
				model.FieldSpec{Name: colProtocolSubscriptionFilterHash, Kind: model.KindBytes},
				model.FieldSpec{Name: colProtocolSubscriptionLastEventID, Kind: model.KindUUID, Nullable: true},
				model.FieldSpec{Name: colProtocolSubscriptionLastSeq, Kind: model.KindInt},
			),
			Indexes: communicationIndexes(
				"sessions_protocol_subscription_cursor_workspace",
				model.IndexSpec{
					Name: "sessions_protocol_subscription_cursor_route_uniq",
					Columns: []string{
						model.ColTenantID, colWorkWorkspaceID, colProtocolSubscriptionRouteHash,
					},
					Unique: true,
				},
				model.IndexSpec{
					Name: "sessions_protocol_subscription_cursor_peer",
					Columns: []string{
						model.ColTenantID, colWorkWorkspaceID, colProtocolSubscriptionProtocol,
						colProtocolSubscriptionPeerAuthority, model.ColID,
					},
				},
			),
		},
		{
			Kind: protocolSubscriptionEventKind, Table: protocolSubscriptionEventTable,
			AppendOnly: true, WorkspaceLineage: hiddenWorkspaceLineage,
			Fields: communicationFields(
				model.FieldSpec{Name: colProtocolSubscriptionHeadID, Kind: model.KindUUID},
				model.FieldSpec{Name: colProtocolSubscriptionCursorID, Kind: model.KindUUID},
				model.FieldSpec{Name: colProtocolSubscriptionCursorSeq, Kind: model.KindInt},
				model.FieldSpec{Name: colProtocolSubscriptionMethod, Kind: model.KindText},
				model.FieldSpec{Name: colProtocolSubscriptionParamsJSON, Kind: model.KindJSON},
				model.FieldSpec{Name: colProtocolSubscriptionParamsHash, Kind: model.KindBytes},
				model.FieldSpec{Name: colProtocolSubscriptionPreviousID, Kind: model.KindUUID, Nullable: true},
			),
			Indexes: communicationIndexes(
				"sessions_protocol_subscription_event_workspace",
				model.IndexSpec{
					Name: "sessions_protocol_subscription_event_seq_uniq",
					Columns: []string{
						model.ColTenantID, colProtocolSubscriptionHeadID,
						colProtocolSubscriptionCursorSeq,
					},
					Unique: true,
				},
				model.IndexSpec{
					Name:    "sessions_protocol_subscription_event_cursor_uniq",
					Columns: []string{model.ColTenantID, colProtocolSubscriptionCursorID},
					Unique:  true,
				},
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
