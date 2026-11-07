// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

// A backup reads the folder registry of whatever store it is given, and a store
// written by an older binary has no backup column yet. The cross-edition release
// gate measured `dr backup` refusing such a store with "no such column: backup".
func TestListBackupFoldersReadsARegistryWithoutTheBackupColumn(t *testing.T) {
	for _, c := range []struct {
		name, schema, row string
		want              bool
	}{
		// The API documents the field as "absent defaults to true".
		{"older store without the column", `CREATE TABLE sessions_workspace (workspace_ref TEXT PRIMARY KEY, root_path TEXT NOT NULL)`, `INSERT INTO sessions_workspace VALUES ('wsp_a', '/srv/a')`, true},
		{"current store with backup false", `CREATE TABLE sessions_workspace (workspace_ref TEXT PRIMARY KEY, root_path TEXT NOT NULL, backup INTEGER)`, `INSERT INTO sessions_workspace VALUES ('wsp_a', '/srv/a', 0)`, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "store.db"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = db.Close() })
			for _, q := range []string{c.schema, c.row} {
				if _, err := db.Exec(q); err != nil {
					t.Fatal(err)
				}
			}
			got, err := ListBackupFolders(context.Background(), db)
			if err != nil {
				t.Fatalf("ListBackupFolders: %v", err)
			}
			if len(got) != 1 || got[0].Ref != "wsp_a" || got[0].RootPath != "/srv/a" || got[0].Backup != c.want {
				t.Fatalf("ListBackupFolders = %+v, want one folder wsp_a /srv/a with Backup %v", got, c.want)
			}
		})
	}
}
