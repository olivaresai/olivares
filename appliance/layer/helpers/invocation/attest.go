// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// Package invocation is the kernel-facing half of the appliance's privileged-helper seam.
//
// Each helper is started by a socket unit with Accept=yes, one instance per connection, with
// the connection as its standard input and output. The helper learns who is asking from the
// connection itself and from nothing the caller wrote: the uid of the socket's peer credentials
// (SO_PEERCRED) and its account, the process the peer's pidfd names (SO_PEERPIDFD), the unit
// whose cgroup holds that process and the process's controlling terminal. When the kernel
// offers no pidfd, the connection's process is not identified and every mutating subcommand is
// refused: a pid read afterwards could name another process. Admission itself is
// helperschema.Admit, a pure decision over what this package attests.
package invocation

import (
	"errors"
	"strconv"
	"strings"

	"github.com/olivaresai/olivares/appliance/layer/helpers/helperschema"
)

// Kernel is what the seam asks the kernel about one connection. The Linux implementation reads
// socket options, procfs and the pidfd; tests describe a kernel without privilege.
type Kernel interface {
	// PeerCred returns the peer's pid and uid from SO_PEERCRED.
	PeerCred(fd int) (pid int, uid uint32, err error)
	// PeerPidfd returns a pidfd for the peer's process from SO_PEERPIDFD, or an error when
	// the kernel does not offer one.
	PeerPidfd(fd int) (int, error)
	// PidOf returns the pid the pidfd names now, or an error when its process has exited.
	PidOf(pidfd int) (int, error)
	// Cgroup returns the cgroup membership of pid, as /proc/<pid>/cgroup states it.
	Cgroup(pid int) (string, error)
	// Stat returns the status line of pid, as /proc/<pid>/stat states it.
	Stat(pid int) (string, error)
	// AccountOf returns the account name the system's user database gives uid.
	AccountOf(uid uint32) (string, error)
	// Alive returns nil while the pidfd's process has not exited.
	Alive(pidfd int) error
	// Close closes a pidfd.
	Close(fd int)
}

// ErrNoPeer reports a standard input that is not a connected socket: the helper was not
// started by its socket unit.
var ErrNoPeer = errors.New("standard input is not a connection: the helper is started by its socket unit")

// Attest returns the peer of the connection fd. It is Attested only when the kernel gave a
// pidfd, the pidfd names the process SO_PEERCRED names, that process's cgroup names one system
// unit, its status line names its controlling terminal, and the process was still alive after
// both were read, so the unit and the terminal are that process's and not a successor's.
// Without a pidfd, Unit and TTY stay empty and Attested false.
func Attest(k Kernel, fd int) (helperschema.Peer, error) {
	pid, uid, err := k.PeerCred(fd)
	if err != nil {
		return helperschema.Peer{}, ErrNoPeer
	}
	peer := helperschema.Peer{UID: uid}
	if account, err := k.AccountOf(uid); err == nil {
		peer.Account = account
	}
	pidfd, err := k.PeerPidfd(fd)
	if err != nil {
		return peer, nil
	}
	defer k.Close(pidfd)
	named, err := k.PidOf(pidfd)
	if err != nil || named != pid || named <= 0 {
		return peer, nil
	}
	cgroup, err := k.Cgroup(named)
	if err != nil {
		return peer, nil
	}
	stat, err := k.Stat(named)
	if err != nil {
		return peer, nil
	}
	unit, ok := unitOf(cgroup)
	if !ok {
		return peer, nil
	}
	tty, ok := ttyOf(stat)
	if !ok || k.Alive(pidfd) != nil {
		return peer, nil
	}
	peer.Unit, peer.TTY, peer.Attested = unit, tty, true
	return peer, nil
}

// ttyOf returns the controlling terminal a /proc/<pid>/stat line names: "/dev/ttyN" for a
// virtual console (major 4, minor 1 to 63), "" for none, and "device <major>:<minor>" for any
// other terminal, which no invoker names. The command name in parentheses may hold any byte, so
// the fields are read after its last closing parenthesis.
func ttyOf(stat string) (string, bool) {
	cut := strings.LastIndexByte(stat, ')')
	if cut < 0 {
		return "", false
	}
	fields := strings.Fields(stat[cut+1:])
	if len(fields) < 5 {
		return "", false
	}
	nr, err := strconv.ParseInt(fields[4], 10, 64)
	if err != nil || nr < 0 || nr > 1<<32-1 {
		return "", false
	}
	major := (nr >> 8) & 0xfff
	minor := (nr & 0xff) | ((nr >> 12) & 0xfff00)
	switch {
	case nr == 0:
		return "", true
	case major == 4 && minor >= 1 && minor <= 63:
		return "/dev/tty" + strconv.FormatInt(minor, 10), true
	}
	return "device " + strconv.FormatInt(major, 10) + ":" + strconv.FormatInt(minor, 10), true
}

// unitOf returns the system service whose cgroup a process is in, from its cgroup v2
// membership: exactly one line, "0::/system.slice/<name>.service". A nested cgroup, a cgroup v1
// hierarchy or any other slice names no unit here.
func unitOf(cgroup string) (string, bool) {
	line := strings.TrimSuffix(cgroup, "\n")
	if strings.Contains(line, "\n") {
		return "", false
	}
	name, ok := strings.CutPrefix(line, "0::/system.slice/")
	if !ok || !strings.HasSuffix(name, ".service") || len(name) > 255 {
		return "", false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case c == '-' || c == '_' || c == '.' || c == '@' || c == ':' || c == '\\':
		default:
			return "", false
		}
	}
	return name, true
}
