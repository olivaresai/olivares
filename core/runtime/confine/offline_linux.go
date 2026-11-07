// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build linux

package confine

import (
	"errors"
	"runtime"
	"unsafe"

	"golang.org/x/sys/unix"
)

// denyNetwork is installed only in the re-exec helper, on its locked OS thread.
// The converter inherits pipes, not sockets. Deny creation and use of sockets as
// well as the alternate paths for obtaining another process's descriptors.
func denyNetwork() error {
	var arch uint32
	switch runtime.GOARCH {
	case "amd64":
		arch = unix.AUDIT_ARCH_X86_64
	case "arm64":
		arch = unix.AUDIT_ARCH_AARCH64
	default:
		return errors.New("offline confinement requires Linux amd64 or arm64")
	}
	deny := uint32(unix.SECCOMP_RET_ERRNO) | uint32(unix.EPERM)
	filter := []unix.SockFilter{
		{Code: unix.BPF_LD | unix.BPF_W | unix.BPF_ABS, K: 4},
		{Code: unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K, K: arch, Jt: 1},
		{Code: unix.BPF_RET | unix.BPF_K, K: unix.SECCOMP_RET_KILL_PROCESS},
		{Code: unix.BPF_LD | unix.BPF_W | unix.BPF_ABS, K: 0},
	}
	if runtime.GOARCH == "amd64" {
		filter = append(filter, unix.SockFilter{Code: unix.BPF_JMP | unix.BPF_JSET | unix.BPF_K, K: 0x40000000, Jf: 1}, unix.SockFilter{Code: unix.BPF_RET | unix.BPF_K, K: deny})
	}
	for _, nr := range []uint32{unix.SYS_SOCKET, unix.SYS_SOCKETPAIR, unix.SYS_CONNECT, unix.SYS_SENDTO, unix.SYS_SENDMSG, unix.SYS_SENDMMSG, unix.SYS_SETNS, unix.SYS_UNSHARE, unix.SYS_PTRACE, unix.SYS_PROCESS_VM_READV, unix.SYS_PROCESS_VM_WRITEV, unix.SYS_PIDFD_GETFD, unix.SYS_IO_URING_SETUP, unix.SYS_IO_URING_ENTER, unix.SYS_IO_URING_REGISTER} {
		filter = append(filter, unix.SockFilter{Code: unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K, K: nr, Jf: 1}, unix.SockFilter{Code: unix.BPF_RET | unix.BPF_K, K: deny})
	}
	// Python's format detector uses worker threads. Allow threads, never a
	// new process or a namespace; clone3 falls back to the inspectable clone ABI.
	filter = append(filter,
		unix.SockFilter{Code: unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K, K: unix.SYS_CLONE3, Jf: 1},
		unix.SockFilter{Code: unix.BPF_RET | unix.BPF_K, K: unix.SECCOMP_RET_ERRNO | uint32(unix.ENOSYS)},
		unix.SockFilter{Code: unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K, K: unix.SYS_CLONE, Jf: 6},
		unix.SockFilter{Code: unix.BPF_LD | unix.BPF_W | unix.BPF_ABS, K: 16},
		unix.SockFilter{Code: unix.BPF_JMP | unix.BPF_JSET | unix.BPF_K, K: unix.CLONE_NEWUSER | unix.CLONE_NEWNET | unix.CLONE_NEWNS | unix.CLONE_NEWPID | unix.CLONE_NEWIPC | unix.CLONE_NEWUTS | unix.CLONE_NEWCGROUP, Jf: 1},
		unix.SockFilter{Code: unix.BPF_RET | unix.BPF_K, K: deny},
		unix.SockFilter{Code: unix.BPF_JMP | unix.BPF_JSET | unix.BPF_K, K: unix.CLONE_THREAD, Jt: 1},
		unix.SockFilter{Code: unix.BPF_RET | unix.BPF_K, K: deny},
		unix.SockFilter{Code: unix.BPF_RET | unix.BPF_K, K: unix.SECCOMP_RET_ALLOW},
	)
	// fork and vfork exist only on amd64; arm64 uses clone, denied above.
	if runtime.GOARCH == "amd64" {
		for _, nr := range []uint32{57, 58} {
			filter = append(filter, unix.SockFilter{Code: unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K, K: nr, Jf: 1}, unix.SockFilter{Code: unix.BPF_RET | unix.BPF_K, K: deny})
		}
	}
	filter = append(filter, unix.SockFilter{Code: unix.BPF_RET | unix.BPF_K, K: unix.SECCOMP_RET_ALLOW})
	program := unix.SockFprog{Len: uint16(len(filter)), Filter: &filter[0]}
	return unix.Prctl(unix.PR_SET_SECCOMP, unix.SECCOMP_MODE_FILTER, uintptr(unsafe.Pointer(&program)), 0, 0)
}
