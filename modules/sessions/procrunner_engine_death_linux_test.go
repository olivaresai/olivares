// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build linux

package sessions

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/olivaresai/olivares/modules/sessions/confine"
	"github.com/olivaresai/olivares/modules/sessions/egress"
)

const (
	engineDeathEnv     = "OLIVARES_TEST_ENGINE_DEATH"
	engineDeathModeEnv = "OLIVARES_TEST_ENGINE_DEATH_MODE"
)

// kill -9 of the engine must not leave a session process behind: nothing runs
// after SIGKILL, so the kernel has to end the session. The engine here is a
// re-executed copy of this test binary that launches the session through the
// real runner and then waits to be killed. The modes are the runners and the
// launch chains production uses: a plain child, Landlock's helper stage in
// front of it, and the network supervisor in front of that. What is proved is
// the whole tree: the launch chain the engine builds and the processes the
// session starts for itself, including one that ignores SIGHUP, one that was
// orphaned by a double fork and one that left the session with setsid(1).
func TestProcRunnerSessionProcessesEndWhenEngineIsKilled(t *testing.T) {
	if dir := os.Getenv(engineDeathEnv); dir != "" {
		engineDeathEngine(dir, os.Getenv(engineDeathModeEnv))
		return
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"pipes", "pty", "landlock", "landlock-network", "pipes-storm"} {
		t.Run(mode, func(t *testing.T) {
			if strings.HasPrefix(mode, "landlock") {
				if s := confine.Probe(); s.Mode != confine.ModeLandlock {
					t.Fatal("this reproducer requires Landlock: " + s.Reason)
				}
			}
			dir := t.TempDir()
			engine := exec.Command(self, "-test.run=^TestProcRunnerSessionProcessesEndWhenEngineIsKilled$")
			engine.Env = append(os.Environ(), engineDeathEnv+"="+dir, engineDeathModeEnv+"="+mode)
			var engineLog strings.Builder
			engine.Stderr = &engineLog
			engine.WaitDelay = 2 * time.Second // a surviving session process holds the stderr pipe open
			if err := engine.Start(); err != nil {
				t.Fatal(err)
			}
			engineGone := false
			defer func() {
				if !engineGone {
					_ = engine.Process.Kill()
					_ = engine.Wait()
				}
			}()
			defer func() {
				if t.Failed() {
					t.Logf("engine stderr: %s", engineLog.String())
				}
			}()
			child := waitForPID(t, filepath.Join(dir, "child"))
			if !waitForFile(filepath.Join(dir, "ready")) {
				t.Fatal("the session never reached the exec")
			}
			// Never leave the sleeps behind when the assertion below fails.
			defer func() { _ = syscall.Kill(-child, syscall.SIGKILL) }()
			if mode == "pipes-storm" {
				// Never leave the storm behind when the assertion below fails.
				defer func() {
					for _, pid := range stormPIDs() {
						_ = syscall.Kill(pid, syscall.SIGKILL)
					}
				}()
				time.Sleep(time.Second) // let the session fork for a while
			}
			chain := append([]int{child}, descendantsOf(child)...)
			started := sessionStartedPIDs(t, dir)
			for _, own := range started {
				defer func() { _ = syscall.Kill(own.pid, syscall.SIGKILL) }()
			}
			if err := engine.Process.Kill(); err != nil {
				t.Fatal(err)
			}
			_ = engine.Wait()
			engineGone = true
			for _, pid := range chain {
				if !endsWithin(pid, 5*time.Second) {
					t.Errorf("process %d of the session's launch chain (the engine started %d) is still alive 5s after the engine was killed with SIGKILL", pid, child)
				}
			}
			if mode == "pipes-storm" {
				// Processes the session was still starting when the engine died are in no
				// list taken before: look for them by what they run.
				if n := stormLive(5 * time.Second); n > 0 {
					t.Errorf("%d of the processes a session kept starting are still alive 5s after the engine was killed with SIGKILL", n)
				}
			}
			for _, own := range started {
				if !endsWithin(own.pid, 5*time.Second) {
					t.Errorf("%s process %d, started by the session for itself, is still alive 5s after the engine was killed with SIGKILL", own.kind, own.pid)
				}
			}
		})
	}
}

// engineDeathEngine is the engine side: launch the session, publish its pid, wait to be killed.
func engineDeathEngine(dir, mode string) {
	runner := NewProcRunner()
	spec := LaunchSpec{Dir: dir, Program: "/bin/sh"}
	switch mode {
	case "pty":
		runner = NewPTYRunner()
	case "landlock", "landlock-network":
		spec.Confinement, spec.ConfinementRequired = &confine.Policy{ReadWrite: []string{dir}}, true
		if mode == "landlock-network" {
			spec.NetworkPolicy = &egress.Policy{Providers: []string{"http://127.0.0.1:11434"}}
		}
	}
	// The session ignores SIGHUP (the disposition survives exec). Without that, the
	// pty runner's session would end from the hangup the kernel sends when the
	// engine's end of the terminal closes, and the test could not tell whether the
	// parent-death signal is set.
	// The session then starts what a real one does for itself: a background
	// command, a daemon whose parent exits at once, and a command that leaves the
	// session's process group (each ignores SIGHUP, as the disposition is inherited).
	// Each writes its pid before the marker, which says the exec is next.
	script := `trap '' HUP
sleep 600 & echo $! > ` + sessionStartedFiles[0].file + `
( sleep 600 & echo $! > ` + sessionStartedFiles[1].file + ` )
if command -v setsid >/dev/null; then setsid sleep 600 & echo $! > ` + sessionStartedFiles[2].file + `; fi
: > ready
exec sleep 600`
	if mode == "pipes-storm" {
		// A session that is still forking when the engine dies: a loop starting one
		// more process every few milliseconds (each runs for a distinctive time).
		script = strings.Replace(script, ": > ready", "(while :; do sleep "+stormSleep+" & sleep 0.01; done) &\n: > ready", 1)
	}
	spec.Args = []string{"-c", script}
	proc, err := runner.Launch(context.Background(), spec)
	if err != nil {
		fmt.Fprintln(os.Stderr, "launch:", err)
		var refused boundaryErr
		// A host that cannot give the launch its network boundary is not the behavior
		// under test. The probe is the kernel's own answer, not the launch's: a launch
		// that fails on a host that can make the namespaces is a failure of ours.
		if errors.As(err, &refused) && !hostMakesNetworkNamespaces() {
			_ = os.WriteFile(filepath.Join(dir, "unavailable"), []byte(err.Error()), 0o600)
		}
		os.Exit(2)
	}
	child := filepath.Join(dir, "child")
	pid := strconv.Itoa(proc.(*procProcess).cmd.Process.Pid)
	if err := os.WriteFile(child+".tmp", []byte(pid+"\n"), 0o600); err != nil {
		fmt.Fprintln(os.Stderr, "write child:", err)
		os.Exit(2)
	}
	if err := os.Rename(child+".tmp", child); err != nil {
		fmt.Fprintln(os.Stderr, "publish child:", err)
		os.Exit(2)
	}
	for range proc.Output() { // prints nothing; blocks until the session ends
	}
}

// sessionStartedFiles are the pid files the session writes for the processes it
// starts for itself, with the kind of each as the failure names it.
var sessionStartedFiles = []struct{ file, kind string }{
	{"background", "background"},
	{"orphan", "double-forked"},
	{"setsid", "setsid"},
}

type startedProcess struct {
	pid  int
	kind string
}

// sessionStartedPIDs reads the pids the session published, which is the only
// way to find a process the kernel has moved away from the session's own tree.
// A file is absent only when the host has no setsid(1).
func sessionStartedPIDs(t *testing.T, dir string) []startedProcess {
	t.Helper()
	var out []startedProcess
	for _, f := range sessionStartedFiles {
		path := filepath.Join(dir, f.file)
		if _, err := os.Stat(path); err != nil {
			if f.file == "setsid" {
				t.Log("setsid(1) is not installed here: the setsid case is not exercised")
				continue
			}
			t.Fatalf("the session never published %s", f.file)
		}
		out = append(out, startedProcess{waitForPID(t, path), f.kind})
	}
	return out
}

// stormSleep is how long the processes of the storm mode run: distinctive, so a
// scan of /proc finds them and nothing else.
const stormSleep = "6137"

// stormPIDs lists the live processes of the storm, by the program they run.
func stormPIDs() []int {
	var pids []int
	entries, _ := os.ReadDir("/proc")
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		cmdline, err := os.ReadFile("/proc/" + e.Name() + "/cmdline")
		if err != nil || string(cmdline) != "sleep\x00"+stormSleep+"\x00" {
			continue
		}
		if stat, err := os.ReadFile("/proc/" + e.Name() + "/stat"); err == nil {
			if i := strings.LastIndexByte(string(stat), ')'); i >= 0 && strings.HasPrefix(string(stat[i+1:]), " Z") {
				continue
			}
		}
		pids = append(pids, pid)
	}
	return pids
}

// stormLive waits up to d for the storm's processes to end and returns how many
// still live.
func stormLive(d time.Duration) int {
	deadline := time.Now().Add(d)
	for {
		n := len(stormPIDs())
		if n == 0 || time.Now().After(deadline) {
			return n
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// hostMakesNetworkNamespaces reports whether an unprivileged process can enter
// the user and network namespaces the network boundary is made of.
func hostMakesNetworkNamespaces() bool {
	probe := exec.Command("/bin/true")
	probe.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags:  syscall.CLONE_NEWUSER | syscall.CLONE_NEWNET,
		UidMappings: []syscall.SysProcIDMap{{ContainerID: os.Getuid(), HostID: os.Getuid(), Size: 1}},
		GidMappings: []syscall.SysProcIDMap{{ContainerID: os.Getgid(), HostID: os.Getgid(), Size: 1}},
	}
	return probe.Run() == nil
}

func waitForPID(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(filepath.Join(filepath.Dir(path), "unavailable")); err == nil {
			t.Skip("launch unavailable on this host: " + string(b))
		}
		if b, err := os.ReadFile(path); err == nil {
			if pid, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil && pid > 0 {
				return pid
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("the engine never published %s", filepath.Base(path))
	return 0
}

// descendantsOf lists the processes below pid, from the parent pids in /proc.
func descendantsOf(pid int) []int {
	entries, _ := os.ReadDir("/proc")
	children := map[int][]int{}
	for _, e := range entries {
		p, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		stat, err := os.ReadFile("/proc/" + e.Name() + "/stat")
		if err != nil {
			continue
		}
		// "pid (comm) S ppid ...": cut at the last ')'.
		i := strings.LastIndexByte(string(stat), ')')
		if i < 0 {
			continue
		}
		if f := strings.Fields(string(stat[i+1:])); len(f) > 1 {
			if ppid, err := strconv.Atoi(f[1]); err == nil {
				children[ppid] = append(children[ppid], p)
			}
		}
	}
	var out []int
	for queue := []int{pid}; len(queue) > 0; queue = queue[1:] {
		out = append(out, children[queue[0]]...)
		queue = append(queue, children[queue[0]]...)
	}
	return out
}

// endsWithin reports whether the process is gone or a zombie, which is dead:
// only its reaper's wait is missing, and in a container that reaper may not exist.
func endsWithin(pid int, d time.Duration) bool {
	deadline := time.Now().Add(d)
	for {
		stat, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
		if err != nil {
			return true
		}
		// "pid (comm) S ...": comm may hold spaces and parentheses, so cut at the last ')'.
		if i := strings.LastIndexByte(string(stat), ')'); i >= 0 && strings.HasPrefix(string(stat[i+1:]), " Z") {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(20 * time.Millisecond)
	}
}
