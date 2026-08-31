// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/internal/pgtest"
	"github.com/olivaresai/olivares/core/internal/store/dialect"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

type directoryWriterTestExecer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func TestDirectoryWriterSQLiteFreshReopenPreservesRawContract(t *testing.T) {
	ctx := context.Background()
	dsn := filepath.Join(t.TempDir(), "writer-reopen.db")
	cfg := store.Config{Engine: store.EngineSQLite, DSN: dsn}

	first, err := Open(ctx, cfg, nil)
	if err != nil {
		t.Fatalf("fresh Open: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("close fresh store: %v", err)
	}
	second, err := Open(ctx, cfg, nil)
	if err != nil {
		t.Fatalf("Open after Close: %v", err)
	}
	if err := second.Close(); err != nil {
		t.Fatalf("close reopened store: %v", err)
	}

	raw, err := openSQLite(dsn)
	if err != nil {
		t.Fatalf("open raw SQLite database: %v", err)
	}
	defer raw.Close() //nolint:errcheck

	var tracked int
	if err := raw.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM "+coreTrackingTable+" WHERE version = ? AND reverted_at IS NULL",
		coreDirectoryMigrationVersion).Scan(&tracked); err != nil {
		t.Fatalf("read v7 tracking row: %v", err)
	}
	if tracked != 1 {
		t.Fatalf("active v7 tracking rows = %d, want 1", tracked)
	}

	var key, mode, keyType, modeType, generationType string
	var generation int64
	if err := raw.QueryRowContext(ctx, `SELECT control_key, mode, expected_generation,
typeof(control_key), typeof(mode), typeof(expected_generation)
FROM main.directory_writer_control`).Scan(
		&key, &mode, &generation, &keyType, &modeType, &generationType); err != nil {
		t.Fatalf("read raw writer singleton: %v", err)
	}
	if key != dialect.DirectoryWriterControlKey || mode != string(directoryWriterStaged) ||
		generation != 1 || keyType != "text" || modeType != "text" || generationType != "integer" {
		t.Fatalf("raw writer singleton = key:%q mode:%q generation:%d types:%s/%s/%s",
			key, mode, generation, keyType, modeType, generationType)
	}
	var markerRows int
	if err := raw.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM main."+dialect.DirectoryWriterMarkerTable).Scan(&markerRows); err != nil {
		t.Fatalf("read raw writer marker: %v", err)
	}
	if markerRows != 0 {
		t.Fatalf("durable marker rows after reopen = %d, want 0", markerRows)
	}

	specs := sqliteDirectoryWriterGuardSpecs()
	if len(specs) != len(directoryWriterSourceTables)*3 {
		t.Fatalf("guard specs = %d, want three events for each of %d source tables",
			len(specs), len(directoryWriterSourceTables))
	}
	for _, spec := range specs {
		var table, definition string
		if err := raw.QueryRowContext(ctx,
			"SELECT tbl_name, sql FROM main.sqlite_master WHERE type = 'trigger' AND name = ?",
			spec.Name).Scan(&table, &definition); err != nil {
			t.Fatalf("read trigger %s: %v", spec.Name, err)
		}
		if table != spec.Table || definition != spec.Definition {
			t.Errorf("trigger %s = table:%q definition:%q, want table:%q definition:%q",
				spec.Name, table, definition, spec.Table, spec.Definition)
		}
	}
}

func TestDirectoryWriterSQLiteRawStagedAndEnforcedGeneration(t *testing.T) {
	h := newDirectoryWriterSQLiteHarness(t)
	ctx := context.Background()

	var triggerCount int
	if err := h.db.QueryRowContext(ctx, `SELECT COUNT(*)
FROM main.sqlite_master
WHERE type = 'trigger' AND instr(name, '_directory_writer_guard_') > 0`).Scan(&triggerCount); err != nil {
		t.Fatalf("count writer triggers: %v", err)
	}
	if want := len(directoryWriterSourceTables) * 3; triggerCount != want {
		t.Fatalf("writer trigger count = %d, want %d", triggerCount, want)
	}

	// Staged admits all three mutation events on every member of the closed source inventory.
	for i, table := range directoryWriterSourceTables {
		directoryWriterTestExerciseSQLiteSource(t, h.db, table, fmt.Sprintf("staged-%d", i))
	}

	directoryWriterTestMustExec(t, h.db, `UPDATE main.directory_writer_control
SET mode = 'enforced', expected_generation = 7`)
	// In enforced mode the absence of a marker refuses every source table, including a
	// non-System org. Failed INSERT statements leave no row behind, so the same IDs can be
	// used below once the exact marker has been presented.
	for i, table := range directoryWriterSourceTables {
		id := fmt.Sprintf("enforced-%d", i)
		var err error
		if table == "orgs" {
			_, err = h.db.ExecContext(ctx,
				"INSERT INTO orgs(id, tenant_id, status, note) VALUES (?, ?, 'active', '')",
				id, "11111111-1111-1111-1111-111111111111")
		} else {
			_, err = h.db.ExecContext(ctx, "INSERT INTO "+table+"(id) VALUES (?)", id)
		}
		directoryWriterTestWantError(t, err, "directory writer generation required")
	}

	tx, err := h.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin generation transaction: %v", err)
	}
	defer tx.Rollback() //nolint:errcheck
	directoryWriterTestMustExec(t, tx,
		"INSERT INTO main.directory_writer_marker(control_key, generation) VALUES (?, ?)",
		dialect.DirectoryWriterControlKey, 6)
	_, err = tx.ExecContext(ctx, "INSERT INTO users(id) VALUES ('wrong-generation')")
	directoryWriterTestWantError(t, err, "directory writer generation required")
	directoryWriterTestMustExec(t, tx,
		"UPDATE main.directory_writer_marker SET generation = 7 WHERE control_key = ?",
		dialect.DirectoryWriterControlKey)
	for i, table := range directoryWriterSourceTables {
		directoryWriterTestExerciseSQLiteSource(t, tx, table, fmt.Sprintf("enforced-%d", i))
	}
	directoryWriterTestMustExec(t, tx, "DELETE FROM main.directory_writer_marker")
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit exact-generation transaction: %v", err)
	}
	var markerRows int
	if err := h.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM main.directory_writer_marker").Scan(&markerRows); err != nil {
		t.Fatalf("read marker baseline after generation transaction: %v", err)
	}
	if markerRows != 0 {
		t.Fatalf("marker rows after generation transaction = %d, want 0", markerRows)
	}
}

func TestDirectoryWriterSQLiteOrgExceptionsStillValidateControl(t *testing.T) {
	h := newDirectoryWriterSQLiteHarness(t)
	ctx := context.Background()
	realTenant := "22222222-2222-2222-2222-222222222222"
	systemTenant := model.SystemTenantID.String()

	directoryWriterTestMustExec(t, h.db,
		"INSERT INTO orgs(id, tenant_id, status, note) VALUES ('real', ?, 'active', '')", realTenant)
	directoryWriterTestMustExec(t, h.db, `UPDATE main.directory_writer_control
SET mode = 'enforced', expected_generation = 9`)

	// System-org lifecycle work and any status-preserving org update are the narrow
	// generation exemptions. Non-System creation, deletion, and status changes are not.
	directoryWriterTestMustExec(t, h.db,
		"INSERT INTO orgs(id, tenant_id, status, note) VALUES ('system-a', ?, 'active', '')", systemTenant)
	directoryWriterTestMustExec(t, h.db,
		"INSERT INTO orgs(id, tenant_id, status, note) VALUES ('system-b', ?, 'active', '')", systemTenant)
	directoryWriterTestMustExec(t, h.db,
		"UPDATE orgs SET status = 'suspended' WHERE id = 'system-a'")
	directoryWriterTestMustExec(t, h.db,
		"UPDATE orgs SET note = 'metadata-only' WHERE id = 'real'")

	_, err := h.db.ExecContext(ctx,
		"INSERT INTO orgs(id, tenant_id, status, note) VALUES ('real-new', ?, 'active', '')", realTenant)
	directoryWriterTestWantError(t, err, "directory writer generation required")
	_, err = h.db.ExecContext(ctx, "UPDATE orgs SET status = 'suspended' WHERE id = 'real'")
	directoryWriterTestWantError(t, err, "directory writer generation required")
	_, err = h.db.ExecContext(ctx, "DELETE FROM orgs WHERE id = 'real'")
	directoryWriterTestWantError(t, err, "directory writer generation required")
	directoryWriterTestMustExec(t, h.db, "DELETE FROM orgs WHERE id = 'system-a'")

	// The exemption skips only the generation proof. It must never skip validation of
	// the raw singleton itself.
	directoryWriterTestMustExec(t, h.db, "DELETE FROM main.directory_writer_control")
	_, err = h.db.ExecContext(ctx, "UPDATE orgs SET note = 'must-refuse' WHERE id = 'system-b'")
	directoryWriterTestWantError(t, err, "directory writer control invalid")
}

func TestDirectoryWriterSQLiteGuardReconciliationIsOneWay(t *testing.T) {
	t.Run("staged missing trigger heals", func(t *testing.T) {
		h := newDirectoryWriterSQLiteHarness(t)
		ctx := context.Background()
		const name = "users_directory_writer_guard_ins"
		directoryWriterTestMustExec(t, h.db, "DROP TRIGGER main."+name)
		if err := reconcileDirectoryWriterGuards(ctx, h.db, h.db, h.dia, false, guardRoles{}); err != nil {
			t.Fatalf("reconcile staged missing trigger: %v", err)
		}
		var definition string
		if err := h.db.QueryRowContext(ctx,
			"SELECT sql FROM main.sqlite_master WHERE type = 'trigger' AND name = ?", name).
			Scan(&definition); err != nil {
			t.Fatalf("read healed trigger: %v", err)
		}
		var want string
		for _, spec := range sqliteDirectoryWriterGuardSpecs() {
			if spec.Name == name {
				want = spec.Definition
				break
			}
		}
		if definition != want || want == "" {
			t.Fatalf("healed definition = %q, want %q", definition, want)
		}
	})

	t.Run("staged drift refuses", func(t *testing.T) {
		h := newDirectoryWriterSQLiteHarness(t)
		ctx := context.Background()
		const name = "users_directory_writer_guard_ins"
		directoryWriterTestMustExec(t, h.db, "DROP TRIGGER main."+name)
		directoryWriterTestMustExec(t, h.db, `CREATE TRIGGER main.users_directory_writer_guard_ins
BEFORE INSERT ON users BEGIN SELECT 1; END`)
		err := reconcileDirectoryWriterGuards(ctx, h.db, h.db, h.dia, false, guardRoles{})
		if !errors.Is(err, errDirectoryWriterGuardInvalid) {
			t.Fatalf("reconcile staged drift error = %v, want %v", err, errDirectoryWriterGuardInvalid)
		}
		var definition string
		if err := h.db.QueryRowContext(ctx,
			"SELECT sql FROM main.sqlite_master WHERE type = 'trigger' AND name = ?", name).
			Scan(&definition); err != nil {
			t.Fatalf("read drifted trigger after refusal: %v", err)
		}
		if !strings.Contains(definition, "SELECT 1") {
			t.Fatalf("staged reconcile replaced a same-name drifted trigger: %q", definition)
		}
	})

	t.Run("enforced missing trigger refuses", func(t *testing.T) {
		h := newDirectoryWriterSQLiteHarness(t)
		ctx := context.Background()
		const name = "users_directory_writer_guard_ins"
		directoryWriterTestMustExec(t, h.db,
			"UPDATE main.directory_writer_control SET mode = 'enforced'")
		directoryWriterTestMustExec(t, h.db, "DROP TRIGGER main."+name)
		err := reconcileDirectoryWriterGuards(ctx, h.db, h.db, h.dia, false, guardRoles{})
		if !errors.Is(err, errDirectoryWriterGuardInvalid) {
			t.Fatalf("reconcile enforced missing trigger error = %v, want %v",
				err, errDirectoryWriterGuardInvalid)
		}
		var count int
		if err := h.db.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM main.sqlite_master WHERE type = 'trigger' AND name = ?", name).
			Scan(&count); err != nil {
			t.Fatalf("read missing trigger after refusal: %v", err)
		}
		if count != 0 {
			t.Fatalf("enforced reconcile recreated missing trigger: count=%d", count)
		}
	})
}

func TestDirectoryWriterSQLiteTrackedRawCorruptionRefusesOpen(t *testing.T) {
	tests := []struct {
		name   string
		mutate []string
		probe  func(*testing.T, *sql.DB)
	}{
		{
			name:   "control relation absent",
			mutate: []string{"DROP TABLE main.directory_writer_control"},
			probe: func(t *testing.T, db *sql.DB) {
				directoryWriterTestWantSQLiteScalar(t, db,
					"SELECT COUNT(*) FROM main.sqlite_master WHERE type = 'table' AND name = 'directory_writer_control'", 0)
			},
		},
		{
			name: "control relation replaced",
			mutate: []string{
				"DROP TABLE main.directory_writer_control",
				"CREATE TABLE directory_writer_control(control_key TEXT)",
				"INSERT INTO directory_writer_control(control_key) VALUES ('core.directory.writer')",
			},
			probe: func(t *testing.T, db *sql.DB) {
				directoryWriterTestWantSQLiteScalar(t, db,
					"SELECT COUNT(*) FROM pragma_table_info('directory_writer_control')", 1)
			},
		},
		{
			name:   "control singleton absent",
			mutate: []string{"DELETE FROM main.directory_writer_control"},
			probe: func(t *testing.T, db *sql.DB) {
				directoryWriterTestWantSQLiteScalar(t, db,
					"SELECT COUNT(*) FROM main.directory_writer_control", 0)
			},
		},
		{
			name:   "control generation has non-integer storage",
			mutate: []string{"UPDATE main.directory_writer_control SET expected_generation = 1.5"},
			probe: func(t *testing.T, db *sql.DB) {
				var storage string
				if err := db.QueryRowContext(context.Background(),
					"SELECT typeof(expected_generation) FROM main.directory_writer_control").Scan(&storage); err != nil {
					t.Fatal(err)
				}
				if storage != "real" {
					t.Fatalf("refused Open changed generation storage class to %q, want real", storage)
				}
			},
		},
		{
			name:   "marker relation absent",
			mutate: []string{"DROP TABLE main.directory_writer_marker"},
			probe: func(t *testing.T, db *sql.DB) {
				directoryWriterTestWantSQLiteScalar(t, db,
					"SELECT COUNT(*) FROM main.sqlite_master WHERE type = 'table' AND name = 'directory_writer_marker'", 0)
			},
		},
		{
			name: "marker relation replaced",
			mutate: []string{
				"DROP TABLE main.directory_writer_marker",
				"CREATE TABLE directory_writer_marker(control_key TEXT)",
			},
			probe: func(t *testing.T, db *sql.DB) {
				directoryWriterTestWantSQLiteScalar(t, db,
					"SELECT COUNT(*) FROM pragma_table_info('directory_writer_marker')", 1)
			},
		},
		{
			name: "durable marker row",
			mutate: []string{
				"INSERT INTO main.directory_writer_marker(control_key, generation) VALUES ('core.directory.writer', 1)",
			},
			probe: func(t *testing.T, db *sql.DB) {
				directoryWriterTestWantSQLiteScalar(t, db,
					"SELECT COUNT(*) FROM main.directory_writer_marker", 1)
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			dsn := filepath.Join(t.TempDir(), "tracked-corruption.db")
			cfg := store.Config{Engine: store.EngineSQLite, DSN: dsn}
			st, err := Open(ctx, cfg, nil)
			if err != nil {
				t.Fatalf("seed tracked SQLite store: %v", err)
			}
			if err := st.Close(); err != nil {
				t.Fatalf("close tracked SQLite store: %v", err)
			}

			raw, err := openSQLite(dsn)
			if err != nil {
				t.Fatalf("open raw corruption handle: %v", err)
			}
			directoryWriterTestWantSQLiteScalar(t, raw,
				"SELECT COUNT(*) FROM "+coreTrackingTable+" WHERE version = 7 AND reverted_at IS NULL", 1)
			for _, stmt := range tc.mutate {
				directoryWriterTestMustExec(t, raw, stmt)
			}
			if err := raw.Close(); err != nil {
				t.Fatalf("close raw corruption handle: %v", err)
			}

			opened, openErr := Open(ctx, cfg, nil)
			if openErr == nil {
				_ = opened.Close()
				t.Fatal("Open accepted corruption after v7 was tracked")
			}
			if !errors.Is(openErr, errDirectoryWriterControlInvalid) {
				t.Fatalf("Open error = %v, want %v", openErr, errDirectoryWriterControlInvalid)
			}

			post, err := openSQLite(dsn)
			if err != nil {
				t.Fatalf("open post-refusal inspection handle: %v", err)
			}
			defer post.Close() //nolint:errcheck
			directoryWriterTestWantSQLiteScalar(t, post,
				"SELECT COUNT(*) FROM "+coreTrackingTable+" WHERE version = 7 AND reverted_at IS NULL", 1)
			tc.probe(t, post)
		})
	}
}

func TestDirectoryWriterSQLiteTempShadowRefusesRawVerifier(t *testing.T) {
	h := newDirectoryWriterSQLiteHarness(t)
	ctx := context.Background()
	directoryWriterTestMustExec(t, h.db,
		"CREATE TEMP TABLE directory_writer_control(control_key TEXT)")
	err := verifyDirectoryWriterControlPerBoot(ctx, h.db, h.dia)
	if !errors.Is(err, errDirectoryWriterControlInvalid) {
		t.Fatalf("raw verifier with temp shadow error = %v, want %v",
			err, errDirectoryWriterControlInvalid)
	}
	var mainRows int
	if err := h.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM main.directory_writer_control").Scan(&mainRows); err != nil {
		t.Fatal(err)
	}
	if mainRows != 1 {
		t.Fatalf("temp-shadow refusal changed main control rows: %d", mainRows)
	}
}

func TestDirectoryWriterPostgresHostileSearchPathAndGeneration(t *testing.T) {
	dsns := isolatedPG(t)
	ctx := context.Background()

	seed, err := sql.Open("pgx", dsns.App)
	if err != nil {
		t.Fatalf("open PostgreSQL seed handle: %v", err)
	}
	var database string
	if err := seed.QueryRowContext(ctx, "SELECT pg_catalog.current_database()").Scan(&database); err != nil {
		_ = seed.Close()
		t.Fatalf("read isolated database name: %v", err)
	}
	for _, stmt := range []string{
		"CREATE SCHEMA writer_hostile",
		`CREATE FUNCTION writer_hostile.always_equal(pg_catalog.text, pg_catalog.text)
RETURNS pg_catalog.bool LANGUAGE sql IMMUTABLE AS $fn$ SELECT true $fn$`,
		`CREATE OPERATOR writer_hostile.= (
LEFTARG = pg_catalog.text, RIGHTARG = pg_catalog.text,
FUNCTION = writer_hostile.always_equal
)`,
		"ALTER DATABASE " + quoteIdent(database) +
			" SET search_path = writer_hostile, pg_catalog, public",
	} {
		if _, err := seed.ExecContext(ctx, stmt); err != nil {
			_ = seed.Close()
			t.Fatalf("seed hostile search_path (%s): %v", stmt, err)
		}
	}
	if err := seed.Close(); err != nil {
		t.Fatalf("close PostgreSQL seed handle: %v", err)
	}

	plain, err := sql.Open("pgx", dsns.App)
	if err != nil {
		t.Fatalf("open hostile-path precondition handle: %v", err)
	}
	var path string
	var hijacked bool
	if err := plain.QueryRowContext(ctx,
		"SELECT pg_catalog.current_setting('search_path'), "+
			"'left'::pg_catalog.text = 'right'::pg_catalog.text").Scan(&path, &hijacked); err != nil {
		_ = plain.Close()
		t.Fatalf("measure hostile path precondition: %v", err)
	}
	if !strings.HasPrefix(path, "writer_hostile") || !hijacked {
		_ = plain.Close()
		t.Fatalf("hostile path precondition = path:%q equality:%t", path, hijacked)
	}
	if err := plain.Close(); err != nil {
		t.Fatalf("close hostile-path precondition handle: %v", err)
	}

	st, err := Open(ctx, store.Config{
		Engine: store.EnginePostgres, DSN: dsns.App, MaxConns: 4,
	}, nil)
	if err != nil {
		t.Fatalf("Open under hostile database search_path: %v", err)
	}
	tenant := provisionTenant(t, st, "writer-hostile")
	if err := st.Close(); err != nil {
		t.Fatalf("close PostgreSQL store: %v", err)
	}

	raw, err := sql.Open("pgx", dsns.App)
	if err != nil {
		t.Fatalf("open PostgreSQL writer probe: %v", err)
	}
	defer raw.Close() //nolint:errcheck
	var exactConfig bool
	if err := raw.QueryRowContext(ctx, `SELECT
p.proconfig IS NOT DISTINCT FROM ARRAY['search_path=pg_catalog']::pg_catalog.text[]
FROM pg_catalog.pg_proc p
JOIN pg_catalog.pg_namespace n ON n.oid = p.pronamespace
WHERE n.nspname = 'public' AND p.proname = $1 AND p.pronargs = 0`,
		dialect.DirectoryWriterGuardFunction).Scan(&exactConfig); err != nil {
		t.Fatalf("read writer function proconfig: %v", err)
	}
	if !exactConfig {
		t.Fatal("writer function proconfig is not exactly search_path=pg_catalog")
	}
	var alwaysTriggers int
	if err := raw.QueryRowContext(ctx, `SELECT pg_catalog.count(*)
FROM pg_catalog.pg_trigger t
JOIN pg_catalog.pg_class c ON c.oid = t.tgrelid
JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
WHERE n.nspname = 'public' AND t.tgname LIKE '%\_directory\_writer\_guard' ESCAPE '\'
  AND NOT t.tgisinternal AND t.tgenabled = 'A'`).Scan(&alwaysTriggers); err != nil {
		t.Fatalf("count ALWAYS writer triggers: %v", err)
	}
	if alwaysTriggers != len(directoryWriterSourceTables) {
		t.Fatalf("ALWAYS writer triggers = %d, want %d", alwaysTriggers, len(directoryWriterSourceTables))
	}

	directoryWriterTestMustExec(t, raw, `UPDATE public.directory_writer_control
SET mode = 'enforced', expected_generation = 1`)
	for _, tc := range []struct {
		name       string
		generation string
	}{
		{name: "missing"},
		{name: "wrong", generation: "2"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tx, err := raw.BeginTx(ctx, nil)
			if err != nil {
				t.Fatalf("begin refused generation transaction: %v", err)
			}
			defer tx.Rollback() //nolint:errcheck
			dia, ok := dialect.New(store.EnginePostgres)
			if !ok {
				t.Fatal("no PostgreSQL dialect")
			}
			if err := dia.BindTenant(ctx, tx, tenant); err != nil {
				t.Fatalf("bind tenant: %v", err)
			}
			if tc.generation != "" {
				directoryWriterTestMustExec(t, tx,
					"SELECT pg_catalog.set_config('app.directory_writer_generation', $1, true)",
					tc.generation)
			}
			_, err = tx.ExecContext(ctx, `UPDATE public.orgs SET status = 'suspended'
WHERE tenant_id OPERATOR(pg_catalog.=) $1`, tenant.String())
			directoryWriterTestWantError(t, err, "directory writer generation required")
		})
	}

	tx, err := raw.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin exact generation transaction: %v", err)
	}
	defer tx.Rollback() //nolint:errcheck
	dia, ok := dialect.New(store.EnginePostgres)
	if !ok {
		t.Fatal("no PostgreSQL dialect")
	}
	if err := dia.BindTenant(ctx, tx, tenant); err != nil {
		t.Fatalf("bind tenant for exact generation: %v", err)
	}
	directoryWriterTestMustExec(t, tx,
		"SELECT pg_catalog.set_config('app.directory_writer_generation', '1', true)")
	result, err := tx.ExecContext(ctx, `UPDATE public.orgs SET status = 'suspended'
WHERE tenant_id OPERATOR(pg_catalog.=) $1`, tenant.String())
	if err != nil {
		t.Fatalf("exact generation update: %v", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		t.Fatalf("exact generation rows affected: %v", err)
	}
	if rows != 1 {
		t.Fatalf("exact generation rows affected = %d, want 1", rows)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit exact generation transaction: %v", err)
	}
}

func TestDirectoryWriterPostgresGuardReconciliationIsOneWay(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name        string
		enforce     bool
		replacement bool
		wantOpen    bool
	}{
		{name: "staged missing trigger heals", wantOpen: true},
		{name: "staged drift refuses", replacement: true},
		{name: "enforced missing trigger refuses", enforce: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			pg := isolatedPG(t)
			cfg := store.Config{Engine: store.EnginePostgres, DSN: pg.App, MaxConns: 4}
			st, err := Open(ctx, cfg, nil)
			if err != nil {
				t.Fatalf("seed PostgreSQL writer guards: %v", err)
			}
			if err := st.Close(); err != nil {
				t.Fatal(err)
			}
			raw, err := sql.Open("pgx", pg.Owner)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = raw.Close() })
			if tc.enforce {
				directoryWriterTestMustExec(t, raw,
					"UPDATE public.directory_writer_control SET mode='enforced'")
			}
			directoryWriterTestMustExec(t, raw,
				"DROP TRIGGER users_directory_writer_guard ON public.users")
			if tc.replacement {
				directoryWriterTestMustExec(t, raw, `CREATE TRIGGER users_directory_writer_guard
BEFORE INSERT ON public.users
FOR EACH ROW EXECUTE FUNCTION public.olivares_directory_writer_guard()`)
				directoryWriterTestMustExec(t, raw,
					"ALTER TABLE ONLY public.users ENABLE ALWAYS TRIGGER users_directory_writer_guard")
			}

			reopened, openErr := Open(ctx, cfg, nil)
			if tc.wantOpen {
				if openErr != nil {
					t.Fatalf("staged Open did not heal one missing guard: %v", openErr)
				}
				if err := reopened.Close(); err != nil {
					t.Fatal(err)
				}
			} else {
				if openErr == nil {
					_ = reopened.Close()
					t.Fatal("Open accepted a writer guard state that must refuse")
				}
				if !errors.Is(openErr, errDirectoryWriterGuardInvalid) {
					t.Fatalf("Open error = %v, want %v", openErr, errDirectoryWriterGuardInvalid)
				}
			}

			var count int
			var definition string
			err = raw.QueryRowContext(ctx, `SELECT pg_catalog.count(*),
       COALESCE(pg_catalog.max(pg_catalog.pg_get_triggerdef(t.oid, false)), '')
FROM pg_catalog.pg_trigger t
JOIN pg_catalog.pg_class c ON c.oid=t.tgrelid
JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace
WHERE n.nspname='public' AND c.relname='users'
  AND t.tgname='users_directory_writer_guard' AND NOT t.tgisinternal`).
				Scan(&count, &definition)
			if err != nil {
				t.Fatal(err)
			}
			switch {
			case tc.wantOpen && count != 1:
				t.Fatalf("staged heal left trigger count %d, want 1", count)
			case tc.replacement && (count != 1 || !strings.Contains(definition, "INSERT ON")):
				t.Fatalf("refused staged reconcile replaced the drift: count=%d definition=%q", count, definition)
			case tc.enforce && count != 0:
				t.Fatalf("enforced reconcile recreated missing trigger: count=%d", count)
			}
		})
	}
}

func TestDirectoryWriterPostgresTrackedRawCorruptionRefusesOpen(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name   string
		mutate []string
		probe  string
	}{
		{
			name:   "control absent",
			mutate: []string{"DROP TABLE public.directory_writer_control"},
			probe:  "control-absent",
		},
		{
			name: "control replaced",
			mutate: []string{
				"DROP TABLE public.directory_writer_control",
				"CREATE TABLE public.directory_writer_control(control_key pg_catalog.text)",
			},
			probe: "control-replaced",
		},
		{
			name:   "singleton absent",
			mutate: []string{"DELETE FROM public.directory_writer_control"},
			probe:  "singleton-absent",
		},
		{
			name:   "SQLite marker planted",
			mutate: []string{"CREATE TABLE public.directory_writer_marker(control_key pg_catalog.text)"},
			probe:  "marker-present",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			pg := isolatedPG(t)
			cfg := store.Config{Engine: store.EnginePostgres, DSN: pg.App, MaxConns: 4}
			st, err := Open(ctx, cfg, nil)
			if err != nil {
				t.Fatalf("seed tracked PostgreSQL store: %v", err)
			}
			if err := st.Close(); err != nil {
				t.Fatal(err)
			}
			raw, err := sql.Open("pgx", pg.Owner)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = raw.Close() })
			for _, stmt := range tc.mutate {
				directoryWriterTestMustExec(t, raw, stmt)
			}
			opened, openErr := Open(ctx, cfg, nil)
			if openErr == nil {
				_ = opened.Close()
				t.Fatal("Open accepted corrupt raw writer state after v7")
			}
			if !errors.Is(openErr, errDirectoryWriterControlInvalid) {
				t.Fatalf("Open error = %v, want %v", openErr, errDirectoryWriterControlInvalid)
			}
			var tracked int
			if err := raw.QueryRowContext(ctx,
				"SELECT COUNT(*) FROM public."+coreTrackingTable+" WHERE version=7 AND reverted_at IS NULL").
				Scan(&tracked); err != nil {
				t.Fatal(err)
			}
			if tracked != 1 {
				t.Fatalf("refused Open changed v7 tracking: %d", tracked)
			}
			switch tc.probe {
			case "control-absent":
				var exists bool
				if err := raw.QueryRowContext(ctx,
					"SELECT pg_catalog.to_regclass('public.directory_writer_control') IS NOT NULL").Scan(&exists); err != nil {
					t.Fatal(err)
				}
				if exists {
					t.Fatal("refused Open recreated absent writer control")
				}
			case "control-replaced":
				var columns int
				if err := raw.QueryRowContext(ctx, `SELECT pg_catalog.count(*)
FROM pg_catalog.pg_attribute a
JOIN pg_catalog.pg_class c ON c.oid=a.attrelid
JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace
WHERE n.nspname='public' AND c.relname='directory_writer_control'
  AND a.attnum>0 AND NOT a.attisdropped`).Scan(&columns); err != nil {
					t.Fatal(err)
				}
				if columns != 1 {
					t.Fatalf("refused Open changed replacement control columns: %d", columns)
				}
			case "singleton-absent":
				var rows int
				if err := raw.QueryRowContext(ctx,
					"SELECT COUNT(*) FROM public.directory_writer_control").Scan(&rows); err != nil {
					t.Fatal(err)
				}
				if rows != 0 {
					t.Fatalf("refused Open reseeded singleton: %d", rows)
				}
			case "marker-present":
				var exists bool
				if err := raw.QueryRowContext(ctx,
					"SELECT pg_catalog.to_regclass('public.directory_writer_marker') IS NOT NULL").Scan(&exists); err != nil {
					t.Fatal(err)
				}
				if !exists {
					t.Fatal("refused Open removed planted PostgreSQL marker")
				}
			}
		})
	}
}

func TestDirectoryWriterPostgresSplitOwnerACL(t *testing.T) {
	pg := isolatedPGSplit(t)
	ctx := context.Background()
	cfg := store.Config{
		Engine: store.EnginePostgres, DSN: pg.App, OwnerDSN: pg.Owner, MaxConns: 4,
	}
	st, err := Open(ctx, cfg, nil)
	if err != nil {
		t.Fatalf("open split-owner writer fixture: %v", err)
	}
	tenant := provisionTenant(t, st, "writer-split")
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	if pg.Result.AppPosture == nil || pg.Result.AppPosture.Role == "" {
		t.Fatalf("split fixture has no verified application role: %+v", pg.Result.AppPosture)
	}
	app, err := sql.Open("pgx", pg.App)
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close() //nolint:errcheck
	if err := verifyPostgresDirectoryWriterACLForRole(ctx, app, pg.Result.AppPosture.Role); err != nil {
		t.Fatalf("split writer ACL: %v", err)
	}
	if err := verifyPostgresDirectoryWriterRoleClosure(ctx, app, pg.Result.AppPosture.Role); err != nil {
		t.Fatalf("split writer role closure: %v", err)
	}

	owner, err := sql.Open("pgx", pg.Owner)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close() //nolint:errcheck
	directoryWriterTestMustExec(t, owner,
		"UPDATE public.directory_writer_control SET mode='enforced', expected_generation=1")
	tx, err := app.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback() //nolint:errcheck
	dia, ok := dialect.New(store.EnginePostgres)
	if !ok {
		t.Fatal("no PostgreSQL dialect")
	}
	if err := dia.BindTenant(ctx, tx, tenant); err != nil {
		t.Fatal(err)
	}
	_, err = tx.ExecContext(ctx, `UPDATE public.orgs SET status='suspended'
WHERE tenant_id OPERATOR(pg_catalog.=) $1`, tenant.String())
	directoryWriterTestWantError(t, err, "directory writer generation required")
	_ = tx.Rollback()

	tx, err = app.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback() //nolint:errcheck
	if err := dia.BindTenant(ctx, tx, tenant); err != nil {
		t.Fatal(err)
	}
	directoryWriterTestMustExec(t, tx,
		"SELECT pg_catalog.set_config('app.directory_writer_generation', '1', true)")
	if _, err := tx.ExecContext(ctx, `UPDATE public.orgs SET status='suspended'
WHERE tenant_id OPERATOR(pg_catalog.=) $1`, tenant.String()); err != nil {
		t.Fatalf("installed trigger did not fire successfully after app EXECUTE was revoked: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func TestDirectoryWriterPostgresSplitOwnerRefusesInheritedSourceAdministration(t *testing.T) {
	pg := isolatedPGSplit(t)
	ctx := context.Background()
	cfg := store.Config{
		Engine: store.EnginePostgres, DSN: pg.App, OwnerDSN: pg.Owner, MaxConns: 4,
	}
	st, err := Open(ctx, cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	if pg.Result.AppPosture == nil || pg.Result.AppPosture.Role == "" {
		t.Fatalf("split fixture has no verified application role: %+v", pg.Result.AppPosture)
	}
	appRole := pg.Result.AppPosture.Role
	group := "olv_dw_" + pgtest.Suffix(t)
	if !plainRoleIdent(appRole) || !plainRoleIdent(group) {
		t.Fatalf("unsafe fixture roles app=%q group=%q", appRole, group)
	}
	super, err := sql.Open("pgx", pg.Superuser)
	if err != nil {
		t.Fatal(err)
	}
	directoryWriterTestMustExec(t, super, "CREATE ROLE "+quoteIdent(group)+" NOLOGIN")
	t.Cleanup(func() {
		_, _ = super.ExecContext(context.Background(),
			"REVOKE "+quoteIdent(group)+" FROM "+quoteIdent(appRole))
		_, _ = super.ExecContext(context.Background(),
			"DROP OWNED BY "+quoteIdent(group))
		_, _ = super.ExecContext(context.Background(),
			"DROP ROLE "+quoteIdent(group))
		_ = super.Close()
	})
	directoryWriterTestMustExec(t, super,
		"GRANT TRUNCATE ON TABLE public.users TO "+quoteIdent(group))
	directoryWriterTestMustExec(t, super,
		"GRANT "+quoteIdent(group)+" TO "+quoteIdent(appRole))
	app, err := sql.Open("pgx", pg.App)
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close() //nolint:errcheck
	var canTruncate bool
	if err := app.QueryRowContext(ctx,
		"SELECT pg_catalog.has_table_privilege('public.users', 'TRUNCATE')").Scan(&canTruncate); err != nil {
		t.Fatal(err)
	}
	if !canTruncate {
		t.Fatal("fixture group did not convey TRUNCATE to the application role")
	}
	opened, openErr := Open(ctx, cfg, nil)
	if openErr == nil {
		_ = opened.Close()
		t.Fatal("Open accepted an assumable/inherited role with TRUNCATE on a writer source")
	}
	if !strings.Contains(openErr.Error(), "directory writer split-owner") &&
		!strings.Contains(openErr.Error(), "directory writer role") {
		t.Fatalf("Open refused for the wrong layer: %v", openErr)
	}
	if err := app.QueryRowContext(ctx,
		"SELECT pg_catalog.has_table_privilege('public.users', 'TRUNCATE')").Scan(&canTruncate); err != nil {
		t.Fatal(err)
	}
	if !canTruncate {
		t.Fatal("refused Open silently rewrote an inherited/group grant instead of denying it")
	}
}

func TestDirectoryWriterPostgresFunctionConfigDriftRefuses(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name   string
		tamper string
	}{
		{name: "missing", tamper: "ALTER FUNCTION public.olivares_directory_writer_guard() RESET ALL"},
		{name: "wrong", tamper: "ALTER FUNCTION public.olivares_directory_writer_guard() SET search_path TO public"},
		{name: "extra", tamper: "ALTER FUNCTION public.olivares_directory_writer_guard() SET work_mem TO '64kB'"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			pg := isolatedPG(t)
			cfg := store.Config{Engine: store.EnginePostgres, DSN: pg.App, MaxConns: 4}
			st, err := Open(ctx, cfg, nil)
			if err != nil {
				t.Fatal(err)
			}
			if err := st.Close(); err != nil {
				t.Fatal(err)
			}
			raw, err := sql.Open("pgx", pg.Owner)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = raw.Close() })
			directoryWriterTestMustExec(t, raw, tc.tamper)
			var before sql.NullString
			if err := raw.QueryRowContext(ctx, `SELECT p.proconfig::pg_catalog.text
FROM pg_catalog.pg_proc p
JOIN pg_catalog.pg_namespace n ON n.oid=p.pronamespace
WHERE n.nspname='public' AND p.proname='olivares_directory_writer_guard'
  AND p.pronargs=0`).Scan(&before); err != nil {
				t.Fatal(err)
			}
			opened, openErr := Open(ctx, cfg, nil)
			if openErr == nil {
				_ = opened.Close()
				t.Fatal("Open accepted writer function proconfig drift")
			}
			if !errors.Is(openErr, errDirectoryWriterGuardInvalid) {
				t.Fatalf("Open error = %v, want %v", openErr, errDirectoryWriterGuardInvalid)
			}
			var after sql.NullString
			if err := raw.QueryRowContext(ctx, `SELECT p.proconfig::pg_catalog.text
FROM pg_catalog.pg_proc p
JOIN pg_catalog.pg_namespace n ON n.oid=p.pronamespace
WHERE n.nspname='public' AND p.proname='olivares_directory_writer_guard'
  AND p.pronargs=0`).Scan(&after); err != nil {
				t.Fatal(err)
			}
			if before != after {
				t.Fatalf("refused Open repaired proconfig: before=%v after=%v", before, after)
			}
		})
	}
}

func TestDirectoryWriterPostgresClosedInventoryRefusesExtraAttachment(t *testing.T) {
	pg := isolatedPG(t)
	ctx := context.Background()
	cfg := store.Config{Engine: store.EnginePostgres, DSN: pg.App, MaxConns: 4}
	st, err := Open(ctx, cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := sql.Open("pgx", pg.Owner)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close() //nolint:errcheck
	for _, stmt := range []string{
		"CREATE TABLE public.writer_extra(id pg_catalog.text PRIMARY KEY)",
		`CREATE TRIGGER writer_extra_directory_writer_guard
BEFORE INSERT OR UPDATE OR DELETE ON public.writer_extra
FOR EACH ROW EXECUTE FUNCTION public.olivares_directory_writer_guard()`,
		"ALTER TABLE ONLY public.writer_extra ENABLE ALWAYS TRIGGER writer_extra_directory_writer_guard",
	} {
		directoryWriterTestMustExec(t, raw, stmt)
	}
	_, err = raw.ExecContext(ctx, "INSERT INTO public.writer_extra(id) VALUES ('x')")
	directoryWriterTestWantError(t, err, "directory writer trigger target invalid")
	opened, openErr := Open(ctx, cfg, nil)
	if openErr == nil {
		_ = opened.Close()
		t.Fatal("Open accepted the reserved writer function attached outside the closed source inventory")
	}
	if !errors.Is(openErr, errDirectoryWriterGuardInvalid) ||
		!strings.Contains(openErr.Error(), "unexpected PostgreSQL writer attachments") {
		t.Fatalf("Open error = %v, want closed-inventory refusal", openErr)
	}
}

type directoryWriterSQLiteHarness struct {
	db  *sql.DB
	dia dialect.Dialect
}

func newDirectoryWriterSQLiteHarness(t *testing.T) directoryWriterSQLiteHarness {
	t.Helper()
	ctx := context.Background()
	db, err := openSQLite(filepath.Join(t.TempDir(), "writer-raw.db"))
	if err != nil {
		t.Fatalf("open raw SQLite writer database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	dia, ok := dialect.New(store.EngineSQLite)
	if !ok {
		t.Fatal("no SQLite dialect")
	}
	for _, table := range directoryWriterSourceTables {
		ddl := "CREATE TABLE " + table + " (id TEXT PRIMARY KEY)"
		if table == "orgs" {
			ddl = `CREATE TABLE orgs (
  id TEXT PRIMARY KEY,
  tenant_id TEXT NOT NULL,
  status TEXT NOT NULL,
  note TEXT NOT NULL
)`
		}
		directoryWriterTestMustExec(t, db, ddl)
	}
	for _, stmt := range dia.DirectoryWriterControlStmts() {
		directoryWriterTestMustExec(t, db, stmt)
	}
	if err := reconcileDirectoryWriterGuards(ctx, db, db, dia, false, guardRoles{}); err != nil {
		t.Fatalf("install raw SQLite writer guards: %v", err)
	}
	return directoryWriterSQLiteHarness{db: db, dia: dia}
}

func directoryWriterTestExerciseSQLiteSource(
	t *testing.T,
	exec directoryWriterTestExecer,
	table string,
	id string,
) {
	t.Helper()
	if table == "orgs" {
		directoryWriterTestMustExec(t, exec,
			"INSERT INTO orgs(id, tenant_id, status, note) VALUES (?, ?, 'active', '')",
			id, "33333333-3333-3333-3333-333333333333")
		directoryWriterTestMustExec(t, exec, "UPDATE orgs SET note = 'updated' WHERE id = ?", id)
		directoryWriterTestMustExec(t, exec, "DELETE FROM orgs WHERE id = ?", id)
		return
	}
	directoryWriterTestMustExec(t, exec, "INSERT INTO "+table+"(id) VALUES (?)", id)
	directoryWriterTestMustExec(t, exec, "UPDATE "+table+" SET id = id WHERE id = ?", id)
	directoryWriterTestMustExec(t, exec, "DELETE FROM "+table+" WHERE id = ?", id)
}

func directoryWriterTestMustExec(
	t *testing.T,
	exec directoryWriterTestExecer,
	query string,
	args ...any,
) {
	t.Helper()
	if _, err := exec.ExecContext(context.Background(), query, args...); err != nil {
		t.Fatalf("exec %q: %v", query, err)
	}
}

func directoryWriterTestWantError(t *testing.T, err error, contains string) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), contains) {
		t.Fatalf("error = %v, want text %q", err, contains)
	}
}

func directoryWriterTestWantSQLiteScalar(t *testing.T, db *sql.DB, query string, want int) {
	t.Helper()
	var got int
	if err := db.QueryRowContext(context.Background(), query).Scan(&got); err != nil {
		t.Fatalf("query %q: %v", query, err)
	}
	if got != want {
		t.Fatalf("query %q = %d, want %d", query, got, want)
	}
}
