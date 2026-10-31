// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"testing"

	"github.com/olivaresai/olivares/core/store"
)

// SQLite writes a statement's temporary b-trees and journals to a file in the system's temporary directory unless
// its temporary store is memory. Where that directory refuses the engine (the Olivares Server's SELinux policy, a
// read-only /tmp), every statement that needs one fails: the engine lost its authorization history that way
// . Every connection the store opens keeps its temporary storage in memory.
func TestEverySQLiteOpenKeepsItsTemporaryStorageInMemory(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "temp-store.db")
	engine, err := openSQLite(path)
	if err != nil {
		t.Fatalf("openSQLite: %v", err)
	}
	defer engine.Close() //nolint:errcheck
	assertPragmaOnReplacementConnections(ctx, t, engine, "temp_store", "2")

	reader, err := openOfflineReader(ctx, store.Config{Engine: store.EngineSQLite, DSN: path})
	if err != nil {
		t.Fatalf("openOfflineReader: %v", err)
	}
	defer reader.Close() //nolint:errcheck
	assertPragmaOnReplacementConnections(ctx, t, reader.db, "temp_store", "2")

	status, err := openForRead(store.Config{Engine: store.EngineSQLite, DSN: path})
	if err != nil {
		t.Fatalf("openForRead: %v", err)
	}
	defer status.Close() //nolint:errcheck
	assertPragmaOnReplacementConnections(ctx, t, status, "temp_store", "2")
}

func assertPragmaOnReplacementConnections(ctx context.Context, t *testing.T, db *sql.DB, name, want string) {
	t.Helper()
	db.SetMaxIdleConns(0)
	for range 2 {
		assertPragma(ctx, t, db, name, want)
	}
}

func TestSQLiteTemporarySortOpensNoFiles(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("open SQLite temporary files are observed through procfs")
	}
	tests := []struct {
		name    string
		open    func(string) (*sql.DB, error)
		control bool
	}{
		{name: "default driver control", open: func(path string) (*sql.DB, error) {
			return sql.Open("sqlite", path)
		}, control: true},
		{name: "engine", open: openSQLite},
		{name: "offline reader", open: func(path string) (*sql.DB, error) {
			reader, err := openOfflineReader(context.Background(), store.Config{Engine: store.EngineSQLite, DSN: path})
			if err != nil {
				return nil, err
			}
			return reader.db, nil
		}},
		{name: "migration status", open: func(path string) (*sql.DB, error) {
			return openForRead(store.Config{Engine: store.EngineSQLite, DSN: path})
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "sort.db")
			if err := os.WriteFile(path, nil, 0o600); err != nil {
				t.Fatal(err)
			}
			db, err := tt.open(path)
			if err != nil {
				t.Fatalf("open: %v", err)
			}
			defer db.Close() //nolint:errcheck
			rows, err := db.QueryContext(context.Background(), `WITH RECURSIVE c(x) AS (
				SELECT 1 UNION ALL SELECT x+1 FROM c WHERE x<200000
			) SELECT x FROM c ORDER BY randomblob(100)`)
			if err != nil {
				t.Fatalf("start sort: %v", err)
			}
			defer rows.Close() //nolint:errcheck
			if !rows.Next() {
				t.Fatalf("first sorted row: %v", rows.Err())
			}
			var x int
			if err := rows.Scan(&x); err != nil {
				t.Fatalf("scan first sorted row: %v", err)
			}
			files := countOpenSQLiteTempFiles(t)
			if tt.control && files == 0 {
				t.Fatal("sort opened no SQLite temporary file; the control does not prove spilling")
			}
			if !tt.control && files != 0 {
				t.Fatalf("sort has %d open SQLite temporary files, want 0", files)
			}
		})
	}
}

// sqliteTempFile matches the /proc/self/fd target of a file SQLite spilled to the
// temporary directory: named etilqs_* up to SQLite 3.53, and from SQLite 3.54.1
// (modernc.org/sqlite 1.61.0) an anonymous O_TMPFILE, listed as "<dir>/#<inode> (deleted)".
var sqliteTempFile = regexp.MustCompile(`etilqs_|/#[0-9]+ \(deleted\)$`)

func countOpenSQLiteTempFiles(t *testing.T) int {
	t.Helper()
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Fatalf("list open files: %v", err)
	}
	count := 0
	for _, entry := range entries {
		target, err := os.Readlink("/proc/self/fd/" + entry.Name())
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue // The descriptor closed between ReadDir and Readlink.
			}
			t.Fatalf("inspect open file %s: %v", entry.Name(), err)
		}
		if sqliteTempFile.MatchString(target) {
			count++
		}
	}
	return count
}
