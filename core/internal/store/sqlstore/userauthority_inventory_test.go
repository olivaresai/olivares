// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"database/sql"
	"errors"
	"github.com/jackc/pgx/v5"
	"github.com/olivaresai/olivares/core/internal/pgtest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

func TestUserAuthorityInventoryGrammar(t *testing.T) {
	tenant := model.NewID().String()
	other := model.NewID().String()
	system := directoryInventoryRow{kind: "org", id: model.SystemTenantID.String(), tenant: model.SystemTenantID.String()}
	org := directoryInventoryRow{kind: "org", id: tenant, tenant: tenant}
	epoch := directoryInventoryRow{kind: "directory_epoch", id: tenant, tenant: tenant, version: sql.NullInt64{Int64: 4, Valid: true}}
	valid := []directoryInventoryRow{epoch, system, org}
	got, err := decodeDirectoryInventory(valid)
	if err != nil || len(got.BusinessTenants) != 1 || len(got.Epochs) != 1 || got.System.ID != model.ID(model.SystemTenantID) {
		t.Fatalf("valid=%+v %v", got, err)
	}
	cases := map[string][]directoryInventoryRow{
		"empty": {}, "missing_SYSTEM": {org, epoch}, "duplicate_SYSTEM": {system, system, org, epoch},
		"malformed_SYSTEM": {{kind: "org", id: tenant, tenant: model.SystemTenantID.String()}},
		"versioned_SYSTEM": {{kind: "org", id: system.id, tenant: system.tenant, version: epoch.version}},
		"missing_G":        {system, org}, "orphan_G": {system, epoch}, "duplicate_G": {system, org, epoch, epoch},
		"G_SYSTEM":         {system, {kind: "directory_epoch", id: system.id, tenant: system.tenant, version: epoch.version}},
		"noncanonical_org": {system, {kind: "org", id: strings.ToUpper(tenant), tenant: strings.ToUpper(tenant)}, epoch},
		"id_tenant_split":  {system, {kind: "org", id: other, tenant: tenant}, epoch},
		"zero_G":           {system, org, {kind: "directory_epoch", id: tenant, tenant: tenant, version: sql.NullInt64{Valid: true}}},
		"unknown_kind":     {system, org, epoch, {kind: "invented"}},
	}
	for name, rows := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := decodeDirectoryInventory(rows); err == nil {
				t.Fatal("malformed inventory accepted")
			}
		})
	}
}

func TestUserAuthorityPostgresInventoryClosure(t *testing.T) {
	ctx := context.Background()
	s, cfg, _, _ := f2aFreshTarget(t, store.EnginePostgres)
	// Obtain only the test-owned DBA pool already registered by isolatedPGSplit.
	pgSuper := os.Getenv(pgtest.EnvSuperuserDSN)
	// The base fixture DSN names a different database. Reuse its authentication
	// against this isolated database through the same production pinning helper.
	var database string
	if err := s.db.QueryRowContext(ctx, "SELECT current_database()").Scan(&database); err != nil {
		t.Fatal(err)
	}
	parsed, err := pgx.ParseConfig(pgSuper)
	if err != nil {
		t.Fatal("invalid fixture DSN")
	}
	super, closeDB, err := openOnDatabase(parsed, database)
	if err != nil {
		t.Fatal(err)
	}
	defer closeDB()
	app, owner := s.directoryGuardRoles.App.Role, s.directoryGuardRoles.Owner.Role
	f2aInstallInventoryForConfig(t, cfg, pgSuper)
	cfg.AdminDSN = ""
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	runSQL := func(stmts ...string) {
		t.Helper()
		for _, stmt := range stmts {
			if _, err := super.ExecContext(ctx, stmt); err != nil {
				t.Fatalf("fixture SQL: %v", err)
			}
		}
	}
	positive := func() {
		t.Helper()
		raw, err := Open(ctx, cfg, nil)
		if err != nil {
			t.Fatalf("closed noAdmin Open: %v", err)
		}
		raw.Close()
		if _, _, changed, err := OpenDirectoryWriterMaintenance(ctx, cfg, nil, 1); err != nil || changed {
			t.Fatalf("closed noAdmin retry: changed=%t err=%v", changed, err)
		}
	}
	// Effective manual grants remain valid after future-object defaults disappear.
	runSQL("ALTER DEFAULT PRIVILEGES FOR ROLE "+quoteIdent(owner)+" IN SCHEMA public REVOKE ALL ON TABLES FROM "+quoteIdent(app), "ALTER DEFAULT PRIVILEGES FOR ROLE "+quoteIdent(owner)+" IN SCHEMA public REVOKE EXECUTE ON FUNCTIONS FROM "+quoteIdent(app))
	positive()
	const mid = "f2a_inventory_mid"
	runSQL("CREATE ROLE " + mid + " NOLOGIN NOINHERIT NOBYPASSRLS NOCREATEROLE NOCREATEDB NOREPLICATION")
	defer runSQL("DROP ROLE " + mid)
	cases := []struct {
		name       string
		set, reset []string
	}{
		{"wrong_owner", []string{"ALTER FUNCTION public.olivares_directory_inventory_v1() OWNER TO " + quoteIdent(owner)}, []string{"ALTER FUNCTION public.olivares_directory_inventory_v1() OWNER TO " + directoryInventoryOwner}},
		{"overload", []string{"CREATE FUNCTION public.olivares_directory_inventory_v1(text) RETURNS bigint LANGUAGE sql AS 'SELECT 1::bigint'"}, []string{"DROP FUNCTION public.olivares_directory_inventory_v1(text)"}},
		{"missing_column_grant", []string{"REVOKE SELECT(id) ON public.orgs FROM " + directoryInventoryOwner}, []string{"GRANT SELECT(id) ON public.orgs TO " + directoryInventoryOwner}},
		{"schema_create", []string{"GRANT CREATE ON SCHEMA public TO " + directoryInventoryOwner}, []string{"REVOKE CREATE ON SCHEMA public FROM " + directoryInventoryOwner}},
		{"administrative_execute", []string{"GRANT EXECUTE ON FUNCTION public.olivares_lock_core_user_authority(text) TO " + directoryInventoryOwner}, []string{"REVOKE EXECUTE ON FUNCTION public.olivares_lock_core_user_authority(text) FROM " + directoryInventoryOwner}},
		{"owner_login", []string{"ALTER ROLE " + directoryInventoryOwner + " LOGIN"}, []string{"ALTER ROLE " + directoryInventoryOwner + " NOLOGIN"}},
		{"owner_inherit", []string{"ALTER ROLE " + directoryInventoryOwner + " INHERIT"}, []string{"ALTER ROLE " + directoryInventoryOwner + " NOINHERIT"}},
		{"wide_column", []string{"GRANT SELECT(name) ON public.orgs TO " + directoryInventoryOwner}, []string{"REVOKE SELECT(name) ON public.orgs FROM " + directoryInventoryOwner}},
		{"table_select", []string{"GRANT SELECT ON public.orgs TO " + directoryInventoryOwner}, []string{"REVOKE SELECT ON public.orgs FROM " + directoryInventoryOwner, "GRANT SELECT(id,tenant_id) ON public.orgs TO " + directoryInventoryOwner}},
		{"column_write", []string{"GRANT UPDATE(id) ON public.orgs TO " + directoryInventoryOwner}, []string{"REVOKE UPDATE(id) ON public.orgs FROM " + directoryInventoryOwner}},
		{"body", []string{strings.Replace(strings.Replace(postgresDirectoryInventoryDDL, "CREATE FUNCTION", "CREATE OR REPLACE FUNCTION", 1), "NULL::pg_catalog.int8", "42::pg_catalog.int8", 1)}, []string{strings.Replace(postgresDirectoryInventoryDDL, "CREATE FUNCTION", "CREATE OR REPLACE FUNCTION", 1)}},
		{"search_path", []string{"ALTER FUNCTION public.olivares_directory_inventory_v1() SET search_path TO public,pg_catalog"}, []string{"ALTER FUNCTION public.olivares_directory_inventory_v1() SET search_path TO pg_catalog"}},
		{"PUBLIC_execute", []string{"GRANT EXECUTE ON FUNCTION public.olivares_directory_inventory_v1() TO PUBLIC"}, []string{"REVOKE EXECUTE ON FUNCTION public.olivares_directory_inventory_v1() FROM PUBLIC"}},
		{"indirect_SET_app_to_inventory", []string{"GRANT " + directoryInventoryOwner + " TO " + mid + " WITH INHERIT FALSE, SET TRUE", "GRANT " + mid + " TO " + quoteIdent(app) + " WITH INHERIT FALSE, SET TRUE"}, []string{"REVOKE " + mid + " FROM " + quoteIdent(app), "REVOKE " + directoryInventoryOwner + " FROM " + mid}},
		{"indirect_INHERIT_app_to_inventory", []string{"GRANT " + directoryInventoryOwner + " TO " + mid + " WITH INHERIT TRUE, SET FALSE", "GRANT " + mid + " TO " + quoteIdent(app) + " WITH INHERIT TRUE, SET FALSE"}, []string{"REVOKE " + mid + " FROM " + quoteIdent(app), "REVOKE " + directoryInventoryOwner + " FROM " + mid}},
		{"indirect_ADMIN_app_to_inventory", []string{"GRANT " + directoryInventoryOwner + " TO " + mid + " WITH ADMIN TRUE, INHERIT FALSE, SET FALSE", "GRANT " + mid + " TO " + quoteIdent(app) + " WITH SET TRUE, INHERIT FALSE"}, []string{"REVOKE " + mid + " FROM " + quoteIdent(app), "REVOKE " + directoryInventoryOwner + " FROM " + mid}},
		{"reverse_inventory_to_owner", []string{"GRANT " + quoteIdent(owner) + " TO " + mid + " WITH INHERIT FALSE, SET TRUE", "GRANT " + mid + " TO " + directoryInventoryOwner + " WITH INHERIT FALSE, SET TRUE"}, []string{"REVOKE " + mid + " FROM " + directoryInventoryOwner, "REVOKE " + quoteIdent(owner) + " FROM " + mid}},
		{"intermediary_admin", []string{"ALTER ROLE " + mid + " CREATEDB", "GRANT " + mid + " TO " + directoryInventoryOwner + " WITH INHERIT FALSE, SET TRUE"}, []string{"REVOKE " + mid + " FROM " + directoryInventoryOwner, "ALTER ROLE " + mid + " NOCREATEDB"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			runSQL(tc.set...)
			defer runSQL(tc.reset...)
			probe, err := openDB(cfg)
			if err != nil {
				t.Fatal(err)
			}
			_, probeErr := verifyPostgresDirectoryInventory(ctx, probe, s.directoryGuardRoles)
			probe.Close()
			if probeErr == nil {
				t.Fatal("inventory attestation admitted its committed drift")
			}
			raw, err := Open(ctx, cfg, nil)
			if raw != nil {
				raw.Close()
			}
			if err == nil {
				t.Fatal("committed inventory drift admitted ordinary Open")
			}
			if _, _, _, err := OpenDirectoryWriterMaintenance(ctx, cfg, nil, 1); err == nil {
				t.Fatal("committed inventory drift admitted maintenance")
			}
		})
	}
	// A membership with no SET, INHERIT or ADMIN edge is inert on PostgreSQL 16.
	runSQL("GRANT "+directoryInventoryOwner+" TO "+mid+" WITH INHERIT FALSE, SET FALSE, ADMIN FALSE", "GRANT "+mid+" TO "+quoteIdent(app)+" WITH INHERIT FALSE, SET TRUE")
	positive()
	runSQL("REVOKE "+mid+" FROM "+quoteIdent(app), "REVOKE "+directoryInventoryOwner+" FROM "+mid)
	positive()
}

// The target missing-H fixture uses the existing internal untracked typed repo
// deliberately, under a valid writer presentation. Product AuthScope never
// exports that repository; this models an inconsistent restored data set.
func TestUserAuthorityTargetMissingHRefusesWithoutRepair(t *testing.T) {
	for _, engine := range store.SupportedEngines() {
		t.Run(string(engine), func(t *testing.T) {
			ctx := context.Background()
			s, cfg, users, tenants := f2aFreshTarget(t, engine)
			var missing model.User
			if err := s.AuthMutate(ctx, func(as store.AuthScope) error {
				a := as.(*authScope)
				w := a.ts.directoryWriter
				if err := w.prepare(ctx, func() ([]model.TenantID, error) { return nil, nil }); err != nil {
					return err
				}
				repo := newTypedRepo(a.ts.repo(userDescriptor), userCodec)
				var err error
				missing, err = repo.Create(ctx, model.User{Email: "inconsistent-restore@example.test", Status: model.StatusActive})
				return err
			}); err != nil {
				t.Fatal(err)
			}
			if h := F2AUserAuthorityVersionForTest(t, s, missing.ID); h != 0 {
				t.Fatal("fixture did not establish missing H")
			}
			err := s.AuthMutate(ctx, func(as store.AuthScope) error { _, err := as.Users().Update(ctx, missing); return err })
			if err == nil {
				t.Fatal("normal target writer repaired missing H")
			}
			if h := F2AUserAuthorityVersionForTest(t, s, missing.ID); h != 0 {
				t.Fatal("normal writer created missing H")
			}
			err = s.Mutate(ctx, tenants[0], func(sc store.Scope) error {
				return sc.(store.AuthoritySnapshotBundleLocker).LockAuthoritySnapshotBundle(ctx, f2aBundle(tenants[0], missing.ID))
			})
			if err == nil {
				t.Fatal("bundle accepted absent H")
			}
			for _, oldMax := range []int{7, 8, 9} {
				if err := preflightCoreMigrationVersion(ctx, s.db, s.dia, oldMax); !errors.Is(err, ErrCoreSchemaVersionAhead) {
					t.Fatalf("max-v%d preflight=%v", oldMax, err)
				}
			}
			if _, _, changed, err := ActivateDirectoryWriter(ctx, s, cfg, 1); err == nil || changed {
				t.Fatalf("tuple-only target retry: changed=%t err=%v", changed, err)
			}
			s.Close()
			raw, err := Open(ctx, cfg, nil)
			if raw != nil {
				raw.Close()
			}
			if err == nil {
				t.Fatal("target reopen accepted missing H")
			}
			if _, _, _, err := OpenDirectoryWriterMaintenance(ctx, cfg, nil, 1); err == nil {
				t.Fatal("maintenance target retry repaired missing H")
			}
			state, h, _ := f2aPhysicalProof(t, cfg)
			if state.CoverageProtocol != coverageProtocolTarget || len(h.Missing) != 1 || h.Missing[0] != missing.ID || h.Rows[users[0].ID].Version != 1 {
				t.Fatal("missing-H refusal changed durable authority")
			}
		})
	}
}

func TestUserAuthorityNonemptyWithoutSystemRefuses(t *testing.T) {
	for _, engine := range store.SupportedEngines() {
		t.Run(string(engine), func(t *testing.T) {
			ctx := context.Background()
			cfg := store.Config{Engine: engine, DSN: filepath.Join(t.TempDir(), "no-system.db")}
			var super string
			if engine == store.EnginePostgres {
				pg := isolatedPGSplit(t)
				cfg.DSN, cfg.OwnerDSN, cfg.AdminDSN = pg.App, pg.Owner, pg.Admin
				super = pg.Superuser
			}
			raw, err := Open(ctx, cfg, nil)
			if err != nil {
				t.Fatal(err)
			}
			status, _, err := raw.(store.DirectoryStatuser).DirectoryStatus(ctx)
			if err != nil || status.EpochCoverageComplete || status.UserAuthorityCoverageComplete || status.InventoryUnavailableReason != "system_bootstrap_pending" {
				t.Fatalf("fresh non-ready status=%+v err=%v", status, err)
			}
			// The corrupt state under test: a business organization exists and SYSTEM
			// genesis never happened. The ordinary helper provisions SYSTEM first, so
			// this negative names the shortcut it takes on purpose.
			provisionTenantWithoutSystemWitness(t, raw, "deliberately-nonempty-before-system")
			if engine == store.EnginePostgres {
				f2aInstallInventoryForConfig(t, cfg, super)
				cfg.AdminDSN = ""
			}
			raw.Close()
			reopened, err := Open(ctx, cfg, nil)
			if reopened != nil {
				reopened.Close()
			}
			if err == nil {
				t.Fatal("nonempty missing SYSTEM was classified as fresh empty")
			}
			if _, _, _, err := OpenDirectoryWriterMaintenance(ctx, cfg, nil, 1); err == nil {
				t.Fatal("maintenance accepted missing SYSTEM")
			}
		})
	}
}

func TestUserAuthorityNoAdminInventoryDataRefusals(t *testing.T) {
	for _, scenario := range []string{"missing_routine", "missing_G", "orphan_G"} {
		t.Run(scenario, func(t *testing.T) {
			ctx := context.Background()
			s, cfg, _, tenants := f2aFreshTarget(t, store.EnginePostgres)
			f2aInstallInventoryForConfig(t, cfg, os.Getenv(pgtest.EnvSuperuserDSN))
			cfg.AdminDSN = ""
			if scenario == "missing_routine" {
				owner, err := openDB(store.Config{Engine: store.EnginePostgres, DSN: cfg.OwnerDSN})
				if err != nil {
					t.Fatal(err)
				}
				// Only the DBA can remove the isolated owner's function.
				var database string
				if err := s.db.QueryRowContext(ctx, "SELECT current_database()").Scan(&database); err != nil {
					t.Fatal(err)
				}
				parsed, err := pgx.ParseConfig(os.Getenv(pgtest.EnvSuperuserDSN))
				if err != nil {
					t.Fatal("fixture DSN")
				}
				super, closeDB, err := openOnDatabase(parsed, database)
				if err != nil {
					t.Fatal(err)
				}
				_, err = super.ExecContext(ctx, "DROP FUNCTION public.olivares_directory_inventory_v1()")
				closeDB()
				owner.Close()
				if err != nil {
					t.Fatal(err)
				}
			} else {
				tx, err := s.db.BeginTx(ctx, nil)
				if err != nil {
					t.Fatal(err)
				}
				defer tx.Rollback()
				tenant := tenants[0]
				if scenario == "orphan_G" {
					tenant = model.TenantID(model.NewID())
				}
				if err := s.dia.BindTenant(ctx, tx, tenant); err != nil {
					t.Fatal(err)
				}
				if scenario == "missing_G" {
					_, err = tx.ExecContext(ctx, "DELETE FROM public.core_directory_epoch WHERE id=$1", tenant.String())
				} else {
					now := model.NewTimestamp(time.Now()).String()
					_, err = tx.ExecContext(ctx, "INSERT INTO public.core_directory_epoch(id,tenant_id,created_at,updated_at,version) VALUES($1,$1,$2,$2,1)", tenant.String(), now)
				}
				if err != nil {
					t.Fatal(err)
				}
				if err := tx.Commit(); err != nil {
					t.Fatal(err)
				}
			}
			s.Close()
			raw, err := Open(ctx, cfg, nil)
			if raw != nil {
				raw.Close()
			}
			if err == nil {
				t.Fatal("noAdmin target Open used a partial census")
			}
			if _, _, _, err := OpenDirectoryWriterMaintenance(ctx, cfg, nil, 1); err == nil {
				t.Fatal("noAdmin target retry repaired inventory")
			}
		})
	}
}
