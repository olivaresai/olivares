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
	"strings"
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

	// The ceiling is derived rather than spelled, so appending a core migration moves
	// this boundary with it instead of leaving the test measuring a version that is now
	// supported. It was a literal 8 until the access-evidence foundation made 8 ordinary.
	ahead := coreSupportedMigrationVersion + 1
	// The compiled plan, not the integers: v12 is reserved and unregistered (R5).
	for _, compiled := range compiledCoreMigrationVersionOrder(dia) {
		version := int(compiled)
		if version <= coreDirectoryMigrationVersion {
			continue
		}
		insertCoreVersion(t, ctx, db, dia, version, nil)
		assertCoreVersionContinues(t, ctx, db, dia, fmt.Sprintf("supported v%d", version))
	}
	insertCoreVersion(t, ctx, db, dia, ahead, nil)
	assertCoreVersionAheadIsReadOnly(t, ctx, db, dia, fmt.Sprintf("active v%d", ahead))
	if _, err := db.ExecContext(ctx,
		dia.Rebind("UPDATE "+coreTrackingTable+" SET reverted_at = ? WHERE version = ?"),
		"2026-08-14T00:00:00Z", ahead); err != nil {
		t.Fatalf("mark v%d reverted: %v", ahead, err)
	}
	assertCoreVersionAheadIsReadOnly(t, ctx, db, dia, fmt.Sprintf("reverted v%d", ahead))
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

	// The ceiling is derived, for the same reason as in the SQLite case: a literal 8
	// stopped being "ahead" when the access-evidence foundation made it ordinary.
	ahead := coreSupportedMigrationVersion + 1
	// The compiled plan, not the integers: v12 is reserved and unregistered (R5).
	for _, compiled := range compiledCoreMigrationVersionOrder(dia) {
		version := int(compiled)
		if version <= coreDirectoryMigrationVersion {
			continue
		}
		insertCoreVersion(t, ctx, db, dia, version, nil)
		assertCoreVersionContinues(t, ctx, db, dia, fmt.Sprintf("supported v%d", version))
	}
	insertCoreVersion(t, ctx, db, dia, ahead, nil)
	assertCoreVersionAheadIsReadOnly(t, ctx, db, dia, fmt.Sprintf("active v%d", ahead))
	if _, err := db.ExecContext(ctx,
		dia.Rebind("UPDATE "+coreTrackingTable+" SET reverted_at = ? WHERE version = ?"),
		"2026-08-14T00:00:00Z", ahead); err != nil {
		t.Fatalf("mark v%d reverted: %v", ahead, err)
	}
	assertCoreVersionAheadIsReadOnly(t, ctx, db, dia, fmt.Sprintf("reverted v%d", ahead))
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
	if _, err := db.ExecContext(ctx, fmt.Sprintf(`INSERT INTO temp.schema_migrations_core
(version, name, applied_at) VALUES (%d, 'shadow-ahead', '2026-08-14T00:00:00Z')`,
		coreSupportedMigrationVersion+1)); err != nil {
		t.Fatal(err)
	}
	assertCoreVersionContinues(t, ctx, db, dia, "main v7 behind an ahead temp shadow")

	if _, err := db.ExecContext(ctx, "DELETE FROM temp.schema_migrations_core"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO temp.schema_migrations_core
(version, name, applied_at) VALUES (7, 'shadow-v7', '2026-08-14T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, fmt.Sprintf(`INSERT INTO main.schema_migrations_core
(version, name, applied_at, phase, reverted_at)
VALUES (%d, 'main-ahead', '2026-08-14T00:00:00Z', 'expand', NULL)`,
		coreSupportedMigrationVersion+1)); err != nil {
		t.Fatal(err)
	}
	assertCoreVersionAheadIsReadOnly(t, ctx, db, dia, "an ahead main version behind temp v7")
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
// wiring, not merely the helper. Open is a one-line wrapper of openPrepared; the
// first statement in that function's withMigrationLock callback must still be the
// future-version preflight. Inspecting openPrepared without proving Open delegates
// to it would miss a wrapper that never reaches the lock. Moving the preflight
// below classification lets the three rollout-control tables commit before the
// old binary refuses the database.
func TestOpenRunsCoreVersionPreflightFirstUnderTheMigrationLock(t *testing.T) {
	t.Parallel()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "store.go", nil, 0)
	if err != nil {
		t.Fatalf("parse store.go: %v", err)
	}

	openFn := sqlstoreFileFunc(file, "Open")
	if openFn == nil || openFn.Body == nil {
		t.Fatal("store.go has no Open function")
	}
	preparedName := openMustReturnPreparedOpener(t, openFn)
	preparedFn := sqlstoreFileFunc(file, preparedName)
	if preparedFn == nil || preparedFn.Body == nil {
		t.Fatalf("Open delegates to %s, which is not declared in store.go", preparedName)
	}
	callback := firstWithMigrationLockCallback(preparedFn.Body)
	if callback == nil || callback.Body == nil || len(callback.Body.List) == 0 {
		t.Fatalf("%s has no inspectable withMigrationLock callback on the Open path", preparedName)
	}
	assertFirstMigrationLockOpIsSupportedVersionPreflight(t, callback.Body.List[0])
}

func sqlstoreFileFunc(file *ast.File, name string) *ast.FuncDecl {
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if ok && fn.Recv == nil && fn.Name.Name == name {
			return fn
		}
	}
	return nil
}

func openMustReturnPreparedOpener(t *testing.T, openFn *ast.FuncDecl) string {
	t.Helper()
	name, err := checkOpenDelegatesToPreparedOpener(openFn)
	if err != nil {
		t.Fatal(err)
	}
	return name
}

// checkOpenDelegatesToPreparedOpener is the shape guard as a PURE function, so the
// guard itself can be shown to reject the shapes it exists to reject (R63 C1).
//
// A guard nobody has watched refuse is not a guard. Returning an error instead of
// calling t.Fatal is the whole difference: the production assertion above is
// unchanged, and the negative cases below can feed it a synthetic Open and read the
// refusal rather than take the guard's word for it.
//
// WHAT IT PINS, and why each part is load-bearing:
//
//   - ONE statement, ONE return, ONE call. A wrapper that does anything before
//     delegating is a wrapper that can reach the store without the migration lock.
//   - SIX arguments. R62 pinned five; the restore composition threads publication
//     inputs through openPrepared, so the count moved — but the count is not the
//     point, the ARGUMENTS are, which is why the last two are checked by value
//     rather than merely counted.
//   - `prepareThroughReadiness`, so Open cannot quietly become a partial preparation.
//   - a NIL maintenance callback. A maintenance function on the ordinary caller would
//     run inside the lock ahead of the preflight.
//   - a ZERO `publicationInputs{}`. A witness supplied here would let the ordinary
//     constructor tighten or relax the restore fence, which is exactly what
//     OpenWithRestoreWitness exists to keep separate and explicit.
func checkOpenDelegatesToPreparedOpener(openFn *ast.FuncDecl) (string, error) {
	if openFn == nil || openFn.Body == nil {
		return "", errors.New("Open is not declared with a body")
	}
	if len(openFn.Body.List) != 1 {
		return "", fmt.Errorf("Open body has %d statements, want the single openPrepared return", len(openFn.Body.List))
	}
	ret, ok := openFn.Body.List[0].(*ast.ReturnStmt)
	if !ok || len(ret.Results) != 1 {
		return "", errors.New("Open is not a single-call return, so the migration-lock preflight is not on the Open path")
	}
	call, ok := ret.Results[0].(*ast.CallExpr)
	if !ok {
		return "", errors.New("Open does not return a call")
	}
	ident, ok := call.Fun.(*ast.Ident)
	if !ok || ident.Name != "openPrepared" {
		got := "<non-ident>"
		if ident != nil {
			got = ident.Name
		} else if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
			got = sel.Sel.Name
		}
		return "", fmt.Errorf("Open delegates to %s, want openPrepared", got)
	}
	if len(call.Args) != 6 {
		return "", fmt.Errorf(
			"Open → openPrepared has %d arguments, want ctx, cfg, register, purpose, maintenance, publication inputs",
			len(call.Args),
		)
	}
	purpose, ok := call.Args[3].(*ast.Ident)
	if !ok || purpose.Name != "prepareThroughReadiness" {
		return "", fmt.Errorf("Open asks openPrepared for purpose %T/%v, want prepareThroughReadiness", call.Args[3], purpose)
	}
	if maintenance, ok := call.Args[4].(*ast.Ident); !ok || maintenance.Name != "nil" {
		return "", fmt.Errorf(
			"Open passes maintenance %T/%v, want nil: a maintenance callback on the ordinary caller runs inside the migration lock ahead of the preflight",
			call.Args[4], call.Args[4],
		)
	}
	inputs, ok := call.Args[5].(*ast.CompositeLit)
	if !ok {
		return "", fmt.Errorf(
			"Open passes publication inputs %T, want the zero publicationInputs{} composite literal",
			call.Args[5],
		)
	}
	inputsType, ok := inputs.Type.(*ast.Ident)
	if !ok || inputsType.Name != "publicationInputs" {
		return "", fmt.Errorf("Open passes a %v literal as publication inputs, want publicationInputs", inputs.Type)
	}
	if len(inputs.Elts) != 0 {
		return "", fmt.Errorf(
			"Open passes a NONZERO publicationInputs{} with %d field(s); the ordinary constructor must not carry a witness or a handed-down admission — that is what OpenWithRestoreWitness is for",
			len(inputs.Elts),
		)
	}
	return ident.Name, nil
}

func firstWithMigrationLockCallback(body *ast.BlockStmt) *ast.FuncLit {
	var callback *ast.FuncLit
	ast.Inspect(body, func(node ast.Node) bool {
		if callback != nil {
			return false
		}
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
	return callback
}

func assertFirstMigrationLockOpIsSupportedVersionPreflight(t *testing.T, first ast.Stmt) {
	t.Helper()
	if err := checkFirstMigrationLockOpIsSupportedVersionPreflight(first); err != nil {
		t.Fatal(err)
	}
}

// checkFirstMigrationLockOpIsSupportedVersionPreflight is the first-operation
// assertion as a pure function, for the same reason as the shape guard above: the
// negative cases below prove it actually refuses a preflight that was removed or
// deferred behind another statement.
func checkFirstMigrationLockOpIsSupportedVersionPreflight(first ast.Stmt) error {
	ifStmt, ok := first.(*ast.IfStmt)
	if !ok {
		return fmt.Errorf("first migration-lock operation is %T, want the core-version preflight if statement", first)
	}
	init, ok := ifStmt.Init.(*ast.AssignStmt)
	if !ok || len(init.Rhs) != 1 {
		return fmt.Errorf("first migration-lock if has init %T, want err := preflightCoreMigrationVersion(...)", ifStmt.Init)
	}
	call, ok := init.Rhs[0].(*ast.CallExpr)
	if !ok {
		return fmt.Errorf("first migration-lock init RHS is %T, want a call", init.Rhs[0])
	}
	fn, ok := call.Fun.(*ast.Ident)
	if !ok || fn.Name != "preflightCoreMigrationVersion" {
		return fmt.Errorf("first migration-lock call is %T/%v, want preflightCoreMigrationVersion", call.Fun, fn)
	}
	if len(call.Args) != 4 {
		return fmt.Errorf("preflightCoreMigrationVersion has %d arguments in Open, want ctx, mdb, dia, supported version", len(call.Args))
	}
	version, ok := call.Args[3].(*ast.Ident)
	if !ok || version.Name != "coreSupportedMigrationVersion" {
		return fmt.Errorf("Open declares supported core version %T/%v, want coreSupportedMigrationVersion", call.Args[3], version)
	}
	return nil
}

// TestOpenShapeGuardRefusesTheShapesItExistsToRefuse drives the two guards above
// against synthetic sources (R63 C1).
//
// The guards passed for months on a tree that had never violated them, which proves
// nothing about what they would do if it did. Each case below is a shape the
// composition made reachable: a preflight moved below another statement, a
// maintenance callback on the ordinary caller, and a witness smuggled into the
// publication inputs that OpenWithRestoreWitness is supposed to be the only door for.
func TestOpenShapeGuardRefusesTheShapesItExistsToRefuse(t *testing.T) {
	t.Parallel()

	parseOpen := func(t *testing.T, body string) *ast.FuncDecl {
		t.Helper()
		src := "package sqlstore\n\nfunc Open(ctx context.Context, cfg store.Config, register func(store.ExtensionRegistry) error) (store.Store, error) {\n" + body + "\n}\n"
		file, err := parser.ParseFile(token.NewFileSet(), "synthetic.go", src, 0)
		if err != nil {
			t.Fatalf("parse synthetic Open: %v", err)
		}
		return sqlstoreFileFunc(file, "Open")
	}

	shapes := []struct {
		name string
		body string
		want string
	}{
		{
			"the real production shape is accepted",
			"\treturn openPrepared(ctx, cfg, register, prepareThroughReadiness, nil, publicationInputs{})",
			"",
		},
		{
			"a nonzero witness on the ordinary caller",
			"\treturn openPrepared(ctx, cfg, register, prepareThroughReadiness, nil, publicationInputs{witness: witness})",
			"NONZERO publicationInputs",
		},
		{
			"a maintenance callback on the ordinary caller",
			"\treturn openPrepared(ctx, cfg, register, prepareThroughReadiness, maintain, publicationInputs{})",
			"want nil",
		},
		{
			"the five-argument shape the composition superseded",
			"\treturn openPrepared(ctx, cfg, register, prepareThroughReadiness, nil)",
			"has 5 arguments",
		},
		{
			"a wrapper that does something before delegating",
			"\tlog()\n\treturn openPrepared(ctx, cfg, register, prepareThroughReadiness, nil, publicationInputs{})",
			"want the single openPrepared return",
		},
		{
			"a partial preparation purpose",
			"\treturn openPrepared(ctx, cfg, register, prepareSchemaOnly, nil, publicationInputs{})",
			"want prepareThroughReadiness",
		},
	}
	for _, shape := range shapes {
		t.Run(shape.name, func(t *testing.T) {
			t.Parallel()
			_, err := checkOpenDelegatesToPreparedOpener(parseOpen(t, shape.body))
			switch {
			case shape.want == "" && err != nil:
				t.Fatalf("the production shape was refused: %v", err)
			case shape.want == "":
			case err == nil:
				t.Fatalf("the guard ACCEPTED %s; it must refuse it", shape.name)
			case !strings.Contains(err.Error(), shape.want):
				t.Fatalf("refusal = %v, want it to name %q", err, shape.want)
			}
		})
	}

	// And the first-operation assertion, against a preflight that is missing and one
	// that is merely deferred — the two ways the R62 invariant is actually lost.
	firstStmt := func(t *testing.T, body string) ast.Stmt {
		t.Helper()
		file, err := parser.ParseFile(token.NewFileSet(), "synthetic.go",
			"package sqlstore\n\nfunc x() {\n"+body+"\n}\n", 0)
		if err != nil {
			t.Fatalf("parse synthetic callback: %v", err)
		}
		return sqlstoreFileFunc(file, "x").Body.List[0]
	}
	t.Run("the preflight is the first operation", func(t *testing.T) {
		t.Parallel()
		if err := checkFirstMigrationLockOpIsSupportedVersionPreflight(firstStmt(t,
			"\tif err := preflightCoreMigrationVersion(ctx, mdb, dia, coreSupportedMigrationVersion); err != nil {\n\t\treturn err\n\t}")); err != nil {
			t.Fatalf("the production first operation was refused: %v", err)
		}
	})
	t.Run("a DEFERRED preflight is refused", func(t *testing.T) {
		t.Parallel()
		err := checkFirstMigrationLockOpIsSupportedVersionPreflight(firstStmt(t,
			"\tif err := classifyRolloutControls(ctx, mdb, dia); err != nil {\n\t\treturn err\n\t}"))
		if err == nil || !strings.Contains(err.Error(), "want preflightCoreMigrationVersion") {
			t.Fatalf("a deferred preflight was accepted: %v", err)
		}
	})
	t.Run("a MISSING preflight is refused", func(t *testing.T) {
		t.Parallel()
		err := checkFirstMigrationLockOpIsSupportedVersionPreflight(firstStmt(t, "\treturn nil"))
		if err == nil || !strings.Contains(err.Error(), "want the core-version preflight if statement") {
			t.Fatalf("a missing preflight was accepted: %v", err)
		}
	})
}

func TestSupportedCoreVersionMatchesMigrationPlan(t *testing.T) {
	t.Parallel()
	dia, ok := dialect.New(store.EngineSQLite)
	if !ok {
		t.Fatal("SQLite dialect unavailable")
	}
	migrations := buildCoreMigrationPlan(dia, coreDescriptors(), nil, nil, guardEditionGraph{}, accessEvidenceBootPlan{}, evidenceStateCalibration{})
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
