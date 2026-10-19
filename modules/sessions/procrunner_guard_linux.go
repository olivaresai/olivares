// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build linux

package sessions

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"

	"github.com/olivaresai/olivares/modules/sessions/confine"
	"github.com/olivaresai/olivares/modules/sessions/egress"
)

// sessionGuardArg selects the session guard among the private reexec stages.
const sessionGuardArg = "--session-guard-v1"

// maxGuardFD bounds the descriptor number the guard is told: the engine passes a
// handful of descriptors, never thousands.
const maxGuardFD = 1024

// The kernel carries the parent-death signal to the process the engine starts,
// not across fork, and nothing runs after a kill -9. So the engine puts a guard
// in front of every session: a process that is its subreaper and so the parent
// of every process the session starts for itself, however it detaches (a double
// fork leaves the guard as parent; setsid leaves the process group, not the tree).
// The guard holds the read end of a pipe whose write end only the engine holds.
// The kernel closes it when the engine dies, and the guard then kills the tree.
//
// Checked before choosing it: a process group kill needs a live process to send
// it; a cgroup kill needs a delegated cgroup (this uid sits in the root cgroup) and
// a live writer; a PID namespace needs an unprivileged user namespace, which a host
// or container may refuse (`unshare --user --pid` fails with EPERM on the build
// host), and by the kernel's rules its init drops SIGTERM from outside, so a
// graceful Stop would turn into SIGKILL.
//
// Limits: the guard covers the session while the program it started runs. After
// that program exits the guard exits, and what the program left running is out of
// its reach, as it was before (a dev server may outlive the tool). A Stop that has
// to escalate kills the process group, the guard with it, so a process that left
// the group with setsid survives that Stop. A process in uninterruptible sleep
// cannot be killed until it wakes; the guard gives up on it after about ten seconds.

// engineWatch is the engine's side of the guard's pipe. read goes to the guard
// and is closed here once the guard has started; hold stays open for the
// session's life, and the engine's death is what closes it.
type engineWatch struct{ read, hold *os.File }

// watchEngine puts the guard in front of cmd and returns the pipe's engine side.
// It runs last, after the confinement and network stages have rewritten cmd, so
// the guard is the process the engine starts and the stages below it are unchanged.
// The guard replaces the parent-death signal on the process the engine starts: a
// guard that is killed with the engine cannot kill the tree.
func watchEngine(cmd *exec.Cmd) (*engineWatch, error) {
	// What Start would have reported for a program that cannot run, now that the
	// process the engine starts is the guard and always can.
	if cmd.Err != nil {
		return nil, cmd.Err
	}
	program := cmd.Path
	if !filepath.IsAbs(program) && cmd.Dir != "" {
		program = filepath.Join(cmd.Dir, program) // the program starts after the chdir
	}
	var st syscall.Stat_t
	if err := syscall.Stat(program, &st); err != nil {
		return nil, &fs.PathError{Op: "fork/exec", Path: cmd.Path, Err: err}
	}
	if st.Mode&syscall.S_IFMT == syscall.S_IFDIR {
		return nil, &fs.PathError{Op: "fork/exec", Path: cmd.Path, Err: syscall.EACCES}
	}
	if err := unix.Access(program, unix.X_OK); err != nil {
		return nil, &fs.PathError{Op: "fork/exec", Path: cmd.Path, Err: err}
	}
	self, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("sessions: locate the engine binary for the session guard: %w", err)
	}
	// A binary upgraded in place runs from a deleted file; its path is the new one.
	self = strings.TrimSuffix(self, " (deleted)")
	read, hold, err := os.Pipe()
	if err != nil {
		return nil, fmt.Errorf("sessions: session guard pipe: %w", err)
	}
	argv := cmd.Args
	if len(argv) == 0 {
		argv = []string{cmd.Path}
	}
	fd := 3 + len(cmd.ExtraFiles)
	cmd.ExtraFiles = append(cmd.ExtraFiles, read)
	cmd.Args = append([]string{self, confine.HelperArg, sessionGuardArg, strconv.Itoa(fd), cmd.Path, "--"}, argv...)
	cmd.Path = self
	if cmd.SysProcAttr != nil {
		cmd.SysProcAttr.Pdeathsig = 0
	}
	return &engineWatch{read: read, hold: hold}, nil
}

// started closes the engine's copy of the guard's end once the guard has it.
func (w *engineWatch) started() {
	if w != nil {
		_ = w.read.Close()
	}
}

// close ends the watch: with the guard gone nothing reads the pipe any more.
func (w *engineWatch) close() {
	if w != nil {
		_ = w.read.Close()
		_ = w.hold.Close()
	}
}

// with returns release that also closes the watch. The write end must stay open
// while the session runs, so the closure is what keeps it from being collected.
func (w *engineWatch) with(release func()) func() {
	if w == nil {
		return release
	}
	return func() { w.close(); release() }
}

// handlesSessionGuard reports whether args select the guard stage.
func handlesSessionGuard(args []string) bool {
	return len(args) > 0 && args[0] == sessionGuardArg
}

// runSessionGuard is the guard stage: args are the stage's flag, the descriptor
// of the pipe, the program's path, "--" and the program's argv. It returns the
// program's exit code, or ends by the signal that ended the program.
func runSessionGuard(args []string) int {
	if len(args) < 5 || args[3] != "--" {
		return guardFailed("malformed arguments")
	}
	fd, err := strconv.Atoi(args[1])
	if err != nil || fd < 3 || fd > maxGuardFD {
		return guardFailed("bad descriptor " + strconv.Quote(args[1]))
	}
	path, argv := args[2], args[4:]
	// Pdeathsig fires when the thread that forked the child exits: stay on this one.
	runtime.LockOSThread()
	engine := os.NewFile(uintptr(fd), "session-guard-engine")
	unix.CloseOnExec(fd)
	if err := unix.Prctl(unix.PR_SET_CHILD_SUBREAPER, 1, 0, 0, 0); err != nil {
		return guardFailed(err.Error())
	}
	holdSignals()
	files := []uintptr{0, 1, 2}
	for i := 3; i < fd; i++ {
		if _, err := unix.FcntlInt(uintptr(i), unix.F_GETFD, 0); err != nil {
			files = append(files, ^uintptr(0)) // the engine left this descriptor closed
			continue
		}
		files = append(files, uintptr(i))
	}
	child, err := syscall.ForkExec(path, argv, &syscall.ProcAttr{Env: os.Environ(), Files: files, Sys: &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}}) // #nosec G204 -- path and argv are the launch chain the engine built for this session (watchEngine), the same program it would have started itself
	if err != nil {
		return guardFailed(fmt.Sprint(&fs.PathError{Op: "fork/exec", Path: path, Err: err}))
	}
	// The stages below own their descriptors now; a copy held here would keep
	// their readiness pipe from ending when they fail.
	for i := 3; i < fd; i++ {
		_ = unix.Close(i)
	}
	dropCapabilities()
	var engineGone atomic.Bool
	go func() {
		if _, err := io.Copy(io.Discard, engine); err != nil { // nil at EOF: every writer is gone, the engine died
			fmt.Fprintln(os.Stderr, "olivares: session guard: cannot watch the engine, the session is not guarded:", err)
			return
		}
		engineGone.Store(true)
		os.Exit(endTree())
	}()
	for {
		var status syscall.WaitStatus
		pid, err := syscall.Wait4(-1, &status, 0, nil) // reaps what the session orphaned, too
		if err == syscall.EINTR {
			continue
		}
		if err != nil {
			return guardFailed("wait: " + err.Error())
		}
		if pid != child {
			continue
		}
		// The program ended while the engine died: the tree is still to be killed.
		if engineGone.Load() || engineClosed(fd) {
			select {} // endTree ends the guard when it is done
		}
		if status.Signaled() {
			return egress.DieOfSignal(status.Signal())
		}
		return status.ExitStatus()
	}
}

// holdSignals keeps the guard alive through the signals that would end it. Stop
// signals the whole group, so the program has its own copy of each, and the
// guard must outlive them while it flushes: the guard's end would kill it. The
// guard's stderr is a pipe only the engine reads, so when the engine dies a
// diagnostic written there ends the program with SIGPIPE before endTree kills
// anything. Notify, not Ignore: an ignored signal stays ignored through exec,
// and the program must keep its default.
func holdSignals() chan os.Signal {
	c := make(chan os.Signal, 1)
	signal.Notify(c, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP, syscall.SIGQUIT, syscall.SIGPIPE)
	return c
}

// guardFailed says why the guard could not start the program. The text reaches
// the engine as the session's diagnostic output, like a helper stage's.
func guardFailed(reason string) int {
	fmt.Fprintln(os.Stderr, "olivares: session guard:", reason)
	return 126
}

// engineClosed reports whether the engine's end of the pipe is closed already.
func engineClosed(fd int) bool {
	polls := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
	n, err := unix.Poll(polls, 0)
	return err == nil && n > 0 && polls[0].Revents&unix.POLLHUP != 0
}

// endTree is the guard's end when the engine died: it kills what the session
// started and says so when it cannot.
func endTree() int {
	survivors, err := killTree(os.Getpid())
	switch {
	case err != nil:
		// Without /proc the tree cannot be listed. The group is all that is left to
		// signal, and it holds the guard too.
		fmt.Fprintln(os.Stderr, "olivares: session guard: cannot list the session's processes, killing its process group:", err)
		_ = syscall.Kill(-os.Getpid(), syscall.SIGKILL)
	case survivors > 0:
		fmt.Fprintf(os.Stderr, "olivares: session guard: %d processes of the session could not be killed\n", survivors)
	}
	return 1
}

// dropCapabilities gives up what the guard holds after the program has started
// with it (the network stage's ambient capabilities): the guard kills same-user
// processes and needs none. Capabilities are per thread, so this goes to all.
// A binary built with cgo cannot do that (ENOTSUP): the engine is built without
// it, so only a test build keeps them.
func dropCapabilities() {
	hdr := unix.CapUserHeader{Version: unix.LINUX_CAPABILITY_VERSION_3}
	var data [2]unix.CapUserData
	_, _, e1 := syscall.AllThreadsSyscall(syscall.SYS_PRCTL, unix.PR_CAP_AMBIENT, unix.PR_CAP_AMBIENT_CLEAR_ALL, 0)
	_, _, e2 := syscall.AllThreadsSyscall(syscall.SYS_CAPSET, uintptr(unsafe.Pointer(&hdr)), uintptr(unsafe.Pointer(&data[0])), 0)
	for _, errno := range []syscall.Errno{e1, e2} {
		if errno != 0 && errno != syscall.ENOTSUP {
			fmt.Fprintf(os.Stderr, "olivares: session guard: keeping its capabilities: %v\n", errno)
			return
		}
	}
}

// killTree kills what lives below the guard and returns how many were still alive
// when it gave up. The guard is the subreaper, so each process of the tree becomes
// its child as soon as the parent it had is killed: a pass that kills the children
// and the next one that kills theirs end it. A process with SIGKILL pending cannot
// finish a fork, so a pass that finds none is the end.
func killTree(guard int) (survivors int, err error) {
	for range 2000 {
		live, err := liveChildren(guard)
		if err != nil || len(live) == 0 {
			return 0, err
		}
		for _, pid := range live {
			_ = syscall.Kill(pid, syscall.SIGKILL) // ESRCH: gone already; anything else shows as a survivor
		}
		time.Sleep(5 * time.Millisecond)
	}
	live, err := liveChildren(guard)
	return len(live), err
}

// liveChildren lists the children of pid that are not zombies, from /proc.
func liveChildren(pid int) ([]int, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}
	var out []int
	for _, e := range entries {
		child, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		stat, err := os.ReadFile("/proc/" + e.Name() + "/stat")
		if err != nil {
			continue
		}
		// "pid (comm) S ppid ...": comm may hold spaces and parentheses, so cut at the last ')'.
		i := strings.LastIndexByte(string(stat), ')')
		if i < 0 {
			continue
		}
		fields := strings.Fields(string(stat[i+1:]))
		if len(fields) < 2 || fields[0] == "Z" || fields[0] == "X" {
			continue
		}
		if ppid, err := strconv.Atoi(fields[1]); err == nil && ppid == pid {
			out = append(out, child)
		}
	}
	return out, nil
}

// isSessionGuard reports whether pid is a session guard, from its argument vector.
func isSessionGuard(procfs string, pid int) bool {
	raw, err := os.ReadFile(filepath.Join(procfs, strconv.Itoa(pid), "cmdline"))
	if err != nil {
		return false
	}
	args := strings.Split(string(raw), "\x00")
	return len(args) > 2 && args[1] == confine.HelperArg && args[2] == sessionGuardArg
}
