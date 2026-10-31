// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build linux

package sessions

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// The guard stands between the engine and the tool, so the engine must still read
// the tool's own end: the code it exited with, or the signal that ended it.
func TestSessionGuardPassesTheToolsEndToTheEngine(t *testing.T) {
	for _, tc := range []struct {
		name, script string
		want         int
	}{
		{"exit code", "exit 7", 7},
		{"killed by SIGTERM", "kill -TERM $$", -1},
		{"killed by SIGKILL", "kill -KILL $$", -1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for name, runner := range map[string]Runner{"pipes": NewProcRunner(), "pty": NewPTYRunner()} {
				proc, err := runner.Launch(context.Background(), LaunchSpec{Dir: t.TempDir(), Program: "/bin/sh", Args: []string{"-c", tc.script}, WaitDelay: time.Second})
				if err != nil {
					t.Fatalf("%s: launch: %v", name, err)
				}
				go func() {
					for range proc.Output() {
					}
				}()
				got, err := proc.Wait()
				if err != nil {
					t.Errorf("%s: wait: %v", name, err)
				}
				if got != tc.want {
					t.Errorf("%s: the engine read exit %d, want %d", name, got, tc.want)
				}
			}
		})
	}
}

// Under -race the guard is this test binary, which pauses before it exits 0 for
// as long as GORACE atexit_sleep_ms says (1 s by default); the engine's guard
// does not. A launch cannot always carry raceGuardEnv to the guard: the Codex
// sandbox probe passes only the homes, and its 3 s deadline held this pause and
// the one of the stand-in Codex the guard starts. The pause is stretched here, so
// a guard that keeps the launch's setting cannot end in time.
func TestSessionGuardEndsWithItsProgramUnderTheRaceDetector(t *testing.T) {
	if !raceDetectorEnabled {
		t.Skip("the exit pause exists only in a race-detector build")
	}
	proc, err := NewProcRunner().Launch(context.Background(), LaunchSpec{Dir: t.TempDir(), Program: "/bin/true", WaitDelay: time.Second,
		Env: []EnvVar{{Name: "GORACE", Value: "atexit_sleep_ms=600000"}}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = proc.Stop(context.Background()) })
	go func() {
		for range proc.Output() {
		}
	}()
	if !proc.(*procProcess).finishedWithin(30 * time.Second) {
		t.Fatal("the session guard did not end with its program")
	}
	if code, err := proc.Wait(); err != nil || code != 0 {
		t.Fatalf("the session guard ended with %d, %v, want its program's 0", code, err)
	}
}

// The guard ends the session when the engine's end of its pipe closes. That end
// is a file the Go runtime closes once nothing refers to it, so a live session
// must keep it referenced: otherwise the collector would end the session.
func TestSessionGuardKeepsTheSessionWhileTheEngineLives(t *testing.T) {
	proc, err := NewProcRunner().Launch(context.Background(), LaunchSpec{Dir: t.TempDir(), Program: "/bin/sleep", Args: []string{"600"}, WaitDelay: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	pid := proc.PID()
	defer func() { _ = proc.Stop(context.Background()) }()
	for range 5 {
		runtime.GC()
		runtime.GC() // a finalizer runs after the cycle that finds its file unreachable
		time.Sleep(100 * time.Millisecond)
	}
	if !processRunning(pid) {
		t.Fatal("the session ended while the engine was alive")
	}
}

// A program that cannot run is a launch error, as it was before the guard stood
// in front of it, not a session that starts and exits.
func TestSessionGuardKeepsALaunchErrorForAProgramThatCannotRun(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "plain"), []byte("#!/bin/sh\n"), 0o644); err != nil { // not executable
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, program string
		is            error
	}{
		{"missing path", "/nonexistent/claude", fs.ErrNotExist},
		{"not on PATH", "olivares-no-such-tool", exec.ErrNotFound},
		{"not executable", filepath.Join(dir, "plain"), fs.ErrPermission},
		{"a folder", dir, fs.ErrPermission},
	} {
		for name, runner := range map[string]Runner{"pipes": NewProcRunner(), "pty": NewPTYRunner()} {
			_, err := runner.Launch(context.Background(), LaunchSpec{Dir: t.TempDir(), Program: tc.program})
			if !errors.Is(err, tc.is) {
				t.Errorf("%s, %s: launching returned %v, want %v", tc.name, name, err, tc.is)
			}
		}
	}
}

// A program named relative to the session's folder starts after the engine has
// changed into it, so that is where it is looked for.
func TestSessionGuardStartsAProgramNamedRelativeToTheSessionFolder(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "tool.sh"), []byte("#!/bin/sh\nexit 5\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	proc, err := NewProcRunner().Launch(context.Background(), LaunchSpec{Dir: dir, Program: "./tool.sh", WaitDelay: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for range proc.Output() {
		}
	}()
	if got, _ := proc.Wait(); got != 5 {
		t.Fatalf("the tool exited %d, want 5", got)
	}
}

// The guard is the subreaper, so what the session orphans becomes its child and
// must be reaped there: a long session must not collect zombies.
func TestSessionGuardReapsWhatTheSessionOrphans(t *testing.T) {
	dir := t.TempDir()
	proc, err := NewProcRunner().Launch(context.Background(), LaunchSpec{Dir: dir, Program: "/bin/sh", WaitDelay: time.Second, Args: []string{"-c",
		`i=0; while [ $i -lt 100 ]; do ( : & ); i=$((i+1)); done; : > ready; exec sleep 600`}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = proc.Stop(context.Background()) }()
	if !waitForFile(filepath.Join(dir, "ready")) {
		t.Fatal("the session never finished forking")
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		zombies := zombieChildren(proc.PID())
		if zombies == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the guard holds %d zombies of what the session orphaned", zombies)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// zombieChildren counts the zombie children of pid.
func zombieChildren(pid int) int {
	n := 0
	entries, _ := os.ReadDir("/proc")
	for _, e := range entries {
		stat, err := os.ReadFile("/proc/" + e.Name() + "/stat")
		if err != nil {
			continue
		}
		i := strings.LastIndexByte(string(stat), ')')
		if i < 0 {
			continue
		}
		if f := strings.Fields(string(stat[i+1:])); len(f) > 1 && f[0] == "Z" && f[1] == strconv.Itoa(pid) {
			n++
		}
	}
	return n
}

// With the guard as the terminal's session leader, the tool must still have the
// terminal as its controlling terminal.
func TestSessionGuardKeepsThePTYTheToolsControllingTerminal(t *testing.T) {
	proc, err := NewPTYRunner().Launch(context.Background(), LaunchSpec{Dir: t.TempDir(), Program: "/bin/sh", WaitDelay: time.Second,
		Args: []string{"-c", `if [ -t 0 ] && : </dev/tty; then echo controlling-terminal-ok; else echo no-controlling-terminal; fi`}})
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for f := range proc.Output() {
		out = append(out, string(f.Data))
	}
	if got := strings.Join(out, ""); !strings.Contains(got, "controlling-terminal-ok") {
		t.Fatalf("the tool saw %q, want the controlling terminal", got)
	}
}

// The tool is the one process the guard started; what the session orphaned is its
// child too and must not be taken for the tool (preview ports rely on it).
func TestSessionGuardIsRecognisedAndItsToolFound(t *testing.T) {
	dir := t.TempDir()
	proc, err := NewProcRunner().Launch(context.Background(), LaunchSpec{Dir: dir, Program: "/bin/sh", WaitDelay: time.Second,
		Args: []string{"-c", `( sleep 6138 & echo $! > orphan ); : > ready; exec sleep 6139`}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = proc.Stop(context.Background()) }()
	if !waitForFile(filepath.Join(dir, "ready")) {
		t.Fatal("the session never reached the exec")
	}
	guard := proc.PID()
	if !isSessionGuard("/proc", guard) {
		t.Fatalf("process %d, the one the engine started, is not recognised as the guard", guard)
	}
	_, parent, err := processTree("/proc", guard)
	if err != nil {
		t.Fatal(err)
	}
	tool := launchedChild("/proc", guard, parent)
	cmdline, _ := os.ReadFile("/proc/" + strconv.Itoa(tool) + "/cmdline")
	if !strings.HasPrefix(string(cmdline), "sleep\x006139") {
		t.Fatalf("the guard's tool is process %d running %q, want the exec'd sleep 6139", tool, cmdline)
	}
	orphan := waitForPID(t, filepath.Join(dir, "orphan"))
	if parent[orphan] != guard {
		t.Fatalf("the orphan's parent is %d, want the guard %d", parent[orphan], guard)
	}
	_ = syscall.Kill(orphan, syscall.SIGKILL)
}

// A command the session left running in the background ends with a stop, even
// one that left the session's process group with setsid, which the stop's group
// signal cannot reach: the guard is its parent by the subreaper, so killing the
// tree before the guard exits reaches it. The shape reproduced: a detached
// background command that survived `olivares session stop`.
func TestSessionGuardEndsWhatAStopLeftRunning(t *testing.T) {
	if _, err := exec.LookPath("setsid"); err != nil {
		t.Skip("setsid(1) is not installed here")
	}
	for name, runner := range map[string]Runner{"pipes": NewProcRunner(), "pty": NewPTYRunner()} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			proc, err := runner.Launch(context.Background(), LaunchSpec{Dir: dir, Program: "/bin/sh", WaitDelay: 200 * time.Millisecond,
				// The second background command is a shell whose own child is only reparented
				// to the guard once the shell is killed: it needs the guard's second pass.
				Args: []string{"-c", `setsid sleep 600 </dev/null >/dev/null 2>&1 & echo $! > left; setsid sh -c 'sleep 600 </dev/null >/dev/null 2>&1 & echo $! > nested; wait' </dev/null >/dev/null 2>&1 & : > ready; exec sleep 600`}})
			if err != nil {
				t.Fatal(err)
			}
			go func() {
				for range proc.Output() {
				}
			}()
			if !waitForFile(filepath.Join(dir, "ready")) {
				t.Fatal("the session never reached the exec")
			}
			left := waitForPID(t, filepath.Join(dir, "left"))
			defer func() { _ = syscall.Kill(left, syscall.SIGKILL) }() // never leave the sleep behind
			nested := waitForPID(t, filepath.Join(dir, "nested"))
			defer func() { _ = syscall.Kill(nested, syscall.SIGKILL) }()
			if !processRunning(left) || !processRunning(nested) {
				t.Fatalf("the background commands %d and %d never ran", left, nested)
			}
			if err := proc.Stop(context.Background()); err != nil {
				t.Fatalf("stop: %v", err)
			}
			if !endsWithin(left, 5*time.Second) {
				t.Errorf("process %d the session left running in the background survived the stop", left)
			}
			if !endsWithin(nested, 5*time.Second) {
				t.Errorf("process %d, a child of a background command the session left, survived the stop", nested)
			}
		})
	}
}

// A stop that has to escalate ends the whole tree too: closing the guard's
// watch makes the guard kill every descendant and then exit, where a group
// SIGKILL would kill the guard with the program and leak what left the group.
func TestSessionGuardEndsWhatAnEscalatedStopLeftRunning(t *testing.T) {
	if _, err := exec.LookPath("setsid"); err != nil {
		t.Skip("setsid(1) is not installed here")
	}
	for name, runner := range map[string]Runner{"pipes": NewProcRunner(), "pty": NewPTYRunner()} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			// The tool ignores SIGTERM (the disposition survives the fork, so its sleep
			// does too): the stop must escalate. The background command left the group.
			proc, err := runner.Launch(context.Background(), LaunchSpec{Dir: dir, Program: "/bin/sh", WaitDelay: 200 * time.Millisecond,
				Args: []string{"-c", `trap '' TERM; setsid sleep 600 </dev/null >/dev/null 2>&1 & echo $! > left; : > ready; sleep 600 & wait`}})
			if err != nil {
				t.Fatal(err)
			}
			go func() {
				for range proc.Output() {
				}
			}()
			if !waitForFile(filepath.Join(dir, "ready")) {
				t.Fatal("the session never reached the wait")
			}
			left := waitForPID(t, filepath.Join(dir, "left"))
			defer func() { _ = syscall.Kill(left, syscall.SIGKILL) }() // never leave the sleep behind
			if !processRunning(left) {
				t.Fatalf("the background command %d never ran", left)
			}
			if err := proc.Stop(context.Background()); err != nil {
				t.Fatalf("stop: %v", err)
			}
			if !endsWithin(left, 5*time.Second) {
				t.Errorf("process %d the session left running in the background survived the escalated stop", left)
			}
			// The guard ends by SIGKILL, as the group kill it replaces did, so the engine
			// records a forced end as killed and not as the tool's own failure.
			if code, _ := proc.Wait(); code != -1 {
				t.Errorf("the engine read exit %d for an escalated stop, want -1 (killed)", code)
			}
		})
	}
}

// When the guard cannot end the tree (its watch is gone, or it is stuck), Stop
// falls back to the group kill after the delay: a tool that ignores SIGTERM must
// not outlive it. A launch with no guard, whose tree-end does nothing here,
// stands in for a guard that cannot act.
func TestProcProcessStopKillsTheGroupWhenTheGuardCannotEndTheTree(t *testing.T) {
	p := launchUnguardedChild(t, "#!/bin/sh\ntrap '' TERM\nprintf 'up\\n'\nexec sleep 300\n", 200*time.Millisecond)
	p.endTree = func() {}
	sink := drainOutput(p)
	waitFor(t, "the child to announce itself", func() bool { _, ok := sink.find("up"); return ok })
	child := p.PID()
	stopped := make(chan error, 1)
	go func() { stopped <- p.Stop(context.Background()) }()
	if err := mustFinishWithin(t, "Stop with a guard that cannot end the tree", 20*time.Second, stopped); err != nil {
		t.Fatalf("stop: %v", err)
	}
	waitFor(t, "the child to be reaped", func() bool { return processGoneOrZombie(child) })
}

// A holder of the output pipe that LEFT the process group cannot be reached by
// the stop's group signal, but it is still the guard's child by the subreaper:
// the escalation goes through the guard, the escapee dies, and the pumps see a
// real EOF with nothing abandoned — before the guard stood in front, this shape
// wedged the teardown into a forced close of the pipes and a dropped tail, and
// the test had to kill the escapee itself. Stop still never hangs and never
// returns nil for a child it did not reap.
func TestProcProcessStopKillsAHolderThatEscapedTheGroup(t *testing.T) {
	t.Parallel()

	setsid, err := exec.LookPath("setsid")
	if err != nil {
		t.Skip("setsid is not installed: this control needs a holder that can leave the process group")
	}
	p := launchOwnedChild(t, fmt.Sprintf(escapedGrandchildScript, setsid), 300*time.Millisecond)
	sink := drainOutput(p)
	var escaped int
	waitFor(t, "the escaped holder to announce its pid", func() bool {
		line, ok := sink.find("escaped ")
		if !ok {
			return false
		}
		escaped, _ = strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(line, "escaped ")))
		return escaped > 0
	})
	t.Cleanup(func() { _ = syscall.Kill(escaped, syscall.SIGKILL) })

	child := p.PID()
	stopped := make(chan error, 1)
	go func() { stopped <- p.Stop(context.Background()) }()
	if stopErr := mustFinishWithin(t, "Stop with an escaped holder of the output pipes", 20*time.Second, stopped); stopErr != nil {
		t.Fatalf("stop: %v", stopErr)
	}
	sink.waitClosed(t, 5*time.Second)
	if _, err := p.Wait(); err != nil {
		t.Fatalf("wait: %v", err)
	}
	waitFor(t, "the child to be reaped", func() bool { return processGoneOrZombie(child) })

	// The stop, not this test, ended the escapee.
	waitFor(t, "the escaped holder to be gone", func() bool { return processGoneOrZombie(escaped) })
}

// A launch whose context ends (a timed probe, a stored-reader read) ends the tree
// too: the cancellation closes the guard's watch rather than killing the group,
// which would take the guard with the program and leak what left the group.
func TestSessionGuardEndsWhatACancelledLaunchLeftRunning(t *testing.T) {
	if _, err := exec.LookPath("setsid"); err != nil {
		t.Skip("setsid(1) is not installed here")
	}
	for name, runner := range map[string]Runner{"pipes": NewProcRunner(), "pty": NewPTYRunner()} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			proc, err := runner.Launch(ctx, LaunchSpec{Dir: dir, Program: "/bin/sh", WaitDelay: 200 * time.Millisecond,
				Args: []string{"-c", `setsid sleep 600 </dev/null >/dev/null 2>&1 & echo $! > left; : > ready; exec sleep 600`}})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = proc.Stop(context.Background()) })
			go func() {
				for range proc.Output() {
				}
			}()
			if !waitForFile(filepath.Join(dir, "ready")) {
				t.Fatal("the session never reached the exec")
			}
			left := waitForPID(t, filepath.Join(dir, "left"))
			defer func() { _ = syscall.Kill(left, syscall.SIGKILL) }() // never leave the sleep behind
			if !processRunning(left) {
				t.Fatalf("the background command %d never ran", left)
			}
			cancel()
			if !endsWithin(left, 5*time.Second) {
				t.Errorf("process %d the session left running in the background survived the cancelled launch", left)
			}
		})
	}
}

// A tool that writes a line past the output ceiling is ended, and what it left
// running with it: the pump's forced end goes through the guard like a stop's.
func TestSessionGuardEndsWhatAnOversizedLineLeftRunning(t *testing.T) {
	if _, err := exec.LookPath("setsid"); err != nil {
		t.Skip("setsid(1) is not installed here")
	}
	for name, runner := range map[string]Runner{"pipes": &procRunner{lineCap: 64}, "pty": &procRunner{lineCap: 64, usePTY: true}} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			// The long line waits for the file the test writes once it has seen the
			// background command run, so only the forced end can have stopped it.
			proc, err := runner.Launch(context.Background(), LaunchSpec{Dir: dir, Program: "/bin/sh", WaitDelay: 200 * time.Millisecond,
				Args: []string{"-c", `setsid sleep 600 </dev/null >/dev/null 2>&1 & echo $! > left; while [ ! -e go ]; do sleep 0.05; done; head -c 4096 /dev/zero | tr '\0' x; echo; exec sleep 600`}})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = proc.Stop(context.Background()) })
			go func() {
				for range proc.Output() {
				}
			}()
			left := waitForPID(t, filepath.Join(dir, "left"))
			defer func() { _ = syscall.Kill(left, syscall.SIGKILL) }() // never leave the sleep behind
			if !processRunning(left) {
				t.Fatalf("the background command %d never ran", left)
			}
			if err := os.WriteFile(filepath.Join(dir, "go"), nil, 0o600); err != nil { // the tool may go on now
				t.Fatal(err)
			}
			if !endsWithin(left, 5*time.Second) {
				t.Errorf("process %d the session left running in the background survived the oversized line", left)
			}
			if _, err := proc.Wait(); !errors.Is(err, ErrOutputLineTooLong) {
				t.Errorf("wait error = %v, want the oversized-line report", err)
			}
		})
	}
}

// A tool that ends by itself while the engine lives keeps its own exit code, and
// what it left running in the background still ends with it: the guard kills the
// tree without turning the status into its own.
func TestSessionGuardKeepsTheExitCodeWhileEndingWhatTheToolLeft(t *testing.T) {
	if _, err := exec.LookPath("setsid"); err != nil {
		t.Skip("setsid(1) is not installed here")
	}
	for name, runner := range map[string]Runner{"pipes": NewProcRunner(), "pty": NewPTYRunner()} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			proc, err := runner.Launch(context.Background(), LaunchSpec{Dir: dir, Program: "/bin/sh", WaitDelay: time.Second,
				Args: []string{"-c", `setsid sleep 600 </dev/null >/dev/null 2>&1 & echo $! > left; while [ ! -e go ]; do sleep 0.05; done; exit 7`}})
			if err != nil {
				t.Fatal(err)
			}
			go func() {
				for range proc.Output() {
				}
			}()
			left := waitForPID(t, filepath.Join(dir, "left"))
			defer func() { _ = syscall.Kill(left, syscall.SIGKILL) }() // never leave the sleep behind
			if !processRunning(left) {
				t.Fatalf("the background command %d never ran", left)
			}
			if err := os.WriteFile(filepath.Join(dir, "go"), nil, 0o600); err != nil { // the tool may go on now
				t.Fatal(err)
			}
			code, err := proc.Wait()
			if err != nil || code != 7 {
				t.Fatalf("wait = (%d, %v), want the tool's own exit code 7", code, err)
			}
			if !endsWithin(left, 5*time.Second) {
				t.Errorf("process %d the tool left running in the background survived its exit", left)
			}
		})
	}
}

// The guard's stderr is a pipe only the engine reads. When the engine dies, the
// guard's first act is to list the tree and, if it cannot, to say so there before
// it kills the group. A write to that broken pipe ends a Go program with SIGPIPE
// unless it catches the signal: the guard would die before it killed anything.
func TestSessionGuardSurvivesABrokenStderr(t *testing.T) {
	stop := holdSignals()
	defer signal.Stop(stop)
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	saved, err := unix.FcntlInt(os.Stderr.Fd(), unix.F_DUPFD_CLOEXEC, 3)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(saved)
	if err := unix.Dup3(int(w.Fd()), int(os.Stderr.Fd()), 0); err != nil {
		t.Fatal(err)
	}
	werr := func() error {
		defer func() {
			if err := unix.Dup3(saved, int(os.Stderr.Fd()), 0); err != nil {
				t.Error(err)
			}
		}()
		_, err := fmt.Fprintln(os.Stderr, "olivares: session guard: cannot list the session's processes")
		return err
	}()
	if !errors.Is(werr, syscall.EPIPE) {
		t.Fatalf("a write to a stderr nobody reads returned %v, want EPIPE and the process alive", werr)
	}
}

// A malformed process record is not an empty tree: cleanup fails closed instead of
// reporting that every descendant was collected.
func TestLiveChildrenReportsMalformedProcessRecords(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "4242"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "4242", "stat"), []byte("not a process record"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := liveChildrenAt(root, 1); err == nil {
		t.Fatal("a malformed process record was treated as an empty process tree")
	}
}

// A process reaped after its stat file was opened reads ESRCH, not ENOENT. That
// ordinary race is a vanished process, not a failure to prove the tree is empty.
func TestLiveChildrenIgnoresAReapedProcessStat(t *testing.T) {
	cmd := exec.Command("/bin/sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	stat, err := os.Open(filepath.Join("/proc", strconv.Itoa(cmd.Process.Pid), "stat"))
	if err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = stat.Close()
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	root := t.TempDir()
	dir := filepath.Join(root, strconv.Itoa(cmd.Process.Pid))
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	openStat := filepath.Join("/proc/self/fd", strconv.Itoa(int(stat.Fd())))
	if err := os.Symlink(openStat, filepath.Join(dir, "stat")); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	var exitErr *exec.ExitError
	if err := cmd.Wait(); !errors.As(err, &exitErr) {
		t.Fatalf("wait for the killed child: %v", err)
	}
	children, err := liveChildrenAt(root, 1)
	if err != nil {
		t.Fatalf("a reaped child's stat read failed the process walk: %v", err)
	}
	if len(children) != 0 {
		t.Fatalf("a reaped child remained in the process list: %v", children)
	}
}

// writeFakeProcess writes a process record under a fake procfs root: its stat line.
func writeFakeProcess(t *testing.T, root string, pid int, state string, ppid int) {
	t.Helper()
	dir := filepath.Join(root, strconv.Itoa(pid))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	stat := fmt.Sprintf("%d (cmd) %s %d 1 1 0 -1", pid, state, ppid)
	if err := os.WriteFile(filepath.Join(dir, "stat"), []byte(stat), 0o600); err != nil {
		t.Fatal(err)
	}
}

// writeFakeChildren writes the kernel's child list of one thread of a process.
func writeFakeChildren(t *testing.T, root string, pid, tid int, children string) {
	t.Helper()
	dir := filepath.Join(root, strconv.Itoa(pid), "task", strconv.Itoa(tid))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "children"), []byte(children), 0o600); err != nil {
		t.Fatal(err)
	}
}

// unreadableStat makes a process whose stat cannot be read (a directory stands in
// for the refusal a confined engine gets for another domain's process).
func unreadableStat(t *testing.T, root string, pid int) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(root, strconv.Itoa(pid), "stat"), 0o700); err != nil {
		t.Fatal(err)
	}
}

// The guard lists its own children from its thread's children file, so a process
// of another domain, whose stat a confined engine may not read, never decides the
// pass: under SELinux the engine may read only the tool's and the commands' state,
// and a walk of every pid died at the first process of anyone else.
func TestLiveChildrenListsTheGuardsChildrenNotEveryProcess(t *testing.T) {
	root := t.TempDir()
	writeFakeProcess(t, root, 4242, "S", 1)
	writeFakeProcess(t, root, 4243, "Z", 1) // a zombie is not live
	writeFakeProcess(t, root, 4245, "D", 1) // one in uninterruptible sleep still is
	writeFakeChildren(t, root, 1, 1, "4242 4243 4245 ")
	unreadableStat(t, root, 9999) // another domain's process
	children, err := liveChildrenAt(root, 1)
	if err != nil {
		t.Fatalf("a process that is not the guard's child failed the pass: %v", err)
	}
	if len(children) != 2 || children[0] != 4242 || children[1] != 4245 {
		t.Fatalf("live children = %v, want [4242 4245]", children)
	}
}

// An orphan of the session goes to the guard's thread the kernel picks, not always
// the one that forked, so every thread's list counts.
func TestLiveChildrenReadsEveryThreadOfTheGuard(t *testing.T) {
	root := t.TempDir()
	writeFakeProcess(t, root, 4242, "S", 1)
	writeFakeChildren(t, root, 1, 1, "")
	writeFakeChildren(t, root, 1, 2, "4242 ")
	children, err := liveChildrenAt(root, 1)
	if err != nil || len(children) != 1 || children[0] != 4242 {
		t.Fatalf("live children = %v, %v; want [4242] from the second thread's list", children, err)
	}
}

// A kernel built without the children file gets the walk of every process, not an
// empty tree.
func TestLiveChildrenWalksWhenTheKernelHasNoChildrenFile(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "1", "task", "1"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeFakeProcess(t, root, 4242, "S", 1)
	writeFakeProcess(t, root, 4243, "S", 77) // someone else's child
	children, err := liveChildrenAt(root, 1)
	if err != nil || len(children) != 1 || children[0] != 4242 {
		t.Fatalf("live children = %v, %v; want [4242] from the walk", children, err)
	}
}

// A child the guard cannot read is not an empty tree: the pass reports it, as for
// a malformed record, and keeps it in the list so a kill pass still ends it.
func TestLiveChildrenKeepsAChildItCannotReadAndSaysSo(t *testing.T) {
	root := t.TempDir()
	writeFakeProcess(t, root, 4242, "S", 1)
	writeFakeChildren(t, root, 1, 1, "4242 4244 ")
	unreadableStat(t, root, 4244)
	children, err := liveChildrenAt(root, 1)
	if err == nil {
		t.Fatal("an unreadable child was treated as an empty process tree")
	}
	if len(children) != 2 || children[0] != 4242 || children[1] != 4244 {
		t.Fatalf("children = %v, want [4242 4244]: the unreadable one is still to be killed", children)
	}
}

// A child whose state cannot be read is killed, and what it started is the guard's
// own child from then on: the next pass must still find and kill it. The fake
// procfs lists the second process only once the first has died, as the kernel
// does when it reparents a dead parent's children to the subreaper.
func TestKillTreeKillsWhatAnUnreadableChildLeftBehind(t *testing.T) {
	saved := killPassPause
	killPassPause = 300 * time.Millisecond // the fake listing changes inside this window
	t.Cleanup(func() { killPassPause = saved })
	start := func() (*exec.Cmd, chan error) {
		cmd := exec.Command("/bin/sleep", "600")
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		exited := make(chan error, 1)
		go func() { exited <- cmd.Wait() }()
		t.Cleanup(func() { _ = cmd.Process.Kill() })
		return cmd, exited
	}
	parent, parentExited := start()
	orphan, orphanExited := start()
	root := t.TempDir()
	writeFakeProcess(t, root, orphan.Process.Pid, "S", 1)
	unreadableStat(t, root, parent.Process.Pid)
	parentPID, orphanPID := strconv.Itoa(parent.Process.Pid), strconv.Itoa(orphan.Process.Pid)
	writeFakeChildren(t, root, 1, 1, parentPID+" ")
	reparented := make(chan error, 1)
	go func() {
		<-parentExited
		reparented <- os.WriteFile(filepath.Join(root, "1", "task", "1", "children"), []byte(parentPID+" "+orphanPID+" "), 0o600)
	}()
	if _, err := killTreeAt(root, 1); err == nil {
		t.Fatal("a child whose state cannot be read was not reported")
	}
	if err := <-reparented; err != nil {
		t.Fatal(err)
	}
	select {
	case <-orphanExited:
	case <-time.After(5 * time.Second):
		t.Fatal("the pass stopped at the error and left the process its unreadable child had started")
	}
}

// One child whose state cannot be read must not keep the pass from killing the
// others: the kill goes out before the error is returned.
func TestKillTreeKillsWhatItListsWhenAStateCannotBeRead(t *testing.T) {
	victim := exec.Command("/bin/sleep", "600")
	if err := victim.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan error, 1)
	go func() { exited <- victim.Wait() }()
	t.Cleanup(func() { _ = victim.Process.Kill() })
	root := t.TempDir()
	writeFakeChildren(t, root, 1, 1, strconv.Itoa(victim.Process.Pid)+" ")
	unreadableStat(t, root, victim.Process.Pid)
	if _, err := killTreeAt(root, 1); err == nil {
		t.Fatal("a child whose state cannot be read was not reported")
	}
	select {
	case <-exited:
	case <-time.After(5 * time.Second):
		t.Fatal("the pass returned its error without killing the child it listed")
	}
}
