// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package dialect

import (
	"testing"

	"github.com/olivaresai/olivares/core/model"
)

func TestRebindPositional(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"SELECT 1", "SELECT 1"},
		{"a = ?", "a = $1"},
		{"a = ? AND b = ?", "a = $1 AND b = $2"},
		{"INSERT INTO t VALUES (?, ?, ?)", "INSERT INTO t VALUES ($1, $2, $3)"},
		// A '?' inside a string literal must NOT be rewritten.
		{"x = '?' AND y = ?", "x = '?' AND y = $1"},
		// Escaped quote ('') inside a literal keeps the literal open.
		{"x = 'a''?''b' AND y = ?", "x = 'a''?''b' AND y = $1"},
	}
	for _, tc := range cases {
		if got := rebindPositional(tc.in); got != tc.want {
			t.Errorf("rebind(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestSQLiteRebindIdentity confirms SQLite leaves '?' untouched.
func TestSQLiteRebindIdentity(t *testing.T) {
	q := "a = ? AND b = ?"
	if got := (sqliteDialect{}).Rebind(q); got != q {
		t.Errorf("sqlite rebind = %q, want unchanged", got)
	}
}

// TestColumnTypeParity checks that every portable kind maps to a concrete,
// NOT NULL-aware column type on both engines (no kind is left unmapped).
func TestColumnTypeParity(t *testing.T) {
	sq := sqliteDialect{}
	pg := postgresDialect{appRole: "olivares_app"}
	for k := model.KindText; k <= model.KindBytes; k++ {
		if !k.Valid() {
			continue
		}
		if got := sq.ColumnType(k, false); got == "" {
			t.Errorf("sqlite ColumnType(%v) empty", k)
		}
		if got := pg.ColumnType(k, true); got == "" {
			t.Errorf("postgres ColumnType(%v) empty", k)
		}
	}
}
