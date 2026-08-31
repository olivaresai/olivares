// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package model

import "strings"

// Kind is a globally unique entity kind, namespaced "<namespace>.<entity>"
// (e.g. "core.agent", "rrw.access_edge"). The segment before the first dot is
// the owning module's namespace; the engine reserves the "core" namespace.
type Kind string

// CoreNamespace is the reserved namespace for engine-owned entities.
const CoreNamespace = "core"

// Namespace returns the segment before the first dot, or "" if malformed.
func (k Kind) Namespace() string {
	s := string(k)
	if i := strings.IndexByte(s, '.'); i > 0 {
		return s[:i]
	}
	return ""
}

// Name returns the segment after the first dot, or "" if malformed.
func (k Kind) Name() string {
	s := string(k)
	if i := strings.IndexByte(s, '.'); i >= 0 && i+1 < len(s) {
		return s[i+1:]
	}
	return ""
}

// Valid reports whether the kind is "<namespace>.<entity>" with both segments
// being lowercase identifiers ([a-z][a-z0-9_]*).
func (k Kind) Valid() bool {
	ns, name := k.Namespace(), k.Name()
	return isIdent(ns) && isIdent(name)
}

func isIdent(s string) bool {
	if s == "" || s[0] < 'a' || s[0] > 'z' {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c != '_' && (c < 'a' || c > 'z') && (c < '0' || c > '9') {
			return false
		}
	}
	return true
}

// The base column names injected by the engine on every entity table. They are
// shared by the codec, the SQL generator, the migrations and the validators so
// all four agree on one spelling.
const (
	// ColID is the primary-key column.
	ColID = "id"
	// ColTenantID is the isolation column present on every entity.
	ColTenantID = "tenant_id"
	// ColCreatedAt is the immutable creation timestamp.
	ColCreatedAt = "created_at"
	// ColUpdatedAt is the last-modification timestamp.
	ColUpdatedAt = "updated_at"
	// ColVersion is the optimistic-concurrency counter.
	ColVersion = "version"
	// ColDeletedAt is the soft-delete tombstone (present only when SoftDelete).
	ColDeletedAt = "deleted_at"
)

// reservedColumns are the base columns a module descriptor must NOT declare;
// the engine injects them.
var reservedColumns = map[string]bool{
	ColID: true, ColTenantID: true, ColCreatedAt: true,
	ColUpdatedAt: true, ColVersion: true, ColDeletedAt: true,
}

// IsReservedColumn reports whether name is an engine-injected base column.
func IsReservedColumn(name string) bool { return reservedColumns[name] }

// FieldSpec declares one entity-specific column. The base columns are injected
// by the engine and must not appear here.
type FieldSpec struct {
	// Name is the column name (lowercase identifier).
	Name string
	// Kind is the portable column type.
	Kind SQLKind
	// Nullable allows NULL in the column.
	Nullable bool
	// Indexed requests a secondary index on (tenant_id, Name).
	Indexed bool
	// Redact DECLARES that a value is sensitive and must never be persisted raw
	// (minimal-data, docs/SECURITY-HARDENING.md). Only meaningful for KindText/KindBytes (the
	// registry rejects it elsewhere).
	//
	// ENFORCED ON THE WRITE PATH (2026-06-04): the sqlstore generic repo
	// replaces a Redact field's value with a one-way SHA-256 digest before INSERT/
	// UPDATE (sqlstore.redactField) — a stable token that supports dedup but cannot
	// disclose the original. This is the engine-level backstop: even if a connector
	// forgets to scrub, the raw value never lands. Redact therefore means "store
	// ONLY a hash". A module that needs a usable, partially-scrubbed value (e.g. a
	// path with the secret removed) must scrub it in its handler and NOT set Redact.
	Redact bool
}

// WorkspaceLineageEncoding says what a lineage column's value CONTAINS, because
// the column type alone cannot say it: both a core workspace id and a provider's
// own workspace handle are stored as TEXT.
type WorkspaceLineageEncoding string

const (
	// WorkspaceLineageID means the column holds the canonical string form of a
	// core Workspace id (model.ID), whether the column is KindUUID or KindText.
	WorkspaceLineageID WorkspaceLineageEncoding = "id"
	// WorkspaceLineageSlug means the column holds the workspace's slug.
	WorkspaceLineageSlug WorkspaceLineageEncoding = "slug"
)

// WorkspaceUnsetSemantics says what an ABSENT lineage value means. It is a
// separate axis from the encoding because "no value" is genuinely ambiguous in
// this data model and guessing either way is wrong in one direction.
type WorkspaceUnsetSemantics string

const (
	// WorkspaceUnsetMeansDefault: an absent value belongs to the tenant's DEFAULT
	// workspace — the FASE X back-compat resolution (scoping.go) that keeps rows
	// written before workspaces existed reachable by the operator confined to the
	// default workspace.
	WorkspaceUnsetMeansDefault WorkspaceUnsetSemantics = "default"
	// WorkspaceUnsetHidden: an absent value proves nothing, so the row is hidden
	// from every confined principal, including one confined to the default.
	WorkspaceUnsetHidden WorkspaceUnsetSemantics = "hidden"
)

// WorkspaceLineageSpec DECLARES which column carries an entity's workspace
// lineage (FASE X /) and how to read it. It is a declaration and not an
// inference on purpose: a column NAMED workspace_id or workspace_ref does not
// prove it holds a core workspace — in this repo sessions.run.workspace_ref is a
// host filesystem root and models/finops workspace_ref is the PROVIDER's
// workspace, neither of which is the authorization axis. Row-level confinement
// (store.ConfineWorkspace) filters an entity only through this spec, and denies
// an entity that declares none rather than assuming it is tenant-wide.
type WorkspaceLineageSpec struct {
	// Column is the entity column carrying the lineage. Empty means the entity
	// declares no lineage.
	Column string
	// Encoding says what the column's value contains.
	Encoding WorkspaceLineageEncoding
	// Unset says what an absent value means.
	Unset WorkspaceUnsetSemantics
}

// AuthorizationLeaseFenceSpec declares the exact row fields needed by the
// payload-free authority locker to pin and OCC-touch a leased authorization
// fact. The store compares a server-observed subject, fence and deadline and
// requires StateColumn to equal ActiveValue; none of those row fields are
// returned through the capability.
//
// This is deliberately descriptor opt-in. Merely having similarly named
// columns never makes an entity reachable through AuthoritySnapshotLocker.
type AuthorizationLeaseFenceSpec struct {
	SubjectColumn  string
	FenceColumn    string
	StateColumn    string
	ActiveValue    string
	DeadlineColumn string
}

// Declared reports whether any part of the lease/fence touch contract is set.
// Registry validation requires a declared contract to be complete.
func (s AuthorizationLeaseFenceSpec) Declared() bool {
	return s.SubjectColumn != "" || s.FenceColumn != "" || s.StateColumn != "" ||
		s.ActiveValue != "" || s.DeadlineColumn != ""
}

// Declared reports whether the entity declares workspace lineage.
func (s WorkspaceLineageSpec) Declared() bool { return s.Column != "" }

// ValidEncoding reports whether e is a known encoding.
func (e WorkspaceLineageEncoding) ValidEncoding() bool {
	return e == WorkspaceLineageID || e == WorkspaceLineageSlug
}

// ValidUnset reports whether u is a known unset semantics.
func (u WorkspaceUnsetSemantics) ValidUnset() bool {
	return u == WorkspaceUnsetMeansDefault || u == WorkspaceUnsetHidden
}

// IndexSpec declares a secondary index on an entity table. The engine prefixes
// tenant_id implicitly is NOT assumed; declare it explicitly when wanted.
type IndexSpec struct {
	// Name is the index name (engine prefixes the table name).
	Name string
	// Columns are the indexed columns in order.
	Columns []string
	// Unique makes the index a uniqueness constraint.
	Unique bool
}

// EntityDescriptor is the full schema declaration for one entity kind. The
// engine uses it to generate CRUD SQL for every backend, to generate and
// validate the table for module entities, and to attach the unconditional
// multi-tenant, audit and append-only guards. A descriptor never names an
// engine-specific type and never declares a base column: isolation is not
// something a descriptor can opt out of.
type EntityDescriptor struct {
	// Kind is the unique, namespaced entity kind.
	Kind Kind
	// Table is the physical table name. For module entities it must be
	// "<namespace>_<snake>"; core tables use bare names.
	Table string
	// Fields are the entity-specific columns (base columns are injected).
	Fields []FieldSpec
	// Indexes are secondary indexes.
	Indexes []IndexSpec
	// Checks are table-level CHECK constraint expressions (portable SQL boolean
	// expressions over this table's columns, e.g. "state IN ('a','b')"),
	// rendered identically by both dialects at table creation. They are
	// CORE-ONLY: module registration rejects them, because the expression is
	// interpolated verbatim into DDL. Applied only when the table is CREATED
	// (the v2 migration on a fresh database, or the whole-table reconcile
	// create); adding a Check to an ALREADY-EXISTING table's descriptor needs a
	// hand-authored migration — the strictly-additive reconcile never ALTERs
	// (the same honesty rule as adding a NOT NULL column).
	Checks []string
	// Audited emits an AuditEvent on every mutation, in the same transaction.
	Audited bool
	// AppendOnly attaches immutability guards (no UPDATE/DELETE). Mutually
	// exclusive with SoftDelete.
	AppendOnly bool
	// RetainOnTenantDrop keeps this entity's tenant rows when SystemScope.DropTenant
	// retires the tenant. Append-only entities are already retained by construction;
	// a mutable retained entity must additionally declare its exact
	// <table>_no_delete schema invariant for every supported engine, so this flag
	// cannot turn an ordinary deletable table into undeclared durable evidence.
	RetainOnTenantDrop bool
	// SoftDelete adds deleted_at and filters it from List by default.
	SoftDelete bool
	// AuthorizationFact explicitly allowlists this entity for the opaque
	// store.AuthoritySnapshotLocker capability. The capability exposes no row
	// payload; it only locks an exact id and compares its version. LockOrder must
	// be non-zero when enabled and establishes one global deadlock-safe order.
	AuthorizationFact      bool
	AuthorizationLockOrder uint16
	// AuthorizationLeaseFence opts this fact into a transaction-stamped no-op
	// OCC touch plus exact subject/fence/state/deadline validation. Its zero value
	// retains the ordinary opaque version-lock behavior.
	AuthorizationLeaseFence AuthorizationLeaseFenceSpec
	// WorkspaceLineage declares the column carrying this entity's workspace
	// lineage, for row-level confinement (B-03). Zero means "no lineage
	// declared": the entity stays fully usable for the engine and for a
	// tenant-wide principal, and is refused to a workspace-confined one.
	WorkspaceLineage WorkspaceLineageSpec
}

// EntityColumns returns the entity-specific column names in declaration order.
func (d EntityDescriptor) EntityColumns() []string {
	cols := make([]string, len(d.Fields))
	for i, f := range d.Fields {
		cols[i] = f.Name
	}
	return cols
}

// BaseColumns returns the engine-injected base columns in canonical order,
// including deleted_at when the entity is soft-deletable.
func (d EntityDescriptor) BaseColumns() []string {
	cols := []string{ColID, ColTenantID, ColCreatedAt, ColUpdatedAt, ColVersion}
	if d.SoftDelete {
		cols = append(cols, ColDeletedAt)
	}
	return cols
}

// AllColumns returns base columns followed by entity columns, in insert order.
func (d EntityDescriptor) AllColumns() []string {
	return append(d.BaseColumns(), d.EntityColumns()...)
}

// field returns the FieldSpec for an entity column, or false.
func (d EntityDescriptor) field(name string) (FieldSpec, bool) {
	for _, f := range d.Fields {
		if f.Name == name {
			return f, true
		}
	}
	return FieldSpec{}, false
}

// KindOfColumn returns the SQLKind for any column (base or entity) of the
// descriptor, so the SQL layer can build typed scan targets. The bool is false
// for an unknown column.
func (d EntityDescriptor) KindOfColumn(name string) (SQLKind, bool) {
	switch name {
	case ColID, ColTenantID:
		return KindUUID, true
	case ColCreatedAt, ColUpdatedAt, ColDeletedAt:
		return KindTimestamp, true
	case ColVersion:
		return KindInt, true
	}
	if f, ok := d.field(name); ok {
		return f.Kind, true
	}
	return 0, false
}

// NullableColumn reports whether a column may be NULL.
func (d EntityDescriptor) NullableColumn(name string) bool {
	if name == ColDeletedAt {
		return true
	}
	if f, ok := d.field(name); ok {
		return f.Nullable
	}
	return false
}

// BaseFields are the engine-managed columns embedded in every entity struct.
// The store stamps them; a caller's values for ID/TenantID/CreatedAt/Version
// are overwritten on Create.
type BaseFields struct {
	// ID is the entity primary key (UUIDv7), assigned by the store.
	ID ID
	// TenantID is the owning tenant, stamped from the scope (never the caller).
	TenantID TenantID
	// CreatedAt is set on Create and never changes.
	CreatedAt Timestamp
	// UpdatedAt is set on Create and bumped on every Update.
	UpdatedAt Timestamp
	// Version is the optimistic-concurrency counter (starts at 1).
	Version int64
	// DeletedAt is the soft-delete tombstone; nil while live.
	DeletedAt *Timestamp
}

// Codec maps a typed entity to and from the engine-neutral Record. There is one
// hand-written Codec per core entity (no runtime reflection on the hot path).
// Base returns a pointer to the entity's embedded BaseFields so the store can
// stamp/read id, tenant and timestamps without reflection.
type Codec[T any] struct {
	// Base returns a pointer to the entity's embedded base fields.
	Base func(*T) *BaseFields
	// Encode returns the entity-specific columns (not base columns). It returns
	// an error if a field cannot be serialized (e.g. non-JSON metadata).
	Encode func(T) (Record, error)
	// Decode reconstructs the entity from its stamped base fields and the full
	// row record.
	Decode func(BaseFields, Record) (T, error)
}
