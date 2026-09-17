// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// P1 — durable terminal observations for a runtime generation.
//
// The lifecycle state and the process fact are different facts. A run row can be
// terminal while nothing confirmed the process died, and until now the ledger could
// not say which of those happened, nor which generation was retired: both terminal
// writers null `runtime_launch_id` inside their own mutation, three lines before the
// event is sealed. These cases drive the real producers and read what actually landed.

// independentPayloadHash is a SECOND implementation of the canonical encoding,
// written from the specification rather than from the function under test: decimal
// UTF-8 byte length, a colon, the bytes; SHA-256 over the concatenation. A vector
// produced by the helper it is meant to check proves nothing.
func independentPayloadHash(parts ...string) string {
	h := sha256.New()
	for _, part := range parts {
		h.Write([]byte(strconv.Itoa(len(part))))
		h.Write([]byte{':'})
		h.Write([]byte(part))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// waitErrProc is a Process whose Wait fails. procProcess maps an ordinary non-zero
// exit to (code, nil); only a genuine wait failure returns an error, and that is the
// case the second observation exists for.
type waitErrProc struct {
	out     chan OutputFrame
	stopped chan struct{}
	mu      sync.Mutex
	done    bool
	err     error
}

func (p *waitErrProc) Send(context.Context, []byte) error { return nil }
func (p *waitErrProc) Output() <-chan OutputFrame         { return p.out }
func (p *waitErrProc) Wait() (int, error)                 { <-p.stopped; return -1, p.err }
func (p *waitErrProc) Stop(context.Context) error         { p.finish(); return nil }
func (p *waitErrProc) PID() int                           { return 4243 }
func (p *waitErrProc) finish() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.done {
		p.done = true
		close(p.out)
		close(p.stopped)
	}
}

type waitErrRunner struct {
	mu    sync.Mutex
	err   error
	procs []*waitErrProc
}

func (r *waitErrRunner) Launch(context.Context, LaunchSpec) (Process, error) {
	p := &waitErrProc{out: make(chan OutputFrame, 4), stopped: make(chan struct{}), err: r.err}
	r.mu.Lock()
	r.procs = append(r.procs, p)
	r.mu.Unlock()
	return p, nil
}

func (r *waitErrRunner) last() *waitErrProc {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.procs[len(r.procs)-1]
}

// terminalEventOf returns the newest event carrying an observation, and the run row.
func terminalEventOf(t *testing.T, m *Module, st store.Store, tenant model.TenantID, ref string) (runEventDTO, model.Record) {
	t.Helper()
	var found runEventDTO
	for _, ev := range listRunEvents(t, st, tenant, ref) {
		if ev.TerminalObservation != "" {
			found = ev
		}
	}
	rec, err := m.loadRun(context.Background(), tenant, ref)
	if err != nil {
		t.Fatalf("load run: %v", err)
	}
	return found, rec
}

func launchedRun(t *testing.T, m *Module, tenant model.TenantID) (string, string) {
	t.Helper()
	ctx := context.Background()
	dto, err := m.createRun(ctx, tenant, CreateRunParams{
		Transport: TransportStreamJSON, Isolation: IsolationNative,
		Actor: "user:u1", ActorKind: model.ActorUser,
	})
	if err != nil {
		t.Fatalf("createRun: %v", err)
	}
	rec, err := m.loadRun(ctx, tenant, dto.RunRef)
	if err != nil {
		t.Fatalf("load run: %v", err)
	}
	launchID := rec.String(colRuntimeLaunchID)
	if launchID == "" {
		t.Fatal("fixture precondition: the launched run carries no runtime_launch_id")
	}
	return dto.RunRef, launchID
}

// A clean exit: Wait returned nil, so this Process is known to have exited, and the
// generation it retired is named.
func TestTerminalEvidenceFinalizeRecordsTheObservedExit(t *testing.T) {
	fr := &fakeRunner{}
	m, st, tenant, _ := newRuntimeHarness(t, WithRunner(fr), WithCredentialSource(staticCred()))
	ref, launchID := launchedRun(t, m, tenant)

	fr.lastProc().finish(0)
	waitFor(t, "terminal state", func() bool {
		d, _ := m.getRun(context.Background(), tenant, ref)
		return d.State == stateStopped
	})

	ev, rec := terminalEventOf(t, m, st, tenant, ref)
	if ev.TerminalObservation != obsProcessExitObserved {
		t.Fatalf("observation = %q, want %q", ev.TerminalObservation, obsProcessExitObserved)
	}
	if ev.RetiredRuntimeLaunchID != launchID {
		t.Fatalf("retired id = %q, want the launch generation %q", ev.RetiredRuntimeLaunchID, launchID)
	}
	// The row itself no longer holds it — which is the whole reason the event must.
	if rec.String(colRuntimeLaunchID) != "" {
		t.Fatal("the terminal transition must still clear the run row's launch id")
	}
	if rec.String(colState) != stateStopped {
		t.Fatalf("state = %q, want stopped", rec.String(colState))
	}
	if ev.Event != "stopped" {
		t.Fatalf("event = %q, want stopped", ev.Event)
	}
}

// Wait failed: the row is still terminal, and the evidence says collection was not
// confirmed. The provider's error text never reaches a stored field.
func TestTerminalEvidenceWaitErrorIsUnverifiedAndTextIsNotStored(t *testing.T) {
	secret := "boom-from-the-runner-9d2f"
	wr := &waitErrRunner{err: errors.New(secret)}
	m, st, tenant, _ := newRuntimeHarness(t, WithRunner(wr), WithCredentialSource(staticCred()))
	ref, launchID := launchedRun(t, m, tenant)

	wr.last().finish()
	waitFor(t, "terminal state", func() bool {
		d, _ := m.getRun(context.Background(), tenant, ref)
		return d.State == stateStopped || d.State == stateFailed
	})

	ev, rec := terminalEventOf(t, m, st, tenant, ref)
	if ev.TerminalObservation != obsProcessWaitUnverified {
		t.Fatalf("observation = %q, want %q", ev.TerminalObservation, obsProcessWaitUnverified)
	}
	if ev.RetiredRuntimeLaunchID != launchID {
		t.Fatalf("retired id = %q, want %q", ev.RetiredRuntimeLaunchID, launchID)
	}
	if rec.String(colState) != stateStopped && rec.String(colState) != stateFailed {
		t.Fatalf("the lifecycle state must still be recorded; got %q", rec.String(colState))
	}
	for _, field := range []string{ev.Detail, ev.TerminalObservation, ev.RetiredRuntimeLaunchID,
		rec.String(colReason)} {
		if strings.Contains(field, secret) {
			t.Fatalf("provider error text leaked into stored evidence: %q", field)
		}
	}
}

// Orphan recovery: unconfirmed either way, and only a row that recorded a reservation
// can bind one.
func TestTerminalEvidenceOrphanBindsOnlyARecordedGeneration(t *testing.T) {
	for _, tc := range []struct {
		name       string
		clearFirst bool
	}{{"recorded generation", false}, {"legacy row without one", true}} {
		t.Run(tc.name, func(t *testing.T) {
			fr := &fakeRunner{}
			m, st, tenant, _ := newRuntimeHarness(t, WithRunner(fr), WithCredentialSource(staticCred()))
			ctx := context.Background()
			ref, launchID := launchedRun(t, m, tenant)
			if tc.clearFirst {
				// A pre-P1 row that never carried a reservation.
				lr, ok := m.rt.getLive(tenant, ref)
				if !ok {
					t.Fatal("no live handle to prepare the legacy row through")
				}
				m.mutateRunBest(ctx, lr, func(rec model.Record) {
					rec[colRuntimeLaunchID] = nil
				})
				waitFor(t, "legacy row without a reservation", func() bool {
					cur, err := m.loadRun(ctx, tenant, ref)
					return err == nil && cur.String(colRuntimeLaunchID) == ""
				})
				launchID = ""
			}
			rec, err := m.loadRun(ctx, tenant, ref)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := m.reconcileTerminal(ctx, tenant, ref, rec, "user:u1", model.ActorUser); err != nil {
				t.Fatalf("reconcileTerminal: %v", err)
			}
			ev, _ := terminalEventOf(t, m, st, tenant, ref)
			if ev.TerminalObservation != obsHandleLostUnconfirmed {
				t.Fatalf("observation = %q, want %q", ev.TerminalObservation, obsHandleLostUnconfirmed)
			}
			if ev.RetiredRuntimeLaunchID != launchID {
				t.Fatalf("retired id = %q, want %q", ev.RetiredRuntimeLaunchID, launchID)
			}
			if !strings.Contains(ev.Detail, "not confirmed terminated") {
				t.Fatalf("the recovery must keep saying what it is: %q", ev.Detail)
			}
		})
	}
}

// The capture point, with its own negative control: the same closure that clears the
// column proves a capture placed after it would have recorded nothing.
func TestTerminalEvidenceCaptureHappensBeforeTheMutationClearsIt(t *testing.T) {
	fr := &fakeRunner{}
	m, st, tenant, _ := newRuntimeHarness(t, WithRunner(fr), WithCredentialSource(staticCred()))
	ctx := context.Background()
	ref, launchID := launchedRun(t, m, tenant)

	afterClear := "unset"
	updated, err := m.transition(ctx, tenant, ref, transitionInput{
		event: "stopped", toState: stateStopped, detail: "exit 0",
		actor: "user:u1", actorKind: model.ActorUser,
		terminalObservation: obsProcessExitObserved,
		mutate: func(rec model.Record) {
			rec[colRuntimeLaunchID] = nil
			// A disposable capture at the point the old code would have reached.
			afterClear = rec.String(colRuntimeLaunchID)
		},
	})
	if err != nil {
		t.Fatalf("transition: %v", err)
	}
	if afterClear != "" {
		t.Fatalf("fixture precondition: the mutation must clear the id, saw %q", afterClear)
	}
	if updated.String(colRuntimeLaunchID) != "" {
		t.Fatal("the committed row must not carry the retired id")
	}
	events := listRunEvents(t, st, tenant, ref)
	last := events[len(events)-1]
	if last.RetiredRuntimeLaunchID != launchID {
		t.Fatalf("sealed id = %q, want the pre-mutation %q; a capture after the "+
			"mutation would have sealed %q", last.RetiredRuntimeLaunchID, launchID, afterClear)
	}
}

// Half-evidence never reaches either ledger, and the refusal rolls the row back.
func TestTerminalEvidenceMalformedRefusesAndCommitsNothing(t *testing.T) {
	for _, tc := range []struct {
		name        string
		observation string
		event       string
		toState     string
	}{
		{"unknown observation", "process_probably_died", "stopped", stateStopped},
		{"observation on a non-terminal transition", obsProcessExitObserved, "stopping", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fr := &fakeRunner{}
			m, st, tenant, _ := newRuntimeHarness(t, WithRunner(fr), WithCredentialSource(staticCred()))
			ctx := context.Background()
			ref, _ := launchedRun(t, m, tenant)
			before, err := m.loadRun(ctx, tenant, ref)
			if err != nil {
				t.Fatal(err)
			}
			eventsBefore := len(listRunEvents(t, st, tenant, ref))

			if _, err := m.transition(ctx, tenant, ref, transitionInput{
				event: tc.event, toState: tc.toState, detail: "x",
				actor: "user:u1", actorKind: model.ActorUser,
				terminalObservation: tc.observation,
				mutate:              func(rec model.Record) { rec[colRuntimeLaunchID] = nil },
			}); !errors.Is(err, errInvalidTerminalEvidence) {
				t.Fatalf("err = %v, want errInvalidTerminalEvidence", err)
			}
			after, err := m.loadRun(ctx, tenant, ref)
			if err != nil {
				t.Fatal(err)
			}
			if after.String(colState) != before.String(colState) {
				t.Fatalf("state moved on a refused transition: %q -> %q",
					before.String(colState), after.String(colState))
			}
			if after.String(colRuntimeLaunchID) != before.String(colRuntimeLaunchID) {
				t.Fatal("the refused transition committed its mutation")
			}
			if got := len(listRunEvents(t, st, tenant, ref)); got != eventsBefore {
				t.Fatalf("a refused transition appended %d event(s)", got-eventsBefore)
			}
		})
	}
}

// Historical digests are frozen. These vectors are computed by the independent
// encoder above, from the specification, and must equal what the production helper
// produces for the same input.
func TestTerminalEvidenceHistoricalHashVectorsAreUnchanged(t *testing.T) {
	const (
		runRef = "run-ref-1"
		seq    = int64(7)
		event  = "stopped"
		from   = "running"
		to     = "stopped"
		detail = "exit 0"
		atTS   = "2026-08-10T10:05:00Z"
	)
	seven := independentPayloadHash(runRef, "7", event, from, to, detail, atTS)
	got := runEventPayloadHash(runRef, seq, event, from, to, detail, atTS)
	if hex.EncodeToString(got[:]) != seven {
		t.Fatalf("seven-field digest changed:\n got %s\nwant %s", hex.EncodeToString(got[:]), seven)
	}

	work := &runtimeWorkGeneration{itemID: model.NewID(), holderSID: "sid-1", fence: 42}
	ten := independentPayloadHash(runRef, "7", event, from, to, detail, atTS,
		work.itemID.String(), "sid-1", "42")
	gotTen := runEventPayloadHashWithWorkGeneration(runRef, seq, event, from, to, detail, atTS, work)
	if hex.EncodeToString(gotTen[:]) != ten {
		t.Fatalf("ten-field digest changed:\n got %s\nwant %s", hex.EncodeToString(gotTen[:]), ten)
	}
	if seven == ten {
		t.Fatal("the two historical encodings must differ")
	}
}

// The new encoding: fourteen strings, every slot present, and both new fields bound.
func TestTerminalEvidenceFourteenFieldVectorAndSensitivity(t *testing.T) {
	const (
		runRef = "run-ref-1"
		seq    = int64(7)
		event  = "stopped"
		from   = "running"
		to     = "stopped"
		detail = "exit 0"
		atTS   = "2026-08-10T10:05:00Z"
	)
	id := model.NewID()
	ev := &runtimeTerminalEvidence{observation: obsProcessExitObserved, launchID: id}
	want := independentPayloadHash(
		"olv.sessions.run_event.terminal.v1",
		runRef, "7", event, from, to, detail, atTS,
		"0", "", "", "",
		id.String(), obsProcessExitObserved,
	)
	got := runEventPayloadHashWithTerminalEvidence(runRef, seq, event, from, to, detail, atTS, nil, ev)
	if hex.EncodeToString(got[:]) != want {
		t.Fatalf("fourteen-field digest:\n got %s\nwant %s", hex.EncodeToString(got[:]), want)
	}

	// Changing either new field must change the digest.
	other := &runtimeTerminalEvidence{observation: obsProcessWaitUnverified, launchID: id}
	changedObs := runEventPayloadHashWithTerminalEvidence(runRef, seq, event, from, to, detail, atTS, nil, other)
	if changedObs == got {
		t.Fatal("the observation is not bound into the digest")
	}
	third := &runtimeTerminalEvidence{observation: obsProcessExitObserved, launchID: model.NewID()}
	changedID := runEventPayloadHashWithTerminalEvidence(runRef, seq, event, from, to, detail, atTS, nil, third)
	if changedID == got {
		t.Fatal("the retired launch id is not bound into the digest")
	}

	// A concurrent work generation is bound too, and the presence slot is explicit.
	work := &runtimeWorkGeneration{itemID: model.NewID(), holderSID: "sid-1", fence: 42}
	wantWork := independentPayloadHash(
		"olv.sessions.run_event.terminal.v1",
		runRef, "7", event, from, to, detail, atTS,
		"1", work.itemID.String(), "sid-1", "42",
		id.String(), obsProcessExitObserved,
	)
	gotWork := runEventPayloadHashWithTerminalEvidence(runRef, seq, event, from, to, detail, atTS, work, ev)
	if hex.EncodeToString(gotWork[:]) != wantWork {
		t.Fatalf("fourteen-field digest with work:\n got %s\nwant %s",
			hex.EncodeToString(gotWork[:]), wantWork)
	}
}

// The digest that actually landed in the projection, checked against the independent
// vector rather than against the helper that wrote it.
func TestTerminalEvidenceStoredProjectionMatchesTheIndependentVector(t *testing.T) {
	fr := &fakeRunner{}
	m, st, tenant, _ := newRuntimeHarness(t, WithRunner(fr), WithCredentialSource(staticCred()))
	ref, launchID := launchedRun(t, m, tenant)
	fr.lastProc().finish(0)
	waitFor(t, "terminal state", func() bool {
		d, _ := m.getRun(context.Background(), tenant, ref)
		return d.State == stateStopped
	})

	ev, _ := terminalEventOf(t, m, st, tenant, ref)
	want := independentPayloadHash(
		"olv.sessions.run_event.terminal.v1",
		ref, strconv.FormatInt(ev.Seq, 10), ev.Event, ev.FromState, ev.ToState, ev.Detail, ev.At,
		"0", "", "", "",
		launchID, obsProcessExitObserved,
	)
	if ev.PayloadHash != want {
		t.Fatalf("stored payload_hash:\n got %s\nwant %s", ev.PayloadHash, want)
	}
	// The same bytes anchor the core audit entry; audit_seq is its position, and a
	// zero there means the degraded spool policy dropped the anchor (unchanged by P1).
	if ev.AuditSeq == 0 {
		t.Log("audit_seq is 0: the degraded policy dropped the anchor; not sealed proof")
	}
}

// A stale incarnation retires nothing, so it must record no observation for the
// generation that replaced it.
func TestTerminalEvidenceStaleFinalizeRecordsNoSuccessorEvidence(t *testing.T) {
	fr := &fakeRunner{}
	m, st, tenant, _ := newRuntimeHarness(t, WithRunner(fr), WithCredentialSource(staticCred()))
	ctx := context.Background()
	ref, launchID := launchedRun(t, m, tenant)

	live, ok := m.rt.getLive(tenant, ref)
	if !ok {
		t.Fatal("no live handle")
	}
	// A handle for a generation this row never carried. Built field by field rather
	// than copied, because liveRun owns a mutex.
	stale := &liveRun{
		tenant: live.tenant, runRef: live.runRef, proc: live.proc, ring: live.ring,
		cancel: func() {}, finalizedCh: make(chan struct{}),
		launchID: model.NewID(),
	}

	m.finalize(stale, 137, nil)

	rec, err := m.loadRun(ctx, tenant, ref)
	if err != nil {
		t.Fatal(err)
	}
	if rec.String(colRuntimeLaunchID) != launchID {
		t.Fatalf("a stale finalize retired the live generation: %q", rec.String(colRuntimeLaunchID))
	}
	for _, ev := range listRunEvents(t, st, tenant, ref) {
		if ev.TerminalObservation != "" {
			t.Fatalf("a stale finalize sealed evidence: %+v", ev)
		}
	}
}

// Every event written before P1 — and every non-terminal one after it — omits both
// fields, through the real read mapping.
func TestTerminalEvidenceNonTerminalEventsOmitBothFields(t *testing.T) {
	fr := &fakeRunner{}
	m, st, tenant, _ := newRuntimeHarness(t, WithRunner(fr), WithCredentialSource(staticCred()))
	ref, _ := launchedRun(t, m, tenant)
	for _, ev := range listRunEvents(t, st, tenant, ref) {
		if ev.TerminalObservation != "" || ev.RetiredRuntimeLaunchID != "" {
			t.Fatalf("a non-terminal event carries evidence: %+v", ev)
		}
	}
}
