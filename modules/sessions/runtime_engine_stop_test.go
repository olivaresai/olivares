// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// restartModule is the next boot: a new module on the same store and session root,
// listing its tenants the way the composition does, restoring a run's launcher the way
// it does, and gated the way production's launch gate is: a launch needs a launcher
// principal whose actor is the intent's.
func restartModule(t *testing.T, previous *Module, st store.Store, tenant model.TenantID, opts ...Option) *Module {
	t.Helper()
	restarted := New(append([]Option{WithLaunchGate(principalGate{})}, opts...)...)
	restarted.UseExecutionEnvironmentRef(previous.rt.environmentRef)
	restarted.UseData(api.NewModuleData(st))
	bindStoreStanding(restarted, st)
	restarted.ApprovalRecoveryTenants = func(context.Context) ([]model.TenantID, error) {
		return []model.TenantID{tenant}, nil
	}
	restarted.QueuedLaunchAuthorization = func(_ context.Context, _ model.TenantID, c auth.QueuedCredential, _ string, _ model.ID) (auth.Principal, error) {
		return auth.Principal{Kind: auth.KindUser, UserID: c.UserID}, nil
	}
	stopModuleAtCleanup(t, restarted)
	return restarted
}

// principalGate refuses a launch with no launcher principal for its own actor, as the
// session credential mint does in the composed engine.
type principalGate struct{}

func (principalGate) Authorize(_ context.Context, _ model.TenantID, intent LaunchIntent) (LaunchDecision, error) {
	if intent.Actor == "" || intent.LauncherPrincipal.Actor() != intent.Actor {
		return LaunchDecision{}, auth.ErrUnauthenticated
	}
	return LaunchDecision{Allowed: true}, nil
}

// launchCredential is the credential an API launch carries: the launcher's login.
func launchCredential(launcher model.ID) auth.QueuedCredential {
	return auth.QueuedCredential{ID: model.NewID(), UserID: launcher, Kind: auth.KindUser, Version: 1}
}

// sealCredential binds a launch credential as the authenticator does: a fingerprint.
func sealCredential(_ context.Context, c auth.QueuedCredential) (auth.QueuedCredential, error) {
	c.Seal = "seal-" + c.ID.String()
	return c, nil
}

// A run that was running when the engine stopped cleanly runs again at the next
// start, with its conversation, and its ledger says why. A run the user stopped
// stays stopped.
func TestRunsStoppedByTheEngineResumeAtTheNextStart(t *testing.T) {
	ctx := context.Background()
	runner := &fakeRunner{}
	options := []Option{WithSessionWorkspaceRoot(t.TempDir()), WithRunner(runner), WithCredentialSource(staticCred())}
	m, st, tenant, _ := newRuntimeHarness(t, options...)
	m.QueuedCredentialCapture = sealCredential
	launcher := model.NewID()

	conversations := map[string]string{}
	launch := func() string {
		t.Helper()
		sid := fmt.Sprintf("engine-stop-conversation-%d", len(conversations))
		runner.initSID = sid
		run, err := createProfiledTestRun(t, m, ctx, tenant, CreateRunParams{Transport: TransportStreamJSON, Isolation: IsolationNative, Actor: actorU, ActorKind: actorKindU, queuedCredential: launchCredential(launcher)})
		if err != nil {
			t.Fatal(err)
		}
		waitFor(t, "conversation id", func() bool { d, _ := m.getRun(ctx, tenant, run.RunRef); return d.ClaudeSessionID == sid })
		conversations[run.RunRef] = sid
		return run.RunRef
	}
	kept := []string{launch(), launch()}
	userStopped := launch()
	if _, err := m.stopRun(ctx, tenant, userStopped, actorU, actorKindU); err != nil {
		t.Fatal(err)
	}
	if err := m.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	for _, ref := range kept {
		if d, err := m.getRun(ctx, tenant, ref); err != nil || d.State != stateStopped || d.Reason != engineStoppedReason {
			t.Fatalf("run %s after the engine stopped: state %q reason %q (%v)", ref, d.State, d.Reason, err)
		}
	}
	before := launchCount(runner)
	runner.initSID = ""

	restarted := restartModule(t, m, st, tenant, options...)
	var revalidated []auth.QueuedCredential // read after the pass ends (approvalWG.Wait)
	restarted.QueuedLaunchAuthorization = func(_ context.Context, _ model.TenantID, c auth.QueuedCredential, _ string, _ model.ID) (auth.Principal, error) {
		revalidated = append(revalidated, c)
		return auth.Principal{Kind: auth.KindUser, UserID: c.UserID}, nil
	}
	if err := restarted.Start(ctx); err != nil {
		t.Fatalf("restart = %v", err)
	}
	for _, ref := range kept {
		waitFor(t, "run "+ref+" running again", func() bool {
			d, err := restarted.getRun(ctx, tenant, ref)
			return err == nil && d.State == stateRunning
		})
	}

	restarted.rt.approvalWG.Wait() // the pass is over: nothing else may start after this
	if len(revalidated) != len(kept) {
		t.Fatalf("credentials revalidated at the restart = %d, want %d: the engine-stopped runs only", len(revalidated), len(kept))
	}
	for _, c := range revalidated {
		if c.UserID != launcher || c.Kind != auth.KindUser || c.Version != 1 || !strings.HasPrefix(c.Seal, "seal-") {
			t.Fatalf("a run was resumed on a credential other than the one that launched it: %+v", c)
		}
	}
	if got := launchCount(runner) - before; got != len(kept) {
		t.Fatalf("launches at the restart = %d, want %d: only the engine-stopped runs", got, len(kept))
	}
	runner.mu.Lock()
	resumed := slices.Clone(runner.specs[before:])
	runner.mu.Unlock()
	var continued []string
	for _, spec := range resumed {
		i := slices.Index(spec.Args, "--resume")
		if i < 0 {
			t.Fatalf("a restart launch did not continue a conversation: %q", spec.Args)
		}
		continued = append(continued, spec.Args[i+1])
	}
	for _, ref := range kept {
		if !slices.Contains(continued, conversations[ref]) {
			t.Fatalf("run %s: its conversation %q was not continued; restart launches resumed %q", ref, conversations[ref], continued)
		}
	}
	for _, ref := range kept {
		lines := 0
		for _, e := range listRunEvents(t, st, tenant, ref) {
			if e.Event == "resumed" && strings.HasPrefix(e.Detail, "resumed automatically after the engine stopped") {
				lines++
				if e.Actor != "user:"+launcher.String() || e.ActorKind != "user" {
					t.Fatalf("run %s: automatic resume recorded actor %q kind %q, want user:%s kind user", ref, e.Actor, e.ActorKind, launcher)
				}
			}
		}
		if lines != 1 {
			t.Fatalf("run %s: ledger lines recording the automatic resume = %d, want 1", ref, lines)
		}
	}
	if d, err := restarted.getRun(ctx, tenant, userStopped); err != nil || d.State != stateStopped {
		t.Fatalf("the run the user stopped: state %q (%v), want %q", d.State, err, stateStopped)
	}

	// A second recovery (a leader promotion) finds nothing left to resume.
	restarted.RecoverWaitingLaunchesForAllTenants(ctx)
	restarted.rt.approvalWG.Wait()
	if got := launchCount(runner) - before; got != len(kept) {
		t.Fatalf("launches after a second recovery = %d, want %d", got, len(kept))
	}
	for _, ref := range append(slices.Clone(kept), userStopped) {
		automatic := 0
		for _, e := range listRunEvents(t, st, tenant, ref) {
			if strings.Contains(e.Detail, "resumed automatically") {
				automatic++
			}
		}
		if want := map[bool]int{true: 0, false: 1}[ref == userStopped]; automatic != want {
			t.Fatalf("run %s: automatic ledger lines after the second recovery = %d, want %d", ref, automatic, want)
		}
	}
}

// A resume the launch gates refuse leaves the run stopped for the engine, so the
// next start tries it again: the refusal must not replace the engine's reason.
func TestAResumeTheGatesRefuseIsTriedAgainAtTheNextStart(t *testing.T) {
	ctx := context.Background()
	runner := &fakeRunner{initSID: "engine-stop-gated"}
	options := []Option{WithSessionWorkspaceRoot(t.TempDir()), WithRunner(runner), WithCredentialSource(staticCred())}
	m, st, tenant, _ := newRuntimeHarness(t, options...)
	m.QueuedCredentialCapture = sealCredential
	run, err := createProfiledTestRun(t, m, ctx, tenant, CreateRunParams{Transport: TransportStreamJSON, Isolation: IsolationNative, Actor: actorU, ActorKind: actorKindU, queuedCredential: launchCredential(model.NewID())})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "conversation id", func() bool { d, _ := m.getRun(ctx, tenant, run.RunRef); return d.ClaudeSessionID != "" })
	if err := m.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	before := launchCount(runner)

	refused := restartModule(t, m, st, tenant, append(slices.Clone(options), WithLaunchGate(denyGate{reason: "budget spent"}))...)
	if err := refused.Start(ctx); err != nil {
		t.Fatal(err)
	}
	refused.rt.approvalWG.Wait()
	if d, err := refused.getRun(ctx, tenant, run.RunRef); err != nil || d.State != stateStopped || d.Reason != engineStoppedReason {
		t.Fatalf("run after a refused resume: state %q reason %q (%v), want it still stopped by the engine", d.State, d.Reason, err)
	}
	if got := launchCount(runner) - before; got != 0 {
		t.Fatalf("launches while the gate refused = %d, want 0", got)
	}
	if err := refused.Stop(ctx); err != nil {
		t.Fatal(err)
	}

	again := restartModule(t, m, st, tenant, options...)
	if err := again.Start(ctx); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the run resumes once the gate allows it", func() bool {
		d, err := again.getRun(ctx, tenant, run.RunRef)
		return err == nil && d.State == stateRunning
	})
}

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

// allowRunAdmin answers every permission question with yes: an administrator.
type allowRunAdmin struct{}

func (allowRunAdmin) Authorize(context.Context, auth.Request) auth.Decision {
	return auth.Decision{Allow: true}
}

// The engine's own resume asks, of the launcher it restores, what a person's resume
// asks: a full-permission session comes back only while its launcher still has run
// administration. Otherwise it stays stopped, and the log names it.
func TestAFullPermissionRunResumesAtTheNextStartOnlyWhileItsLauncherIsAnAdministrator(t *testing.T) {
	for _, administrator := range []bool{false, true} {
		t.Run(fmt.Sprintf("administrator=%t", administrator), func(t *testing.T) {
			ctx := context.Background()
			runner := &fakeRunner{initSID: "engine-stop-full"}
			options := []Option{WithSessionWorkspaceRoot(t.TempDir()), WithRunner(runner), WithCredentialSource(staticCred())}
			m, st, tenant, _ := newRuntimeHarness(t, options...)
			m.QueuedCredentialCapture = sealCredential
			run, err := createProfiledTestRun(t, m, ctx, tenant, CreateRunParams{
				Transport: TransportStreamJSON, Isolation: IsolationNative, PermissionMode: permModeBypass,
				Actor: actorU, ActorKind: actorKindU, MayRunUnrestricted: true, queuedCredential: launchCredential(model.NewID()),
			})
			if err != nil {
				t.Fatal(err)
			}
			waitFor(t, "conversation id", func() bool { d, _ := m.getRun(ctx, tenant, run.RunRef); return d.ClaudeSessionID != "" })
			if err := m.Stop(ctx); err != nil {
				t.Fatal(err)
			}
			before := launchCount(runner)

			var logs bytes.Buffer
			restarted := restartModule(t, m, st, tenant, options...)
			restarted.log = slog.New(slog.NewTextHandler(&logs, nil))
			if administrator {
				restarted.WorkAuthorizer = allowRunAdmin{}
			}
			if err := restarted.Start(ctx); err != nil {
				t.Fatalf("restart = %v", err)
			}
			restarted.rt.approvalWG.Wait()

			d, err := restarted.getRun(ctx, tenant, run.RunRef)
			if err != nil {
				t.Fatal(err)
			}
			if administrator {
				if d.State != stateRunning || launchCount(runner)-before != 1 {
					t.Fatalf("full-permission run of an administrator: state %q after %d launches, want it running after 1", d.State, launchCount(runner)-before)
				}
				return
			}
			if d.State != stateStopped || d.Reason != engineStoppedReason {
				t.Fatalf("full-permission run of a non-administrator: state %q reason %q, want it stopped by the engine", d.State, d.Reason)
			}
			if got := launchCount(runner) - before; got != 0 {
				t.Fatalf("launches at the restart = %d, want 0", got)
			}
			if !strings.Contains(logs.String(), run.RunRef) || !strings.Contains(logs.String(), "only an administrator") {
				t.Fatalf("the refusal must name the run and say why in the log: %s", logs.String())
			}
		})
	}
}

// A run whose launcher the directory no longer authorizes (disabled, removed, forbidden
// since) stays stopped, and the log says so.
func TestARunWhoseLauncherIsGoneStaysStoppedAtTheNextStart(t *testing.T) {
	ctx := context.Background()
	runner := &fakeRunner{initSID: "engine-stop-gone"}
	options := []Option{WithSessionWorkspaceRoot(t.TempDir()), WithRunner(runner), WithCredentialSource(staticCred())}
	m, st, tenant, _ := newRuntimeHarness(t, options...)
	m.QueuedCredentialCapture = sealCredential
	run, err := createProfiledTestRun(t, m, ctx, tenant, CreateRunParams{Transport: TransportStreamJSON, Isolation: IsolationNative, Actor: actorU, ActorKind: actorKindU, queuedCredential: launchCredential(model.NewID())})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "conversation id", func() bool { d, _ := m.getRun(ctx, tenant, run.RunRef); return d.ClaudeSessionID != "" })
	if err := m.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	before := launchCount(runner)

	var logs bytes.Buffer
	restarted := restartModule(t, m, st, tenant, options...)
	restarted.log = slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	restarted.QueuedLaunchAuthorization = func(context.Context, model.TenantID, auth.QueuedCredential, string, model.ID) (auth.Principal, error) {
		return auth.Principal{}, auth.ErrUnauthenticated
	}
	if err := restarted.Start(ctx); err != nil {
		t.Fatal(err)
	}
	restarted.rt.approvalWG.Wait()
	if d, err := restarted.getRun(ctx, tenant, run.RunRef); err != nil || d.State != stateStopped || d.Reason != engineStoppedReason {
		t.Fatalf("run of a launcher who is gone: state %q reason %q (%v), want it stopped by the engine", d.State, d.Reason, err)
	}
	if got := launchCount(runner) - before; got != 0 {
		t.Fatalf("launches for a launcher who is gone = %d, want 0", got)
	}
	if !strings.Contains(logs.String(), run.RunRef) || !strings.Contains(logs.String(), "credential no longer authorizes") {
		t.Fatalf("the refusal must name the run and say why in the log: %s", logs.String())
	}
}

// A run that records no launcher credential (one launched before the engine kept it, or
// with no credential at all) cannot be revalidated, so it stays stopped and the log says so.
func TestARunWithoutALauncherCredentialStaysStoppedAtTheNextStart(t *testing.T) {
	ctx := context.Background()
	runner := &fakeRunner{initSID: "engine-stop-legacy"}
	options := []Option{WithSessionWorkspaceRoot(t.TempDir()), WithRunner(runner), WithCredentialSource(staticCred())}
	m, st, tenant, _ := newRuntimeHarness(t, options...)
	run, err := createProfiledTestRun(t, m, ctx, tenant, CreateRunParams{Transport: TransportStreamJSON, Isolation: IsolationNative, Actor: actorU, ActorKind: actorKindU})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "conversation id", func() bool { d, _ := m.getRun(ctx, tenant, run.RunRef); return d.ClaudeSessionID != "" })
	if err := m.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	before := launchCount(runner)

	var logs bytes.Buffer
	restarted := restartModule(t, m, st, tenant, options...)
	restarted.log = slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	if err := restarted.Start(ctx); err != nil {
		t.Fatal(err)
	}
	restarted.rt.approvalWG.Wait()
	if d, err := restarted.getRun(ctx, tenant, run.RunRef); err != nil || d.State != stateStopped || d.Reason != engineStoppedReason {
		t.Fatalf("run with no launcher credential: state %q reason %q (%v), want it stopped by the engine", d.State, d.Reason, err)
	}
	if got := launchCount(runner) - before; got != 0 {
		t.Fatalf("launches for a run with no launcher credential = %d, want 0", got)
	}
	if !strings.Contains(logs.String(), run.RunRef) || !strings.Contains(logs.String(), "records no launcher credential") {
		t.Fatalf("the refusal must name the run and say why in the log: %s", logs.String())
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

// review on e97d5e7c: the same managed row goes from ended back to live when the session
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

// approvalGate sends every launch to a human approval, which a resume answers with a 409.
type approvalGate struct{}

func (approvalGate) Authorize(context.Context, model.TenantID, LaunchIntent) (LaunchDecision, error) {
	return LaunchDecision{DeniedStatus: http.StatusAccepted, ApprovalRef: "approval-engine-resume"}, nil
}

// A resume the launch gate sends to a human answers 409, as does a person who got there
// first. Only the second is silent: this run is still stopped for the engine, so the log must
// name it, or it stays stopped at every start with no trace.
func TestAConflictThatKeepsARunStoppedForTheEngineIsLogged(t *testing.T) {
	ctx := context.Background()
	runner := &fakeRunner{initSID: "engine-stop-approval"}
	options := []Option{WithSessionWorkspaceRoot(t.TempDir()), WithRunner(runner), WithCredentialSource(staticCred())}
	m, st, tenant, _ := newRuntimeHarness(t, options...)
	m.QueuedCredentialCapture = sealCredential
	run, err := createProfiledTestRun(t, m, ctx, tenant, CreateRunParams{Transport: TransportStreamJSON, Isolation: IsolationNative, Actor: actorU, ActorKind: actorKindU, queuedCredential: launchCredential(model.NewID())})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "conversation id", func() bool { d, _ := m.getRun(ctx, tenant, run.RunRef); return d.ClaudeSessionID != "" })
	if err := m.Stop(ctx); err != nil {
		t.Fatal(err)
	}

	var logs bytes.Buffer
	restarted := restartModule(t, m, st, tenant, append(slices.Clone(options), WithLaunchGate(approvalGate{}))...)
	restarted.log = slog.New(slog.NewTextHandler(&logs, nil))
	if err := restarted.Start(ctx); err != nil {
		t.Fatal(err)
	}
	restarted.rt.approvalWG.Wait()
	if d, err := restarted.getRun(ctx, tenant, run.RunRef); err != nil || d.State != stateStopped || d.Reason != engineStoppedReason {
		t.Fatalf("run whose resume needs an approval: state %q reason %q (%v), want it stopped by the engine", d.State, d.Reason, err)
	}
	if !strings.Contains(logs.String(), run.RunRef) || !strings.Contains(logs.String(), "approval") {
		t.Fatalf("a run left stopped by a 409 must be named in the log with its cause: %s", logs.String())
	}
}

// The pass lists the engine's runs first and resumes them one by one, so a run a person
// stopped in between is no longer the engine's to resume: it stays stopped.
func TestTheEnginesResumeLeavesARunThatNoLongerAwaitsIt(t *testing.T) {
	ctx := context.Background()
	runner := &fakeRunner{initSID: "engine-stop-person"}
	options := []Option{WithSessionWorkspaceRoot(t.TempDir()), WithRunner(runner), WithCredentialSource(staticCred())}
	m, st, tenant, _ := newRuntimeHarness(t, options...)
	m.QueuedCredentialCapture = sealCredential
	launcher := auth.Principal{Kind: auth.KindUser, UserID: model.NewID()}
	run, err := createProfiledTestRun(t, m, ctx, tenant, CreateRunParams{Transport: TransportStreamJSON, Isolation: IsolationNative, Actor: actorU, ActorKind: actorKindU, queuedCredential: launchCredential(launcher.UserID)})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "conversation id", func() bool { d, _ := m.getRun(ctx, tenant, run.RunRef); return d.ClaudeSessionID != "" })
	if _, err := m.stopRun(ctx, tenant, run.RunRef, actorU, actorKindU); err != nil {
		t.Fatal(err)
	}
	if err := m.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	before := launchCount(runner)

	restarted := restartModule(t, m, st, tenant, options...)
	if err := restarted.Start(ctx); err != nil {
		t.Fatal(err)
	}
	restarted.rt.approvalWG.Wait()
	_, err = restarted.resumeRun(withEngineResume(ctx, launcher), tenant, run.RunRef, launcher.Actor(), model.ActorUser, "")
	if !isRunConflict(err) {
		t.Fatalf("the engine's resume of a run a person stopped = %v, want a conflict", err)
	}
	if got := launchCount(runner) - before; got != 0 {
		t.Fatalf("launches for a run a person stopped = %d, want 0", got)
	}
	if d, err := restarted.getRun(ctx, tenant, run.RunRef); err != nil || d.State != stateStopped || d.Reason == engineStoppedReason {
		t.Fatalf("run a person stopped: state %q reason %q (%v), want it stopped for that person's reason", d.State, d.Reason, err)
	}
}

// A launch whose credential cannot be kept still starts, and does not come back by itself:
// the run records no credential, so it stays stopped at the next start and the log says why.
func TestALaunchWhoseCredentialCouldNotBeKeptStaysStoppedAtTheNextStart(t *testing.T) {
	ctx := context.Background()
	runner := &fakeRunner{initSID: "engine-stop-unbound"}
	options := []Option{WithSessionWorkspaceRoot(t.TempDir()), WithRunner(runner), WithCredentialSource(staticCred())}
	m, st, tenant, _ := newRuntimeHarness(t, options...)
	m.QueuedCredentialCapture = func(context.Context, auth.QueuedCredential) (auth.QueuedCredential, error) {
		return auth.QueuedCredential{}, fmt.Errorf("auth store unavailable")
	}
	run, err := createProfiledTestRun(t, m, ctx, tenant, CreateRunParams{Transport: TransportStreamJSON, Isolation: IsolationNative, Actor: actorU, ActorKind: actorKindU, queuedCredential: launchCredential(model.NewID())})
	if err != nil {
		t.Fatalf("a launch whose credential cannot be kept must still start: %v", err)
	}
	waitFor(t, "conversation id", func() bool { d, _ := m.getRun(ctx, tenant, run.RunRef); return d.ClaudeSessionID != "" })
	if err := m.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	before := launchCount(runner)

	var logs bytes.Buffer
	restarted := restartModule(t, m, st, tenant, options...)
	restarted.log = slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	if err := restarted.Start(ctx); err != nil {
		t.Fatal(err)
	}
	restarted.rt.approvalWG.Wait()
	if d, err := restarted.getRun(ctx, tenant, run.RunRef); err != nil || d.State != stateStopped || d.Reason != engineStoppedReason {
		t.Fatalf("run with no kept credential: state %q reason %q (%v), want it stopped by the engine", d.State, d.Reason, err)
	}
	if got := launchCount(runner) - before; got != 0 {
		t.Fatalf("launches for a run with no kept credential = %d, want 0", got)
	}
	if !strings.Contains(logs.String(), run.RunRef) || !strings.Contains(logs.String(), "records no launcher credential") {
		t.Fatalf("the refusal must name the run and say why in the log: %s", logs.String())
	}
}

// A run a crashed engine left behind reads stopped with the reason that opens
// engineDiedReason and a detail after it. It awaits the next start as a clean stop does;
// a run stopped for any other reason, even one that merely mentions it, stays stopped.
func TestARunTheEngineDiedWithResumesAtTheNextStart(t *testing.T) {
	ctx := context.Background()
	runner := &fakeRunner{}
	options := []Option{WithSessionWorkspaceRoot(t.TempDir()), WithRunner(runner), WithCredentialSource(staticCred())}
	m, st, tenant, _ := newRuntimeHarness(t, options...)
	m.QueuedCredentialCapture = sealCredential
	launcher := model.NewID()
	launch := func(sid string) string {
		t.Helper()
		runner.initSID = sid
		run, err := createProfiledTestRun(t, m, ctx, tenant, CreateRunParams{Transport: TransportStreamJSON, Isolation: IsolationNative, Actor: actorU, ActorKind: actorKindU, queuedCredential: launchCredential(launcher)})
		if err != nil {
			t.Fatal(err)
		}
		waitFor(t, "conversation id", func() bool { d, _ := m.getRun(ctx, tenant, run.RunRef); return d.ClaudeSessionID == sid })
		return run.RunRef
	}
	died, diedBare, mentions, shouted := launch("engine-died-conversation"), launch("engine-died-bare"), launch("engine-died-mention"), launch("engine-died-case")
	if err := m.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	reasons := map[string]string{
		died:     engineDiedReason + " (runtime handle lost; process not confirmed terminated)",
		diedBare: engineDiedReason,
		mentions: "stopped by a person; " + engineDiedReason,
		shouted:  strings.ToUpper(engineDiedReason), // the store's LIKE may match it; the reason is not the engine's
	}
	for ref, reason := range reasons {
		mutateRunForCredentialTest(t, m.Data, tenant, ref, func(rec model.Record) { rec[colReason] = reason })
	}
	before := launchCount(runner)
	runner.initSID = ""

	var logs bytes.Buffer
	var revalidated int // read after the pass ends (approvalWG.Wait)
	restarted := restartModule(t, m, st, tenant, options...)
	restarted.log = slog.New(slog.NewTextHandler(&logs, nil))
	restarted.QueuedLaunchAuthorization = func(_ context.Context, _ model.TenantID, c auth.QueuedCredential, _ string, _ model.ID) (auth.Principal, error) {
		revalidated++
		return auth.Principal{Kind: auth.KindUser, UserID: c.UserID}, nil
	}
	if err := restarted.Start(ctx); err != nil {
		t.Fatal(err)
	}
	for _, ref := range []string{died, diedBare} {
		waitFor(t, "run "+ref+" running again", func() bool {
			d, err := restarted.getRun(ctx, tenant, ref)
			return err == nil && d.State == stateRunning
		})
	}
	restarted.rt.approvalWG.Wait()
	if got := launchCount(runner) - before; got != 2 || revalidated != 2 {
		t.Fatalf("launches at the restart = %d, credentials revalidated = %d, want 2 and 2: the two runs the engine died with", got, revalidated)
	}
	if strings.Contains(logs.String(), "cannot resume by themselves") {
		t.Fatalf("every run that was listed resumed, yet a warning counts runs left waiting: %s", logs.String())
	}
	for _, ref := range []string{mentions, shouted} {
		if d, err := restarted.getRun(ctx, tenant, ref); err != nil || d.State != stateStopped || d.Reason != reasons[ref] {
			t.Fatalf("a run stopped for another reason: state %q reason %q (%v), want it left stopped", d.State, d.Reason, err)
		}
	}
	for _, ref := range []string{died, diedBare} {
		lines := 0
		for _, e := range listRunEvents(t, st, tenant, ref) {
			if e.Event == "resumed" && strings.HasPrefix(e.Detail, "resumed automatically after the engine stopped") {
				lines++
			}
		}
		if lines != 1 {
			t.Fatalf("run %s: ledger lines recording the automatic resume = %d, want 1", ref, lines)
		}
	}
}

// The reasons the engine writes are told apart from the reasons a person or a policy writes:
// the clean stop in full, the crash by its opening. The same answer decides what the pass
// resumes, what a refused resume keeps, and what blocks an account's retirement.
func TestOnlyTheEnginesOwnReasonsAwaitItsResume(t *testing.T) {
	for _, tc := range []struct {
		name, reason string
		want         bool
	}{
		{"the clean stop", engineStoppedReason, true},
		{"the crash with its detail", engineDiedReason + " (runtime handle lost; process not confirmed terminated)", true},
		{"the crash without a detail", engineDiedReason, true},
		{"a clean stop with something added", engineStoppedReason + " (by hand)", false},
		{"a reason that mentions the crash", "stopped by a person; " + engineDiedReason, false},
		{"the crash cut short", strings.TrimSuffix(engineDiedReason, " to continue"), false},
		{"a person's stop", "stopped by user", false},
		{"no reason", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := endedByTheEngine(tc.reason); got != tc.want {
				t.Fatalf("endedByTheEngine(%q) = %v, want %v", tc.reason, got, tc.want)
			}
			row := model.Record{colState: stateStopped, colReason: tc.reason, colRunResumeCredentialSeal: "seal"}
			if got := awaitsEngineResume(row); got != tc.want {
				t.Fatalf("awaitsEngineResume(reason %q) = %v, want %v", tc.reason, got, tc.want)
			}
		})
	}
}

// A resume the launch gates refuse keeps the reason of a run the engine died with, as it keeps
// the clean stop's: the next start finds the run and tries it again.
func TestAResumeTheGatesRefuseKeepsTheReasonOfARunTheEngineDiedWith(t *testing.T) {
	ctx := context.Background()
	runner := &fakeRunner{initSID: "engine-died-gated"}
	options := []Option{WithSessionWorkspaceRoot(t.TempDir()), WithRunner(runner), WithCredentialSource(staticCred())}
	m, st, tenant, _ := newRuntimeHarness(t, options...)
	m.QueuedCredentialCapture = sealCredential
	run, err := createProfiledTestRun(t, m, ctx, tenant, CreateRunParams{Transport: TransportStreamJSON, Isolation: IsolationNative, Actor: actorU, ActorKind: actorKindU, queuedCredential: launchCredential(model.NewID())})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "conversation id", func() bool { d, _ := m.getRun(ctx, tenant, run.RunRef); return d.ClaudeSessionID != "" })
	if err := m.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	died := engineDiedReason + " (runtime handle lost; process not confirmed terminated)"
	mutateRunForCredentialTest(t, m.Data, tenant, run.RunRef, func(rec model.Record) { rec[colReason] = died })

	refused := restartModule(t, m, st, tenant, append(slices.Clone(options), WithLaunchGate(denyGate{reason: "budget spent"}))...)
	if err := refused.Start(ctx); err != nil {
		t.Fatal(err)
	}
	refused.rt.approvalWG.Wait()
	if d, err := refused.getRun(ctx, tenant, run.RunRef); err != nil || d.State != stateStopped || d.Reason != died {
		t.Fatalf("run after a refused resume: state %q reason %q (%v), want it still stopped with %q", d.State, d.Reason, err, died)
	}
	if err := refused.Stop(ctx); err != nil {
		t.Fatal(err)
	}

	again := restartModule(t, m, st, tenant, options...)
	if err := again.Start(ctx); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the run resumes once the gate allows it", func() bool {
		d, err := again.getRun(ctx, tenant, run.RunRef)
		return err == nil && d.State == stateRunning
	})
}

// A launcher whose credential is gone is a run only a person can resume (counted in the one
// summary); a launcher that could not be asked at all (the directory is down) is not: that run
// is tried again at the next start, and says so in its own warning.
func TestARunWhoseLauncherCouldNotBeAskedIsNotCountedAsWaitingForAPerson(t *testing.T) {
	ctx := context.Background()
	runner := &fakeRunner{}
	options := []Option{WithSessionWorkspaceRoot(t.TempDir()), WithRunner(runner), WithCredentialSource(staticCred())}
	m, st, tenant, _ := newRuntimeHarness(t, options...)
	m.QueuedCredentialCapture = sealCredential
	gone, down := launchCredential(model.NewID()), launchCredential(model.NewID())
	launch := func(sid string, c auth.QueuedCredential) string {
		t.Helper()
		runner.initSID = sid
		run, err := createProfiledTestRun(t, m, ctx, tenant, CreateRunParams{Transport: TransportStreamJSON, Isolation: IsolationNative, Actor: actorU, ActorKind: actorKindU, queuedCredential: c})
		if err != nil {
			t.Fatal(err)
		}
		waitFor(t, "conversation id", func() bool { d, _ := m.getRun(ctx, tenant, run.RunRef); return d.ClaudeSessionID == sid })
		return run.RunRef
	}
	goneRun, downRun := launch("engine-stop-gone-login", gone), launch("engine-stop-directory-down", down)
	if err := m.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	before := launchCount(runner)

	var logs bytes.Buffer
	restarted := restartModule(t, m, st, tenant, options...)
	restarted.log = slog.New(slog.NewTextHandler(&logs, nil))
	restarted.QueuedLaunchAuthorization = func(_ context.Context, _ model.TenantID, c auth.QueuedCredential, _ string, _ model.ID) (auth.Principal, error) {
		if c.ID == gone.ID {
			return auth.Principal{}, store.ErrNotFound
		}
		return auth.Principal{}, fmt.Errorf("directory down")
	}
	if err := restarted.Start(ctx); err != nil {
		t.Fatal(err)
	}
	restarted.rt.approvalWG.Wait()
	for _, ref := range []string{goneRun, downRun} {
		if d, err := restarted.getRun(ctx, tenant, ref); err != nil || d.State != stateStopped || d.Reason != engineStoppedReason {
			t.Fatalf("run %s: state %q reason %q (%v), want it stopped by the engine", ref, d.State, d.Reason, err)
		}
	}
	if got := launchCount(runner) - before; got != 0 {
		t.Fatalf("launches while no launcher could be restored = %d, want 0", got)
	}
	out := logs.String()
	if got := strings.Count(out, "could not have its launcher restored"); got != 1 || !strings.Contains(out, downRun) || strings.Contains(out, goneRun) {
		t.Fatalf("want one warning naming only the run whose launcher could not be asked: %s", out)
	}
	if !strings.Contains(out, "runs=1") {
		t.Fatalf("want the summary to count only the run whose login is gone (runs=1): %s", out)
	}
}

// A fenced caller's stop reason becomes the run's reason, so it must not be able to say the
// engine stopped the run: the next start would resume it as its launcher.
func TestAStopForWorkCannotUseTheEnginesReason(t *testing.T) {
	t.Parallel()

	fx := newRuntimeWorkControlFixture(t)
	ctx := context.Background()
	for _, reason := range []string{engineStoppedReason, engineDiedReason, engineDiedReason + " (runtime handle lost)"} {
		if err := fx.m.StopForWork(ctx, fx.tenant, fx.runRef, fx.lease.Fence, reason); err == nil {
			t.Fatalf("StopForWork accepted the engine's own reason %q", reason)
		}
	}
	if got := fx.proc.stopCount(); got != 0 {
		t.Fatalf("a refused reason still called Process.Stop %d time(s)", got)
	}
	if err := fx.m.StopForWork(ctx, fx.tenant, fx.runRef, fx.lease.Fence, "operator requested"); err != nil {
		t.Fatalf("an ordinary reason: %v", err)
	}
	d, err := fx.m.getRun(ctx, fx.tenant, fx.runRef)
	if err != nil || endedByTheEngine(d.Reason) {
		t.Fatalf("run reason after the stop = %q (%v), must not read as the engine's", d.Reason, err)
	}
}

// The engine resumes a token-launched run as its launcher, and the ledger names the
// launcher the way a person's own resume through the API does: actor token:<id>, kind token.
func TestTheEnginesResumeOfATokenLaunchedRunRecordsTheTokenAsItsActor(t *testing.T) {
	ctx := context.Background()
	runner := &fakeRunner{initSID: "engine-stop-token"}
	options := []Option{WithSessionWorkspaceRoot(t.TempDir()), WithRunner(runner), WithCredentialSource(staticCred())}
	m, st, tenant, _ := newRuntimeHarness(t, options...)
	m.QueuedCredentialCapture = sealCredential
	token := auth.QueuedCredential{ID: model.NewID(), UserID: model.NewID(), Kind: auth.KindToken, Version: 1}
	run, err := createProfiledTestRun(t, m, ctx, tenant, CreateRunParams{Transport: TransportStreamJSON, Isolation: IsolationNative, Actor: actorU, ActorKind: actorKindU, queuedCredential: token})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "conversation id", func() bool { d, _ := m.getRun(ctx, tenant, run.RunRef); return d.ClaudeSessionID != "" })
	if err := m.Stop(ctx); err != nil {
		t.Fatal(err)
	}

	restarted := restartModule(t, m, st, tenant, options...)
	restarted.QueuedLaunchAuthorization = func(_ context.Context, _ model.TenantID, c auth.QueuedCredential, _ string, _ model.ID) (auth.Principal, error) {
		return auth.Principal{Kind: auth.KindToken, UserID: c.UserID, CredID: c.ID}, nil
	}
	if err := restarted.Start(ctx); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the token's run running again", func() bool {
		d, err := restarted.getRun(ctx, tenant, run.RunRef)
		return err == nil && d.State == stateRunning
	})
	restarted.rt.approvalWG.Wait()
	found := false
	for _, e := range listRunEvents(t, st, tenant, run.RunRef) {
		if e.Event == "resumed" && strings.HasPrefix(e.Detail, "resumed automatically") {
			found = true
			if e.Actor != "token:"+token.ID.String() || e.ActorKind != "token" {
				t.Fatalf("automatic resume recorded actor %q kind %q, want token:%s kind token", e.Actor, e.ActorKind, token.ID)
			}
		}
	}
	if !found {
		t.Fatal("no ledger line records the automatic resume")
	}
}

// Runs that can never resume by themselves are one warning per start, not one per run:
// after an upgrade there can be many, and every boot would list them again.
func TestRunsThatCannotResumeByThemselvesAreOneWarningAtEachStart(t *testing.T) {
	ctx := context.Background()
	runner := &fakeRunner{}
	options := []Option{WithSessionWorkspaceRoot(t.TempDir()), WithRunner(runner), WithCredentialSource(staticCred())}
	m, st, tenant, _ := newRuntimeHarness(t, options...)
	var refs []string
	for i := range 3 {
		runner.initSID = fmt.Sprintf("engine-stop-nocred-%d", i)
		run, err := createProfiledTestRun(t, m, ctx, tenant, CreateRunParams{Transport: TransportStreamJSON, Isolation: IsolationNative, Actor: actorU, ActorKind: actorKindU})
		if err != nil {
			t.Fatal(err)
		}
		waitFor(t, "conversation id", func() bool { d, _ := m.getRun(ctx, tenant, run.RunRef); return d.ClaudeSessionID != "" })
		refs = append(refs, run.RunRef)
	}
	if err := m.Stop(ctx); err != nil {
		t.Fatal(err)
	}

	var logs bytes.Buffer
	restarted := restartModule(t, m, st, tenant, options...)
	restarted.log = slog.New(slog.NewTextHandler(&logs, nil)) // the default level: warnings and up
	if err := restarted.Start(ctx); err != nil {
		t.Fatal(err)
	}
	restarted.rt.approvalWG.Wait()
	if got := strings.Count(logs.String(), "cannot resume by themselves"); got != 1 || !strings.Contains(logs.String(), "runs=3") {
		t.Fatalf("summary warnings = %d, want one saying runs=3: %s", got, logs.String())
	}
	for _, ref := range refs {
		if strings.Contains(logs.String(), ref) {
			t.Fatalf("run %s is named in a warning; the summary counts them: %s", ref, logs.String())
		}
	}
}
