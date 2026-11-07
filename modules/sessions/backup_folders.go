// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// BackupFolder is a registered folder as the disaster-recovery backup reads it.
type BackupFolder struct {
	// Ref is the folder's registry reference, a stable unique token.
	Ref string
	// RootPath is the canonical host path the folder was registered with.
	RootPath string
	// Backup is false for a folder registered with backup: false (a scratch folder);
	// a row that never set it is backed up.
	Backup bool
}

// ListBackupFolders reads every registered folder, whatever its tenant or state, from
// a SQLite store snapshot. A disabled folder still holds a person's files. A
// snapshot without the folder registry has no folders.
func ListBackupFolders(ctx context.Context, snapshot *sql.DB) ([]BackupFolder, error) {
	// A store written by an older binary has no backup column yet, and the snapshot is
	// read-only, so it cannot be migrated first: read the column only where it exists.
	// Absent reads as NULL, which is backed up, as the API documents.
	backupCol := "NULL"
	var present int
	if err := snapshot.QueryRowContext(ctx, "SELECT count(*) FROM pragma_table_info(?) WHERE name = ?", workspaceTable, colWsBackup).Scan(&present); err != nil {
		return nil, fmt.Errorf("read registered folders: %w", err)
	}
	if present > 0 {
		backupCol = colWsBackup
	}
	rows, err := snapshot.QueryContext(ctx, "SELECT "+colWsRef+", "+colWsRootPath+", "+backupCol+" FROM "+workspaceTable+" ORDER BY "+colWsRef)
	if err != nil {
		if strings.Contains(err.Error(), "no such table") {
			return nil, nil
		}
		return nil, fmt.Errorf("read registered folders: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []BackupFolder
	for rows.Next() {
		var f BackupFolder
		var backup sql.NullBool
		if err := rows.Scan(&f.Ref, &f.RootPath, &backup); err != nil {
			return nil, fmt.Errorf("read registered folders: %w", err)
		}
		f.Backup = !backup.Valid || backup.Bool
		out = append(out, f)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read registered folders: %w", err)
	}
	return out, nil
}
