// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build linux && !386

package portal

import (
	"errors"
	"net"
	"syscall"
	"testing"
)

func TestPortalListen_BoundDeviceReadsTheKernelBindingOfARealSocket(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	if got := boundDevice(l); got != "" {
		t.Fatalf("a socket bound to no interface reads %q", got)
	}

	conn, ok := l.(syscall.Conn)
	if !ok {
		t.Fatalf("%T carries no descriptor", l)
	}
	raw, err := conn.SyscallConn()
	if err != nil {
		t.Fatal(err)
	}
	var bindErr error
	if err := raw.Control(func(fd uintptr) { bindErr = syscall.BindToDevice(int(fd), "lo") }); err != nil {
		t.Fatal(err)
	}
	if errors.Is(bindErr, syscall.EPERM) {
		t.Skip("binding a socket to a device needs CAP_NET_RAW on this kernel")
	}
	if bindErr != nil {
		t.Fatal(bindErr)
	}
	// The kernel reports the name with its terminating NUL in the option length.
	if got := boundDevice(l); got != "lo" {
		t.Fatalf("a socket bound to lo reads %q", got)
	}
}
