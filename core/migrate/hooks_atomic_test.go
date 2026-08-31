// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package migrate

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/internal/store/dialect"
	"github.com/olivaresai/olivares/core/store"
)

func TestMigrationCallbacksRunInTheDeclaredTransactionOrder(t *testing.T) {
	ctx := context.Background()
	db, dia := openMem(t)
	const tracking = "schema_migrations_hook_order"

	if _, err := db.ExecContext(ctx, `CREATE TABLE hook_order_probe (
		ordinal INTEGER PRIMARY KEY AUTOINCREMENT,
		step TEXT NOT NULL UNIQUE
	)`); err != nil {
		t.Fatal(err)
	}
	if err := Apply(ctx, db, dia, tracking, []Migration{{
		Version: 1,
		Name:    "ordered_callbacks",
		Before: func(ctx context.Context, tx *sql.Tx) error {
			if hasMigrationRow(ctx, tx, tracking, 1) {
				return errors.New("tracking row existed in Before")
			}
			_, err := tx.ExecContext(ctx, "INSERT INTO hook_order_probe(step) VALUES ('before')")
			return err
		},
		Stmts: []string{"INSERT INTO hook_order_probe(step) VALUES ('statement')"},
		Exec: func(ctx context.Context, tx *sql.Tx) error {
			if got := hookSteps(t, ctx, tx); strings.Join(got, ",") != "before,statement" {
				return errors.New("Exec did not observe Before then Stmts")
			}
			if hasMigrationRow(ctx, tx, tracking, 1) {
				return errors.New("tracking row existed in Exec")
			}
			_, err := tx.ExecContext(ctx, "INSERT INTO hook_order_probe(step) VALUES ('exec')")
			return err
		},
		After: func(ctx context.Context, tx *sql.Tx) error {
			if got := hookSteps(t, ctx, tx); strings.Join(got, ",") != "before,statement,exec" {
				return errors.New("After did not observe Before, Stmts and Exec")
			}
			if hasMigrationRow(ctx, tx, tracking, 1) {
				return errors.New("tracking row existed in After")
			}
			_, err := tx.ExecContext(ctx, "INSERT INTO hook_order_probe(step) VALUES ('after')")
			return err
		},
	}}); err != nil {
		t.Fatalf("apply: %v", err)
	}

	if got := countRows(t, db, "hook_order_probe"); got != 4 {
		t.Fatalf("callback/statement rows = %d, want 4", got)
	}
	if got := countRows(t, db, tracking); got != 1 {
		t.Fatalf("tracking rows = %d, want 1", got)
	}
	if got := hookSteps(t, ctx, db); strings.Join(got, ",") != "before,statement,exec,after" {
		t.Fatalf("committed callback order = %v, want Before, Stmts, Exec, After", got)
	}
}

func TestMigrationHookFailuresRollbackEverythingAndLeaveNoLedger(t *testing.T) {
	for _, tc := range []struct {
		name string
		mig  func(error) Migration
	}{
		{
			name: "before",
			mig: func(boom error) Migration {
				return Migration{
					Version: 1, Name: "before_fails",
					Before: func(ctx context.Context, tx *sql.Tx) error {
						if _, err := tx.ExecContext(ctx, "INSERT INTO hook_rollback_probe(step) VALUES ('before')"); err != nil {
							return err
						}
						return boom
					},
					Stmts: []string{"INSERT INTO hook_rollback_probe(step) VALUES ('statement')"},
				}
			},
		},
		{
			name: "after",
			mig: func(boom error) Migration {
				return Migration{
					Version: 1, Name: "after_fails",
					Before: func(ctx context.Context, tx *sql.Tx) error {
						_, err := tx.ExecContext(ctx, "INSERT INTO hook_rollback_probe(step) VALUES ('before')")
						return err
					},
					Stmts: []string{"INSERT INTO hook_rollback_probe(step) VALUES ('statement')"},
					Exec: func(ctx context.Context, tx *sql.Tx) error {
						_, err := tx.ExecContext(ctx, "INSERT INTO hook_rollback_probe(step) VALUES ('exec')")
						return err
					},
					After: func(ctx context.Context, tx *sql.Tx) error {
						if _, err := tx.ExecContext(ctx, "INSERT INTO hook_rollback_probe(step) VALUES ('after')"); err != nil {
							return err
						}
						return boom
					},
				}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			db, dia := openMem(t)
			const tracking = "schema_migrations_hook_rollback"
			if _, err := db.ExecContext(ctx, "CREATE TABLE hook_rollback_probe (step TEXT PRIMARY KEY)"); err != nil {
				t.Fatal(err)
			}
			boom := errors.New(tc.name + " refused")
			err := Apply(ctx, db, dia, tracking, []Migration{tc.mig(boom)})
			if !errors.Is(err, boom) {
				t.Fatalf("Apply error = %v, want wrapped %v", err, boom)
			}
			if got := countRows(t, db, "hook_rollback_probe"); got != 0 {
				t.Fatalf("callback/statement rows survived = %d, want 0", got)
			}
			if got := countRows(t, db, tracking); got != 0 {
				t.Fatalf("tracking rows after refusal = %d, want 0", got)
			}
		})
	}
}

func TestMigrationTrackingUsesTheDurableSchemaWhenATempTableShadowsIt(t *testing.T) {
	ctx := context.Background()
	db, dia := openMem(t)
	const (
		tracking = "schema_migrations_temp_shadow"
		probe    = "tracking_shadow_probe"
	)
	m := Migration{
		Version: 1,
		Name:    "temp_tracking_shadow",
		Stmts: []string{
			"CREATE TEMP TABLE " + tracking +
				" (version INTEGER PRIMARY KEY, name TEXT NOT NULL, applied_at TEXT NOT NULL, phase TEXT NOT NULL, reverted_at TEXT)",
			"CREATE TABLE " + probe + " (id INTEGER PRIMARY KEY)",
		},
		DownStmts: []string{"DROP TABLE " + probe},
	}
	if err := Apply(ctx, db, dia, tracking, []Migration{m}); err != nil {
		t.Fatalf("apply with temporary tracking shadow: %v", err)
	}
	if got := countRows(t, db, `main."`+tracking+`"`); got != 1 {
		t.Fatalf("durable tracking rows = %d, want 1", got)
	}
	if got := countRows(t, db, `temp."`+tracking+`"`); got != 0 {
		t.Fatalf("temporary shadow rows = %d, want 0", got)
	}
	if err := Apply(ctx, db, dia, tracking, []Migration{m}); err != nil {
		t.Fatalf("idempotent apply read the temporary tracking shadow: %v", err)
	}
	if err := Revert(ctx, db, dia, tracking, m); err != nil {
		t.Fatalf("revert with temporary tracking shadow: %v", err)
	}
	var reverted bool
	if err := db.QueryRowContext(ctx,
		`SELECT reverted_at IS NOT NULL FROM main."`+tracking+`" WHERE version = 1`,
	).Scan(&reverted); err != nil {
		t.Fatal(err)
	}
	if !reverted {
		t.Fatal("Revert updated the temporary shadow instead of the durable ledger")
	}
	if got := countRows(t, db, `temp."`+tracking+`"`); got != 0 {
		t.Fatalf("temporary shadow rows after Revert = %d, want 0", got)
	}
	if hasTable(t, db, probe) {
		t.Fatal("Revert did not run its down statement")
	}
}

func TestTransactionalCallbacksAreAllRefusedBeforeNonTransactionalStatements(t *testing.T) {
	for _, tc := range []struct {
		name   string
		attach func(*Migration, func(context.Context, *sql.Tx) error)
	}{
		{"before", func(m *Migration, hook func(context.Context, *sql.Tx) error) { m.Before = hook }},
		{"exec", func(m *Migration, hook func(context.Context, *sql.Tx) error) { m.Exec = hook }},
		{"after", func(m *Migration, hook func(context.Context, *sql.Tx) error) { m.After = hook }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			db, dia := openMem(t)
			const tracking = "schema_migrations_hook_nontx"
			called := false
			mig := Migration{
				Version: 1, Name: tc.name, NonTransactional: true,
				Stmts: []string{"CREATE TABLE hook_nontx_probe (id INTEGER PRIMARY KEY)"},
			}
			tc.attach(&mig, func(context.Context, *sql.Tx) error {
				called = true
				return nil
			})
			err := Apply(ctx, db, dia, tracking, []Migration{mig})
			if err == nil || !strings.Contains(err.Error(), "non-transactional") {
				t.Fatalf("refusal = %v, want non-transactional callback refusal", err)
			}
			if called {
				t.Fatal("transactional callback ran in non-transactional mode")
			}
			if hasTable(t, db, "hook_nontx_probe") {
				t.Fatal("statement ran before the non-transactional callback refusal")
			}
			if hasTable(t, db, tracking) {
				t.Fatal("tracking reconciliation ran before the non-transactional callback refusal")
			}
		})
	}
}

func TestCallbackMigrationsRejectTransactionControlBeforeAnyWrite(t *testing.T) {
	for _, tc := range []struct {
		name string
		sql  string
		want string
	}{
		{
			name: "commit after successful DDL",
			sql:  "CREATE TABLE transaction_control_probe (id INTEGER PRIMARY KEY); COMMIT",
			want: "COMMIT",
		},
		{
			name: "rollback behind comments",
			sql: "SELECT 1; -- the boundary follows\n" +
				"/* ordinary block comment */ ROLLBACK",
			want: "ROLLBACK",
		},
		{
			name: "SQLite comment ends at its first terminator",
			sql: "CREATE TABLE transaction_control_probe (id INTEGER PRIMARY KEY); " +
				"/* outer /* inner */ COMMIT; */ SELECT 2",
			want: "COMMIT",
		},
		{name: "savepoint", sql: "SAVEPOINT migration_escape", want: "SAVEPOINT"},
		{name: "release", sql: "RELEASE SAVEPOINT migration_escape", want: "RELEASE"},
		{name: "begin", sql: "BEGIN TRANSACTION", want: "BEGIN"},
		{name: "start", sql: "START TRANSACTION", want: "START"},
		{name: "end alias", sql: "END TRANSACTION", want: "END"},
		{name: "abort alias", sql: "ABORT", want: "ABORT"},
		{name: "prepare transaction", sql: "PREPARE TRANSACTION 'gid'", want: "PREPARE"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			db, dia := openMem(t)
			const tracking = "schema_migrations_transaction_control"
			beforeRan := false
			err := Apply(ctx, db, dia, tracking, []Migration{{
				Version: 1, Name: "transaction_control", Stmts: []string{tc.sql},
				Before: func(context.Context, *sql.Tx) error {
					beforeRan = true
					return nil
				},
			}})
			if err == nil || !strings.Contains(err.Error(), "transaction control "+tc.want) {
				t.Fatalf("Apply error = %v, want preflight refusal of %s", err, tc.want)
			}
			if beforeRan {
				t.Fatal("Before ran before transaction-control preflight refused the plan")
			}
			if hasTable(t, db, tracking) || hasTable(t, db, "transaction_control_probe") {
				t.Fatal("transaction-control refusal happened after a schema or tracking write")
			}
		})
	}

	t.Run("a later invalid plan blocks every earlier migration", func(t *testing.T) {
		ctx := context.Background()
		db, dia := openMem(t)
		const tracking = "schema_migrations_late_transaction_control"
		noop := func(context.Context, *sql.Tx) error { return nil }
		err := Apply(ctx, db, dia, tracking, []Migration{
			{
				Version: 1, Name: "would_write_first",
				Stmts:  []string{"CREATE TABLE transaction_control_first (id INTEGER PRIMARY KEY)"},
				Before: noop,
			},
			{
				Version: 2, Name: "invalid_later", Stmts: []string{"COMMIT"}, Before: noop,
			},
		})
		if err == nil || !strings.Contains(err.Error(), "transaction control COMMIT") {
			t.Fatalf("Apply error = %v, want later-plan transaction-control refusal", err)
		}
		if hasTable(t, db, tracking) || hasTable(t, db, "transaction_control_first") {
			t.Fatal("an earlier migration wrote before the whole callback plan was prevalidated")
		}
	})

	t.Run("an already tracked version cannot hide an invalid callback plan", func(t *testing.T) {
		ctx := context.Background()
		db, dia := openMem(t)
		const tracking = "schema_migrations_tracked_transaction_control"
		if err := Apply(ctx, db, dia, tracking, []Migration{{
			Version: 1, Name: "original", Stmts: []string{"SELECT 1"},
		}}); err != nil {
			t.Fatal(err)
		}
		beforeRan := false
		err := Apply(ctx, db, dia, tracking, []Migration{{
			Version: 1, Name: "mutated", Stmts: []string{"COMMIT"},
			Before: func(context.Context, *sql.Tx) error {
				beforeRan = true
				return nil
			},
		}})
		if err == nil || !strings.Contains(err.Error(), "transaction control COMMIT") {
			t.Fatalf("Apply error = %v, want tracked-plan transaction-control refusal", err)
		}
		if beforeRan || countRows(t, db, tracking) != 1 {
			t.Fatalf("tracked-plan refusal changed state: before_ran=%t rows=%d",
				beforeRan, countRows(t, db, tracking))
		}
	})

	t.Run("PostgreSQL dollar body cannot hide a later commit", func(t *testing.T) {
		pg, ok := dialect.New(store.EnginePostgres)
		if !ok {
			t.Fatal("PostgreSQL dialect unavailable")
		}
		db, _ := openMem(t)
		noop := func(context.Context, *sql.Tx) error { return nil }
		err := Apply(context.Background(), db, pg, "must_not_be_touched", []Migration{{
			Version: 1, Name: "postgres_commit_escape", Before: noop,
			Stmts: []string{`DO $procedure$
BEGIN
  RAISE NOTICE 'COMMIT inside data';
END
$procedure$;
COMMIT`},
		}})
		if err == nil || !strings.Contains(err.Error(), "transaction control COMMIT") {
			t.Fatalf("Apply error = %v, want PostgreSQL COMMIT preflight refusal", err)
		}
	})
}

func TestAllTransactionalMigrationsRejectTransactionControlBeforeWrites(t *testing.T) {
	ctx := context.Background()
	db, dia := openMem(t)
	const tracking = "schema_migrations_plain_transaction_control"
	err := Apply(ctx, db, dia, tracking, []Migration{{
		Version: 1,
		Name:    "plain_transaction_control",
		Stmts: []string{
			"CREATE TABLE plain_transaction_control_probe (id INTEGER PRIMARY KEY); " +
				"COMMIT; SELECT * FROM definitely_missing",
		},
	}})
	if err == nil || !strings.Contains(err.Error(), "transaction control COMMIT") {
		t.Fatalf("Apply error = %v, want non-callback transaction-control refusal", err)
	}
	if hasTable(t, db, tracking) || hasTable(t, db, "plain_transaction_control_probe") {
		t.Fatal("plain transactional migration wrote before transaction-control preflight")
	}
}

func TestRevertRejectsTransactionControlBeforeDownStatements(t *testing.T) {
	ctx := context.Background()
	db, dia := openMem(t)
	const (
		tracking = "schema_migrations_revert_transaction_control"
		probe    = "revert_transaction_control_probe"
	)
	m := Migration{
		Version:   1,
		Name:      "revert_transaction_control",
		Stmts:     []string{"CREATE TABLE " + probe + " (id INTEGER PRIMARY KEY)"},
		DownStmts: []string{"DROP TABLE " + probe},
	}
	if err := Apply(ctx, db, dia, tracking, []Migration{m}); err != nil {
		t.Fatal(err)
	}
	m.DownStmts = []string{
		"DROP TABLE " + probe + "; COMMIT; SELECT * FROM definitely_missing",
	}
	err := Revert(ctx, db, dia, tracking, m)
	if err == nil || !strings.Contains(err.Error(), "transaction control COMMIT") {
		t.Fatalf("Revert error = %v, want transaction-control refusal", err)
	}
	if !hasTable(t, db, probe) {
		t.Fatal("down DDL survived a rejected Revert")
	}
	var reverted bool
	if err := db.QueryRowContext(ctx,
		`SELECT reverted_at IS NOT NULL FROM main."`+tracking+`" WHERE version = 1`,
	).Scan(&reverted); err != nil {
		t.Fatal(err)
	}
	if reverted {
		t.Fatal("rejected Revert marked the durable ledger row reverted")
	}
}

func TestRevertRollsBackDownStatementsWhenVersionWasNeverApplied(t *testing.T) {
	ctx := context.Background()
	db, dia := openMem(t)
	const (
		tracking = "schema_migrations_revert_unapplied"
		probe    = "revert_unapplied_probe"
	)
	if _, err := db.ExecContext(ctx,
		"CREATE TABLE "+probe+" (id INTEGER PRIMARY KEY)",
	); err != nil {
		t.Fatal(err)
	}
	err := Revert(ctx, db, dia, tracking, Migration{
		Version:   99,
		Name:      "never_applied",
		DownStmts: []string{"DROP TABLE " + probe},
	})
	if err == nil || !strings.Contains(err.Error(), "want exactly 1") {
		t.Fatalf("Revert error = %v, want exact tracking-row refusal", err)
	}
	if !hasTable(t, db, probe) {
		t.Fatal("DownStmts became durable without an applied-version ledger row")
	}
	if got := countRows(t, db, `main."`+tracking+`"`); got != 0 {
		t.Fatalf("tracking rows = %d, want 0", got)
	}
}

func TestRevertRollsBackWhenTrackingVersionIsNotUnique(t *testing.T) {
	ctx := context.Background()
	db, dia := openMem(t)
	const (
		tracking = "schema_migrations_revert_duplicate"
		probe    = "revert_duplicate_probe"
	)
	if _, err := db.ExecContext(ctx, `CREATE TABLE main."`+tracking+`" (
		version INTEGER NOT NULL,
		name TEXT NOT NULL,
		applied_at TEXT NOT NULL,
		phase TEXT NOT NULL DEFAULT 'expand',
		reverted_at TEXT
	)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx,
		`INSERT INTO main."`+tracking+`" (version, name, applied_at, phase)
		 VALUES (7, 'duplicate_a', '2026-08-15T00:00:00Z', 'expand'),
		        (7, 'duplicate_b', '2026-08-15T00:00:01Z', 'expand')`,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx,
		"CREATE TABLE "+probe+" (id INTEGER PRIMARY KEY)",
	); err != nil {
		t.Fatal(err)
	}

	err := Revert(ctx, db, dia, tracking, Migration{
		Version:   7,
		Name:      "duplicate_tracking_rows",
		DownStmts: []string{"DROP TABLE " + probe},
	})
	if err == nil || !strings.Contains(err.Error(), "reverted tracking rows = 2, want exactly 1") {
		t.Fatalf("Revert error = %v, want duplicate tracking-row refusal", err)
	}
	if !hasTable(t, db, probe) {
		t.Fatal("DownStmts survived duplicate tracking-row refusal")
	}
	var stamped int
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM main."`+tracking+`" WHERE reverted_at IS NOT NULL`,
	).Scan(&stamped); err != nil {
		t.Fatal(err)
	}
	if stamped != 0 {
		t.Fatalf("stamped duplicate tracking rows = %d, want 0", stamped)
	}
}

func TestCallbackTransactionControlLexerIgnoresDataAndProcedureBodies(t *testing.T) {
	noop := func(context.Context, *sql.Tx) error { return nil }
	for _, tc := range []struct {
		name   string
		engine store.Engine
		sql    string
	}{
		{
			name:   "PostgreSQL comments strings identifiers and dollar body",
			engine: store.EnginePostgres,
			sql: `-- COMMIT is prose
CREATE FUNCTION "COMMIT"() RETURNS void LANGUAGE plpgsql AS $procedure$
BEGIN
  RAISE NOTICE 'ROLLBACK; SAVEPOINT';
END
$procedure$;
/* PostgreSQL keeps /* COMMIT */ this nested text inside the outer comment */
SELECT 'BEGIN; END', E'COMMIT\\ROLLBACK', "transaction_control_column" /* RELEASE */`,
		},
		{
			name:   "SQLite trigger body and rollback conflict action",
			engine: store.EngineSQLite,
			sql: `CREATE TRIGGER guarded BEFORE DELETE ON guarded_table
BEGIN
  SELECT CASE WHEN OLD.id = 1 THEN RAISE(ROLLBACK, 'COMMIT is data') END;
  SELECT 'SAVEPOINT; RELEASE';
END`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := Migration{
				Version: 1, Name: "quoted_control_words", Stmts: []string{tc.sql}, Before: noop,
			}
			if err := validateMigrationPlan(m, tc.engine); err != nil {
				t.Fatalf("quoted/procedural transaction words were treated as transaction control: %v", err)
			}
		})
	}
}

func TestCallbackTransactionControlLexerRejectsAmbiguousFraming(t *testing.T) {
	noop := func(context.Context, *sql.Tx) error { return nil }
	for _, tc := range []struct {
		name string
		sql  string
		want string
	}{
		{
			name: "single quote changes meaning with standard conforming strings",
			sql:  `SELECT 'ambiguous\'; COMMIT`,
			want: "standard_conforming_strings",
		},
		{name: "unterminated dollar body", sql: `DO $body$ BEGIN NULL; END`, want: "unterminated PostgreSQL dollar"},
		{name: "unterminated block comment", sql: `SELECT 1 /*`, want: "unterminated SQL block comment"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := Migration{
				Version: 1, Name: "ambiguous_framing", Stmts: []string{tc.sql}, Before: noop,
			}
			err := validateMigrationPlan(m, store.EnginePostgres)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("validation error = %v, want fail-closed framing error containing %q", err, tc.want)
			}
		})
	}
}

func hasMigrationRow(ctx context.Context, tx *sql.Tx, tracking string, version int) bool {
	var count int
	if err := tx.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM "+tracking+" WHERE version = ?", version).Scan(&count); err != nil {
		return true
	}
	return count != 0
}

type hookStepQuerier interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func hookSteps(t *testing.T, ctx context.Context, q hookStepQuerier) []string {
	t.Helper()
	rows, err := q.QueryContext(ctx, "SELECT step FROM hook_order_probe ORDER BY ordinal")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var steps []string
	for rows.Next() {
		var step string
		if err := rows.Scan(&step); err != nil {
			t.Fatal(err)
		}
		steps = append(steps, step)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return steps
}
