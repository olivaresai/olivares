// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"database/sql"
	"testing"
)

// TestCoreTablesMatchDescriptors guards against drift between a generated core
// table and its descriptor: the live columns (in order) must equal
// descriptor.AllColumns(). Because core tables are generated from descriptors,
// this should always hold — the test ensures a future hand-edit cannot silently
// break it.
func TestCoreTablesMatchDescriptors(t *testing.T) {
	st := openSQLiteTest(t, registerWidget)
	ss := st.(*sqlStore)
	ctx := context.Background()

	for _, d := range ss.reg.descriptors() {
		got := tableColumns(t, ss.db, d.Table)
		want := d.AllColumns()
		if !equalStrings(got, want) {
			t.Errorf("table %s columns = %v, want %v", d.Table, got, want)
		}
		_ = ctx
	}

	// The audit ledger's columns match the reader's expectation.
	got := tableColumns(t, ss.db, auditTable)
	if !equalStrings(got, auditColumns) {
		t.Errorf("audit_events columns = %v, want %v", got, auditColumns)
	}
}

// tableColumns returns a SQLite table's columns in definition order.
func tableColumns(t *testing.T, db *sql.DB, table string) []string {
	t.Helper()
	rows, err := db.Query("PRAGMA table_info(" + table + ")")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var cols []string
	for rows.Next() {
		var (
			cid, notnull, pk int
			name, ctype      string
			dflt             sql.NullString
		)
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			t.Fatal(err)
		}
		cols = append(cols, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return cols
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
