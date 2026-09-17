// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package store

import (
	"context"

	"github.com/olivaresai/olivares/core/model"
)

// confinedReadOnlyGenericRepo is what a workspace-confined Scope.Ext returns for
// an entity whose descriptor sets WorkspaceConfinedReadOnly.
//
// It is deliberately NOT built from confinedGenericRepo. That decorator's Update
// (and its transaction-stamped variant) does check the STORED row as well as the
// submitted one: checkStoredAndIncoming requires both lineages before either
// update delegate runs, so a foreign stored row is ErrNotFound and the raw writer
// does not run. This type does not rest on that guard. Embedding that decorator —
// or the raw repository — would promote a writable surface at all, together with
// TransactionStampedGenericRepo and RowLocker (whose SQLite Lock reserves the
// writer through an UPDATE), for an entity whose descriptor asked for none. So the
// raw handle is a NAMED private field, the reads are filtered here, and every
// required write refuses before anything is delegated.
type confinedReadOnlyGenericRepo struct {
	readOnlyRaw GenericRepo
	b           workspaceBoundary
	spec        model.WorkspaceLineageSpec
	kind        model.Kind
}

// confinedReadOnlyDistinctProjectingGenericRepo exists only when the raw
// repository actually implements DistinctProjector, so the confined repository
// is never assertable as a capability that can only fail later. The projection
// keeps the forced lineage predicate.
type confinedReadOnlyDistinctProjectingGenericRepo struct {
	confinedReadOnlyGenericRepo
	projector DistinctProjector
}

var (
	_ GenericRepo       = confinedReadOnlyGenericRepo{}
	_ GenericRepo       = confinedReadOnlyDistinctProjectingGenericRepo{}
	_ DistinctProjector = confinedReadOnlyDistinctProjectingGenericRepo{}
)

// newConfinedReadOnlyGenericRepo selects the read-only wrapper for raw. desc is
// the descriptor Ext already read, so a refused write never calls the raw handle
// even for its Descriptor.
func newConfinedReadOnlyGenericRepo(
	raw GenericRepo,
	b workspaceBoundary,
	desc model.EntityDescriptor,
) GenericRepo {
	reader := confinedReadOnlyGenericRepo{
		readOnlyRaw: raw,
		b:           b,
		spec:        desc.WorkspaceLineage,
		kind:        desc.Kind,
	}
	if projector, ok := raw.(DistinctProjector); ok {
		return confinedReadOnlyDistinctProjectingGenericRepo{
			confinedReadOnlyGenericRepo: reader,
			projector:                   projector,
		}
	}
	return reader
}

func (r confinedReadOnlyGenericRepo) Descriptor() model.EntityDescriptor {
	return r.readOnlyRaw.Descriptor()
}

func (r confinedReadOnlyGenericRepo) List(
	ctx context.Context,
	q model.Query,
) ([]model.Record, model.Page, error) {
	return r.readOnlyRaw.List(ctx, forceQuery(q, r.b.filterFor(r.spec)))
}

func (r confinedReadOnlyGenericRepo) Get(ctx context.Context, id model.ID) (model.Record, error) {
	rec, err := r.readOnlyRaw.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	inside, ok := r.b.owns(r.spec, rec.String(r.spec.Column))
	if !ok {
		// A lineage column holding something that is not a workspace is a fault
		// in the row, not a license to serve it.
		return nil, deniedWrite("lineage column " + r.spec.Column + " holds an unreadable value")
	}
	if !inside {
		return nil, ErrNotFound
	}
	return rec, nil
}

func (r confinedReadOnlyGenericRepo) Create(context.Context, model.Record) (model.Record, error) {
	return nil, r.refuseWrite("create")
}

func (r confinedReadOnlyGenericRepo) CreateWithID(
	context.Context,
	model.ID,
	model.Record,
) (model.Record, error) {
	return nil, r.refuseWrite("create with id")
}

func (r confinedReadOnlyGenericRepo) Update(context.Context, model.Record) (model.Record, error) {
	return nil, r.refuseWrite("update")
}

func (r confinedReadOnlyGenericRepo) Delete(context.Context, model.ID) error {
	return r.refuseWrite("delete")
}

func (r confinedReadOnlyGenericRepo) refuseWrite(op string) error {
	return deniedWrite(string(r.kind) + " is read-only under workspace confinement: " + op + " refused")
}

func (r confinedReadOnlyDistinctProjectingGenericRepo) ProjectDistinct(
	ctx context.Context,
	p DistinctProjection,
) (DistinctPage, error) {
	forced := forceQuery(model.Query{Filters: p.Filters}, r.b.filterFor(r.spec))
	p.Filters = forced.Filters
	return r.projector.ProjectDistinct(ctx, p)
}
