// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package dr

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"testing"
)

// The snapshot's read connection is one of the SQLite connections the engine opens,
// so it keeps its temporary storage in memory like every other: with the
// default temp_store a statement that needs a temporary b-tree writes a temporary
// file into the system's temporary directory, which the Olivares Server's SELinux
// policy refuses, and the statement — here a backup's or the pre-upgrade snapshot's
// VACUUM INTO — fails loudly (#3374).
func TestSnapshotSourceSetsTempStoreOnEveryConnection(t *testing.T) {
	ctx := context.Background()
	src := filepath.Join(t.TempDir(), "source.db")
	db, err := openSnapshotSource(src)
	if err != nil {
		t.Fatalf("openSnapshotSource: %v", err)
	}
	defer db.Close() //nolint:errcheck
	// SetMaxIdleConns(0) makes every query dial a fresh physical connection, so both
	// reads below configure a replacement, not the same lucky first connection.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(0)
	for range 2 {
		var got string
		if err := db.QueryRowContext(ctx, "PRAGMA temp_store").Scan(&got); err != nil {
			t.Fatalf("read temp_store: %v", err)
		}
		if got != "2" {
			t.Fatalf("temp_store = %q on a fresh connection, want \"2\"", got)
		}
	}
}

// The behavioral leg, with the recipe the reviewer of #3352 used: a statement that
// needs a temporary b-tree (a 200,000-row sort of random 100-byte blobs) holds a
// temporary file open through the DEFAULT driver open — the control — and none
// through the snapshot's source open. (VACUUM INTO itself was measured to open no
// temporary file at up to 500,000 indexed 200-byte blobs; the pragma protects the
// connection, and this statement is how that is observed.)
func TestSnapshotSourceTemporarySortOpensNoFiles(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("open SQLite temporary files are observed through procfs")
	}
	ctx := context.Background()
	tests := []struct {
		name    string
		open    func(string) (*sql.DB, error)
		control bool
	}{
		{name: "default driver control", open: func(path string) (*sql.DB, error) {
			return sql.Open("sqlite", path)
		}, control: true},
		{name: "snapshot source", open: openSnapshotSource},
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
			rows, err := db.QueryContext(ctx, `WITH RECURSIVE c(x) AS (
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

// SnapshotSQLite end to end on the public seam: the pragma-carrying DSN still
// snapshots every row.
func TestSnapshotSQLiteCopiesRows(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	src := filepath.Join(dir, "source.db")
	db, err := sql.Open("sqlite", src)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, "CREATE TABLE t (k INTEGER PRIMARY KEY, v TEXT)"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, "INSERT INTO t(v) VALUES ('a'),('b'),('c')"); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(dir, "snapshot.db")
	if err := SnapshotSQLite(ctx, src, dst); err != nil {
		t.Fatalf("SnapshotSQLite: %v", err)
	}
	snap, err := sql.Open("sqlite", dst)
	if err != nil {
		t.Fatal(err)
	}
	defer snap.Close() //nolint:errcheck
	var n int
	if err := snap.QueryRowContext(ctx, "SELECT count(*) FROM t").Scan(&n); err != nil {
		t.Fatalf("read snapshot: %v", err)
	}
	if n != 3 {
		t.Fatalf("snapshot has %d rows, want 3", n)
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
