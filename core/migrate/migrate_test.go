// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package migrate

import (
	"context"
	"database/sql"
	"testing"

	"github.com/olivaresai/olivares/core/internal/store/dialect"
	"github.com/olivaresai/olivares/core/store"

	_ "modernc.org/sqlite"
)

func openMem(t *testing.T) (*sql.DB, dialect.Dialect) {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	dia, ok := dialect.New(store.EngineSQLite)
	if !ok {
		t.Fatal("no sqlite dialect")
	}
	return db, dia
}

func countRows(t *testing.T, db *sql.DB, table string) int {
	t.Helper()
	var n int
	if err := db.QueryRow("SELECT count(*) FROM " + table).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestApplyAndIdempotent(t *testing.T) {
	ctx := context.Background()
	db, dia := openMem(t)
	const tracking = "schema_migrations_test"

	migs := []Migration{
		{Version: 1, Name: "t1", Stmts: []string{"CREATE TABLE t1(x INTEGER)"}},
		{Version: 2, Name: "t2", Stmts: []string{"CREATE TABLE t2(y INTEGER)", "CREATE INDEX t2_y ON t2(y)"}},
	}
	if err := Apply(ctx, db, dia, tracking, migs); err != nil {
		t.Fatalf("first apply: %v", err)
	}
	if got := countRows(t, db, tracking); got != 2 {
		t.Fatalf("tracking rows = %d, want 2", got)
	}
	// Re-applying is a no-op: the tables already exist, but nothing re-runs.
	if err := Apply(ctx, db, dia, tracking, migs); err != nil {
		t.Fatalf("second apply: %v", err)
	}
	if got := countRows(t, db, tracking); got != 2 {
		t.Fatalf("tracking rows after re-apply = %d, want 2", got)
	}

	// Adding a new migration applies only the new one.
	migs = append(migs, Migration{Version: 3, Name: "t3", Stmts: []string{"CREATE TABLE t3(z INTEGER)"}})
	if err := Apply(ctx, db, dia, tracking, migs); err != nil {
		t.Fatalf("incremental apply: %v", err)
	}
	if got := countRows(t, db, tracking); got != 3 {
		t.Fatalf("tracking rows = %d, want 3", got)
	}
}

func TestApplyAtomicOnFailure(t *testing.T) {
	ctx := context.Background()
	db, dia := openMem(t)
	const tracking = "schema_migrations_fail"

	migs := []Migration{
		{Version: 1, Name: "ok", Stmts: []string{"CREATE TABLE ok(x INTEGER)"}},
		{Version: 2, Name: "bad", Stmts: []string{
			"CREATE TABLE good_part(y INTEGER)",
			"CREATE TABLE good_part(y INTEGER)", // duplicate -> fails
		}},
	}
	if err := Apply(ctx, db, dia, tracking, migs); err == nil {
		t.Fatal("expected failure on duplicate-table migration")
	}
	// Migration 1 committed; migration 2 rolled back wholly (its partial first
	// statement must not survive, and it must not be recorded).
	if got := countRows(t, db, tracking); got != 1 {
		t.Fatalf("tracking rows = %d, want 1 (only the good migration)", got)
	}
	var n int
	err := db.QueryRow("SELECT count(*) FROM sqlite_master WHERE type='table' AND name='good_part'").Scan(&n)
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatal("failed migration left a partial table behind")
	}
}
