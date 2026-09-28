// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// Package localsession admits an OS peer and serves read-only local requests.
// OS admission never supplies product or repair authorization.
package localsession

import (
	"errors"
	"strings"
	"sync"
)

// Kernel is the narrow process, account and socket observation seam. Production
// uses Linux; tests replace observations without granting production authority.
type Kernel interface {
	PeerCred(fd int) (int, uint32, error)
	PeerPidfd(fd int) (int, error)
	PidOf(pidfd int) (int, error)
	Cgroup(pid int) (string, error)
	Alive(pidfd int) error
	Close(fd int)
	PassCred(fd int) (bool, error)
	Admin(uid uint32) (bool, error)
	Unread(fd int) (int, error)
}

type owner struct {
	pid    int
	uid    uint32
	pidfd  int
	cgroup string
	kernel Kernel
	once   sync.Once
}

func (o *owner) Close()       { o.once.Do(func() { o.kernel.Close(o.pidfd) }) }
func (o *owner) Alive() error { return o.kernel.Alive(o.pidfd) }
func (o *owner) Check() error {
	if o.uid != 0 {
		ok, err := o.kernel.Admin(o.uid)
		if err != nil || !ok {
			return errors.New("local_peer_refused")
		}
	}
	cgroup, err := o.kernel.Cgroup(o.pid)
	if err != nil || cgroup != o.cgroup {
		return errors.New("context_unverifiable")
	}
	return o.Alive()
}

func admit(fd int, k Kernel) (*owner, string) {
	if k == nil {
		return nil, "local_peer_refused"
	}
	pass, err := k.PassCred(fd)
	if err != nil || !pass {
		return nil, "passcred_unset"
	}
	pid, uid, err := k.PeerCred(fd)
	if err != nil || pid <= 0 {
		return nil, "local_peer_refused"
	}
	if uid != 0 {
		ok, err := k.Admin(uid)
		if err != nil || !ok {
			return nil, "local_peer_refused"
		}
	}
	pidfd, err := k.PeerPidfd(fd)
	if err != nil {
		return nil, "pidfd_unproven"
	}
	o := &owner{pid: pid, uid: uid, pidfd: pidfd, kernel: k}
	actual, err := k.PidOf(pidfd)
	if err != nil || actual != pid {
		o.Close()
		return nil, "pidfd_unproven"
	}
	// Read process facts only after holding its kernel-created pidfd.
	cgroup, err := k.Cgroup(pid)
	line := strings.TrimSuffix(cgroup, "\n")
	if err != nil || !strings.HasPrefix(line, "0::/") || strings.ContainsAny(line, "\n\r\x00") || k.Alive(pidfd) != nil {
		o.Close()
		return nil, "pidfd_unproven"
	}
	o.cgroup = cgroup
	return o, ""
}
