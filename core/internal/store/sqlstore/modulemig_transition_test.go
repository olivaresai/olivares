// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/olivaresai/olivares/core/internal/pgtest"
	"github.com/olivaresai/olivares/core/internal/store/dialect"
	"github.com/olivaresai/olivares/core/store"
)

const (
	transitionTestNamespace = "transitiontest"
	transitionTestTable     = "transitiontest_fact"
	transitionTestTable2    = "transitiontest_note"
	transitionTestTrigger   = "transitiontest_guard"
)

func TestPrepareModuleMigrationsRejectsMissingOrDuplicateTransitionVersion(t *testing.T) {
	dia, ok := dialect.New(store.EngineSQLite)
	if !ok {
		t.Fatal("SQLite dialect unavailable")
	}
	oldDigest := strings.Repeat("a", 64)
	newDigest := strings.Repeat("b", 64)
	for _, tc := range []struct {
		name string
		fsys fstest.MapFS
		want string
	}{
		{
			name: "missing",
			fsys: fstest.MapFS{
				"sqlite/0001_initial.sql": &fstest.MapFile{Data: []byte("SELECT 1")},
			},
			want: "found 0",
		},
		{
			name: "duplicate",
			fsys: fstest.MapFS{
				"sqlite/0002_first.sql":  &fstest.MapFile{Data: []byte("SELECT 1")},
				"sqlite/0002_second.sql": &fstest.MapFile{Data: []byte("SELECT 2")},
			},
			want: "found 2",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newRegistry()
			if err := r.Migrations(transitionTestNamespace, tc.fsys); err != nil {
				t.Fatal(err)
			}
			trigger := store.SchemaTrigger{
				Name: transitionTestTrigger, Table: transitionTestTable,
				DefinitionSHA256: newDigest,
				Transitions: []store.SchemaTriggerTransition{{
					MigrationVersion: 2, PreviousDefinitionSHA256: oldDigest,
				}},
			}
			if err := r.SchemaInvariants(transitionTestNamespace, bothEngines(
				[]store.SchemaTrigger{trigger},
				[]store.SchemaTrigger{withPostgresTransitionIdentities(trigger)},
			)); err != nil {
				t.Fatal(err)
			}
			if _, err := prepareModuleFileMigrations(dia, store.EngineSQLite, r); err == nil ||
				!strings.Contains(err.Error(), tc.want) {
				t.Fatalf("prepare error = %v, want refusal containing %q", err, tc.want)
			}
		})
	}
}

func TestPrepareModuleMigrationsRejectsTransitionWithoutMigrationFilesystem(t *testing.T) {
	dia, ok := dialect.New(store.EngineSQLite)
	if !ok {
		t.Fatal("SQLite dialect unavailable")
	}
	r := newRegistry()
	trigger := store.SchemaTrigger{
		Name: transitionTestTrigger, Table: transitionTestTable,
		DefinitionSHA256: strings.Repeat("b", 64),
		Transitions: []store.SchemaTriggerTransition{{
			MigrationVersion: 2, PreviousDefinitionSHA256: strings.Repeat("a", 64),
		}},
	}
	if err := r.SchemaInvariants(transitionTestNamespace, bothEngines(
		[]store.SchemaTrigger{trigger},
		[]store.SchemaTrigger{withPostgresTransitionIdentities(trigger)},
	)); err != nil {
		t.Fatal(err)
	}
	if _, err := prepareModuleFileMigrations(dia, store.EngineSQLite, r); err == nil ||
		!strings.Contains(err.Error(), "registered no migration filesystem") {
		t.Fatalf("prepare error = %v, want missing-filesystem refusal", err)
	}
}

func TestSQLiteSchemaTransitionCommitsOnlyAnExactPostcondition(t *testing.T) {
	oldDDL := "CREATE TRIGGER " + transitionTestTrigger + " BEFORE DELETE ON " +
		transitionTestTable + " BEGIN SELECT RAISE(ABORT, 'old'); END"
	newDDL := "CREATE TRIGGER " + transitionTestTrigger + " BEFORE DELETE ON " +
		transitionTestTable + " BEGIN SELECT RAISE(ABORT, 'new'); END"
	mutantDDL := "CREATE TRIGGER " + transitionTestTrigger + " BEFORE DELETE ON " +
		transitionTestTable + " BEGIN SELECT RAISE(ABORT, 'mutant'); END"
	oldDigest := sqliteTransitionDigest(t, oldDDL)
	newDigest := sqliteTransitionDigest(t, newDDL)

	for _, tc := range []struct {
		name        string
		replacement string
		wantErr     bool
		wantDigest  string
		wantLedger  int
	}{
		{name: "exact", replacement: newDDL, wantDigest: newDigest, wantLedger: 2},
		{name: "mutant rolls back", replacement: mutantDDL, wantErr: true, wantDigest: oldDigest, wantLedger: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			db := openTransitionSQLite(t)
			if _, err := db.ExecContext(ctx,
				"CREATE TABLE "+transitionTestTable+" (id INTEGER PRIMARY KEY)"); err != nil {
				t.Fatal(err)
			}
			files := fstest.MapFS{
				"sqlite/0001_old.sql": &fstest.MapFile{Data: []byte(oldDDL)},
				"sqlite/0002_replace.sql": &fstest.MapFile{Data: []byte(
					"DROP TRIGGER " + transitionTestTrigger + "; " + tc.replacement,
				)},
			}
			r := transitionRegistry(t, files, []store.SchemaTrigger{{
				Name: transitionTestTrigger, Table: transitionTestTable,
				DefinitionSHA256: newDigest,
				Transitions: []store.SchemaTriggerTransition{{
					MigrationVersion: 2, PreviousDefinitionSHA256: oldDigest,
				}},
			}})
			dia, _ := dialect.New(store.EngineSQLite)
			plans, err := prepareModuleFileMigrations(dia, store.EngineSQLite, r)
			if err != nil {
				t.Fatalf("prepare: %v", err)
			}
			err = applyModuleFileMigrations(ctx, db, dia, plans)
			if tc.wantErr {
				if !errors.Is(err, store.ErrSchemaTriggerTampered) {
					t.Fatalf("apply error = %v, want tampered postcondition", err)
				}
			} else if err != nil {
				t.Fatalf("apply: %v", err)
			}
			if got := liveTransitionDigest(t, db, dia, transitionTestTable, transitionTestTrigger); got != tc.wantDigest {
				t.Fatalf("live digest = %s, want %s", got, tc.wantDigest)
			}
			if got := countRows(t, db,
				"SELECT COUNT(*) FROM schema_migrations_mod_"+transitionTestNamespace); got != tc.wantLedger {
				t.Fatalf("tracking rows = %d, want %d", got, tc.wantLedger)
			}
		})
	}
}

func TestSQLiteSchemaTransitionReservesTheWriterBeforeCatalogProjection(t *testing.T) {
	ctx := context.Background()
	dsn := filepath.Join(t.TempDir(), "transition-lock.db")
	db, err := openSQLite(dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	oldDDL := "CREATE TRIGGER " + transitionTestTrigger + " BEFORE DELETE ON " +
		transitionTestTable + " BEGIN SELECT RAISE(ABORT, 'old'); END"
	for _, stmt := range []string{
		"CREATE TABLE " + transitionTestTable + " (id INTEGER PRIMARY KEY)",
		"CREATE TABLE schema_migrations_mod_" + transitionTestNamespace +
			" (version INTEGER PRIMARY KEY, name TEXT NOT NULL, applied_at TEXT NOT NULL, phase TEXT NOT NULL, reverted_at TEXT)",
		oldDDL,
	} {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			t.Fatal(err)
		}
	}
	dia, _ := dialect.New(store.EngineSQLite)
	oldDigest := liveTransitionDigest(t, db, dia, transitionTestTable, transitionTestTrigger)
	hook := &schemaTransitionHook{
		dia: dia, engine: store.EngineSQLite, namespace: transitionTestNamespace,
		trackingTable: "schema_migrations_mod_" + transitionTestNamespace,
		version:       2,
		specs: []schemaTransitionSpec{{
			table: transitionTestTable, name: transitionTestTrigger,
			previous: oldDigest, next: strings.Repeat("b", 64),
		}},
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := hook.before(ctx, tx); err != nil {
		_ = tx.Rollback()
		t.Fatalf("Before: %v", err)
	}

	contender, err := sql.Open("sqlite", dsn)
	if err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	contender.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = contender.Close() })
	if _, err := contender.ExecContext(ctx, "PRAGMA busy_timeout=50"); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if _, err := contender.ExecContext(ctx, "DROP TRIGGER "+transitionTestTrigger); err == nil {
		_ = tx.Rollback()
		t.Fatal("a concurrent writer replaced the trigger after Before projected it")
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if _, err := contender.ExecContext(ctx, "DROP TRIGGER "+transitionTestTrigger); err != nil {
		t.Fatalf("contender remained unable to write after the transition transaction released: %v", err)
	}
}

func TestPostgresSchemaTransitionMovesOnlyTheSelectedSharedCaller(t *testing.T) {
	requireTransitionPostgres(t)
	pg := isolatedPG(t)
	db := openTransitionPostgres(t, pg.Owner)
	ctx := context.Background()
	dia := transitionPostgresDialect(t)
	const (
		oldFunction    = "transitiontest_shared_old_fn"
		nextFunction   = "transitiontest_command_v2_fn"
		foreignSchema  = "transitiontest_foreign"
		foreignTable   = "transitiontest_foreign_fact"
		foreignTrigger = "transitiontest_foreign_guard"
	)
	postgresMustExec(t, db,
		"CREATE TABLE "+quoteIdent(transitionTestTable)+" (id bigint PRIMARY KEY)",
		"CREATE SCHEMA "+quoteIdent(foreignSchema),
		"CREATE TABLE "+quoteIdent(foreignSchema)+"."+quoteIdent(foreignTable)+" (id bigint PRIMARY KEY)",
		postgresTransitionFunctionDDL(oldFunction, "OLD"),
		postgresTriggerDDL("public", transitionTestTable, transitionTestTrigger, "public", oldFunction),
		postgresTriggerDDL(foreignSchema, foreignTable, foreignTrigger, "public", oldFunction),
	)
	oldDigest := liveTransitionDigest(t, db, dia, transitionTestTable, transitionTestTrigger)
	oldFunctionInfo := livePostgresTransitionFunction(t, db, dia, "public", oldFunction)
	oldCallers := livePostgresTransitionCallers(t, db, dia, "public", oldFunction)
	foreignKey := dialect.TriggerKey{Schema: foreignSchema, Table: foreignTable, Name: foreignTrigger}
	foreignBefore, ok := oldCallers[foreignKey]
	if !ok {
		t.Fatalf("cross-schema old caller %s was not projected", foreignKey)
	}
	newDigest := measurePostgresIdentityDigest(
		t, db, dia, transitionTestTable, transitionTestTrigger, oldFunction, nextFunction,
	)

	files := postgresIdentityMigrationFiles(
		transitionTestTable, transitionTestTrigger, nextFunction,
	)
	r := transitionRegistry(t, files, []store.SchemaTrigger{
		postgresIdentityTransition(
			transitionTestTable, transitionTestTrigger,
			oldDigest, newDigest, oldFunction, nextFunction,
		),
	})
	plans, err := prepareModuleFileMigrations(dia, store.EnginePostgres, r)
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if err := applyModuleFileMigrations(ctx, db, dia, plans); err != nil {
		t.Fatalf("apply identity transition: %v", err)
	}
	if got := liveTransitionDigest(t, db, dia, transitionTestTable, transitionTestTrigger); got != newDigest {
		t.Fatalf("selected trigger digest = %s, want %s", got, newDigest)
	}
	if got := livePostgresTransitionFunction(t, db, dia, "public", oldFunction); got != oldFunctionInfo {
		t.Fatalf("old shared function changed: got=%+v want=%+v", got, oldFunctionInfo)
	}
	oldAfter := livePostgresTransitionCallers(t, db, dia, "public", oldFunction)
	if got, ok := oldAfter[foreignKey]; !ok || got != foreignBefore {
		t.Fatalf("cross-schema old caller changed: got=%+v present=%t want=%+v", got, ok, foreignBefore)
	}
	nextAfter := livePostgresTransitionCallers(t, db, dia, "public", nextFunction)
	selectedKey := dialect.TriggerKey{Schema: "public", Table: transitionTestTable, Name: transitionTestTrigger}
	if len(nextAfter) != 1 || nextAfter[selectedKey].FunctionName != nextFunction {
		t.Fatalf("new function callers = %+v, want only %s", nextAfter, selectedKey)
	}
	if got := countRows(t, db,
		"SELECT COUNT(*) FROM schema_migrations_mod_"+transitionTestNamespace); got != 1 {
		t.Fatalf("tracking rows = %d, want 1", got)
	}
}

func TestPostgresSchemaTransitionRejectsPreexistingNextFunction(t *testing.T) {
	requireTransitionPostgres(t)
	pg := isolatedPG(t)
	db := openTransitionPostgres(t, pg.Owner)
	ctx := context.Background()
	dia := transitionPostgresDialect(t)
	const (
		oldFunction  = "transitiontest_preexisting_old_fn"
		nextFunction = "transitiontest_preexisting_next_fn"
	)
	postgresMustExec(t, db,
		"CREATE TABLE "+quoteIdent(transitionTestTable)+" (id bigint PRIMARY KEY)",
		postgresTransitionFunctionDDL(oldFunction, "OLD"),
		postgresTriggerDDL("public", transitionTestTable, transitionTestTrigger, "public", oldFunction),
	)
	oldDigest := liveTransitionDigest(t, db, dia, transitionTestTable, transitionTestTrigger)
	newDigest := measurePostgresIdentityDigest(
		t, db, dia, transitionTestTable, transitionTestTrigger, oldFunction, nextFunction,
	)
	postgresMustExec(t, db, postgresTransitionFunctionDDL(nextFunction, "PREEXISTING"))
	nextBefore := livePostgresTransitionFunction(t, db, dia, "public", nextFunction)
	files := postgresIdentityMigrationFiles(
		transitionTestTable, transitionTestTrigger, nextFunction,
	)
	r := transitionRegistry(t, files, []store.SchemaTrigger{
		postgresIdentityTransition(
			transitionTestTable, transitionTestTrigger,
			oldDigest, newDigest, oldFunction, nextFunction,
		),
	})
	plans, err := prepareModuleFileMigrations(dia, store.EnginePostgres, r)
	if err != nil {
		t.Fatal(err)
	}
	err = applyModuleFileMigrations(ctx, db, dia, plans)
	if err == nil || !strings.Contains(err.Error(), "to be absent before reservation") {
		t.Fatalf("apply error = %v, want pre-existing-next refusal", err)
	}
	if got := livePostgresTransitionFunction(t, db, dia, "public", nextFunction); got != nextBefore {
		t.Fatalf("pre-existing next function changed: got=%+v want=%+v", got, nextBefore)
	}
	assertPostgresTransitionRolledBack(t, db, dia, oldDigest)
}

func TestPostgresSchemaTransitionRollsBackReservationWhenMigrationSQLFails(t *testing.T) {
	requireTransitionPostgres(t)
	pg := isolatedPG(t)
	db := openTransitionPostgres(t, pg.Owner)
	ctx := context.Background()
	dia := transitionPostgresDialect(t)
	const (
		oldFunction  = "transitiontest_failure_old_fn"
		nextFunction = "transitiontest_failure_next_fn"
	)
	postgresMustExec(t, db,
		"CREATE TABLE "+quoteIdent(transitionTestTable)+" (id bigint PRIMARY KEY)",
		postgresTransitionFunctionDDL(oldFunction, "OLD"),
		postgresTriggerDDL("public", transitionTestTable, transitionTestTrigger, "public", oldFunction),
	)
	oldDigest := liveTransitionDigest(t, db, dia, transitionTestTable, transitionTestTrigger)
	newDigest := measurePostgresIdentityDigest(
		t, db, dia, transitionTestTable, transitionTestTrigger, oldFunction, nextFunction,
	)
	broken := postgresTransitionFunctionDDL(nextFunction, "NEW") +
		"; SELECT * FROM transitiontest_deliberately_missing"
	files := fstest.MapFS{
		"postgres/0002_identity.sql": &fstest.MapFile{Data: []byte(broken)},
	}
	r := transitionRegistry(t, files, []store.SchemaTrigger{
		postgresIdentityTransition(
			transitionTestTable, transitionTestTrigger,
			oldDigest, newDigest, oldFunction, nextFunction,
		),
	})
	plans, err := prepareModuleFileMigrations(dia, store.EnginePostgres, r)
	if err != nil {
		t.Fatal(err)
	}
	if err := applyModuleFileMigrations(ctx, db, dia, plans); err == nil {
		t.Fatal("migration with failing SQL committed")
	}
	if _, exists := lookupPostgresTransitionFunction(t, db, dia, "public", nextFunction); exists {
		t.Fatal("reserved next function survived migration rollback")
	}
	assertPostgresTransitionRolledBack(t, db, dia, oldDigest)
}

func TestPostgresSchemaTransitionRecordsLedgerInPublicDespiteTempShadow(t *testing.T) {
	requireTransitionPostgres(t)
	pg := isolatedPG(t)
	db := openTransitionPostgres(t, pg.Owner)
	db.SetMaxOpenConns(1)
	ctx := context.Background()
	dia := transitionPostgresDialect(t)
	const (
		oldFunction  = "transitiontest_ledger_old_fn"
		nextFunction = "transitiontest_ledger_next_fn"
	)
	tracking := "schema_migrations_mod_" + transitionTestNamespace
	postgresMustExec(t, db,
		"CREATE TABLE "+quoteIdent(transitionTestTable)+" (id bigint PRIMARY KEY)",
		postgresTransitionFunctionDDL(oldFunction, "OLD"),
		postgresTriggerDDL("public", transitionTestTable, transitionTestTrigger, "public", oldFunction),
	)
	oldDigest := liveTransitionDigest(t, db, dia, transitionTestTable, transitionTestTrigger)
	newDigest := measurePostgresIdentityDigest(
		t, db, dia, transitionTestTable, transitionTestTrigger, oldFunction, nextFunction,
	)
	stmt := "SET search_path = public, pg_catalog; CREATE TEMP TABLE " + quoteIdent(tracking) +
		" (version integer PRIMARY KEY, name text NOT NULL, applied_at text NOT NULL, " +
		"phase text NOT NULL, reverted_at text); " +
		postgresIdentityMigrationSQL(transitionTestTable, transitionTestTrigger, nextFunction)
	files := fstest.MapFS{
		"postgres/0002_identity.sql": &fstest.MapFile{Data: []byte(stmt)},
	}
	r := transitionRegistry(t, files, []store.SchemaTrigger{
		postgresIdentityTransition(
			transitionTestTable, transitionTestTrigger,
			oldDigest, newDigest, oldFunction, nextFunction,
		),
	})
	plans, err := prepareModuleFileMigrations(dia, store.EnginePostgres, r)
	if err != nil {
		t.Fatal(err)
	}
	if err := applyModuleFileMigrations(ctx, db, dia, plans); err != nil {
		t.Fatalf("apply with temporary tracking shadow: %v", err)
	}
	if got := countRows(t, db,
		"SELECT COUNT(*) FROM public."+quoteIdent(tracking)); got != 1 {
		t.Fatalf("durable tracking rows = %d, want 1", got)
	}
	if got := countRows(t, db,
		"SELECT COUNT(*) FROM pg_temp."+quoteIdent(tracking)); got != 0 {
		t.Fatalf("temporary tracking rows = %d, want 0", got)
	}
	if err := applyModuleFileMigrations(ctx, db, dia, plans); err != nil {
		t.Fatalf("idempotent apply read temporary tracking shadow: %v", err)
	}
	if got := liveTransitionDigest(t, db, dia, transitionTestTable, transitionTestTrigger); got != newDigest {
		t.Fatalf("transition digest = %s, want %s", got, newDigest)
	}
	var searchPath string
	if err := db.QueryRowContext(ctx,
		"SELECT pg_catalog.current_setting('search_path')",
	).Scan(&searchPath); err != nil {
		t.Fatal(err)
	}
	if searchPath != dialect.EngineSchema {
		t.Fatalf("pooled session search_path = %q, want %q", searchPath, dialect.EngineSchema)
	}
}

func TestPostgresSchemaTransitionAfterChecksTheExactAppRoleInTheOwnerTransaction(t *testing.T) {
	requireTransitionPostgres(t)
	superDSN := os.Getenv(pgtest.EnvSuperuserDSN)
	suffix := pgtest.Suffix(t)
	app := store.PgRole{Name: "olv_app_" + suffix, Password: "pw-" + suffix}
	owner := store.PgRole{Name: "olv_own_" + suffix, Password: "opw-" + suffix}
	admin := store.PgRole{Name: "olv_to_" + suffix, Password: "apw-" + suffix}
	if app.Name == dialect.DefaultAppRole || app.Name == owner.Name {
		t.Fatalf("fixture roles are not distinct and custom: app=%q owner=%q", app.Name, owner.Name)
	}
	spec := store.PgProvisionSpec{
		Database: "olv_" + suffix, App: app, Owner: owner, Admin: &admin,
	}
	result := pgtest.Provision(
		t, superDSN, spec, ProvisionPostgres, app.Name, owner.Name, admin.Name,
	)
	if result.AppPosture == nil || result.AppPosture.Role != app.Name ||
		result.OwnerPosture == nil || result.OwnerPosture.Role != owner.Name ||
		result.AdminPosture == nil || result.AdminPosture.Role != admin.Name {
		t.Fatalf("split fixture did not attest its custom roles: app=%+v owner=%+v admin=%+v",
			result.AppPosture, result.OwnerPosture, result.AdminPosture)
	}
	dsnFor := func(role store.PgRole) string {
		t.Helper()
		u, err := url.Parse(superDSN)
		if err != nil {
			t.Fatalf("parse %s: %v", pgtest.EnvSuperuserDSN, err)
		}
		u.User = url.UserPassword(role.Name, role.Password)
		u.Path = "/" + spec.Database
		return u.String()
	}
	db := openTransitionPostgres(t, dsnFor(owner))
	ctx := context.Background()
	dia, ok := dialect.NewForAppRole(store.EnginePostgres, app.Name)
	if !ok {
		t.Fatalf("bind PostgreSQL dialect to application role %q", app.Name)
	}
	const (
		oldFunction  = "transitiontest_app_old_fn"
		nextFunction = "transitiontest_app_next_fn"
	)
	postgresMustExec(t, db,
		"CREATE TABLE "+quoteIdent(transitionTestTable)+" (id bigint PRIMARY KEY)",
		postgresTransitionFunctionDDL(oldFunction, "OLD"),
		postgresTriggerDDL("public", transitionTestTable, transitionTestTrigger, "public", oldFunction),
		"REVOKE EXECUTE ON FUNCTION public."+quoteIdent(oldFunction)+"() FROM PUBLIC",
		"GRANT EXECUTE ON FUNCTION public."+quoteIdent(oldFunction)+"() TO "+quoteIdent(app.Name),
	)
	oldDigest := liveTransitionDigest(t, db, dia, transitionTestTable, transitionTestTrigger)
	newDigest := measurePostgresIdentityDigest(
		t, db, dia, transitionTestTable, transitionTestTrigger, oldFunction, nextFunction,
	)
	files := postgresIdentityMigrationFiles(
		transitionTestTable, transitionTestTrigger, nextFunction,
	)
	r := transitionRegistry(t, files, []store.SchemaTrigger{
		postgresIdentityTransition(
			transitionTestTable, transitionTestTrigger,
			oldDigest, newDigest, oldFunction, nextFunction,
		),
	})
	plans, err := prepareModuleFileMigrations(dia, store.EnginePostgres, r)
	if err != nil {
		t.Fatal(err)
	}
	oldInfo := livePostgresTransitionFunction(t, db, dia, "public", oldFunction)
	for _, tc := range []struct {
		name         string
		mutateACL    string
		wantSentinel error
		wantText     string
	}{
		{
			name: "configured application role revoked",
			mutateACL: "REVOKE EXECUTE ON FUNCTION public." + quoteIdent(nextFunction) +
				"() FROM " + quoteIdent(app.Name),
			wantSentinel: store.ErrSchemaTriggerUnexecutable,
			wantText:     "application role cannot execute",
		},
		{
			name: "application role gains grant option",
			mutateACL: "GRANT EXECUTE ON FUNCTION public." + quoteIdent(nextFunction) +
				"() TO " + quoteIdent(app.Name) + " WITH GRANT OPTION",
			wantText: "exact owner/application-role ACL",
		},
		{
			name: "an additional role gains execute",
			mutateACL: "GRANT EXECUTE ON FUNCTION public." + quoteIdent(nextFunction) +
				"() TO " + quoteIdent(admin.Name),
			wantText: "exact owner/application-role ACL",
		},
		{
			name: "exact ACL rendering changes with equivalent grants",
			mutateACL: "REVOKE EXECUTE ON FUNCTION public." + quoteIdent(nextFunction) +
				"() FROM " + quoteIdent(app.Name) + "; " +
				"REVOKE EXECUTE ON FUNCTION public." + quoteIdent(nextFunction) +
				"() FROM " + quoteIdent(owner.Name) + "; " +
				"GRANT EXECUTE ON FUNCTION public." + quoteIdent(nextFunction) +
				"() TO " + quoteIdent(app.Name) + "; " +
				"GRANT EXECUTE ON FUNCTION public." + quoteIdent(nextFunction) +
				"() TO " + quoteIdent(owner.Name),
			wantText: "changed the exact reserved ACL",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			execRan := false
			plans[0].migrations[0].Exec = func(ctx context.Context, tx *sql.Tx) error {
				execRan = true
				_, err := tx.ExecContext(ctx, tc.mutateACL)
				return err
			}
			err = applyModuleFileMigrations(ctx, db, dia, plans)
			if err == nil || (tc.wantSentinel != nil && !errors.Is(err, tc.wantSentinel)) ||
				!strings.Contains(err.Error(), tc.wantText) {
				t.Fatalf(
					"apply error = %v, want sentinel %v and text %q",
					err, tc.wantSentinel, tc.wantText,
				)
			}
			if !execRan || !strings.Contains(err.Error(), "after:") {
				t.Fatalf("refusal happened before the postcondition: exec_ran=%t error=%v", execRan, err)
			}
			if _, exists := lookupPostgresTransitionFunction(
				t, db, dia, "public", nextFunction,
			); exists {
				t.Fatal("reserved next function or mutated ACL survived rollback")
			}
			assertPostgresTransitionRolledBack(t, db, dia, oldDigest)
			if got := livePostgresTransitionFunction(t, db, dia, "public", oldFunction); got != oldInfo {
				t.Fatalf("old exact application-role ACL was not restored: got=%+v want=%+v", got, oldInfo)
			}
		})
	}
}

func TestPostgresSchemaTransitionNormalizesHostileDefaultFunctionACL(t *testing.T) {
	requireTransitionPostgres(t)
	superDSN := os.Getenv(pgtest.EnvSuperuserDSN)
	suffix := pgtest.Suffix(t)
	app := store.PgRole{Name: "olv_app_" + suffix, Password: "pw-" + suffix}
	owner := store.PgRole{Name: "olv_own_" + suffix, Password: "opw-" + suffix}
	extra := store.PgRole{Name: "olv_to_" + suffix, Password: "xpw-" + suffix}
	spec := store.PgProvisionSpec{
		Database: "olv_" + suffix, App: app, Owner: owner, Admin: &extra,
	}
	result := pgtest.Provision(
		t, superDSN, spec, ProvisionPostgres, app.Name, owner.Name, extra.Name,
	)
	if result.AppPosture == nil || result.AppPosture.Role != app.Name ||
		result.OwnerPosture == nil || result.OwnerPosture.Role != owner.Name ||
		result.AdminPosture == nil || result.AdminPosture.Role != extra.Name {
		t.Fatalf("split fixture did not attest custom roles: app=%+v owner=%+v extra=%+v",
			result.AppPosture, result.OwnerPosture, result.AdminPosture)
	}
	dsnFor := func(role store.PgRole) string {
		t.Helper()
		u, err := url.Parse(superDSN)
		if err != nil {
			t.Fatalf("parse %s: %v", pgtest.EnvSuperuserDSN, err)
		}
		u.User = url.UserPassword(role.Name, role.Password)
		u.Path = "/" + spec.Database
		return u.String()
	}
	db := openTransitionPostgres(t, dsnFor(owner))
	ctx := context.Background()
	dia, ok := dialect.NewForAppRole(store.EnginePostgres, app.Name)
	if !ok {
		t.Fatalf("bind PostgreSQL dialect to application role %q", app.Name)
	}
	const (
		oldFunction  = "transitiontest_default_acl_old_fn"
		nextFunction = "transitiontest_default_acl_next_fn"
	)
	postgresMustExec(t, db,
		"CREATE TABLE "+quoteIdent(transitionTestTable)+" (id bigint PRIMARY KEY)",
		postgresTransitionFunctionDDL(oldFunction, "OLD"),
		postgresTriggerDDL("public", transitionTestTable, transitionTestTrigger, "public", oldFunction),
		"REVOKE EXECUTE ON FUNCTION public."+quoteIdent(oldFunction)+"() FROM PUBLIC",
		"GRANT EXECUTE ON FUNCTION public."+quoteIdent(oldFunction)+"() TO "+quoteIdent(app.Name),
	)
	oldDigest := liveTransitionDigest(t, db, dia, transitionTestTable, transitionTestTrigger)
	newDigest := measurePostgresIdentityDigest(
		t, db, dia, transitionTestTable, transitionTestTrigger, oldFunction, nextFunction,
	)
	postgresMustExec(t, db,
		"ALTER DEFAULT PRIVILEGES FOR ROLE "+quoteIdent(owner.Name)+
			" GRANT EXECUTE ON FUNCTIONS TO "+quoteIdent(app.Name)+" WITH GRANT OPTION",
		"ALTER DEFAULT PRIVILEGES FOR ROLE "+quoteIdent(owner.Name)+
			" GRANT EXECUTE ON FUNCTIONS TO "+quoteIdent(extra.Name),
	)
	files := postgresIdentityMigrationFiles(
		transitionTestTable, transitionTestTrigger, nextFunction,
	)
	r := transitionRegistry(t, files, []store.SchemaTrigger{
		postgresIdentityTransition(
			transitionTestTable, transitionTestTrigger,
			oldDigest, newDigest, oldFunction, nextFunction,
		),
	})
	plans, err := prepareModuleFileMigrations(dia, store.EnginePostgres, r)
	if err != nil {
		t.Fatal(err)
	}
	if err := applyModuleFileMigrations(ctx, db, dia, plans); err != nil {
		t.Fatalf("apply with hostile default function ACL: %v", err)
	}
	info := livePostgresTransitionFunction(t, db, dia, "public", nextFunction)
	if !info.ACLIsExact || !info.CanExecute || !info.AppRoleDirectExecute || info.PublicCanExecute {
		t.Fatalf("reserved function ACL was not normalized exactly: %+v", info)
	}
	var extraCanExecute, appHasGrantOption bool
	if err := db.QueryRowContext(ctx, `
SELECT pg_catalog.has_function_privilege($1::pg_catalog.text, p.oid, 'EXECUTE'),
       EXISTS (
           SELECT 1
             FROM pg_catalog.aclexplode(
                      COALESCE(p.proacl, pg_catalog.acldefault('f', p.proowner))
                  ) acl
            WHERE acl.grantee <> 0
              AND pg_catalog.pg_get_userbyid(acl.grantee) = $2::pg_catalog.text
              AND acl.privilege_type = 'EXECUTE'
              AND acl.is_grantable
       )
  FROM pg_catalog.pg_proc p
  JOIN pg_catalog.pg_namespace n ON n.oid = p.pronamespace
 WHERE n.nspname = 'public'
   AND p.proname = $3::pg_catalog.text
   AND p.pronargs = 0`, extra.Name, app.Name, nextFunction).Scan(
		&extraCanExecute, &appHasGrantOption,
	); err != nil {
		t.Fatal(err)
	}
	if extraCanExecute || appHasGrantOption {
		t.Fatalf("hostile defaults survived: extra_execute=%t app_grant_option=%t ACL=%s",
			extraCanExecute, appHasGrantOption, info.ACL)
	}
	if got := liveTransitionDigest(t, db, dia, transitionTestTable, transitionTestTrigger); got != newDigest {
		t.Fatalf("transition digest = %s, want %s", got, newDigest)
	}
	if got := countRows(t, db,
		"SELECT COUNT(*) FROM schema_migrations_mod_"+transitionTestNamespace); got != 1 {
		t.Fatalf("tracking rows = %d, want 1", got)
	}
}

func TestPostgresSchemaTransitionRejectsExtraCallerOfReservedNextFunction(t *testing.T) {
	requireTransitionPostgres(t)
	pg := isolatedPG(t)
	db := openTransitionPostgres(t, pg.Owner)
	ctx := context.Background()
	dia := transitionPostgresDialect(t)
	const (
		oldFunction  = "transitiontest_extra_old_fn"
		nextFunction = "transitiontest_extra_next_fn"
		extraTrigger = "transitiontest_extra_guard"
	)
	postgresMustExec(t, db,
		"CREATE TABLE "+quoteIdent(transitionTestTable)+" (id bigint PRIMARY KEY)",
		"CREATE TABLE "+quoteIdent(transitionTestTable2)+" (id bigint PRIMARY KEY)",
		postgresTransitionFunctionDDL(oldFunction, "OLD"),
		postgresTriggerDDL("public", transitionTestTable, transitionTestTrigger, "public", oldFunction),
	)
	oldDigest := liveTransitionDigest(t, db, dia, transitionTestTable, transitionTestTrigger)
	newDigest := measurePostgresIdentityDigest(
		t, db, dia, transitionTestTable, transitionTestTrigger, oldFunction, nextFunction,
	)
	stmt := postgresIdentityMigrationSQL(
		transitionTestTable, transitionTestTrigger, nextFunction,
	) + "; " + postgresTriggerDDL(
		"public", transitionTestTable2, extraTrigger, "public", nextFunction,
	)
	files := fstest.MapFS{
		"postgres/0002_identity.sql": &fstest.MapFile{Data: []byte(stmt)},
	}
	r := transitionRegistry(t, files, []store.SchemaTrigger{
		postgresIdentityTransition(
			transitionTestTable, transitionTestTrigger,
			oldDigest, newDigest, oldFunction, nextFunction,
		),
	})
	plans, err := prepareModuleFileMigrations(dia, store.EnginePostgres, r)
	if err != nil {
		t.Fatal(err)
	}
	err = applyModuleFileMigrations(ctx, db, dia, plans)
	if err == nil || !strings.Contains(err.Error(), "created undeclared caller") {
		t.Fatalf("apply error = %v, want extra-new-caller refusal", err)
	}
	if _, exists := lookupPostgresTransitionFunction(t, db, dia, "public", nextFunction); exists {
		t.Fatal("reserved next function survived extra-caller rollback")
	}
	live, err := dia.SchemaTriggers(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	extraKey := dialect.TriggerKey{Schema: "public", Table: transitionTestTable2, Name: extraTrigger}
	if _, exists := live[extraKey]; exists {
		t.Fatalf("undeclared next caller %s survived rollback", extraKey)
	}
	assertPostgresTransitionRolledBack(t, db, dia, oldDigest)
}

func TestPostgresSchemaTransitionCatalogProjectionIgnoresSearchPathAndTempShadows(t *testing.T) {
	requireTransitionPostgres(t)
	pg := isolatedPG(t)
	db := openTransitionPostgres(t, pg.Owner)
	db.SetMaxOpenConns(1)
	ctx := context.Background()
	dia := transitionPostgresDialect(t)
	const (
		oldFunction  = "transitiontest_catalog_old_fn"
		nextFunction = "transitiontest_catalog_next_fn"
		extraTrigger = "transitiontest_catalog_extra_guard"
		falseOIDFn   = "transitiontest_catalog_false_oid_eq"
	)
	postgresMustExec(t, db,
		"CREATE TABLE "+quoteIdent(transitionTestTable)+" (id bigint PRIMARY KEY)",
		"CREATE TABLE "+quoteIdent(transitionTestTable2)+" (id bigint PRIMARY KEY)",
		postgresTransitionFunctionDDL(oldFunction, "OLD"),
		postgresTriggerDDL("public", transitionTestTable, transitionTestTrigger, "public", oldFunction),
	)
	oldDigest := liveTransitionDigest(t, db, dia, transitionTestTable, transitionTestTrigger)
	newDigest := measurePostgresIdentityDigest(
		t, db, dia, transitionTestTable, transitionTestTrigger, oldFunction, nextFunction,
	)
	postgresMustExec(t, db,
		"CREATE FUNCTION public."+quoteIdent(falseOIDFn)+
			"(pg_catalog.oid, pg_catalog.oid) RETURNS boolean LANGUAGE sql IMMUTABLE STRICT AS 'SELECT false'",
		"CREATE OPERATOR public.= (LEFTARG = pg_catalog.oid, RIGHTARG = pg_catalog.oid, "+
			"FUNCTION = public."+quoteIdent(falseOIDFn)+")",
		"CREATE TEMP VIEW pg_trigger AS SELECT * FROM pg_catalog.pg_trigger WHERE tgname <> "+
			"'"+transitionTestTrigger+"' AND tgname <> '"+extraTrigger+"'",
		"CREATE DOMAIN pg_temp.text AS pg_catalog.text CHECK (VALUE <> 'public')",
		"SET search_path = public, pg_catalog",
	)
	stmt := "SET LOCAL search_path = public, pg_catalog; " +
		postgresIdentityMigrationSQL(
			transitionTestTable, transitionTestTrigger, nextFunction,
		) + "; " + postgresTriggerDDL(
		"public", transitionTestTable2, extraTrigger, "public", nextFunction,
	)
	files := fstest.MapFS{
		"postgres/0002_identity.sql": &fstest.MapFile{Data: []byte(stmt)},
	}
	r := transitionRegistry(t, files, []store.SchemaTrigger{
		postgresIdentityTransition(
			transitionTestTable, transitionTestTrigger,
			oldDigest, newDigest, oldFunction, nextFunction,
		),
	})
	plans, err := prepareModuleFileMigrations(dia, store.EnginePostgres, r)
	if err != nil {
		t.Fatal(err)
	}
	execRan := false
	plans[0].migrations[0].Exec = func(context.Context, *sql.Tx) error {
		execRan = true
		return nil
	}
	err = applyModuleFileMigrations(ctx, db, dia, plans)
	if err == nil || !strings.Contains(err.Error(), "created undeclared caller") {
		t.Fatalf("apply error = %v, want exact extra-caller refusal", err)
	}
	if !execRan || !strings.Contains(err.Error(), "after:") {
		t.Fatalf("catalog projection refused in the wrong phase: exec_ran=%t error=%v", execRan, err)
	}
	postgresMustExec(t, db, "SET search_path = public")
	if _, exists := lookupPostgresTransitionFunction(t, db, dia, "public", nextFunction); exists {
		t.Fatal("reserved next function survived catalog-shadow rollback")
	}
	assertPostgresTransitionRolledBack(t, db, dia, oldDigest)
}

func TestPostgresSchemaTransitionRejectsDroppingTheReservedNextIdentity(t *testing.T) {
	requireTransitionPostgres(t)
	pg := isolatedPG(t)
	db := openTransitionPostgres(t, pg.Owner)
	ctx := context.Background()
	dia := transitionPostgresDialect(t)
	const (
		oldFunction  = "transitiontest_oid_old_fn"
		nextFunction = "transitiontest_oid_next_fn"
	)
	postgresMustExec(t, db,
		"CREATE TABLE "+quoteIdent(transitionTestTable)+" (id bigint PRIMARY KEY)",
		postgresTransitionFunctionDDL(oldFunction, "OLD"),
		postgresTriggerDDL("public", transitionTestTable, transitionTestTrigger, "public", oldFunction),
	)
	oldDigest := liveTransitionDigest(t, db, dia, transitionTestTable, transitionTestTrigger)
	newDigest := measurePostgresIdentityDigest(
		t, db, dia, transitionTestTable, transitionTestTrigger, oldFunction, nextFunction,
	)
	stmt := "DROP FUNCTION public." + quoteIdent(nextFunction) + "(); " +
		postgresIdentityMigrationSQL(transitionTestTable, transitionTestTrigger, nextFunction) +
		"; REVOKE ALL ON FUNCTION public." + quoteIdent(nextFunction) + "() FROM PUBLIC" +
		"; GRANT EXECUTE ON FUNCTION public." + quoteIdent(nextFunction) + "() TO " +
		quoteIdent(dialect.DefaultAppRole)
	files := fstest.MapFS{
		"postgres/0002_identity.sql": &fstest.MapFile{Data: []byte(stmt)},
	}
	r := transitionRegistry(t, files, []store.SchemaTrigger{
		postgresIdentityTransition(
			transitionTestTable, transitionTestTrigger,
			oldDigest, newDigest, oldFunction, nextFunction,
		),
	})
	plans, err := prepareModuleFileMigrations(dia, store.EnginePostgres, r)
	if err != nil {
		t.Fatal(err)
	}
	err = applyModuleFileMigrations(ctx, db, dia, plans)
	if err == nil || !strings.Contains(err.Error(), "dropped and recreated reserved function") {
		t.Fatalf("apply error = %v, want reserved-OID refusal", err)
	}
	if _, exists := lookupPostgresTransitionFunction(t, db, dia, "public", nextFunction); exists {
		t.Fatal("recreated next function survived OID-refusal rollback")
	}
	assertPostgresTransitionRolledBack(t, db, dia, oldDigest)
}

func TestPostgresSchemaTransitionRejectsOldFunctionOrCallerDrift(t *testing.T) {
	for _, tc := range []struct {
		name  string
		drift func(oldFunction string) string
		want  string
	}{
		{
			name: "old function body",
			drift: func(oldFunction string) string {
				return postgresTransitionFunctionDDL(oldFunction, "DRIFT")
			},
			want: "old schema-transition function",
		},
		{
			name: "old caller firing state",
			drift: func(string) string {
				return "ALTER TABLE ONLY public." + quoteIdent(transitionTestTable2) +
					" DISABLE TRIGGER " + quoteIdent(transitionTestTrigger+"_witness")
			},
			want: "old shared-function caller changed",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			requireTransitionPostgres(t)
			pg := isolatedPG(t)
			db := openTransitionPostgres(t, pg.Owner)
			ctx := context.Background()
			dia := transitionPostgresDialect(t)
			oldFunction := "transitiontest_drift_old_fn"
			nextFunction := "transitiontest_drift_next_fn"
			witnessTrigger := transitionTestTrigger + "_witness"
			fixtures := []string{
				"CREATE TABLE " + quoteIdent(transitionTestTable) + " (id bigint PRIMARY KEY)",
				postgresTransitionFunctionDDL(oldFunction, "OLD"),
				postgresTriggerDDL("public", transitionTestTable, transitionTestTrigger, "public", oldFunction),
			}
			if tc.name == "old caller firing state" {
				fixtures = append(fixtures,
					"CREATE TABLE "+quoteIdent(transitionTestTable2)+" (id bigint PRIMARY KEY)",
					postgresTriggerDDL(
						"public", transitionTestTable2, witnessTrigger, "public", oldFunction,
					),
				)
			}
			postgresMustExec(t, db, fixtures...)
			oldDigest := liveTransitionDigest(t, db, dia, transitionTestTable, transitionTestTrigger)
			newDigest := measurePostgresIdentityDigest(
				t, db, dia, transitionTestTable, transitionTestTrigger, oldFunction, nextFunction,
			)
			stmt := postgresIdentityMigrationSQL(
				transitionTestTable, transitionTestTrigger, nextFunction,
			) + "; " + tc.drift(oldFunction)
			files := fstest.MapFS{
				"postgres/0002_identity.sql": &fstest.MapFile{Data: []byte(stmt)},
			}
			r := transitionRegistry(t, files, []store.SchemaTrigger{
				postgresIdentityTransition(
					transitionTestTable, transitionTestTrigger,
					oldDigest, newDigest, oldFunction, nextFunction,
				),
			})
			plans, err := prepareModuleFileMigrations(dia, store.EnginePostgres, r)
			if err != nil {
				t.Fatal(err)
			}
			err = applyModuleFileMigrations(ctx, db, dia, plans)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("apply error = %v, want old-state refusal containing %q", err, tc.want)
			}
			if _, exists := lookupPostgresTransitionFunction(t, db, dia, "public", nextFunction); exists {
				t.Fatal("reserved next function survived old-state rollback")
			}
			assertPostgresTransitionRolledBack(t, db, dia, oldDigest)
		})
	}
}

func TestPostgresSchemaTransitionAllowsConcurrentNewCallerOfUnchangedOldFunction(t *testing.T) {
	requireTransitionPostgres(t)
	pg := isolatedPG(t)
	db := openTransitionPostgres(t, pg.Owner)
	ctx := context.Background()
	dia := transitionPostgresDialect(t)
	const (
		oldFunction     = "transitiontest_concurrent_old_fn"
		nextFunction    = "transitiontest_concurrent_next_fn"
		externalTable   = "transitiontest_external_fact"
		externalTrigger = "transitiontest_external_guard"
	)
	postgresMustExec(t, db,
		"CREATE TABLE "+quoteIdent(transitionTestTable)+" (id bigint PRIMARY KEY)",
		"CREATE TABLE "+quoteIdent(transitionTestTable2)+" (id bigint PRIMARY KEY)",
		"CREATE TABLE "+quoteIdent(externalTable)+" (id bigint PRIMARY KEY)",
		postgresTransitionFunctionDDL(oldFunction, "OLD"),
		postgresTriggerDDL("public", transitionTestTable, transitionTestTrigger, "public", oldFunction),
		postgresTriggerDDL("public", transitionTestTable2, transitionTestTrigger+"_witness", "public", oldFunction),
	)
	oldDigest := liveTransitionDigest(t, db, dia, transitionTestTable, transitionTestTrigger)
	oldFunctionBefore := livePostgresTransitionFunction(t, db, dia, "public", oldFunction)
	newDigest := measurePostgresIdentityDigest(
		t, db, dia, transitionTestTable, transitionTestTrigger, oldFunction, nextFunction,
	)
	files := postgresIdentityMigrationFiles(
		transitionTestTable, transitionTestTrigger, nextFunction,
	)
	r := transitionRegistry(t, files, []store.SchemaTrigger{
		postgresIdentityTransition(
			transitionTestTable, transitionTestTrigger,
			oldDigest, newDigest, oldFunction, nextFunction,
		),
	})
	plans, err := prepareModuleFileMigrations(dia, store.EnginePostgres, r)
	if err != nil {
		t.Fatal(err)
	}
	contender := openTransitionPostgres(t, pg.Owner)
	contender.SetMaxOpenConns(1)
	postgresMustExec(t, contender, "SET lock_timeout = '500ms'")
	execRan := false
	callerMutationBlocked := false
	functionMutationBlocked := false
	plans[0].migrations[0].Exec = func(ctx context.Context, _ *sql.Tx) error {
		execRan = true
		if _, err := contender.ExecContext(ctx,
			postgresTriggerDDL("public", externalTable, externalTrigger, "public", oldFunction)); err != nil {
			return fmt.Errorf("create harmless concurrent old caller: %w", err)
		}
		if _, err := contender.ExecContext(ctx,
			"ALTER TABLE ONLY public."+quoteIdent(transitionTestTable2)+
				" DISABLE TRIGGER "+quoteIdent(transitionTestTrigger+"_witness")); err == nil {
			return errors.New("concurrent pre-existing caller mutation escaped its table lock")
		} else if !strings.Contains(err.Error(), "lock timeout") {
			return fmt.Errorf("concurrent pre-existing caller mutation failed for the wrong reason: %w", err)
		}
		callerMutationBlocked = true
		if _, err := contender.ExecContext(ctx,
			postgresTransitionFunctionDDL(oldFunction, "EXTERNAL DRIFT")); err == nil {
			return errors.New("concurrent old-function replacement escaped its fence")
		} else if !strings.Contains(err.Error(), "lock timeout") {
			return fmt.Errorf("concurrent old-function replacement failed for the wrong reason: %w", err)
		}
		functionMutationBlocked = true
		return nil
	}
	if err := applyModuleFileMigrations(ctx, db, dia, plans); err != nil {
		t.Fatalf("identity transition rejected harmless concurrent old caller: %v", err)
	}
	if !execRan {
		t.Fatal("concurrent old-caller probe did not run between migration SQL and After")
	}
	if !callerMutationBlocked {
		t.Fatal("old caller table lock did not block a concurrent firing-state mutation")
	}
	if !functionMutationBlocked {
		t.Fatal("old function fence did not block a concurrent body replacement")
	}
	oldCallers := livePostgresTransitionCallers(t, db, dia, "public", oldFunction)
	externalKey := dialect.TriggerKey{Schema: "public", Table: externalTable, Name: externalTrigger}
	if _, ok := oldCallers[externalKey]; !ok {
		t.Fatalf("concurrent old caller %s did not commit", externalKey)
	}
	if got := livePostgresTransitionFunction(t, db, dia, "public", oldFunction); got != oldFunctionBefore {
		t.Fatalf("unchanged old function drifted while external caller was added: got=%+v want=%+v",
			got, oldFunctionBefore)
	}
	if got := liveTransitionDigest(t, db, dia, transitionTestTable, transitionTestTrigger); got != newDigest {
		t.Fatalf("selected trigger digest = %s, want %s", got, newDigest)
	}
	if got := countRows(t, db,
		"SELECT COUNT(*) FROM schema_migrations_mod_"+transitionTestNamespace); got != 1 {
		t.Fatalf("tracking rows = %d, want 1", got)
	}
}

func TestPostgresSchemaTransitionHandlesHostileFunctionIdentitiesWithSCSOff(t *testing.T) {
	requireTransitionPostgres(t)
	pg := isolatedPG(t)
	db := openTransitionPostgres(t, pg.Owner)
	db.SetMaxOpenConns(1)
	ctx := context.Background()
	dia := transitionPostgresDialect(t)
	oldFunction := `o\\"$guardfence$$olivares_schema_transition_reservation$`
	nextFunction := `n\\"$guardfence$$olivares_schema_transition_reservation$`
	postgresMustExec(t, db,
		"SET standard_conforming_strings = off",
		"CREATE TABLE "+quoteIdent(transitionTestTable)+" (id bigint PRIMARY KEY)",
		postgresTransitionFunctionDDL(oldFunction, "OLD"),
		postgresTriggerDDL("public", transitionTestTable, transitionTestTrigger, "public", oldFunction),
	)
	oldDigest := liveTransitionDigest(t, db, dia, transitionTestTable, transitionTestTrigger)
	newDigest := measurePostgresIdentityDigest(
		t, db, dia, transitionTestTable, transitionTestTrigger, oldFunction, nextFunction,
	)
	files := postgresIdentityMigrationFiles(
		transitionTestTable, transitionTestTrigger, nextFunction,
	)
	r := transitionRegistry(t, files, []store.SchemaTrigger{
		postgresIdentityTransition(
			transitionTestTable, transitionTestTrigger,
			oldDigest, newDigest, oldFunction, nextFunction,
		),
	})
	plans, err := prepareModuleFileMigrations(dia, store.EnginePostgres, r)
	if err != nil {
		t.Fatal(err)
	}
	if err := applyModuleFileMigrations(ctx, db, dia, plans); err != nil {
		t.Fatalf("hostile identity transition with standard_conforming_strings=off: %v", err)
	}
	if got := liveTransitionDigest(t, db, dia, transitionTestTable, transitionTestTrigger); got != newDigest {
		t.Fatalf("hostile identity digest = %s, want %s", got, newDigest)
	}
	info := livePostgresTransitionFunction(t, db, dia, "public", nextFunction)
	if !info.ACLIsExact || !info.CanExecute || !info.AppRoleDirectExecute || info.PublicCanExecute {
		t.Fatalf("hostile next function lost its exact reservation ACL: %+v", info)
	}
}

func TestPostgresSchemaTransitionFenceRendererIsRuntimeSafe(t *testing.T) {
	schema := `transition\\schema$guardfence$$olv$`
	function := `transition\\function$guardfence$$olv1$`
	stmt := dialect.PostgresFunctionFenceStatement(schema, function)
	const prefix = "DO "
	if !strings.HasPrefix(stmt, prefix+"$") {
		t.Fatalf("fence does not start with a dollar-quoted DO body: %q", stmt)
	}
	rest := strings.TrimPrefix(stmt, prefix)
	end := strings.Index(rest[1:], "$")
	if end < 0 {
		t.Fatalf("fence has no complete outer dollar tag: %q", stmt)
	}
	tag := rest[:end+2]
	body := strings.TrimSuffix(strings.TrimPrefix(rest, tag), tag)
	if strings.Contains(body, tag) {
		t.Fatalf("outer dollar tag %q occurs inside the rendered body", tag)
	}
	if tag == "$guardfence$" || !strings.Contains(body, schema) || !strings.Contains(body, function) {
		t.Fatalf("runtime identifiers were not safely framed: tag=%q body=%q", tag, body)
	}
}

func TestPostgresSchemaTransitionReservationRendererIsExclusiveAndRoleExact(t *testing.T) {
	role := `app\\role"$reservation$`
	schema := `schema\\name"$reservation$`
	function := `function\\name"$olivares_schema_transition_reservation$`
	dia, ok := dialect.NewForAppRole(store.EnginePostgres, role)
	if !ok {
		t.Fatal("PostgreSQL dialect unavailable")
	}
	catalog, ok := dia.(dialect.SchemaTriggerFunctionCatalog)
	if !ok {
		t.Fatal("PostgreSQL dialect lacks transition function reservation")
	}
	recorder := &transitionReservationRecorder{}
	if err := catalog.ReserveSchemaTriggerFunction(
		context.Background(), recorder,
		dialect.SchemaTriggerFunctionKey{Schema: schema, Name: function},
	); err != nil {
		t.Fatal(err)
	}
	if len(recorder.statements) != 4 {
		t.Fatalf("reservation statements = %d, want 4", len(recorder.statements))
	}
	target := quoteIdent(schema) + "." + quoteIdent(function) + "()"
	if !strings.HasPrefix(recorder.statements[0], "CREATE FUNCTION "+target+" ") ||
		strings.Contains(recorder.statements[0], "CREATE OR REPLACE") {
		t.Fatalf("reservation did not use exclusive plain CREATE FUNCTION: %q", recorder.statements[0])
	}
	if recorder.statements[1] != "REVOKE ALL ON FUNCTION "+target+" FROM PUBLIC" {
		t.Fatalf("PUBLIC revoke = %q", recorder.statements[1])
	}
	normalization := recorder.statements[2]
	if !strings.HasPrefix(normalization, "DO $") ||
		!strings.Contains(normalization, schema) || !strings.Contains(normalization, function) ||
		!strings.Contains(normalization, "REVOKE ALL PRIVILEGES") ||
		!strings.Contains(normalization, "owner_name") {
		t.Fatalf("inherited-ACL normalization is incomplete or unsafely framed: %q", normalization)
	}
	if recorder.statements[3] != "GRANT EXECUTE ON FUNCTION "+target+" TO "+quoteIdent(role) {
		t.Fatalf("exact application-role grant = %q", recorder.statements[3])
	}
	for i, args := range recorder.args {
		if len(args) != 0 {
			t.Fatalf("reservation statement %d unexpectedly carried arguments: %v", i, args)
		}
	}
}

func TestPostgresSchemaTransitionFenceRunsWithHostileNamesAndSCSOff(t *testing.T) {
	requireTransitionPostgres(t)
	pg := isolatedPG(t)
	db, err := sql.Open("pgx", pg.Owner)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op after the deliberate rollback below
	schema := `transition\\schema$guardfence$$olv$`
	function := `transition\\function$guardfence$$olv1$`
	if _, err := tx.ExecContext(ctx, "CREATE SCHEMA "+quoteIdent(schema)); err != nil {
		t.Fatal(err)
	}
	functionDDL := `CREATE FUNCTION ` + quoteIdent(schema) + `.` + quoteIdent(function) + `()
RETURNS trigger LANGUAGE plpgsql AS $body$
BEGIN
  RETURN OLD;
END
$body$`
	if _, err := tx.ExecContext(ctx, functionDDL); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, "SET LOCAL standard_conforming_strings = off"); err != nil {
		t.Fatal(err)
	}
	const costQuery = `SELECT p.procost
FROM pg_catalog.pg_proc p
JOIN pg_catalog.pg_namespace n ON n.oid = p.pronamespace
WHERE n.nspname = $1 AND p.proname = $2 AND p.pronargs = 0`
	var before float64
	if err := tx.QueryRowContext(ctx, costQuery, schema, function).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, dialect.PostgresFunctionFenceStatement(schema, function)); err != nil {
		t.Fatalf("runtime-safe function fence: %v", err)
	}
	var after float64
	if err := tx.QueryRowContext(ctx, costQuery, schema, function).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Fatalf("function fence changed procost: before=%v after=%v", before, after)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
}

func TestSchemaTransitionFenceSetsAreLexical(t *testing.T) {
	functions := sortedTransitionFunctions(map[transitionFunction]bool{
		{schema: "zeta", name: "alpha"}:  true,
		{schema: "alpha", name: "zeta"}:  true,
		{schema: "alpha", name: "alpha"}: true,
	})
	wantFunctions := []transitionFunction{
		{schema: "alpha", name: "alpha"},
		{schema: "alpha", name: "zeta"},
		{schema: "zeta", name: "alpha"},
	}
	if fmt.Sprint(functions) != fmt.Sprint(wantFunctions) {
		t.Fatalf("function fence order = %v, want %v", functions, wantFunctions)
	}
	tables := transitionCallerTables(map[dialect.TriggerKey]dialect.TriggerInfo{
		{Schema: "zeta", Table: "beta", Name: "guard"}:   {},
		{Schema: "alpha", Table: "zeta", Name: "guard"}:  {},
		{Schema: "alpha", Table: "alpha", Name: "guard"}: {},
	})
	wantTables := []dialect.TriggerKey{
		{Schema: "alpha", Table: "alpha"},
		{Schema: "alpha", Table: "zeta"},
		{Schema: "zeta", Table: "beta"},
	}
	if fmt.Sprint(tables) != fmt.Sprint(wantTables) {
		t.Fatalf("table lock order = %v, want %v", tables, wantTables)
	}
}

func TestExactNextFunctionCallerSetIncludesEverySchema(t *testing.T) {
	selectedKey := dialect.TriggerKey{
		Schema: "public", Table: transitionTestTable, Name: transitionTestTrigger,
	}
	foreignKey := dialect.TriggerKey{
		Schema: "foreign", Table: transitionTestTable2, Name: transitionTestTrigger,
	}
	function := transitionFunction{schema: "public", name: "reserved_guard"}
	live := map[dialect.TriggerKey]dialect.TriggerInfo{
		selectedKey: {
			FunctionSchema: function.schema, FunctionName: function.name,
		},
		foreignKey: {
			FunctionSchema: function.schema, FunctionName: function.name,
		},
	}
	snapshot := &schemaTransitionSnapshot{byTrigger: map[dialect.TriggerKey]transitionTriggerState{
		selectedKey: {function: function},
	}}
	err := exactNextFunctionCallerSet(
		live,
		map[dialect.TriggerKey]bool{selectedKey: true},
		snapshot,
	)
	if err == nil || !strings.Contains(err.Error(), foreignKey.String()) {
		t.Fatalf("exact next-caller error = %v, want foreign-schema trigger %s", err, foreignKey)
	}
}

func requireTransitionPostgres(t *testing.T) {
	t.Helper()
	if !pgtest.Available(t) {
		t.Skip("PostgreSQL is not reachable; transition integration leg skipped")
	}
}

func transitionRegistry(
	t *testing.T,
	files fstest.MapFS,
	active []store.SchemaTrigger,
) *registry {
	t.Helper()
	r := newRegistry()
	if err := r.Migrations(transitionTestNamespace, files); err != nil {
		t.Fatal(err)
	}
	// The inactive engine still needs a structurally complete declaration. SQLite
	// must not carry PostgreSQL identity metadata, while every PostgreSQL transition
	// must carry an old/new identity even when only the SQLite plan is prepared.
	isPostgres := false
	for name := range files {
		if strings.HasPrefix(name, "postgres/") {
			isPostgres = true
			break
		}
	}
	sqlite := make([]store.SchemaTrigger, len(active))
	postgres := make([]store.SchemaTrigger, len(active))
	for i, trigger := range active {
		sqlite[i] = cloneSchemaTrigger(trigger)
		postgres[i] = cloneSchemaTrigger(trigger)
		for j := range sqlite[i].Transitions {
			sqlite[i].Transitions[j].PostgresFunctionIdentity = nil
		}
		for j := range postgres[i].Transitions {
			if postgres[i].Transitions[j].PostgresFunctionIdentity == nil {
				postgres[i].Transitions[j].PostgresFunctionIdentity =
					&store.SchemaTriggerFunctionIdentityTransition{
						PreviousName: fmt.Sprintf("inactive_function_%d", j),
						NextName:     fmt.Sprintf("inactive_function_%d", j+1),
					}
			}
		}
	}
	if isPostgres {
		postgres = active
	} else {
		sqlite = active
	}
	if err := r.SchemaInvariants(transitionTestNamespace, bothEngines(sqlite, postgres)); err != nil {
		t.Fatal(err)
	}
	return r
}

func openTransitionSQLite(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func sqliteTransitionDigest(t *testing.T, triggerDDL string) string {
	t.Helper()
	db := openTransitionSQLite(t)
	if _, err := db.Exec("CREATE TABLE " + transitionTestTable + " (id INTEGER PRIMARY KEY)"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(triggerDDL); err != nil {
		t.Fatal(err)
	}
	dia, _ := dialect.New(store.EngineSQLite)
	return liveTransitionDigest(t, db, dia, transitionTestTable, transitionTestTrigger)
}

func liveTransitionDigest(
	t *testing.T,
	db *sql.DB,
	dia dialect.Dialect,
	table string,
	trigger string,
) string {
	t.Helper()
	schema, err := dia.SchemaName(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	live, err := dia.SchemaTriggers(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	info, ok := live[dialect.TriggerKey{Schema: schema, Table: table, Name: trigger}]
	if !ok {
		t.Fatalf("live trigger %s.%s.%s is absent", schema, table, trigger)
	}
	digest := sha256.Sum256([]byte(info.Definition))
	return hex.EncodeToString(digest[:])
}

func postgresTransitionFunctionDDL(name, marker string) string {
	return fmt.Sprintf(`CREATE OR REPLACE FUNCTION %s() RETURNS trigger
LANGUAGE plpgsql AS $body$
BEGIN
  RAISE EXCEPTION '%s';
END
$body$`, quoteIdent(name), marker)
}

func openTransitionPostgres(t *testing.T, dsn string) *sql.DB {
	t.Helper()
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func transitionPostgresDialect(t *testing.T) dialect.Dialect {
	t.Helper()
	dia, ok := dialect.New(store.EnginePostgres)
	if !ok {
		t.Fatal("PostgreSQL dialect unavailable")
	}
	return dia
}

func postgresMustExec(t *testing.T, db *sql.DB, statements ...string) {
	t.Helper()
	for _, stmt := range statements {
		if _, err := db.ExecContext(context.Background(), stmt); err != nil {
			t.Fatalf("execute PostgreSQL fixture statement: %v\nSQL: %s", err, stmt)
		}
	}
}

func postgresTriggerDDL(
	tableSchema string,
	table string,
	trigger string,
	functionSchema string,
	function string,
) string {
	return "CREATE TRIGGER " + quoteIdent(trigger) + " BEFORE DELETE ON " +
		quoteIdent(tableSchema) + "." + quoteIdent(table) +
		" FOR EACH ROW EXECUTE FUNCTION " + quoteIdent(functionSchema) + "." +
		quoteIdent(function) + "()"
}

func postgresRetargetTriggerDDL(table, trigger, function string) string {
	return "DROP TRIGGER " + quoteIdent(trigger) + " ON public." + quoteIdent(table) +
		"; " + postgresTriggerDDL("public", table, trigger, "public", function)
}

func postgresIdentityMigrationSQL(table, trigger, nextFunction string) string {
	return postgresTransitionFunctionDDL(nextFunction, "NEW") + "; " +
		postgresRetargetTriggerDDL(table, trigger, nextFunction)
}

func postgresIdentityMigrationFiles(table, trigger, nextFunction string) fstest.MapFS {
	return fstest.MapFS{
		"postgres/0002_identity.sql": &fstest.MapFile{Data: []byte(
			postgresIdentityMigrationSQL(table, trigger, nextFunction),
		)},
	}
}

func postgresIdentityTransition(
	table string,
	trigger string,
	oldDigest string,
	newDigest string,
	oldFunction string,
	nextFunction string,
) store.SchemaTrigger {
	return store.SchemaTrigger{
		Name: trigger, Table: table, DefinitionSHA256: newDigest,
		Transitions: []store.SchemaTriggerTransition{{
			MigrationVersion: 2, PreviousDefinitionSHA256: oldDigest,
			PostgresFunctionIdentity: &store.SchemaTriggerFunctionIdentityTransition{
				PreviousName: oldFunction,
				NextName:     nextFunction,
			},
		}},
	}
}

func measurePostgresIdentityDigest(
	t *testing.T,
	db *sql.DB,
	dia dialect.Dialect,
	table string,
	trigger string,
	oldFunction string,
	nextFunction string,
) string {
	t.Helper()
	oldDigest := liveTransitionDigest(t, db, dia, table, trigger)
	postgresMustExec(t, db,
		postgresTransitionFunctionDDL(nextFunction, "NEW"),
		postgresRetargetTriggerDDL(table, trigger, nextFunction),
	)
	newDigest := liveTransitionDigest(t, db, dia, table, trigger)
	postgresMustExec(t, db,
		postgresRetargetTriggerDDL(table, trigger, oldFunction),
		"DROP FUNCTION public."+quoteIdent(nextFunction)+"()",
	)
	if got := liveTransitionDigest(t, db, dia, table, trigger); got != oldDigest {
		t.Fatalf("digest fixture did not restore old trigger: got %s want %s", got, oldDigest)
	}
	return newDigest
}

func lookupPostgresTransitionFunction(
	t *testing.T,
	db *sql.DB,
	dia dialect.Dialect,
	schema string,
	name string,
) (dialect.SchemaTriggerFunctionInfo, bool) {
	t.Helper()
	catalog, ok := dia.(dialect.SchemaTriggerFunctionCatalog)
	if !ok {
		t.Fatal("PostgreSQL dialect lacks transition function catalog")
	}
	info, exists, err := catalog.SchemaTriggerFunction(
		context.Background(), db,
		dialect.SchemaTriggerFunctionKey{Schema: schema, Name: name},
	)
	if err != nil {
		t.Fatal(err)
	}
	return info, exists
}

func livePostgresTransitionFunction(
	t *testing.T,
	db *sql.DB,
	dia dialect.Dialect,
	schema string,
	name string,
) dialect.SchemaTriggerFunctionInfo {
	t.Helper()
	info, exists := lookupPostgresTransitionFunction(t, db, dia, schema, name)
	if !exists {
		t.Fatalf("PostgreSQL function %s.%s is absent", schema, name)
	}
	return info
}

func livePostgresTransitionCallers(
	t *testing.T,
	db *sql.DB,
	dia dialect.Dialect,
	schema string,
	name string,
) map[dialect.TriggerKey]dialect.TriggerInfo {
	t.Helper()
	inventory, ok := dia.(dialect.SchemaTriggerCallerInventory)
	if !ok {
		t.Fatal("PostgreSQL dialect lacks transition caller inventory")
	}
	callers, err := inventory.SchemaTriggerCallers(
		context.Background(), db,
		[]dialect.SchemaTriggerFunctionKey{{Schema: schema, Name: name}},
	)
	if err != nil {
		t.Fatal(err)
	}
	return callers
}

func assertPostgresTransitionRolledBack(
	t *testing.T,
	db *sql.DB,
	dia dialect.Dialect,
	oldDigest string,
) {
	t.Helper()
	if got := liveTransitionDigest(t, db, dia, transitionTestTable, transitionTestTrigger); got != oldDigest {
		t.Fatalf("selected trigger changed despite rollback: digest %s, want %s", got, oldDigest)
	}
	if got := countRows(t, db,
		"SELECT COUNT(*) FROM schema_migrations_mod_"+transitionTestNamespace); got != 0 {
		t.Fatalf("tracking rows after rollback = %d, want 0", got)
	}
}

type transitionReservationRecorder struct {
	statements []string
	args       [][]any
}

func (r *transitionReservationRecorder) QueryContext(
	context.Context,
	string,
	...any,
) (*sql.Rows, error) {
	return nil, errors.New("reservation renderer unexpectedly queried")
}

func (r *transitionReservationRecorder) ExecContext(
	_ context.Context,
	query string,
	args ...any,
) (sql.Result, error) {
	r.statements = append(r.statements, query)
	r.args = append(r.args, append([]any(nil), args...))
	return nil, nil
}
