// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/internal/store/dialect"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

const workspaceKind model.Kind = "core.workspace"

// TestWorkspaceTreeWritesAdvanceTheWorkspaceLineageOnce pins that a position in the
// organization tree is lineage: the resolver walks parent_id and path, so a SetParent
// that rewrites a subtree must advance the workspace generation exactly once, and a
// presentation-only update must not.
func TestWorkspaceTreeWritesAdvanceTheWorkspaceLineageOnce(t *testing.T) {
	workspaceTreeEngines(t, func(t *testing.T, st store.Store, _ store.Config) {
		tenant := provisionTenant(t, st, "acme")
		sales := mkWorkspace(t, st, tenant, "", "sales")
		emea := mkWorkspace(t, st, tenant, sales.ID, "sales-emea")
		mkWorkspace(t, st, tenant, emea.ID, "sales-iberia")
		marketing := mkWorkspace(t, st, tenant, "", "marketing")

		generation := func() store.AuthorizationFactRef { return lineageTestFacts(t, st, tenant)[workspaceKind] }

		before := generation()
		// sales-emea and sales-iberia both get a new path in this one transaction.
		if err := setWorkspaceParent(st, tenant, emea.ID, marketing.ID); err != nil {
			t.Fatalf("move sales-emea under marketing: %v", err)
		}
		if got := generation(); got.Version != before.Version+1 {
			t.Errorf("SetParent over a two-row subtree: workspace generation %d -> %d, want %d", before.Version, got.Version, before.Version+1)
		}

		before = generation()
		if err := setWorkspaceParent(st, tenant, emea.ID, ""); err != nil {
			t.Fatalf("make sales-emea a root: %v", err)
		}
		if got := generation(); got.Version != before.Version+1 {
			t.Errorf("SetParent to a root: workspace generation %d -> %d, want %d", before.Version, got.Version, before.Version+1)
		}

		before = generation()
		if err := st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
			cur, err := sc.Workspaces().Get(context.Background(), marketing.ID)
			if err != nil {
				return err
			}
			cur.Slug = "brand"
			_, err = sc.Workspaces().Update(context.Background(), cur)
			return err
		}); err != nil {
			t.Fatalf("change the slug of marketing: %v", err)
		}
		if got := generation(); got.Version != before.Version+1 {
			t.Errorf("a slug change: workspace generation %d -> %d, want %d", before.Version, got.Version, before.Version+1)
		}

		before = generation()
		if err := st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
			cur, err := sc.Workspaces().Get(context.Background(), marketing.ID)
			if err != nil {
				return err
			}
			cur.Name = "Marketing and brand"
			_, err = sc.Workspaces().Update(context.Background(), cur)
			return err
		}); err != nil {
			t.Fatalf("rename marketing: %v", err)
		}
		if got := generation(); got.Version != before.Version {
			t.Errorf("a rename advanced the workspace generation %d -> %d, want it unchanged", before.Version, got.Version)
		}
	})
}

// regressWorkspaceGuards rewinds a closed database to what core v8 left: the
// edition 1 workspace guard (slug only), with version 2 of the lineage guard tracker
// not yet recorded. keepTracker leaves that record in place, which no honest upgrade
// produces. drift also alters one edition 1 guard.
func regressWorkspaceGuards(t *testing.T, cfg store.Config, keepTracker, drift bool) {
	t.Helper()
	dia, ok := dialect.New(cfg.Engine)
	if !ok {
		t.Fatalf("no dialect for %s", cfg.Engine)
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
	exec := func(statement string) {
		t.Helper()
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("%s: %v", statement, err)
		}
	}
	for _, object := range lineageGuardObjectsFor(dia, lineageRelationsEdition1()) {
		if object.table != lineageWorkspaceRelation {
			continue
		}
		switch {
		case cfg.Engine == store.EngineSQLite:
			exec("DROP TRIGGER main." + object.name)
			if drift && strings.HasSuffix(object.name, "_delete") {
				continue // the delete guard stays absent
			}
			exec(object.statement)
		case object.body != "":
			statement := strings.Replace(object.statement, "CREATE FUNCTION ", "CREATE OR REPLACE FUNCTION ", 1)
			if drift {
				statement = strings.Replace(statement, object.body, object.body+"\n-- drift", 1)
			}
			exec(statement)
		}
	}
	if !keepTracker {
		exec("DELETE FROM " + directoryWriterRelation(dia, lineageGuardsTracker) + " WHERE version = 2")
	}
}

// TestWorkspaceLineageGuardUpgradesFromEdition1 boots a database that core v8 left
// with the slug-only workspace guard. The boot must install edition 2, record it, and
// from then on advance the generation once for a SetParent.
func TestWorkspaceLineageGuardUpgradesFromEdition1(t *testing.T) {
	workspaceTreeEngines(t, func(t *testing.T, st store.Store, cfg store.Config) {
		tenant := provisionTenant(t, st, "acme")
		sales := mkWorkspace(t, st, tenant, "", "sales")
		emea := mkWorkspace(t, st, tenant, sales.ID, "sales-emea")
		marketing := mkWorkspace(t, st, tenant, "", "marketing")
		if err := st.Close(); err != nil {
			t.Fatal(err)
		}

		regressWorkspaceGuards(t, cfg, false, false)
		reopened, err := Open(context.Background(), cfg, nil)
		if err != nil {
			t.Fatalf("boot over edition 1 workspace guards: %v", err)
		}
		t.Cleanup(func() { _ = reopened.Close() })

		before := lineageTestFacts(t, reopened, tenant)[workspaceKind]
		if err := setWorkspaceParent(reopened, tenant, emea.ID, marketing.ID); err != nil {
			t.Fatalf("move sales-emea under marketing: %v", err)
		}
		if got := lineageTestFacts(t, reopened, tenant)[workspaceKind]; got.Version != before.Version+1 {
			t.Errorf("SetParent after the upgrade: workspace generation %d -> %d, want %d", before.Version, got.Version, before.Version+1)
		}
		if err := reopened.Close(); err != nil {
			t.Fatal(err)
		}
		// The edition is recorded and the next boot verifies it, installing nothing.
		if recorded := lineageGuardTrackerRows(t, cfg); recorded != "workspace_tree_guards/expand" {
			t.Errorf("lineage guard tracker version 2 = %q, want workspace_tree_guards/expand", recorded)
		}
		again, err := Open(context.Background(), cfg, nil)
		if err != nil {
			t.Fatalf("second boot over edition 2: %v", err)
		}
		_ = again.Close()
	})
}

// lineageGuardTrackerRows reads version 2 of the lineage guard tracker, "name/phase".
func lineageGuardTrackerRows(t *testing.T, cfg store.Config) string {
	t.Helper()
	dia, _ := dialect.New(cfg.Engine)
	driver, dsn := "sqlite", cfg.DSN
	if cfg.Engine == store.EnginePostgres {
		driver, dsn = "pgx", cfg.OwnerDSN
	}
	db, err := sql.Open(driver, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var name, phase string
	q := dia.Rebind("SELECT name, phase FROM " + directoryWriterRelation(dia, lineageGuardsTracker) + " WHERE version = ?")
	if err := db.QueryRow(q, lineageWorkspaceTreeGuardVersion).Scan(&name, &phase); err != nil {
		return err.Error()
	}
	return name + "/" + phase
}

// TestWorkspaceLineageGuardRefusesAnInconsistentEdition pins that the tracker, not
// the installed text, decides the edition, and that neither a drifted edition 1 guard
// nor an edition 1 guard under an edition 2 record boots.
func TestWorkspaceLineageGuardRefusesAnInconsistentEdition(t *testing.T) {
	for _, tc := range []struct {
		name               string
		keepTracker, drift bool
	}{
		{"edition 1 guard altered", false, true},
		{"edition 1 guard under an edition 2 record", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			workspaceTreeEngines(t, func(t *testing.T, st store.Store, cfg store.Config) {
				provisionTenant(t, st, "acme")
				if err := st.Close(); err != nil {
					t.Fatal(err)
				}
				regressWorkspaceGuards(t, cfg, tc.keepTracker, tc.drift)
				reopened, err := Open(context.Background(), cfg, nil)
				if err == nil {
					_ = reopened.Close()
					t.Fatal("boot accepted a workspace guard that does not match the recorded edition")
				}
				if !errors.Is(err, store.ErrLineageUnavailable) {
					t.Errorf("boot error %v, want ErrLineageUnavailable", err)
				}
			})
		})
	}
}

// TestPostgresRestoreVerifiesTheRecordedLineageGuardEdition pins that the inventory restore
// checks the lineage routines of the edition the tracker names: an estate restored from a
// database that core v8 left (edition 1, no version 2) restores, and an edition 1 routine
// under a version 2 record is refused.
func TestPostgresRestoreVerifiesTheRecordedLineageGuardEdition(t *testing.T) {
	for _, tc := range []struct {
		name        string
		keepTracker bool
	}{
		{"edition 1 estate", false},
		{"edition 1 routine under an edition 2 record", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			pg := isolatedPGSplit(t)
			cfg := store.Config{Engine: store.EnginePostgres, DSN: pg.App, OwnerDSN: pg.Owner, AdminDSN: pg.Admin, MaxConns: 2}
			st, err := Open(ctx, cfg, nil)
			if err != nil {
				t.Fatal(err)
			}
			if err := st.Close(); err != nil {
				t.Fatal(err)
			}
			app := openCustodyPGPool(t, pg.App)
			owner := openCustodyPGPool(t, pg.Owner)
			super := openCustodyPGPool(t, pg.Superuser)
			roles := guardRoles{App: guardRoleFact{Known: true, Role: currentCustodyRole(t, app)}, Owner: guardRoleFact{Known: true, Role: currentCustodyRole(t, owner)}, OwnerConfigured: true}
			tx, err := super.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			if err := installDirectoryInventoryTx(ctx, tx, roles); err != nil {
				_ = tx.Rollback()
				t.Fatal(err)
			}
			if err := tx.Commit(); err != nil {
				t.Fatal(err)
			}
			// What pg_restore --no-owner --no-privileges leaves of the inventory function.
			mustExec(t, super, "REVOKE SELECT(id,tenant_id) ON public.orgs FROM "+directoryInventoryOwner)
			mustExec(t, super, "REVOKE SELECT(id,tenant_id,version) ON public.core_directory_epoch FROM "+directoryInventoryOwner)
			mustExec(t, super, "ALTER FUNCTION public.olivares_directory_inventory_v1() OWNER TO "+quoteIdent(roles.Owner.Role))
			mustExec(t, super, "UPDATE pg_catalog.pg_proc SET proacl=NULL WHERE oid='public.olivares_directory_inventory_v1()'::regprocedure")

			regressWorkspaceGuards(t, cfg, tc.keepTracker, false)
			err = RestorePostgresDirectoryInventory(ctx, cfg, pg.Superuser)
			if tc.keepTracker {
				if !errors.Is(err, store.ErrLineageUnavailable) {
					t.Fatalf("restore over an edition 1 routine under an edition 2 record = %v, want ErrLineageUnavailable", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("restore of an edition 1 estate: %v", err)
			}
			again, err := Open(ctx, cfg, nil)
			if err != nil {
				t.Fatalf("boot after restoring an edition 1 estate: %v", err)
			}
			_ = again.Close()
		})
	}
}

// Edition 2 cannot exist without the recorded edition-1 installation. Keeping
// the newer guard text must not let a lost v1 tracker row pass boot admission.
func TestWorkspaceLineageGuardRefusesMissingEdition1Record(t *testing.T) {
	workspaceTreeEngines(t, func(t *testing.T, st store.Store, cfg store.Config) {
		provisionTenant(t, st, "acme")
		if err := st.Close(); err != nil {
			t.Fatal(err)
		}
		driver := "sqlite"
		if cfg.Engine == store.EnginePostgres {
			driver = "pgx"
		}
		dsn := cfg.DSN
		if cfg.OwnerDSN != "" {
			dsn = cfg.OwnerDSN
		}
		db, err := sql.Open(driver, dsn)
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		dia, _ := dialect.New(cfg.Engine)
		if _, err := db.Exec("DELETE FROM " + directoryWriterRelation(dia, lineageGuardsTracker) + " WHERE version = 1"); err != nil {
			t.Fatal(err)
		}
		reopened, err := Open(context.Background(), cfg, nil)
		if err == nil {
			_ = reopened.Close()
			t.Fatal("boot accepted edition 2 without the edition 1 installation record")
		}
		if !errors.Is(err, store.ErrLineageUnavailable) {
			t.Fatalf("boot error %v, want ErrLineageUnavailable", err)
		}
	})
}
