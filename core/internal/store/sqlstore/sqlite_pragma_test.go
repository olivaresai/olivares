// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only

package sqlstore

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

// TestOpenSQLiteAppliesPragmasToEveryConnection is the P1-1 RED, plus the
// pre-existing defect it uncovered.
//
// The pragmas were executed once with db.Exec after opening the pool. That sets them
// on whichever physical connection served that one call — not on the pool. The pool
// is capped at one connection, but database/sql discards a connection it considers
// broken and dials a fresh one, and the replacement would come up with SQLite's
// defaults: foreign_keys OFF, busy_timeout 0, journal_mode DELETE. Passing them as
// DSN parameters makes the driver apply them inside its own connection setup, so
// every physical connection is configured.
//
// recursive_triggers is the new one and it is a security guard. SQLite's REPLACE
// conflict resolution DELETES the conflicting row, and those deletes fire BEFORE
// DELETE triggers only when recursive_triggers is enabled. Every append-only table in
// this schema is enforced by exactly such a trigger, so with the pragma off an
// INSERT OR REPLACE quietly deletes an append-only row — including a promoted
// evidence-policy fact — and the immutability guard never runs.
func TestOpenSQLiteAppliesPragmasToEveryConnection(t *testing.T) {
	ctx := context.Background()
	db, err := openSQLite(filepath.Join(t.TempDir(), "pragma.db"))
	if err != nil {
		t.Fatalf("openSQLite: %v", err)
	}
	defer db.Close() //nolint:errcheck

	for _, tc := range []struct {
		pragma string
		want   string
	}{
		{"recursive_triggers", "1"},
		{"foreign_keys", "1"},
		{"busy_timeout", "5000"},
		{"journal_mode", "wal"},
	} {
		var got string
		if err := db.QueryRowContext(ctx, "PRAGMA "+tc.pragma).Scan(&got); err != nil {
			t.Fatalf("PRAGMA %s: %v", tc.pragma, err)
		}
		if got != tc.want {
			t.Errorf("PRAGMA %s = %q, want %q", tc.pragma, got, tc.want)
		}
	}

	// The behavior that matters: an append-only guard must survive REPLACE.
	for _, stmt := range []string{
		"CREATE TABLE probe (id TEXT PRIMARY KEY, mode TEXT NOT NULL)",
		"CREATE TRIGGER probe_no_delete BEFORE DELETE ON probe\n" +
			"BEGIN SELECT RAISE(ABORT,'probe is append-only'); END",
		"CREATE TRIGGER probe_no_update BEFORE UPDATE ON probe\n" +
			"BEGIN SELECT RAISE(ABORT,'probe is append-only'); END",
		"INSERT INTO probe (id, mode) VALUES ('fact-1','required')",
	} {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("setup %q: %v", stmt, err)
		}
	}

	if _, err := db.ExecContext(ctx,
		"INSERT OR REPLACE INTO probe (id, mode) VALUES ('fact-1','legacy_optional')"); err == nil {
		t.Fatal("INSERT OR REPLACE overwrote an append-only row: the BEFORE DELETE guard did not fire")
	}

	var mode string
	if err := db.QueryRowContext(ctx, "SELECT mode FROM probe WHERE id='fact-1'").Scan(&mode); err != nil {
		t.Fatalf("re-read probe row: %v", err)
	}
	if mode != "required" {
		t.Fatalf("append-only row was downgraded to %q by REPLACE", mode)
	}
}

// TestOpenSQLitePragmasSurviveAReplacementConnection proves the pragmas are a
// property of the DSN rather than of one warmed-up connection: a second pool opened
// on the same DSN — which is what database/sql effectively does when it dials a
// replacement — comes up configured too.
func TestOpenSQLitePragmasSurviveAReplacementConnection(t *testing.T) {
	ctx := context.Background()
	dsn := filepath.Join(t.TempDir(), "pragma-reconnect.db")

	first, err := openSQLite(dsn)
	if err != nil {
		t.Fatalf("openSQLite: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("close first pool: %v", err)
	}

	second, err := openSQLite(dsn)
	if err != nil {
		t.Fatalf("re-open: %v", err)
	}
	defer second.Close() //nolint:errcheck

	assertPragma(ctx, t, second, "recursive_triggers", "1")
	assertPragma(ctx, t, second, "foreign_keys", "1")
}

func assertPragma(ctx context.Context, t *testing.T, db *sql.DB, pragma, want string) {
	t.Helper()
	var got string
	if err := db.QueryRowContext(ctx, "PRAGMA "+pragma).Scan(&got); err != nil {
		t.Fatalf("PRAGMA %s: %v", pragma, err)
	}
	if got != want {
		t.Fatalf("PRAGMA %s = %q, want %q", pragma, got, want)
	}
}
