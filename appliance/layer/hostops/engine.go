// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package hostops

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
)

// Engine is the operation read model for one records directory. Two engines
// opened on the same directory see the same records.
type Engine struct {
	dir     string
	mu      sync.Mutex
	catalog *Catalog
}

// Open opens the records directory, creating it if needed. The directory is
// chosen by the caller of Open, not by a field of a command.
func Open(dir string) (*Engine, error) {
	catalog, err := NewCatalog([]Descriptor{StatusDescriptor()})
	if err != nil {
		return nil, err
	}
	return OpenWithCatalog(dir, catalog)
}

// OpenWithCatalog binds new claims to a module owner's validated, closed task
// catalog. Existing identifiers remain retrievable after a catalog change.
func OpenWithCatalog(dir string, catalog *Catalog) (*Engine, error) {
	if catalog == nil {
		return nil, errors.New("task catalog unavailable")
	}

	if dir == "" || strings.Contains(dir, "\x00") {
		return nil, errors.New("the records directory is not a local directory")
	}
	if !filepath.IsAbs(dir) && !filepath.IsLocal(dir) {
		return nil, errors.New("the records directory is not a local directory")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	lock := filepath.Join(dir, "store.lock")
	f, err := os.OpenFile(lock, os.O_RDWR|os.O_CREATE|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, err
	}
	if err := f.Close(); err != nil {
		return nil, err
	}
	return &Engine{dir: dir, catalog: catalog}, nil
}

type diskRecord struct {
	Record    Record `json:"record"`
	RequestID string `json:"consumer_request_id,omitempty"`
}

// Submit records cmd and runs effect once. A later submit of the same operation
// id with the same plan returns the record; a changed plan refuses. effect's error
// text is not stored. The returned status is the transport status of this
// record.
func (e *Engine) Submit(cmd Command, effect func() error) (Record, int, error) {
	rec, status, claimed, err := e.claim(cmd)
	if err != nil || !claimed {
		return rec, status, err
	}
	if effect != nil {
		_ = effect()
	}
	return rec, status, nil
}

func (e *Engine) claim(cmd Command) (Record, int, bool, error) {
	if !isHex(cmd.OperationID, 32) {
		return Record{}, StatusRefused, false, refuse("operation_id")
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	release, err := e.hold()
	if err != nil {
		return Record{}, 0, false, err
	}
	defer release()

	final := e.recordPath(cmd.OperationID)
	if rec, ok, err := e.read(final); err != nil {
		return Record{}, 0, false, err
	} else if ok {
		if !samePlan(cmd, rec) {
			return Record{}, StatusLocked, false, &PlanChanged{}
		}
		return rec, statusFor(rec), false, nil
	}
	if err := validate(cmd); err != nil {
		return Record{}, StatusRefused, false, err
	}
	if _, ok := e.catalog.entries[cmd.Module+"."+cmd.Verb]; !ok {
		return Record{}, StatusRefused, false, refuse("task")
	}
	if holder, busy, err := e.conflicting(cmd.Target, cmd.OperationID); err != nil {
		return Record{}, 0, false, err
	} else if busy {
		return Record{}, StatusLocked, false, &TargetLocked{OperationID: holder.OperationID, State: holder.State}
	}
	rec := Record{
		OperationID: cmd.OperationID,
		TaskID:      cmd.TaskID,
		Module:      cmd.Module,
		Verb:        cmd.Verb,
		Target:      cmd.Target,
		PlanDigest:  cmd.PlanDigest,
		Mode:        cmd.Mode,
		Surface:     cmd.Surface,
		Actor:       cmd.Actor,
		State:       StateRunning,
		Outcome:     OutcomeUnknown,
		LogRef:      logRef(cmd.OperationID),
	}
	if err := e.create(final, diskRecord{Record: rec}); err != nil {
		if errors.Is(err, os.ErrExist) {
			got, ok, rerr := e.read(final)
			if rerr != nil {
				return Record{}, 0, false, rerr
			}
			if ok {
				if !samePlan(cmd, got) {
					return Record{}, StatusLocked, false, &PlanChanged{}
				}
				return got, statusFor(got), false, nil
			}
		}
		return Record{}, 0, false, err
	}
	return rec, statusFor(rec), true, nil
}

// Get reads the operation recorded under id. It does not run an effect.
// A value that is not an operation id is refused without being repeated.
func (e *Engine) Get(id string) (Record, int, error) {
	if !isHex(id, 32) {
		return Record{}, StatusRefused, refuse("operation_id")
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	release, err := e.hold()
	if err != nil {
		return Record{}, 0, err
	}
	defer release()
	rec, ok, err := e.read(e.recordPath(id))
	if err != nil {
		return Record{}, 0, err
	}
	if !ok {
		return Record{}, 0, errors.New("no such operation")
	}
	return rec, statusFor(rec), nil
}

func (e *Engine) recordPath(id string) string {
	return filepath.Join(e.dir, id+".json")
}

// conflicting reports an unfinished operation on target other than self.
func (e *Engine) conflicting(target, self string) (Record, bool, error) {
	entries, err := os.ReadDir(e.dir)
	if err != nil {
		return Record{}, false, err
	}
	for _, entry := range entries {
		name := entry.Name()
		id, ok := strings.CutSuffix(name, ".json")
		if !ok || id == self || !isHex(id, 32) {
			continue
		}
		rec, found, err := e.read(e.recordPath(id))
		if err != nil {
			return Record{}, false, err
		}
		if found && rec.Target == target && !terminal(rec.State) {
			return rec, true, nil
		}
	}
	return Record{}, false, nil
}

func (e *Engine) hold() (func(), error) {
	f, err := os.OpenFile(filepath.Join(e.dir, "store.lock"), os.O_RDWR|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	if err := lockFile(f); err != nil {
		_ = f.Close()
		return nil, err
	}
	return func() {
		_ = unlockFile(f)
		_ = f.Close()
	}, nil
}

func (e *Engine) read(final string) (Record, bool, error) {
	f, err := os.OpenFile(final, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if errors.Is(err, os.ErrNotExist) {
		return Record{}, false, nil
	}
	if err != nil {
		return Record{}, false, err
	}
	defer f.Close()
	var stored diskRecord
	dec := json.NewDecoder(f)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&stored); err != nil {
		return Record{}, false, errors.New("an operation record is unreadable")
	}
	if dec.Decode(new(any)) != io.EOF || !isHex(stored.Record.OperationID, 32) || filepath.Base(final) != stored.Record.OperationID+".json" || !validRecord(stored.Record) {
		return Record{}, false, errors.New("an operation record is unreadable")
	}
	if stored.RequestID != "" && (!isHex(stored.RequestID, 32) || stored.RequestID == stored.Record.OperationID) {
		return Record{}, false, errors.New("an operation record is unreadable")
	}
	stored.Record.consumerRequestID = stored.RequestID
	return stored.Record, true, nil
}

func (e *Engine) create(final string, stored diskRecord) error {
	f, err := os.OpenFile(final, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(f)
	enc.SetEscapeHTML(false)
	writeErr := enc.Encode(stored)
	syncErr := f.Sync()
	closeErr := f.Close()
	if writeErr != nil || syncErr != nil || closeErr != nil {
		_ = os.Remove(final)
		if writeErr != nil {
			return writeErr
		}
		if syncErr != nil {
			return syncErr
		}
		return closeErr
	}
	dir, err := os.Open(e.dir)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
