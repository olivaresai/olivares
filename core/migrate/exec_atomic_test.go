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
)

// exec_atomic_test.go pins Migration.Exec's ONE promise: it commits with the statements and
// the tracking row, or none of them commit.
//
// It exists because the promise was made and not measured. A comment in the guard control
// plane's SQLite test claimed the atomicity was covered by a test named
// TestSQLiteGuardBootstrapIsAtomic — and that test did not exist. A clean run cannot tell the
// difference: an implementation that committed the DDL and the tracking row and THEN wrote the
// rows in a second transaction produces exactly the same tables, rows and chains when nothing
// fails. The only way to see the difference is to make something fail.

// hasTable reports whether a table is present in this SQLite database.
//
// Named apart from the package's existing tableExists helper, which takes a different shape.
func hasTable(t *testing.T, db *sql.DB, table string) bool {
	t.Helper()
	var n int
	if err := db.QueryRow(
		"SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?", table).Scan(&n); err != nil {
		t.Fatalf("probe %s: %v", table, err)
	}
	return n > 0
}

// TestExecCommitsWithTheStatementsAndTheTrackingRow is the atomicity regression.
//
// The failure is injected in Exec, AFTER the statements have run inside the transaction. That
// position is the whole test: the statements succeeded, so an implementation that did not share
// their transaction would leave the table behind.
func TestExecCommitsWithTheStatementsAndTheTrackingRow(t *testing.T) {
	ctx := context.Background()
	db, dia := openMem(t)
	const tracking = "schema_migrations_exec"

	boom := errors.New("the bootstrap refused")
	err := Apply(ctx, db, dia, tracking, []Migration{{
		Version: 1,
		Name:    "creates_then_fails",
		Stmts:   []string{"CREATE TABLE exec_atomic_probe (id INTEGER PRIMARY KEY)"},
		Exec: func(ctx context.Context, tx *sql.Tx) error {
			// Prove the transaction is the SAME one: the statements' table must be visible from
			// here. If Exec ran on its own connection this would fail with "no such table",
			// which is a different error and would make the assertion below meaningless.
			var n int
			if qerr := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM exec_atomic_probe").Scan(&n); qerr != nil {
				return errors.New("Exec cannot see the statements' own table, so it is not in their transaction: " + qerr.Error())
			}
			if _, ierr := tx.ExecContext(ctx, "INSERT INTO exec_atomic_probe (id) VALUES (1)"); ierr != nil {
				return ierr
			}
			return boom
		},
	}})
	if err == nil {
		t.Fatal("Apply returned nil for a migration whose Exec failed")
	}
	if !errors.Is(err, boom) {
		t.Errorf("Apply returned %v, which does not wrap the bootstrap's own error", err)
	}

	// NOTHING SURVIVED. All three halves are checked, because each one failing alone would be a
	// different defect: the table is the statements, the row is Exec's own write, and the
	// tracking row is what would make a later boot skip the migration forever.
	if hasTable(t, db, "exec_atomic_probe") {
		t.Error("the statements' table survived a failed Exec, so Exec does not share their transaction")
	}
	if !hasTable(t, db, tracking) {
		t.Fatalf("the tracking table %s is absent, so this test cannot say anything about its rows", tracking)
	}
	if n := countRows(t, db, tracking); n != 0 {
		t.Errorf("the tracking table holds %d rows after a failed Exec; a later boot would skip this migration forever while its objects were never created", n)
	}

	// AND THE RETRY WORKS. A failure that left the version recorded would be undetectable
	// afterwards, so the same migration is applied again with an Exec that succeeds.
	if err := Apply(ctx, db, dia, tracking, []Migration{{
		Version: 1,
		Name:    "creates_then_fails",
		Stmts:   []string{"CREATE TABLE exec_atomic_probe (id INTEGER PRIMARY KEY)"},
		Exec: func(ctx context.Context, tx *sql.Tx) error {
			_, ierr := tx.ExecContext(ctx, "INSERT INTO exec_atomic_probe (id) VALUES (1)")
			return ierr
		},
	}}); err != nil {
		t.Fatalf("re-applying after a failed Exec: %v", err)
	}
	if !hasTable(t, db, "exec_atomic_probe") {
		t.Error("the retry did not create the table")
	}
	if n := countRows(t, db, "exec_atomic_probe"); n != 1 {
		t.Errorf("the retry left %d rows, want 1", n)
	}
	if n := countRows(t, db, tracking); n != 1 {
		t.Errorf("the tracking table holds %d rows after a successful retry, want 1", n)
	}
}

// TestExecRunsAfterTheStatementsAndBeforeTheTrackingRow pins the ORDER, which is a separate
// property from the atomicity.
//
// Order matters in both directions. Exec's rows are only meaningful against the objects the
// statements create, so running it first would make it write against nothing; and the tracking
// row must not exist yet when Exec runs, or a failing Exec would be recorded as applied.
func TestExecRunsAfterTheStatementsAndBeforeTheTrackingRow(t *testing.T) {
	ctx := context.Background()
	db, dia := openMem(t)
	const tracking = "schema_migrations_order"

	var sawTable, sawTracking bool
	if err := Apply(ctx, db, dia, tracking, []Migration{{
		Version: 1,
		Name:    "order",
		Stmts:   []string{"CREATE TABLE exec_order_probe (id INTEGER PRIMARY KEY)"},
		Exec: func(ctx context.Context, tx *sql.Tx) error {
			var n int
			if err := tx.QueryRowContext(ctx,
				"SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='exec_order_probe'").Scan(&n); err != nil {
				return err
			}
			sawTable = n > 0
			if err := tx.QueryRowContext(ctx,
				"SELECT COUNT(*) FROM "+tracking+" WHERE version = 1").Scan(&n); err != nil {
				return err
			}
			sawTracking = n > 0
			return nil
		},
	}}); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if !sawTable {
		t.Error("Exec ran BEFORE the statements, so its rows would be written against objects that do not exist yet")
	}
	if sawTracking {
		t.Error("the tracking row already existed when Exec ran, so a failing Exec would be recorded as applied")
	}
}

// TestExecIsRefusedOnANonTransactionalMigration pins the refusal rather than a silent downgrade.
//
// A non-transactional migration runs its statements outside any transaction, so there is no
// transaction for Exec to share. Honoring the field there would keep the call and quietly drop
// the guarantee, which is worse than not offering it.
func TestExecIsRefusedOnANonTransactionalMigration(t *testing.T) {
	ctx := context.Background()
	db, dia := openMem(t)
	const tracking = "schema_migrations_nontx"

	called := false
	err := Apply(ctx, db, dia, tracking, []Migration{{
		Version:          1,
		Name:             "both",
		NonTransactional: true,
		Stmts:            []string{"CREATE TABLE exec_nontx_probe (id INTEGER PRIMARY KEY)"},
		Exec: func(context.Context, *sql.Tx) error {
			called = true
			return nil
		},
	}})
	if err == nil {
		t.Fatal("a non-transactional migration with Exec was accepted")
	}
	if !strings.Contains(err.Error(), "non-transactional") {
		t.Errorf("the refusal was %q, which does not name the reason", err)
	}
	if called {
		t.Error("Exec was called on a non-transactional migration, so the guarantee was dropped rather than refused")
	}
	if hasTable(t, db, "exec_nontx_probe") {
		t.Error("the refusal happened after the statements ran; it must come first")
	}
}

// TestExecIsSkippedForAnAlreadyAppliedVersion pins that the bootstrap does not run twice.
//
// Apply skips a recorded version, and Exec must be skipped with it — otherwise every restart
// would re-run the bootstrap rows against a control plane that already has them.
func TestExecIsSkippedForAnAlreadyAppliedVersion(t *testing.T) {
	ctx := context.Background()
	db, dia := openMem(t)
	const tracking = "schema_migrations_skip"

	calls := 0
	mig := []Migration{{
		Version: 1,
		Name:    "once",
		Stmts:   []string{"CREATE TABLE exec_skip_probe (id INTEGER PRIMARY KEY)"},
		Exec: func(ctx context.Context, tx *sql.Tx) error {
			calls++
			_, err := tx.ExecContext(ctx, "INSERT INTO exec_skip_probe (id) VALUES (?)", calls)
			return err
		},
	}}
	for i := 0; i < 3; i++ {
		if err := Apply(ctx, db, dia, tracking, mig); err != nil {
			t.Fatalf("apply %d: %v", i+1, err)
		}
	}
	if calls != 1 {
		t.Errorf("Exec ran %d times across three Applies, want 1", calls)
	}
	if n := countRows(t, db, "exec_skip_probe"); n != 1 {
		t.Errorf("the bootstrap wrote %d rows across three Applies, want 1", n)
	}
}
