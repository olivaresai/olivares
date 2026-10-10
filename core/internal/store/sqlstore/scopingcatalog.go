// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import "github.com/olivaresai/olivares/core/model"

// This file catalogs the FASE X scoping entities — Workspace, AgentGroup
// and AgentGroupMember — in the same descriptor+codec style as catalog.go. They
// are ordinary core, tenant-resident entities: the engine generates and guards
// their tables, they carry tenant_id, and they are reached through the
// tenant-pinned Scope (NOT the auth partition — they are not global principals).
// Auditing is off at the row level like every other core entity (binds the
// real actor and turns it on).

// scopingDescriptors are appended to coreDescriptors() so the engine generates
// and guards their tables. Order is irrelevant (no DB-level foreign keys).
func scopingDescriptors() []model.EntityDescriptor {
	return []model.EntityDescriptor{
		workspaceDescriptor, agentGroupDescriptor, agentGroupMemberDescriptor,
	}
}

// --- Workspace ---------------------------------------------------------------

var workspaceDescriptor = model.EntityDescriptor{
	Kind:  "core.workspace",
	Table: "workspaces",
	Fields: []model.FieldSpec{
		pdecl(field("name", model.KindText, false),
			model.None("the workspace's display name: core/model/scoping.go:38")),
		pdecl(indexedField("slug", model.KindText, false),
			model.None("a tenant-unique URL-safe workspace handle: core/model/scoping.go:40")),
		pdecl(field("status", model.KindText, false), pdeclNoneLifecycle),
		pdecl(field("settings", model.KindJSON, true),
			model.None("free-form, non-sensitive workspace configuration: core/model/scoping.go:49")),
		// Core v25 tree columns, nullable and appended last: parent_id NULL is a
		// root; path is the store-maintained materialized path, indexed so a
		// subtree is one prefix scan (the resources pattern).
		pdecl(indexedField("parent_id", model.KindUUID, true),
			model.None("the parent workspace row: core/model/scoping.go:55")),
		pdecl(indexedField("path", model.KindText, true),
			model.None("the store-maintained path of ancestor workspace ids: core/model/scoping.go:59")),
	},
	// Slug is unique per tenant: it is the stable handle, and the reserved
	// "default" slug must resolve to exactly one row per tenant (the index is the
	// atomic backstop behind the application-level default-workspace ensure).
	Indexes: []model.IndexSpec{
		{Name: "workspaces_slug_uniq", Columns: []string{"tenant_id", "slug"}, Unique: true},
	},
}

var workspaceCodec = model.Codec[model.Workspace]{
	Base: func(w *model.Workspace) *model.BaseFields { return &w.BaseFields },
	Encode: func(w model.Workspace) (model.Record, error) {
		settings, err := encJSON(w.Settings)
		if err != nil {
			return nil, err
		}
		return model.Record{"name": w.Name, "slug": w.Slug, "status": string(w.Status),
			"settings": settings, "parent_id": encOptID(w.ParentID), "path": encOptStr(w.Path)}, nil
	},
	Decode: func(b model.BaseFields, r model.Record) (model.Workspace, error) {
		settings, err := decJSON(r, "settings")
		if err != nil {
			return model.Workspace{}, err
		}
		return model.Workspace{BaseFields: b, Name: r.String("name"), Slug: r.String("slug"),
			Status: model.LifecycleStatus(r.String("status")), Settings: settings,
			ParentID: decID(r, "parent_id"), Path: r.String("path")}, nil
	},
}

// --- AgentGroup --------------------------------------------------------------

var agentGroupDescriptor = model.EntityDescriptor{
	Kind:  "core.agent_group",
	Table: "agent_groups",
	Fields: []model.FieldSpec{
		// workspace_id NULL means the tenant's default workspace (back-compat).
		pdecl(indexedField("workspace_id", model.KindUUID, true), pdeclNoneWorkspaceLineage),
		pdecl(field("name", model.KindText, false),
			model.None("the agent group's display name: core/model/scoping.go:73")),
		pdecl(indexedField("slug", model.KindText, false),
			model.None("a tenant-unique URL-safe agent-group handle: core/model/scoping.go:75")),
		pdecl(field("description", model.KindText, true),
			model.None("a short, non-sensitive description: core/model/scoping.go:78")),
		pdecl(field("status", model.KindText, false), pdeclNoneLifecycle),
		pdecl(field("metadata", model.KindJSON, true),
			model.None("free-form, non-sensitive context: core/model/scoping.go:82")),
	},
	// Slug is unique per tenant (a single namespace across workspaces): a group is
	// a tenant-level handle that may be workspace-scoped, not a per-workspace name.
	Indexes: []model.IndexSpec{
		{Name: "agent_groups_slug_uniq", Columns: []string{"tenant_id", "slug"}, Unique: true},
	},
	WorkspaceLineage: model.WorkspaceLineageSpec{
		Column:   "workspace_id",
		Encoding: model.WorkspaceLineageID,
		Unset:    model.WorkspaceUnsetMeansDefault,
	},
}

var agentGroupCodec = model.Codec[model.AgentGroup]{
	Base: func(g *model.AgentGroup) *model.BaseFields { return &g.BaseFields },
	Encode: func(g model.AgentGroup) (model.Record, error) {
		meta, err := encJSON(g.Metadata)
		if err != nil {
			return nil, err
		}
		return model.Record{"workspace_id": encOptID(g.WorkspaceID), "name": g.Name, "slug": g.Slug,
			"description": g.Description, "status": string(g.Status), "metadata": meta}, nil
	},
	Decode: func(b model.BaseFields, r model.Record) (model.AgentGroup, error) {
		meta, err := decJSON(r, "metadata")
		if err != nil {
			return model.AgentGroup{}, err
		}
		return model.AgentGroup{BaseFields: b, WorkspaceID: decID(r, "workspace_id"),
			Name: r.String("name"), Slug: r.String("slug"), Description: r.String("description"),
			Status: model.LifecycleStatus(r.String("status")), Metadata: meta}, nil
	},
}

// --- AgentGroupMember --------------------------------------------------------

var agentGroupMemberDescriptor = model.EntityDescriptor{
	Kind:  "core.agent_group_member",
	Table: "agent_group_members",
	Fields: []model.FieldSpec{
		pdecl(indexedField("group_id", model.KindUUID, false),
			model.None("the agent group row the membership belongs to: core/model/scoping.go:92")),
		pdecl(indexedField("agent_id", model.KindUUID, false),
			model.None("the member agent row, never an account: core/model/scoping.go:94")),
	},
	// One row per (group, agent); enumerated by group_id (roster) and agent_id
	// (an agent's groups — the access-engine expansion fold).
	Indexes: []model.IndexSpec{
		{Name: "agent_group_members_uniq", Columns: []string{"tenant_id", "group_id", "agent_id"}, Unique: true},
	},
}

var agentGroupMemberCodec = model.Codec[model.AgentGroupMember]{
	Base: func(m *model.AgentGroupMember) *model.BaseFields { return &m.BaseFields },
	Encode: func(m model.AgentGroupMember) (model.Record, error) {
		return model.Record{"group_id": m.GroupID.String(), "agent_id": m.AgentID.String()}, nil
	},
	Decode: func(b model.BaseFields, r model.Record) (model.AgentGroupMember, error) {
		return model.AgentGroupMember{BaseFields: b, GroupID: decID(r, "group_id"),
			AgentID: decID(r, "agent_id")}, nil
	},
}
