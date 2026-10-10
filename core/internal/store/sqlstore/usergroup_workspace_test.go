// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"database/sql"
	"path/filepath"
	"slices"
	"testing"

	"github.com/olivaresai/olivares/core/internal/store/dialect"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

func mkUserGroup(t *testing.T, st store.Store, tenant model.TenantID, name string, workspace model.ID) model.UserGroup {
	t.Helper()
	var got model.UserGroup
	err := st.AuthMutate(context.Background(), func(as store.AuthScope) error {
		g, err := as.Groups().Create(context.Background(), model.UserGroup{
			TargetTenantID: tenant, DisplayName: name, WorkspaceID: workspace,
		})
		got = g
		return err
	})
	if err != nil {
		t.Fatalf("create user group %q: %v", name, err)
	}
	return got
}

func getUserGroup(t *testing.T, st store.Store, id model.ID) model.UserGroup {
	t.Helper()
	var got model.UserGroup
	err := st.AuthView(context.Background(), func(as store.AuthScope) error {
		g, err := as.Groups().Get(context.Background(), id)
		got = g
		return err
	})
	if err != nil {
		t.Fatalf("get user group %s: %v", id, err)
	}
	return got
}

// A group keeps the workspace it is placed in across create, update and clear,
// and a group never placed reads back with no place: today's tenant-wide group.
func TestUserGroupKeepsItsWorkspacePlace(t *testing.T) {
	workspaceTreeEngines(t, func(t *testing.T, st store.Store, _ store.Config) {
		ctx := context.Background()
		tenant := provisionTenant(t, st, "acme")
		sales := mkWorkspace(t, st, tenant, "", "sales")
		ops := mkWorkspace(t, st, tenant, "", "ops")

		placed := mkUserGroup(t, st, tenant, "Sales staff", sales.ID)
		free := mkUserGroup(t, st, tenant, "Everyone", "")
		if got := getUserGroup(t, st, placed.ID); got.WorkspaceID != sales.ID {
			t.Fatalf("placed group reads workspace %q, want %q", got.WorkspaceID, sales.ID)
		}
		if got := getUserGroup(t, st, free.ID); !got.WorkspaceID.IsZero() {
			t.Fatalf("unplaced group reads workspace %q, want none", got.WorkspaceID)
		}

		move := func(g model.UserGroup, to model.ID) {
			t.Helper()
			err := st.AuthMutate(ctx, func(as store.AuthScope) error {
				cur, err := as.Groups().Get(ctx, g.ID)
				if err != nil {
					return err
				}
				cur.WorkspaceID = to
				_, err = as.Groups().Update(ctx, cur)
				return err
			})
			if err != nil {
				t.Fatalf("update group %s: %v", g.ID, err)
			}
		}
		move(placed, ops.ID)
		if got := getUserGroup(t, st, placed.ID); got.WorkspaceID != ops.ID {
			t.Fatalf("moved group reads workspace %q, want %q", got.WorkspaceID, ops.ID)
		}
		move(placed, "")
		if got := getUserGroup(t, st, placed.ID); !got.WorkspaceID.IsZero() {
			t.Fatalf("cleared group reads workspace %q, want none", got.WorkspaceID)
		}
	})
}

// A group's place does not touch the columns that decide authorization.
func TestUserGroupPlaceLeavesTheOtherColumnsAlone(t *testing.T) {
	workspaceTreeEngines(t, func(t *testing.T, st store.Store, _ store.Config) {
		ctx := context.Background()
		tenant := provisionTenant(t, st, "acme")
		ws := mkWorkspace(t, st, tenant, "", "sales")
		parent := mkUserGroup(t, st, tenant, "All", "")
		var child model.UserGroup
		err := st.AuthMutate(ctx, func(as store.AuthScope) error {
			g, err := as.Groups().Create(ctx, model.UserGroup{
				TargetTenantID: tenant, DisplayName: "Sales", ExternalID: "ext-1",
				MappedRole: "editor", ParentGroupID: parent.ID, ProvisionedBy: "operator", WorkspaceID: ws.ID,
			})
			child = g
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		got := getUserGroup(t, st, child.ID)
		if got.ExternalID != "ext-1" || got.MappedRole != "editor" || got.ParentGroupID != parent.ID ||
			got.ProvisionedBy != "operator" || got.DisplayName != "Sales" || got.WorkspaceID != ws.ID {
			t.Fatalf("group row = %+v, want every column kept beside the place", got)
		}
	})
}

// TestUserGroupWorkspaceMigrationUpgradesAV26Store rebuilds the v26 shape (no
// place column, no v27 record) under existing rows and reopens: v27 adds the
// column and its index, keeps the rows, and a group can then be placed.
func TestUserGroupWorkspaceMigrationUpgradesAV26Store(t *testing.T) {
	workspaceTreeEngines(t, func(t *testing.T, st store.Store, cfg store.Config) {
		ctx := context.Background()
		tenant := provisionTenant(t, st, "acme")
		ws := mkWorkspace(t, st, tenant, "", "sales")
		old := mkUserGroup(t, st, tenant, "Everyone", "")
		if err := st.Close(); err != nil {
			t.Fatal(err)
		}
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
			"DROP INDEX user_groups_workspace_id_idx",
			"ALTER TABLE user_groups DROP COLUMN workspace_id",
			"DELETE FROM schema_migrations_core WHERE version=27",
		} {
			if _, err := db.Exec(stmt); err != nil {
				t.Fatalf("%s: %v", stmt, err)
			}
		}

		reopened, err := Open(ctx, cfg, nil)
		if err != nil {
			t.Fatalf("reopen a v26 store: %v", err)
		}
		t.Cleanup(func() { _ = reopened.Close() })
		indexQuery := "SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name='user_groups_workspace_id_idx'"
		if cfg.Engine == store.EnginePostgres {
			indexQuery = "SELECT COUNT(*) FROM pg_indexes WHERE indexname='user_groups_workspace_id_idx'"
		}
		var indexes int
		if err := db.QueryRow(indexQuery).Scan(&indexes); err != nil || indexes != 1 {
			t.Fatalf("place index after v27 = %d (err %v), want 1", indexes, err)
		}
		var recorded int
		if err := db.QueryRow("SELECT COUNT(*) FROM schema_migrations_core WHERE version=27 AND name='user_group_workspace'").Scan(&recorded); err != nil || recorded != 1 {
			t.Fatalf("v27 recorded %d times (err %v), want once", recorded, err)
		}
		if got := getUserGroup(t, reopened, old.ID); !got.WorkspaceID.IsZero() || got.DisplayName != "Everyone" {
			t.Fatalf("upgraded row: %+v, want the old row with no place", got)
		}
		placed := mkUserGroup(t, reopened, tenant, "Sales staff", ws.ID)
		if got := getUserGroup(t, reopened, placed.ID); got.WorkspaceID != ws.ID {
			t.Fatalf("group placed after v27 reads %q, want %q", got.WorkspaceID, ws.ID)
		}
	})
}

// TestHistoricalMigrationsRenderUserGroupsWithoutThePlace pins v2 and v21: their
// user_groups statements are the ones rendered before v27 owned the place column.
func TestHistoricalMigrationsRenderUserGroupsWithoutThePlace(t *testing.T) {
	old := userGroupDescriptor
	old.Fields = slices.DeleteFunc(slices.Clone(old.Fields), func(f model.FieldSpec) bool {
		return slices.Contains(userGroupWorkspaceColumns, f.Name)
	})
	if len(old.Fields) != len(userGroupDescriptor.Fields)-len(userGroupWorkspaceColumns) {
		t.Fatal("the place column is not declared on the user group descriptor")
	}
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
		// v2 also omits older versioned columns. Compare its actual DDL with
		// that historical shape, so retaining workspace_id alone goes red.
		v2Group := beforeWorkspaceTree(beforeExternalProvider(beforeGroupOrigin(beforeConsentCustody(beforeAuthenticationFreshness(old)))))
		for _, stmt := range dia.CreateTableStmts(v2Group) {
			if !slices.Contains(v2, stmt) {
				t.Fatalf("%s: v2 does not preserve the historical user_groups DDL: %q", engine, stmt)
			}
		}

		for _, d := range coreDescriptorsV21() {
			if d.Kind == userGroupDescriptor.Kind && !slices.Equal(dia.CreateTableStmts(d), dia.CreateTableStmts(old)) {
				t.Fatalf("%s: v21 renders the user group place column", engine)
			}
		}
	}
}

// TestUserGroupWorkspaceMigrationRefusesAMissingRelation keeps v27 from
// recording a stub: without the user_groups relation it fails instead of
// creating one.
func TestUserGroupWorkspaceMigrationRefusesAMissingRelation(t *testing.T) {
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
	if err := coreUserGroupWorkspaceMigration(dia).Exec(ctx, tx); err == nil {
		t.Fatal("v27 ran without a user_groups relation")
	}
}

// ReadWorkspaceUserGroups counts the groups placed in one workspace of one
// organization. A group never placed is tenant-wide and sits in no workspace,
// not even the default one; another organization's group never counts, even
// when its row names this workspace.
func TestReadWorkspaceUserGroups(t *testing.T) {
	workspaceTreeEngines(t, func(t *testing.T, st store.Store, _ store.Config) {
		ctx := context.Background()
		tenant := provisionTenant(t, st, "acme")
		other := provisionTenant(t, st, "other")
		sales := mkWorkspace(t, st, tenant, "", "sales")
		ops := mkWorkspace(t, st, tenant, "", "ops")
		var def model.Workspace
		if err := st.View(ctx, tenant, func(sc store.Scope) error {
			var err error
			def, err = sc.DefaultWorkspace(ctx)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		mkUserGroup(t, st, tenant, "Sales A", sales.ID)
		mkUserGroup(t, st, tenant, "Sales B", sales.ID)
		mkUserGroup(t, st, tenant, "Ops", ops.ID)
		mkUserGroup(t, st, tenant, "Everyone", "")
		mkUserGroup(t, st, other, "Forged", sales.ID)

		count := func(ws model.ID) (store.KindCount, error) {
			var got store.KindCount
			err := st.AuthView(ctx, func(as store.AuthScope) error {
				var err error
				got, err = store.ReadWorkspaceUserGroups(ctx, as, tenant, ws)
				return err
			})
			return got, err
		}
		for name, tc := range map[string]struct {
			ws   model.ID
			want int
		}{"sales": {sales.ID, 2}, "ops": {ops.ID, 1}, "default": {def.ID, 0}} {
			if got, err := count(tc.ws); err != nil || got != (store.KindCount{Count: tc.want}) {
				t.Errorf("%s user groups = %+v, %v; want %d", name, got, err, tc.want)
			}
		}
		if _, err := count(""); err == nil {
			t.Error("a zero workspace id read user groups; it must be refused, never read as tenant-wide")
		}
	})
}
