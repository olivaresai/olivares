// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/internal/pgtest"
	"github.com/olivaresai/olivares/core/internal/store/dialect"
	"github.com/olivaresai/olivares/core/store"
)

const restoreAuthorityFunctions = `n.nspname='public' AND p.proname IN ('olivares_lock_core_user_authority','olivares_retain_user_authority')`

// A catalog perturbation isolates the exact NULL ACL measured after real
// pg_restore --no-privileges. TestDRPostgresRoundTripAcrossBothPostures exercises
// the actual dump/restore/caller; this test targets refusals and atomicity using
// the same functions without paying for a dump for every negative.
func TestPostgresRestoreUserAuthorityClosure(t *testing.T) {
	for _, split := range []bool{false, true} {
		name := "single"
		if split {
			name = "split"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			var pg pgtest.DSNs
			if split {
				pg = isolatedPGSplit(t)
			} else {
				pg = isolatedPG(t)
			}
			cfg := store.Config{Engine: store.EnginePostgres, DSN: pg.App, AdminDSN: pg.Admin, MaxConns: 1}
			if split {
				cfg.OwnerDSN = pg.Owner
			}
			st, err := Open(ctx, cfg, nil)
			if err != nil {
				t.Fatal(err)
			}
			if err := st.Close(); err != nil {
				t.Fatal(err)
			}
			owner, err := openPGPinnedToEngineSchema(pg.Owner, 2)
			if err != nil {
				t.Fatal(err)
			}
			defer owner.Close()
			super, err := openPGPinnedToEngineSchema(pg.Superuser, 2)
			if err != nil {
				t.Fatal(err)
			}
			defer super.Close()
			var ownerRole, appRole, adminRole string
			if err := owner.QueryRow("SELECT current_user").Scan(&ownerRole); err != nil {
				t.Fatal(err)
			}
			appDB, err := openPGPinnedToEngineSchema(pg.App, 1)
			if err != nil {
				t.Fatal(err)
			}
			if err := appDB.QueryRow("SELECT current_user").Scan(&appRole); err != nil {
				t.Fatal(err)
			}
			appDB.Close()
			adminDB, err := openPGPinnedToEngineSchema(pg.Admin, 1)
			if err != nil {
				t.Fatal(err)
			}
			if err := adminDB.QueryRow("SELECT current_user").Scan(&adminRole); err != nil {
				t.Fatal(err)
			}
			adminDB.Close()
			stripped := func() {
				mustExec(t, super, `UPDATE pg_catalog.pg_proc p SET proacl=NULL FROM pg_catalog.pg_namespace n WHERE n.oid=p.pronamespace AND `+restoreAuthorityFunctions)
			}
			snapshot := func() string { return restoreAuthoritySnapshot(t, super) }
			original := snapshot()
			stripped()
			before := snapshot()
			st, err = Open(ctx, cfg, nil)
			if st != nil {
				st.Close()
			}
			if err == nil || !strings.Contains(err.Error(), "signature/owner/ACL is not closed") {
				t.Fatalf("ordinary Open repaired or misclassified stripped ACL: %v", err)
			}
			if snapshot() != before {
				t.Fatal("ordinary Open altered the stripped functions")
			}
			// AdminDSN is deliberately unusable: only owner and app participate.
			closureCfg := cfg
			closureCfg.AdminDSN = "not-a-connection-string"
			if err := RestorePostgresUserAuthorityPrivileges(ctx, closureCfg); err != nil {
				t.Fatal(err)
			}
			if snapshot() != original {
				t.Fatal("closure did not restore exact source owner/definition/config/ACL")
			}
			if err := RestorePostgresUserAuthorityPrivileges(ctx, closureCfg); err != nil {
				t.Fatal(err)
			}
			if snapshot() != original {
				t.Fatal("idempotent closure changed functions")
			}
			st, err = Open(ctx, cfg, nil)
			if err != nil {
				t.Fatalf("closed reopen: %v", err)
			}
			st.Close()

			t.Run("rollback_after_both_acl_writes", func(t *testing.T) {
				stripped()
				before := snapshot()
				injected := errors.New("injected precommit failure")
				reached := false
				postgresRestoreAuthorityCommitTestHook = func(tx *sql.Tx) error {
					for _, name := range []string{"olivares_lock_core_user_authority", "olivares_retain_user_authority"} {
						acl, err := readPostgresAuthorityFunctionACL(ctx, tx, name, ownerRole, appRole)
						if err != nil || !acl.closed() {
							t.Fatalf("hook preceded complete closure: %+v %v", acl, err)
						}
					}
					reached = true
					return injected
				}
				t.Cleanup(func() { postgresRestoreAuthorityCommitTestHook = nil })
				err := RestorePostgresUserAuthorityPrivileges(ctx, closureCfg)
				postgresRestoreAuthorityCommitTestHook = nil
				if !reached || !errors.Is(err, injected) {
					t.Fatalf("injection not reached: %v", err)
				}
				if snapshot() != before {
					t.Fatal("failed closure committed either function ACL")
				}
			})

			t.Run("global_writer_serialization", func(t *testing.T) {
				stripped()
				before := snapshot()
				tx, err := owner.BeginTx(ctx, nil)
				if err != nil {
					t.Fatal(err)
				}
				defer tx.Rollback()
				if _, err := tx.ExecContext(ctx, `SELECT pg_catalog.pg_advisory_xact_lock(pg_catalog.hashtextextended($1,0))`, directoryWriterLockKey); err != nil {
					t.Fatal(err)
				}
				blocked, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
				defer cancel()
				if err := RestorePostgresUserAuthorityPrivileges(blocked, closureCfg); err == nil {
					t.Fatal("closure bypassed held directory writer lock")
				}
				if blocked.Err() == nil {
					t.Fatal("closure refused before attempting serialized acquisition")
				}
				if snapshot() != before {
					t.Fatal("blocked closure changed ACL")
				}
			})
			t.Run("different_database", func(t *testing.T) {
				stripped()
				before := snapshot()
				other := isolatedPG(t)
				wrong := closureCfg
				wrong.OwnerDSN = pg.Owner
				wrong.DSN = other.App
				err := RestorePostgresUserAuthorityPrivileges(ctx, wrong)
				if err == nil || !strings.Contains(err.Error(), "database") {
					t.Fatalf("wrong-database witness was accepted: %v", err)
				}
				if snapshot() != before {
					t.Fatal("wrong-database refusal changed ACL")
				}
			})
			t.Run("privileged_role_is_not_waived", func(t *testing.T) {
				stripped()
				before := snapshot()
				wrong := closureCfg
				wrong.DSN = pg.Superuser
				wrong.OwnerDSN = pg.Owner
				wrong.AllowPrivilegedRole = true
				err := RestorePostgresUserAuthorityPrivileges(ctx, wrong)
				if err == nil || !strings.Contains(err.Error(), "unsafe posture") {
					t.Fatalf("privileged application role was accepted: %v", err)
				}
				if snapshot() != before {
					t.Fatal("privileged-role refusal changed ACL")
				}
			})

			// Each negative starts with both ACLs stripped. Altering either compiled
			// definition must refuse before the first ACL can be changed.
			cases := []struct{ name, mutate, undo, want string }{
				{"lock_config", "ALTER FUNCTION public.olivares_lock_core_user_authority(text) SET search_path=public", "ALTER FUNCTION public.olivares_lock_core_user_authority(text) SET search_path=pg_catalog", "signature/owner"},
				{"retention_overload", "CREATE FUNCTION public.olivares_retain_user_authority(text) RETURNS boolean LANGUAGE sql AS 'SELECT true'", "DROP FUNCTION public.olivares_retain_user_authority(text)", "overload"},
				{"retention_security", "ALTER FUNCTION public.olivares_retain_user_authority() SECURITY DEFINER", "ALTER FUNCTION public.olivares_retain_user_authority() SECURITY INVOKER", "definition drift"},
				{"retention_body", strings.Replace(postgresUserAuthorityRetentionDDL, "CREATE FUNCTION", "CREATE OR REPLACE FUNCTION", 1), strings.Replace(postgresUserAuthorityRetentionDDL, "CREATE FUNCTION", "CREATE OR REPLACE FUNCTION", 1), "definition drift"},
				{"retention_owner", "ALTER FUNCTION public.olivares_retain_user_authority() OWNER TO " + quoteIdent(adminRole), "ALTER FUNCTION public.olivares_retain_user_authority() OWNER TO " + quoteIdent(ownerRole), "signature/owner"},
				{"foreign_grant", "GRANT EXECUTE ON FUNCTION public.olivares_retain_user_authority() TO " + quoteIdent(adminRole), "REVOKE ALL ON FUNCTION public.olivares_retain_user_authority() FROM " + quoteIdent(adminRole), "neither stripped nor closed"},
				{"effective_role_path", "GRANT " + quoteIdent(appRole) + " TO " + quoteIdent(adminRole), "REVOKE " + quoteIdent(appRole) + " FROM " + quoteIdent(adminRole), "role closure"},
				// One version above this binary's supported ceiling: the fixture used to
				// hard-code 11, which core v11 now records as a real history row.
				{"future_tracking", "INSERT INTO public.schema_migrations_core(version,name,applied_at) VALUES (" + strconv.Itoa(coreSupportedMigrationVersion+1) + ",'future','2026-09-07T00:00:00Z')", "DELETE FROM public.schema_migrations_core WHERE version=" + strconv.Itoa(coreSupportedMigrationVersion+1), "incompatible"},
				{"renamed_tracking", "UPDATE public.schema_migrations_core SET name='other' WHERE version=10", "UPDATE public.schema_migrations_core SET name='user_authority' WHERE version=10", "exact active record"},
				{"missing_tracking", "DELETE FROM public.schema_migrations_core WHERE version=4", "INSERT INTO public.schema_migrations_core(version,name,applied_at) VALUES (4,'federation_multi_idp','2026-09-07T00:00:00Z')", "recognized prefix"},
			}
			for i := range cases {
				if cases[i].name == "retention_body" {
					cases[i].mutate = strings.Replace(cases[i].mutate, "User authority is permanent", "changed body", 1)
				}
			}
			for _, tc := range cases {
				t.Run(tc.name, func(t *testing.T) {
					stripped()
					mustExec(t, super, tc.mutate)
					t.Cleanup(func() { mustExec(t, super, tc.undo) })
					before := snapshot()
					err := RestorePostgresUserAuthorityPrivileges(ctx, closureCfg)
					if err == nil || !strings.Contains(err.Error(), tc.want) {
						t.Fatalf("wanted %q refusal, got %v", tc.want, err)
					}
					if snapshot() != before {
						t.Fatal("refusal changed an authority function")
					}
				})
			}
			t.Run("rollback_failure_preserves_primary", func(t *testing.T) {
				stripped()
				before := snapshot()
				injected := errors.New("injected primary closure failure")
				postgresRestoreAuthorityCommitTestHook = func(tx *sql.Tx) error {
					terminateRestoreAuthorityBackend(t, ctx, super, tx)
					return injected
				}
				t.Cleanup(func() { postgresRestoreAuthorityCommitTestHook = nil })
				err := RestorePostgresUserAuthorityPrivileges(ctx, closureCfg)
				postgresRestoreAuthorityCommitTestHook = nil
				if !errors.Is(err, injected) {
					t.Fatalf("primary closure failure was lost: %v", err)
				}
				if !strings.Contains(err.Error(), "rollback could not be confirmed") {
					t.Fatalf("rollback failure was lost: %v", err)
				}
				if snapshot() != before {
					t.Fatal("terminated closure committed either function ACL")
				}
			})
			t.Run("commit_failure_remains_ambiguous", func(t *testing.T) {
				stripped()
				before := snapshot()
				postgresRestoreAuthorityCommitTestHook = func(tx *sql.Tx) error {
					terminateRestoreAuthorityBackend(t, ctx, super, tx)
					return nil
				}
				t.Cleanup(func() { postgresRestoreAuthorityCommitTestHook = nil })
				err := RestorePostgresUserAuthorityPrivileges(ctx, closureCfg)
				postgresRestoreAuthorityCommitTestHook = nil
				if err == nil {
					t.Fatal("terminated commit returned success")
				}
				if strings.Contains(err.Error(), "rollback could not be confirmed") {
					t.Fatalf("commit error was replaced by a rollback claim: %v", err)
				}
				if snapshot() != before {
					t.Fatal("terminated commit changed either function ACL")
				}
			})
			stripped()
			if err := RestorePostgresUserAuthorityPrivileges(ctx, closureCfg); err != nil {
				t.Fatal(err)
			}
			t.Log("both NULL ACLs -> exact source ACLs; ordinary Open refusal; rollback and exact negative prestates measured")
		})
	}
}

func restoreAuthoritySnapshot(t *testing.T, db *sql.DB) string {
	t.Helper()
	var snapshot string
	err := db.QueryRow(`SELECT jsonb_agg(jsonb_build_array(p.proname,p.proowner,p.proacl::text,pg_catalog.pg_get_functiondef(p.oid)) ORDER BY p.proname)::text
 FROM pg_catalog.pg_proc p JOIN pg_catalog.pg_namespace n ON n.oid=p.pronamespace WHERE ` + restoreAuthorityFunctions).Scan(&snapshot)
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

// terminateRestoreAuthorityBackend waits for PostgreSQL to terminate the exact
// backend that owns tx. The fixture superuser belongs to the disposable test
// cluster; production never receives this capability.
func terminateRestoreAuthorityBackend(t *testing.T, ctx context.Context, super *sql.DB, tx *sql.Tx) {
	t.Helper()
	var pid int
	if err := tx.QueryRowContext(ctx, "SELECT pg_catalog.pg_backend_pid()").Scan(&pid); err != nil {
		t.Fatalf("read restore transaction backend pid: %v", err)
	}
	var terminated bool
	if err := super.QueryRowContext(ctx,
		"SELECT pg_catalog.pg_terminate_backend($1, 5000)", pid,
	).Scan(&terminated); err != nil {
		t.Fatalf("terminate restore transaction backend %d: %v", pid, err)
	}
	if !terminated {
		t.Fatalf("restore transaction backend %d was not terminated", pid)
	}
}

func TestPostgresRestoreUserAuthorityLegacyPredecessor(t *testing.T) {
	ctx := context.Background()
	cfg, _, superDSN := F2AOldFixtureForTest(t, store.EnginePostgres, "staged", false)
	owner, err := openPGPinnedToEngineSchema(cfg.OwnerDSN, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	observer, err := openPGPinnedToEngineSchema(superDSN, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer observer.Close()
	before := pgLogicalSnapshot(t, observer, "public")
	t.Run("rollback_failure_is_not_success", func(t *testing.T) {
		postgresRestoreAuthorityLegacyRollbackTestHook = func(tx *sql.Tx) error {
			terminateRestoreAuthorityBackend(t, ctx, observer, tx)
			return nil
		}
		t.Cleanup(func() { postgresRestoreAuthorityLegacyRollbackTestHook = nil })
		err := RestorePostgresUserAuthorityPrivileges(ctx, cfg)
		postgresRestoreAuthorityLegacyRollbackTestHook = nil
		if err == nil || !strings.Contains(err.Error(), "close legacy logical restore authority transaction") {
			t.Fatalf("legacy rollback failure returned success or lost its cause: %v", err)
		}
		if got := pgLogicalSnapshot(t, observer, "public"); got != before {
			t.Fatal("failed legacy rollback changed predecessor state")
		}
	})
	if err := RestorePostgresUserAuthorityPrivileges(ctx, cfg); err != nil {
		t.Fatalf("source-native v9 no-op: %v", err)
	}
	if got := pgLogicalSnapshot(t, observer, "public"); got != before {
		t.Fatal("pre-H closure changed legacy state")
	}
	mustExec(t, owner, "CREATE TABLE public.core_user_authority(id text)")
	mixed := pgLogicalSnapshot(t, observer, "public")
	err = RestorePostgresUserAuthorityPrivileges(ctx, cfg)
	if err == nil || !strings.Contains(err.Error(), "mixed User authority") {
		t.Fatalf("mixed predecessor accepted: %v", err)
	}
	if got := pgLogicalSnapshot(t, observer, "public"); got != mixed {
		t.Fatal("mixed refusal changed state")
	}
	mustExec(t, owner, "DROP TABLE public.core_user_authority")
	st, err := Open(ctx, cfg, nil)
	if err != nil {
		t.Fatalf("legacy upgrade after no-op: %v", err)
	}
	st.Close()
	var version int
	if err := owner.QueryRow("SELECT version FROM public.schema_migrations_core WHERE name='user_authority'").Scan(&version); err != nil || version != 10 {
		t.Fatalf("upgrade v%d: %v", version, err)
	}
	t.Log("source-native v9 unchanged by closure; mixed H refused unchanged; ordinary upgrade reached v10")
}

func TestPostgresRestoreUserAuthorityHistoricalTracking(t *testing.T) {
	ctx := context.Background()
	pg := isolatedPG(t)
	db, err := openPGPinnedToEngineSchema(pg.Owner, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	dia, _ := dialect.New(store.EnginePostgres)
	for _, stmt := range dia.TenancyStmts() {
		mustExec(t, db, stmt)
	}
	mustExec(t, db, "CREATE TABLE public.schema_migrations_core(version integer PRIMARY KEY,name text NOT NULL,applied_at text NOT NULL)")
	mustExec(t, db, "INSERT INTO public.schema_migrations_core VALUES(1,'tenancy','2026-01-01T00:00:00Z')")
	cfg := store.Config{Engine: store.EnginePostgres, DSN: pg.App, MaxConns: 1}
	before := pgLogicalSnapshot(t, db, "public")
	if err := RestorePostgresUserAuthorityPrivileges(ctx, cfg); err != nil {
		t.Fatalf("historical three-column v1: %v", err)
	}
	if got := pgLogicalSnapshot(t, db, "public"); got != before {
		t.Fatal("historical closure mutated state")
	}
}
