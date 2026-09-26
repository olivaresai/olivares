// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package base

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"

	"github.com/olivaresai/olivares/appliance/answers/carriers"
)

// StateDir holds the first-boot record. The unit creates it (StateDirectory=) under
// /var/lib, which an appliance image keeps outside the system snapshot subvolume so a
// rollback never makes first boot repeat.
const StateDir = "/var/lib/olivares-appliance"

const (
	recordFile     = "state.json"
	readyFile      = "ready"
	lockFile       = "apply.lock"
	recordSchema   = "olivares-appliance-firstboot/v1"
	maxRecordBytes = 256 * 1024
)

// Record is the persisted first-boot status: evidence of the last completed observation,
// not proof of current service health.
type Record struct {
	Schema    string      `json:"schema"`
	State     State       `json:"state"`
	Stage     Stage       `json:"stage,omitempty"`
	Reason    string      `json:"reason,omitempty"`
	Source    string      `json:"source,omitempty"`
	Digest    string      `json:"input_digest,omitempty"`
	Completed []Completed `json:"completed"`
	Observed  string      `json:"observed_at"`
	// ProductMayHaveStarted is set in the same atomic save as Applying@start-services, before
	// the product's start can be queued, and nothing clears it: whatever a later run records,
	// the product's store and keys are then this instance's, not an imported installation's.
	// The field is additive within schema v1; a record written without it has it derived when
	// read from Applying@start-services or a completed start-services, never from a refusal
	// recorded at start-services, and the next save writes it.
	ProductMayHaveStarted bool `json:"product_may_have_started,omitempty"`
}

// Completed is one stage whose effect was applied and persisted.
type Completed struct {
	Stage  Stage  `json:"stage"`
	Effect Effect `json:"effect"`
}

// Store persists the record in a directory only the applying account can write.
type Store struct{ Dir string }

// SchemaError refuses a record another version of first boot wrote. The migration rule: a
// version rewrites only records of its own schema; a newer version migrates older records
// forward when it reads them, and an older version refuses a newer record, by name, until the
// newer package is installed again. The record is never rewritten or removed here.
type SchemaError struct{ Found string }

func (e *SchemaError) Error() string {
	return fmt.Sprintf("the first-boot record has schema %q; this version reads %s and leaves it in place",
		e.Found, recordSchema)
}

// Load reads the record. A missing record reports false; a record that is not a protected
// regular file, does not parse, or has another schema is an error and is left untouched.
func (s Store) Load() (Record, bool, error) {
	data, ok, err := carriers.ReadProtected(filepath.Join(s.Dir, recordFile), maxRecordBytes)
	if err != nil || !ok {
		return Record{}, ok, err
	}
	var r Record
	if err := json.Unmarshal(data, &r); err != nil {
		return Record{}, false, errors.New("the first-boot record is unreadable; it was left in place")
	}
	if r.Schema != recordSchema {
		return Record{}, false, &SchemaError{Found: r.Schema}
	}
	if _, started := effectOf(r, StageStartServices); started || (r.Stage == StageStartServices && r.State == Applying) {
		r.ProductMayHaveStarted = true
	}
	return r, true, nil
}

// Save replaces the record atomically.
func (s Store) Save(r Record) error {
	if r.Completed == nil {
		r.Completed = []Completed{}
	}
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(s.Dir, recordFile, append(data, '\n'), 0o600)
}

// MarkReady writes the marker the unit's ConditionPathExists= tests, after the ready
// record: a run interrupted between the two finds the ready record and writes the marker.
func (s Store) MarkReady(r Record) error {
	return writeAtomic(s.Dir, readyFile, []byte(r.Observed+"\n"), 0o600)
}

// Lock takes the instance-local lock that serializes apply. The lock file is created once
// and never renamed or removed, so every run locks the same inode.
func (s Store) Lock() (func(), error) {
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return nil, err
	}
	info, err := os.Lstat(s.Dir)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode().Perm()&0o022 != 0 {
		return nil, errors.New(s.Dir + " is not a directory only its owner can write")
	}
	f, err := os.OpenFile(filepath.Join(s.Dir, lockFile), os.O_RDWR|os.O_CREATE|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		return nil, errors.New("another first-boot run holds the lock")
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}, nil
}

// writeAtomic replaces dir/name with data: a temporary file in the same directory is
// written, synced and renamed over the target, then the directory is synced so the new
// entry survives a power loss. A reader sees the old content or the new, never a part.
func writeAtomic(dir, name string, data []byte, mode os.FileMode) error {
	f, err := os.CreateTemp(dir, "."+name+".*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp) // a no-op once the rename succeeded
	if err := f.Chmod(mode); err != nil {
		_ = f.Close()
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, filepath.Join(dir, name)); err != nil {
		return err
	}
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
