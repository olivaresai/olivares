// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"fmt"
	"strings"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// resourceRepo is the typed Resource repository plus the tree operations
// (folders/hierarchy, FASE X /). The embedded Repository supplies the flat
// Get/List/Delete; Create and Update are overridden to MAINTAIN the materialized
// path, and CreateUnder/Children/Subtree/Move add the hierarchy. The path
// ("/<root>/…/<self>") is store-managed end to end: Create derives it from the
// parent, Move rewrites it across the subtree, and Update preserves it — so a
// caller can never desync parent_id from path.
type resourceRepo struct {
	store.Repository[model.Resource] // promoted Get/List/Delete; Create/Update overridden
	g                                *genericRepo
}

func newResourceRepo(g *genericRepo) store.ResourceRepo {
	return &resourceRepo{Repository: newTypedRepo(g, resourceCodec), g: g}
}

// Create inserts a resource, deriving its materialized path from its parent. A
// zero ParentID is a tree root (path "/<id>"); a set ParentID must exist in the
// same tenant (else ErrNotFound). The id is allocated up front so it can be
// embedded in the path before the insert. If the parent is a legacy (pre-
// NULL-path) resource, its root path is healed first (see effectivePath), so the
// child is rooted UNDER the parent and not mistaken for a second root.
func (r *resourceRepo) Create(ctx context.Context, res model.Resource) (model.Resource, error) {
	if r.g.readOnly {
		return model.Resource{}, store.ErrReadOnly
	}
	parentPath := ""
	if !res.ParentID.IsZero() {
		parent, err := r.Get(ctx, res.ParentID)
		if err != nil {
			return model.Resource{}, err // ErrNotFound: no parent to attach under
		}
		parentPath, err = r.effectivePath(ctx, parent)
		if err != nil {
			return model.Resource{}, err
		}
	}
	id := model.NewID()
	res.Path = parentPath + "/" + id.String()
	rec, err := resourceCodec.Encode(res)
	if err != nil {
		return model.Resource{}, err
	}
	full, err := r.g.CreateWithID(ctx, id, rec)
	if err != nil {
		return model.Resource{}, err
	}
	return decodeResource(full)
}

// CreateUnder creates res as a child of parent (zero parent = root). It is sugar
// for setting ParentID and calling Create.
func (r *resourceRepo) CreateUnder(ctx context.Context, parent model.ID, res model.Resource) (model.Resource, error) {
	res.ParentID = parent
	return r.Create(ctx, res)
}

// Update modifies a resource but PRESERVES its tree position: parent_id and path
// are forced back to the stored row's values, so structure changes only through
// Move. Everything else (name, uri, sensitivity, owner, workspace_id, metadata)
// updates normally, optimistic-concurrency checked on res.Version.
func (r *resourceRepo) Update(ctx context.Context, res model.Resource) (model.Resource, error) {
	if r.g.readOnly {
		return model.Resource{}, store.ErrReadOnly
	}
	current, err := r.Get(ctx, res.ID)
	if err != nil {
		return model.Resource{}, err
	}
	res.ParentID = current.ParentID
	res.Path = current.Path
	return r.Repository.Update(ctx, res)
}

// Children lists the direct children of parent (parent_id = parent); a zero
// parent lists the tree roots (parent_id IS NULL).
func (r *resourceRepo) Children(ctx context.Context, parent model.ID, q model.Query) ([]model.Resource, model.Page, error) {
	if parent.IsZero() {
		return r.queryResources(ctx, "parent_id IS NULL", nil, q)
	}
	return r.queryResources(ctx, "parent_id = ?", []any{parent.String()}, q)
}

// Subtree lists root and all its descendants via a single materialized-path
// prefix scan. A root with no path (a pre flat resource) has no subtree, so
// only itself is returned.
func (r *resourceRepo) Subtree(ctx context.Context, root model.ID, q model.Query) ([]model.Resource, model.Page, error) {
	rootRes, err := r.Get(ctx, root)
	if err != nil {
		return nil, model.Page{}, err
	}
	if rootRes.Path == "" {
		return []model.Resource{rootRes}, model.Page{}, nil
	}
	// path = root.Path matches the root itself; path LIKE root.Path||'/%' matches
	// every descendant. The trailing '/' is what stops a sibling whose path merely
	// shares a textual prefix from matching.
	return r.queryResources(ctx, "(path = ? OR path LIKE ?)",
		[]any{rootRes.Path, rootRes.Path + "/%"}, q)
}

// Move reparents node under newParent (zero = root), rewriting node's path and
// every descendant's in one atomic step. It rejects a move that would make node
// its own ancestor (ErrResourceCycle) and is optimistic-concurrency checked on
// node's version (ErrConflict if node changed concurrently). It must run inside a
// Mutate scope; the surrounding transaction makes the self+descendant rewrite
// all-or-nothing.
func (r *resourceRepo) Move(ctx context.Context, node, newParent model.ID) (model.Resource, error) {
	g := r.g
	if g.readOnly {
		return model.Resource{}, store.ErrReadOnly
	}
	cur, err := r.Get(ctx, node)
	if err != nil {
		return model.Resource{}, err
	}
	// A legacy (pre-NULL-path) node is necessarily a root, so its effective
	// path is "/<id>". Computing it here makes the cycle guard and the descendant
	// rewrite below correct for legacy and fresh nodes alike, never gated on path
	// presence (an empty path is "not yet materialized", never "is a root").
	curPath := cur.Path
	if curPath == "" {
		curPath = "/" + node.String()
	}
	newParentPath := ""
	if !newParent.IsZero() {
		if newParent == node {
			return model.Resource{}, store.ErrResourceCycle
		}
		np, err := r.Get(ctx, newParent)
		if err != nil {
			return model.Resource{}, err
		}
		// Heal a legacy new-parent's NULL path so the moved subtree is rooted under a
		// real path and the parent's own Subtree includes it.
		newParentPath, err = r.effectivePath(ctx, np)
		if err != nil {
			return model.Resource{}, err
		}
		// A cycle would form iff the proposed parent is node itself or sits inside
		// node's own subtree (its path is node's path, or under it).
		if newParentPath == curPath || strings.HasPrefix(newParentPath, curPath+"/") {
			return model.Resource{}, store.ErrResourceCycle
		}
	}
	newSelfPath := newParentPath + "/" + node.String()
	now := g.clock.Now()

	// 1. Rewrite the descendants' path prefix (curPath -> newSelfPath). substr is
	//    1-based and the start index is the constant byte length of the old prefix
	//    (UUID paths are ASCII, so byte length == char length). A legacy node has no
	//    path-keyed descendants, so the LIKE matches nothing — harmless.
	updDesc := g.dia.Rebind(fmt.Sprintf(
		"UPDATE %s SET path = ? || substr(path, ?), updated_at = ?, version = version + 1 WHERE tenant_id = ? AND path LIKE ?",
		resourceDescriptor.Table))
	if _, err := g.tx.ExecContext(ctx, updDesc,
		newSelfPath, len(curPath)+1, now.String(), g.tenant.String(), curPath+"/%"); err != nil {
		return model.Resource{}, mapWriteErr(err)
	}

	// 2. Update the node itself, optimistic-concurrency checked on the version we
	//    read. The descendant rewrite above never touches node's own row (its path
	//    is the prefix, not under it), so its version is unchanged here.
	updSelf := g.dia.Rebind(fmt.Sprintf(
		"UPDATE %s SET parent_id = ?, path = ?, updated_at = ?, version = version + 1 WHERE id = ? AND tenant_id = ? AND version = ?",
		resourceDescriptor.Table))
	res, err := g.tx.ExecContext(ctx, updSelf,
		encOptID(newParent), newSelfPath, now.String(), node.String(), g.tenant.String(), cur.Version)
	if err != nil {
		return model.Resource{}, mapWriteErr(err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return model.Resource{}, err
	}
	if n == 0 {
		return model.Resource{}, store.ErrConflict // node changed under us
	}
	return r.Get(ctx, node)
}

// queryResources runs a resource list with an optional leading predicate (the
// tree predicates Children/Subtree need but the generic filter set cannot
// express: IS NULL, an OR, a LIKE prefix), then the caller's q.Filters, ordering
// and id-keyset paging — mirroring genericRepo.List. Resources are hard-delete
// (no deleted_at clause).
func (r *resourceRepo) queryResources(ctx context.Context, extraWhere string, extraArgs []any, q model.Query) ([]model.Resource, model.Page, error) {
	g := r.g
	cols := resourceDescriptor.AllColumns()
	where := []string{"tenant_id = ?"}
	args := []any{g.tenant.String()}
	if extraWhere != "" {
		where = append(where, extraWhere)
		args = append(args, extraArgs...)
	}
	for _, f := range q.Filters {
		frag, val, err := g.filterFragment(f)
		if err != nil {
			return nil, model.Page{}, err
		}
		where = append(where, frag)
		args = append(args, val)
	}
	orderBy, customSort, err := g.orderClause(q.Sort)
	if err != nil {
		return nil, model.Page{}, err
	}
	if q.Cursor != "" {
		if customSort {
			return nil, model.Page{}, store.ErrCursorWithSort
		}
		where = append(where, "id > ?")
		args = append(args, q.Cursor)
	}
	limit := q.Limit
	if limit <= 0 {
		limit = defaultLimit
	}
	if limit > maxLimit {
		limit = maxLimit
	}
	sqlText := fmt.Sprintf("SELECT %s FROM %s WHERE %s ORDER BY %s LIMIT %d",
		strings.Join(cols, ", "), resourceDescriptor.Table, strings.Join(where, " AND "), orderBy, limit+1)
	g.guard(sqlText)
	rows, err := g.tx.QueryContext(ctx, g.dia.Rebind(sqlText), args...)
	if err != nil {
		return nil, model.Page{}, err
	}
	defer rows.Close()
	var out []model.Resource
	for rows.Next() {
		st, err := newScanState(resourceDescriptor, cols)
		if err != nil {
			return nil, model.Page{}, err
		}
		if err := rows.Scan(st.dests...); err != nil {
			return nil, model.Page{}, err
		}
		res, err := decodeResource(st.record())
		if err != nil {
			return nil, model.Page{}, err
		}
		out = append(out, res)
	}
	if err := rows.Err(); err != nil {
		return nil, model.Page{}, err
	}
	page := model.Page{}
	if len(out) > limit {
		out = out[:limit]
		page.HasMore = true
		if !customSort {
			page.Cursor = out[len(out)-1].ID.String()
		}
	}
	return out, page, nil
}

// effectivePath returns res's materialized path, lazily HEALING a legacy
// (pre) resource whose path is NULL. A pre row predates the tree
// columns, so it has neither a path NOR a parent_id — it is necessarily a tree
// root, and its path is therefore "/<id>". We persist that in the same
// transaction the first time the row is touched as a parent (Create) or as a
// move target (Move), so the tree becomes self-consistent: the legacy node then
// appears in its own Subtree and its children sit under it. This is lazy
// heal-on-touch, not an eager full-table rewrite, so it honors the back-compat
// "no row rewrite" rule (it fires only when a legacy row actually gains a child
// or is reparented). It is a no-op when res already has a path.
func (r *resourceRepo) effectivePath(ctx context.Context, res model.Resource) (string, error) {
	if res.Path != "" {
		return res.Path, nil
	}
	p := "/" + res.ID.String()
	now := r.g.clock.Now()
	// The "path IS NULL" guard keeps the heal idempotent under a concurrent toucher
	// (the path is deterministic — "/<id>" — so a lost race still converges).
	q := r.g.dia.Rebind(fmt.Sprintf(
		"UPDATE %s SET path = ?, updated_at = ?, version = version + 1 WHERE id = ? AND tenant_id = ? AND path IS NULL",
		resourceDescriptor.Table))
	if _, err := r.g.tx.ExecContext(ctx, q, p, now.String(), res.ID.String(), r.g.tenant.String()); err != nil {
		return "", mapWriteErr(err)
	}
	return p, nil
}

// decodeResource reconstructs a Resource from a full row record.
func decodeResource(rec model.Record) (model.Resource, error) {
	base, err := baseFromRecord(rec)
	if err != nil {
		return model.Resource{}, err
	}
	return resourceCodec.Decode(base, rec)
}
