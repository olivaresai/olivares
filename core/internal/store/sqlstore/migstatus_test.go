// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/olivaresai/olivares/core/internal/store/dialect"
	"github.com/olivaresai/olivares/core/migrate"
	"github.com/olivaresai/olivares/core/store"
)

// TestMigrationStatusSQLite seeds two tracking tables (core + a module) via the REAL
// migrate runner — an expand we then revert, a contract, and a module expand — and
// asserts MigrationStatus reads back the version, phase and reverted state of each.
func TestMigrationStatusSQLite(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "olivares.db")
	dia, ok := dialect.New(store.EngineSQLite)
	if !ok {
		t.Fatal("sqlite dialect unavailable")
	}

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	core := []migrate.Migration{
		{Version: 1, Name: "init", Stmts: []string{"CREATE TABLE t1 (id INTEGER)"}, DownStmts: []string{"DROP TABLE t1"}},
		{Version: 2, Name: "cleanup", Phase: migrate.Contract, Stmts: []string{"CREATE TABLE t2 (id INTEGER)"}},
	}
	if err := migrate.Apply(ctx, db, dia, "schema_migrations_core", core); err != nil {
		t.Fatalf("apply core: %v", err)
	}
	if err := migrate.Revert(ctx, db, dia, "schema_migrations_core", core[0]); err != nil {
		t.Fatalf("revert core v1: %v", err)
	}
	mod := []migrate.Migration{{Version: 1, Name: "0001_index.sql", Stmts: []string{"CREATE TABLE m1 (id INTEGER)"}}}
	if err := migrate.Apply(ctx, db, dia, "schema_migrations_mod_demo", mod); err != nil {
		t.Fatalf("apply mod: %v", err)
	}
	_ = db.Close()

	recs, err := MigrationStatus(ctx, store.Config{Engine: store.EngineSQLite, DSN: dbPath})
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if len(recs) != 3 {
		t.Fatalf("want 3 records, got %d: %+v", len(recs), recs)
	}
	byKey := map[string]store.MigrationRecord{}
	for _, r := range recs {
		byKey[fmt.Sprintf("%s/%d", r.Table, r.Version)] = r
	}
	if v := byKey["schema_migrations_core/1"]; v.Phase != "expand" || !v.Reverted {
		t.Errorf("core v1: want expand+reverted, got %+v", v)
	}
	if v := byKey["schema_migrations_core/2"]; v.Phase != "contract" || v.Reverted {
		t.Errorf("core v2: want contract+applied, got %+v", v)
	}
	if v := byKey["schema_migrations_mod_demo/1"]; v.Phase != "expand" || v.Reverted || v.Name != "0001_index.sql" {
		t.Errorf("mod v1: unexpected %+v", v)
	}
}

// TestMigrationStatusEmpty: a database with no schema_migrations* tables returns no
// records and no error (and an UNRELATED table is not mistaken for a tracking table).
func TestMigrationStatusEmpty(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "empty.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := db.Exec("CREATE TABLE unrelated (id INTEGER)"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	_ = db.Close()

	recs, err := MigrationStatus(context.Background(), store.Config{Engine: store.EngineSQLite, DSN: dbPath})
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if len(recs) != 0 {
		t.Fatalf("want 0 records, got %d: %+v", len(recs), recs)
	}
}
