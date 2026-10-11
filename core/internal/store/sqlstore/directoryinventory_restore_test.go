// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/store"
)

func TestPostgresRestoreReestablishesDirectoryInventoryAuthority(t *testing.T) {
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
	if present, err := verifyPostgresDirectoryInventory(ctx, super, roles); err != nil || !present {
		t.Fatalf("source inventory: present=%v err=%v", present, err)
	}
	mustExec(t, super, "REVOKE SELECT(id,tenant_id) ON public.orgs FROM "+directoryInventoryOwner)
	mustExec(t, super, "REVOKE SELECT(id,tenant_id,version) ON public.core_directory_epoch FROM "+directoryInventoryOwner)
	// pg_restore --no-owner --no-privileges creates the function under the target owner with default PUBLIC EXECUTE.
	mustExec(t, super, "ALTER FUNCTION public.olivares_directory_inventory_v1() OWNER TO "+quoteIdent(roles.Owner.Role))
	mustExec(t, super, "UPDATE pg_catalog.pg_proc SET proacl=NULL WHERE oid='public.olivares_directory_inventory_v1()'::regprocedure")
	if _, err := verifyPostgresDirectoryInventory(ctx, super, roles); err == nil {
		t.Fatal("boot guard accepted imported authority")
	}
	// The schema owner has deliberately no path to the isolated BYPASSRLS role.
	if _, err := owner.ExecContext(ctx, "ALTER FUNCTION public.olivares_directory_inventory_v1() OWNER TO "+quoteIdent(directoryInventoryOwner)); err == nil {
		t.Fatal("schema owner can acquire inventory authority")
	}
	if err := RestorePostgresUserAuthorityPrivileges(ctx, cfg); err != nil {
		t.Fatal(err)
	}

	for name, dsn := range map[string]string{"app": pg.App, "owner": pg.Owner, "backup_admin": pg.Admin} {
		t.Run("refuse_"+name, func(t *testing.T) {
			if err := CheckPostgresInventoryRestoreAuthority(ctx, cfg, dsn); err == nil {
				t.Fatal("accepted non-DBA authority")
			}
		})
	}
	t.Run("wrong_database", func(t *testing.T) {
		other := isolatedPGSplit(t)
		if err := CheckPostgresInventoryRestoreAuthority(ctx, cfg, other.Superuser); err == nil {
			t.Fatal("accepted DBA on another database")
		}
	})
	t.Run("definition_drift", func(t *testing.T) {
		mustExec(t, super, "ALTER FUNCTION public.olivares_directory_inventory_v1() SET search_path TO public")
		if err := RestorePostgresDirectoryInventory(ctx, cfg, pg.Superuser); err == nil {
			t.Fatal("repaired definition drift")
		}
		mustExec(t, super, "ALTER FUNCTION public.olivares_directory_inventory_v1() SET search_path TO pg_catalog")
	})
	t.Run("unsafe_inventory_role_rolls_back", func(t *testing.T) {
		mustExec(t, super, "GRANT EXECUTE ON FUNCTION public.olivares_lineage_begin(text) TO PUBLIC")
		mustExec(t, super, "ALTER ROLE "+directoryInventoryOwner+" LOGIN")
		if err := RestorePostgresDirectoryInventory(ctx, cfg, pg.Superuser); err == nil {
			t.Fatal("accepted login-capable inventory role")
		}
		var gotOwner string
		var stripped bool
		if err := super.QueryRowContext(ctx, "SELECT pg_catalog.pg_get_userbyid(proowner),proacl IS NULL FROM pg_catalog.pg_proc WHERE oid='public.olivares_directory_inventory_v1()'::regprocedure").Scan(&gotOwner, &stripped); err != nil || gotOwner != roles.Owner.Role || !stripped {
			t.Fatalf("failed restore changed authority: owner=%s stripped=%v err=%v", gotOwner, stripped, err)
		}
		var publicExec bool
		if err := super.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM pg_proc p,LATERAL aclexplode(p.proacl) a WHERE p.oid='public.olivares_lineage_begin(text)'::regprocedure AND a.grantee=0)").Scan(&publicExec); err != nil || !publicExec {
			t.Fatalf("failed inventory recovery committed lineage grants: public=%v err=%v", publicExec, err)
		}
		mustExec(t, super, "ALTER ROLE "+directoryInventoryOwner+" NOLOGIN")
	})
	t.Run("connection_error_redacts_password", func(t *testing.T) {
		err := CheckPostgresInventoryRestoreAuthority(ctx, cfg, "postgres://dba:RESTORE_TEST_SECRET@127.0.0.1:1/postgres?connect_timeout=1")
		if err == nil || strings.Contains(err.Error(), "RESTORE_TEST_SECRET") {
			t.Fatal("DBA connection error missing or leaked its password")
		}
	})
	if err := CheckPostgresInventoryRestoreAuthority(ctx, cfg, pg.Superuser); err != nil {
		t.Fatal(err)
	}
	if err := RestorePostgresDirectoryInventory(ctx, cfg, pg.Superuser); err != nil {
		t.Fatal(err)
	}
	if present, err := verifyPostgresDirectoryInventory(ctx, super, roles); err != nil || !present {
		t.Fatalf("restored inventory: present=%v err=%v", present, err)
	}
	if err := RestorePostgresDirectoryInventory(ctx, cfg, pg.Superuser); err != nil {
		t.Fatalf("idempotent restore: %v", err)
	}
	st, err = Open(ctx, cfg, nil)
	if err != nil {
		t.Fatalf("restored boot: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
}
