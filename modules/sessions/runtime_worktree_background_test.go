// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// shortInlineWait lets a test see the pending answer a long setup gives: the request
// waits this long for the setup before the session owns it.
func shortInlineWait(t *testing.T) {
	t.Helper()
	restore := worktreeSetupInlineWait
	worktreeSetupInlineWait = 50 * time.Millisecond
	t.Cleanup(func() { worktreeSetupInlineWait = restore })
}

func (h *worktreeHarness) waitState(ctx context.Context, runRef, want string) runDTO {
	h.t.Helper()
	var got runDTO
	waitFor(h.t, "run state "+want, func() bool {
		var err error
		got, err = h.m.getRun(ctx, h.tenant, runRef)
		return err == nil && got.State == want
	})
	return got
}

// setupEvents returns the detail of the run's worktree_setup events, in order.
func setupEvents(t *testing.T, h *worktreeHarness, runRef string) []string {
	t.Helper()
	var out []string
	for _, ev := range listRunEvents(t, h.st, h.tenant, runRef) {
		if ev.Event == worktreeSetupEvent {
			out = append(out, ev.Detail)
		}
	}
	return out
}

func (r *setupRunner) setupProc(t *testing.T) *fakeProc {
	t.Helper()
	var proc *fakeProc
	waitFor(t, "setup process", func() bool {
		r.mu.Lock()
		defer r.mu.Unlock()
		proc = r.last
		return proc != nil
	})
	return proc
}

// An installer belongs to the session lifecycle, rather than the request that
// created it. The pending response must arrive while its setup is still running, and
// a stop reaches the setup through the session.
func TestWorktreeSetupReturnsPendingBeforeInstallerFinishes(t *testing.T) {
	shortInlineWait(t)
	started := make(chan struct{})
	runner := &setupRunner{waitForContext: true, onSetup: func(LaunchSpec) { close(started) }}
	h, ctx := newSetupHarness(t, runner)
	h.approveSetup("npm ci")
	request, cancel := context.WithCancel(ctx)
	defer cancel()
	type result struct {
		dto runDTO
		err error
	}
	created := make(chan result, 1)
	go func() {
		dto, err := createProfiledTestRun(t, h.m, request, h.tenant, h.params(true))
		created <- result{dto, err}
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("approved setup did not start")
	}
	var got result
	select {
	case got = <-created:
	case <-time.After(2 * time.Second):
		cancel()
		<-created
		t.Fatal("create waited for the installer instead of returning its pending session")
	}
	if got.err != nil || got.dto.State != statePending {
		t.Fatalf("create while setup runs = state %q, error %v; want pending", got.dto.State, got.err)
	}
	cancel() // Disconnecting the create request must leave the setup owned.
	if stored, err := h.m.getRun(ctx, h.tenant, got.dto.RunRef); err != nil || stored.State != statePending {
		t.Fatalf("setup after the request left = state %q, error %v; want still pending", stored.State, err)
	}
	if _, err := h.m.stopRun(ctx, h.tenant, got.dto.RunRef, "user:u1", "user"); err != nil {
		t.Fatalf("stop pending setup: %v", err)
	}
	stored := h.waitState(ctx, got.dto.RunRef, stateStopped)
	if stored.State != stateStopped {
		t.Fatalf("a stopped setup left the run %q", stored.State)
	}
	if events := setupEvents(t, h, got.dto.RunRef); len(events) != 1 || !strings.Contains(events[0], "cancelled") {
		t.Fatalf("a stopped setup's ledger = %q, want one cancelled worktree_setup event", events)
	}
	if len(h.fr.specs) != 0 {
		t.Fatalf("a session started after its setup was stopped: %d launch(es)", len(h.fr.specs))
	}
}

func TestWorktreeSetupConcurrentFirstTurnsWaitAndResumeDoesNotRepeat(t *testing.T) {
	shortInlineWait(t)
	runner := &setupRunner{startRunning: true}
	h, ctx := newSetupHarness(t, runner)
	h.approveSetup("npm ci")
	request, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	dto, err := createProfiledTestRun(t, h.m, request, h.tenant, h.params(true))
	if err != nil || dto.State != statePending {
		t.Fatalf("installer start = state %q, error %v; want pending", dto.State, err)
	}
	_, err = h.m.resumeRun(ctx, h.tenant, dto.RunRef, "user:u1", "user", "")
	wantRunErr(t, err, http.StatusConflict, "state=pending")
	for range 3 {
		if got, err := h.m.getRun(ctx, h.tenant, dto.RunRef); err != nil || got.State != statePending {
			t.Fatalf("reconnect = state %q, error %v; want pending", got.State, err)
		}
	}
	turnCtx, cancelTurns := context.WithTimeout(ctx, 3*time.Second)
	defer cancelTurns()
	turns := make(chan error, 2)
	for range 2 {
		go func() {
			turns <- h.m.sendInput(turnCtx, h.tenant, dto.RunRef, []byte(`{"type":"user","message":{"role":"user","content":"hello"}}`))
		}()
	}
	select {
	case err := <-turns:
		t.Fatalf("first turn returned before setup completed: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	runner.setupProc(t).finish(0)
	for range 2 {
		if err := <-turns; err != nil {
			t.Fatalf("first turn after setup: %v", err)
		}
	}
	if n := len(runner.setupLaunches()); n != 1 {
		t.Fatalf("setup starts = %d, want exactly one", n)
	}
	if got := runner.setupLaunches()[0].sessionsBefore; got != 0 {
		t.Fatalf("setup ran after %d session launch(es), want before the first turn", got)
	}
	if stored := h.waitState(ctx, dto.RunRef, stateRunning); stored.State != stateRunning {
		t.Fatalf("session after setup = %q, want running", stored.State)
	}
	if got := h.fr.lastProc().sentCount(); got != 2 {
		t.Fatalf("delivered turns = %d, want two serialized requests", got)
	}
	if events := setupEvents(t, h, dto.RunRef); len(events) != 1 || !strings.Contains(events[0], "finished with status 0") {
		t.Fatalf("setup ledger = %q, want one finished event", events)
	}
}

// An input does not wait past the response budget: when its wait ends while the setup
// still runs, it meets the ordinary refusal of a session that is not running yet and
// never reaches the tool; a turn sent after the setup ended is delivered.
func TestWorktreeSetupFirstTurnIsRefusedWhenItsWaitEndsDuringSetup(t *testing.T) {
	shortInlineWait(t)
	restore := launchInputWait
	launchInputWait = 100 * time.Millisecond
	t.Cleanup(func() { launchInputWait = restore })
	runner := &setupRunner{startRunning: true}
	h, ctx := newSetupHarness(t, runner)
	h.approveSetup("npm ci")
	dto, err := createProfiledTestRun(t, h.m, ctx, h.tenant, h.params(true))
	if err != nil || dto.State != statePending {
		t.Fatalf("create = state %q, error %v; want pending", dto.State, err)
	}
	line := []byte(`{"type":"user","message":{"role":"user","content":"hello"}}`)
	wantRunErr(t, h.m.sendInput(ctx, h.tenant, dto.RunRef, line), http.StatusConflict, "state=pending")
	if len(h.fr.specs) != 0 {
		t.Fatalf("a session started while its setup still ran: %d launch(es)", len(h.fr.specs))
	}
	runner.setupProc(t).finish(0)
	h.waitState(ctx, dto.RunRef, stateRunning)
	if err := h.m.sendInput(ctx, h.tenant, dto.RunRef, line); err != nil {
		t.Fatalf("turn after the setup: %v", err)
	}
	if got := h.fr.lastProc().sentCount(); got != 1 {
		t.Fatalf("delivered turns = %d, want only the one sent after the setup", got)
	}
}

// A setup that fails after the request answered stops the first turn, keeps its
// output on the ledger and leaves a failed session that never started its tool.
func TestWorktreeSetupFailureAfterPendingStopsTheFirstTurnAndKeepsItsOutput(t *testing.T) {
	shortInlineWait(t)
	runner := &setupRunner{startRunning: true, output: "npm ERR! registry unreachable"}
	h, ctx := newSetupHarness(t, runner)
	h.approveSetup("npm ci")
	dto, err := createProfiledTestRun(t, h.m, ctx, h.tenant, h.params(true))
	if err != nil || dto.State != statePending {
		t.Fatalf("create = state %q, error %v; want pending", dto.State, err)
	}
	turn := make(chan error, 1)
	go func() {
		turn <- h.m.sendInput(ctx, h.tenant, dto.RunRef, []byte(`{"type":"user","message":{"role":"user","content":"hello"}}`))
	}()
	runner.setupProc(t).finish(1)
	wantRunErr(t, <-turn, http.StatusConflict, "state=failed")
	stored := h.waitState(ctx, dto.RunRef, stateFailed)
	if !strings.Contains(stored.Reason, "exited with status 1") {
		t.Fatalf("failed run reason = %q, want the setup's exit status", stored.Reason)
	}
	events := setupEvents(t, h, dto.RunRef)
	if len(events) != 1 || !strings.Contains(events[0], "status 1") || !strings.Contains(events[0], "npm ERR! registry unreachable") {
		t.Fatalf("setup ledger = %q, want the exit status and the setup's last output", events)
	}
	if len(h.fr.specs) != 0 {
		t.Fatalf("a session started after its setup failed: %d launch(es)", len(h.fr.specs))
	}
}

// The setup's own time limit ends a setup the session owns, and the first turn is refused.
func TestWorktreeSetupTimeLimitAfterPendingStopsTheFirstTurn(t *testing.T) {
	shortInlineWait(t)
	restore := worktreeSetupTimeout
	worktreeSetupTimeout = 300 * time.Millisecond
	t.Cleanup(func() { worktreeSetupTimeout = restore })
	runner := &setupRunner{waitForContext: true}
	h, ctx := newSetupHarness(t, runner)
	h.approveSetup("npm ci")
	dto, err := createProfiledTestRun(t, h.m, ctx, h.tenant, h.params(true))
	if err != nil || dto.State != statePending {
		t.Fatalf("create = state %q, error %v; want pending", dto.State, err)
	}
	wantRunErr(t, h.m.sendInput(ctx, h.tenant, dto.RunRef, []byte(`{"type":"user","message":{"role":"user","content":"hello"}}`)), http.StatusConflict, "state=failed")
	if events := setupEvents(t, h, dto.RunRef); len(events) != 1 || !strings.Contains(events[0], "time limit") {
		t.Fatalf("setup ledger = %q, want one time limit event", events)
	}
	if len(h.fr.specs) != 0 {
		t.Fatalf("a session started after its setup hit the time limit: %d launch(es)", len(h.fr.specs))
	}
}

// The same start with a real /bin/sh under the real confinement: the request answers
// pending while the installer runs, the first turn waits for it, and the session starts
// only after the installer's files exist.
func TestWorktreeSetupRealInstallerHoldsTheFirstTurnUntilItsFilesExist(t *testing.T) {
	shortInlineWait(t)
	runner := &setupRunner{real: NewProcRunner()}
	h, ctx := newConfinedSetupHarness(t, runner)
	h.approveSetup("sleep 2; echo installed > installed.txt")
	dto, err := createProfiledTestRun(t, h.m, ctx, h.tenant, h.params(true))
	if err != nil || dto.State != statePending {
		t.Fatalf("create = state %q, error %v; want pending", dto.State, err)
	}
	installed := filepath.Join(runner.setupLaunches()[0].spec.Dir, "installed.txt")
	if _, err := os.Stat(installed); err == nil {
		t.Fatal("the installer had finished when its session was still pending")
	}
	if err := h.m.sendInput(ctx, h.tenant, dto.RunRef, []byte(`{"type":"user","message":{"role":"user","content":"hello"}}`)); err != nil {
		t.Fatalf("first turn: %v", err)
	}
	if _, err := os.Stat(installed); err != nil {
		t.Fatalf("the first turn was delivered before the installer finished: %v", err)
	}
	if n := len(h.fr.specs); n != 1 {
		t.Fatalf("session launches = %d, want one, after the installer", n)
	}
	if stored, err := h.m.getRun(ctx, h.tenant, dto.RunRef); err != nil || stored.State != stateRunning {
		t.Fatalf("session after the installer = state %q, error %v; want running", stored.State, err)
	}
}

// A stop ends a real installer and everything it started, and no session starts.
func TestWorktreeSetupStopEndsARealInstallerTree(t *testing.T) {
	shortInlineWait(t)
	runner := &setupRunner{real: NewProcRunner()}
	h, ctx := newConfinedSetupHarness(t, runner)
	h.approveSetup("echo $$ > .setup.pid; sleep 60")
	dto, err := createProfiledTestRun(t, h.m, ctx, h.tenant, h.params(true))
	if err != nil || dto.State != statePending {
		t.Fatalf("create = state %q, error %v; want pending", dto.State, err)
	}
	pidFile := filepath.Join(runner.setupLaunches()[0].spec.Dir, ".setup.pid")
	var pid int
	waitFor(t, "installer pid", func() bool {
		raw, err := os.ReadFile(pidFile)
		if err != nil {
			return false
		}
		pid, err = strconv.Atoi(strings.TrimSpace(string(raw)))
		return err == nil && pid > 0
	})
	started := time.Now()
	if _, err := h.m.stopRun(ctx, h.tenant, dto.RunRef, "user:u1", "user"); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if elapsed := time.Since(started); elapsed > 10*time.Second {
		t.Fatalf("stopping the installer took %s", elapsed)
	}
	h.waitState(ctx, dto.RunRef, stateStopped)
	waitFor(t, "installer process gone", func() bool { return syscall.Kill(pid, 0) == syscall.ESRCH })
	if len(h.fr.specs) != 0 {
		t.Fatalf("a session started after its installer was stopped: %d launch(es)", len(h.fr.specs))
	}
}

// A setup whose process tree cannot be confirmed gone keeps the session from ever
// starting again, as it does for a setup the request waited for.
func TestWorktreeSetupUnconfirmedCleanupAfterPendingBlocksResume(t *testing.T) {
	shortInlineWait(t)
	runner := &setupRunner{startRunning: true, guardCleanupFailed: true}
	h, ctx := newSetupHarness(t, runner)
	h.approveSetup("make setup")
	dto, err := createProfiledTestRun(t, h.m, ctx, h.tenant, h.params(true))
	if err != nil || dto.State != statePending {
		t.Fatalf("create = state %q, error %v; want pending", dto.State, err)
	}
	runner.setupProc(t).finish(0)
	h.waitState(ctx, dto.RunRef, stateFailed)
	_, resumeErr := h.resume(ctx, dto.RunRef)
	wantRunErr(t, resumeErr, http.StatusUnprocessableEntity, "cleanup was not confirmed")
	if n := len(runner.setupLaunches()); n != 1 {
		t.Fatalf("setup launches after an unconfirmed cleanup = %d, want still 1", n)
	}
	if len(h.fr.specs) != 0 {
		t.Fatalf("a session started after an unconfirmed setup cleanup: %d launch(es)", len(h.fr.specs))
	}
	if filepath.Base(runner.setupLaunches()[0].spec.Dir) != dto.RunRef {
		t.Fatalf("setup ran in %q, want the session's own worktree %s", runner.setupLaunches()[0].spec.Dir, dto.RunRef)
	}
}
