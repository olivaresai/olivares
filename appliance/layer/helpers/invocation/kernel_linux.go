// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build linux && (amd64 || arm64)

package invocation

import (
	"bufio"
	"errors"
	"os"
	"os/user"
	"strconv"
	"strings"
	"syscall"
)

// soPeerPidfd is SO_PEERPIDFD (Linux 6.5), the asm-generic value amd64 and arm64 use.
const soPeerPidfd = 77

// sysPidfdSendSignal is pidfd_send_signal(2) (Linux 5.1), the same number on every
// architecture.
const sysPidfdSendSignal = 424

// Linux is the kernel of an installed appliance.
type Linux struct{}

// PeerCred implements Kernel.
func (Linux) PeerCred(fd int) (int, uint32, error) {
	cred, err := syscall.GetsockoptUcred(fd, syscall.SOL_SOCKET, syscall.SO_PEERCRED)
	if err != nil {
		return 0, 0, err
	}
	return int(cred.Pid), cred.Uid, nil
}

// PeerPidfd implements Kernel. A kernel older than 6.5 answers ENOPROTOOPT.
func (Linux) PeerPidfd(fd int) (int, error) {
	return syscall.GetsockoptInt(fd, syscall.SOL_SOCKET, soPeerPidfd)
}

// PidOf implements Kernel from the pidfd's fdinfo, whose "Pid:" line is the pid in this pid
// namespace, or -1 once the process has exited.
func (Linux) PidOf(pidfd int) (int, error) {
	f, err := os.Open("/proc/self/fdinfo/" + strconv.Itoa(pidfd))
	if err != nil {
		return 0, err
	}
	defer f.Close()
	lines := bufio.NewScanner(f)
	for lines.Scan() {
		if value, ok := strings.CutPrefix(lines.Text(), "Pid:"); ok {
			pid, err := strconv.Atoi(strings.TrimSpace(value))
			if err != nil || pid <= 0 {
				return 0, errors.New("the pidfd names no live process")
			}
			return pid, nil
		}
	}
	return 0, errors.New("the pidfd's fdinfo states no pid")
}

// Cgroup implements Kernel.
func (Linux) Cgroup(pid int) (string, error) {
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/cgroup")
	return string(data), err
}

// Stat implements Kernel.
func (Linux) Stat(pid int) (string, error) {
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	return string(data), err
}

// AccountOf implements Kernel from the system's user database.
func (Linux) AccountOf(uid uint32) (string, error) {
	account, err := user.LookupId(strconv.FormatUint(uint64(uid), 10))
	if err != nil {
		return "", err
	}
	return account.Username, nil
}

// Alive implements Kernel with signal 0, which delivers nothing. EPERM means the process
// exists and belongs to another account; only ESRCH, and any other failure, means it is gone
// or cannot be told.
func (Linux) Alive(pidfd int) error {
	_, _, errno := syscall.Syscall6(sysPidfdSendSignal, uintptr(pidfd), 0, 0, 0, 0, 0)
	if errno == 0 || errno == syscall.EPERM {
		return nil
	}
	return errno
}

// Close implements Kernel.
func (Linux) Close(fd int) { _ = syscall.Close(fd) }
