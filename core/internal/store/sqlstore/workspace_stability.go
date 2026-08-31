// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"fmt"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// stableWorkspaceRepo enforces the model-level promise that the reserved
// "default" slug is assigned once and never reassigned. An Agent with an empty
// WorkspaceID uses that row ID as part of its durable directory principal, so
// allowing default A→B would change an existing binding's identity after a
// retirement tombstone was sealed.
//
// This is intentionally not a new v7 directory-writer source: ordinary
// non-default workspace lifecycle does not alter directory identity. The
// wrapper narrows only the reserved assignment and leaves System provisioning
// and the explicit legacy-backfill repair on their raw engine-owned seams.
type stableWorkspaceRepo struct {
	inner      store.Repository[model.Workspace]
	tracker    *directoryWriteTracker
	readOnly   bool
	initialize func(context.Context, model.Workspace) error
}

func newStableWorkspaceRepo(
	inner store.Repository[model.Workspace],
	tracker *directoryWriteTracker,
	readOnly bool,
	initialize func(context.Context, model.Workspace) error,
) store.Repository[model.Workspace] {
	return &stableWorkspaceRepo{
		inner: inner, tracker: tracker, readOnly: readOnly, initialize: initialize,
	}
}

func (r *stableWorkspaceRepo) Get(
	ctx context.Context,
	id model.ID,
) (model.Workspace, error) {
	return r.inner.Get(ctx, id)
}

func (r *stableWorkspaceRepo) List(
	ctx context.Context,
	query model.Query,
) ([]model.Workspace, model.Page, error) {
	return r.inner.List(ctx, query)
}

func (r *stableWorkspaceRepo) Lock(
	ctx context.Context,
	id model.ID,
) (model.Workspace, error) {
	locker, ok := r.inner.(store.RowLocker[model.Workspace])
	if !ok {
		return model.Workspace{}, fmt.Errorf("workspace repository does not implement row locking")
	}
	return locker.Lock(ctx, id)
}

func (r *stableWorkspaceRepo) Create(
	ctx context.Context,
	in model.Workspace,
) (_ model.Workspace, retErr error) {
	if r.readOnly {
		return r.inner.Create(ctx, in)
	}
	defer func() { r.poison(retErr) }()
	if in.Slug == model.DefaultWorkspaceSlug {
		return model.Workspace{}, immutableDefaultWorkspaceError()
	}
	created, err := r.inner.Create(ctx, in)
	if err != nil {
		return model.Workspace{}, err
	}
	if r.initialize != nil {
		if err := r.initialize(ctx, created); err != nil {
			return model.Workspace{}, fmt.Errorf(
				"initialize workspace %s: %w", created.ID, err,
			)
		}
	}
	return created, nil
}

func (r *stableWorkspaceRepo) Update(
	ctx context.Context,
	in model.Workspace,
) (_ model.Workspace, retErr error) {
	if r.readOnly {
		return r.inner.Update(ctx, in)
	}
	defer func() { r.poison(retErr) }()
	old, err := r.inner.Get(ctx, in.ID)
	if err != nil {
		return model.Workspace{}, err
	}
	if (old.Slug == model.DefaultWorkspaceSlug) !=
		(in.Slug == model.DefaultWorkspaceSlug) {
		return model.Workspace{}, immutableDefaultWorkspaceError()
	}
	return r.inner.Update(ctx, in)
}

func (r *stableWorkspaceRepo) Delete(
	ctx context.Context,
	id model.ID,
) (retErr error) {
	if r.readOnly {
		return r.inner.Delete(ctx, id)
	}
	defer func() { r.poison(retErr) }()
	old, err := r.inner.Get(ctx, id)
	if err != nil {
		return err
	}
	if old.Slug == model.DefaultWorkspaceSlug {
		return immutableDefaultWorkspaceError()
	}
	return r.inner.Delete(ctx, id)
}

func (r *stableWorkspaceRepo) poison(err error) {
	if r.tracker != nil {
		r.tracker.poison(err)
	}
}

func immutableDefaultWorkspaceError() error {
	return fmt.Errorf("%w: reserved default workspace assignment is immutable", store.ErrConflict)
}
