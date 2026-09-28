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

// Principal declarations of the module's text, JSON and UUID columns
// (core/model/principal_decl.go). Every None cites the writer or reader lines
// that show the value names no account.
var (
	// pdeclObservedRef is a connector's natural reference for an observed entity.
	// For an identity origin it is the source system's own reference for a person
	// or credential (materialize.go:77-79), so it is matched against every account
	// alias as evidence of what was observed; no reader grants anything from it.
	pdeclObservedRef = model.Scan(model.ClassEvidence)
	// pdeclEntityID is the id of the core entity a catalog entry overlays; for the
	// identity kind it is a row of the identity roster (entities.go:77-88).
	pdeclEntityID = model.Ref(model.EncodeIdentity, model.ClassEvidence)

	// Values that name no account.
	pdeclNoneEntityKind     = model.None("an entity-kind label the materializer sets from the module's kind constants: materialize.go:54-66, materialize.go:114, materialize.go:291")
	pdeclNoneStatus         = model.None("the catalog liveness state, active or stale: catalog.go:58, catalog.go:323")
	pdeclNoneSignal         = model.None("a signal-source label naming the collector class: sdk/model/enums.go:44-47, materialize.go:34-37, materialize.go:283")
	pdeclNoneHost           = model.None("the host an edge was observed on; no producer supplies one yet: refs.go:79-83")
	pdeclNoneCursor         = model.None("the store's opaque keyset cursor, stored and handed back unchanged: catalog.go:306-313, catalog.go:330")
	pdeclNoneReceiptKey     = model.None("a digest of the delivering event id, used as the replay key: provenance.go:121-126, provenance.go:134")
	pdeclNoneEventID        = model.None("the bus event id, compared only for replay identity: provenance.go:140, provenance.go:154")
	pdeclNoneFactsHash      = model.None("a SHA-256 digest of the stored facts text: provenance.go:119, provenance.go:142, provenance_read.go:374")
	pdeclNoneReceiptID      = model.None("the id of this module's own observation receipt row: provenance.go:187, provenance.go:217")
	pdeclNoneObservationKey = model.None("a digest of the source-qualified native reference: provenance.go:176-185, provenance_read.go:478")
	pdeclNoneSourceID       = model.None("the persistent id of a configured ingestion source's roster row: cmd/olivares/reconcile.go:112, provenance.go:175")
	pdeclNoneInstant        = model.None("a canonical instant text: provenance.go:72-77, provenance_read.go:466")

	// JSON string sets of signal sources and hosts (catalog.go:59-60, catalog.go:398-408).
	pdeclSignalSources = model.Nested([]string(nil), model.ClassEvidence, model.Leaf("[]", pdeclNoneSignal))
	pdeclHosts         = model.Nested([]string(nil), model.ClassEvidence, model.Leaf("[]", pdeclNoneHost))

	// pdeclFacts is the projected observation a receipt or a conflicting
	// redelivery stores (provenance.go:115, provenance.go:155, provenance.go:217).
	pdeclFacts = model.Nested(inventoryFactsV1{}, model.ClassEvidence,
		model.Leaf("type", model.None("the first-party observation type, one of two: provenance_read.go:383-394")),
		model.Leaf("source_label", model.None("the ingestion instance label, which identifies no user: sdk/event/event.go:76-78")),
		model.Leaf("registration_state", model.None("a closed registration state: provenance.go:86-92, provenance_read.go:395-404")),
		model.Leaf("registration.BindingRef", model.None("an approved provider-binding reference stamped by the host: modules/sessions/provider_source_admission.go:50")),
		model.Leaf("registration.SourceID", pdeclNoneSourceID),
		model.Leaf("registration.EnvironmentRef", model.None("the persistent execution-environment id of the node that applied the registration: cmd/olivares/reconcile.go:112")),
		model.Leaf("envelope_occurred_at", pdeclNoneInstant),
		model.Leaf("edge.OriginKind", model.None("the origin kind label the materializer switches on: materialize.go:54-68")),
		model.Leaf("edge.OriginRef", pdeclObservedRef),
		model.Leaf("edge.ResourceKind", model.None("the resource kind label the materializer switches on: materialize.go:86-89")),
		model.Leaf("edge.ResourceRef", pdeclObservedRef),
		model.Leaf("edge.ToolRef", model.None("a tool or operation name, materialized as a Tool: materialize.go:259-264")),
		model.Leaf("edge.Mode", model.None("a closed access mode: sdk/model/enums.go:35-42")),
		model.Leaf("edge.Signal", pdeclNoneSignal),
		model.Leaf("edge.Confidence", model.None("a closed confidence level: sdk/model/enums.go:140-146")),
		model.Leaf("edge.OccurredAt", pdeclNoneInstant),
		model.Leaf("cost.ProviderRef", model.None("a provider name, materialized as a Provider: materialize.go:288")),
		model.Leaf("cost.ModelRef", model.None("a model name, materialized as a Model: materialize.go:296")),
		model.Leaf("cost.Gateway", model.None("the deployment surface label a model call was served through: sdk/model/enums.go:200-206")),
		model.Leaf("cost.OccurredAt", pdeclNoneInstant),
	)

	// pdeclMemberFacts is one committed member of a receipt (provenance.go:169, provenance.go:189).
	pdeclMemberFacts = model.Nested(observationMember{}, model.ClassEvidence,
		model.Leaf("kind", pdeclNoneEntityKind),
		model.Leaf("catalog_entity_id", pdeclEntityID),
		model.Leaf("native.namespace", model.None("a kind or gateway namespace label: provenance.go:277, materialize.go:73-76, materialize.go:291")),
		model.Leaf("native.parent", model.None("the MCP server, origin agent or provider the entity sits under: provenance.go:284, provenance.go:290, provenance.go:298, provenance.go:302, materialize.go:300")),
		model.Leaf("native.ref", pdeclObservedRef),
		model.Leaf("Name", pdeclObservedRef),
		model.Leaf("Ref", pdeclObservedRef),
		model.Leaf("Signal", pdeclNoneSignal),
		model.Leaf("Host", pdeclNoneHost),
		model.Leaf("occurred_at", pdeclNoneInstant),
	)
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
			{Name: colEntityKind, Kind: model.KindText, Indexed: true, Principal: pdeclNoneEntityKind},
			{Name: colEntityID, Kind: model.KindUUID, Principal: pdeclEntityID},
			{Name: colName, Kind: model.KindText, Principal: pdeclObservedRef},
			{Name: colRef, Kind: model.KindText, Nullable: true, Principal: pdeclObservedRef},
			{Name: colStatus, Kind: model.KindText, Indexed: true, Principal: pdeclNoneStatus},
			{Name: colSignalSources, Kind: model.KindJSON, Nullable: true, Principal: pdeclSignalSources},
			{Name: colHosts, Kind: model.KindJSON, Nullable: true, Principal: pdeclHosts},
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
			// non-nullable one (core/internal/store/sqlstore/schema.go:688-693), so this
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
	if err := registerProvenanceSchema(reg); err != nil {
		return err
	}
	return registerCoverageSchema(reg)
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
// column would be refused, core/internal/store/sqlstore/schema.go:688-693), and
// it is what lets the row be created lazily and empty on a tenant's first turn
// instead of demanding a backfill over the whole directory.
func registerFreshnessSweepSchema(reg store.ExtensionRegistry) error {
	return reg.Register(model.EntityDescriptor{
		Kind:  freshnessSweepKind,
		Table: freshnessSweepTable,
		Fields: []model.FieldSpec{
			{Name: colCycleCutoffAt, Kind: model.KindTimestamp, Nullable: true},
			{Name: colCatalogCursor, Kind: model.KindText, Nullable: true, Principal: pdeclNoneCursor},
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
			{Name: colReceiptKey, Kind: model.KindText, Principal: pdeclNoneReceiptKey},
			{Name: colEventID, Kind: model.KindText, Nullable: true, Principal: pdeclNoneEventID},
			{Name: colFactsHash, Kind: model.KindText, Principal: pdeclNoneFactsHash},
			{Name: colFacts, Kind: model.KindJSON, Principal: pdeclFacts},
			{Name: colFirstSeen, Kind: model.KindTimestamp},
			{Name: colLastSeen, Kind: model.KindTimestamp},
			{Name: colDeliveries, Kind: model.KindInt},
			{Name: colMemberCount, Kind: model.KindInt},
			{Name: colCollectionLink, Kind: model.KindJSON, Nullable: true, Principal: pdeclCollectionLink},
		}, Indexes: []model.IndexSpec{{Name: "inventory_receipt_uniq", Columns: []string{model.ColTenantID, colReceiptKey}, Unique: true}}},
		{Kind: observationMemberKind, Table: "inventory_observation_member", Fields: []model.FieldSpec{
			{Name: colReceiptID, Kind: model.KindUUID, Indexed: true, Principal: pdeclNoneReceiptID},
			{Name: colMemberOrdinal, Kind: model.KindInt},
			{Name: colObservationKey, Kind: model.KindText, Nullable: true, Indexed: true, Principal: pdeclNoneObservationKey},
			{Name: colSourceID, Kind: model.KindText, Nullable: true, Indexed: true, Principal: pdeclNoneSourceID},
			{Name: colEntityKind, Kind: model.KindText, Principal: pdeclNoneEntityKind},
			{Name: colEntityID, Kind: model.KindUUID, Principal: pdeclEntityID},
			{Name: colFacts, Kind: model.KindJSON, Principal: pdeclMemberFacts},
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
			{Name: colReceiptID, Kind: model.KindUUID, Principal: pdeclNoneReceiptID},
			{Name: colFactsHash, Kind: model.KindText, Principal: pdeclNoneFactsHash},
			{Name: colFacts, Kind: model.KindJSON, Principal: pdeclFacts},
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
