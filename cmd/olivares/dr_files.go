// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/olivaresai/olivares/core/dr"
	"github.com/olivaresai/olivares/modules/sessions"
)

// drDataTrees are the data-directory subtrees a backup carries: the tools' own
// sign-ins and homes, the provider account homes, the installed tools and the skill
// homes of the sessions. A subtree the host does not have is skipped.
var drDataTrees = []string{"profile-homes", toolLoginsDir, "tools", "session-skill-homes", "accounts", "gitpublish", "skills"}

// drDataTreesLeftOut are subtrees that exist but are deliberately not carried.
var drDataTreesLeftOut = []struct{ name, reason string }{
	{"model-servers", "model files that download again"},
	{"ollama", "model files that download again"},
}

// drTreeSources lists what a backup carries beside the store: the data
// directory's subtrees and every registered folder not registered with
// backup: false, read from the snapshot the bundle holds. Postgres keeps the
// folder registry in the database, which this read does not open, so it says so.
func drTreeSources(ctx context.Context, dataDir, snapshot string, sqliteStore bool) ([]dr.TreeSource, []dr.NotIncluded, error) {
	abs, err := filepath.Abs(dataDir)
	if err != nil {
		return nil, nil, err
	}
	var sources []dr.TreeSource
	var left []dr.NotIncluded
	for _, name := range drDataTrees {
		sources = append(sources, dr.TreeSource{Kind: dr.TreeKindData, Name: name, Path: filepath.Join(abs, name)})
	}
	for _, l := range drDataTreesLeftOut {
		if _, err := os.Stat(filepath.Join(abs, l.name)); err == nil {
			left = append(left, dr.NotIncluded{Kind: dr.TreeKindData, Name: l.name, Path: filepath.Join(abs, l.name), Reason: l.reason})
		}
	}
	if !sqliteStore {
		left = append(left, dr.NotIncluded{Kind: dr.TreeKindFolder, Name: "registered-folders", Path: abs,
			Reason: "the folder registry of a PostgreSQL store is not read by the backup; copy the registered folders separately"})
		return sources, left, nil
	}
	folders, err := registeredFolders(ctx, snapshot)
	if err != nil {
		return nil, nil, err
	}
	for _, f := range folders {
		if !f.Backup {
			left = append(left, dr.NotIncluded{Kind: dr.TreeKindFolder, Name: f.Ref, Path: f.RootPath, Reason: "registered with backup: false"})
			continue
		}
		sources = append(sources, dr.TreeSource{Kind: dr.TreeKindFolder, Name: f.Ref, Path: f.RootPath})
	}
	return sources, left, nil
}

func registeredFolders(ctx context.Context, snapshot string) (_ []sessions.BackupFolder, err error) {
	u := url.URL{Scheme: "file", Path: snapshot, RawQuery: "mode=ro&immutable=1"}
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, fmt.Errorf("read registered folders: %w", err)
	}
	defer func() {
		if cerr := db.Close(); err == nil && cerr != nil {
			err = fmt.Errorf("read registered folders: %w", cerr)
		}
	}()
	return sessions.ListBackupFolders(ctx, db)
}

// restoreTreeFiles writes the bundle's files once the store is restored and
// proven. An in-place restore first moves the live data subtrees aside, as it does
// the store and the keys, so nothing of the previous state is lost.
func restoreTreeFiles(cmd *cobra.Command, cipher *dr.KeyCipher, work string, targets []dr.TreeTarget, inPlace bool, ts string) error {
	if len(targets) == 0 {
		return nil
	}
	if inPlace {
		if err := dr.PreserveDataTrees(targets, ".pre-restore-"+ts); err != nil {
			return err
		}
	}
	rep, err := dr.RestoreTrees(cipher, work, targets)
	if err != nil {
		return fmt.Errorf("the store is restored but its files are not: %w", err)
	}
	drNote(cmd, fmt.Sprintf("restored %d files (%d bytes) from %d folders", rep.Files, rep.Bytes, len(targets)))
	if rep.OwnerSkipped > 0 || rep.ACLSkipped > 0 {
		drNote(cmd, fmt.Sprintf("note: %d owners and %d ACLs were not restored (run the restore as root on a filesystem with ACLs to keep them)", rep.OwnerSkipped, rep.ACLSkipped))
	}
	return nil
}

// drTreesSummary is what a backup prints about the files it carried, or "" when it
// carried no file and left no registered folder out, so a backup without files
// prints what it always did.
func drTreesSummary(m *dr.Manifest) string {
	files, bytes, changed, leftFolders := 0, int64(0), []string{}, 0
	for _, t := range m.Trees {
		files += len(t.Files)
		for _, f := range t.Files {
			bytes += f.SizeBytes
			if f.Changed {
				changed = append(changed, t.ID+"/"+f.Path)
			}
		}
	}
	for _, n := range m.NotIncluded {
		if n.Kind == dr.TreeKindFolder {
			leftFolders++
		}
	}
	if files == 0 && leftFolders == 0 {
		return ""
	}
	line := fmt.Sprintf("  files: %d in %d folders (%d bytes)  left out: %d", files, len(m.Trees), bytes, len(m.NotIncluded))
	if len(changed) > 0 {
		if len(changed) > 3 {
			changed = changed[:3]
		}
		line += fmt.Sprintf("\n  note: files were written to while they were copied (%s ...); back up again with the sessions paused for a clean copy", strings.Join(changed, ", "))
	}
	return line
}
