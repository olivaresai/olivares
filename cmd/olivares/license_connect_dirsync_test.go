// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build linux && (amd64 || arm64)

package main

// license_connect_dirsync_test.go: a key transition whose completion record is visible after a
// failed directory sync must not destroy a key until the record is published again with every sync
// succeeding (Root review R1 of correction-1).
//
// The failure comes from the KERNEL. The command runs in a child process that installs a seccomp
// user-notification filter on fsync(2); a supervisor goroutine in that child answers an fsync with
// EIO when its descriptor is the connect directory AND state.json there holds a completion record,
// and lets every other fsync run. The product code, state serialization, renames, key files, lease
// and the protocol-unit stub of license_connect_test.go are the real ones; nothing is swapped in.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
	"unsafe"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
)

const (
	// envConnectDirSyncChild names the connect directory whose fsync fails; envConnectDirSyncArgv
	// is the JSON argv of the command.
	envConnectDirSyncChild     = "OLIVARES_CONNECT_DIRSYNC_CHILD"
	envConnectDirSyncArgv      = "OLIVARES_CONNECT_DIRSYNC_ARGV"
	envConnectDirSyncFsyncOnce = "OLIVARES_CONNECT_DIRSYNC_FSYNC_ONCE"

	// Child exit statuses. Any other status is a broken harness, not an answer of the client.
	dirSyncChildEIO         = 24 // the command failed with EIO
	dirSyncChildUnsupported = 25 // the kernel refused the filter
	dirSyncChildUnexpected  = 4
)

// The seccomp ABI (include/uapi/linux/seccomp.h, filter.h, audit.h) used below.
const (
	prSetNoNewPrivs              = 38
	seccompSetModeFilter         = 1
	seccompFilterFlagTSync       = 1
	seccompFilterFlagNewListener = 8
	seccompFilterFlagTSyncESRCH  = 16
	seccompRetAllow              = 0x7fff0000
	seccompRetUserNotif          = 0x7fc00000
	seccompUserNotifFlagContinue = 1
	seccompIoctlNotifRecv        = 0xc0502100
	seccompIoctlNotifSend        = 0xc0182101
	seccompIoctlNotifIDValid     = 0x40082102
	bpfLdWAbs                    = 0x20
	bpfJmpJeqK                   = 0x15
	bpfRetK                      = 0x06
)

var seccompABI = map[string]struct {
	auditArch            uint32
	sysSeccomp, sysFsync uintptr
}{
	"amd64": {auditArch: 0xc000003e, sysSeccomp: 317, sysFsync: 74},
	"arm64": {auditArch: 0xc00000b7, sysSeccomp: 277, sysFsync: 82},
}

type seccompData struct {
	Nr                 int32
	Arch               uint32
	InstructionPointer uint64
	Args               [6]uint64
}

type seccompNotif struct {
	ID    uint64
	Pid   uint32
	Flags uint32
	Data  seccompData
}

type seccompNotifResp struct {
	ID    uint64
	Val   int64
	Error int32
	Flags uint32
}

// installFsyncListener sends every fsync of this process, on every thread, to the returned
// notification descriptor.
func installFsyncListener() (int, syscall.Errno) {
	abi := seccompABI[runtime.GOARCH]
	prog := []syscall.SockFilter{
		{Code: bpfLdWAbs, K: 4},                            // A = seccomp_data.arch
		{Code: bpfJmpJeqK, Jf: 3, K: abi.auditArch},        // another ABI: allow
		{Code: bpfLdWAbs, K: 0},                            // A = seccomp_data.nr
		{Code: bpfJmpJeqK, Jf: 1, K: uint32(abi.sysFsync)}, // not fsync: allow
		{Code: bpfRetK, K: seccompRetUserNotif},
		{Code: bpfRetK, K: seccompRetAllow},
	}
	fprog := syscall.SockFprog{Len: uint16(len(prog)), Filter: &prog[0]}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if _, _, e := syscall.RawSyscall6(syscall.SYS_PRCTL, prSetNoNewPrivs, 1, 0, 0, 0, 0); e != 0 {
		return -1, e
	}
	fd, _, e := syscall.RawSyscall(abi.sysSeccomp, seccompSetModeFilter,
		seccompFilterFlagTSync|seccompFilterFlagNewListener|seccompFilterFlagTSyncESRCH, uintptr(unsafe.Pointer(&fprog)))
	runtime.KeepAlive(prog)
	if e != 0 {
		return -1, e
	}
	return int(fd), 0
}

// connectDirFsyncMustFail reports whether fd is dir while dir/state.json holds a completion record.
func connectDirFsyncMustFail(fd uint64, dir string) bool {
	target, err := os.Readlink("/proc/self/fd/" + strconv.FormatUint(fd, 10))
	if err != nil || target != dir {
		return false
	}
	data, err := os.ReadFile(filepath.Join(dir, connectStateFileName))
	if err != nil {
		return false
	}
	var st connectState
	return json.Unmarshal(data, &st) == nil && st.Completion != nil
}

// superviseConnectDirFsync answers the fsync notifications: EIO for the connect directory while it
// holds a completion record, the real system call for everything else.
//
// The injection count is published before the send that unblocks the caller's fsync, so a Load
// after that syscall returns cannot race the measurement (08 §A row 2).
func superviseConnectDirFsync(listener int, dir string, injected *atomic.Int64) {
	for {
		var n seccompNotif
		if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, uintptr(listener), seccompIoctlNotifRecv, uintptr(unsafe.Pointer(&n))); e != 0 {
			if e == syscall.EINTR || e == syscall.ENOENT {
				continue
			}
			fmt.Printf("seccomp notification receive: %v\n", e)
			os.Exit(dirSyncChildUnexpected)
		}
		resp := seccompNotifResp{ID: n.ID, Flags: seccompUserNotifFlagContinue}
		fail := false
		if connectDirFsyncMustFail(n.Data.Args[0], dir) {
			// The descriptor number is only meaningful while the notification is still live.
			id := n.ID
			if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, uintptr(listener), seccompIoctlNotifIDValid, uintptr(unsafe.Pointer(&id))); e == 0 {
				resp, fail = seccompNotifResp{ID: n.ID, Error: -int32(syscall.EIO)}, true
			}
		}
		if fail {
			injected.Add(1)
		}
		if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, uintptr(listener), seccompIoctlNotifSend, uintptr(unsafe.Pointer(&resp))); e != 0 {
			if fail {
				injected.Add(-1)
			}
			if e == syscall.ENOENT { // the caller is gone; the count is rolled back above
				continue
			}
			fmt.Printf("seccomp notification send: %v\n", e)
			os.Exit(dirSyncChildUnexpected)
		}
	}
}

// TestConnectDirSyncFaultChild is not a test: it is the helper process of the directory-sync
// regression.
func TestConnectDirSyncFaultChild(t *testing.T) {
	dir := os.Getenv(envConnectDirSyncChild)
	if dir == "" {
		t.Skip("helper process for the connect directory-sync regression")
	}
	fsyncOnce := os.Getenv(envConnectDirSyncFsyncOnce) != ""
	var argv []string
	if !fsyncOnce {
		if err := json.Unmarshal([]byte(os.Getenv(envConnectDirSyncArgv)), &argv); err != nil {
			os.Exit(2)
		}
	}
	if unsafe.Sizeof(seccompNotif{}) != 80 || unsafe.Sizeof(seccompNotifResp{}) != 24 ||
		uintptr(syscall.SYS_FSYNC) != seccompABI[runtime.GOARCH].sysFsync {
		fmt.Println("the seccomp ABI of this test does not match the platform")
		os.Exit(2)
	}
	listener, e := installFsyncListener()
	if e != 0 {
		fmt.Printf("seccomp user notification unavailable: %v\n", e)
		os.Exit(dirSyncChildUnsupported)
	}
	var injected atomic.Int64
	go superviseConnectDirFsync(listener, dir, &injected)
	if fsyncOnce {
		reportFsyncOnceAndExit(dir, &injected)
	}
	root := newRootCmd()
	var out, errb bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errb)
	root.SetArgs(argv)
	_, err := root.ExecuteC()
	fmt.Printf("stdout: %s\nstderr: %s\nerr: %v\ninjected_eio=%d\n", out.String(), errb.String(), err, injected.Load())
	switch {
	case err == nil:
		os.Exit(0)
	case errors.Is(err, syscall.EIO):
		os.Exit(dirSyncChildEIO)
	}
	os.Exit(dirSyncChildUnexpected)
}

// reportFsyncOnceAndExit fsyncs dir and prints the injection count immediately when that
// syscall returns. The count must already be visible: it is published before the send
// that unblocks fsync, so this Load does not wait for the notifier.
func reportFsyncOnceAndExit(dir string, injected *atomic.Int64) {
	f, err := os.Open(dir)
	if err != nil {
		fmt.Printf("open connect dir: %v\n", err)
		os.Exit(dirSyncChildUnexpected)
	}
	fsyncErr := syscall.Fsync(int(f.Fd()))
	fmt.Printf("stdout: \nstderr: \nerr: %v\ninjected_eio=%d\n", fsyncErr, injected.Load())
	_ = f.Close()
	if errors.Is(fsyncErr, syscall.EIO) {
		os.Exit(dirSyncChildEIO)
	}
	os.Exit(dirSyncChildUnexpected)
}

var injectedEIOLine = regexp.MustCompile(`(?m)^injected_eio=(\d+)$`)

// runConnectDirSyncChild runs argv in a child whose fsync of dir fails while it holds a completion.
func runConnectDirSyncChild(t *testing.T, dir string, argv []string, extraEnv ...string) (code, injected int, out string) {
	t.Helper()
	enc, err := json.Marshal(argv)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestConnectDirSyncFaultChild$", "-test.count=1", "-test.timeout=2m")
	cmd.Dir = t.TempDir()
	cmd.Env = append(os.Environ(),
		"OLIVARES_CLI_TRAMPOLINE=", // the child must be a test process, not the CLI
		"OLIVARES_CLI_CONFIG="+filepath.Join(cmd.Dir, "config.yaml"),
		"OLIVARES_SERVER_URL=", "OLIVARES_TOKEN=", "OLIVARES_TENANT=",
		"OLIVARES_LICENSE=", "OLIVARES_LICENSE_PATH=",
		envConnectDirSyncChild+"="+dir,
		envConnectDirSyncArgv+"="+string(enc),
	)
	cmd.Env = append(cmd.Env, extraEnv...)
	var buf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &buf, &buf
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			t.Fatalf("the child did not run: %v", err)
		}
	}
	out, code = buf.String(), cmd.ProcessState.ExitCode()
	t.Logf("child -> %v\n%s", cmd.ProcessState, out)
	if code == dirSyncChildUnsupported {
		t.Skipf("the kernel refused a seccomp user-notification filter, so no directory fsync can be failed here: %s", out)
	}
	m := injectedEIOLine.FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("the child reported no injection count (exit %d)", code)
	}
	injected, _ = strconv.Atoi(m[1])
	return code, injected, out
}

// TestReviewConnectRetryMustConfirmRecordBeforeDestructivePromotion: for rotate-key, recover and
// reactivate, the first call publishes the completion record and its directory sync fails with EIO;
// the record is visible and both key roles remain. A FRESH invocation, with that fsync still failing,
// loads the visible record: it must publish the record again before promoting, get EIO, and leave
// both key files, the pending step and the record bytes untouched, sending nothing. Once the
// directory syncs again, the same command completes locally, exactly once.
func TestReviewConnectRetryMustConfirmRecordBeforeDestructivePromotion(t *testing.T) {
	for _, intent := range []string{"rotate", "recover", "reactivate"} {
		t.Run(intent, func(t *testing.T) {
			k := arrangeKeyTransition(t, intent)
			c, stub := k.c, k.stub
			dir, err := filepath.EvalSymlinks(filepath.Join(c.dir, connectDirName))
			if err != nil {
				t.Fatal(err)
			}
			idPath, nextPath, statePath := filepath.Join(dir, connectIdentityFileName), filepath.Join(dir, connectNextKeyFileName), filepath.Join(dir, connectStateFileName)
			sent := func() int {
				stub.mu.Lock()
				defer stub.mu.Unlock()
				return len(stub.seen)
			}
			custody := func(stage, boundKID string) {
				t.Helper()
				cur, curOK := identityKIDAt(t, idPath)
				next, nextOK := identityKIDAt(t, nextPath)
				if curOK != k.oldKept || (curOK && cur != k.oldKID) || !nextOK || next != boundKID {
					t.Fatalf("%s: a key role changed before the completion was confirmed: current %q (%v), next %q (%v)", stage, cur, curOK, next, nextOK)
				}
			}

			// 1. The completion record is renamed into place; its directory sync fails.
			code, injected, out := runConnectDirSyncChild(t, dir, k.argv())
			c.outputs = append(c.outputs, out)
			if code != dirSyncChildEIO || injected != 1 {
				t.Fatalf("first call: exit %d with %d injected EIO, want exit %d after exactly one", code, injected, dirSyncChildEIO)
			}
			stub.mu.Lock()
			boundKID, transitions := stub.deps[k.dep].kid, stub.transitions[k.dep]
			stub.mu.Unlock()
			if transitions != 1 || boundKID == k.oldKID {
				t.Fatalf("the service committed %d transitions", transitions)
			}
			mid := c.state()
			if mid.Completion == nil || mid.Completion.PopKID != boundKID || mid.Pending == nil || mid.Pending.Intent != intent ||
				mid.Binding == nil || mid.Binding.BindingEpoch != 1 {
				t.Fatalf("first call: the visible record, the pending step and binding epoch 1 must remain: %+v", mid)
			}
			custody("first call", boundKID)
			stateBefore, sentBefore := c.file(statePath), sent()

			// 2. A fresh invocation loads the visible record; the directory sync still fails.
			code, injected, out = runConnectDirSyncChild(t, dir, k.argv())
			c.outputs = append(c.outputs, out)
			if code != dirSyncChildEIO || injected != 1 {
				t.Fatalf("retry: exit %d with %d injected EIO, want exit %d after exactly one more", code, injected, dirSyncChildEIO)
			}
			if n := sent(); n != sentBefore {
				t.Fatalf("retry: %d requests were sent", n-sentBefore)
			}
			if !bytes.Equal(c.file(statePath), stateBefore) {
				t.Fatal("retry: the recorded state changed")
			}
			custody("retry under persistent EIO", boundKID)

			// 3. Positive control: the directory syncs again, and the same command completes locally.
			code, rep := c.run(k.args...)
			if code != exitcode.OK || rep["status"] != connectTransitionDone[intent] {
				t.Fatalf("with directory sync restored: %d %v", code, rep)
			}
			if n := sent(); n != sentBefore {
				t.Fatalf("the local completion sent %d requests", n-sentBefore)
			}
			stub.mu.Lock()
			transitions = stub.transitions[k.dep]
			stub.mu.Unlock()
			final := c.state()
			if transitions != 1 || final.Completion != nil || final.Pending != nil || final.Binding == nil ||
				final.Binding.PopKID != boundKID || final.Binding.BindingEpoch != 2 || final.Binding.Status != "active" {
				t.Fatalf("after the completion: %d transitions, state %+v", transitions, final)
			}
			if kid, ok := identityKIDAt(t, idPath); !ok || kid != boundKID {
				t.Fatalf("identity.key holds %q (%v), want the bound key", kid, ok)
			}
			if _, err := os.Lstat(nextPath); !errors.Is(err, fs.ErrNotExist) {
				t.Fatalf("the proposed identity file remains: %v", err)
			}
			if code, rep := c.run("refresh"); code != exitcode.OK || rep["status"] != "refreshed" {
				t.Fatalf("refresh with the promoted key = %d %v", code, rep)
			}
			c.assertNoSecretsLeaked()
		})
	}
}

// TestConnectDirSyncInjectorCountIsVisibleWhenFsyncReturns locks the class: the injection
// count must already be published when fsync returns EIO, without draining the notifier.
// A count incremented after the send that unblocks the caller races this assertion.
func TestConnectDirSyncInjectorCountIsVisibleWhenFsyncReturns(t *testing.T) {
	raw := t.TempDir()
	dir, err := filepath.EvalSymlinks(raw)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, connectStateFileName), []byte(`{"completion":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	code, injected, out := runConnectDirSyncChild(t, dir, nil, envConnectDirSyncFsyncOnce+"=1")
	if code != dirSyncChildEIO || injected != 1 {
		t.Fatalf("fsync-once: exit %d with %d injected EIO, want exit %d after exactly one\n%s", code, injected, dirSyncChildEIO, out)
	}
}
