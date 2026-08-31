// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"reflect"
	"testing"

	"github.com/olivaresai/olivares/core/internal/store/dialect"
	"github.com/olivaresai/olivares/core/store"
)

func TestCoreMigrationVersionPreflightSQLite(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	dia, ok := dialect.New(store.EngineSQLite)
	if !ok {
		t.Fatal("SQLite dialect unavailable")
	}

	assertCoreVersionContinues(t, ctx, db, dia, "no tracking table")
	assertCoreVersionControlsAbsent(t, ctx, db, dia)

	createCoreVersionTracking(t, ctx, db)
	insertCoreVersion(t, ctx, db, dia, 7, nil)
	assertCoreVersionContinues(t, ctx, db, dia, "supported v7")
	assertCoreVersionControlsAbsent(t, ctx, db, dia)

	insertCoreVersion(t, ctx, db, dia, 8, nil)
	assertCoreVersionAheadIsReadOnly(t, ctx, db, dia, "active v8")
	if _, err := db.ExecContext(ctx,
		dia.Rebind("UPDATE "+coreTrackingTable+" SET reverted_at = ? WHERE version = ?"),
		"2026-08-14T00:00:00Z", 8); err != nil {
		t.Fatalf("mark v8 reverted: %v", err)
	}
	assertCoreVersionAheadIsReadOnly(t, ctx, db, dia, "reverted v8")
}

func TestCoreMigrationVersionPreflightPostgres(t *testing.T) {
	pg := isolatedPG(t)
	ctx := context.Background()
	db, err := sql.Open("pgx", pg.App)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	dia, ok := dialect.New(store.EnginePostgres)
	if !ok {
		t.Fatal("PostgreSQL dialect unavailable")
	}

	assertCoreVersionContinues(t, ctx, db, dia, "no tracking table")
	assertCoreVersionControlsAbsent(t, ctx, db, dia)

	createCoreVersionTracking(t, ctx, db)
	insertCoreVersion(t, ctx, db, dia, 7, nil)
	assertCoreVersionContinues(t, ctx, db, dia, "supported v7")
	assertCoreVersionControlsAbsent(t, ctx, db, dia)

	insertCoreVersion(t, ctx, db, dia, 8, nil)
	assertCoreVersionAheadIsReadOnly(t, ctx, db, dia, "active v8")
	if _, err := db.ExecContext(ctx,
		dia.Rebind("UPDATE "+coreTrackingTable+" SET reverted_at = ? WHERE version = ?"),
		"2026-08-14T00:00:00Z", 8); err != nil {
		t.Fatalf("mark v8 reverted: %v", err)
	}
	assertCoreVersionAheadIsReadOnly(t, ctx, db, dia, "reverted v8")
}

func TestCoreMigrationVersionPreflightRefusesAnIllegibleTracker(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	dia, ok := dialect.New(store.EngineSQLite)
	if !ok {
		t.Fatal("SQLite dialect unavailable")
	}
	if _, err := db.ExecContext(ctx,
		"CREATE TABLE "+coreTrackingTable+" (version INTEGER PRIMARY KEY, name TEXT NOT NULL)"); err != nil {
		t.Fatal(err)
	}

	before := coreVersionSchemaSnapshot(t, ctx, db, dia)
	continued, err := runCoreVersionPreflight(ctx, db, dia)
	if err == nil {
		t.Fatal("an existing tracker without applied_at was accepted")
	}
	if continued {
		t.Fatal("migration callback continued after the malformed tracker refusal")
	}
	after := coreVersionSchemaSnapshot(t, ctx, db, dia)
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("read-only malformed-tracker refusal changed schema\nbefore=%v\nafter=%v", before, after)
	}
	assertCoreVersionControlsAbsent(t, ctx, db, dia)
}

func TestCoreMigrationVersionPreflightRefusesMalformedTrackerShape(t *testing.T) {
	t.Run("SQLite text affinity cannot hide a future version", func(t *testing.T) {
		ctx := context.Background()
		db, err := sql.Open("sqlite", ":memory:")
		if err != nil {
			t.Fatal(err)
		}
		db.SetMaxOpenConns(1)
		t.Cleanup(func() { _ = db.Close() })
		dia, ok := dialect.New(store.EngineSQLite)
		if !ok {
			t.Fatal("SQLite dialect unavailable")
		}
		if _, err := db.ExecContext(ctx, `CREATE TABLE schema_migrations_core (
version TEXT PRIMARY KEY,
name TEXT NOT NULL,
applied_at TEXT NOT NULL,
phase TEXT NOT NULL DEFAULT 'expand',
reverted_at TEXT
)`); err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(ctx, `INSERT INTO schema_migrations_core
(version, name, applied_at, phase) VALUES
('7', 'directory_fence', '2026-08-14T00:00:00Z', 'expand'),
('100', 'future', '2026-08-14T00:00:01Z', 'expand')`); err != nil {
			t.Fatal(err)
		}
		assertMalformedCoreTrackerRefused(t, ctx, db, dia)
	})

	t.Run("SQLite missing primary key cannot admit duplicate history", func(t *testing.T) {
		ctx := context.Background()
		db, err := sql.Open("sqlite", ":memory:")
		if err != nil {
			t.Fatal(err)
		}
		db.SetMaxOpenConns(1)
		t.Cleanup(func() { _ = db.Close() })
		dia, ok := dialect.New(store.EngineSQLite)
		if !ok {
			t.Fatal("SQLite dialect unavailable")
		}
		if _, err := db.ExecContext(ctx, `CREATE TABLE schema_migrations_core (
version INTEGER NOT NULL,
name TEXT NOT NULL,
applied_at TEXT NOT NULL,
phase TEXT NOT NULL DEFAULT 'expand',
reverted_at TEXT
)`); err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(ctx, `INSERT INTO schema_migrations_core
(version, name, applied_at, phase) VALUES
(7, 'directory_fence', '2026-08-14T00:00:00Z', 'expand'),
(7, 'directory_fence', '2026-08-14T00:00:01Z', 'expand')`); err != nil {
			t.Fatal(err)
		}
		assertMalformedCoreTrackerRefused(t, ctx, db, dia)
	})

	t.Run("SQLite composite primary key is not the version authority", func(t *testing.T) {
		ctx := context.Background()
		db, err := sql.Open("sqlite", ":memory:")
		if err != nil {
			t.Fatal(err)
		}
		db.SetMaxOpenConns(1)
		t.Cleanup(func() { _ = db.Close() })
		dia, ok := dialect.New(store.EngineSQLite)
		if !ok {
			t.Fatal("SQLite dialect unavailable")
		}
		if _, err := db.ExecContext(ctx, `CREATE TABLE schema_migrations_core (
version INTEGER NOT NULL,
name TEXT NOT NULL,
applied_at TEXT NOT NULL,
phase TEXT NOT NULL DEFAULT 'expand',
reverted_at TEXT,
PRIMARY KEY(version, name)
)`); err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(ctx, `INSERT INTO schema_migrations_core
(version, name, applied_at, phase)
VALUES (7, 'directory_fence', '2026-08-14T00:00:00Z', 'expand')`); err != nil {
			t.Fatal(err)
		}
		assertMalformedCoreTrackerRefused(t, ctx, db, dia)
	})

	t.Run("SQLite generated columns remain visible to the shape witness", func(t *testing.T) {
		ctx := context.Background()
		db, err := sql.Open("sqlite", ":memory:")
		if err != nil {
			t.Fatal(err)
		}
		db.SetMaxOpenConns(1)
		t.Cleanup(func() { _ = db.Close() })
		dia, ok := dialect.New(store.EngineSQLite)
		if !ok {
			t.Fatal("SQLite dialect unavailable")
		}
		if _, err := db.ExecContext(ctx, `CREATE TABLE schema_migrations_core (
version INTEGER PRIMARY KEY,
name TEXT NOT NULL,
applied_at TEXT NOT NULL,
phase TEXT NOT NULL DEFAULT 'expand',
reverted_at TEXT,
hidden_future INTEGER GENERATED ALWAYS AS (version + 1) VIRTUAL
)`); err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(ctx, `INSERT INTO schema_migrations_core
(version, name, applied_at, phase)
VALUES (7, 'directory_fence', '2026-08-14T00:00:00Z', 'expand')`); err != nil {
			t.Fatal(err)
		}
		assertMalformedCoreTrackerRefused(t, ctx, db, dia)
	})

	t.Run("SQLite non-canonical default is refused", func(t *testing.T) {
		ctx := context.Background()
		db, err := sql.Open("sqlite", ":memory:")
		if err != nil {
			t.Fatal(err)
		}
		db.SetMaxOpenConns(1)
		t.Cleanup(func() { _ = db.Close() })
		dia, ok := dialect.New(store.EngineSQLite)
		if !ok {
			t.Fatal("SQLite dialect unavailable")
		}
		if _, err := db.ExecContext(ctx, `CREATE TABLE schema_migrations_core (
version INTEGER PRIMARY KEY,
name TEXT NOT NULL,
applied_at TEXT NOT NULL,
phase TEXT NOT NULL DEFAULT 'expand',
reverted_at TEXT DEFAULT 'tampered'
)`); err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(ctx, `INSERT INTO schema_migrations_core
(version, name, applied_at, phase, reverted_at)
VALUES (7, 'directory_fence', '2026-08-14T00:00:00Z', 'expand', NULL)`); err != nil {
			t.Fatal(err)
		}
		assertMalformedCoreTrackerRefused(t, ctx, db, dia)
	})

	t.Run("SQLite tracking trigger is refused", func(t *testing.T) {
		ctx := context.Background()
		db, err := sql.Open("sqlite", ":memory:")
		if err != nil {
			t.Fatal(err)
		}
		db.SetMaxOpenConns(1)
		t.Cleanup(func() { _ = db.Close() })
		dia, ok := dialect.New(store.EngineSQLite)
		if !ok {
			t.Fatal("SQLite dialect unavailable")
		}
		createCoreVersionTracking(t, ctx, db)
		insertCoreVersion(t, ctx, db, dia, coreDirectoryMigrationVersion, nil)
		if _, err := db.ExecContext(ctx, `CREATE TRIGGER ignore_core_tracking_insert
BEFORE INSERT ON schema_migrations_core BEGIN SELECT RAISE(IGNORE); END`); err != nil {
			t.Fatal(err)
		}
		assertMalformedCoreTrackerRefused(t, ctx, db, dia)
	})

	t.Run("PostgreSQL wrong affinity and missing primary key refuse", func(t *testing.T) {
		pg := isolatedPG(t)
		ctx := context.Background()
		db, err := sql.Open("pgx", pg.App)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = db.Close() })
		dia, ok := dialect.New(store.EnginePostgres)
		if !ok {
			t.Fatal("PostgreSQL dialect unavailable")
		}
		if _, err := db.ExecContext(ctx, `CREATE TABLE schema_migrations_core (
version TEXT NOT NULL,
name TEXT NOT NULL,
applied_at TEXT NOT NULL,
phase TEXT NOT NULL DEFAULT 'expand',
reverted_at TEXT
)`); err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(ctx, `INSERT INTO schema_migrations_core
(version, name, applied_at, phase) VALUES
('7', 'directory_fence', '2026-08-14T00:00:00Z', 'expand'),
('100', 'future', '2026-08-14T00:00:01Z', 'expand')`); err != nil {
			t.Fatal(err)
		}
		assertMalformedCoreTrackerRefused(t, ctx, db, dia)
	})

	t.Run("PostgreSQL zero-column relation is not fresh", func(t *testing.T) {
		pg := isolatedPG(t)
		ctx := context.Background()
		db, err := sql.Open("pgx", pg.App)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = db.Close() })
		dia, ok := dialect.New(store.EnginePostgres)
		if !ok {
			t.Fatal("PostgreSQL dialect unavailable")
		}
		if _, err := db.ExecContext(ctx, "CREATE TABLE schema_migrations_core ()"); err != nil {
			t.Fatal(err)
		}
		assertMalformedCoreTrackerRefused(t, ctx, db, dia)
	})

	t.Run("PostgreSQL row security cannot hide a future version", func(t *testing.T) {
		pg := isolatedPG(t)
		ctx := context.Background()
		db, err := sql.Open("pgx", pg.App)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = db.Close() })
		dia, ok := dialect.New(store.EnginePostgres)
		if !ok {
			t.Fatal("PostgreSQL dialect unavailable")
		}
		createCoreVersionTracking(t, ctx, db)
		insertCoreVersion(t, ctx, db, dia, coreDirectoryMigrationVersion, nil)
		insertCoreVersion(t, ctx, db, dia, 100, nil)
		if _, err := db.ExecContext(ctx, `ALTER TABLE schema_migrations_core ENABLE ROW LEVEL SECURITY;
ALTER TABLE schema_migrations_core FORCE ROW LEVEL SECURITY;
CREATE POLICY hide_future_core_versions ON schema_migrations_core
USING (version <= 7)`); err != nil {
			t.Fatal(err)
		}
		assertMalformedCoreTrackerRefused(t, ctx, db, dia)
	})

	t.Run("PostgreSQL unlogged tracker is refused", func(t *testing.T) {
		pg := isolatedPG(t)
		ctx := context.Background()
		db, err := sql.Open("pgx", pg.App)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = db.Close() })
		dia, ok := dialect.New(store.EnginePostgres)
		if !ok {
			t.Fatal("PostgreSQL dialect unavailable")
		}
		if _, err := db.ExecContext(ctx, `CREATE UNLOGGED TABLE schema_migrations_core (
version INTEGER PRIMARY KEY,
name TEXT NOT NULL,
applied_at TEXT NOT NULL,
phase TEXT NOT NULL DEFAULT 'expand',
reverted_at TEXT
)`); err != nil {
			t.Fatal(err)
		}
		insertCoreVersion(t, ctx, db, dia, coreDirectoryMigrationVersion, nil)
		assertMalformedCoreTrackerRefused(t, ctx, db, dia)
	})

	t.Run("PostgreSQL inherited tracker is refused", func(t *testing.T) {
		pg := isolatedPG(t)
		ctx := context.Background()
		db, err := sql.Open("pgx", pg.App)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = db.Close() })
		dia, ok := dialect.New(store.EnginePostgres)
		if !ok {
			t.Fatal("PostgreSQL dialect unavailable")
		}
		createCoreVersionTracking(t, ctx, db)
		insertCoreVersion(t, ctx, db, dia, coreDirectoryMigrationVersion, nil)
		if _, err := db.ExecContext(ctx,
			"CREATE TABLE core_tracking_child () INHERITS (schema_migrations_core)"); err != nil {
			t.Fatal(err)
		}
		assertMalformedCoreTrackerRefused(t, ctx, db, dia)
	})

	t.Run("PostgreSQL tracking trigger is refused", func(t *testing.T) {
		pg := isolatedPG(t)
		ctx := context.Background()
		db, err := sql.Open("pgx", pg.App)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = db.Close() })
		dia, ok := dialect.New(store.EnginePostgres)
		if !ok {
			t.Fatal("PostgreSQL dialect unavailable")
		}
		createCoreVersionTracking(t, ctx, db)
		insertCoreVersion(t, ctx, db, dia, coreDirectoryMigrationVersion, nil)
		if _, err := db.ExecContext(ctx, `CREATE FUNCTION ignore_core_tracking_insert()
RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RETURN NULL; END $$;
CREATE TRIGGER ignore_core_tracking_insert
BEFORE INSERT ON schema_migrations_core
FOR EACH ROW EXECUTE FUNCTION ignore_core_tracking_insert()`); err != nil {
			t.Fatal(err)
		}
		assertMalformedCoreTrackerRefused(t, ctx, db, dia)
	})

	t.Run("SQLite wrong relation kind is not fresh", func(t *testing.T) {
		ctx := context.Background()
		db, err := sql.Open("sqlite", ":memory:")
		if err != nil {
			t.Fatal(err)
		}
		db.SetMaxOpenConns(1)
		t.Cleanup(func() { _ = db.Close() })
		dia, ok := dialect.New(store.EngineSQLite)
		if !ok {
			t.Fatal("SQLite dialect unavailable")
		}
		if _, err := db.ExecContext(ctx,
			"CREATE VIEW schema_migrations_core AS SELECT 7 AS version"); err != nil {
			t.Fatal(err)
		}
		assertMalformedCoreTrackerRefused(t, ctx, db, dia)
	})
}

func assertMalformedCoreTrackerRefused(
	t *testing.T,
	ctx context.Context,
	db *sql.DB,
	dia dialect.Dialect,
) {
	t.Helper()
	before := coreVersionSchemaSnapshot(t, ctx, db, dia)
	continued, err := runCoreVersionPreflight(ctx, db, dia)
	if err == nil || continued {
		t.Fatalf("malformed tracker continued=%t error=%v, want read-only refusal", continued, err)
	}
	after := coreVersionSchemaSnapshot(t, ctx, db, dia)
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("malformed-tracker refusal changed schema\nbefore=%v\nafter=%v", before, after)
	}
	assertCoreVersionControlsAbsent(t, ctx, db, dia)
}

func TestCoreMigrationVersionPreflightReadsTheEngineSchemaNotATempShadow(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	dia, ok := dialect.New(store.EngineSQLite)
	if !ok {
		t.Fatal("SQLite dialect unavailable")
	}
	createCoreVersionTracking(t, ctx, db)
	insertCoreVersion(t, ctx, db, dia, coreDirectoryMigrationVersion, nil)
	if _, err := db.ExecContext(ctx, `CREATE TEMP TABLE schema_migrations_core (
version INTEGER PRIMARY KEY,
name TEXT NOT NULL,
applied_at TEXT NOT NULL
)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO temp.schema_migrations_core
(version, name, applied_at) VALUES (8, 'shadow-v8', '2026-08-14T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	assertCoreVersionContinues(t, ctx, db, dia, "main v7 behind temp v8")

	if _, err := db.ExecContext(ctx, "DELETE FROM temp.schema_migrations_core"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO temp.schema_migrations_core
(version, name, applied_at) VALUES (7, 'shadow-v7', '2026-08-14T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO main.schema_migrations_core
(version, name, applied_at, phase, reverted_at)
VALUES (8, 'main-v8', '2026-08-14T00:00:00Z', 'expand', NULL)`); err != nil {
		t.Fatal(err)
	}
	assertCoreVersionAheadIsReadOnly(t, ctx, db, dia, "main v8 behind temp v7")
}

func TestCoreMigrationVersionPreflightBindsTheV7TrackingRecord(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		column string
		value  any
	}{
		{name: "name", column: "name", value: "another_migration"},
		{name: "applied time", column: "applied_at", value: ""},
		{name: "phase", column: "phase", value: "contract"},
		{name: "reverted", column: "reverted_at", value: "2026-08-14T00:00:00Z"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			db, err := sql.Open("sqlite", ":memory:")
			if err != nil {
				t.Fatal(err)
			}
			db.SetMaxOpenConns(1)
			t.Cleanup(func() { _ = db.Close() })
			dia, ok := dialect.New(store.EngineSQLite)
			if !ok {
				t.Fatal("SQLite dialect unavailable")
			}
			createCoreVersionTracking(t, ctx, db)
			if _, err := db.ExecContext(ctx, `INSERT INTO main.schema_migrations_core
(version, name, applied_at, phase, reverted_at)
VALUES (?, ?, ?, ?, NULL)`, coreDirectoryMigrationVersion, coreDirectoryMigrationName,
				"2026-08-14T00:00:00Z", "expand"); err != nil {
				t.Fatal(err)
			}
			if _, err := db.ExecContext(ctx,
				"UPDATE main.schema_migrations_core SET "+tc.column+" = ? WHERE version = ?", // #nosec G202 -- closed test table
				tc.value, coreDirectoryMigrationVersion); err != nil {
				t.Fatal(err)
			}
			before := coreVersionSchemaSnapshot(t, ctx, db, dia)
			continued, err := runCoreVersionPreflight(ctx, db, dia)
			if err == nil || continued {
				t.Fatalf("tampered v7 continued=%t error=%v, want read-only refusal", continued, err)
			}
			after := coreVersionSchemaSnapshot(t, ctx, db, dia)
			if !reflect.DeepEqual(after, before) {
				t.Fatalf("tampered v7 refusal changed schema\nbefore=%v\nafter=%v", before, after)
			}
		})
	}
}

// TestOpenRunsCoreVersionPreflightFirstUnderTheMigrationLock pins the production
// wiring, not merely the helper. The first statement in withMigrationLock's callback
// must be the future-version preflight; moving it below classification lets the three
// rollout-control tables commit before the old binary refuses the database.
func TestOpenRunsCoreVersionPreflightFirstUnderTheMigrationLock(t *testing.T) {
	t.Parallel()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "store.go", nil, 0)
	if err != nil {
		t.Fatalf("parse store.go: %v", err)
	}

	var openBody *ast.BlockStmt
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if ok && fn.Name.Name == "Open" {
			openBody = fn.Body
			break
		}
	}
	if openBody == nil {
		t.Fatal("store.go has no Open function")
	}
	var callback *ast.FuncLit
	ast.Inspect(openBody, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		fn, ok := call.Fun.(*ast.Ident)
		if !ok || fn.Name != "withMigrationLock" || len(call.Args) != 4 {
			return true
		}
		callback, _ = call.Args[3].(*ast.FuncLit)
		return false
	})
	if callback == nil || len(callback.Body.List) == 0 {
		t.Fatal("Open has no inspectable withMigrationLock callback")
	}
	first, ok := callback.Body.List[0].(*ast.IfStmt)
	if !ok {
		t.Fatalf("first migration-lock operation is %T, want the core-version preflight if statement", callback.Body.List[0])
	}
	init, ok := first.Init.(*ast.AssignStmt)
	if !ok || len(init.Rhs) != 1 {
		t.Fatalf("first migration-lock if has init %T, want err := preflightCoreMigrationVersion(...)", first.Init)
	}
	call, ok := init.Rhs[0].(*ast.CallExpr)
	if !ok {
		t.Fatalf("first migration-lock init RHS is %T, want a call", init.Rhs[0])
	}
	fn, ok := call.Fun.(*ast.Ident)
	if !ok || fn.Name != "preflightCoreMigrationVersion" {
		t.Fatalf("first migration-lock call is %T/%v, want preflightCoreMigrationVersion", call.Fun, fn)
	}
	if len(call.Args) != 4 {
		t.Fatalf("preflightCoreMigrationVersion has %d arguments in Open, want ctx, mdb, dia, supported version", len(call.Args))
	}
	version, ok := call.Args[3].(*ast.Ident)
	if !ok || version.Name != "coreSupportedMigrationVersion" {
		t.Fatalf("Open declares supported core version %T/%v, want coreSupportedMigrationVersion", call.Args[3], version)
	}
}

func TestSupportedCoreVersionMatchesMigrationPlan(t *testing.T) {
	t.Parallel()
	dia, ok := dialect.New(store.EngineSQLite)
	if !ok {
		t.Fatal("SQLite dialect unavailable")
	}
	migrations := buildCoreMigrations(dia, coreDescriptors(), nil, nil)
	var latest int
	for _, migration := range migrations {
		if migration.Version > latest {
			latest = migration.Version
		}
	}
	if latest != coreSupportedMigrationVersion {
		t.Fatalf("supported core version = %d, latest migration = %d", coreSupportedMigrationVersion, latest)
	}
}

func createCoreVersionTracking(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()
	if _, err := db.ExecContext(ctx, "CREATE TABLE "+coreTrackingTable+` (
version INTEGER PRIMARY KEY,
name TEXT NOT NULL,
applied_at TEXT NOT NULL,
phase TEXT NOT NULL DEFAULT 'expand',
reverted_at TEXT
)`); err != nil {
		t.Fatalf("create core tracking fixture: %v", err)
	}
}

func insertCoreVersion(
	t *testing.T,
	ctx context.Context,
	db *sql.DB,
	dia dialect.Dialect,
	version int,
	revertedAt any,
) {
	t.Helper()
	name := fmt.Sprintf("v%d", version)
	if version == coreDirectoryMigrationVersion {
		name = coreDirectoryMigrationName
	}
	if _, err := db.ExecContext(ctx, dia.Rebind(
		"INSERT INTO "+coreTrackingTable+" (version, name, applied_at, phase, reverted_at) VALUES (?, ?, ?, ?, ?)"),
		version, name, "2026-08-14T00:00:00Z", "expand", revertedAt); err != nil {
		t.Fatalf("insert core v%d fixture: %v", version, err)
	}
}

func runCoreVersionPreflight(
	ctx context.Context,
	db *sql.DB,
	dia dialect.Dialect,
) (continued bool, err error) {
	err = withMigrationLock(ctx, db, dia, func(mdb dialect.Execer) error {
		if err := preflightCoreMigrationVersion(ctx, mdb, dia, coreSupportedMigrationVersion); err != nil {
			return err
		}
		continued = true
		return nil
	})
	return continued, err
}

func assertCoreVersionContinues(
	t *testing.T,
	ctx context.Context,
	db *sql.DB,
	dia dialect.Dialect,
	state string,
) {
	t.Helper()
	before := coreVersionSchemaSnapshot(t, ctx, db, dia)
	continued, err := runCoreVersionPreflight(ctx, db, dia)
	if err != nil {
		t.Fatalf("%s: preflight: %v", state, err)
	}
	if !continued {
		t.Fatalf("%s: the migration callback did not continue", state)
	}
	after := coreVersionSchemaSnapshot(t, ctx, db, dia)
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("%s: read-only preflight changed schema\nbefore=%v\nafter=%v", state, before, after)
	}
}

func assertCoreVersionAheadIsReadOnly(
	t *testing.T,
	ctx context.Context,
	db *sql.DB,
	dia dialect.Dialect,
	state string,
) {
	t.Helper()
	before := coreVersionSchemaSnapshot(t, ctx, db, dia)
	continued, err := runCoreVersionPreflight(ctx, db, dia)
	if !errors.Is(err, ErrCoreSchemaVersionAhead) {
		t.Fatalf("%s: preflight = %v, want ErrCoreSchemaVersionAhead", state, err)
	}
	if continued {
		t.Fatalf("%s: migration callback continued after the future-version refusal", state)
	}
	after := coreVersionSchemaSnapshot(t, ctx, db, dia)
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("%s: future-version refusal changed schema\nbefore=%v\nafter=%v", state, before, after)
	}
	assertCoreVersionControlsAbsent(t, ctx, db, dia)
}

func assertCoreVersionControlsAbsent(
	t *testing.T,
	ctx context.Context,
	db *sql.DB,
	dia dialect.Dialect,
) {
	t.Helper()
	tables := []string{
		dialect.ControlRolloutStateTable,
		dialect.ControlRolloutTransitionTable,
		dialect.ControlRolloutClassificationTable,
	}
	tables = append(tables, dialect.GuardControlPlaneTables()...)
	for _, table := range tables {
		cols, err := dia.TableColumns(ctx, db, table)
		if err != nil {
			t.Fatalf("inspect control relation %s: %v", table, err)
		}
		if len(cols) != 0 {
			t.Errorf("read-only core-version preflight created control relation %s", table)
		}
	}
}

func coreVersionSchemaSnapshot(
	t *testing.T,
	ctx context.Context,
	db *sql.DB,
	dia dialect.Dialect,
) []string {
	t.Helper()
	var query string
	switch dia.Name() {
	case store.EngineSQLite:
		query = `SELECT type, name, tbl_name, COALESCE(sql, '')
FROM sqlite_master
ORDER BY type, name, tbl_name, COALESCE(sql, '')`
	case store.EnginePostgres:
		query = `SELECT 'relation', c.relkind::text, c.relname, ''
FROM pg_catalog.pg_class c
JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
WHERE n.nspname = 'public'
UNION ALL
SELECT 'column', cols.table_name, cols.column_name,
       cols.ordinal_position::text || ':' || cols.data_type || ':' || cols.is_nullable || ':' || COALESCE(cols.column_default, '')
FROM information_schema.columns cols
WHERE cols.table_schema = 'public'
ORDER BY 1, 2, 3, 4`
	default:
		t.Fatalf("unsupported snapshot engine %q", dia.Name())
	}
	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		t.Fatalf("snapshot %s schema: %v", dia.Name(), err)
	}
	defer rows.Close() //nolint:errcheck // test read
	var snapshot []string
	for rows.Next() {
		var a, b, c, d string
		if err := rows.Scan(&a, &b, &c, &d); err != nil {
			t.Fatalf("scan %s schema snapshot: %v", dia.Name(), err)
		}
		snapshot = append(snapshot, fmt.Sprintf("%s|%s|%s|%s", a, b, c, d))
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read %s schema snapshot: %v", dia.Name(), err)
	}
	return snapshot
}
