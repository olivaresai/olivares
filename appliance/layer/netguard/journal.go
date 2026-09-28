// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package netguard

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"syscall"
)

// Bounds never evict a pending identity or reuse a consumed operation id. Once the
// retained-window bound is reached new operations refuse until explicit maintenance.
const (
	MaxCallsPerWindow = 64
	MaxWindows        = 1024
	MaxWindowBytes    = 128 * 1024
)

var ErrJournalFull = errors.New("network_journal_capacity_exhausted")

type Journal struct{ dir string }

func OpenJournal(dir string) (*Journal, error) {
	if err := os.Mkdir(dir, 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return nil, err
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return nil, err
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.IsDir() || info.Mode().Perm() != 0700 || int(st.Uid) != os.Geteuid() {
		return nil, errors.New("network_journal_custody_refused")
	}
	return &Journal{dir: dir}, nil
}
func (j *Journal) Load(id string) (Window, error) {
	var w Window
	if !validID(id) {
		return w, errors.New("network_operation_id_refused")
	}
	f, err := os.OpenFile(filepath.Join(j.dir, id+".json"), os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return w, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return w, err
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || int(st.Uid) != os.Geteuid() || st.Nlink != 1 {
		return w, errors.New("network_journal_custody_refused")
	}
	b, err := io.ReadAll(io.LimitReader(f, MaxWindowBytes+1))
	if err != nil {
		return w, err
	}
	if len(b) > MaxWindowBytes {
		return w, ErrJournalFull
	}
	if err := json.Unmarshal(b, &w); err != nil {
		return w, err
	}
	if w.Schema != JournalSchema || w.OperationID != id || len(w.Calls) > MaxCallsPerWindow || len(w.RestoreAttempts) > MaxCallsPerWindow/2 {
		return w, errors.New("network_journal_schema_refused")
	}
	return w, nil
}
func (j *Journal) All() ([]Window, error) {
	files, err := os.ReadDir(j.dir)
	if err != nil {
		return nil, err
	}
	var windows []Window
	for _, f := range files {
		name := f.Name()
		if len(name) > 0 && name[0] == '.' {
			continue
		}
		if len(name) != 37 || name[32:] != ".json" {
			return nil, errors.New("network_journal_entry_refused")
		}
		w, err := j.Load(name[:32])
		if err != nil {
			return nil, err
		}
		windows = append(windows, w)
		if len(windows) > MaxWindows {
			return nil, ErrJournalFull
		}
	}
	return windows, nil
}
func (j *Journal) Create(w Window) error {
	all, err := j.All()
	if err != nil {
		return err
	}
	if len(all) >= MaxWindows {
		return ErrJournalFull
	}
	for _, existing := range all {
		if existing.OperationID == w.OperationID {
			return errors.New("network_operation_id_consumed")
		}
	}
	return j.write(w, true)
}
func (j *Journal) Save(w Window) error {
	if _, err := j.Load(w.OperationID); err != nil {
		return err
	}
	return j.write(w, false)
}
func (j *Journal) write(w Window, create bool) error {
	if !validID(w.OperationID) || w.Schema != JournalSchema {
		return errors.New("network_journal_schema_refused")
	}
	if len(w.Calls) > MaxCallsPerWindow || len(w.RestoreAttempts) > MaxCallsPerWindow/2 {
		return ErrJournalFull
	}
	b, err := json.Marshal(w)
	if err != nil {
		return err
	}
	if len(b) > MaxWindowBytes {
		return ErrJournalFull
	}
	f, err := os.CreateTemp(j.dir, ".window-")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	defer f.Close()
	if _, err = f.Write(b); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	target := filepath.Join(j.dir, w.OperationID+".json")
	if create {
		if err = os.Link(name, target); err != nil {
			return err
		}
		if err = os.Remove(name); err != nil {
			return err
		}
	} else {
		if err = os.Rename(name, target); err != nil {
			return err
		}
	}
	dir, err := os.Open(j.dir)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
