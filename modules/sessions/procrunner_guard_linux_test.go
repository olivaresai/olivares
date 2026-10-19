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
