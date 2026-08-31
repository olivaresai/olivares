// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package migrate

import (
	"context"
	"database/sql"
	"testing"
)

func TestApplyRecordsPhase(t *testing.T) {
	ctx := context.Background()
	db, dia := openMem(t)
	const tracking = "sm_phase"

	migs := []Migration{
		{Version: 1, Name: "add-col", Phase: Expand, Stmts: []string{"CREATE TABLE a(x INTEGER)"}},
		{Version: 2, Name: "drop-col", Phase: Contract, Stmts: []string{"CREATE TABLE b(y INTEGER)"}},
	}
	if err := Apply(ctx, db, dia, tracking, migs); err != nil {
		t.Fatalf("apply: %v", err)
	}
	var phase string
	if err := db.QueryRow("SELECT phase FROM " + tracking + " WHERE version=1").Scan(&phase); err != nil {
		t.Fatal(err)
	}
	if phase != "expand" {
		t.Errorf("v1 phase = %q, want expand", phase)
	}
	if err := db.QueryRow("SELECT phase FROM " + tracking + " WHERE version=2").Scan(&phase); err != nil {
		t.Fatal(err)
	}
	if phase != "contract" {
		t.Errorf("v2 phase = %q, want contract", phase)
	}
}

func TestRevertReversesExpand(t *testing.T) {
	ctx := context.Background()
	db, dia := openMem(t)
	const tracking = "sm_revert"

	m := Migration{
		Version: 1, Name: "add-widgets", Phase: Expand,
		Stmts:     []string{"CREATE TABLE widgets(x INTEGER)"},
		DownStmts: []string{"DROP TABLE widgets"},
	}
	if err := Apply(ctx, db, dia, tracking, []Migration{m}); err != nil {
		t.Fatalf("apply: %v", err)
	}
	// The table exists.
	if !tableExists(t, db, "widgets") {
		t.Fatal("expand did not create the table")
	}
	// Revert drops it and stamps reverted_at — but keeps the tracking row.
	if err := Revert(ctx, db, dia, tracking, m); err != nil {
		t.Fatalf("revert: %v", err)
	}
	if tableExists(t, db, "widgets") {
		t.Fatal("revert did not drop the table")
	}
	if got := countRows(t, db, tracking); got != 1 {
		t.Fatalf("revert deleted the tracking row (rows=%d); it must keep history", got)
	}
	var reverted string
	if err := db.QueryRow("SELECT COALESCE(reverted_at,'') FROM " + tracking + " WHERE version=1").Scan(&reverted); err != nil {
		t.Fatal(err)
	}
	if reverted == "" {
		t.Error("reverted_at was not stamped")
	}
	// A re-Apply does NOT resurrect a reverted version (no accidental re-run).
	if err := Apply(ctx, db, dia, tracking, []Migration{m}); err != nil {
		t.Fatalf("re-apply after revert: %v", err)
	}
	if tableExists(t, db, "widgets") {
		t.Fatal("re-apply silently re-ran a reverted migration; it must not")
	}
}

func TestRevertRefusesForwardOnly(t *testing.T) {
	ctx := context.Background()
	db, dia := openMem(t)
	const tracking = "sm_fwd"
	m := Migration{Version: 1, Name: "fwd", Stmts: []string{"CREATE TABLE c(x INTEGER)"}}
	if err := Apply(ctx, db, dia, tracking, []Migration{m}); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if err := Revert(ctx, db, dia, tracking, m); err == nil {
		t.Fatal("Revert of a forward-only migration (no DownStmts) must fail loudly")
	}
}

func TestNonTransactionalApply(t *testing.T) {
	ctx := context.Background()
	db, dia := openMem(t)
	const tracking = "sm_nontx"
	// SQLite has no CREATE INDEX CONCURRENTLY, but the non-transactional code path is
	// engine-agnostic: it must run the statements and still record the tracking row.
	m := Migration{
		Version: 1, Name: "online-index", Phase: Expand, NonTransactional: true,
		Stmts: []string{"CREATE TABLE d(x INTEGER)", "CREATE INDEX d_x ON d(x)"},
	}
	if err := Apply(ctx, db, dia, tracking, []Migration{m}); err != nil {
		t.Fatalf("non-transactional apply: %v", err)
	}
	if got := countRows(t, db, tracking); got != 1 {
		t.Fatalf("tracking rows = %d, want 1 (the non-tx migration must be recorded)", got)
	}
	if !tableExists(t, db, "d") {
		t.Fatal("non-transactional migration did not run its statements")
	}
}

func TestEnsureTrackingReconcilesOldTable(t *testing.T) {
	ctx := context.Background()
	db, dia := openMem(t)
	const tracking = "sm_legacy"
	// Simulate a pre tracking table (no phase / reverted_at columns).
	if _, err := db.ExecContext(ctx,
		"CREATE TABLE "+tracking+" (version INTEGER PRIMARY KEY, name TEXT NOT NULL, applied_at TEXT NOT NULL)"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx,
		"INSERT INTO "+tracking+"(version,name,applied_at) VALUES(1,'old','2026-01-01T00:00:00Z')"); err != nil {
		t.Fatal(err)
	}
	// Apply must additively reconcile the new columns onto the old table, then apply
	// the new migration — no destructive change to the bookkeeping.
	m := Migration{Version: 2, Name: "new", Phase: Contract, Stmts: []string{"CREATE TABLE e(x INTEGER)"}}
	if err := Apply(ctx, db, dia, tracking, []Migration{m}); err != nil {
		t.Fatalf("apply against a legacy tracking table: %v", err)
	}
	// The legacy row defaulted to expand; the new one recorded contract.
	var oldPhase, newPhase string
	if err := db.QueryRow("SELECT phase FROM " + tracking + " WHERE version=1").Scan(&oldPhase); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow("SELECT phase FROM " + tracking + " WHERE version=2").Scan(&newPhase); err != nil {
		t.Fatal(err)
	}
	if oldPhase != "expand" || newPhase != "contract" {
		t.Fatalf("phases = (%q,%q), want (expand,contract)", oldPhase, newPhase)
	}
}

// TestEnsureTrackingRollsBackLegacyReconciliation proves the two additive
// ALTERs are one unit. SQLite deliberately hides generated columns from
// pragma_table_info: a hidden generated reverted_at therefore lets phase be
// added first and makes the second ALTER fail with a duplicate name. The
// transaction must remove phase again, leaving the historical three-column
// shape retryable rather than a permanently unsupported four-column tracker.
func TestEnsureTrackingRollsBackLegacyReconciliation(t *testing.T) {
	ctx := context.Background()
	db, dia := openMem(t)
	const tracking = "sm_legacy_atomic"
	if _, err := db.ExecContext(ctx, `CREATE TABLE `+tracking+` (
version INTEGER PRIMARY KEY,
name TEXT NOT NULL,
applied_at TEXT NOT NULL,
reverted_at TEXT GENERATED ALWAYS AS ('hidden fault') VIRTUAL
)`); err != nil {
		t.Fatal(err)
	}
	cols, err := dia.TableColumns(ctx, db, tracking)
	if err != nil {
		t.Fatal(err)
	}
	if cols["phase"] || cols["reverted_at"] {
		t.Fatalf("fault fixture is visible through TableColumns: %+v", cols)
	}

	if err := Apply(ctx, db, dia, tracking, nil); err == nil {
		t.Fatal("tracking reconciliation succeeded despite the hidden duplicate reverted_at")
	}
	cols, err = dia.TableColumns(ctx, db, tracking)
	if err != nil {
		t.Fatal(err)
	}
	if cols["phase"] {
		t.Fatalf("failed reconciliation committed phase: %+v", cols)
	}
	var phasePhysical int
	if err := db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM pragma_table_xinfo(?, 'main') WHERE name='phase'", tracking).
		Scan(&phasePhysical); err != nil {
		t.Fatal(err)
	}
	if phasePhysical != 0 {
		t.Fatalf("failed reconciliation left %d physical phase columns", phasePhysical)
	}

	if _, err := db.ExecContext(ctx, "ALTER TABLE "+tracking+" DROP COLUMN reverted_at"); err != nil {
		t.Fatalf("remove injected hidden column: %v", err)
	}
	if err := Apply(ctx, db, dia, tracking, nil); err != nil {
		t.Fatalf("retry tracking reconciliation: %v", err)
	}
	cols, err = dia.TableColumns(ctx, db, tracking)
	if err != nil {
		t.Fatal(err)
	}
	if !cols["phase"] || !cols["reverted_at"] {
		t.Fatalf("retry did not converge both tracking columns: %+v", cols)
	}
}

func tableExists(t *testing.T, db *sql.DB, name string) bool {
	t.Helper()
	var n int
	if err := db.QueryRow("SELECT count(*) FROM sqlite_master WHERE type='table' AND name=?", name).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n > 0
}
