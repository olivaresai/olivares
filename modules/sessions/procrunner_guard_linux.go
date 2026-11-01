// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build linux

package sessions

import (
	"errors"
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

// sessionGuardKillTreeArg selects the same guard with a typed cleanup outcome for
// the engine, used only by one-shot worktree setup commands: every guard kills the
// tree when its program exits, only this one reports whether it could.
const sessionGuardKillTreeArg = "--session-guard-kill-tree-v1"

// sessionGuardCleanupFailedCode reports unconfirmed setup cleanup. Its trusted
// counterpart is the outcome byte, never this status alone or setup output.
const sessionGuardCleanupFailedCode = 125

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
// The guard covers the session to its end: when the program it started exits —
// the tool finishing, or a stop's signal reaching it — the guard kills every
// remaining descendant before it exits, so a command the session left running
// in the background ends with the session even after it left the process group
// with setsid. A stop that has to escalate closes the guard's watch instead of
// killing the group: the guard then kills the whole tree rather than dying with
// it. What the guard cannot kill it says plainly on the diagnostic stream. A
// process in uninterruptible sleep cannot be killed until it wakes; the guard
// gives up on it after about ten seconds. A guard whose wait itself fails has no
// child left to wait for and ends without the pass.

// engineWatch is the engine's side of the guard's pipes. read and hold watch the
// engine's lifetime. outcomeRead receives the setup tree's typed cleanup outcome;
// outcomeWrite is the guard's end, closed here once the guard has started.
type engineWatch struct{ read, hold, outcomeRead, outcomeWrite *os.File }

// watchEngine puts the guard in front of cmd and returns the pipe's engine side.
// It runs last, after the confinement and network stages have rewritten cmd, so
// the guard is the process the engine starts and the stages below it are unchanged.
// The guard replaces the parent-death signal on the process the engine starts: a
// guard that is killed with the engine cannot kill the tree.
func watchEngine(cmd *exec.Cmd, killTreeOnExit bool) (*engineWatch, error) {
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
	var outcomeRead, outcomeWrite *os.File
	if killTreeOnExit {
		outcomeRead, outcomeWrite, err = os.Pipe()
		if err != nil {
			_ = read.Close()
			_ = hold.Close()
			return nil, fmt.Errorf("sessions: setup guard outcome pipe: %w", err)
		}
	}
	argv := cmd.Args
	if len(argv) == 0 {
		argv = []string{cmd.Path}
	}
	fd := 3 + len(cmd.ExtraFiles)
	cmd.ExtraFiles = append(cmd.ExtraFiles, read)
	stage := sessionGuardArg
	guardArgs := []string{self, confine.HelperArg, stage, strconv.Itoa(fd), cmd.Path, "--"}
	if killTreeOnExit {
		stage = sessionGuardKillTreeArg
		cmd.ExtraFiles = append(cmd.ExtraFiles, outcomeWrite)
		guardArgs = []string{self, confine.HelperArg, stage, strconv.Itoa(fd), strconv.Itoa(fd + 1), cmd.Path, "--"}
	}
	cmd.Args = append(guardArgs, argv...)
	cmd.Path = self
	if cmd.SysProcAttr != nil {
		cmd.SysProcAttr.Pdeathsig = 0
	}
	return &engineWatch{read: read, hold: hold, outcomeRead: outcomeRead, outcomeWrite: outcomeWrite}, nil
}

// started closes the engine's copy of the guard's end once the guard has it.
func (w *engineWatch) started() {
	if w != nil {
		_ = w.read.Close()
		_ = w.outcomeWrite.Close()
	}
}

// guardOutcome is the engine's read end of the setup's typed outcome channel.
func (w *engineWatch) guardOutcome() *os.File {
	if w == nil {
		return nil
	}
	return w.outcomeRead
}

// endLifetime closes only the lifetime watch. The outcome reader stays open until
// the process waiter collects the guard's verdict.
func (w *engineWatch) endLifetime() {
	if w != nil {
		_ = w.read.Close()
		_ = w.hold.Close()
	}
}

// close ends the watch: with the guard gone nothing reads the pipe any more.
func (w *engineWatch) close() {
	w.endLifetime()
	if w != nil {
		_ = w.outcomeRead.Close()
		_ = w.outcomeWrite.Close()
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
	return len(args) > 0 && (args[0] == sessionGuardArg || args[0] == sessionGuardKillTreeArg)
}

// runSessionGuard is the guard stage: args are the stage's flag, the lifetime
// descriptor, the program's path, "--" and the program's argv. Setup adds an
// outcome descriptor after the lifetime descriptor. It returns the program's exit
// code, or ends by the signal that ended the program.
func runSessionGuard(args []string) int {
	killOnExit := len(args) > 0 && args[0] == sessionGuardKillTreeArg
	if killOnExit {
		if len(args) < 6 || args[4] != "--" {
			return guardFailed("malformed setup arguments")
		}
	} else if len(args) < 5 || args[3] != "--" {
		return guardFailed("malformed arguments")
	}
	fd, err := strconv.Atoi(args[1])
	if err != nil || fd < 3 || fd > maxGuardFD {
		return guardFailed("bad descriptor " + strconv.Quote(args[1]))
	}
	path, argv := args[2], args[4:]
	var outcome *os.File
	if killOnExit {
		outcomeFD, err := strconv.Atoi(args[2])
		if err != nil || outcomeFD < 3 || outcomeFD > maxGuardFD {
			return guardFailed("bad setup outcome descriptor " + strconv.Quote(args[2]))
		}
		outcome = os.NewFile(uintptr(outcomeFD), "session-guard-outcome")
		unix.CloseOnExec(outcomeFD)
		path, argv = args[3], args[5:]
	}
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
		if killOnExit {
			os.Exit(endSetupTree(outcome))
		}
		os.Exit(endTree())
	}()
	for {
		var status syscall.WaitStatus
		pid, err := syscall.Wait4(-1, &status, 0, nil) // reaps what the session orphaned, too
		if err == syscall.EINTR {
			continue
		}
		if err != nil {
			if killOnExit {
				fmt.Fprintln(os.Stderr, "olivares: session guard: wait:", err)
				return endSetupTree(outcome)
			}
			return guardFailed("wait: " + err.Error())
		}
		if pid != child {
			continue
		}
		// The program ended while the engine died: the tree is still to be killed.
		if engineGone.Load() || engineClosed(fd) {
			select {} // endTree ends the guard when it is done
		}
		// The session is over: what it started ends with it. A command the tool
		// left running in the background is the guard's child by the subreaper,
		// whatever process group it left for, so this pass reaches what a stop's
		// group signal cannot.
		survivors, err := killTree(os.Getpid())
		if killOnExit {
			failed := err != nil || survivors > 0
			if err != nil {
				fmt.Fprintln(os.Stderr, "olivares: session guard: cannot list the setup's processes:", err)
			}
			if survivors > 0 {
				fmt.Fprintf(os.Stderr, "olivares: session guard: %d setup processes could not be killed\n", survivors)
			}
			reportSessionGuardOutcome(outcome, failed)
			if failed {
				return sessionGuardCleanupFailedCode
			}
		} else {
			// Unlike endTree, which has only the group left to signal, a program that
			// ended by itself keeps its own exit status: the group kill would take this
			// guard with it and turn that status into SIGKILL. So when the tree cannot be
			// fully listed the guard kills what it did list, says what it could not on its
			// stderr, the session's diagnostic output, and a later Stop still ends the group's members.
			if err != nil {
				fmt.Fprintln(os.Stderr, "olivares: session guard: cannot list the session's processes, what it left running may still run:", err)
			}
			if survivors > 0 {
				fmt.Fprintf(os.Stderr, "olivares: session guard: %d processes of the session could not be killed\n", survivors)
			}
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
		fmt.Fprintln(os.Stderr, "olivares: session guard: cannot list the session's processes, killing its process group; what left the group may still run:", err)
		_ = syscall.Kill(-os.Getpid(), syscall.SIGKILL)
	case survivors > 0:
		fmt.Fprintf(os.Stderr, "olivares: session guard: %d processes of the session could not be killed\n", survivors)
	}
	// End by SIGKILL, as the group kill this replaces did: the engine reads a forced
	// end as killed, not as the tool's own failure.
	return egress.DieOfSignal(syscall.SIGKILL)
}

// endSetupTree is endTree for a one-shot setup: its typed outcome reaches the
// engine even when cancellation was the reason the guard ended.
func endSetupTree(outcome *os.File) int {
	survivors, err := killTree(os.Getpid())
	if err != nil {
		fmt.Fprintln(os.Stderr, "olivares: session guard: cannot list the setup's processes:", err)
	}
	if survivors > 0 {
		fmt.Fprintf(os.Stderr, "olivares: session guard: %d setup processes could not be killed\n", survivors)
	}
	reportSessionGuardOutcome(outcome, err != nil || survivors > 0)
	return 1
}

// reportSessionGuardOutcome writes one trusted byte: 0 means the setup tree is
// empty, 1 means cleanup is unconfirmed. Setup output is never this channel.
func reportSessionGuardOutcome(outcome *os.File, failed bool) {
	if outcome == nil {
		return
	}
	status := byte(0)
	if failed {
		status = 1
	}
	_, _ = outcome.Write([]byte{status})
	_ = outcome.Close()
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
	return killTreeAt("/proc", guard)
}

// killPassPause is the wait between two kill passes, long enough for a killed
// process to die and hand its children to the guard. A variable so a test can
// widen the window it changes the listing in.
var killPassPause = 5 * time.Millisecond

// killTreeAt is killTree at a procfs root. A pass kills every child it lists before
// it looks at the error: a child whose state cannot be read is still killed, and
// the error says the tree could not be proven empty. Such a child cannot be told
// from a zombie, so it is signalled once and the passes go on until one finds
// nothing new to kill: what it had started is the guard's child by then.
func killTreeAt(procfs string, guard int) (survivors int, err error) {
	signalled := map[int]bool{}
	var unread error
	for range 2000 {
		live, err := liveChildrenAt(procfs, guard)
		if unread == nil {
			unread = err
		}
		fresh := false
		for _, pid := range live {
			fresh = fresh || !signalled[pid]
			signalled[pid] = true
			_ = syscall.Kill(pid, syscall.SIGKILL) // ESRCH: gone already; anything else shows as a survivor
		}
		if len(live) == 0 || (err != nil && !fresh) {
			return 0, unread
		}
		time.Sleep(killPassPause)
	}
	live, err := liveChildrenAt(procfs, guard)
	if err == nil {
		err = unread
	}
	return len(live), err
}

// liveChildrenAt lists the children of pid that are not zombies, from the procfs
// at root. The kernel keeps the list of a process's children in its own /proc
// entry (one file per thread), so the guard reads only what it needs: under
// SELinux the engine may read the state of its tool's and commands' domains and no
// other, and a walk of every pid died at the first process of anyone else. A
// kernel without that file (CONFIG_PROC_CHILDREN) gets the walk. Only a process
// that vanished between the listing and its stat read is ignored; every other
// failure is an error, never evidence that the tree is empty. A listed child whose
// state cannot be read stays in the list with the error, so a kill pass can still
// end it.
func liveChildrenAt(procfs string, pid int) ([]int, error) {
	kids, ok, err := listedChildren(procfs, pid)
	if err != nil {
		return nil, err
	}
	if !ok {
		return scannedChildren(procfs, pid)
	}
	var out []int
	var unread error
	for _, child := range kids {
		state, _, err := processState(procfs, child)
		if err != nil {
			out = append(out, child) // state unknown: kill it, and say it could not be read
			if unread == nil {
				unread = err
			}
			continue
		}
		if isLive(state) {
			out = append(out, child)
		}
	}
	return out, unread
}

// listedChildren reads pid's children from /proc/<pid>/task/<tid>/children, each
// thread's own list: an orphan of the session goes to the guard's thread the
// kernel picks, not always the one that forked. ok is false when no thread has the
// file, as on a kernel built without it.
func listedChildren(procfs string, pid int) (kids []int, ok bool, err error) {
	taskDir := filepath.Join(procfs, strconv.Itoa(pid), "task")
	tasks, err := os.ReadDir(taskDir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, false, nil
		}
		return nil, false, err
	}
	for _, task := range tasks {
		raw, err := os.ReadFile(filepath.Join(taskDir, task.Name(), "children"))
		if errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ESRCH) {
			continue // no such file here, or the thread ended since the listing
		}
		if err != nil {
			return nil, false, fmt.Errorf("read the children of process %d: %w", pid, err)
		}
		ok = true
		for _, field := range strings.Fields(string(raw)) {
			child, err := strconv.Atoi(field)
			if err != nil {
				return nil, false, fmt.Errorf("parse the children of process %d: %w", pid, err)
			}
			kids = append(kids, child)
		}
	}
	return kids, ok, nil
}

// scannedChildren is liveChildrenAt without the kernel's list: every process's
// parent, read from its stat. A record it cannot read leaves it unable to say whose
// child that process is, so it returns the error and lists nothing, and the pass
// kills nothing: only a kernel without CONFIG_PROC_CHILDREN reaches this.
func scannedChildren(procfs string, pid int) ([]int, error) {
	entries, err := os.ReadDir(procfs)
	if err != nil {
		return nil, err
	}
	var out []int
	for _, e := range entries {
		child, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		state, ppid, err := processState(procfs, child)
		if err != nil {
			return nil, err
		}
		if isLive(state) && ppid == pid {
			out = append(out, child)
		}
	}
	return out, nil
}

// isLive reports whether a state letter from processState is a process that still
// runs: not vanished, not a zombie, not dead.
func isLive(state string) bool { return state != "" && state != "Z" && state != "X" }

// processState reads a process's state letter and parent from /proc/<pid>/stat.
// An empty state means the process vanished between the listing and this read.
func processState(procfs string, pid int) (state string, ppid int, err error) {
	stat, err := os.ReadFile(filepath.Join(procfs, strconv.Itoa(pid), "stat"))
	if err != nil {
		if os.IsNotExist(err) || errors.Is(err, syscall.ESRCH) {
			return "", 0, nil
		}
		return "", 0, fmt.Errorf("read process %d stat: %w", pid, err)
	}
	// "pid (comm) S ppid ...": comm may hold spaces and parentheses, so cut at the last ')'.
	i := strings.LastIndexByte(string(stat), ')')
	if i < 0 {
		return "", 0, fmt.Errorf("parse process %d stat", pid)
	}
	fields := strings.Fields(string(stat[i+1:]))
	if len(fields) < 2 {
		return "", 0, fmt.Errorf("parse process %d stat", pid)
	}
	ppid, err = strconv.Atoi(fields[1])
	if err != nil {
		return "", 0, fmt.Errorf("parse process %d parent: %w", pid, err)
	}
	return fields[0], ppid, nil
}

// isSessionGuard reports whether pid is a session guard, from its argument vector.
func isSessionGuard(procfs string, pid int) bool {
	raw, err := os.ReadFile(filepath.Join(procfs, strconv.Itoa(pid), "cmdline"))
	if err != nil {
		return false
	}
	args := strings.Split(string(raw), "\x00")
	return len(args) > 2 && args[1] == confine.HelperArg && (args[2] == sessionGuardArg || args[2] == sessionGuardKillTreeArg)
}
