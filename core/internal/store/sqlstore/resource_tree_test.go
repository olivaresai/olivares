// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/internal/store/dialect"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// mkResource creates a resource under parent (zero = root) and returns it.
func mkResource(t *testing.T, st store.Store, tenant model.TenantID, parent model.ID, name string) model.Resource {
	t.Helper()
	var got model.Resource
	err := st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		r, err := sc.Resources().CreateUnder(context.Background(), parent, model.Resource{
			Name: name, Kind: "folder",
		})
		got = r
		return err
	})
	if err != nil {
		t.Fatalf("create resource %q: %v", name, err)
	}
	return got
}

// TestResourceTreeCreateAndPath checks parent links and the materialized path.
func TestResourceTreeCreateAndPath(t *testing.T) {
	st := openSQLiteTest(t, nil)
	tenant := provisionTenant(t, st, "acme")

	root := mkResource(t, st, tenant, "", "root")
	child := mkResource(t, st, tenant, root.ID, "child")
	grand := mkResource(t, st, tenant, child.ID, "grand")

	if root.ParentID != "" && !root.ParentID.IsZero() {
		t.Errorf("root parent = %s, want zero", root.ParentID)
	}
	if want := "/" + root.ID.String(); root.Path != want {
		t.Errorf("root path = %q, want %q", root.Path, want)
	}
	if child.ParentID != root.ID {
		t.Errorf("child parent = %s, want %s", child.ParentID, root.ID)
	}
	if want := root.Path + "/" + child.ID.String(); child.Path != want {
		t.Errorf("child path = %q, want %q", child.Path, want)
	}
	if want := child.Path + "/" + grand.ID.String(); grand.Path != want {
		t.Errorf("grand path = %q, want %q", grand.Path, want)
	}
}

// TestResourceSubtreeAndChildren checks the prefix query and direct-children
// listing, and that a sibling subtree is not swept in.
func TestResourceSubtreeAndChildren(t *testing.T) {
	st := openSQLiteTest(t, nil)
	tenant := provisionTenant(t, st, "acme")
	ctx := context.Background()

	root := mkResource(t, st, tenant, "", "root")
	child := mkResource(t, st, tenant, root.ID, "child")
	grand := mkResource(t, st, tenant, child.ID, "grand")
	sibling := mkResource(t, st, tenant, root.ID, "sibling") // under root, parallel to child
	otherRoot := mkResource(t, st, tenant, "", "other-root") // an unrelated tree

	if err := st.View(ctx, tenant, func(sc store.Scope) error {
		// Subtree(root) = root, child, grand, sibling (4) — NOT otherRoot.
		sub, _, err := sc.Resources().Subtree(ctx, root.ID, model.Query{})
		if err != nil {
			return err
		}
		if got := idset(sub); !got[root.ID] || !got[child.ID] || !got[grand.ID] || !got[sibling.ID] || got[otherRoot.ID] || len(got) != 4 {
			t.Errorf("subtree(root) = %v (len %d), want {root,child,grand,sibling}", got, len(got))
		}
		// Subtree(child) = child, grand (2).
		sub2, _, err := sc.Resources().Subtree(ctx, child.ID, model.Query{})
		if err != nil {
			return err
		}
		if got := idset(sub2); !got[child.ID] || !got[grand.ID] || len(got) != 2 {
			t.Errorf("subtree(child) = %v, want {child,grand}", got)
		}
		// Children(root) = child, sibling (direct only — not grand).
		kids, _, err := sc.Resources().Children(ctx, root.ID, model.Query{})
		if err != nil {
			return err
		}
		if got := idset(kids); !got[child.ID] || !got[sibling.ID] || got[grand.ID] || len(got) != 2 {
			t.Errorf("children(root) = %v, want {child,sibling}", got)
		}
		// Children(zero) = the tree roots (root, otherRoot) — and ONLY roots.
		roots, _, err := sc.Resources().Children(ctx, "", model.Query{})
		if err != nil {
			return err
		}
		if got := idset(roots); !got[root.ID] || !got[otherRoot.ID] || got[child.ID] || len(got) != 2 {
			t.Errorf("children(roots) = %v, want {root,otherRoot}", got)
		}
		return nil
	}); err != nil {
		t.Fatalf("subtree/children: %v", err)
	}
}

// TestResourceSubtreeFilter checks q.Filters narrow a subtree.
func TestResourceSubtreeFilter(t *testing.T) {
	st := openSQLiteTest(t, nil)
	tenant := provisionTenant(t, st, "acme")
	ctx := context.Background()

	root := mkResource(t, st, tenant, "", "root")
	// A leaf of a different kind under root.
	var leaf model.Resource
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		r, err := sc.Resources().CreateUnder(ctx, root.ID, model.Resource{Name: "tbl", Kind: "postgres.table"})
		leaf = r
		return err
	}); err != nil {
		t.Fatalf("create leaf: %v", err)
	}
	if err := st.View(ctx, tenant, func(sc store.Scope) error {
		sub, _, err := sc.Resources().Subtree(ctx, root.ID, model.Query{
			Filters: []model.Filter{{Column: "kind", Op: model.OpEq, Value: "postgres.table"}},
		})
		if err != nil {
			return err
		}
		if got := idset(sub); !got[leaf.ID] || got[root.ID] || len(got) != 1 {
			t.Errorf("filtered subtree = %v, want {leaf}", got)
		}
		return nil
	}); err != nil {
		t.Fatalf("filtered subtree: %v", err)
	}
}

// TestResourceMove reparents a subtree and checks every path is rewritten.
func TestResourceMove(t *testing.T) {
	st := openSQLiteTest(t, nil)
	tenant := provisionTenant(t, st, "acme")
	ctx := context.Background()

	root := mkResource(t, st, tenant, "", "root")
	child := mkResource(t, st, tenant, root.ID, "child")
	grand := mkResource(t, st, tenant, child.ID, "grand")
	dest := mkResource(t, st, tenant, "", "dest")

	var movedChild model.Resource
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		m, err := sc.Resources().Move(ctx, child.ID, dest.ID)
		movedChild = m
		return err
	}); err != nil {
		t.Fatalf("move: %v", err)
	}

	// child now under dest; its path reflects the new parent.
	if movedChild.ParentID != dest.ID {
		t.Errorf("moved child parent = %s, want %s", movedChild.ParentID, dest.ID)
	}
	if want := dest.Path + "/" + child.ID.String(); movedChild.Path != want {
		t.Errorf("moved child path = %q, want %q", movedChild.Path, want)
	}

	if err := st.View(ctx, tenant, func(sc store.Scope) error {
		// grandchild's path rewritten under the moved child; parent link unchanged.
		g, err := sc.Resources().Get(ctx, grand.ID)
		if err != nil {
			return err
		}
		if g.ParentID != child.ID {
			t.Errorf("grand parent = %s, want %s (unchanged)", g.ParentID, child.ID)
		}
		if want := movedChild.Path + "/" + grand.ID.String(); g.Path != want {
			t.Errorf("grand path = %q, want %q", g.Path, want)
		}
		// dest subtree now holds dest, child, grand (3); root subtree only root.
		destSub, _, err := sc.Resources().Subtree(ctx, dest.ID, model.Query{})
		if err != nil {
			return err
		}
		if s := idset(destSub); !s[dest.ID] || !s[child.ID] || !s[grand.ID] || len(s) != 3 {
			t.Errorf("dest subtree = %v, want {dest,child,grand}", s)
		}
		rootSub, _, err := sc.Resources().Subtree(ctx, root.ID, model.Query{})
		if err != nil {
			return err
		}
		if s := idset(rootSub); !s[root.ID] || len(s) != 1 {
			t.Errorf("root subtree = %v, want {root}", s)
		}
		return nil
	}); err != nil {
		t.Fatalf("post-move checks: %v", err)
	}

	// Move to root (zero parent): path becomes "/<id>".
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		m, err := sc.Resources().Move(ctx, child.ID, "")
		if err != nil {
			return err
		}
		if want := "/" + child.ID.String(); m.Path != want {
			t.Errorf("move-to-root path = %q, want %q", m.Path, want)
		}
		if !m.ParentID.IsZero() {
			t.Errorf("move-to-root parent = %s, want zero", m.ParentID)
		}
		return nil
	}); err != nil {
		t.Fatalf("move to root: %v", err)
	}
}

// TestResourceMoveCycleGuard rejects a move that would make a node its own ancestor.
func TestResourceMoveCycleGuard(t *testing.T) {
	st := openSQLiteTest(t, nil)
	tenant := provisionTenant(t, st, "acme")
	ctx := context.Background()

	root := mkResource(t, st, tenant, "", "root")
	child := mkResource(t, st, tenant, root.ID, "child")
	grand := mkResource(t, st, tenant, child.ID, "grand")

	cases := []struct {
		name            string
		node, newParent model.ID
	}{
		{"under-self", root.ID, root.ID},
		{"under-direct-descendant", root.ID, child.ID},
		{"under-deep-descendant", root.ID, grand.ID},
	}
	for _, c := range cases {
		err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
			_, e := sc.Resources().Move(ctx, c.node, c.newParent)
			return e
		})
		if !errors.Is(err, store.ErrResourceCycle) {
			t.Errorf("%s: err = %v, want ErrResourceCycle", c.name, err)
		}
	}
}

// TestResourceUpdatePreservesTree proves Update cannot restructure the tree:
// parent_id/path are forced back to the stored values, so only Move reparents.
func TestResourceUpdatePreservesTree(t *testing.T) {
	st := openSQLiteTest(t, nil)
	tenant := provisionTenant(t, st, "acme")
	ctx := context.Background()

	root := mkResource(t, st, tenant, "", "root")
	child := mkResource(t, st, tenant, root.ID, "child")

	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		// Attempt to reparent child to root-of-tree AND poison its path via Update.
		tampered := child
		tampered.ParentID = ""
		tampered.Path = "/hijacked"
		tampered.Name = "renamed"
		updated, err := sc.Resources().Update(ctx, tampered)
		if err != nil {
			return err
		}
		if updated.ParentID != root.ID {
			t.Errorf("update changed parent to %s, want preserved %s", updated.ParentID, root.ID)
		}
		if updated.Path != child.Path {
			t.Errorf("update changed path to %q, want preserved %q", updated.Path, child.Path)
		}
		if updated.Name != "renamed" {
			t.Errorf("update did not apply name change: %q", updated.Name)
		}
		return nil
	}); err != nil {
		t.Fatalf("update: %v", err)
	}
}

// TestResourceCreateUnderMissingParent rejects attaching under a non-existent parent.
func TestResourceCreateUnderMissingParent(t *testing.T) {
	st := openSQLiteTest(t, nil)
	tenant := provisionTenant(t, st, "acme")
	ctx := context.Background()

	err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		_, e := sc.Resources().CreateUnder(ctx, model.NewID(), model.Resource{Name: "orphan", Kind: "folder"})
		return e
	})
	if !errors.Is(err, store.ErrNotFound) {
		t.Errorf("create under missing parent: err = %v, want ErrNotFound", err)
	}
}

// TestResourceFlatCreateGetsRootPath proves a plain Create (no parent) still gets
// a valid root path — back-compat for callers that ignore the hierarchy.
func TestResourceFlatCreateGetsRootPath(t *testing.T) {
	st := openSQLiteTest(t, nil)
	tenant := provisionTenant(t, st, "acme")
	ctx := context.Background()

	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		r, err := sc.Resources().Create(ctx, model.Resource{Name: "flat", Kind: "s3.bucket"})
		if err != nil {
			return err
		}
		if want := "/" + r.ID.String(); r.Path != want {
			t.Errorf("flat resource path = %q, want %q", r.Path, want)
		}
		if !strings.HasPrefix(r.Path, "/") {
			t.Errorf("flat resource path %q is not rooted", r.Path)
		}
		return nil
	}); err != nil {
		t.Fatalf("flat create: %v", err)
	}
}

// makeLegacyResourcePath forces a resource to the pre shape (NULL path AND
// NULL parent_id, the way the additive reconcile leaves a row that predates the
// tree columns) via a raw maintenance UPDATE. The SQLite scope pin is cleared so
// the write runs on the privileged path, like the engine's own migrations.
func makeLegacyResourcePath(t *testing.T, st store.Store, id model.ID) {
	t.Helper()
	ss := st.(*sqlStore)
	if _, err := ss.db.Exec("DELETE FROM " + dialect.ScopeTenantTable); err != nil {
		t.Fatalf("clear scope pin: %v", err)
	}
	if _, err := ss.db.Exec("UPDATE resources SET path = NULL, parent_id = NULL WHERE id = ?", id.String()); err != nil {
		t.Fatalf("null resource path: %v", err)
	}
}

// TestResourceLegacyParentHealedOnCreate proves a child created under a legacy
// (pre-NULL-path) resource is rooted UNDER it — the parent's path is healed
// — instead of becoming a phantom second root invisible in the parent's subtree.
func TestResourceLegacyParentHealedOnCreate(t *testing.T) {
	st := openSQLiteTest(t, nil)
	tenant := provisionTenant(t, st, "acme")
	ctx := context.Background()

	legacy := mkResource(t, st, tenant, "", "legacy")
	makeLegacyResourcePath(t, st, legacy.ID)

	var child model.Resource
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		lg, err := sc.Resources().Get(ctx, legacy.ID)
		if err != nil {
			return err
		}
		if lg.Path != "" {
			t.Fatalf("setup: legacy path = %q, want empty (NULL)", lg.Path)
		}
		c, err := sc.Resources().CreateUnder(ctx, legacy.ID, model.Resource{Name: "child", Kind: "folder"})
		child = c
		return err
	}); err != nil {
		t.Fatalf("create under legacy parent: %v", err)
	}

	if want := "/" + legacy.ID.String() + "/" + child.ID.String(); child.Path != want {
		t.Errorf("child path = %q, want %q (rooted under the parent)", child.Path, want)
	}
	if err := st.View(ctx, tenant, func(sc store.Scope) error {
		lg, err := sc.Resources().Get(ctx, legacy.ID)
		if err != nil {
			return err
		}
		if want := "/" + legacy.ID.String(); lg.Path != want {
			t.Errorf("legacy parent not healed: path = %q, want %q", lg.Path, want)
		}
		sub, _, err := sc.Resources().Subtree(ctx, legacy.ID, model.Query{})
		if err != nil {
			return err
		}
		if s := idset(sub); !s[legacy.ID] || !s[child.ID] || len(s) != 2 {
			t.Errorf("subtree(legacy) = %v, want {legacy, child} — child must be visible", s)
		}
		return nil
	}); err != nil {
		t.Fatalf("post-create checks: %v", err)
	}
}

// TestResourceMoveLegacyCycleGuard proves the cycle guard fires even when the
// moved node has a NULL path: moving a legacy node under its own child is a cycle.
func TestResourceMoveLegacyCycleGuard(t *testing.T) {
	st := openSQLiteTest(t, nil)
	tenant := provisionTenant(t, st, "acme")
	ctx := context.Background()

	legacy := mkResource(t, st, tenant, "", "legacy")
	child := mkResource(t, st, tenant, legacy.ID, "child") // child.parent_id = legacy
	makeLegacyResourcePath(t, st, legacy.ID)               // now legacy has a NULL path again

	err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		_, e := sc.Resources().Move(ctx, legacy.ID, child.ID)
		return e
	})
	if !errors.Is(err, store.ErrResourceCycle) {
		t.Errorf("move legacy node under its own child: err = %v, want ErrResourceCycle", err)
	}
}

// TestResourceMoveUnderLegacyParent proves a node moved under a legacy (NULL-path)
// parent is rooted under it (the parent is healed), not desynced into a phantom root.
func TestResourceMoveUnderLegacyParent(t *testing.T) {
	st := openSQLiteTest(t, nil)
	tenant := provisionTenant(t, st, "acme")
	ctx := context.Background()

	legacy := mkResource(t, st, tenant, "", "legacy")
	node := mkResource(t, st, tenant, "", "node") // a separate root
	makeLegacyResourcePath(t, st, legacy.ID)

	var moved model.Resource
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		m, err := sc.Resources().Move(ctx, node.ID, legacy.ID)
		moved = m
		return err
	}); err != nil {
		t.Fatalf("move under legacy parent: %v", err)
	}
	if want := "/" + legacy.ID.String() + "/" + node.ID.String(); moved.Path != want {
		t.Errorf("moved path = %q, want %q", moved.Path, want)
	}
	if moved.ParentID != legacy.ID {
		t.Errorf("moved parent = %s, want %s", moved.ParentID, legacy.ID)
	}
	if err := st.View(ctx, tenant, func(sc store.Scope) error {
		lg, err := sc.Resources().Get(ctx, legacy.ID)
		if err != nil {
			return err
		}
		if want := "/" + legacy.ID.String(); lg.Path != want {
			t.Errorf("legacy parent not healed on move: path = %q, want %q", lg.Path, want)
		}
		sub, _, err := sc.Resources().Subtree(ctx, legacy.ID, model.Query{})
		if err != nil {
			return err
		}
		if s := idset(sub); !s[legacy.ID] || !s[node.ID] || len(s) != 2 {
			t.Errorf("subtree(legacy) = %v, want {legacy, node}", s)
		}
		return nil
	}); err != nil {
		t.Fatalf("post-move checks: %v", err)
	}
}

// idset collects resource ids into a set for membership assertions.
func idset(rs []model.Resource) map[model.ID]bool {
	m := make(map[model.ID]bool, len(rs))
	for _, r := range rs {
		m[r.ID] = true
	}
	return m
}
