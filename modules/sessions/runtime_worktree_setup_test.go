// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"errors"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/sessions/confine"
)

// setupRunner plays the setup command's process. A /bin/sh launch finishes at once with
// the scripted status and output, or runs for real when real is set; every other launch
// is the session's and goes to the fake runner that plays the session.
type setupRunner struct {
	session *fakeRunner
	real    Runner
	status  int
	output  string
	// launchErr is returned with a synthetic process, as Runner allows.
	launchErr error
	// onSetup runs when a setup launch starts, before its process is played; onStop runs
	// when that process is stopped. waitForContext leaves the process live until the
	// launch context ends, as a command that ignores its own work does.
	onSetup        func(LaunchSpec)
	onStop         func()
	stopErr        error
	waitForContext bool
	// guardCleanupFailed is returned through the fake process's typed metadata.
	guardCleanupFailed bool
	// stopLeavesLive keeps the fake process's Wait blocked after Stop. startRunning
	// suppresses the initial finish too, independently of context cancellation.
	stopLeavesLive bool
	startRunning   bool
	// waitErr is the fake setup process's collection error, returned by Wait.
	waitErr error
	last    *fakeProc
	mu      sync.Mutex
	setups  []setupLaunch
}

// setupLaunch is one setup launch and how many session launches had happened before it.
type setupLaunch struct {
	spec           LaunchSpec
	sessionsBefore int
}

func (r *setupRunner) Launch(ctx context.Context, spec LaunchSpec) (Process, error) {
	if spec.Program != worktreeSetupShell {
		return r.session.Launch(ctx, spec)
	}
	r.mu.Lock()
	r.setups = append(r.setups, setupLaunch{spec: spec, sessionsBefore: len(r.session.specs)})
	r.mu.Unlock()
	if r.onSetup != nil {
		r.onSetup(spec)
	}
	if r.real != nil {
		return r.real.Launch(ctx, spec)
	}
	p := &fakeProc{out: make(chan OutputFrame, 1), stopped: make(chan struct{}), onStop: r.onStop, stopErr: r.stopErr, guardCleanupFailed: r.guardCleanupFailed, stopLeavesLive: r.stopLeavesLive, waitErr: r.waitErr}
	if r.output != "" {
		p.out <- OutputFrame{Stream: streamStdout, Data: []byte(r.output)}
	}
	// Published once its output is queued: a test that finishes the process as soon as it
	// sees it must not close the channel under this send.
	r.mu.Lock()
	r.last = p
	r.mu.Unlock()
	if r.waitForContext {
		go func() {
			<-ctx.Done()
			p.finish(r.status)
		}()
	} else if !r.startRunning {
		p.finish(r.status)
	}
	if r.launchErr != nil {
		return p, r.launchErr
	}
	return p, nil
}

func (r *setupRunner) setupLaunches() []setupLaunch {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]setupLaunch(nil), r.setups...)
}

// newSetupHarness is a worktree harness whose setup launches go through runner and whose
// session launches go through the harness's own fake.
func newSetupHarness(t *testing.T, runner *setupRunner, opts ...Option) (*worktreeHarness, context.Context) {
	t.Helper()
	h, ctx := newWorktreeHarness(t, append([]Option{WithRunner(runner)}, opts...)...)
	runner.session = h.fr
	return h, ctx
}

// Real setup processes require the same Linux confinement as production. Tests
// that exercise the no-policy refusal use newSetupHarness deliberately instead.
func newConfinedSetupHarness(t *testing.T, runner *setupRunner) (*worktreeHarness, context.Context) {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("real setup processes require the Linux session guard")
	}
	if s := confine.Probe(); s.Mode != confine.ModeLandlock {
		t.Skip("host cannot confine: " + s.Reason)
	}
	return newSetupHarness(t, runner, WithConfinement([]string{t.TempDir()}, false))
}

// An approved setup never falls back to the engine user's unconfined authority:
// without a confinement policy, the native runner refuses it with the existing 502.
func TestWorktreeSetupRefusesWhenConfinementIsUnavailable(t *testing.T) {
	runner := &setupRunner{real: NewProcRunner()}
	h, ctx := newSetupHarness(t, runner)
	h.approveSetup("true")

	_, err := createProfiledTestRun(t, h.m, ctx, h.tenant, h.params(true))
	wantRunErr(t, err, http.StatusBadGateway, "no session confinement policy")
	if len(h.fr.specs) != 0 {
		t.Fatalf("a session started after the unconfined setup was refused: %d launch(es)", len(h.fr.specs))
	}
}

// approveSetup stores the command text and then approves its exact SHA-256, as a person
// does through the API.
func (h *worktreeHarness) approveSetup(command string) {
	h.t.Helper()
	ctx := context.Background()
	if _, err := h.m.configureWorkspaceSetup(ctx, h.tenant, h.ws, &command, "", "user:u1", "user"); err != nil {
		h.t.Fatalf("store setup command: %v", err)
	}
	if _, err := h.m.configureWorkspaceSetup(ctx, h.tenant, h.ws, nil, sha256Hex([]byte(command)), "user:u1", "user"); err != nil {
		h.t.Fatalf("approve setup command: %v", err)
	}
}

// The setup of a new worktree runs once, in its own directory, before the session's
// first launch, and a resume of that session does not run it again.
func TestWorktreeSetupRunsOnceBeforeTheFirstTurnAndNotOnResume(t *testing.T) {
	runner := &setupRunner{}
	h, ctx := newSetupHarness(t, runner)
	const command = "echo ready > .setup-ran"
	h.approveSetup(command)

	dto := h.launch(ctx, true)
	setups := runner.setupLaunches()
	if len(setups) != 1 {
		t.Fatalf("setup launches = %d, want 1", len(setups))
	}
	setup := setups[0]
	if setup.sessionsBefore != 0 {
		t.Fatalf("setup ran after %d session launch(es), want before the first turn", setup.sessionsBefore)
	}
	if got := strings.Join(setup.spec.Args, "\x00"); got != "-c\x00"+command {
		t.Fatalf("setup argv = %q, want the approved text as one command", setup.spec.Args)
	}
	if filepath.Base(setup.spec.Dir) != dto.RunRef {
		t.Fatalf("setup ran in %q, want the session's own worktree %s", setup.spec.Dir, dto.RunRef)
	}
	if len(setup.spec.Env) != 0 {
		t.Fatalf("setup received %d environment variable(s); it must get no session credential", len(setup.spec.Env))
	}
	session := h.fr.specs[0]
	if setup.spec.Isolation != session.Isolation || !reflect.DeepEqual(setup.spec.NetworkPolicy, session.NetworkPolicy) {
		t.Fatalf("setup isolation or network policy differs from the session's: setup %+v, session %+v", setup.spec, session)
	}
	recorded := false
	for _, ev := range listRunEvents(t, h.st, h.tenant, dto.RunRef) {
		recorded = recorded || (ev.Event == worktreeSetupEvent && strings.Contains(ev.Detail, "finished with status 0"))
	}
	if !recorded {
		t.Fatalf("a finished setup left no worktree_setup event on the run's ledger")
	}

	h.stop(ctx, dto.RunRef)
	if _, err := h.resume(ctx, dto.RunRef); err != nil {
		t.Fatalf("resume: %v", err)
	}
	if n := len(runner.setupLaunches()); n != 1 {
		t.Fatalf("setup launches after resume = %d, want still 1", n)
	}
}

// Text that was never approved, or that changed after its approval, never reaches a
// process: the launch is refused before the worktree is made.
func TestWorktreeSetupNeverRunsUnapprovedOrChangedText(t *testing.T) {
	runner := &setupRunner{}
	h, ctx := newSetupHarness(t, runner)
	command := "echo approved"
	if _, err := h.m.configureWorkspaceSetup(ctx, h.tenant, h.ws, &command, "", "user:u1", "user"); err != nil {
		t.Fatal(err)
	}
	_, err := createProfiledTestRun(t, h.m, ctx, h.tenant, h.params(true))
	wantRunErr(t, err, http.StatusConflict, "needs approval")

	h.approveSetup(command)
	changed := command + " "
	if _, err := h.m.configureWorkspaceSetup(ctx, h.tenant, h.ws, &changed, "", "user:u1", "user"); err != nil {
		t.Fatal(err)
	}
	_, err = createProfiledTestRun(t, h.m, ctx, h.tenant, h.params(true))
	wantRunErr(t, err, http.StatusConflict, "needs approval")

	if n := len(runner.setupLaunches()); n != 0 {
		t.Fatalf("setup launches = %d for text that is not approved, want 0", n)
	}
	if len(h.fr.specs) != 0 {
		t.Fatalf("a session launched for a refused setup: %d launch(es)", len(h.fr.specs))
	}
}

// A non-zero exit stops the start, refuses with the command's last output, and keeps that
// output as a worktree_setup event on the run's ledger.
func TestWorktreeSetupNonZeroExitStopsTheStartAndKeepsItsOutput(t *testing.T) {
	runner := &setupRunner{status: 3, output: "resolving\nerror: no lockfile\n"}
	h, ctx := newSetupHarness(t, runner)
	h.approveSetup("make setup")

	_, err := createProfiledTestRun(t, h.m, ctx, h.tenant, h.params(true))
	wantRunErr(t, err, http.StatusUnprocessableEntity, "exited with status 3")
	if !strings.Contains(err.Error(), "error: no lockfile") {
		t.Fatalf("the refusal %q must keep the command's last output", err.Error())
	}
	if len(h.fr.specs) != 0 {
		t.Fatalf("a session started after its setup failed: %d launch(es)", len(h.fr.specs))
	}
	runRef := filepath.Base(runner.setupLaunches()[0].spec.Dir)
	events := listRunEvents(t, h.st, h.tenant, runRef)
	for _, ev := range events {
		if ev.Event == worktreeSetupEvent && strings.Contains(ev.Detail, "exited with status 3") && strings.Contains(ev.Detail, "error: no lockfile") {
			return
		}
	}
	t.Fatalf("no worktree_setup event with the exit status and output on %s: %+v", runRef, events)
}

// A process whose outcome cannot be collected stops the start: an unknown exit is never
// treated as a success, and no session starts on it.
func TestWorktreeSetupCollectionErrorStopsTheStart(t *testing.T) {
	runner := &setupRunner{waitErr: errors.New("collection fixture")}
	h, ctx := newSetupHarness(t, runner)
	h.approveSetup("true")

	_, err := createProfiledTestRun(t, h.m, ctx, h.tenant, h.params(true))
	wantRunErr(t, err, http.StatusUnprocessableEntity, "could not be collected")
	if len(h.fr.specs) != 0 {
		t.Fatalf("a session started although its setup outcome was unknown: %d launch(es)", len(h.fr.specs))
	}
}

// A command that runs past the time limit is stopped and refuses the start.
func TestWorktreeSetupTimeLimitStopsARunawayCommand(t *testing.T) {
	restore := worktreeSetupTimeout
	worktreeSetupTimeout = 300 * time.Millisecond
	t.Cleanup(func() { worktreeSetupTimeout = restore })
	runner := &setupRunner{real: NewProcRunner()}
	h, ctx := newConfinedSetupHarness(t, runner)
	h.approveSetup("sleep 30")

	started := time.Now()
	_, err := createProfiledTestRun(t, h.m, ctx, h.tenant, h.params(true))
	if elapsed := time.Since(started); elapsed > 20*time.Second {
		t.Fatalf("a setup stopped at 300ms took %s", elapsed)
	}
	wantRunErr(t, err, http.StatusUnprocessableEntity, "time limit")
	if len(h.fr.specs) != 0 {
		t.Fatalf("a session started after its setup timed out: %d launch(es)", len(h.fr.specs))
	}
}

// The command runs under the session's confinement: a write inside the worktree succeeds,
// and a write outside the folder is refused by the host, not by the command.
func TestWorktreeSetupRunsUnderTheSessionConfinement(t *testing.T) {
	outside := filepath.Join(t.TempDir(), "outside.txt")
	runner := &setupRunner{real: NewProcRunner()}
	h, ctx := newConfinedSetupHarness(t, runner)
	h.approveSetup("echo inside > inside.txt && echo outside > " + outside)

	_, err := createProfiledTestRun(t, h.m, ctx, h.tenant, h.params(true))
	wantRunErr(t, err, http.StatusUnprocessableEntity, "exited with status")
	if _, statErr := os.Stat(outside); !errors.Is(statErr, fs.ErrNotExist) {
		t.Fatalf("the setup wrote outside its folder: %v", statErr)
	}
	worktree := runner.setupLaunches()[0].spec.Dir
	if _, statErr := os.Stat(filepath.Join(worktree, "inside.txt")); statErr != nil {
		t.Fatalf("the setup could not write inside its worktree: %v", statErr)
	}
}

// The setup shares the session's folder but not its account home: the profile's home is
// granted to the session's own confinement, and a setup that writes into it is refused
// by the host while the same command writes inside its worktree.
func TestWorktreeSetupExcludesTheProfileHomeFromItsConfinement(t *testing.T) {
	runner := &setupRunner{real: NewProcRunner()}
	h, ctx := newConfinedSetupHarness(t, runner)
	// An account-home profile binds to the node's execution environment, as in production.
	h.m.UseExecutionEnvironmentRef(testEnvRef)
	home := t.TempDir()
	leak := filepath.Join(home, "leaked.txt")
	profile := mustCreateProfile(t, h.m, h.tenant, CreateProfileInput{
		Driver: "claude", AuthSource: AuthSourceAccountHome,
		ConfigHome: home, UserHome: home, DisplayName: "setup home exclusion",
	})
	h.approveSetup("echo inside > .inside.txt && echo leak > " + leak)
	p := h.params(true)
	p.ProviderProfileRef = profile.Ref

	_, err := createProfiledTestRun(t, h.m, ctx, h.tenant, p)
	wantRunErr(t, err, http.StatusUnprocessableEntity, "exited with status")
	if _, statErr := os.Stat(leak); !errors.Is(statErr, fs.ErrNotExist) {
		t.Fatalf("the setup wrote into the profile's account home: %v", statErr)
	}
	worktree := runner.setupLaunches()[0].spec.Dir
	if _, statErr := os.Stat(filepath.Join(worktree, ".inside.txt")); statErr != nil {
		t.Fatalf("the setup could not write inside its worktree: %v", statErr)
	}
}

// The permission mode's preset governs the tool's turns, not the person's approved
// command: a plan-mode worktree start still runs a writing setup.
func TestWorktreeSetupRunsWritableInPlanMode(t *testing.T) {
	runner := &setupRunner{real: NewProcRunner()}
	h, ctx := newConfinedSetupHarness(t, runner)
	h.approveSetup("echo prepared > .prepared.txt")
	p := h.params(true)
	p.PermissionMode = "plan"

	dto, err := createProfiledTestRun(t, h.m, ctx, h.tenant, p)
	if err != nil {
		t.Fatalf("a plan-mode worktree start with an approved writing setup: %v", err)
	}
	worktree := runner.setupLaunches()[0].spec.Dir
	if _, statErr := os.Stat(filepath.Join(worktree, ".prepared.txt")); statErr != nil {
		t.Fatalf("the setup could not write in plan mode: %v", statErr)
	}
	if len(h.fr.specs) != 1 {
		t.Fatalf("session launches = %d, want 1", len(h.fr.specs))
	}
	h.stop(ctx, dto.RunRef)
}

// Files the setup writes are read by the same project-configuration check as files that
// were there before it: a setup that leaves flagged agent configuration in the worktree
// stops the start before any session runs on it.
func TestWorktreeSetupFilesReachTheProjectConfigCheck(t *testing.T) {
	runner := &setupRunner{onSetup: func(spec LaunchSpec) {
		if err := os.WriteFile(filepath.Join(spec.Dir, ".mcp.json"), []byte(`{"mcpServers":{"a":{"command":"x"}}}`), 0o600); err != nil {
			t.Error(err)
		}
	}}
	h, ctx := newSetupHarness(t, runner)
	h.approveSetup("npm ci")

	_, err := createProfiledTestRun(t, h.m, ctx, h.tenant, h.params(true))
	wantRunErr(t, err, http.StatusConflict, ".mcp.json")
	if len(h.fr.specs) != 0 {
		t.Fatalf("a session started on configuration its setup wrote: %d launch(es)", len(h.fr.specs))
	}
	worktree := runner.setupLaunches()[0].spec.Dir
	if err := os.Remove(filepath.Join(worktree, ".mcp.json")); err != nil {
		t.Fatal(err)
	}
	if _, err := h.resume(ctx, filepath.Base(worktree)); err != nil {
		t.Fatalf("resume after the flagged file is removed: %v", err)
	}
	if n := len(runner.setupLaunches()); n != 1 {
		t.Fatalf("a successful setup ran again after a later launch failure: %d launches, want 1", n)
	}
}

// A worktree start that waits for an approval runs the setup once, before the session's
// first launch, when the approval lets it start.
func TestWorktreeSetupRunsForAnApprovalGatedStart(t *testing.T) {
	gate := &controlledLaunchApproval{}
	runner := &setupRunner{}
	h, ctx := newSetupHarness(t, runner, WithLaunchGate(gate))
	h.approveSetup("make setup")

	run, err := createProfiledTestRun(t, h.m, ctx, h.tenant, h.params(true))
	if err != nil || run.State != stateWaitingApproval {
		t.Fatalf("parked start = %q, err %v, want waiting_approval", run.State, err)
	}
	if n := len(runner.setupLaunches()); n != 0 {
		t.Fatalf("setup launches before the approval = %d, want 0", n)
	}
	h.m.stopApprovalWorkers(ctx)
	gate.approved.Store(true)
	if _, err := h.m.resumeRunInternal(ctx, h.tenant, run.RunRef, "", "", "", true, callerAsks{}); err != nil {
		t.Fatalf("approved start: %v", err)
	}
	setups := runner.setupLaunches()
	if len(setups) != 1 || setups[0].sessionsBefore != 0 {
		t.Fatalf("setup launches = %+v, want one before the first session launch", setups)
	}
	if filepath.Base(setups[0].spec.Dir) != run.RunRef {
		t.Fatalf("setup ran in %q, want the session's worktree %s", setups[0].spec.Dir, run.RunRef)
	}
}

// A run whose setup failed has not had a first turn: resuming it starts it again, and the
// setup runs again before the session does, so a failed setup never turns into a start
// without one.
func TestWorktreeSetupRunsAgainWhenAFailedStartIsResumed(t *testing.T) {
	runner := &setupRunner{status: 3, output: "error: no lockfile\n"}
	h, ctx := newSetupHarness(t, runner)
	h.approveSetup("make setup")

	_, err := createProfiledTestRun(t, h.m, ctx, h.tenant, h.params(true))
	wantRunErr(t, err, http.StatusUnprocessableEntity, "exited with status 3")
	runRef := filepath.Base(runner.setupLaunches()[0].spec.Dir)

	if _, err := h.resume(ctx, runRef); err == nil {
		t.Fatal("a resume whose setup fails again started the session")
	}
	if len(h.fr.specs) != 0 {
		t.Fatalf("a session started after its setup failed twice: %d launch(es)", len(h.fr.specs))
	}

	runner.status = 0
	if _, err := h.resume(ctx, runRef); err != nil {
		t.Fatalf("resume after the setup is fixed: %v", err)
	}
	setups := runner.setupLaunches()
	if len(setups) != 3 || setups[2].sessionsBefore != 0 || len(h.fr.specs) != 1 {
		t.Fatalf("setup launches = %+v, session launches = %d, want the third setup before the one session launch", setups, len(h.fr.specs))
	}
	h.stop(ctx, runRef)
	if _, err := h.resume(ctx, runRef); err != nil {
		t.Fatalf("resume of a started session: %v", err)
	}
	if n := len(runner.setupLaunches()); n != 3 {
		t.Fatalf("setup launches after the session had started = %d, want still 3", n)
	}
}

// A start that asked to skip the setup never runs it later either, when an approval
// queues the first start and the queued parameters are restored for its resume.
func TestWorktreeSetupSkippedByNoSetupStaysSkippedOnResume(t *testing.T) {
	gate := &controlledLaunchApproval{}
	runner := &setupRunner{}
	h, ctx := newSetupHarness(t, runner, WithLaunchGate(gate))
	h.approveSetup("make setup")
	p := h.params(true)
	p.NoSetup = true
	run, err := createProfiledTestRun(t, h.m, ctx, h.tenant, p)
	if err != nil || run.State != stateWaitingApproval {
		t.Fatalf("parked start = %q, err %v, want waiting_approval", run.State, err)
	}
	h.m.stopApprovalWorkers(ctx)
	gate.approved.Store(true)
	if _, err := h.m.resumeRunInternal(ctx, h.tenant, run.RunRef, "", "", "", true, callerAsks{}); err != nil {
		t.Fatalf("approved start: %v", err)
	}
	if n := len(runner.setupLaunches()); n != 0 {
		t.Fatalf("setup launches = %d for a start that skipped it, want 0", n)
	}
}

// A setup that finishes cleanly but cannot be written to the ledger stops the start: the
// session never runs on a worktree whose setup left no record.
func TestWorktreeSetupLedgerWriteFailureStopsTheStart(t *testing.T) {
	runner := &setupRunner{}
	h, ctx := newSetupHarness(t, runner)
	h.approveSetup("make setup")
	data := h.m.Data
	standby := &standbyRuntimeData{inner: data}
	runner.onSetup = func(LaunchSpec) { h.m.Data = standby }
	t.Cleanup(func() { h.m.Data = data })

	_, err := createProfiledTestRun(t, h.m, ctx, h.tenant, h.params(true))
	if err == nil {
		t.Fatal("a setup that could not be recorded let the start through")
	}
	if standby.writeCount() == 0 {
		t.Fatal("the ledger write was never attempted")
	}
	if len(h.fr.specs) != 0 {
		t.Fatalf("a session started after its setup could not be recorded: %d launch(es)", len(h.fr.specs))
	}
}

// A failed setup must leave its status and output on the ledger too. If that write
// fails, the refusal says the outcome could not be recorded instead of hiding it.
func TestWorktreeSetupFailureLedgerWriteFailureIsNotLost(t *testing.T) {
	runner := &setupRunner{status: 3, output: "error: no lockfile\n"}
	h, ctx := newSetupHarness(t, runner)
	h.approveSetup("make setup")
	data := h.m.Data
	standby := &standbyRuntimeData{inner: data}
	runner.onSetup = func(LaunchSpec) { h.m.Data = standby }
	t.Cleanup(func() { h.m.Data = data })

	_, err := createProfiledTestRun(t, h.m, ctx, h.tenant, h.params(true))
	wantRunErr(t, err, http.StatusBadGateway, "could not be recorded")
	if !strings.Contains(err.Error(), "error: no lockfile") {
		t.Fatalf("the recording failure discarded the setup output: %v", err)
	}
	if standby.writeCount() == 0 {
		t.Fatal("the failed setup's ledger write was never attempted")
	}
	if len(h.fr.specs) != 0 {
		t.Fatalf("a session started after its setup failure could not be recorded: %d launch(es)", len(h.fr.specs))
	}
}

// The setup event can fail while the later failed transition succeeds. Its typed
// cleanup outcome must survive in that transition, or a recovered store allows overlap.
func TestWorktreeSetupCleanupFlagSurvivesEventWriteFailure(t *testing.T) {
	runner := &setupRunner{guardCleanupFailed: true}
	h, ctx := newSetupHarness(t, runner)
	h.approveSetup("make setup")
	data := h.m.Data
	wrapped := &failOnceData{inner: data}
	runner.onSetup = func(LaunchSpec) { h.m.Data = wrapped }
	t.Cleanup(func() { h.m.Data = data })
	_, err := createProfiledTestRun(t, h.m, ctx, h.tenant, h.params(true))
	wantRunErr(t, err, http.StatusBadGateway, "could not be recorded")
	runRef := filepath.Base(runner.setupLaunches()[0].spec.Dir)
	h.m.Data = data

	_, resumeErr := h.resume(ctx, runRef)
	wantRunErr(t, resumeErr, http.StatusUnprocessableEntity, "cleanup was not confirmed")
	if n := len(runner.setupLaunches()); n != 1 {
		t.Fatalf("setup launches after recovered event-write failure = %d, want still 1", n)
	}
}

// Background processes the setup leaves running do not outlive it: the process group is
// stopped once the command has finished, on success as on failure.
func TestWorktreeSetupStopsWhatItLeftRunning(t *testing.T) {
	var stopped atomic.Int32
	runner := &setupRunner{onStop: func() { stopped.Add(1) }}
	h, ctx := newSetupHarness(t, runner)
	h.approveSetup("nohup sleep 100 >/dev/null 2>&1 &")

	if _, err := createProfiledTestRun(t, h.m, ctx, h.tenant, h.params(true)); err != nil {
		t.Fatal(err)
	}
	if n := stopped.Load(); n != 1 {
		t.Fatalf("setup process Stop calls after a clean exit = %d, want 1", n)
	}
}

// A secret the output limit would cut in two leaves no fragment behind: each frame is
// redacted before the tail is cut.
func TestCollectSetupOutputRedactsBeforeTheCut(t *testing.T) {
	const secret = "s3cr3t-registry-token"
	redact := newSecretRedactor([]SecretEnvRef{{Env: "TOKEN", Secret: "registry"}}, []EnvVar{{Name: "TOKEN", Value: secret}})
	frames := make(chan OutputFrame, 2)
	first := "k=" + secret
	// The second frame pushes the tail's start 10 bytes into the secret.
	frames <- OutputFrame{Stream: streamStdout, Data: []byte(first)}
	frames <- OutputFrame{Stream: streamStdout, Data: []byte(strings.Repeat("x", worktreeSetupBytes-len(first)-1+10))}
	close(frames)

	got := string(collectSetupOutput(frames, redact))
	if strings.Contains(got, secret[len(secret)-10:]) {
		t.Fatalf("the tail kept a fragment of the secret: %q", got)
	}
}

// The setup finds the session's profile programs the way the session does: their install
// directory is first on its PATH, and that is the only variable it receives.
func TestWorktreeSetupGetsTheProfileProgramsOnItsPATH(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "rea"), []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	resolve := func(driver string) string {
		if driver == "declared-rea" {
			return filepath.Join(bin, "rea")
		}
		return ""
	}
	runner := &setupRunner{}
	h, ctx := newSetupHarness(t, runner, WithProgramResolver(resolve))
	declareRegistries(t, h.m, h.tenant, nil, []string{"rea"})
	h.approveSetup("rea install")

	if _, err := createProfiledTestRun(t, h.m, ctx, h.tenant, h.params(true)); err != nil {
		t.Fatal(err)
	}
	setup := runner.setupLaunches()[0].spec
	want := []EnvVar{{Name: "PATH", Value: bin + string(os.PathListSeparator) + os.Getenv("PATH")}}
	if !reflect.DeepEqual(setup.Env, want) {
		t.Fatalf("setup env = %+v, want only the profile PATH %+v", setup.Env, want)
	}
}

// A session that started with no setup command to run never gets one later: approving a
// command after its first turn does not run it on a resume of that session.
func TestWorktreeSetupApprovedAfterTheFirstTurnDoesNotRunOnResume(t *testing.T) {
	runner := &setupRunner{}
	h, ctx := newSetupHarness(t, runner)
	dto := h.launch(ctx, true)
	h.approveSetup("make setup")
	h.stop(ctx, dto.RunRef)
	if _, err := h.resume(ctx, dto.RunRef); err != nil {
		t.Fatalf("resume: %v", err)
	}
	if n := len(runner.setupLaunches()); n != 0 {
		t.Fatalf("setup launches = %d for a session that had already started, want 0", n)
	}
}

// A worktree row written before setup-pending was recorded has no flag. Its empty
// started_at still proves the first launch never happened, so resume must not skip setup.
func TestWorktreeSetupLegacyNeverStartedRunRunsOnResume(t *testing.T) {
	gate := &controlledLaunchApproval{}
	runner := &setupRunner{}
	h, ctx := newSetupHarness(t, runner, WithLaunchGate(gate))
	h.approveSetup("make setup")
	run, err := createProfiledTestRun(t, h.m, ctx, h.tenant, h.params(true))
	if err != nil || run.State != stateWaitingApproval {
		t.Fatalf("parked start = %q, err %v, want waiting_approval", run.State, err)
	}
	mutateRunForCredentialTest(t, h.m.Data, h.tenant, run.RunRef, func(rec model.Record) {
		rec[colRunSetupPending] = nil
		rec[colStartedAt] = nil
	})
	h.m.stopApprovalWorkers(ctx)
	gate.approved.Store(true)
	if _, err := h.m.resumeRunInternal(ctx, h.tenant, run.RunRef, "", "", "", true, callerAsks{}); err != nil {
		t.Fatalf("legacy first start: %v", err)
	}
	if n := len(runner.setupLaunches()); n != 1 {
		t.Fatalf("setup launches for a legacy never-started worktree = %d, want 1", n)
	}
}

// A cleanup error after a clean exit is not a successful setup: the session cannot
// start while the runner says a setup process was not reaped.
func TestWorktreeSetupStopFailureStopsTheStart(t *testing.T) {
	var stops atomic.Int32
	runner := &setupRunner{stopErr: errors.New("unreaped fixture"), onStop: func() { stops.Add(1) }}
	h, ctx := newSetupHarness(t, runner)
	h.approveSetup("make setup")
	_, err := createProfiledTestRun(t, h.m, ctx, h.tenant, h.params(true))
	wantRunErr(t, err, http.StatusUnprocessableEntity, "could not be stopped")
	if len(h.fr.specs) != 0 {
		t.Fatalf("a session started after setup cleanup failed: %d launch(es)", len(h.fr.specs))
	}
	if n := stops.Load(); n != 2 {
		t.Fatalf("cleanup attempts after an error = %d, want setup cleanup and the caller's custody retry", n)
	}
}

// A setup whose process was not reaped stays in the existing launch-custody path:
// resume cannot run another setup or start a session while that process is unconfirmed.
func TestWorktreeSetupUnreapedProcessBlocksResume(t *testing.T) {
	runner := &setupRunner{stopErr: ErrChildNotReaped}
	h, ctx := newSetupHarness(t, runner)
	h.approveSetup("make setup")
	_, err := createProfiledTestRun(t, h.m, ctx, h.tenant, h.params(true))
	wantRunErr(t, err, http.StatusUnprocessableEntity, "could not be stopped")
	runRef := filepath.Base(runner.setupLaunches()[0].spec.Dir)

	if _, resumeErr := h.resume(ctx, runRef); resumeErr == nil {
		t.Fatal("resume forgot the unreaped setup process")
	}
	if n := len(runner.setupLaunches()); n != 1 {
		t.Fatalf("setup launches after an unreaped setup = %d, want still 1", n)
	}
	if len(h.fr.specs) != 0 {
		t.Fatalf("a session started while setup cleanup was unconfirmed: %d launch(es)", len(h.fr.specs))
	}
}

// Once the guard has exited with detached cleanup unconfirmed, no later start may
// pretend the tree is gone. Resume is refused instead of running beside it.
func TestWorktreeSetupGuardCleanupFailureBlocksResume(t *testing.T) {
	runner := &setupRunner{
		status: 0, guardCleanupFailed: true,
		output: "olivares: session guard: setup cleanup unconfirmed: procfs failed\n" + strings.Repeat("later output\n", 100),
	}
	h, ctx := newSetupHarness(t, runner)
	h.approveSetup("make setup")
	_, err := createProfiledTestRun(t, h.m, ctx, h.tenant, h.params(true))
	wantRunErr(t, err, http.StatusUnprocessableEntity, "could not be stopped")
	runRef := filepath.Base(runner.setupLaunches()[0].spec.Dir)

	_, resumeErr := h.resume(ctx, runRef)
	wantRunErr(t, resumeErr, http.StatusUnprocessableEntity, "cleanup was not confirmed")
	if n := len(runner.setupLaunches()); n != 1 {
		t.Fatalf("setup launches after guard cleanup failure = %d, want still 1", n)
	}
	if len(h.fr.specs) != 0 {
		t.Fatalf("a session started after guard cleanup failure: %d launch(es)", len(h.fr.specs))
	}
}

// Timeout cleanup carries the same typed outcome as a natural exit. A survivor
// after the guard's endTree path keeps every later resume refused.
func TestWorktreeSetupTimeoutKeepsGuardCleanupFailure(t *testing.T) {
	restore := worktreeSetupTimeout
	worktreeSetupTimeout = 50 * time.Millisecond
	t.Cleanup(func() { worktreeSetupTimeout = restore })
	runner := &setupRunner{waitForContext: true, guardCleanupFailed: true}
	h, ctx := newSetupHarness(t, runner)
	h.approveSetup("make setup")
	_, err := createProfiledTestRun(t, h.m, ctx, h.tenant, h.params(true))
	wantRunErr(t, err, http.StatusUnprocessableEntity, "time limit")
	runRef := filepath.Base(runner.setupLaunches()[0].spec.Dir)
	_, resumeErr := h.resume(ctx, runRef)
	wantRunErr(t, resumeErr, http.StatusUnprocessableEntity, "cleanup was not confirmed")
	if n := len(runner.setupLaunches()); n != 1 {
		t.Fatalf("setup launches after timeout cleanup failure = %d, want still 1", n)
	}
}

// A timeout whose tree was cleaned is not a permanent failure: after approval of a
// working command, resume runs setup and the session starts normally.
func TestWorktreeSetupTimeoutWithCleanTreeCanResume(t *testing.T) {
	restore := worktreeSetupTimeout
	t.Cleanup(func() { worktreeSetupTimeout = restore })
	worktreeSetupTimeout = 100 * time.Millisecond
	runner := &setupRunner{real: NewProcRunner()}
	h, ctx := newConfinedSetupHarness(t, runner)
	h.approveSetup("sleep 30")
	_, err := createProfiledTestRun(t, h.m, ctx, h.tenant, h.params(true))
	wantRunErr(t, err, http.StatusUnprocessableEntity, "time limit")
	runRef := filepath.Base(runner.setupLaunches()[0].spec.Dir)

	worktreeSetupTimeout = 5 * time.Second
	h.approveSetup("true")
	if _, err := h.resume(ctx, runRef); err != nil {
		t.Fatalf("resume after a cleaned timeout: %v", err)
	}
	if n := len(runner.setupLaunches()); n != 2 {
		t.Fatalf("setup launches after a cleaned timeout = %d, want 2", n)
	}
	if len(h.fr.specs) != 1 {
		t.Fatalf("session launches after a cleaned timeout = %d, want 1", len(h.fr.specs))
	}
}

// A timeout never hides an unconfirmed cleanup: the refusal carries both facts, so
// the person does not mistake a live setup process for one that ended at the limit.
func TestWorktreeSetupTimeoutReportsCleanupFailure(t *testing.T) {
	restore := worktreeSetupTimeout
	worktreeSetupTimeout = 50 * time.Millisecond
	t.Cleanup(func() { worktreeSetupTimeout = restore })
	runner := &setupRunner{waitForContext: true, stopErr: errors.New("unreaped fixture")}
	h, ctx := newSetupHarness(t, runner)
	h.approveSetup("make setup")
	_, err := createProfiledTestRun(t, h.m, ctx, h.tenant, h.params(true))
	wantRunErr(t, err, http.StatusUnprocessableEntity, "time limit")
	if !strings.Contains(err.Error(), "could not be stopped") {
		t.Fatalf("the timeout refusal hides the cleanup failure: %v", err)
	}
}

// A detached descendant is not outside the setup guard: it is killed before the
// setup finishes, even though it left the shell's process group with setsid.
func TestWorktreeSetupKillsEscapedDescendants(t *testing.T) {
	if !procfsAvailable {
		t.Skip("process tree evidence requires procfs")
	}
	runner := &setupRunner{real: NewProcRunner()}
	h, ctx := newConfinedSetupHarness(t, runner)
	h.approveSetup("setsid sh -c 'echo $$ > escaped.pid; exec sleep 30' >/dev/null 2>&1 & while [ ! -s escaped.pid ]; do sleep 0.01; done")
	if _, err := createProfiledTestRun(t, h.m, ctx, h.tenant, h.params(true)); err != nil {
		t.Fatal(err)
	}
	setups := runner.setupLaunches()
	if len(setups) != 1 || !setups[0].spec.KillTreeOnExit {
		t.Fatalf("setup did not request kill-on-exit descendant cleanup: %+v", setups)
	}
	raw, err := os.ReadFile(filepath.Join(setups[0].spec.Dir, "escaped.pid"))
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil || pid <= 0 {
		t.Fatalf("escaped pid %q: %v", raw, err)
	}
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
	if !processGoneOrZombie(pid) {
		t.Fatalf("escaped setup process %d is still running", pid)
	}
}

// The timeout also gives the guard time to clean the tree: a detached descendant
// cannot survive cancellation by killing the guard first.
func TestWorktreeSetupTimeoutKillsEscapedDescendants(t *testing.T) {
	if !procfsAvailable {
		t.Skip("process tree evidence requires procfs")
	}
	restore := worktreeSetupTimeout
	worktreeSetupTimeout = 5 * time.Second
	t.Cleanup(func() { worktreeSetupTimeout = restore })
	runner := &setupRunner{real: NewProcRunner()}
	h, ctx := newConfinedSetupHarness(t, runner)
	h.approveSetup("setsid sh -c 'echo $$ > escaped.pid; exec sleep 30' >/dev/null 2>&1 & while [ ! -s escaped.pid ]; do sleep 0.01; done; sleep 30")
	_, err := createProfiledTestRun(t, h.m, ctx, h.tenant, h.params(true))
	wantRunErr(t, err, http.StatusUnprocessableEntity, "time limit")
	setups := runner.setupLaunches()
	if len(setups) != 1 {
		t.Fatalf("setup launches = %d, want 1", len(setups))
	}
	raw, err := os.ReadFile(filepath.Join(setups[0].spec.Dir, "escaped.pid"))
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil || pid <= 0 {
		t.Fatalf("escaped pid %q: %v", raw, err)
	}
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
	if !processGoneOrZombie(pid) {
		t.Fatalf("escaped setup process %d survived the timeout", pid)
	}
}

// A process returned with a launch error is stopped, and an unconfirmed cleanup is
// included in the refusal instead of disappearing into a warning.
func TestWorktreeSetupLaunchErrorReportsCleanupFailure(t *testing.T) {
	var stops atomic.Int32
	runner := &setupRunner{
		launchErr: errors.New("launch fixture"), stopErr: errors.New("unreaped fixture"),
		onStop: func() { stops.Add(1) },
	}
	h, ctx := newSetupHarness(t, runner)
	h.approveSetup("make setup")
	_, err := createProfiledTestRun(t, h.m, ctx, h.tenant, h.params(true))
	wantRunErr(t, err, http.StatusUnprocessableEntity, "could not start")
	if !strings.Contains(err.Error(), "cleanup was not confirmed") {
		t.Fatalf("refusal %q hides the cleanup failure", err)
	}
	if len(h.fr.specs) != 0 {
		t.Fatalf("a session started after launch and cleanup failed: %d launch(es)", len(h.fr.specs))
	}
	if n := stops.Load(); n != 2 {
		t.Fatalf("Stop calls after launch error = %d, want setup cleanup plus the caller's custody retry", n)
	}
}

// An unreaped launch-error process goes straight to custody: the launch path never
// blocks in Wait or the outcome classifier before returning that live handle.
func TestWorktreeSetupLaunchErrorDoesNotWaitForUnreapedProcess(t *testing.T) {
	runner := &setupRunner{
		launchErr: errors.New("readiness fixture"), stopErr: ErrChildNotReaped,
		startRunning: true, stopLeavesLive: true,
	}
	h, ctx := newSetupHarness(t, runner)
	h.approveSetup("make setup")
	t.Cleanup(func() {
		if runner.last != nil {
			runner.last.finish(143)
		}
	})
	done := make(chan error, 1)
	go func() {
		_, err := createProfiledTestRun(t, h.m, ctx, h.tenant, h.params(true))
		done <- err
	}()
	select {
	case err := <-done:
		wantRunErr(t, err, http.StatusUnprocessableEntity, "could not start")
	case <-time.After(2 * time.Second):
		t.Fatal("launch error waited for an unreaped setup process")
	}
}

// The caller's own retry can confirm reaping before the bridge settles. Its failed
// transition must classify the process then, not reuse the earlier launch error alone.
func TestWorktreeSetupCallerRetryKeepsGuardCleanupFailure(t *testing.T) {
	runner := &setupRunner{
		launchErr: errors.New("readiness fixture"), stopErr: ErrChildNotReaped,
		startRunning: true, stopLeavesLive: true, guardCleanupFailed: true,
	}
	h, ctx := newSetupHarness(t, runner)
	h.approveSetup("make setup")
	stops := 0
	runner.onStop = func() {
		stops++
		if stops == 1 {
			runner.last.stopErr = nil
			return
		}
		runner.last.stopLeavesLive = false
	}
	_, err := createProfiledTestRun(t, h.m, ctx, h.tenant, h.params(true))
	wantRunErr(t, err, http.StatusUnprocessableEntity, "could not start")
	runRef := filepath.Base(runner.setupLaunches()[0].spec.Dir)
	runner.startRunning = false
	_, resumeErr := h.resume(ctx, runRef)
	wantRunErr(t, resumeErr, http.StatusUnprocessableEntity, "cleanup was not confirmed")
	if n := len(runner.setupLaunches()); n != 1 {
		t.Fatalf("setup launches after caller-side cleanup settlement = %d, want still 1", n)
	}
}

// A launch-error process reaped by the bridge later gets its outcome classified at
// that final settlement too: late reaping cannot erase the durable resume block.
func TestWorktreeSetupLateReapingKeepsGuardCleanupFailure(t *testing.T) {
	runner := &setupRunner{
		launchErr: errors.New("readiness fixture"), stopErr: ErrChildNotReaped,
		startRunning: true, stopLeavesLive: true, guardCleanupFailed: true,
	}
	h, ctx := newSetupHarness(t, runner)
	h.approveSetup("make setup")
	_, err := createProfiledTestRun(t, h.m, ctx, h.tenant, h.params(true))
	wantRunErr(t, err, http.StatusUnprocessableEntity, "could not start")
	runRef := filepath.Base(runner.setupLaunches()[0].spec.Dir)
	runner.last.finish(143)
	waitFor(t, "setup cleanup final settlement", func() bool {
		failed := false
		_ = h.m.Data.View(ctx, h.tenant, func(sc store.Scope) error {
			repo, err := sc.Ext(runKind)
			if err != nil {
				return err
			}
			rec, err := findRunRec(ctx, repo, runRef)
			if err == nil {
				failed = rec.Bool(colRunSetupCleanupFailed)
			}
			return nil
		})
		return failed
	})
	_, resumeErr := h.resume(ctx, runRef)
	wantRunErr(t, resumeErr, http.StatusUnprocessableEntity, "cleanup was not confirmed")
	if n := len(runner.setupLaunches()); n != 1 {
		t.Fatalf("setup launches after late cleanup settlement = %d, want still 1", n)
	}
}

// A runner can return a process with its launch/readiness error. Its guard verdict
// is still durable, so resume cannot start beside an unconfirmed detached tree.
func TestWorktreeSetupLaunchErrorKeepsGuardCleanupFailure(t *testing.T) {
	runner := &setupRunner{launchErr: errors.New("readiness fixture"), guardCleanupFailed: true}
	h, ctx := newSetupHarness(t, runner)
	h.approveSetup("make setup")
	_, err := createProfiledTestRun(t, h.m, ctx, h.tenant, h.params(true))
	wantRunErr(t, err, http.StatusUnprocessableEntity, "could not start")
	runRef := filepath.Base(runner.setupLaunches()[0].spec.Dir)
	_, resumeErr := h.resume(ctx, runRef)
	wantRunErr(t, resumeErr, http.StatusUnprocessableEntity, "cleanup was not confirmed")
	if n := len(runner.setupLaunches()); n != 1 {
		t.Fatalf("setup launches after readiness cleanup failure = %d, want still 1", n)
	}
}

// The create request's deadline owns setup: a client timeout cannot lose the response
// and then let the session start in the background. The session child has its own context.
func TestWorktreeSetupRequestCancelStopsStart(t *testing.T) {
	restore := worktreeSetupTimeout
	worktreeSetupTimeout = 5 * time.Second
	t.Cleanup(func() { worktreeSetupTimeout = restore })
	runner := &setupRunner{real: NewProcRunner()}
	h, ctx := newConfinedSetupHarness(t, runner)
	h.approveSetup("sleep 30")
	requestCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	runner.onSetup = func(LaunchSpec) { cancel() }
	started := time.Now()
	_, err := createProfiledTestRun(t, h.m, requestCtx, h.tenant, h.params(true))
	wantRunErr(t, err, http.StatusUnprocessableEntity, "cancelled")
	if strings.Contains(err.Error(), "time limit") {
		t.Fatalf("the request's deadline was reported as the setup's own time limit: %v", err)
	}
	if elapsed := time.Since(started); elapsed > 4*time.Second {
		t.Fatalf("setup ignored the request deadline for %s", elapsed)
	}
	if len(h.fr.specs) != 0 {
		t.Fatalf("a session started after the create request was cancelled: %d launch(es)", len(h.fr.specs))
	}
}

// A connected client's 60-second response timeout does not cancel the request, so
// what a request waits for setup, and what a resume lets setup run, must leave time
// for the answer. A setup the session owns is not bound by it.
func TestWorktreeSetupWaitLeavesHTTPResponseHeadroom(t *testing.T) {
	if worktreeSetupInlineWait+sessionLaunchSetupTimeout >= time.Minute {
		t.Fatalf("the request's wait and launch preparation can exceed the response timeout: %s + %s", worktreeSetupInlineWait, sessionLaunchSetupTimeout)
	}
	if syncSetupLimit() > worktreeSetupInlineWait {
		t.Fatalf("a setup a request waits for to its end can outlast the wait: %s > %s", syncSetupLimit(), worktreeSetupInlineWait)
	}
}

type failOnceData struct {
	inner  api.ModuleData
	writes int
}

func (d *failOnceData) View(ctx context.Context, tenant model.TenantID, fn func(store.Scope) error) error {
	return d.inner.View(ctx, tenant, fn)
}

func (d *failOnceData) Mutate(ctx context.Context, tenant model.TenantID, fn func(store.Scope) error) error {
	d.writes++
	if d.writes == 1 {
		return errors.New("setup event write fixture")
	}
	return d.inner.Mutate(ctx, tenant, fn)
}

type cancelAfterWriteData struct {
	inner  api.ModuleData
	cancel func()
	writes int
}

func (d *cancelAfterWriteData) View(ctx context.Context, tenant model.TenantID, fn func(store.Scope) error) error {
	return d.inner.View(ctx, tenant, fn)
}

func (d *cancelAfterWriteData) Mutate(ctx context.Context, tenant model.TenantID, fn func(store.Scope) error) error {
	err := d.inner.Mutate(ctx, tenant, fn)
	d.writes++
	if d.writes == 5 {
		d.cancel()
	}
	return err
}

// Cancellation after a committed setup still stops before Runner.Launch: the event
// may commit, but the canceled request must not create a session process.
func TestWorktreeSetupCancelAfterSuccessPreventsSessionLaunch(t *testing.T) {
	runner := &setupRunner{}
	h, ctx := newSetupHarness(t, runner)
	h.approveSetup("make setup")
	requestCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	data := h.m.Data
	wrapped := &cancelAfterWriteData{inner: data, cancel: cancel}
	h.m.Data = wrapped
	t.Cleanup(func() { h.m.Data = data })
	_, err := createProfiledTestRun(t, h.m, requestCtx, h.tenant, h.params(true))
	t.Logf("store writes=%d, create error=%v", wrapped.writes, err)
	if err == nil {
		t.Fatal("a session launched after cancellation after setup succeeded")
	}
	if !strings.Contains(err.Error(), "create request was cancelled") {
		t.Fatalf("cancellation came from the wrong stage: %v", err)
	}
	if len(h.fr.specs) != 0 {
		t.Fatalf("a session process launched after cancellation: %d launch(es)", len(h.fr.specs))
	}
}
