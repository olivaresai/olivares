// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build linux

package nativepam

import (
	"context"
	"errors"
	"golang.org/x/sys/unix"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestOriginalAuthorityHoldsKernelPeerWithoutChangingStream(t *testing.T) {
	zero := new(AuthorityPeer)
	if zero.Check(context.Background()) == nil {
		t.Fatal("zero peer supplied kernel custody")
	}
	stdinFlags, stdinErr := unix.FcntlInt(0, unix.F_GETFD, 0)
	zero.Close()
	if flags, err := unix.FcntlInt(0, unix.F_GETFD, 0); err != stdinErr || flags != stdinFlags {
		t.Fatal("zero peer closed unowned standard input")
	}
	if os.Geteuid() == 0 {
		t.Skip("UID0 cannot acquire product attribution")
	}
	path, err := os.MkdirTemp("", "idc-ka-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(path) })
	socket := filepath.Join(path, "peer.sock")
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: socket, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	client, err := net.DialUnix("unix", nil, listener.Addr().(*net.UnixAddr))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	server, err := listener.AcceptUnix()
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	raw, err := server.SyscallConn()
	if err != nil {
		t.Fatal(err)
	}
	fd := -1
	var failure error
	if err = raw.Control(func(value uintptr) { fd, failure = unix.FcntlInt(value, unix.F_DUPFD_CLOEXEC, 0) }); err != nil || failure != nil {
		t.Fatal(err, failure)
	}
	defer unix.Close(fd)
	before, err := unix.FcntlInt(uintptr(fd), unix.F_GETFL, 0)
	if err != nil {
		t.Fatal(err)
	}
	if peer, err := HoldOriginalAuthority(context.Background(), fd); err == nil || peer != nil {
		t.Fatal("foreign socket path admitted")
	}
	peer, err := holdOriginalAuthority(context.Background(), fd, socket)
	if err != nil {
		t.Fatal(err)
	}
	pidfd := peer.peer.pidfd
	defer peer.Close()
	if peer.UID() != uint32(os.Geteuid()) || peer.PID() != int32(os.Getpid()) {
		t.Fatal("kernel identity substituted")
	}
	cloexec, err := unix.FcntlInt(uintptr(pidfd), unix.F_GETFD, 0)
	if err != nil || cloexec&unix.FD_CLOEXEC == 0 {
		t.Fatal("pidfd not close-on-exec")
	}
	after, err := unix.FcntlInt(uintptr(fd), unix.F_GETFL, 0)
	if err != nil || after != before {
		t.Fatal("original stream flags changed")
	}
	if _, err = client.Write([]byte{7}); err != nil {
		t.Fatal(err)
	}
	_ = server.SetReadDeadline(time.Now().Add(time.Second))
	var payload [1]byte
	if _, err = server.Read(payload[:]); err != nil || payload[0] != 7 {
		t.Fatal("proof check consumed original stream")
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if peer.Check(canceled) == nil {
		t.Fatal("canceled proof admitted")
	}
	if err = client.Close(); err != nil {
		t.Fatal(err)
	}
	if peer.Check(context.Background()) == nil {
		t.Fatal("disconnected proof admitted")
	}
	peer.Close()
	if _, err = unix.FcntlInt(uintptr(pidfd), unix.F_GETFD, 0); !errors.Is(err, unix.EBADF) {
		t.Fatal("held pidfd leaked")
	}
	if _, err = unix.GetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_TYPE); err != nil {
		t.Fatal("closing proof closed caller-owned socket")
	}
}
