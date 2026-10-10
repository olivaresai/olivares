// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build linux

package nativepam

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func nativePair(t *testing.T) (*net.UnixConn, *net.UnixConn) {
	t.Helper()
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: filepath.Join(t.TempDir(), "pam.sock"), Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	client, err := net.DialUnix("unix", nil, listener.Addr().(*net.UnixAddr))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	server, err := listener.AcceptUnix()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close() })
	if passCred(server, true) != nil {
		t.Fatal("pass credentials")
	}
	_ = server.SetDeadline(time.Now().Add(time.Second))
	return client, server
}

func TestNativeConversationRejectsDescriptorsAndForeignSender(t *testing.T) {
	for _, foreign := range []bool{false, true} {
		client, server := nativePair(t)
		held, err := peer(server)
		if err != nil {
			t.Fatal(err)
		}
		defer unix.Close(held.pidfd)
		var reply [1]byte
		if foreign {
			held.cred.Pid++
			_, err = client.Write([]byte{1})
		} else {
			file, e := os.Open(os.DevNull)
			if e != nil {
				t.Fatal(e)
			}
			defer file.Close()
			_, _, err = client.WriteMsgUnix([]byte{1}, unix.UnixRights(int(file.Fd())), nil)
		}
		if err != nil {
			t.Fatal(err)
		}
		if err := readAuthenticated(server, reply[:], &held); !errors.Is(err, ErrRefused) || reply[0] != 0 {
			t.Fatal("untrusted range reached the credential parser")
		}
	}
	// Control: an ordinary credential-bearing range from the held kernel
	// sender is accepted; descriptor refusal is not a universally failing read.
	client, server := nativePair(t)
	held, err := peer(server)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(held.pidfd)
	_, _ = client.Write([]byte{7})
	var reply [1]byte
	if err := readAuthenticated(server, reply[:], &held); err != nil || reply[0] != 7 {
		t.Fatal("held peer control failed")
	}
}

func TestNativeAccountRefusalDisposesRequestSecret(t *testing.T) {
	for _, check := range []func(context.Context, string, []byte) (Result, error){CheckRepair, CheckAccount} {
		secret := []byte("fixture-password")
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if result, err := check(ctx, "operator", secret); result != (Result{}) || !errors.Is(err, ErrRefused) {
			t.Fatal("canceled native request was admitted")
		}
		for _, value := range secret {
			if value != 0 {
				t.Fatal("refusal retained the request password")
			}
		}
	}
}

func TestNativeWorkerExitAndCancellationGateCompletion(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	child := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestNativeWorkerLifetimeHelper$")
	child.Env = append(os.Environ(), "IDC_NATIVE_CHILD=1")
	input, err := child.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = input.Close(); _ = child.Process.Kill(); _ = child.Wait() })
	fd, err := unix.PidfdOpen(child.Process.Pid, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(fd)
	held := heldPeer{pidfd: fd}
	if held.finished(context.Background(), time.Now().Add(time.Millisecond)) {
		t.Fatal("a live native worker supplied completion")
	}
	_ = input.Close()
	if !held.finished(context.Background(), time.Now().Add(time.Second)) {
		t.Fatal("the exited native worker control did not complete")
	}
	canceled, stop := context.WithCancel(context.Background())
	stop()
	if held.finished(canceled, time.Now().Add(time.Second)) {
		t.Fatal("a canceled request supplied completion")
	}
}

func TestNativeWorkerLifetimeHelper(t *testing.T) {
	if os.Getenv("IDC_NATIVE_CHILD") != "1" {
		return
	}
	var value [1]byte
	_, _ = os.Stdin.Read(value[:])
}

// The bounded native launcher runs this root/non-root pair with no capabilities
// and NoNewPrivileges. It does not depend on proc executable dereferencing.
func TestNativePeerDifferentUID(t *testing.T) {
	mode := os.Getenv("IDC_NATIVE_CROSS_UID")
	if mode == "" {
		t.Skip("requires the bounded different-UID native launcher")
	}
	var caps [2]unix.CapUserData
	if err := unix.Capget(&unix.CapUserHeader{Version: unix.LINUX_CAPABILITY_VERSION_3}, &caps[0]); err != nil {
		t.Fatal(err)
	}
	for _, c := range caps {
		if c.Effective != 0 || c.Permitted != 0 || c.Inheritable != 0 {
			t.Fatal("native fixture has capabilities")
		}
	}
	nnp, err := unix.PrctlRetInt(unix.PR_GET_NO_NEW_PRIVS, 0, 0, 0, 0)
	if err != nil || nnp != 1 {
		t.Fatal("native fixture lacks NoNewPrivileges")
	}
	address := &net.UnixAddr{Name: os.Getenv("IDC_NATIVE_SOCKET"), Net: "unix"}
	var conn *net.UnixConn
	if mode == "server" {
		listener, e := net.ListenUnix("unix", address)
		if e != nil {
			t.Fatal(e)
		}
		defer listener.Close()
		if e = os.Chmod(address.Name, 0666); e != nil {
			t.Fatal(e)
		}
		conn, e = listener.AcceptUnix()
		if e != nil {
			t.Fatal(e)
		}
	} else if mode == "client" {
		conn, err = net.DialUnix("unix", nil, address)
		if err != nil {
			t.Fatal(err)
		}
	} else {
		t.Fatal("unexpected native fixture role")
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	if passCred(conn, true) != nil {
		t.Fatal("pass credentials")
	}
	held, e := peer(conn)
	if e != nil {
		t.Fatal(e)
	}
	defer unix.Close(held.pidfd)
	if held.cred.Uid == uint32(os.Geteuid()) {
		t.Fatal("fixture did not cross UIDs")
	}
	// This reproduces the original failure, without logging peer argv or data.
	if _, e = os.Stat(fmt.Sprintf("/proc/%d/exe", held.cred.Pid)); !errors.Is(e, os.ErrPermission) {
		t.Fatalf("cross-UID executable denial: %v", e)
	}
	group, e := held.cgroup(os.Args[0])
	if e != nil || group == "" {
		t.Fatalf("held peer cgroup proof refused across UID: %v", e)
	}
	var data [1]byte
	if mode == "client" {
		if _, e = conn.Write([]byte{7}); e != nil {
			t.Fatal(e)
		}
	}
	if e = readAuthenticated(conn, data[:], &held); e != nil {
		t.Fatal(e)
	}
	if !held.alive() {
		t.Fatal("peer proof no longer live")
	}
	unit := "olv-idc-peer-client.service"
	if mode == "client" {
		unit = "olv-idc-peer-server.service"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if !unitMainProcess(ctx, held, unit) {
		t.Fatal("held peer was not its unit MainPID")
	}

	if mode == "server" {
		if data[0] != 7 {
			t.Fatal("foreign credential range")
		}
		if _, e = conn.Write([]byte{9}); e != nil {
			t.Fatal(e)
		}
		if e = readAuthenticated(conn, data[:], &held); e != nil || data[0] != 1 {
			t.Fatal("native peer completion")
		}
	} else {
		if data[0] != 9 {
			t.Fatal("foreign reply range")
		}
		if _, e = conn.Write([]byte{1}); e != nil {
			t.Fatal(e)
		}
		if _, e = conn.Read(data[:]); !errors.Is(e, io.EOF) {
			t.Fatal("native server did not close")
		}
	}
}

// The native launcher invokes this inside its own zero-capability unit. A
// same-UID/same-cgroup child must not inherit the main process's admission.
func TestNativeMainPIDRejectsChild(t *testing.T) {
	mode := os.Getenv("IDC_NATIVE_MAIN_CHILD")
	if mode == "" {
		t.Skip("requires the bounded native unit launcher")
	}
	address := &net.UnixAddr{Name: os.Getenv("IDC_NATIVE_SOCKET"), Net: "unix"}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	if mode == "child" {
		conn, e := net.DialUnix("unix", nil, address)
		if e != nil {
			t.Fatal(e)
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
		var data [1]byte
		if _, e = conn.Read(data[:]); e != nil {
			t.Fatal(e)
		}
		return
	}
	listener, e := net.ListenUnix("unix", address)
	if e != nil {
		t.Fatal(e)
	}
	defer listener.Close()
	child := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestNativeMainPIDRejectsChild$")
	child.Env = []string{"IDC_NATIVE_MAIN_CHILD=child", "IDC_NATIVE_SOCKET=" + address.Name}
	if e = child.Start(); e != nil {
		t.Fatal(e)
	}
	defer func() { _ = child.Process.Kill(); _ = child.Wait() }()
	conn, e := listener.AcceptUnix()
	if e != nil {
		t.Fatal(e)
	}
	defer conn.Close()
	held, e := peer(conn)
	if e != nil {
		t.Fatal(e)
	}
	defer unix.Close(held.pidfd)
	if held.cred.Uid != uint32(os.Geteuid()) {
		t.Fatal("child UID control")
	}
	group, e := held.cgroup(os.Args[0])
	if e != nil || group != "0::/system.slice/olv-idc-peer-child.service" {
		t.Fatalf("child inherited-unit control: %v", e)
	}
	if unitMainProcess(ctx, held, "olv-idc-peer-child.service") {
		t.Fatal("same-unit child inherited native admission")
	}
	self, e := unix.PidfdOpen(os.Getpid(), 0)
	if e != nil {
		t.Fatal(e)
	}
	defer unix.Close(self)
	if !unitMainProcess(ctx, heldPeer{cred: unix.Ucred{Pid: int32(os.Getpid())}, pidfd: self}, "olv-idc-peer-child.service") {
		t.Fatal("native MainPID control failed")
	}
	_, _ = conn.Write([]byte{1})
}
