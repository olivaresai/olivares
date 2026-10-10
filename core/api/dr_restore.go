// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/olivaresai/olivares/core/dr"
	"github.com/olivaresai/olivares/core/secure"
)

// consoleRestoreStage keeps private key files on the destination filesystem.
// The database is promoted through SQLite, preserving its writer-lock and WAL
// guarantees; key renames are atomic and a failed promotion restores both sides.
type consoleRestoreStage struct {
	dir      string
	keys     []string
	previous map[string]bool
}

func stageConsoleRestoreKeys(dataDir, work string, m *dr.Manifest, cipher *dr.KeyCipher) (*consoleRestoreStage, error) {
	if err := secure.EnsureDataDir(dataDir); err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp(dataDir, ".dr-staging-")
	if err != nil {
		return nil, err
	}
	stage := &consoleRestoreStage{dir: dir, previous: map[string]bool{}}
	ok := false
	defer func() {
		if !ok {
			_ = os.RemoveAll(dir)
		}
	}()
	for _, sub := range []string{"keys", "previous"} {
		if err := os.Mkdir(filepath.Join(dir, sub), 0700); err != nil {
			return nil, err
		}
	}
	seen := map[string]bool{}
	for _, kr := range m.Keys {
		if err := validRestoredKeyName(kr.Name); err != nil {
			return nil, err
		}
		if seen[kr.Name] {
			return nil, fmt.Errorf("duplicate bundle key %s", kr.Name)
		}
		seen[kr.Name] = true
		live := filepath.Join(dataDir, kr.Name)
		info, err := os.Lstat(live)
		switch {
		case os.IsNotExist(err):
		case err != nil:
			return nil, fmt.Errorf("inspect key %s: %w", kr.Name, err)
		case !info.Mode().IsRegular():
			return nil, fmt.Errorf("key destination %s is not a regular file; live data and keys were not replaced", kr.Name)
		default:
			if err := dr.CopyFile(live, filepath.Join(dir, "previous", kr.Name)); err != nil {
				return nil, fmt.Errorf("stage previous key %s: %w", kr.Name, err)
			}
			stage.previous[kr.Name] = true
		}
		sealed, err := os.ReadFile(filepath.Join(work, filepath.FromSlash(kr.File)))
		if err != nil {
			return nil, fmt.Errorf("read sealed key %s: %w", kr.Name, err)
		}
		plain, err := cipher.Open(sealed)
		if err != nil {
			return nil, fmt.Errorf("decrypt key %s: %w", kr.Name, err)
		}
		err = os.WriteFile(filepath.Join(dir, "keys", kr.Name), plain, 0600)
		clear(plain)
		if err != nil {
			return nil, fmt.Errorf("stage key %s: %w", kr.Name, err)
		}
		stage.keys = append(stage.keys, kr.Name)
	}
	ok = true
	return stage, nil
}

// preserve uses a SQLite snapshot, including committed WAL data, rather than
// copying the live database file. Copies remain available even after rollback.
func (stage *consoleRestoreStage) preserve(ctx context.Context, dataDir, suffix string) error {
	dbCopy := filepath.Join(stage.dir, "previous.db")
	if err := dr.SnapshotSQLite(ctx, filepath.Join(dataDir, "olivares.db"), dbCopy); err != nil {
		return err
	}
	if err := dr.CopyFile(dbCopy, filepath.Join(dataDir, "olivares.db")+suffix); err != nil {
		return err
	}
	for _, name := range stage.keys {
		if stage.previous[name] {
			if err := dr.CopyFile(filepath.Join(stage.dir, "previous", name), filepath.Join(dataDir, name)+suffix); err != nil {
				return fmt.Errorf("preserve key %s: %w", name, err)
			}
		}
	}
	return nil
}

func (stage *consoleRestoreStage) promoteKeys(dataDir string) (int, error) {
	for i, name := range stage.keys {
		if err := os.Rename(filepath.Join(stage.dir, "keys", name), filepath.Join(dataDir, name)); err != nil {
			return i, fmt.Errorf("promote key %s: %w", name, err)
		}
	}
	return len(stage.keys), nil
}

func (stage *consoleRestoreStage) rollback(dataDir string, promotedKeys int) error {
	// Recovery must finish even if the original operation's context expired.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var errs []error
	for _, name := range stage.keys[:promotedKeys] {
		path := filepath.Join(dataDir, name)
		var err error
		if stage.previous[name] {
			err = os.Rename(filepath.Join(stage.dir, "previous", name), path)
		} else {
			err = os.Remove(path)
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("rollback key %s: %w", name, err))
		}
	}
	if err := promoteSQLiteSnapshot(ctx, filepath.Join(stage.dir, "previous.db"), filepath.Join(dataDir, "olivares.db")); err != nil {
		errs = append(errs, fmt.Errorf("rollback store: %w", err))
	}
	return errors.Join(errs...)
}
