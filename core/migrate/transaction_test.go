// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package migrate

import (
	"context"
	"testing"
)

func TestApplyTxSharesTheCallersCommit(t *testing.T) {
	ctx := context.Background()
	db, dia := openMem(t)
	plan := []Migration{{Version: 1, Name: "schema", Stmts: []string{"CREATE TABLE schema_probe (id INTEGER)"}}}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := ApplyTx(ctx, tx, dia, "schema_migrations_probe", plan); err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if hasTable(t, db, "schema_probe") || hasTable(t, db, "schema_migrations_probe") {
		t.Fatal("schema or tracking survived rollback")
	}
	tx, err = db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := ApplyTx(ctx, tx, dia, "schema_migrations_probe", plan); err != nil {
		t.Fatal(err)
	}
	// The non-idempotent statement must be skipped on the second application.
	if err := ApplyTx(ctx, tx, dia, "schema_migrations_probe", plan); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if !hasTable(t, db, "schema_probe") {
		t.Fatal("schema missing after commit")
	}
}

func TestReconcileTxRepairsWithoutRewritingOrRevivingHistory(t *testing.T) {
	ctx := context.Background()
	db, dia := openMem(t)
	plan := []Migration{{Version: 1, Name: "guard", Stmts: []string{"CREATE TABLE IF NOT EXISTS guard_probe (id INTEGER)"}}}
	repair := func() error {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		if err := ReconcileTx(ctx, tx, dia, "schema_migrations_guard", plan); err != nil {
			return err
		}
		return tx.Commit()
	}
	if err := repair(); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("UPDATE schema_migrations_guard SET applied_at = 'original'; DROP TABLE guard_probe"); err != nil {
		t.Fatal(err)
	}
	if err := repair(); err != nil {
		t.Fatal(err)
	}
	if !hasTable(t, db, "guard_probe") {
		t.Fatal("guard was not repaired")
	}
	var stamp string
	if err := db.QueryRow("SELECT applied_at FROM schema_migrations_guard").Scan(&stamp); err != nil {
		t.Fatal(err)
	}
	if stamp != "original" {
		t.Fatal("repair rewrote history")
	}
	if _, err := db.Exec("UPDATE schema_migrations_guard SET reverted_at = 'reverted'; DROP TABLE guard_probe"); err != nil {
		t.Fatal(err)
	}
	if err := repair(); err == nil {
		t.Fatal("repair revived a reverted migration")
	}
	if hasTable(t, db, "guard_probe") {
		t.Fatal("refused repair changed schema")
	}
}
