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

// freshnessSweepKind is the durable progress of the staleness sweep, one row per
// tenant. It is the whole of C2a's new state: where a cycle got to, so a restart
// resumes instead of starting the catalog again, and which cutoff was last
// carried to the end.
const freshnessSweepKind model.Kind = "inventory.freshness_sweep"

// freshnessSweepTable is its physical table.
const freshnessSweepTable = "inventory_freshness_sweep"

// freshness-sweep columns. All three are NULLABLE and all three mean "not yet"
// when unset — the row is created lazily on a tenant's first turn, never
// backfilled.
const (
	// colCycleCutoffAt is the FIXED cutoff of the cycle in progress; unset means
	// no cycle is open. Fixing it is what stops a long cycle from chasing the
	// clock: every page of one cycle is judged against the same instant.
	colCycleCutoffAt = "cycle_cutoff_at"
	// colCatalogCursor is the opaque keyset cursor of the last catalog Page of
	// the open cycle. It is the store's own cursor, never a sort of our own.
	colCatalogCursor = "catalog_cursor"
	// colLastCompletedCutoffAt is the greatest cutoff whose cycle reached the end
	// of the catalog. It records a finished SWEEP, and nothing more: it is not a
	// claim that any source was completely enumerated.
	colLastCompletedCutoffAt = "last_completed_cutoff_at"
)

const (
	observationReceiptKind  model.Kind = "inventory.observation_receipt"
	observationMemberKind   model.Kind = "inventory.observation_member"
	observationConflictKind model.Kind = "inventory.observation_conflict"
	colReceiptKey                      = "receipt_key"
	colReceiptID                       = "receipt_id"
	colEventID                         = "event_id"
	colFactsHash                       = "facts_hash"
	colFacts                           = "facts"
	colDeliveries                      = "deliveries"
	colMemberCount                     = "member_count"
	colMemberOrdinal                   = "member_ordinal"
	colObservationKey                  = "observation_key"
	colSourceID                        = "source_id"
)

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
	colOccurredAt    = "occurred_at"
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
	if err := reg.Register(model.EntityDescriptor{
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
			// occurred_at is the instant the SOURCE says the fact happened, as the source
			// declared it — NULL when it declared none, never filled from our clock. It is
			// the other half of the planner decision A (#122): last_seen answers "when did this
			// platform last SEE it", this answers "when does the source say it happened",
			// and conflating them is what made last_seen unusable as either.
			//
			// NULLABLE IS LOAD-BEARING, not a style choice: sqlstore reconciles an existing
			// table by ALTER TABLE ADD COLUMN only for nullable fields and REFUSES a
			// non-nullable one (core/internal/store/sqlstore/schema.go:683-688), so this
			// FieldSpec IS the additive migration for deployments that already have the table.
			{Name: colOccurredAt, Kind: model.KindTimestamp, Nullable: true},
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
	}); err != nil {
		return err
	}
	if err := registerFreshnessSweepSchema(reg); err != nil {
		return err
	}
	return registerProvenanceSchema(reg)
}

// registerFreshnessSweepSchema declares the sweep's durable progress.
//
// It is NOT audited, for the same reason the catalog entry is not: this is
// high-frequency automated bookkeeping about where a background pass got to, not
// a security-sensitive mutation. It carries no source, family, selector,
// revision or projection status — those belong to the later per-source result
// work, and putting them here would turn a freshness cursor into a second
// ledger.
//
// The unique index leads with tenant_id and contains nothing else, so
// "at most one progress row per tenant" is enforced by the database rather than
// by every caller remembering it; a concurrent first turn loses the race as a
// VISIBLE conflict and the next turn reopens the winning row.
//
// Every field is nullable on purpose. It is what lets the store's strictly
// additive reconciler bring an existing deployment forward (a non-nullable
// column would be refused, core/internal/store/sqlstore/schema.go:683-688), and
// it is what lets the row be created lazily and empty on a tenant's first turn
// instead of demanding a backfill over the whole directory.
func registerFreshnessSweepSchema(reg store.ExtensionRegistry) error {
	return reg.Register(model.EntityDescriptor{
		Kind:  freshnessSweepKind,
		Table: freshnessSweepTable,
		Fields: []model.FieldSpec{
			{Name: colCycleCutoffAt, Kind: model.KindTimestamp, Nullable: true},
			{Name: colCatalogCursor, Kind: model.KindText, Nullable: true},
			{Name: colLastCompletedCutoffAt, Kind: model.KindTimestamp, Nullable: true},
		},
		Indexes: []model.IndexSpec{{
			Name:    "inventory_freshness_sweep_uniq",
			Columns: []string{model.ColTenantID},
			Unique:  true,
		}},
	})
}

// These are additive, module-owned evidence projections. No columns are added
// to core or the legacy catalog, and no workspace lineage/authority is declared.
// Old writers simply leave this evidence absent. C1 has no receipt retention;
// any later retention policy must explicitly bound its replay guarantee.
func registerProvenanceSchema(reg store.ExtensionRegistry) error {
	for _, d := range []model.EntityDescriptor{
		{Kind: observationReceiptKind, Table: "inventory_observation_receipt", Fields: []model.FieldSpec{
			{Name: colReceiptKey, Kind: model.KindText},
			{Name: colEventID, Kind: model.KindText, Nullable: true},
			{Name: colFactsHash, Kind: model.KindText},
			{Name: colFacts, Kind: model.KindJSON},
			{Name: colFirstSeen, Kind: model.KindTimestamp},
			{Name: colLastSeen, Kind: model.KindTimestamp},
			{Name: colDeliveries, Kind: model.KindInt},
			{Name: colMemberCount, Kind: model.KindInt},
		}, Indexes: []model.IndexSpec{{Name: "inventory_receipt_uniq", Columns: []string{model.ColTenantID, colReceiptKey}, Unique: true}}},
		{Kind: observationMemberKind, Table: "inventory_observation_member", Fields: []model.FieldSpec{
			{Name: colReceiptID, Kind: model.KindUUID, Indexed: true},
			{Name: colMemberOrdinal, Kind: model.KindInt},
			{Name: colObservationKey, Kind: model.KindText, Nullable: true, Indexed: true},
			{Name: colSourceID, Kind: model.KindText, Nullable: true, Indexed: true},
			{Name: colEntityKind, Kind: model.KindText},
			{Name: colEntityID, Kind: model.KindUUID},
			{Name: colFacts, Kind: model.KindJSON},
		}, Indexes: []model.IndexSpec{
			{Name: "inventory_member_uniq", Columns: []string{model.ColTenantID, colReceiptID, colMemberOrdinal}, Unique: true},
			// C3 read index: the observation history of ONE entity is a DISTINCT
			// projection of receipt_id under (tenant, entity_kind, entity_id), ordered and
			// keyset-anchored on receipt_id (provenance_read.go). Leading with the two
			// equality columns and ending with the projected column lets both engines
			// answer that page from the index without visiting the members of every other
			// entity the tenant ever observed. Additive: no row, column or C1 semantics
			// change; the reconciler adds it with CREATE INDEX IF NOT EXISTS on reopen.
			{Name: "inventory_member_entity_receipt", Columns: []string{model.ColTenantID, colEntityKind, colEntityID, colReceiptID}},
		}},
		{Kind: observationConflictKind, Table: "inventory_observation_conflict", Fields: []model.FieldSpec{
			{Name: colReceiptID, Kind: model.KindUUID},
			{Name: colFactsHash, Kind: model.KindText},
			{Name: colFacts, Kind: model.KindJSON},
			{Name: colFirstSeen, Kind: model.KindTimestamp},
			{Name: colLastSeen, Kind: model.KindTimestamp},
			{Name: colDeliveries, Kind: model.KindInt},
		}, Indexes: []model.IndexSpec{
			{Name: "inventory_conflict_uniq", Columns: []string{model.ColTenantID, colReceiptID, colFactsHash}, Unique: true},
			// C3 read index: "does at least one retained conflicting variant exist for
			// this receipt" is a Limit-1 List by receipt_id in the default id order. The
			// unique index above ends in facts_hash, so it can find the rows but not
			// hand them back in id order; this one does. Additive, like the member index.
			{Name: "inventory_conflict_receipt_page", Columns: []string{model.ColTenantID, colReceiptID, model.ColID}},
		}},
	} {
		if err := reg.Register(d); err != nil {
			return err
		}
	}
	return nil
}
