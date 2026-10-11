// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"testing"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// The session that was running when the engine stopped read "Failed" after the
// restart, because the shutdown's SIGTERM exit was recorded as the tool failing. The
// engine's own stop is recorded as stopped, with what to do next.
func TestASessionRunningAtEngineStopIsStoppedNotFailed(t *testing.T) {
	ctx := context.Background()
	fr := &fakeRunner{initSID: "engine-stop"}
	m, _, tenant, _ := newRuntimeHarness(t, WithRunner(fr), WithCredentialSource(staticCred()))
	run, err := createProfiledTestRun(t, m, ctx, tenant, CreateRunParams{Transport: TransportStreamJSON, Isolation: IsolationNative, Actor: actorU, ActorKind: actorKindU})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "running", func() bool { d, _ := m.getRun(ctx, tenant, run.RunRef); return d.ClaudeSessionID != "" })
	// A restart stops it, so the console says how many before it applies one (#507).
	if n := m.RunningSessions(); n != 1 {
		t.Fatalf("running sessions before the engine stopped = %d, want 1", n)
	}
	// A run already asked to stop, and not yet finished, is not one the restart stops.
	lr := m.rt.snapshotLive()[0]
	lr.mu.Lock()
	lr.stopRequested = true
	lr.mu.Unlock()
	if n := m.RunningSessions(); n != 0 {
		t.Fatalf("running sessions with the run stopping = %d, want 0", n)
	}
	lr.mu.Lock()
	lr.stopRequested = false
	lr.mu.Unlock()
	if err := m.stopAllRuns(ctx); err != nil {
		t.Fatal(err)
	}
	if n := m.RunningSessions(); n != 0 {
		t.Fatalf("running sessions after the engine stopped = %d, want 0", n)
	}
	// The engine binds the count when the sessions module is off, too.
	if n := (*Module)(nil).RunningSessions(); n != 0 {
		t.Fatalf("running sessions without the sessions module = %d, want 0", n)
	}
	d, err := m.getRun(ctx, tenant, run.RunRef)
	if err != nil || d.State != stateStopped || d.Reason != engineStoppedReason {
		t.Fatalf("after the engine stopped: state %q reason %q (%v), want %q %q", d.State, d.Reason, err, stateStopped, engineStoppedReason)
	}
}

// Now counted that stopped session as live, because a live row's state comes
// from how recent its last activity is. The plane's own row of a run that stops reads
// ended at once.
func TestTheLiveRowOfAStoppedManagedRunReadsEnded(t *testing.T) {
	for _, be := range profileBackends(t) {
		t.Run(be.name, func(t *testing.T) {
			ctx := context.Background()
			fr := &fakeRunner{initSID: "managed-ended"}
			m, st, _ := openProfiledRuntime(t, be, WithRunner(fr), WithCredentialSource(staticCred()))
			tenant := ensureTenant(t, st, "ended-"+be.name)
			config, user, _, _ := twoHomes(t)
			prof := mustCreateProfile(t, m, tenant, CreateProfileInput{Driver: "claude", ConfigHome: config, UserHome: user, DisplayName: "A"})
			run, err := createProfiledTestRun(t, m, ctx, tenant, CreateRunParams{
				Transport: TransportStreamJSON, PermissionMode: "default", Isolation: IsolationNative,
				Actor: "user:u1", ActorKind: model.ActorUser, ProviderProfileRef: prof.Ref,
			})
			if err != nil {
				t.Fatal(err)
			}
			waitFor(t, "managed row", func() bool { d, _ := m.getRun(ctx, tenant, run.RunRef); return d.LiveRef != "" })
			d, _ := m.getRun(ctx, tenant, run.RunRef)
			state := func() string {
				t.Helper()
				var rec model.Record
				if err := st.View(ctx, tenant, func(sc store.Scope) error {
					var err error
					rec, err = findLiveByID(ctx, sc, model.ID(d.LiveRef))
					return err
				}); err != nil {
					t.Fatal(err)
				}
				return m.toLiveDTO(rec).CCState
			}
			if got := state(); got == ccEnded {
				t.Fatalf("a running session's live row = %q", got)
			}
			if err := m.stopAllRuns(ctx); err != nil {
				t.Fatal(err)
			}
			if got := state(); got != ccEnded {
				t.Fatalf("the live row after the engine stopped the run = %q, want %q", got, ccEnded)
			}
		})
	}
}

// SR2C on e97d5e7c: the same managed row goes from ended back to live when the session
// resumes, and is ended again when it stops (live_scope.go clears ended_at on the bind).
func TestTheManagedRowEndsResumesAndEndsAgain(t *testing.T) {
	for _, be := range profileBackends(t) {
		t.Run(be.name, func(t *testing.T) {
			ctx := context.Background()
			fr := &fakeRunner{initSID: "managed-resume"}
			m, st, _ := openProfiledRuntime(t, be, WithRunner(fr), WithCredentialSource(staticCred()))
			tenant := ensureTenant(t, st, "resume-"+be.name)
			config, user, _, _ := twoHomes(t)
			prof := mustCreateProfile(t, m, tenant, CreateProfileInput{Driver: "claude", ConfigHome: config, UserHome: user, DisplayName: "A"})
			run, err := createProfiledTestRun(t, m, ctx, tenant, CreateRunParams{
				Transport: TransportStreamJSON, PermissionMode: "default", Isolation: IsolationNative,
				Actor: "user:u1", ActorKind: model.ActorUser, ProviderProfileRef: prof.Ref,
			})
			if err != nil {
				t.Fatal(err)
			}
			waitFor(t, "managed row", func() bool { d, _ := m.getRun(ctx, tenant, run.RunRef); return d.LiveRef != "" })
			d, _ := m.getRun(ctx, tenant, run.RunRef)
			live := func() model.Record {
				t.Helper()
				var rec model.Record
				if err := st.View(ctx, tenant, func(sc store.Scope) error {
					var err error
					rec, err = findLiveByID(ctx, sc, model.ID(d.LiveRef))
					return err
				}); err != nil {
					t.Fatal(err)
				}
				return rec
			}
			stop := func(step string) {
				t.Helper()
				if _, err := m.stopRun(ctx, tenant, run.RunRef, "user:u1", "user"); err != nil {
					t.Fatalf("%s: stop: %v", step, err)
				}
				if rec := live(); rec.IsNull(colLiveEndedAt) || m.toLiveDTO(rec).CCState != ccEnded {
					t.Fatalf("%s: live row after the stop = ended_at %q cc_state %q, want ended", step, rec.String(colLiveEndedAt), m.toLiveDTO(rec).CCState)
				}
			}
			stop("first stop")
			if _, err := m.resumeRun(ctx, tenant, run.RunRef, "user:u1", "user", ""); err != nil {
				t.Fatalf("resume: %v", err)
			}
			waitFor(t, "resumed managed row", func() bool { return live().IsNull(colLiveEndedAt) })
			if rec := live(); m.toLiveDTO(rec).CCState == ccEnded {
				t.Fatalf("live row after the resume = %q, want live", m.toLiveDTO(rec).CCState)
			}
			if again, _ := m.getRun(ctx, tenant, run.RunRef); again.LiveRef != d.LiveRef {
				t.Fatalf("the resumed session's live row = %q, want the same %q", again.LiveRef, d.LiveRef)
			}
			stop("second stop")
		})
	}
}
