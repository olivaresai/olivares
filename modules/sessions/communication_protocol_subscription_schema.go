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
				model.FieldSpec{Name: colProtocolSubscriptionProtocol, Kind: model.KindText, Principal: pdeclNoneBindingProtocol},
				model.FieldSpec{Name: colProtocolSubscriptionPeerAuthority, Kind: model.KindText, Principal: pdeclNonePeerAuthority},
				model.FieldSpec{Name: colProtocolSubscriptionRouteHash, Kind: model.KindBytes, Principal: model.None("the SHA-256 of the subscription route over workspace, protocol, peer, subject digest and filter digest: communication_protocol_subscription.go:136-140, communication_protocol_subscription.go:444")},
				model.FieldSpec{Name: colProtocolSubscriptionSubjectHash, Kind: model.KindBytes, Principal: model.None("a domain-separated SHA-256 of the subscribing MCP subject, compared only for route equality: communication_protocol_subscription.go:135, communication_protocol_subscription.go:444")},
				model.FieldSpec{Name: colProtocolSubscriptionFilterHash, Kind: model.KindBytes, Principal: model.None("a caller-supplied SHA-256 of the subscription filter: communication_protocol_subscription.go:131-134")},
				model.FieldSpec{Name: colProtocolSubscriptionLastEventID, Kind: model.KindUUID, Nullable: true, Principal: model.None("the id of the latest event row of the cursor: communication_protocol_subscription.go:473")},
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
				model.FieldSpec{Name: colProtocolSubscriptionHeadID, Kind: model.KindUUID, Principal: model.None("the id of the cursor head the event belongs to: communication_protocol_subscription.go:504")},
				model.FieldSpec{Name: colProtocolSubscriptionCursorID, Kind: model.KindUUID, Principal: model.None("the event's replay cursor id: communication_protocol_subscription.go:505")},
				model.FieldSpec{Name: colProtocolSubscriptionCursorSeq, Kind: model.KindInt},
				model.FieldSpec{Name: colProtocolSubscriptionMethod, Kind: model.KindText, Principal: model.None("an MCP notification method, a closed set: communication_protocol_subscription.go:151-157")},
				model.FieldSpec{Name: colProtocolSubscriptionParamsJSON, Kind: model.KindJSON, Principal: pdeclNoneSubscriptionParams},
				model.FieldSpec{Name: colProtocolSubscriptionParamsHash, Kind: model.KindBytes, Principal: model.None("a SHA-256 of the canonical params: communication_protocol_subscription.go:175, communication_protocol_subscription.go:509")},
				model.FieldSpec{Name: colProtocolSubscriptionPreviousID, Kind: model.KindUUID, Nullable: true, Principal: model.None("the id of the previous event of the cursor: communication_protocol_subscription.go:389, communication_protocol_subscription.go:510")},
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

// Principal declarations of the MCP relay cursor and its event log. The
// subscribing subject is stored only as a domain-separated digest and a
// notification's params are relayed verbatim; neither is resolved to an
// account.
var (
	pdeclNoneSubscriptionParams = model.None("the canonical params object of an MCP list-changed or resource-updated notification, relayed verbatim to the subscribed client and never resolved to an account: communication_protocol_subscription.go:149-175, communication_protocol_subscription.go:508")
)
