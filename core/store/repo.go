// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package store

import (
	"context"

	"github.com/olivaresai/olivares/core/model"
)

// ReadRepository is the read-only typed repository surface shared by mutable
// and fully deletable entities.
type ReadRepository[T any] interface {
	Get(ctx context.Context, id model.ID) (T, error)
	List(ctx context.Context, q model.Query) ([]T, model.Page, error)
}

// Repository is the typed CRUD contract for one core entity kind. No method
// accepts a tenant id: the owning Scope pins it. Create stamps the id, tenant,
// timestamps and version, overwriting any caller-supplied values; Update is
// checked for optimistic-concurrency conflicts; Delete is a soft delete for
// soft-deletable entities and a hard delete otherwise.
type Repository[T any] interface {
	ReadRepository[T]
	// Get returns the entity, or ErrNotFound if it is absent or owned by
	// another tenant.
	// Create inserts a new entity and returns it with stamped base fields.
	Create(ctx context.Context, v T) (T, error)
	// Update modifies an existing entity, returning ErrConflict on a version
	// mismatch and ErrNotFound if it is absent/other-tenant.
	Update(ctx context.Context, v T) (T, error)
	// Delete removes the entity (soft or hard per its descriptor). It is a no-op
	// returning ErrNotFound if the entity is absent/other-tenant.
	Delete(ctx context.Context, id model.ID) error
}

// MutableRepository is the exact public surface for an entity that may be
// created and changed but whose irreversible deletion belongs to a separate
// engine-owned ceremony. It deliberately does not embed Repository: concrete
// implementations returned through this interface must also omit Delete, so a
// caller cannot recover hard deletion with a type assertion.
type MutableRepository[T any] interface {
	ReadRepository[T]
	Create(ctx context.Context, v T) (T, error)
	Update(ctx context.Context, v T) (T, error)
}

// RowLocker is an OPTIONAL repository capability for authorization facts that
// must remain stable until the surrounding Mutate transaction commits. Lock
// returns the same row shape as Get while acquiring a database row lock on
// engines that support one. On SQLite, Mutate is the serialized writer unit and
// Lock reads from that transaction after the caller has acquired write
// authority. Calling Lock from a read-only View must return ErrReadOnly.
//
// Keeping this separate from Repository avoids pretending that every store or
// test fake can provide a cross-process fence. Security-sensitive callers must
// fail closed when the capability is absent or the lock cannot be obtained.
type RowLocker[T any] interface {
	Lock(context.Context, model.ID) (T, error)
}

// GenericRepo is the untyped repository for module-registered entities, reached
// via Scope.Ext. Values flow as model.Record so the engine needs no
// compile-time knowledge of a module's Go types. It is tenant-pinned exactly
// like a typed Repository.
type GenericRepo interface {
	// Descriptor returns the entity's schema declaration, so a caller that must
	// reason about the SHAPE of the rows — notably row-level workspace
	// confinement, which needs the declared workspace lineage (B-03) — can do
	// so without a second, drift-prone registry lookup. It returns a copy: the
	// descriptor is a declaration, never a handle to mutate.
	Descriptor() model.EntityDescriptor
	// Get returns the row, or ErrNotFound if absent/other-tenant.
	Get(ctx context.Context, id model.ID) (model.Record, error)
	// List returns a page of rows matching q.
	List(ctx context.Context, q model.Query) ([]model.Record, model.Page, error)
	// Create inserts a row (the engine stamps base columns) and returns it.
	Create(ctx context.Context, r model.Record) (model.Record, error)
	// CreateWithID inserts a row under the exact caller-supplied canonical UUIDv7.
	// The engine still stamps every other base column. It returns ErrInvalidID
	// before touching the database when id is zero, non-canonical or not UUIDv7.
	CreateWithID(ctx context.Context, id model.ID, r model.Record) (model.Record, error)
	// Update modifies a row (optimistic-concurrency checked) and returns it.
	Update(ctx context.Context, r model.Record) (model.Record, error)
	// Delete removes the row (soft or hard per its descriptor).
	Delete(ctx context.Context, id model.ID) error
}

// TransactionStampedGenericRepo is an OPTIONAL GenericRepo capability for
// planners that must bind domain effects to the authoritative DB time observed
// through TransactionClock in the same Scope. Its methods never accept a time
// from the caller: they reuse the most recent successful TransactionNow value
// observed by that transaction, or fail with ErrTransactionTimeNotObserved. Ordinary
// GenericRepo methods deliberately keep their established application-clock
// semantics for compatibility with existing modules.
type TransactionStampedGenericRepo interface {
	GenericRepo
	CreateAtTransactionTime(ctx context.Context, r model.Record) (model.Record, error)
	CreateWithIDAtTransactionTime(
		ctx context.Context,
		id model.ID,
		r model.Record,
	) (model.Record, error)
	UpdateAtTransactionTime(ctx context.Context, r model.Record) (model.Record, error)
}

// ResourceRepo is the typed Resource repository plus the tree operations that
// make a resource hierarchy (folders, FASE X /) efficient. The embedded
// Repository provides flat CRUD; CreateUnder/Children/Subtree/Move add the
// hierarchy. The materialized path is store-maintained: Create (and CreateUnder)
// set it from the parent, Move rewrites it across the subtree, and Update
// deliberately PRESERVES it — a caller restructures the tree only through Move,
// never by editing parent_id/path on an Update, so the path can never silently
// drift from the parent_id graph.
type ResourceRepo interface {
	Repository[model.Resource]
	// CreateUnder creates r as a child of parent (a zero parent is a tree root),
	// computing its materialized path from the parent's. It is sugar for setting
	// r.ParentID and calling Create. It returns ErrNotFound if parent is absent
	// or owned by another tenant.
	CreateUnder(ctx context.Context, parent model.ID, r model.Resource) (model.Resource, error)
	// Children lists the DIRECT children of parent (parent_id = parent). A zero
	// parent lists the tree roots (resources with no parent). Ordering and paging
	// follow q like any List.
	Children(ctx context.Context, parent model.ID, q model.Query) ([]model.Resource, model.Page, error)
	// Subtree lists root and ALL its descendants (root included), via a single
	// materialized-path prefix scan — the efficient alternative to a recursive
	// walk. It returns ErrNotFound if root is absent/other-tenant. q's filters
	// further restrict the subtree (e.g. by kind or workspace_id).
	Subtree(ctx context.Context, root model.ID, q model.Query) ([]model.Resource, model.Page, error)
	// Move reparents node under newParent (a zero newParent makes it a root),
	// rewriting node's materialized path and every descendant's in one atomic
	// step. It returns ErrNotFound if node or newParent is absent/other-tenant,
	// ErrResourceCycle if newParent is node itself or one of its descendants, and
	// ErrConflict if node changed concurrently (it is optimistic-concurrency
	// checked on the version read at the start of the move).
	Move(ctx context.Context, node, newParent model.ID) (model.Resource, error)
}

// AccessEdgeRepo is the typed repository for the differential AccessEdge entity
// plus the graph queries that make the R/RW map (module III) a view over the
// model rather than a separate schema (ARCHITECTURE.md, §6).
type AccessEdgeRepo interface {
	Repository[model.AccessEdge]
	// Neighbors returns the edges touching node in the given direction.
	Neighbors(ctx context.Context, node model.NodeRef, dir model.Direction) ([]model.AccessEdge, error)
	// Drift returns the least-privilege discrepancies (permitted != observed),
	// the killer feature of ARCHITECTURE.md.
	Drift(ctx context.Context, q model.Query) ([]model.PrivilegeDrift, error)
	// Upsert merges a connector observation into an existing edge by natural
	// key (bump last_seen, raise occurrence_count, OR observed) — an idempotent,
	// monotonic merge that does not take a version.
	Upsert(ctx context.Context, e model.AccessEdge) (model.AccessEdge, error)
}
