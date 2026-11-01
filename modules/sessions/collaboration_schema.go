// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only

package sessions

import (
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// registerCollaborationSchema is storage only. Registering the same additive
// tables in every edition preserves data when a deployment changes editions.
// No reader, grant policy or session-input path is enabled by these descriptors.
func registerCollaborationSchema(reg store.ExtensionRegistry) error {
	lineage := model.WorkspaceLineageSpec{Column: "workspace_id", Encoding: model.WorkspaceLineageID, Unset: model.WorkspaceUnsetHidden}
	opaque := model.None("opaque record linkage, never an account reference: collaboration_schema.go:20")
	descriptors := []model.EntityDescriptor{
		{Kind: "sessions.share", Table: "sessions_share", Audited: true, Internal: true, WorkspaceLineage: lineage, Fields: []model.FieldSpec{
			{Name: "run_ref", Kind: model.KindText, Indexed: true, Principal: model.None("the unique external reference read by findRunRec, not a principal: runtime.go:4081")},
			{Name: "run_id", Kind: model.KindUUID, Principal: opaque},
			{Name: "workspace_id", Kind: model.KindUUID, Nullable: true, Principal: opaque},
			{Name: "owner_id", Kind: model.KindUUID, Principal: model.Ref(model.EncodeUserID, model.ClassAuthority)},
			{Name: "guest_id", Kind: model.KindUUID, Indexed: true, Principal: model.Ref(model.EncodeUserID, model.ClassAuthority)},
			{Name: "guest_label", Kind: model.KindText, Nullable: true, Principal: model.Ref(model.EncodeEmail, model.ClassEvidence)},
			{Name: "expires_at", Kind: model.KindTimestamp},
			{Name: "revoked", Kind: model.KindBool},
		}},
		{Kind: "sessions.proposal", Table: "sessions_proposal", Audited: true, Internal: true, WorkspaceLineage: lineage, Fields: []model.FieldSpec{
			{Name: "share_id", Kind: model.KindUUID, Indexed: true, Principal: opaque},
			{Name: "workspace_id", Kind: model.KindUUID, Nullable: true, Principal: opaque},
			{Name: "guest_id", Kind: model.KindUUID, Principal: model.Ref(model.EncodeUserID, model.ClassEvidence)},
			{Name: "text", Kind: model.KindText, Principal: model.Scan(model.ClassThirdPartyText)},
			{Name: "state", Kind: model.KindText, Principal: model.None("proposal disposition, not an identity: collaboration_schema.go:33")},
		}},
	}
	for _, descriptor := range descriptors {
		if err := reg.Register(descriptor); err != nil {
			return err
		}
	}
	return nil
}
