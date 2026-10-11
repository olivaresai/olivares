// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build linux

package nativepam

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

type heldPeer struct {
	cred  unix.Ucred
	pidfd int
}

func (p heldPeer) alive() bool {
	fds := []unix.PollFd{{Fd: int32(p.pidfd), Events: unix.POLLIN}}
	n, err := unix.Poll(fds, 0)
	return err == nil && n == 0 && fds[0].Revents == 0
}

func (p heldPeer) finished(ctx context.Context, until time.Time) bool {
	fds := []unix.PollFd{{Fd: int32(p.pidfd), Events: unix.POLLIN}}
	for time.Now().Before(until) {
		n, err := unix.Poll(fds, max(1, int(time.Until(until)/time.Millisecond)))
		if n > 0 || (err != nil && err != unix.EINTR) {
			return err == nil && n == 1 && fds[0].Revents&unix.POLLIN != 0 &&
				fds[0].Revents&(unix.POLLNVAL|unix.POLLERR) == 0 && ctx.Err() == nil
		}
	}
	return false
}

func peer(c *net.UnixConn) (heldPeer, error) {
	var p heldPeer
	raw, err := c.SyscallConn()
	if err != nil {
		return p, ErrRefused
	}
	var failure error
	err = raw.Control(func(fd uintptr) {
		cred, e := unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
		if e != nil || cred.Pid <= 0 {
			failure = ErrRefused
			return
		}
		p.cred = *cred
		p.pidfd, failure = unix.GetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_PEERPIDFD)
		if failure == nil {
			unix.CloseOnExec(p.pidfd)
		}
	})
	if err != nil || failure != nil {
		if failure == nil {
			_ = unix.Close(p.pidfd)
		}
		return heldPeer{}, ErrRefused
	}
	return p, nil
}

func passCred(c *net.UnixConn, set bool) error {
	raw, err := c.SyscallConn()
	if err != nil {
		return ErrRefused
	}
	var failure error
	err = raw.Control(func(fd uintptr) {
		if set {
			failure = unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_PASSCRED, 1)
			return
		}
		value, e := unix.GetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_PASSCRED)
		if e != nil || value != 1 {
			failure = ErrRefused
		}
	})
	if err != nil || failure != nil {
		return ErrRefused
	}
	return nil
}

// readAuthenticated refuses and closes every received descriptor. Each range
// must come from the same live kernel sender; no password bytes are copied to
// the request until its ancillary credentials have been checked.
func readAuthenticated(c *net.UnixConn, dst []byte, expected *heldPeer) error {
	raw, err := c.SyscallConn()
	if err != nil {
		return ErrRefused
	}
	for offset := 0; offset < len(dst); {
		buf := make([]byte, len(dst)-offset)
		oob := make([]byte, unix.CmsgSpace(unix.SizeofUcred)+unix.CmsgSpace(253*4))
		var n, on, flags int
		var receiveErr error
		err = raw.Read(func(fd uintptr) bool {
			n, on, flags, _, receiveErr = unix.Recvmsg(int(fd), buf, oob, unix.MSG_CMSG_CLOEXEC)
			return receiveErr != unix.EAGAIN && receiveErr != unix.EWOULDBLOCK && receiveErr != unix.EINTR
		})
		var cred *unix.Ucred
		valid := true
		var controlErr error
		for offset := 0; offset+unix.SizeofCmsghdr <= on; {
			var length uint64
			if unix.SizeofCmsghdr == 16 {
				length = binary.NativeEndian.Uint64(oob[offset : offset+8])
			} else {
				length = uint64(binary.NativeEndian.Uint32(oob[offset : offset+4]))
			}
			if length < uint64(unix.CmsgLen(0)) || length > uint64(on-offset) {
				valid = false
				break
			}
			messages, e := unix.ParseSocketControlMessage(oob[offset : offset+int(length)])
			if e != nil || len(messages) != 1 {
				valid = false
				break
			}
			m := messages[0]
			switch {
			case m.Header.Level == unix.SOL_SOCKET && m.Header.Type == unix.SCM_RIGHTS:
				// Even a later malformed message cannot hide already delivered FDs.
				for i := 0; i+4 <= len(m.Data); i += 4 {
					_ = unix.Close(int(int32(binary.NativeEndian.Uint32(m.Data[i : i+4]))))
				}
				valid = false
			case m.Header.Level == unix.SOL_SOCKET && m.Header.Type == unix.SCM_CREDENTIALS && cred == nil:
				cred, controlErr = unix.ParseUnixCredentials(&m)
			default:
				valid = false
			}
			offset += unix.CmsgSpace(int(length) - unix.CmsgLen(0))
		}
		if err != nil || receiveErr != nil || controlErr != nil || !valid || n == 0 ||
			flags&(unix.MSG_CTRUNC|unix.MSG_TRUNC|unix.MSG_OOB|unix.MSG_ERRQUEUE) != 0 || cred == nil || cred.Pid <= 0 {
			clear(buf)
			return ErrRefused
		}
		if expected.pidfd < 0 {
			// A socket-activated connection's SO_PEERCRED names systemd. The
			// credential-bearing ready reply pins the actual fixed worker.
			if cred.Uid != 0 {
				clear(buf)
				return ErrRefused
			}
			fd, e := unix.PidfdOpen(int(cred.Pid), 0)
			if e != nil {
				clear(buf)
				return ErrRefused
			}
			unix.CloseOnExec(fd)
			expected.cred, expected.pidfd = *cred, fd
		}
		if *cred != expected.cred || !expected.alive() {
			clear(buf)
			return ErrRefused
		}
		copy(dst[offset:], buf[:n])
		clear(buf)
		offset += n
	}
	return nil
}

func trustedFile(path string, executable bool) bool {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0022 != 0 {
		return false
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	return ok && st.Uid == 0 && (!executable || info.Mode().Perm()&0111 != 0)
}

func trustedSocket(path string) bool {
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSocket == 0 || info.Mode().Perm() != 0660 {
		return false
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || st.Uid != 0 {
		return false
	}
	group, err := user.LookupGroup("olivares-appliance")
	if err != nil {
		return false
	}
	gid, err := strconv.ParseUint(group.Gid, 10, 32)
	if err != nil || st.Gid != uint32(gid) {
		return false
	}
	for dir := filepath.Dir(path); ; dir = filepath.Dir(dir) {
		info, err = os.Lstat(dir)
		if err != nil || !info.IsDir() || info.Mode().Perm()&0022 != 0 {
			return false
		}
		st, ok = info.Sys().(*syscall.Stat_t)
		if !ok || st.Uid != 0 {
			return false
		}
		if dir == "/" {
			return true
		}
	}
}

// cgroup reads only the kernel-attested PID held by this connection. Cross-UID
// /proc/PID/exe is ptrace-gated, so it cannot attest these zero-capability units.
// The root-owned system unit fixes the binary; UID, live pidfd and exact system
// cgroup membership attest the process without granting ptrace or shadow access.
func (p heldPeer) cgroup(executable string) (string, error) {
	if !p.alive() || !trustedFile(executable, true) {
		return "", ErrRefused
	}
	file, err := os.Open(fmt.Sprintf("/proc/%d/cgroup", p.cred.Pid))
	if err != nil {
		return "", ErrRefused
	}
	data, err := io.ReadAll(io.LimitReader(file, 4097))
	_ = file.Close()
	if err != nil || len(data) > 4096 {
		return "", ErrRefused
	}
	if !p.alive() {
		return "", ErrRefused
	}
	return strings.TrimSuffix(string(data), "\n"), nil
}
func nativeUnit(ctx context.Context, p heldPeer, unit, executable string, template bool) bool {
	line, err := p.cgroup(executable)
	if err != nil {
		return false
	}
	name := unit + ".service"
	if template {
		parent, observedName := filepath.Split(line)
		name = observedName
		parent = strings.TrimSuffix(parent, "/")
		templateSlice := "0::/system.slice/system-" + strings.ReplaceAll(unit, "-", `\x2d`) + ".slice"
		if (parent != "0::/system.slice" && parent != templateSlice) || !strings.HasPrefix(name, unit+"@") || !strings.HasSuffix(name, ".service") {
			return false
		}
	} else if line != "0::/system.slice/"+unit+".service" {
		return false
	}
	return unitMainProcess(ctx, p, name)
}

// Unit membership alone admits descendants (including engine session/MCP
// children). The existing system manager must identify this exact held PID as
// the unit's current main process; the fixed native read is bounded and uncached.
func unitMainProcess(ctx context.Context, p heldPeer, unit string) bool {
	if ctx == nil || ctx.Err() != nil || !p.alive() {
		return false
	}
	pid, err := unitMainPID(ctx, unit)
	return err == nil && uint32(p.cred.Pid) == pid && p.alive()
}

func unitMainPID(ctx context.Context, unit string) (uint32, error) {
	if ctx == nil || ctx.Err() != nil || !trustedFile("/usr/bin/systemctl", true) || len(unit) > 255 || strings.ContainsAny(unit, "\n\r\x00/") {
		return 0, ErrRefused
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "/usr/bin/systemctl", "--system", "show", "--property=MainPID", "--value", "--", unit)
	var output bytes.Buffer
	command.Stdout = &output
	command.WaitDelay = 250 * time.Millisecond
	if command.Run() != nil || ctx.Err() != nil || output.Len() > 16 {
		return 0, ErrRefused
	}
	pid, err := strconv.ParseUint(strings.TrimSuffix(output.String(), "\n"), 10, 32)
	if err != nil || pid == 0 || pid > 2147483647 {
		return 0, ErrRefused
	}
	return uint32(pid), nil
}

func callerService(ctx context.Context, p heldPeer) (string, error) {
	for _, role := range []struct{ account, unit, binary, service string }{
		{"olivares-portal", "olivares-portal", "/usr/libexec/olivares/olivares-portal", RepairService},
		{"olivares", "olivares", "/usr/bin/olivares", AccountService},
	} {
		account, err := user.Lookup(role.account)
		if err != nil {
			continue
		}
		uid, err := strconv.ParseUint(account.Uid, 10, 32)
		if err == nil && uid != 0 && p.cred.Uid == uint32(uid) && nativeUnit(ctx, p, role.unit, role.binary, false) {
			return role.service, nil
		}
	}
	return "", ErrRefused
}

func check(ctx context.Context, service, login string, password []byte) (result Result, failure error) {
	if ctx == nil || ctx.Err() != nil || !ValidRequest(login, password) || !trustedSocket(SocketPath) {
		return Result{}, ErrRefused
	}
	ctx, cancel := context.WithTimeout(ctx, WorkerLifetime)
	defer cancel()
	conn, err := (&net.Dialer{}).DialContext(ctx, "unix", SocketPath)
	if err != nil {
		return Result{}, ErrRefused
	}
	c, ok := conn.(*net.UnixConn)
	if !ok {
		_ = conn.Close()
		return Result{}, ErrRefused
	}
	worker := heldPeer{pidfd: -1}
	defer func() {
		clear(password)
		_ = c.Close()
		if worker.pidfd >= 0 {
			// Disconnect cancels the worker even while PAM is blocked. Do not
			// return a lost/canceled check while its native process still lives.
			if !worker.finished(ctx, time.Now().Add(WorkerLifetime+time.Second)) {
				result, failure = Result{}, ErrRefused
			}
			_ = unix.Close(worker.pidfd)
		}
	}()
	stop := context.AfterFunc(ctx, func() { _ = c.Close() })
	defer stop()
	deadline, _ := ctx.Deadline()
	_ = c.SetDeadline(deadline)
	if passCred(c, true) != nil {
		return Result{}, ErrRefused
	}
	if _, err := c.Write([]byte("OPM1")); err != nil {
		return Result{}, ErrRefused
	}
	var ready [1]byte
	wantService := byte(1)
	if service == AccountService {
		wantService = 2
	}
	if readAuthenticated(c, ready[:], &worker) != nil || ready[0] != wantService || !nativeUnit(ctx, worker, "olivares-portal-pam", WorkerPath, true) {
		return Result{}, ErrRefused
	}
	var header [4]byte
	binary.BigEndian.PutUint16(header[:2], uint16(len(login)))
	binary.BigEndian.PutUint16(header[2:], uint16(len(password)))
	for _, part := range [][]byte{header[:], []byte(login), password} {
		if n, err := c.Write(part); err != nil || n != len(part) {
			return Result{}, ErrRefused
		}
	}
	var reply [5]byte
	if readAuthenticated(c, reply[:], &worker) != nil || !nativeUnit(ctx, worker, "olivares-portal-pam", WorkerPath, true) || ctx.Err() != nil || reply[0] > 3 || reply[0] == 2 {
		return Result{}, ErrRefused
	}
	if _, err := c.Write([]byte{1}); err != nil {
		return Result{}, ErrRefused
	}
	result = Result{Authenticated: reply[0]&1 != 0, AccountAllowed: reply[0]&2 != 0, Login: login, UID: binary.BigEndian.Uint32(reply[1:])}
	if service == AccountService && result.UID == 0 {
		return Result{}, ErrRefused
	}
	return result, nil
}

// ServeWorker serves exactly one systemd-accepted Unix connection. The fixed
// binary must terminate its whole process when ctx is canceled: PAM's C call
// cannot safely be interrupted or moved to a retained in-process transaction.
func ServeWorker(ctx context.Context, c *net.UnixConn, cancel context.CancelFunc) error {
	defer c.Close()
	if ctx == nil || cancel == nil || ctx.Err() != nil || !nativeBuilt || passCred(c, false) != nil {
		return ErrRefused
	}
	p, err := peer(c)
	if err != nil {
		return ErrRefused
	}
	defer unix.Close(p.pidfd)
	service, err := callerService(ctx, p)
	if err != nil || !readablePolicy(service) {
		return ErrRefused
	}
	deadline, ok := ctx.Deadline()
	if !ok || time.Until(deadline) > WorkerLifetime {
		return ErrRefused
	}
	_ = c.SetDeadline(deadline)
	var hello [4]byte
	if readAuthenticated(c, hello[:], &p) != nil || string(hello[:]) != "OPM1" {
		return ErrRefused
	}
	ready := byte(1)
	if service == AccountService {
		ready = 2
	}
	if _, err := c.Write([]byte{ready}); err != nil {
		return ErrRefused
	}
	var header [4]byte
	if readAuthenticated(c, header[:], &p) != nil {
		return ErrRefused
	}
	loginSize, passwordSize := int(binary.BigEndian.Uint16(header[:2])), int(binary.BigEndian.Uint16(header[2:]))
	if loginSize == 0 || loginSize > MaxLogin || passwordSize == 0 || passwordSize > MaxPassword {
		return ErrRefused
	}
	loginBytes, password := make([]byte, loginSize), make([]byte, passwordSize)
	defer clear(password)
	if readAuthenticated(c, loginBytes, &p) != nil || readAuthenticated(c, password, &p) != nil {
		return ErrRefused
	}
	login := string(loginBytes)
	if !ValidRequest(login, password) {
		return ErrRefused
	}
	uid, err := LocalAccount(login)
	if err != nil || (service == AccountService && uid == 0) {
		return ErrRefused
	}
	if service == RepairService {
		groups, err := LocalGroups(login)
		if err != nil || !slices.Contains(groups, AdministratorsGroup) {
			return ErrRefused
		}
	}
	ack := make(chan error, 1)
	go func() {
		var value [1]byte
		err := readAuthenticated(c, value[:], &p)
		if err != nil || value[0] != 1 {
			cancel()
			ack <- ErrRefused
			return
		}
		ack <- nil
	}()
	// The executable's context watcher also observes a disconnected caller
	// through cancel while this native function is blocked.
	stop := context.AfterFunc(ctx, func() { _ = c.Close() })
	defer stop()
	result, err := authenticate(service, login, password)
	clear(password)
	if err != nil || ctx.Err() != nil {
		return ErrRefused
	}
	after, err := LocalAccount(login)
	currentService, peerErr := callerService(ctx, p)
	if err != nil || after != uid || peerErr != nil || currentService != service {
		return ErrRefused
	}
	if service == RepairService {
		groups, err := LocalGroups(login)
		if err != nil || !slices.Contains(groups, AdministratorsGroup) {
			return ErrRefused
		}
	}
	var reply [5]byte
	if result.Authenticated {
		reply[0] = 1
	}
	if result.Authenticated && result.AccountAllowed {
		reply[0] |= 2
	}
	binary.BigEndian.PutUint32(reply[1:], uid)
	if _, err := c.Write(reply[:]); err != nil {
		return ErrRefused
	}
	select {
	case err := <-ack:
		return err
	case <-ctx.Done():
		return ErrRefused
	}
}
