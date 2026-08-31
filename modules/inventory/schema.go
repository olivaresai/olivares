// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package inventory

import (
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// Entity-kind labels for catalog entries. They name the core entity a catalog
// entry overlays, so a UI can group and filter the estate by kind.
const (
	kindSession   = "session"
	kindAgent     = "agent"
	kindIdentity  = "identity"
	kindMCPServer = "mcp_server"
	kindTool      = "tool"
	kindResource  = "resource"
	kindSkill     = "skill"
	kindModel     = "model"
	kindProvider  = "provider"
)

// Catalog-entry status values: active while the entity keeps being observed,
// stale once it has gone unseen past the threshold (a discovery gap, docs/SECURITY-HARDENING.md).
const (
	statusActive = "active"
	statusStale  = "stale"
)

// catalogEntryKind is the registered kind of the module's owned entity.
const catalogEntryKind model.Kind = "inventory.catalog_entry"

// catalogEntryTable is its physical table.
const catalogEntryTable = "inventory_catalog_entry"

// catalog-entry columns.
const (
	colEntityKind    = "entity_kind"
	colEntityID      = "entity_id"
	colName          = "name"
	colRef           = "ref"
	colStatus        = "status"
	colSignalSources = "signal_sources"
	colHosts         = "hosts"
	colFirstSeen     = "first_seen"
	colLastSeen      = "last_seen"
	colOccurrence    = "occurrence_count"
)

// RegisterSchema declares the module's owned catalog-entry entity. It satisfies
// the engine-side runtime.SchemaProvider seam (structural, so the module need
// not import the runtime package) and is called once, at store-construction
// time, before any Scope exists (S02 §7 /). The engine creates the table,
// injects the base columns and attaches the tenant guards — a module cannot opt
// out of isolation.
//
// The catalog entry is a discovery overlay over a core entity: it records how an
// entity was discovered (the signal sources that saw it, optional hosts), when
// it was first and last seen, how many times, and whether it is still live. It
// is deliberately NOT audited: discovery is high-frequency automated ingestion
// (like the AccessEdge upsert), not a security-sensitive mutation, and reads of
// the catalog are gated by RBAC at the API.
func (m *Module) RegisterSchema(reg store.ExtensionRegistry) error {
	return reg.Register(model.EntityDescriptor{
		Kind:  catalogEntryKind,
		Table: catalogEntryTable,
		Fields: []model.FieldSpec{
			{Name: colEntityKind, Kind: model.KindText, Indexed: true},
			{Name: colEntityID, Kind: model.KindUUID},
			{Name: colName, Kind: model.KindText},
			{Name: colRef, Kind: model.KindText, Nullable: true},
			{Name: colStatus, Kind: model.KindText, Indexed: true},
			{Name: colSignalSources, Kind: model.KindJSON, Nullable: true},
			{Name: colHosts, Kind: model.KindJSON, Nullable: true},
			{Name: colFirstSeen, Kind: model.KindTimestamp},
			{Name: colLastSeen, Kind: model.KindTimestamp, Indexed: true},
			{Name: colOccurrence, Kind: model.KindInt},
		},
		Indexes: []model.IndexSpec{{
			// One catalog entry per (kind, entity): a unique index keyed on
			// tenant_id first (requires it; a unique index that did not
			// start with tenant_id would couple tenants and leak existence).
			Name:    "inventory_catalog_entry_uniq",
			Columns: []string{model.ColTenantID, colEntityKind, colEntityID},
			Unique:  true,
		}},
	})
}
