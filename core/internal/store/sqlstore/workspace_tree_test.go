// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"slices"
	"sync"
	"testing"

	"github.com/olivaresai/olivares/core/internal/store/dialect"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// workspaceTreeEngines runs body on a fresh store per engine. PostgreSQL uses the
// split-owner topology, so tenant isolation is the real RLS of a NOBYPASSRLS
// application role; that leg skips when no test database is configured.
func workspaceTreeEngines(t *testing.T, body func(t *testing.T, st store.Store, cfg store.Config)) {
	for _, engine := range store.SupportedEngines() {
		t.Run(string(engine), func(t *testing.T) {
			cfg := store.Config{Engine: engine, DSN: filepath.Join(t.TempDir(), "tree.db")}
			if engine == store.EnginePostgres {
				pg := isolatedPGSplit(t)
				cfg.DSN, cfg.OwnerDSN, cfg.AdminDSN = pg.App, pg.Owner, pg.Admin
			}
			st, err := Open(context.Background(), cfg, nil)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = st.Close() })
			body(t, st, cfg)
		})
	}
}

func mkWorkspace(t *testing.T, st store.Store, tenant model.TenantID, parent model.ID, slug string) model.Workspace {
	t.Helper()
	var got model.Workspace
	err := st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		ws, err := sc.Workspaces().Create(context.Background(), model.Workspace{
			Name: slug, Slug: slug, Status: model.StatusActive, ParentID: parent,
		})
		got = ws
		return err
	})
	if err != nil {
		t.Fatalf("create workspace %q: %v", slug, err)
	}
	return got
}

func setWorkspaceParent(st store.Store, tenant model.TenantID, node, parent model.ID) error {
	_, err := setWorkspaceParentGot(st, tenant, node, parent)
	return err
}

func setWorkspaceParentGot(st store.Store, tenant model.TenantID, node, parent model.ID) (model.Workspace, error) {
	ctx := context.Background()
	var got model.Workspace
	err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		ws, err := sc.Workspaces().SetParent(ctx, node, parent)
		got = ws
		return err
	})
	return got, err
}

func getWorkspace(t *testing.T, st store.Store, tenant model.TenantID, id model.ID) model.Workspace {
	t.Helper()
	var got model.Workspace
	err := st.View(context.Background(), tenant, func(sc store.Scope) error {
		ws, err := sc.Workspaces().Get(context.Background(), id)
		got = ws
		return err
	})
	if err != nil {
		t.Fatalf("get workspace %s: %v", id, err)
	}
	return got
}

// wantTree asserts each workspace's stored parent and path.
func wantTree(t *testing.T, st store.Store, tenant model.TenantID, want map[model.ID][2]string) {
	t.Helper()
	for id, w := range want {
		got := getWorkspace(t, st, tenant, id)
		if got.ParentID.String() != w[0] || got.Path != w[1] {
			t.Errorf("workspace %s: parent=%q path=%q, want parent=%q path=%q",
				got.Slug, got.ParentID, got.Path, w[0], w[1])
		}
	}
}

func TestWorkspaceSetParentRewritesTheSubtree(t *testing.T) {
	workspaceTreeEngines(t, func(t *testing.T, st store.Store, _ store.Config) {
		tenant := provisionTenant(t, st, "acme")
		a := mkWorkspace(t, st, tenant, "", "sales")
		b := mkWorkspace(t, st, tenant, a.ID, "sales-emea")
		c := mkWorkspace(t, st, tenant, b.ID, "sales-iberia")
		d := mkWorkspace(t, st, tenant, "", "marketing")
		pa, pd := "/"+a.ID.String(), "/"+d.ID.String()
		wantTree(t, st, tenant, map[model.ID][2]string{
			a.ID: {"", pa},
			b.ID: {a.ID.String(), pa + "/" + b.ID.String()},
			c.ID: {b.ID.String(), pa + "/" + b.ID.String() + "/" + c.ID.String()},
			d.ID: {"", pd},
		})

		moved, err := setWorkspaceParentGot(st, tenant, b.ID, d.ID)
		if err != nil {
			t.Fatalf("move sales-emea under marketing: %v", err)
		}
		if stored := getWorkspace(t, st, tenant, b.ID); moved.ParentID != d.ID || moved.Path != stored.Path || moved.Version != b.Version+1 || stored.Version != moved.Version {
			t.Errorf("SetParent returned parent=%s path=%q version=%d; stored path=%q version=%d; want the stored row at version %d",
				moved.ParentID, moved.Path, moved.Version, stored.Path, stored.Version, b.Version+1)
		}
		wantTree(t, st, tenant, map[model.ID][2]string{
			a.ID: {"", pa},
			b.ID: {d.ID.String(), pd + "/" + b.ID.String()},
			c.ID: {b.ID.String(), pd + "/" + b.ID.String() + "/" + c.ID.String()},
			d.ID: {"", pd},
		})

		if err := setWorkspaceParent(st, tenant, b.ID, ""); err != nil {
			t.Fatalf("make sales-emea a root: %v", err)
		}
		pb := "/" + b.ID.String()
		wantTree(t, st, tenant, map[model.ID][2]string{
			b.ID: {"", pb},
			c.ID: {b.ID.String(), pb + "/" + c.ID.String()},
			d.ID: {"", pd},
		})
	})
}

// TestWorkspaceSetParentRefusesACycle is the cycle check COCKPIT-02 section 6
// did not need while a workspace had no parent.
func TestWorkspaceSetParentRefusesACycle(t *testing.T) {
	workspaceTreeEngines(t, func(t *testing.T, st store.Store, _ store.Config) {
		tenant := provisionTenant(t, st, "acme")
		a := mkWorkspace(t, st, tenant, "", "sales")
		b := mkWorkspace(t, st, tenant, a.ID, "sales-emea")
		c := mkWorkspace(t, st, tenant, b.ID, "sales-iberia")
		before := map[model.ID][2]string{}
		for _, ws := range []model.Workspace{a, b, c} {
			before[ws.ID] = [2]string{ws.ParentID.String(), ws.Path}
		}
		for _, tc := range []struct {
			name         string
			node, parent model.ID
		}{
			{"under itself", a.ID, a.ID},
			{"under its child", a.ID, b.ID},
			{"under its grandchild", a.ID, c.ID},
			{"middle under its child", b.ID, c.ID},
		} {
			if err := setWorkspaceParent(st, tenant, tc.node, tc.parent); !errors.Is(err, store.ErrWorkspaceCycle) {
				t.Errorf("%s: err = %v, want ErrWorkspaceCycle", tc.name, err)
			}
		}
		wantTree(t, st, tenant, before)
	})
}

func TestWorkspaceTreeRefusesAnotherOrganization(t *testing.T) {
	workspaceTreeEngines(t, func(t *testing.T, st store.Store, _ store.Config) {
		ctx := context.Background()
		acme := provisionTenant(t, st, "acme")
		globex := provisionTenant(t, st, "globex")
		ours := mkWorkspace(t, st, acme, "", "sales")
		theirs := mkWorkspace(t, st, globex, "", "research")

		for _, tc := range []struct {
			name         string
			node, parent model.ID
		}{
			{"parent in another organization", ours.ID, theirs.ID},
			{"node in another organization", theirs.ID, ours.ID},
		} {
			if err := setWorkspaceParent(st, acme, tc.node, tc.parent); !errors.Is(err, store.ErrNotFound) {
				t.Errorf("%s: err = %v, want ErrNotFound", tc.name, err)
			}
		}
		err := st.Mutate(ctx, acme, func(sc store.Scope) error {
			_, err := sc.Workspaces().Create(ctx, model.Workspace{
				Name: "spy", Slug: "spy", Status: model.StatusActive, ParentID: theirs.ID,
			})
			return err
		})
		if !errors.Is(err, store.ErrNotFound) {
			t.Errorf("create under another organization's workspace: err = %v, want ErrNotFound", err)
		}
		wantTree(t, st, acme, map[model.ID][2]string{ours.ID: {"", ours.Path}})
		wantTree(t, st, globex, map[model.ID][2]string{theirs.ID: {"", theirs.Path}})
	})
}

func TestWorkspaceUpdateKeepsTheTreePosition(t *testing.T) {
	workspaceTreeEngines(t, func(t *testing.T, st store.Store, _ store.Config) {
		ctx := context.Background()
		tenant := provisionTenant(t, st, "acme")
		a := mkWorkspace(t, st, tenant, "", "sales")
		b := mkWorkspace(t, st, tenant, a.ID, "sales-emea")
		err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
			edit := b
			edit.Name, edit.ParentID, edit.Path = "EMEA sales", "", "/forged"
			_, err := sc.Workspaces().Update(ctx, edit)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		got := getWorkspace(t, st, tenant, b.ID)
		if got.Name != "EMEA sales" || got.ParentID != a.ID || got.Path != b.Path {
			t.Errorf("after update: name=%q parent=%s path=%q, want the new name and parent=%s path=%q",
				got.Name, got.ParentID, got.Path, a.ID, b.Path)
		}
	})
}

// nullWorkspaceTree gives a workspace the shape of a row written before v25: no
// parent and no path. It runs inside Mutate so the lineage writer is armed.
func nullWorkspaceTree(t *testing.T, st store.Store, tenant model.TenantID, id model.ID) {
	t.Helper()
	ctx := context.Background()
	err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		ts := sc.(*tenantScope)
		_, err := ts.tx.ExecContext(ctx, ts.s.dia.Rebind(
			"UPDATE workspaces SET path = NULL, parent_id = NULL WHERE id = ?"), id.String())
		return err
	})
	if err != nil {
		t.Fatalf("null workspace tree columns: %v", err)
	}
	if got := getWorkspace(t, st, tenant, id); got.Path != "" || !got.ParentID.IsZero() {
		t.Fatalf("legacy fixture did not land: parent=%s path=%q", got.ParentID, got.Path)
	}
}

// TestWorkspaceTreeHealsRowsWrittenBeforeV25 covers the rows v25 does not
// rewrite: the default workspace (inserted by the system path without a path)
// and any workspace created before the upgrade.
func TestWorkspaceTreeHealsRowsWrittenBeforeV25(t *testing.T) {
	workspaceTreeEngines(t, func(t *testing.T, st store.Store, _ store.Config) {
		ctx := context.Background()
		tenant := provisionTenant(t, st, "acme")
		var def model.Workspace
		if err := st.View(ctx, tenant, func(sc store.Scope) error {
			var err error
			def, err = sc.DefaultWorkspace(ctx)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		// Provisioning writes the default workspace without a path; the fixture
		// pins that shape rather than relying on it.
		nullWorkspaceTree(t, st, tenant, def.ID)
		child := mkWorkspace(t, st, tenant, def.ID, "team")
		pdef := "/" + def.ID.String()
		wantTree(t, st, tenant, map[model.ID][2]string{
			def.ID:   {"", pdef},
			child.ID: {def.ID.String(), pdef + "/" + child.ID.String()},
		})

		// An unrelated subtree must not be swept into a legacy node's move.
		x := mkWorkspace(t, st, tenant, "", "ops")
		y := mkWorkspace(t, st, tenant, x.ID, "ops-sre")
		legacy := mkWorkspace(t, st, tenant, "", "legal")
		nullWorkspaceTree(t, st, tenant, legacy.ID)
		if err := setWorkspaceParent(st, tenant, legacy.ID, child.ID); err != nil {
			t.Fatalf("move a legacy workspace: %v", err)
		}
		px := "/" + x.ID.String()
		wantTree(t, st, tenant, map[model.ID][2]string{
			legacy.ID: {child.ID.String(), pdef + "/" + child.ID.String() + "/" + legacy.ID.String()},
			x.ID:      {"", px},
			y.ID:      {x.ID.String(), px + "/" + y.ID.String()},
		})

		other := mkWorkspace(t, st, tenant, "", "finance")
		nullWorkspaceTree(t, st, tenant, other.ID)
		if err := setWorkspaceParent(st, tenant, x.ID, other.ID); err != nil {
			t.Fatalf("move under a legacy workspace: %v", err)
		}
		po := "/" + other.ID.String()
		wantTree(t, st, tenant, map[model.ID][2]string{
			other.ID: {"", po},
			x.ID:     {other.ID.String(), po + "/" + x.ID.String()},
			y.ID:     {x.ID.String(), po + "/" + x.ID.String() + "/" + y.ID.String()},
		})
	})
}

func TestConfinedCallerCannotSetAWorkspaceParent(t *testing.T) {
	workspaceTreeEngines(t, func(t *testing.T, st store.Store, _ store.Config) {
		ctx := context.Background()
		tenant := provisionTenant(t, st, "acme")
		a := mkWorkspace(t, st, tenant, "", "sales")
		b := mkWorkspace(t, st, tenant, "", "marketing")
		err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
			confined, err := store.ConfineWorkspace(ctx, sc, a.ID)
			if err != nil {
				return err
			}
			_, err = confined.Workspaces().SetParent(ctx, a.ID, b.ID)
			return err
		})
		if !errors.Is(err, store.ErrWorkspaceConfinement) {
			t.Errorf("confined SetParent: err = %v, want ErrWorkspaceConfinement", err)
		}
		wantTree(t, st, tenant, map[model.ID][2]string{a.ID: {"", a.Path}})
	})
}

// TestWorkspaceTreeMigrationUpgradesAV24Store rebuilds the v24 shape (no tree
// columns, tracking ending at v24) under existing rows and reopens: v25 adds the columns
// and indexes, keeps the rows, and the tree works on them.
func TestWorkspaceTreeMigrationUpgradesAV24Store(t *testing.T) {
	workspaceTreeEngines(t, func(t *testing.T, st store.Store, cfg store.Config) {
		ctx := context.Background()
		tenant := provisionTenant(t, st, "acme")
		old := mkWorkspace(t, st, tenant, "", "sales")
		if err := st.Close(); err != nil {
			t.Fatal(err)
		}
		// A v24 store carries the core v8 workspace guard, which does not name the tree
		// columns; the boot that adds them also moves the guard to its current edition.
		regressWorkspaceGuards(t, cfg, false, false)
		driver, dsn := "sqlite", cfg.DSN
		if cfg.Engine == store.EnginePostgres {
			driver, dsn = "pgx", cfg.OwnerDSN
		}
		db, err := sql.Open(driver, dsn)
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		for _, stmt := range []string{
			"DROP INDEX workspaces_parent_id_idx",
			"DROP INDEX workspaces_path_idx",
			"ALTER TABLE workspaces DROP COLUMN parent_id",
			"ALTER TABLE workspaces DROP COLUMN path",
			"DROP INDEX user_groups_workspace_id_idx",
			"ALTER TABLE user_groups DROP COLUMN workspace_id",
			"DELETE FROM schema_migrations_core WHERE version>24",
		} {
			if _, err := db.Exec(stmt); err != nil {
				t.Fatalf("%s: %v", stmt, err)
			}
		}

		reopened, err := Open(ctx, cfg, nil)
		if err != nil {
			t.Fatalf("reopen a v24 store: %v", err)
		}
		t.Cleanup(func() { _ = reopened.Close() })
		indexQuery := "SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name IN ('workspaces_parent_id_idx', 'workspaces_path_idx')"
		if cfg.Engine == store.EnginePostgres {
			indexQuery = "SELECT COUNT(*) FROM pg_indexes WHERE indexname IN ('workspaces_parent_id_idx', 'workspaces_path_idx')"
		}
		var indexes int
		if err := db.QueryRow(indexQuery).Scan(&indexes); err != nil || indexes != 2 {
			t.Fatalf("tree indexes after v25 = %d (err %v), want 2", indexes, err)
		}
		var recorded int
		if err := db.QueryRow("SELECT COUNT(*) FROM schema_migrations_core WHERE version=25 AND name='workspace_tree'").Scan(&recorded); err != nil || recorded != 1 {
			t.Fatalf("v25 recorded %d times (err %v), want once", recorded, err)
		}
		if got := lineageGuardTrackerRows(t, cfg); got != "workspace_tree_guards/expand" {
			t.Fatalf("lineage guard tracker version 2 = %q after the upgrade, want workspace_tree_guards/expand", got)
		}
		cols, err := reopened.(*sqlStore).dia.TableColumns(ctx, db, "workspaces")
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range workspaceTreeColumns {
			if !cols[c] {
				t.Errorf("column %q missing after v25", c)
			}
		}
		if got := getWorkspace(t, reopened, tenant, old.ID); got.Path != "" || !got.ParentID.IsZero() || got.Name != "sales" {
			t.Fatalf("upgraded row: %+v, want the old row with no tree position", got)
		}
		child := mkWorkspace(t, reopened, tenant, old.ID, "sales-emea")
		po := "/" + old.ID.String()
		wantTree(t, reopened, tenant, map[model.ID][2]string{
			old.ID:   {"", po},
			child.ID: {old.ID.String(), po + "/" + child.ID.String()},
		})
	})
}

// TestHistoricalMigrationsRenderWorkspacesWithoutTheTree pins v2 and v21: their
// workspace statements are the ones rendered before v25 owned the tree columns.
func TestHistoricalMigrationsRenderWorkspacesWithoutTheTree(t *testing.T) {
	old := workspaceDescriptor
	old.Fields = slices.Clone(old.Fields[:len(old.Fields)-len(workspaceTreeColumns)])
	for _, engine := range store.SupportedEngines() {
		dia, ok := dialect.New(engine)
		if !ok {
			t.Fatalf("no dialect for %s", engine)
		}
		var v2 []string
		for _, m := range buildCoreMigrations(dia, coreDescriptors(), nil, nil) {
			if m.Version == 2 {
				v2 = m.Stmts
			}
		}
		for _, stmt := range dia.CreateTableStmts(old) {
			if !slices.Contains(v2, stmt) {
				t.Fatalf("%s: v2 lost its historical workspace statement %q", engine, stmt)
			}
		}
		if live := dia.CreateTableStmts(workspaceDescriptor)[0]; slices.Contains(v2, live) {
			t.Fatalf("%s: v2 renders the workspace tree columns: %q", engine, live)
		}
		for _, d := range coreDescriptorsV21() {
			if d.Kind == workspaceDescriptor.Kind && !slices.Equal(dia.CreateTableStmts(d), dia.CreateTableStmts(old)) {
				t.Fatalf("%s: v21 renders the workspace tree columns", engine)
			}
		}
	}
}

func TestWorkspaceSetParentInAViewIsRefused(t *testing.T) {
	workspaceTreeEngines(t, func(t *testing.T, st store.Store, _ store.Config) {
		ctx := context.Background()
		tenant := provisionTenant(t, st, "acme")
		a := mkWorkspace(t, st, tenant, "", "sales")
		b := mkWorkspace(t, st, tenant, "", "marketing")
		err := st.View(ctx, tenant, func(sc store.Scope) error {
			_, err := sc.Workspaces().SetParent(ctx, a.ID, b.ID)
			return err
		})
		if !errors.Is(err, store.ErrReadOnly) {
			t.Errorf("SetParent in a View: err = %v, want ErrReadOnly", err)
		}
		wantTree(t, st, tenant, map[model.ID][2]string{a.ID: {"", a.Path}})
	})
}

// TestASwallowedSetParentRefusalCannotCommit proves the poison: a callback that
// discards SetParent's error still cannot commit the transaction.
func TestASwallowedSetParentRefusalCannotCommit(t *testing.T) {
	workspaceTreeEngines(t, func(t *testing.T, st store.Store, _ store.Config) {
		ctx := context.Background()
		tenant := provisionTenant(t, st, "acme")
		a := mkWorkspace(t, st, tenant, "", "sales")
		b := mkWorkspace(t, st, tenant, a.ID, "sales-emea")
		err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
			_, _ = sc.Workspaces().SetParent(ctx, a.ID, b.ID)
			return nil
		})
		if err == nil {
			t.Fatal("a Mutate that swallowed a refused SetParent committed")
		}
		wantTree(t, st, tenant, map[model.ID][2]string{a.ID: {"", a.Path}, b.ID: {a.ID.String(), b.Path}})
	})
}

// TestWorkspaceMoveWithAStaleVersionChangesNothing drives the shared tree write
// with a version the node no longer has: the self update matches no row, the
// move fails with ErrConflict, and the descendant rewrite rolls back with it.
func TestWorkspaceMoveWithAStaleVersionChangesNothing(t *testing.T) {
	workspaceTreeEngines(t, func(t *testing.T, st store.Store, _ store.Config) {
		ctx := context.Background()
		tenant := provisionTenant(t, st, "acme")
		a := mkWorkspace(t, st, tenant, "", "sales")
		b := mkWorkspace(t, st, tenant, a.ID, "sales-emea")
		c := mkWorkspace(t, st, tenant, b.ID, "sales-iberia")
		d := mkWorkspace(t, st, tenant, "", "marketing")
		err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
			g := sc.(*tenantScope).repo(workspaceDescriptor)
			return g.moveTreeNode(ctx, b.ID, d.ID, b.Version+7, b.Path, d.Path+"/"+b.ID.String())
		})
		if !errors.Is(err, store.ErrConflict) {
			t.Fatalf("stale move: err = %v, want ErrConflict", err)
		}
		wantTree(t, st, tenant, map[model.ID][2]string{b.ID: {a.ID.String(), b.Path}, c.ID: {b.ID.String(), c.Path}})
	})
}

func TestConfinedCallerSeesNoTreePosition(t *testing.T) {
	workspaceTreeEngines(t, func(t *testing.T, st store.Store, _ store.Config) {
		ctx := context.Background()
		tenant := provisionTenant(t, st, "acme")
		a := mkWorkspace(t, st, tenant, "", "sales")
		b := mkWorkspace(t, st, tenant, a.ID, "sales-emea")
		hidden := func(where string, ws model.Workspace) {
			if !ws.ParentID.IsZero() || ws.Path != "" {
				t.Errorf("%s: a confined caller sees parent=%s path=%q, want neither", where, ws.ParentID, ws.Path)
			}
		}
		err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
			confined, err := store.ConfineWorkspace(ctx, sc, b.ID)
			if err != nil {
				return err
			}
			got, err := confined.Workspaces().Get(ctx, b.ID)
			if err != nil {
				return err
			}
			hidden("Get", got)
			list, _, err := confined.Workspaces().List(ctx, model.Query{})
			if err != nil {
				return err
			}
			if len(list) != 1 {
				t.Fatalf("confined List = %d rows, want its own workspace", len(list))
			}
			hidden("List", list[0])
			got.Name, got.ParentID, got.Path = "EMEA", "", "/forged"
			updated, err := confined.Workspaces().Update(ctx, got)
			if err != nil {
				return err
			}
			hidden("Update", updated)
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		if got := getWorkspace(t, st, tenant, b.ID); got.Name != "EMEA" || got.ParentID != a.ID || got.Path != b.Path {
			t.Errorf("after a confined update: name=%q parent=%s path=%q, want the new name and the stored tree position", got.Name, got.ParentID, got.Path)
		}
	})
}

func TestTheDefaultWorkspaceStaysARoot(t *testing.T) {
	workspaceTreeEngines(t, func(t *testing.T, st store.Store, _ store.Config) {
		ctx := context.Background()
		tenant := provisionTenant(t, st, "acme")
		var def model.Workspace
		if err := st.View(ctx, tenant, func(sc store.Scope) error {
			var err error
			def, err = sc.DefaultWorkspace(ctx)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		x := mkWorkspace(t, st, tenant, "", "ops")
		if err := setWorkspaceParent(st, tenant, def.ID, x.ID); !errors.Is(err, store.ErrConflict) {
			t.Errorf("default under a department: err = %v, want ErrConflict", err)
		}
		if err := setWorkspaceParent(st, tenant, def.ID, ""); err != nil {
			t.Errorf("default to root: %v", err)
		}
		if err := setWorkspaceParent(st, tenant, x.ID, def.ID); err != nil {
			t.Errorf("a department under the default workspace: %v", err)
		}
		pdef := "/" + def.ID.String()
		wantTree(t, st, tenant, map[model.ID][2]string{
			def.ID: {"", pdef},
			x.ID:   {def.ID.String(), pdef + "/" + x.ID.String()},
		})
	})
}

func TestWorkspaceDeleteRefusesAParentWithChildren(t *testing.T) {
	workspaceTreeEngines(t, func(t *testing.T, st store.Store, _ store.Config) {
		ctx := context.Background()
		tenant := provisionTenant(t, st, "acme")
		a := mkWorkspace(t, st, tenant, "", "sales")
		b := mkWorkspace(t, st, tenant, a.ID, "sales-emea")
		del := func(id model.ID) error {
			return st.Mutate(ctx, tenant, func(sc store.Scope) error { return sc.Workspaces().Delete(ctx, id) })
		}
		if err := del(a.ID); !errors.Is(err, store.ErrConflict) {
			t.Fatalf("delete a parent: err = %v, want ErrConflict", err)
		}
		wantTree(t, st, tenant, map[model.ID][2]string{a.ID: {"", a.Path}})
		if err := del(b.ID); err != nil {
			t.Fatalf("delete the child: %v", err)
		}
		if err := del(a.ID); err != nil {
			t.Fatalf("delete the emptied parent: %v", err)
		}
	})
}

// TestWorkspaceTreeMigrationRefusesAMissingRelation keeps v25 from recording a
// stub: without the workspace relation it fails instead of creating one.
func TestWorkspaceTreeMigrationRefusesAMissingRelation(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "empty.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	dia, _ := dialect.New(store.EngineSQLite)
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback() //nolint:errcheck // test
	if err := coreWorkspaceTreeMigration(dia).Exec(ctx, tx); err == nil {
		t.Fatal("v25 ran without a workspace relation")
	}
}

// TestOpposingWorkspaceMovesCannotFormACycle runs "a under b" and "b under a"
// at once: the tenant lock serializes them, so exactly one wins and the other
// sees the committed tree and refuses the cycle.
func TestOpposingWorkspaceMovesCannotFormACycle(t *testing.T) {
	workspaceTreeEngines(t, func(t *testing.T, st store.Store, _ store.Config) {
		tenant := provisionTenant(t, st, "acme")
		a := mkWorkspace(t, st, tenant, "", "sales")
		b := mkWorkspace(t, st, tenant, "", "marketing")
		errs := make([]error, 2)
		var wg sync.WaitGroup
		for i, move := range [][2]model.ID{{a.ID, b.ID}, {b.ID, a.ID}} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				errs[i] = setWorkspaceParent(st, tenant, move[0], move[1])
			}()
		}
		wg.Wait()
		won, refused := 0, 0
		for _, err := range errs {
			switch {
			case err == nil:
				won++
			case errors.Is(err, store.ErrWorkspaceCycle):
				refused++
			default:
				t.Fatalf("opposing move: unexpected error %v", err)
			}
		}
		if won != 1 || refused != 1 {
			t.Fatalf("opposing moves: %d won, %d refused (%v), want one of each", won, refused, errs)
		}
		ga, gb := getWorkspace(t, st, tenant, a.ID), getWorkspace(t, st, tenant, b.ID)
		if ga.ParentID == b.ID && gb.ParentID == a.ID {
			t.Fatalf("a cycle was stored: a under b and b under a")
		}
	})
}
