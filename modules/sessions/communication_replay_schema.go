// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

const (
	protocolReplayGuardKind  model.Kind = "sessions.communication_replay_guard"
	protocolReplayGuardTable            = "sessions_communication_replay_guard"

	colReplayProtocol      = "protocol"
	colReplayPeerAuthority = "peer_authority"
	colReplayKind          = "replay_kind"
	colReplayHash          = "replay_hash"
	colReplayFirstSeenAt   = "first_seen_at"
	colReplayExpiresAt     = "expires_at"
	colReplayBindingID     = "binding_id"
)

// registerProtocolReplayGuardSchema is the additive K5 replay authority. The
// raw provider identifier never crosses this descriptor: only its domain-bound
// SHA-256 commitment is durable. Rows are immutable evidence until an explicit
// expiry-GC contract is introduced.
func (m *Module) registerProtocolReplayGuardSchema(reg store.ExtensionRegistry) error {
	return reg.Register(model.EntityDescriptor{
		Kind: protocolReplayGuardKind, Table: protocolReplayGuardTable,
		AppendOnly: true, WorkspaceLineage: hiddenWorkspaceLineage,
		Fields: communicationFields(
			model.FieldSpec{Name: colReplayProtocol, Kind: model.KindText, Principal: pdeclNoneBindingProtocol},
			model.FieldSpec{Name: colReplayPeerAuthority, Kind: model.KindText, Principal: pdeclNonePeerAuthority},
			model.FieldSpec{Name: colReplayKind, Kind: model.KindText, Principal: model.None("a replay identifier kind, a closed set: communication_replay_service.go:26-28, communication_replay_service.go:91-94")},
			model.FieldSpec{Name: colReplayHash, Kind: model.KindBytes, Principal: pdeclNoneReplayHash},
			model.FieldSpec{Name: colReplayFirstSeenAt, Kind: model.KindTimestamp},
			model.FieldSpec{Name: colReplayExpiresAt, Kind: model.KindTimestamp},
			model.FieldSpec{Name: colReplayBindingID, Kind: model.KindUUID, Nullable: true, Principal: model.None("the id of the protocol binding the guarded mutation settled: communication_replay_service.go:285")},
		),
		Indexes: communicationIndexes(
			"sessions_communication_replay_guard_workspace",
			model.IndexSpec{
				Name: "sessions_communication_replay_guard_claim_uniq",
				Columns: []string{
					model.ColTenantID, colReplayProtocol, colReplayPeerAuthority,
					colReplayKind, colReplayHash,
				},
				Unique: true,
			},
			model.IndexSpec{
				Name:    "sessions_communication_replay_guard_expiry",
				Columns: []string{model.ColTenantID, colReplayExpiresAt, model.ColID},
			},
			model.IndexSpec{
				Name:    "sessions_communication_replay_guard_binding",
				Columns: []string{model.ColTenantID, colWorkWorkspaceID, colReplayBindingID, model.ColID},
			},
		),
	})
}

// Principal declarations of the replay guard. The provider's replay identifier
// is kept only as a domain-bound digest.
var (
	pdeclNoneReplayHash = model.None("a domain-bound SHA-256 of the provider's replay identifier; the raw identifier is never stored: communication_replay_service.go:116-120, communication_replay_service.go:282")
)
