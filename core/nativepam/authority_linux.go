// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build linux

package nativepam

import (
	"context"
	"net"
	"os"
	"os/user"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

const AuthoritySocketPath = "/run/olivares-portal-api/authority.sock"
const originalPortalSocket = "/run/olivares-portal-api/local.sock"

// AuthorityPeer reuses the native PAM channel's held-kernel-peer and fixed-unit
// attestation. Only the closed constructors can supply its private custody.
// It authenticates transport/process identity, never product authority.
type AuthorityPeer struct {
	peer    heldPeer
	service string
	socket  int
}

func (p *AuthorityPeer) UID() uint32 { return p.peer.cred.Uid }
func (p *AuthorityPeer) PID() int32  { return p.peer.cred.Pid }
func (p *AuthorityPeer) Close() {
	if p != nil && p.peer.cred.Pid > 0 && p.peer.pidfd >= 0 {
		_ = unix.Close(p.peer.pidfd)
		p.peer.pidfd = -1
	}
}
func (p *AuthorityPeer) Check(ctx context.Context) error {
	if p.Alive(ctx) != nil {
		return ErrRefused
	}
	if p.service != "" {
		service, err := callerService(ctx, p.peer)
		if err != nil || service != p.service {
			return ErrRefused
		}
	}
	return nil
}

func (p *AuthorityPeer) Alive(ctx context.Context) error {
	if p == nil || ctx == nil || ctx.Err() != nil || p.peer.cred.Pid <= 0 || p.peer.cred.Uid == 0 || p.peer.pidfd < 0 || !p.peer.alive() {
		return ErrRefused
	}
	if p.socket >= 0 {
		fds := []unix.PollFd{{Fd: int32(p.socket)}}
		_, err := unix.Poll(fds, 0)
		if err != nil || fds[0].Revents&(unix.POLLHUP|unix.POLLERR|unix.POLLNVAL) != 0 {
			return ErrRefused
		}
	}
	return nil
}

// HoldPortalAuthority accepts only the fixed portal main process; an inherited
// socket or another process with the same UID/unit is insufficient.
func HoldPortalAuthority(ctx context.Context, c *net.UnixConn) (*AuthorityPeer, error) {
	if c == nil || passCred(c, false) != nil {
		return nil, ErrRefused
	}
	p, err := peer(c)
	if err != nil {
		return nil, ErrRefused
	}
	fd := -1
	raw, err := c.SyscallConn()
	if err != nil || raw.Control(func(value uintptr) { fd = int(value) }) != nil {
		_ = unix.Close(p.pidfd)
		return nil, ErrRefused
	}
	held := &AuthorityPeer{peer: p, service: RepairService, socket: fd}
	if held.Check(ctx) != nil {
		held.Close()
		return nil, ErrRefused
	}
	return held, nil
}

// HoldEngineAuthority pins the existing system manager's current engine, not
// SO_PEERCRED of the socket activator (which may identify PID1). The consumer
// must also authenticate every reply range against this held PID and UID.
func HoldEngineAuthority(ctx context.Context) (*AuthorityPeer, error) {
	if !trustedSocket(AuthoritySocketPath) {
		return nil, ErrRefused
	}
	account, err := user.Lookup("olivares")
	if err != nil {
		return nil, ErrRefused
	}
	uid, err := strconv.ParseUint(account.Uid, 10, 32)
	if err != nil || uid == 0 {
		return nil, ErrRefused
	}
	pid, err := unitMainPID(ctx, "olivares.service")
	if err != nil {
		return nil, ErrRefused
	}
	pidfd, err := unix.PidfdOpen(int(pid), 0)
	if err != nil {
		return nil, ErrRefused
	}
	unix.CloseOnExec(pidfd)
	held := &AuthorityPeer{peer: heldPeer{cred: unix.Ucred{Pid: int32(pid), Uid: uint32(uid)}, pidfd: pidfd}, service: AccountService, socket: -1}
	if held.Check(ctx) != nil {
		held.Close()
		return nil, ErrRefused
	}
	return held, nil
}

// HoldOriginalAuthority verifies the transferred original accepted socket
// without wrapping it in net.FileConn, reading it or changing its flags.
// Its nonzero UID is observed from the kernel, never from an RPC field.
func HoldOriginalAuthority(ctx context.Context, fd int) (*AuthorityPeer, error) {
	return holdOriginalAuthority(ctx, fd, originalPortalSocket)
}

func holdOriginalAuthority(ctx context.Context, fd int, expectedPath string) (*AuthorityPeer, error) {
	typ, err := unix.GetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_TYPE)
	if err != nil || typ != unix.SOCK_STREAM {
		return nil, ErrRefused
	}
	domain, err := unix.GetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_DOMAIN)
	if err != nil || domain != unix.AF_UNIX {
		return nil, ErrRefused
	}
	address, err := unix.Getsockname(fd)
	local, ok := address.(*unix.SockaddrUnix)
	if err != nil || !ok || local.Name != expectedPath {
		return nil, ErrRefused
	}
	if _, err = unix.Getpeername(fd); err != nil {
		return nil, ErrRefused
	}
	cred, err := unix.GetsockoptUcred(fd, unix.SOL_SOCKET, unix.SO_PEERCRED)
	if err != nil || cred.Pid <= 0 || cred.Uid == 0 {
		return nil, ErrRefused
	}
	// Namespace overflow identities cannot name a host account. Read the same
	// fixed kernel settings used by the local framed connection admission.
	for _, item := range []struct {
		path string
		id   uint32
	}{
		{"/proc/sys/kernel/overflowuid", cred.Uid}, {"/proc/sys/kernel/overflowgid", cred.Gid},
	} {
		data, err := os.ReadFile(item.path)
		if err != nil {
			return nil, ErrRefused
		}
		overflow, err := strconv.ParseUint(strings.TrimSpace(string(data)), 10, 32)
		if err != nil || uint32(overflow) == item.id {
			return nil, ErrRefused
		}
	}
	pidfd, err := unix.GetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_PEERPIDFD)
	if err != nil {
		return nil, ErrRefused
	}
	unix.CloseOnExec(pidfd)
	held := &AuthorityPeer{peer: heldPeer{cred: *cred, pidfd: pidfd}, socket: fd}
	if held.Check(ctx) != nil {
		held.Close()
		return nil, ErrRefused
	}
	return held, nil
}
