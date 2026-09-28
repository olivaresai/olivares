// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package hostops

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"syscall"
)

// Observation is what recovery measured. It is not an instruction to run the
// effect again. P1 names a receipt outcome or the terminal journal outcome abandoned.
type Observation struct {
	P1             string
	MeasuredChange bool
	// PendingEffect annotates unresolved work; terminal outcomes clear it.
	PendingEffect  bool
	Postconditions []string
	// RevertDefined is true only where the module defines a revert.
	RevertDefined bool
	// RevertMeasured is true when that revert was measured.
	RevertMeasured bool
}

// Recover applies obs to the recorded operation and writes the result. It
// never runs an effect. rolled_back is recorded only when a revert is defined,
// measured, and accompanied by postconditions.
func (e *Engine) Recover(id string, obs Observation) (Record, error) {
	return e.recover(id, obs, "")
}

func (e *Engine) recover(id string, obs Observation, requestID string) (Record, error) {
	if !isHex(id, 32) {
		return Record{}, refuse("operation_id")
	}
	state, outcome, err := mapObservation(obs)
	if err != nil {
		return Record{}, err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	release, err := e.hold()
	if err != nil {
		return Record{}, err
	}
	defer release()
	final := e.recordPath(id)
	rec, ok, err := e.read(final)
	if err != nil {
		return Record{}, err
	}
	if !ok {
		return Record{}, errors.New("no such operation")
	}
	bindingChanged := false
	if requestID != "" {
		if rec.consumerRequestID != "" && rec.consumerRequestID != requestID {
			return Record{}, errors.New("operation journal mismatch")
		}
		available, err := e.requestAvailable(id, requestID)
		if err != nil {
			return Record{}, err
		}
		if !available {
			return Record{}, errors.New("operation journal mismatch")
		}
		bindingChanged = rec.consumerRequestID == ""
		rec.consumerRequestID = requestID
	}
	if terminal(rec.State) {
		if state != rec.State {
			return Record{}, errors.New("operation state is final")
		}
		if rec.EffectPending || bindingChanged {
			rec.EffectPending = false
			if err := e.rewrite(final, rec); err != nil {
				return Record{}, err
			}
		}
		return rec, nil
	}
	rec.EffectPending = obs.PendingEffect && !terminal(state)
	rec.State = state
	rec.Outcome = outcome
	if obs.P1 == "abandoned" {
		rec.Reason = "abandoned"
	}
	if len(obs.Postconditions) == 0 {
		rec.Postconditions = nil
	} else {
		rec.Postconditions = append([]string(nil), obs.Postconditions...)
	}
	if err := e.rewrite(final, rec); err != nil {
		return Record{}, err
	}
	return rec, nil
}

func mapObservation(obs Observation) (string, string, error) {
	if len(obs.Postconditions) > 64 {
		return "", "", refuse("postconditions")
	}
	for _, fact := range obs.Postconditions {
		if !plainText(fact) {
			return "", "", refuse("postconditions")
		}
	}

	// A definite receipt wins over annotations from the effect/revert observer.
	// Only performed can be refined by a measured revert or unfinished work.
	switch obs.P1 {
	case "unknown":
		return StateRunning, OutcomeUnknown, nil
	case "not-performed":
		return StateFailed, "", nil
	case "partial":
		return StatePartial, "", nil
	case "failed", "abandoned":
		if obs.MeasuredChange {
			return StatePartial, "", nil
		}
		return StateFailed, "", nil
	case "performed":
	default:
		return "", "", refuse("outcome")
	}
	if obs.PendingEffect {
		return StatePartial, "", nil
	}
	if obs.RevertMeasured {
		if !obs.RevertDefined {
			return "", "", refuse("revert")
		}
		if len(obs.Postconditions) > 0 {
			return StateRolledBack, "", nil
		}
		return StatePartial, "", nil
	}
	// A defined but unmeasured revert is only a module capability.
	if len(obs.Postconditions) > 0 {
		return StateSucceeded, "", nil
	}
	return StatePartial, "", nil
}

func (e *Engine) rewrite(final string, rec Record) error {
	tmp := final + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(f)
	enc.SetEscapeHTML(false)
	writeErr := enc.Encode(diskRecord{Record: rec, RequestID: rec.consumerRequestID})
	syncErr := f.Sync()
	closeErr := f.Close()
	if writeErr != nil || syncErr != nil || closeErr != nil {
		_ = os.Remove(tmp)
		if writeErr != nil {
			return writeErr
		}
		if syncErr != nil {
			return syncErr
		}
		return closeErr
	}
	if err := os.Rename(tmp, final); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	dir, err := os.Open(e.dir)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

// JournalReader is supplied by the module that owns the existing consumer journal
// and host measurements. It has no operation that can issue or retry an effect.
type JournalReader interface {
	ReadOperation(operationID string) (JournalEntry, error)
}

// JournalEntry correlates a consumer receipt with the client operation. RequestID
// is minted independently by the consumer and is not added to the surface record.
type JournalEntry struct {
	OperationID string
	RequestID   string
	PlanDigest  string
	Observation Observation
}

// Reconcile refreshes the read model from its owning journal. An absent journal,
// mismatched receipt or unreadable host state never proves an operation finished.
func (e *Engine) Reconcile(id string, journal JournalReader) (Record, error) {
	if journal == nil {
		return Record{}, errors.New("operation journal unavailable")
	}
	rec, _, err := e.Get(id)
	if err != nil {
		return Record{}, err
	}
	entry, err := journal.ReadOperation(id)
	if err != nil {
		return Record{}, errors.New("operation journal unavailable")
	}
	if entry.OperationID != id || entry.PlanDigest != rec.PlanDigest || !isHex(entry.RequestID, 32) || entry.RequestID == id {
		return Record{}, errors.New("operation journal mismatch")
	}
	return e.recover(id, entry.Observation, entry.RequestID)
}

// requestAvailable runs under the same store lock as the record rewrite. The
// correlation is projection metadata, not a replacement for the consumer ledger.
func (e *Engine) requestAvailable(id, requestID string) (bool, error) {
	entries, err := os.ReadDir(e.dir)
	if err != nil {
		return false, err
	}
	for _, entry := range entries {
		other, ok := strings.CutSuffix(entry.Name(), ".json")
		if !ok || other == id || !isHex(other, 32) {
			continue
		}
		rec, found, err := e.read(e.recordPath(other))
		if err != nil {
			return false, err
		}
		if found && rec.consumerRequestID == requestID {
			return false, nil
		}
	}
	return true, nil
}
