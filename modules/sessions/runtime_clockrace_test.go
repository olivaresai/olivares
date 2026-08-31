// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
)

// The regression for the clock race the detector reported on main.
//
// WHAT BROKE. testClock used to be a bare `struct{ now time.Time }`. The module hands
// that clock to PRODUCTION code (WithClock), and production reads it from a goroutine
// the test body never joins: createRun starts `go m.bridge(lr)` (runtime.go:384) and the
// bridge reads Module.now() (runtime.go:1291) twice over — once per frame at the top of
// its pump (runtime_bridge.go:32), and again, one frame deeper, down
// onStdout → captureSessionID → bindProviderSession (runtime_bridge.go:104), which is the
// stack CI printed. A test body writing the field at the same time is a real data race,
// and it landed as a red main, in someone else's run, blaming the wrong pull request. The
// clock is mutex-guarded now (live_test.go:24-33) — nothing else stopped the bare field
// from coming back, which is what this file is.
//
// WHY IT IS NOT VACUOUS. A test that moves the clock "somewhere around there" while it
// waits for something is exactly the test that stays green forever. This one
// SYNCHRONIZES with the bridge goroutine, and the synchronization costs production
// nothing: the process double's output channel is UNBUFFERED, so `proc.out <- frame`
// cannot return until the bridge has received that frame. No hook, no test-only branch,
// no new field in any production type — the Process port already hands the bridge a
// channel (runtime_ports.go:186), and a double is free to choose its capacity. fakeRunner
// cannot serve, and that is the point: its channel is buffered (runtime_test.go:82), so
// its send returns before the bridge has looked at anything.
//
// The ORDER is what makes the detector fire, and it is worth stating precisely rather
// than as a picture of where the bridge "is". The receive is completed before the send
// returns, so both what the bridge does next (read the clock, runtime_bridge.go:31-32
// has no branch between the two) and what the test does next (write it) are successors
// of that one rendezvous — and neither is a successor of the other. Unordered
// conflicting accesses is the definition of the race, whichever one the scheduler runs
// first. ONE handoff is therefore enough by construction; the extra ones are redundancy,
// not the source of determinism.
//
// The barrier could not be built the other way round. A double that signaled "I have
// read the clock" and let the test write afterwards would create exactly the
// happens-before edge that makes the race disappear, and the test would pass forever.
//
// The three assertions at the end are the second half of not being vacuous. Two of them
// pin the reported stack — the launch took a claim, and the provider id resolves to the
// canonical session that claim named, which is observable only if bindProviderSession
// ran its body. The third pins the thing the other two do not: that the bridge actually
// CALLED the clock, counted on the double itself. Without it, production could keep the
// capture and the alias while losing both reads and this file would still be green.
//
// HOW TO PROVE IT STILL BITES (the mutation, and the only accepted evidence): put
// testClock back to a bare field — drop the mutex, and let Now/get/set/advance touch
// c.now directly — then run
//
//	go test -race -count=1 -run TestRuntime_TheBridgeReadsTheClockWhileTheTestBodyMovesIt ./modules/sessions/
//
// Measured 2026-08-05, and this is what that run printed rather than a paraphrase of it:
// `WARNING: DATA RACE`, read in (*testClock).Now called from (*Module).now at
// runtime.go:1291, from (*Module).bridge at runtime_bridge.go:32, on the goroutine
// createRun starts at runtime.go:384; previous write in (*testClock).advance called from
// the handoff loop of this test. (The two testClock frames carry the MUTANT's line
// numbers, not this file's, so they are named by function instead.) That run reported the
// per-frame read at the top of the pump; nothing here claims the detector stops at the
// first race it meets, and by default it does not. Restore the mutex afterwards, and
// verify the restore with sha256 rather than by eye.
//
// LIMITATION, stated rather than discovered later: the RACE half of this guard is a
// race-detector guard. Without -race a bare field passes it, as it passes everywhere
// else; what holds that line is CI running this package under -race, and this test only
// makes the finding deterministic instead of a once-in-many-runs flake. The read-count
// assertion is the part that does fail without -race.

// clockRaceSID is the provider session id the init envelope carries. It is asserted on,
// so a bridge that silently stopped parsing init frames fails this test loudly.
const clockRaceSID = "sess-clock-race"

// clockRaceHandoffs is how many frames follow the init one. One is already enough for
// the race (see the file comment); these are redundancy, and they are what the read
// count is measured against.
const clockRaceHandoffs = 16

// handoffTimeout bounds a rendezvous. A bare send would be correct but unkind: if the
// bridge ever stopped consuming Output, the test would produce no assertion at all and
// hold the whole package until go test's global timeout.
const handoffTimeout = 10 * time.Second

// countingClock is the clock for THIS regression: testClock, plus a count of how many
// times production called Now.
//
// The count deliberately lives behind its OWN mutex and never touches the instant: the
// mutant has to be able to strip testClock's mutex and still race, so nothing here may
// order an access to `now`. Note that Now reads the instant BEFORE it touches cmu, and
// that the test observes the count exactly twice — a baseline before any frame has been
// handed over, and the check after the last advance. Neither observation falls between an
// instant read and the write it is contrasted with, so neither can retro-order them.
// (Happens-before is not confined to the variable being observed, which is why this has to
// be argued rather than waved at: a lock or a channel on ANY location orders whatever
// precedes it. That is also why the barrier could not be built the other way round.)
type countingClock struct {
	*testClock
	cmu   sync.Mutex
	calls int
}

func (c *countingClock) Now() model.Timestamp {
	ts := c.testClock.Now() // the read that races in the mutant — before any counting
	c.cmu.Lock()
	c.calls++
	c.cmu.Unlock()
	return ts
}

func (c *countingClock) count() int {
	c.cmu.Lock()
	defer c.cmu.Unlock()
	return c.calls
}

// handoffRunner hands out ONE process the test built itself, so the test owns the exact
// moment each frame crosses into the bridge goroutine.
type handoffRunner struct{ proc *fakeProc }

func (r *handoffRunner) Launch(context.Context, LaunchSpec) (Process, error) { return r.proc, nil }

func TestRuntime_TheBridgeReadsTheClockWhileTheTestBodyMovesIt(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	gate := &spyGate{inner: LaunchDecision{Allowed: true}}
	// UNBUFFERED on purpose: this channel IS the synchronization point. See the file
	// comment — with a buffer the send returns before the bridge has received the frame.
	proc := &fakeProc{out: make(chan OutputFrame), stopped: make(chan struct{})}
	clk := &countingClock{testClock: &testClock{now: baseTime}}
	m, _, tenant, _ := newRuntimeHarness(t,
		WithRunner(&handoffRunner{proc: proc}),
		WithCredentialSource(staticCred()),
		WithLaunchGate(gate),
		WithClock(clk)) // applied after the harness's own clock, so this one wins

	dto, err := m.createRun(ctx, tenant, CreateRunParams{
		Transport: TransportStreamJSON, Isolation: IsolationNative,
		WorkspaceRef: registerTestWorkspace(t, m, tenant, t.TempDir()),
		Actor:        "user:u1", ActorKind: "user",
	})
	if err != nil {
		t.Fatalf("createRun: %v", err)
	}
	// From here on the bridge is alive, so every exit from this test — including a
	// Fatal — has to end it. finish is idempotent, so the explicit stopRun below still
	// exercises the happy path.
	t.Cleanup(func() {
		proc.finish(143)
		if lr, ok := m.rt.getLive(tenant, dto.RunRef); ok {
			select {
			case <-lr.finalizedCh:
			case <-time.After(handoffTimeout):
			}
		}
	})
	// bindProviderSession returns BEFORE reading the clock when the launch took no
	// claim. Without a claim this test would still pass, and would be testing the early
	// return instead of the reported stack.
	claimSID := gate.last(t).ClaimSID
	if claimSID == "" {
		t.Fatal("the launch acquired no claim, so bindProviderSession returns before it ever " +
			"reads the clock: this test would be exercising the early return, not the race")
	}
	// The baseline is taken here: createRun's own reads are on THIS goroutine and are
	// not what the count is about. No frame has been handed over yet, so the bridge has
	// read nothing.
	baseline := clk.count()

	// handoff returns only once the bridge has received the frame.
	handoff := func(what, data string) {
		t.Helper()
		select {
		case proc.out <- OutputFrame{Stream: streamStdout, Data: []byte(data)}:
		case <-time.After(handoffTimeout):
			t.Fatalf("the bridge did not take the %s frame within %s: it is no longer draining "+
				"Output, so this test can no longer meet it at the clock read", what, handoffTimeout)
		}
	}

	// The init envelope drives the stack the detector printed:
	// onStdout → captureSessionID → bindProviderSession → Module.now().
	handoff("init", `{"type":"system","subtype":"init","session_id":"`+clockRaceSID+`"}`)
	clk.advance(time.Second)

	// Every later frame re-enters the read at the top of the pump.
	for i := 0; i < clockRaceHandoffs; i++ {
		handoff("assistant", `{"type":"assistant","message":{"role":"assistant"}}`)
		clk.advance(time.Second)
	}

	// --- the bridge really walked that stack, so the reads above really happened ----

	// Read the count BEFORE anything else on this goroutine touches the clock again.
	// The bridge cannot accept frame N+1 without having finished frame N, so when the
	// last send returned, the first 1+clockRaceHandoffs-1 frames were fully processed:
	// one read each at the top of the pump, plus the bind read the init frame adds.
	if got := clk.count() - baseline; got < clockRaceHandoffs+1 {
		t.Fatalf("the bridge called the clock %d times across %d handed-over frames, want at least %d: "+
			"production no longer reads the clock on the path this regression watches, so the race "+
			"it pins can no longer happen and this test has stopped meaning anything",
			got, clockRaceHandoffs+1, clockRaceHandoffs+1)
	}

	waitFor(t, "the bridge to capture the session id from the init frame", func() bool {
		d, _ := m.getRun(ctx, tenant, dto.RunRef)
		return d.ClaudeSessionID == clockRaceSID
	})
	// captureSessionID is only the door. The reported read is one frame further in, in
	// bindProviderSession's body — which is observable exactly here: the provider id now
	// resolves to the SAME canonical session the launch's claim named. If that body had
	// been skipped, this resolve would mint a fresh identity and the ids would differ.
	sid, err := m.ResolveSession(ctx, tenant, SessionBinding{Provider: "claude", ExternalID: clockRaceSID})
	if err != nil {
		t.Fatalf("resolve the provider alias: %v", err)
	}
	if sid != claimSID {
		t.Fatalf("the provider session id resolves to %q instead of the launch's canonical session %q: "+
			"bindProviderSession never ran its body, so the clock read this test is built on never happened",
			sid, claimSID)
	}

	if _, err := m.stopRun(ctx, tenant, dto.RunRef, "user:u1", "user"); err != nil {
		t.Fatalf("stopRun: %v", err)
	}
}
