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

// workspaceRepo is the typed Workspace repository plus SetParent: departments
// as nodes of the organization tree (core v25). It follows resource_tree.go:
// Create derives the materialized path from ParentID, SetParent rewrites it
// across the subtree, and Update preserves both, so parent_id and path never
// desync. The tenant-pinned scope makes another organization's workspace
// ErrNotFound as a node and as a parent.
type workspaceRepo struct {
	*typedRepo[model.Workspace] // promoted Get/List/Lock and g
}

// The default-slug wrapper locks rows through the inner repository.
var _ store.RowLocker[model.Workspace] = (*workspaceRepo)(nil)

func newWorkspaceRepo(g *genericRepo) store.WorkspaceRepo {
	return &workspaceRepo{&typedRepo[model.Workspace]{g: g, codec: workspaceCodec}}
}

// Create inserts a workspace under ws.ParentID (zero = root). A set parent must
// exist in the same tenant, else ErrNotFound; a parent written before v25 has
// its root path healed first.
func (r *workspaceRepo) Create(ctx context.Context, ws model.Workspace) (model.Workspace, error) {
	if r.g.readOnly {
		return model.Workspace{}, store.ErrReadOnly
	}
	parentPath := ""
	if !ws.ParentID.IsZero() {
		parent, err := r.Get(ctx, ws.ParentID)
		if err != nil {
			return model.Workspace{}, err
		}
		if parentPath, err = r.g.healTreePath(ctx, parent.ID, parent.Path); err != nil {
			return model.Workspace{}, err
		}
	}
	id := model.NewID()
	ws.Path = parentPath + "/" + id.String()
	rec, err := workspaceCodec.Encode(ws)
	if err != nil {
		return model.Workspace{}, err
	}
	full, err := r.g.CreateWithID(ctx, id, rec)
	if err != nil {
		return model.Workspace{}, err
	}
	base, err := baseFromRecord(full)
	if err != nil {
		return model.Workspace{}, err
	}
	return workspaceCodec.Decode(base, full)
}

// Update changes a workspace but keeps its tree position: parent_id and path
// are forced back to the stored row's, so the tree changes only through
// SetParent.
func (r *workspaceRepo) Update(ctx context.Context, ws model.Workspace) (model.Workspace, error) {
	if r.g.readOnly {
		return model.Workspace{}, store.ErrReadOnly
	}
	current, err := r.Get(ctx, ws.ID)
	if err != nil {
		return model.Workspace{}, err
	}
	ws.ParentID, ws.Path = current.ParentID, current.Path
	return r.typedRepo.Update(ctx, ws)
}

// Delete refuses a workspace that still has child workspaces, as rmdir refuses
// a non-empty directory: removing it would leave their parent_id dangling.
func (r *workspaceRepo) Delete(ctx context.Context, id model.ID) error {
	children, _, err := r.List(ctx, model.Query{
		Filters: []model.Filter{{Column: "parent_id", Op: model.OpEq, Value: id.String()}},
		Limit:   1,
	})
	if err != nil {
		return err
	}
	if len(children) > 0 {
		return fmt.Errorf("%w: workspace %s still has child workspaces", store.ErrConflict, id)
	}
	return r.typedRepo.Delete(ctx, id)
}

// SetParent places node under parent (zero = root) and rewrites the subtree's
// paths in one step. It refuses a parent that is node itself or one of its
// descendants (ErrWorkspaceCycle) and is optimistic-concurrency checked on
// node's version. Like resource Move it poisons the envelope on failure, so a
// half-applied rewrite can never commit. Mutate's exclusive per-tenant lineage
// lock serializes tree writers, so the cycle check reads the committed tree.
func (r *workspaceRepo) SetParent(ctx context.Context, node, parent model.ID) (_ model.Workspace, retErr error) {
	defer func() {
		if r.g.poison != nil {
			r.g.poison(retErr)
		}
	}()
	if r.g.readOnly {
		return model.Workspace{}, store.ErrReadOnly
	}
	cur, err := r.Get(ctx, node)
	if err != nil {
		return model.Workspace{}, err
	}
	// A row with no path (the default workspace, or a row written before v25)
	// has no parent either: it is a root.
	curPath := cur.Path
	if curPath == "" {
		curPath = "/" + cur.ID.String()
	}
	parentPath := ""
	if !parent.IsZero() {
		if parent == node {
			return model.Workspace{}, store.ErrWorkspaceCycle
		}
		p, err := r.Get(ctx, parent)
		if err != nil {
			return model.Workspace{}, err
		}
		if parentPath, err = r.g.healTreePath(ctx, p.ID, p.Path); err != nil {
			return model.Workspace{}, err
		}
		if treeMoveCycles(curPath, parentPath) {
			return model.Workspace{}, store.ErrWorkspaceCycle
		}
	}
	if err := r.g.moveTreeNode(ctx, cur.ID, parent, cur.Version, curPath, parentPath+"/"+cur.ID.String()); err != nil {
		return model.Workspace{}, err
	}
	return r.Get(ctx, node)
}
