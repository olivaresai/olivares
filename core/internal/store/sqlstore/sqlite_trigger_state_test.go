// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only

package sqlstore

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/olivaresai/olivares/core/internal/store/dialect"
	"github.com/olivaresai/olivares/core/store"
)

// TestSQLiteDialectReportsNoEnableState closes a vacuity the fourth adversarial
// round found in the fix for the third.
//
// The constants were separated so that a policy about PostgreSQL's chosen ENABLE
// ALWAYS state could never govern SQLite, which has no per-trigger state at all —
// and a unit test asserted the two constants differ. That is not enough: nothing
// asserted which one the SQLite DIALECT actually reports. Reverting sqlite.go to
// TriggerFiresAlways would have reinstated the whole hazard with every test green.
//
// This runs against a real SQLite database, so it needs no server and no skip.
func TestSQLiteDialectReportsNoEnableState(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dsn := filepath.Join(t.TempDir(), "state.db")
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer db.Close() //nolint:errcheck

	for _, stmt := range []string{
		"CREATE TABLE facts (id INTEGER PRIMARY KEY, note TEXT)",
		"CREATE TRIGGER facts_no_delete BEFORE DELETE ON facts " +
			"BEGIN SELECT RAISE(ABORT,'table is append-only'); END",
	} {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("exec %q: %v", stmt, err)
		}
	}

	dia, ok := dialect.New(store.EngineSQLite)
	if !ok {
		t.Fatal("no sqlite dialect")
	}
	live, err := dia.SchemaTriggers(ctx, db)
	if err != nil {
		t.Fatalf("SchemaTriggers: %v", err)
	}
	info, ok := live[dialect.TriggerKey{Schema: "main", Table: "facts", Name: "facts_no_delete"}]
	if !ok {
		t.Fatalf("the trigger is absent from the catalog read; got %d triggers", len(live))
	}

	if info.EnableState == dialect.TriggerFiresAlways {
		t.Fatal("the SQLite dialect reports PostgreSQL's ENABLE ALWAYS. That state is one an " +
			"operator CHOSE and carries an open policy question; SQLite has no per-trigger " +
			"state at all, so any policy about ALWAYS would silently govern this engine and " +
			"could refuse every SQLite trigger")
	}
	if info.EnableState != dialect.TriggerNoEnableState {
		t.Fatalf("the SQLite dialect reports %q, want %q",
			info.EnableState, dialect.TriggerNoEnableState)
	}
	// And the guard must still be judged as firing: SQLite triggers always run.
	if !info.EnableState.Fires() {
		t.Fatal("a trigger present in a SQLite schema fires; the self-test must not call it inert")
	}
	// The body is still reported, which is what the SQLite digest check consumes.
	if info.Definition == "" {
		t.Fatal("the SQLite dialect reported no definition text; the body digest would be vacuous")
	}
}
