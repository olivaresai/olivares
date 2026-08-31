// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package catalog

import (
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// Owned entity kinds and their physical tables.
const (
	entryKind     model.Kind = "catalog.entry"
	entryTable               = "catalog_entry"
	instanceKind  model.Kind = "catalog.instance"
	instanceTable            = "catalog_instance"
)

// Catalog entry kinds (what the entry curates).
const (
	kindAgent    = "agent"
	kindMCP      = "mcp"
	kindSkill    = "skill"
	kindTemplate = "template"
	// kindModel (G15) curates an admitted MODEL: a signed, versioned model
	// artifact published into the approved catalog (XIV). A model entry's spec
	// references the model_version it curates (spec.version_ref); approving it is
	// deny-closed under the tenant's signed-model-admission policy (modeladmission.go).
	kindModel = "model"
	// kindConnector (S142, EXT-3) curates a VERIFIED third-party connector — a
	// released, signed connector plugin artifact. A connector entry's spec records
	// the artifact it curates (artifact_digest sha256, release/OCI ref, publisher,
	// descriptor name); approving it is deny-closed under the tenant's
	// connector-admission policy (connectoradmission.go).
	kindConnector = "connector"
)

// Catalog entry lifecycle states. A registry artifact is created as a draft, may
// be submitted for review (pending), approved (frozen + hashed + optionally
// signed) and later deprecated. Only a draft is mutable.
const (
	statusDraft      = "draft"
	statusPending    = "pending"
	statusApproved   = "approved"
	statusDeprecated = "deprecated"
)

// Instance lifecycle states. A self-service instantiation is requested; the
// approval DECISION and provisioning belong to governance and deployment
// — this module records and transitions the request.
const (
	instRequested = "requested"
	instApproved  = "approved"
	instRejected  = "rejected"
	instActive    = "active"
)

// entry columns.
const (
	colEntryKind   = "entry_kind"
	colName        = "name"
	colSlug        = "slug"
	colVersion     = "semver" // the entry's semantic version ("version" is a reserved base column)
	colStatus      = "status"
	colSummary     = "summary"
	colSpec        = "spec"
	colOwnerRef    = "owner_ref"
	colContentHash = "content_hash"
	colSignature   = "signature"
	colSigAlg      = "sig_alg"
	colSignedBy    = "signed_by"
	colApprovedBy  = "approved_by"
	colApprovedAt  = "approved_at"
)

// instance columns.
const (
	colEntryID      = "entry_id"
	colEntrySlug    = "entry_slug"
	colEntryVersion = "entry_version"
	colInstName     = "name"
	colTargetRef    = "target_ref"
	colInstStatus   = "status"
	colRequestedBy  = "requested_by"
	colDecidedBy    = "decided_by"
	colNote         = "note"
)

// RegisterSchema declares the module's owned entities. It satisfies the
// engine-side runtime.SchemaProvider seam (structural — no runtime import) and is
// called once, at store construction, before any Scope exists (S02 §7 /).
// The engine creates the tables, injects the base columns and attaches the tenant
// guards; a module cannot opt out of isolation.
//
// The registry keys an entry uniquely by (kind, slug, version): each version is
// its own immutable artifact, so publishing a new version is a new row and
// approval/signing happen per version (README.md). The unique index leads with
// tenant_id so it cannot couple tenants or leak existence.
//
// Neither entity is descriptor-audited: the descriptor's auto-audit attributes a
// mutation to the SYSTEM actor, which would defeat the self-audit purpose ("who
// approved which entry"). Instead each privileged handler appends a semantic audit
// attributed to the real principal in the same transaction (entries.go,
// instances.go), exactly as module X's key governance does.
func (m *Module) RegisterSchema(reg store.ExtensionRegistry) error {
	if err := reg.Register(model.EntityDescriptor{
		Kind:  entryKind,
		Table: entryTable,
		Fields: []model.FieldSpec{
			{Name: colEntryKind, Kind: model.KindText, Indexed: true},
			{Name: colName, Kind: model.KindText},
			{Name: colSlug, Kind: model.KindText, Indexed: true},
			{Name: colVersion, Kind: model.KindText},
			{Name: colStatus, Kind: model.KindText, Indexed: true},
			{Name: colSummary, Kind: model.KindText, Nullable: true},
			{Name: colSpec, Kind: model.KindJSON, Nullable: true},
			{Name: colOwnerRef, Kind: model.KindText, Nullable: true},
			{Name: colContentHash, Kind: model.KindText, Nullable: true},
			{Name: colSignature, Kind: model.KindText, Nullable: true},
			{Name: colSigAlg, Kind: model.KindText, Nullable: true},
			{Name: colSignedBy, Kind: model.KindText, Nullable: true},
			{Name: colApprovedBy, Kind: model.KindText, Nullable: true},
			{Name: colApprovedAt, Kind: model.KindTimestamp, Nullable: true},
		},
		Indexes: []model.IndexSpec{{
			Name:    "catalog_entry_uniq",
			Columns: []string{model.ColTenantID, colEntryKind, colSlug, colVersion},
			Unique:  true,
		}},
	}); err != nil {
		return err
	}

	// signed MCP-entry admission (policy + verdicts; mcpadmission.go).
	if err := registerMCPAdmissionSchemas(reg); err != nil {
		return err
	}

	// S142: signed CONNECTOR-entry admission (policy + verdicts; connectoradmission.go).
	// Own kinds/tables, same shape as the MCP pair — evidence is counted by kind.
	if err := registerConnectorAdmissionSchemas(reg); err != nil {
		return err
	}

	return reg.Register(model.EntityDescriptor{
		Kind:  instanceKind,
		Table: instanceTable,
		Fields: []model.FieldSpec{
			{Name: colEntryID, Kind: model.KindUUID, Indexed: true},
			{Name: colEntryKind, Kind: model.KindText},
			{Name: colEntrySlug, Kind: model.KindText},
			{Name: colEntryVersion, Kind: model.KindText},
			{Name: colInstName, Kind: model.KindText},
			{Name: colTargetRef, Kind: model.KindText, Nullable: true},
			{Name: colInstStatus, Kind: model.KindText, Indexed: true},
			{Name: colRequestedBy, Kind: model.KindText, Nullable: true},
			{Name: colDecidedBy, Kind: model.KindText, Nullable: true},
			{Name: colNote, Kind: model.KindText, Nullable: true},
		},
		Indexes: []model.IndexSpec{{
			// One instance name per source entry. Unique index leads with tenant_id.
			Name:    "catalog_instance_uniq",
			Columns: []string{model.ColTenantID, colEntryID, colInstName},
			Unique:  true,
		}},
	})
}
