// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build linux

package egress

import (
	"errors"
	"fmt"
	"runtime"
	"unsafe"

	"golang.org/x/sys/unix"

	"github.com/olivaresai/olivares/core/runtime/confine"
)

// The bridge alone may open host Unix sockets. The tool can create IP sockets
// inside its empty network namespace, and anonymous stream pairs for child IPC.
// Namespace entry, descriptor theft and alternate syscall paths are denied.
func restrictSockets() error {
	runtime.LockOSThread() // the existing Landlock helper execs from this thread
	var arch uint32
	switch runtime.GOARCH {
	case "amd64":
		arch = unix.AUDIT_ARCH_X86_64
	case "arm64":
		arch = unix.AUDIT_ARCH_AARCH64
	default:
		return errors.New("session network confinement requires Linux amd64 or arm64")
	}
	if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		return err
	}
	if err := confine.DropCapabilities(true); err != nil {
		return err
	}
	deny := uint32(unix.SECCOMP_RET_ERRNO) | uint32(unix.EPERM)
	filter := []unix.SockFilter{
		{Code: unix.BPF_LD | unix.BPF_W | unix.BPF_ABS, K: 4},
		{Code: unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K, K: arch, Jt: 1},
		{Code: unix.BPF_RET | unix.BPF_K, K: unix.SECCOMP_RET_KILL_PROCESS},
		{Code: unix.BPF_LD | unix.BPF_W | unix.BPF_ABS, K: 0},
	}
	// x32 shares the x86_64 audit arch but has a separate syscall table.
	if runtime.GOARCH == "amd64" {
		filter = append(filter, unix.SockFilter{Code: unix.BPF_JMP | unix.BPF_JSET | unix.BPF_K, K: 0x40000000, Jf: 1}, unix.SockFilter{Code: unix.BPF_RET | unix.BPF_K, K: deny})
	}
	for _, nr := range []uint32{unix.SYS_SETNS, unix.SYS_UNSHARE, unix.SYS_PTRACE, unix.SYS_PROCESS_VM_READV, unix.SYS_PROCESS_VM_WRITEV, unix.SYS_PIDFD_GETFD, unix.SYS_IO_URING_SETUP, unix.SYS_IO_URING_ENTER, unix.SYS_IO_URING_REGISTER} {
		filter = append(filter, unix.SockFilter{Code: unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K, K: nr, Jf: 1}, unix.SockFilter{Code: unix.BPF_RET | unix.BPF_K, K: deny})
	}
	filter = append(filter,
		// Datagram pairs can sendto a different pathname on the host. Only
		// already-connected Unix streams are safe anonymous child IPC.
		unix.SockFilter{Code: unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K, K: unix.SYS_SOCKETPAIR, Jf: 8},
		unix.SockFilter{Code: unix.BPF_LD | unix.BPF_W | unix.BPF_ABS, K: 16},
		unix.SockFilter{Code: unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K, K: unix.AF_UNIX, Jt: 1},
		unix.SockFilter{Code: unix.BPF_RET | unix.BPF_K, K: deny},
		unix.SockFilter{Code: unix.BPF_LD | unix.BPF_W | unix.BPF_ABS, K: 24},
		unix.SockFilter{Code: unix.BPF_ALU | unix.BPF_AND | unix.BPF_K, K: ^uint32(unix.SOCK_CLOEXEC | unix.SOCK_NONBLOCK)},
		unix.SockFilter{Code: unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K, K: unix.SOCK_STREAM, Jt: 1},
		unix.SockFilter{Code: unix.BPF_RET | unix.BPF_K, K: deny},
		unix.SockFilter{Code: unix.BPF_RET | unix.BPF_K, K: unix.SECCOMP_RET_ALLOW},
		unix.SockFilter{Code: unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K, K: unix.SYS_CLONE3, Jf: 1},
		unix.SockFilter{Code: unix.BPF_RET | unix.BPF_K, K: unix.SECCOMP_RET_ERRNO | uint32(unix.ENOSYS)},
		unix.SockFilter{Code: unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K, K: unix.SYS_CLONE, Jf: 4},
		unix.SockFilter{Code: unix.BPF_LD | unix.BPF_W | unix.BPF_ABS, K: 16},
		unix.SockFilter{Code: unix.BPF_JMP | unix.BPF_JSET | unix.BPF_K, K: unix.CLONE_NEWUSER | unix.CLONE_NEWNET | unix.CLONE_NEWNS | unix.CLONE_NEWPID | unix.CLONE_NEWIPC | unix.CLONE_NEWUTS | unix.CLONE_NEWCGROUP, Jf: 1},
		unix.SockFilter{Code: unix.BPF_RET | unix.BPF_K, K: deny},
		unix.SockFilter{Code: unix.BPF_RET | unix.BPF_K, K: unix.SECCOMP_RET_ALLOW},
		unix.SockFilter{Code: unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K, K: unix.SYS_SOCKET, Jf: 4},
		unix.SockFilter{Code: unix.BPF_LD | unix.BPF_W | unix.BPF_ABS, K: 16},
		unix.SockFilter{Code: unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K, K: unix.AF_INET, Jt: 2},
		unix.SockFilter{Code: unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K, K: unix.AF_INET6, Jt: 1},
		unix.SockFilter{Code: unix.BPF_RET | unix.BPF_K, K: deny},
		unix.SockFilter{Code: unix.BPF_RET | unix.BPF_K, K: unix.SECCOMP_RET_ALLOW},
	)
	program := unix.SockFprog{Len: uint16(len(filter)), Filter: &filter[0]}
	if err := unix.Prctl(unix.PR_SET_SECCOMP, unix.SECCOMP_MODE_FILTER, uintptr(unsafe.Pointer(&program)), 0, 0); err != nil {
		return fmt.Errorf("install socket seccomp filter: %w", err)
	}
	return nil
}
