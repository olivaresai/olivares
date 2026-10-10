// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/olivaresai/olivares/core/model"
)

// With an API key the provider refused, Claude Code retried
// the call up to ten times and the turn failed only at the end. A refused credential
// does not change between attempts, so the engine interrupts the turn on the first
// such api_retry and records why, with what fixes it. The outcome is the one the
// retries would have reached, sooner. Other causes keep Claude Code's own retries.

// credentialRefusedActor is who interrupts: the engine, not a person.
const credentialRefusedActor = "engine:credential-refused"

// credentialRefusedReason is the run's reason after the interrupt.
func credentialRefusedReason(status int) string {
	return fmt.Sprintf("the provider refused the credential (HTTP %d): replace the API key in AI tools › API keys, "+
		"or sign the tool in (olivares tool login claude)", status)
}

// beginCredentialRefusal reports whether this turn has not been interrupted for a
// refused credential yet, and which turn that is; endCredentialRefusal clears it
// when the turn ends and counts the ended turn.
func (lr *liveRun) beginCredentialRefusal() (uint64, bool) {
	lr.mu.Lock()
	defer lr.mu.Unlock()
	if lr.credentialRefused {
		return 0, false
	}
	lr.credentialRefused = true
	return lr.claudeTurnsEnded, true
}

func (lr *liveRun) endCredentialRefusal() {
	lr.mu.Lock()
	lr.credentialRefused = false
	lr.claudeTurnsEnded++
	lr.mu.Unlock()
}

// credentialRefusalCurrent reports whether the turn the refusal was raised for is
// still the turn running: no result has ended it since.
func (lr *liveRun) credentialRefusalCurrent(turn uint64) bool {
	lr.mu.Lock()
	defer lr.mu.Unlock()
	return lr.credentialRefused && lr.claudeTurnsEnded == turn
}

// sendIfRefusalCurrent writes the control frame only while the refused turn is still
// the turn running, decided under lr.mu: the lock under which the bridge ends turns
// (endCredentialRefusal) and counts queued ones, so no result can fall between the
// check and the frame. False: the work is stale and nothing was written.
//
// The same decision closes call admission and takes the calls to
// cancel, which are the refused turn's own. A result read after it reopens admission
// for an accepted successor, and nothing of the refusal closes it again.
func (lr *liveRun) sendIfRefusalCurrent(ctx context.Context, turn uint64, line []byte) ([]*runtimeSessionCall, bool, error) {
	lr.mu.Lock()
	defer lr.mu.Unlock()
	if !lr.credentialRefused || lr.claudeTurnsEnded != turn {
		return nil, false, nil
	}
	if err := lr.proc.Send(ctx, line); err != nil {
		return nil, true, err
	}
	lr.sessionCallsEnded = true
	return slices.Clone(lr.sessionCalls), true, nil
}

// stopTurnOnRefusedCredential interrupts the turn the way an operator's interrupt
// does, under the run's operation lock, with the reason on the run. A run under an
// active work lease is left to its fenced plane.
//
// The work waits for the run lock, and meanwhile the refused turn
// can end and an accepted input start the next one. The work is bound to the
// launch's live handle and to the turn it was raised for, both rechecked under the
// lock; stale work is dropped before any ledger write or control frame.
func (m *Module) stopTurnOnRefusedCredential(lr *liveRun, status int, turn uint64) {
	if status == 0 {
		status = 401
	}
	ctx, cancel := context.WithTimeout(context.Background(), claudeInterruptWait+10*time.Second)
	defer cancel()
	release, err := m.rt.lockRunContext(ctx, liveKey(lr.tenant, lr.runRef))
	if err != nil {
		return
	}
	defer release()
	if live, ok := m.rt.getLive(lr.tenant, lr.runRef); !ok || live != lr || !lr.credentialRefusalCurrent(turn) {
		return
	}
	rec, err := m.loadRun(ctx, lr.tenant, lr.runRef)
	if err != nil {
		return
	}
	if m.refuseUnfencedActiveWork(ctx, lr.tenant, rec) != nil {
		return
	}
	if err := m.interruptRefusedTurn(ctx, lr, rec, credentialRefusedReason(status), turn); err != nil {
		m.warnf("session turn with a refused credential was not interrupted", "run_ref", lr.runRef)
	}
}

// interruptRefusedTurn is interruptClaudeTurn for the engine's own interrupt, with the
// turn re-proved at the control boundary: the frame is written only
// while the refused turn still runs, decided with the bridge's turn accounting, and the
// ledger is written after it. A turn that ended meanwhile (a result read while the row
// was loading) leaves no ledger row and no frame, so an accepted successor is never
// interrupted for its predecessor's refusal.
func (m *Module) interruptRefusedTurn(ctx context.Context, lr *liveRun, rec model.Record, reason string, turn uint64) error {
	driver := rec.String(colRunProfileDriver)
	if rec.String(colState) != stateRunning || lr.session != nil ||
		Transport(rec.String(colTransport)) != TransportStreamJSON || (driver != "" && driver != providerDriverClaude) {
		return nil
	}
	if err := m.assertRunAuthority(ctx, lr); err != nil {
		return err
	}
	id := "olv-interrupt-" + string(model.NewID())
	line, err := json.Marshal(map[string]any{
		"type":       "control_request",
		"request_id": id,
		"request":    map[string]any{"subtype": "interrupt"},
	})
	if err != nil {
		return err
	}
	cursor := lr.ring.cursorNow()
	calls, sent, err := lr.sendIfRefusalCurrent(ctx, turn, line)
	if !sent {
		return nil
	}
	// Once the frame is sent the attempt and its reason are recorded
	// at once, whatever Claude Code answers, and the answer is recorded as it came:
	// confirmed ("interrupted"), or not confirmed with why, never a claimed completion.
	record := func(event, detail string) error {
		// The write and the wait for its answer can spend ctx, so each
		// outcome is recorded under its own short bound, still launch- and claim-guarded.
		rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_, err := m.transition(rctx, lr.tenant, lr.runRef, transitionInput{
			event: event, actor: credentialRefusedActor, actorKind: model.ActorSystem,
			detail: interruptionDetail(detail, reason),
			lease:  lr.claim, guard: guardRuntimeLaunch(lr.launchID),
		})
		return err
	}
	if err != nil {
		// A write that failed is recorded with what Process.Send said
		// happened to the bytes (not attempted, or possibly in part) and the reason.
		return errors.Join(err, record("interrupting", "provider turn interruption write failed: "+clipCause(err.Error())))
	}
	sentErr := record("interrupting", "provider turn interruption sent")
	callErr := cancelCalls(calls, "")
	if ackErr := m.awaitClaudeControlResponse(ctx, lr.ring, cursor, id); ackErr != nil {
		return errors.Join(sentErr, callErr, ackErr,
			record("interrupting", "provider turn interruption not confirmed: "+clipCause(ackErr.Error())))
	}
	return errors.Join(sentErr, callErr, record("interrupted", "provider turn interrupted; the owned process stays live"))
}
