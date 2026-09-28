// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build linux && (amd64 || arm64)

package localsession

import (
	"errors"
	"os/user"
	"strconv"
	"syscall"
	"unsafe"

	"github.com/olivaresai/olivares/appliance/layer/helpers/invocation"
)

// Linux reuses the helper's kernel-attested peer facts and holds the pidfd until
// the entire local connection closes.
type Linux struct{ invocation.Linux }

func (Linux) PassCred(fd int) (bool, error) {
	v, err := syscall.GetsockoptInt(fd, syscall.SOL_SOCKET, syscall.SO_PASSCRED)
	return v == 1, err
}
func (Linux) PeerPidfd(fd int) (int, error) {
	pidfd, err := (invocation.Linux{}).PeerPidfd(fd)
	if err == nil {
		syscall.CloseOnExec(pidfd)
	}
	return pidfd, err
}
func (Linux) Admin(uid uint32) (bool, error) {
	group, err := user.LookupGroup("olivares-admins")
	if err != nil {
		return false, err
	}
	account, err := user.LookupId(strconv.FormatUint(uint64(uid), 10))
	if err != nil {
		return false, err
	}
	groups, err := account.GroupIds()
	if err != nil {
		return false, err
	}
	for _, gid := range groups {
		if gid == group.Gid {
			return true, nil
		}
	}
	return false, nil
}

// Alive polls the held pidfd without waiting. A readable pidfd includes an
// exited process that still has a numeric PID (for example an unreaped child).
func (Linux) Alive(pidfd int) error {
	descriptor := struct {
		FD      int32
		Events  int16
		Revents int16
	}{FD: int32(pidfd), Events: 1}
	timeout := syscall.Timespec{}
	n, _, errno := syscall.Syscall6(syscall.SYS_PPOLL, uintptr(unsafe.Pointer(&descriptor)), 1, uintptr(unsafe.Pointer(&timeout)), 0, 0, 0)
	if errno != 0 {
		return errno
	}
	if n != 0 || descriptor.Revents != 0 {
		return errors.New("owner_exited")
	}
	return nil
}
func (Linux) Unread(fd int) (int, error) {
	var n int32
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), syscall.TIOCINQ, uintptr(unsafe.Pointer(&n)))
	if errno != 0 {
		return 0, errno
	}
	return int(n), nil
}
