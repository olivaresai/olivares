// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build linux && (amd64 || arm64)

package localframe

import (
	"os"
	"syscall"
	"testing"
)

func TestReader_KernelDescriptorsArriveCloseOnExecAndAreClosed(t *testing.T) {
	pair, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM|syscall.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer syscall.Close(pair[0])
	defer syscall.Close(pair[1])
	if err := syscall.SetsockoptInt(pair[1], syscall.SOL_SOCKET, syscall.SO_PASSCRED, 1); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if err := syscall.Sendmsg(pair[0], wire(`{"t":"ready","v":1}`), syscall.UnixRights(int(file.Fd())), nil, 0); err != nil {
		t.Fatal(err)
	}
	received := -1
	recv := func(p, oob []byte, flags int) (int, int, int, error) {
		n, on, out, _, err := syscall.Recvmsg(pair[1], p, oob, flags)
		messages, _ := syscall.ParseSocketControlMessage(oob[:on])
		for _, m := range messages {
			if m.Header.Type == syscall.SCM_RIGHTS {
				fds, _ := syscall.ParseUnixRights(&m)
				for _, fd := range fds {
					received = fd
					bits, _, errno := syscall.Syscall(syscall.SYS_FCNTL, uintptr(fd), syscall.F_GETFD, 0)
					if errno != 0 || bits&syscall.FD_CLOEXEC == 0 {
						t.Fatal("received descriptor lacks close-on-exec")
					}
				}
			}
		}
		return n, on, out, err
	}
	if _, err := next(recv, Expect{UID: uint32(os.Getuid())}, nil, 65534, 65534); err == nil {
		t.Fatal("descriptor accepted")
	}
	if received < 0 {
		t.Fatal("no kernel descriptor measured")
	}
	if err := syscall.Close(received); err != syscall.EBADF {
		t.Fatalf("descriptor leaked: %v", err)
	}
}
