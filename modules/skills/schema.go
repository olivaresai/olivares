// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package skills

import (
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// The organization catalog belongs to its default workspace until catalog
// placement is exposed. NULL gives existing packs and immutable revisions that
// same lineage without rewriting them. Confined scopes receive readers only;
// catalog administration remains tenant-wide. Assignments carry target lineage.
func (m *Module) RegisterSchema(reg store.ExtensionRegistry) error {
	none := model.None("catalog identifiers, bounded labels and content hashes are rendered or compared, never resolved to a principal: catalog.go:140, assignment.go:115")
	actor := model.Ref(model.EncodeUserRef, model.ClassEvidence)
	text := model.Scan(model.ClassThirdPartyText)
	revision := model.Nested(Revision{}, model.ClassEvidence,
		model.Leaf("id", none), model.Leaf("pack_id", none), model.Leaf("source.kind", none), model.Leaf("source.origin", text), model.Leaf("source.requested_ref", none), model.Leaf("source.resolved_commit", none), model.Leaf("source.source_digest", none),
		model.Leaf("manifest_digest", none), model.Leaf("manifest[].path", text), model.Leaf("manifest[].sha256", none),
		model.Leaf("members[].name", text), model.Leaf("members[].directory", text), model.Leaf("members[].description", text), model.Leaf("members[].license", text), model.Leaf("members[].compatibility", text), model.Leaf("members[].metadata{}", text), model.Leaf("members[].metadata{key}", text), model.Leaf("members[].skill_digest", none), model.Leaf("members[].content_digest", none), model.Leaf("members[].unsupported_extensions[]", text), model.Leaf("members[].scripts[]", text),
		model.Leaf("validator", none), model.Leaf("created_by", actor), model.Leaf("created_at", none))
	pack := model.Nested(Pack{}, model.ClassEvidence, model.Leaf("id", none), model.Leaf("name", text), model.Leaf("state", none), model.Leaf("latest_revision_id", none), model.Leaf("created_by", actor), model.Leaf("created_at", none), model.Leaf("updated_at", none))
	catalogLineage := model.WorkspaceLineageSpec{Column: "workspace_id", Encoding: model.WorkspaceLineageID, Unset: model.WorkspaceUnsetMeansDefault}
	for _, d := range []model.EntityDescriptor{
		{Kind: PackKind, Table: "skills_pack", WorkspaceLineage: catalogLineage, WorkspaceConfinedReadOnly: true, Fields: []model.FieldSpec{
			{Name: "workspace_id", Kind: model.KindUUID, Nullable: true, Principal: none},
			{Name: "name", Kind: model.KindText, Principal: text}, {Name: "state", Kind: model.KindText, Principal: none}, {Name: "latest_revision_id", Kind: model.KindUUID, Principal: none}, {Name: "latest_revision", Kind: model.KindInt}, {Name: "created_by", Kind: model.KindText, Principal: actor},
		}, Indexes: []model.IndexSpec{{Name: "name_uniq", Columns: []string{model.ColTenantID, "name"}, Unique: true}}},
		{Kind: RevisionKind, Table: "skills_revision", AppendOnly: true, WorkspaceLineage: catalogLineage, WorkspaceConfinedReadOnly: true, Fields: []model.FieldSpec{
			{Name: "workspace_id", Kind: model.KindUUID, Nullable: true, Principal: none},
			{Name: "pack_id", Kind: model.KindUUID, Indexed: true, Principal: none}, {Name: "payload", Kind: model.KindJSON, Principal: revision}, {Name: "created_by", Kind: model.KindText, Principal: actor},
		}},
		{Kind: receiptKind, Table: "skills_import_receipt", Fields: []model.FieldSpec{
			{Name: "key_hash", Kind: model.KindText, Principal: none}, {Name: "request_digest", Kind: model.KindText, Principal: none}, {Name: "pack_id", Kind: model.KindUUID, Principal: none}, {Name: "revision_id", Kind: model.KindUUID, Principal: none}, {Name: "pack_snapshot", Kind: model.KindJSON, Principal: pack},
		}, Indexes: []model.IndexSpec{{Name: "key_uniq", Columns: []string{model.ColTenantID, "key_hash"}, Unique: true}}},
		{Kind: AssignmentKind, Table: "skills_assignment", WorkspaceLineage: model.WorkspaceLineageSpec{Column: "workspace_id", Encoding: model.WorkspaceLineageID, Unset: model.WorkspaceUnsetHidden}, Fields: []model.FieldSpec{
			{Name: "target_kind", Kind: model.KindText, Principal: none}, {Name: "target_id", Kind: model.KindUUID, Principal: none}, {Name: "target_version", Kind: model.KindInt},
			{Name: "workspace_id", Kind: model.KindUUID, Nullable: true, Principal: none}, {Name: "pack_id", Kind: model.KindUUID, Indexed: true, Principal: none}, {Name: "revision_id", Kind: model.KindUUID, Principal: none},
			{Name: "members", Kind: model.KindJSON, Principal: model.Nested([]string{}, model.ClassEvidence, model.Leaf("[]", text))}, {Name: "created_by", Kind: model.KindText, Principal: actor},
		}, Indexes: []model.IndexSpec{{Name: "target_pack_uniq", Columns: []string{model.ColTenantID, "target_kind", "target_id", "pack_id"}, Unique: true}}},
	} {
		if err := reg.Register(d); err != nil {
			return err
		}
	}
	return nil
}
