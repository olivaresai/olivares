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

// The materialized-path writes of a relation with parent_id and path columns
// (resources). A path is "/<root>/…/<self>"; the typed repository derives
// it from the parent, rules out a cycle with treeMoveCycles, and calls these.

// treeMoveCycles reports whether placing a node whose path is curPath under a
// parent whose path is parentPath would make the node its own ancestor: the
// parent is the node itself or sits inside its subtree.
func treeMoveCycles(curPath, parentPath string) bool {
	return parentPath == curPath || strings.HasPrefix(parentPath, curPath+"/")
}

// moveTreeNode reparents node under newParent, rewriting its path from curPath
// to newSelfPath and every descendant's in one step. It is
// optimistic-concurrency checked on version (ErrConflict if node changed
// concurrently). It must run inside a Mutate scope; the surrounding transaction
// makes the self+descendant rewrite all-or-nothing.
func (g *genericRepo) moveTreeNode(ctx context.Context, node, newParent model.ID, version int64, curPath, newSelfPath string) error {
	now := g.clock.Now()
	// Both statements below are built here rather than through updateAt, so the
	// custodial write gate is reported explicitly. One report covers the pair:
	// the descendant rewrite and the node update are one logical move.
	if err := g.noteWrite(node); err != nil {
		return err
	}

	// 1. Rewrite the descendants' path prefix (curPath -> newSelfPath). substr is
	//    1-based and the start index is the constant byte length of the old prefix
	//    (UUID paths are ASCII, so byte length == char length). A legacy node has no
	//    path-keyed descendants, so the LIKE matches nothing — harmless.
	updDesc := g.dia.Rebind(fmt.Sprintf(
		"UPDATE %s SET path = ? || substr(path, ?), updated_at = ?, version = version + 1 WHERE tenant_id = ? AND path LIKE ?",
		g.relation()))
	if _, err := g.tx.ExecContext(ctx, updDesc,
		newSelfPath, len(curPath)+1, now.String(), g.tenant.String(), curPath+"/%"); err != nil {
		return mapWriteErr(err)
	}

	// 2. Update the node itself, optimistic-concurrency checked on the version we
	//    read. The descendant rewrite above never touches node's own row (its path
	//    is the prefix, not under it), so its version is unchanged here.
	updSelf := g.dia.Rebind(fmt.Sprintf(
		"UPDATE %s SET parent_id = ?, path = ?, updated_at = ?, version = version + 1 WHERE id = ? AND tenant_id = ? AND version = ?",
		g.relation()))
	res, err := g.tx.ExecContext(ctx, updSelf,
		encOptID(newParent), newSelfPath, now.String(), node.String(), g.tenant.String(), version)
	if err != nil {
		return mapWriteErr(err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return store.ErrConflict // node changed under us
	}
	return nil
}

// healTreePath returns the row's materialized path, persisting "/<id>" first when
// path is empty: a row written before its relation had tree columns has no parent
// either, so it is a root. The heal fires only when such a row gains a child or
// becomes a move target, never as a full-table rewrite.
func (g *genericRepo) healTreePath(ctx context.Context, id model.ID, path string) (string, error) {
	if path != "" {
		return path, nil
	}
	p := "/" + id.String()
	now := g.clock.Now()
	if err := g.noteWrite(id); err != nil {
		return "", err
	}
	// The "path IS NULL" guard keeps the heal idempotent under a concurrent toucher
	// (the path is deterministic — "/<id>" — so a lost race still converges).
	q := g.dia.Rebind(fmt.Sprintf(
		"UPDATE %s SET path = ?, updated_at = ?, version = version + 1 WHERE id = ? AND tenant_id = ? AND path IS NULL",
		g.relation()))
	if _, err := g.tx.ExecContext(ctx, q, p, now.String(), id.String(), g.tenant.String()); err != nil {
		return "", mapWriteErr(err)
	}
	return p, nil
}
