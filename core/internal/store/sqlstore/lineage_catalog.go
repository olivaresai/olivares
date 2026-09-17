// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"fmt"

	"github.com/olivaresai/olivares/core/model"
)

type lineageRelation struct {
	kind    model.Kind
	table   string
	columns []string
}

// This is the complete projection consumed by and Cedar. A new resolver
// dependency must extend this inventory and the versioned SQL guard contract.
var lineageRelations = []lineageRelation{
	{"core.session", "sessions", []string{"workspace_id", "agent_id", "model_id", "deleted_at"}},
	{"core.agent", "agents", []string{"workspace_id", "deleted_at"}},
	{"core.resource", "resources", []string{"workspace_id", "path", "kind", "sensitivity"}},
	{"core.workspace", "workspaces", []string{"slug"}},
	{"core.agent_group", "agent_groups", []string{"workspace_id", "slug"}},
	{"core.agent_group_member", "agent_group_members", []string{"agent_id", "group_id"}},
}

func (r lineageRelation) descriptor() model.EntityDescriptor {
	kind, _ := model.LineageEpochKind(r.kind)
	table := "core_" + r.table + "_lineage_epoch"
	return model.EntityDescriptor{
		Kind: kind, Table: table, AuthorizationFact: true,
		AuthorizationLockOrder: 6,
		Indexes:                []model.IndexSpec{{Name: table + "_tenant_uniq", Columns: []string{"tenant_id"}, Unique: true}},
		Checks:                 []string{"id = tenant_id", "version >= 1", fmt.Sprintf("tenant_id <> '%s'", model.SystemTenantID)},
	}
}

func lineageDescriptors() []model.EntityDescriptor {
	out := make([]model.EntityDescriptor, 0, len(lineageRelations))
	for _, r := range lineageRelations {
		out = append(out, r.descriptor())
	}
	return out
}

func lineageForTable(table string) (lineageRelation, bool) {
	for _, r := range lineageRelations {
		if r.table == table {
			return r, true
		}
	}
	return lineageRelation{}, false
}
