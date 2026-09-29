// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build linux && !386

package portal

import (
	"bytes"
	"syscall"
	"unsafe"
)

// socketDevice reads the socket's SO_BINDTODEVICE option: the interface the kernel
// restricts the socket to, or "" when it is bound to none or the option cannot be read.
// The kernel names the interface in the socket's own network namespace, so the answer
// holds although the service runs in a private one. linux/386 reaches socket options
// through socketcall and is served by device_other.go.
func socketDevice(raw syscall.RawConn) string {
	var name [syscall.IFNAMSIZ]byte
	size := uint32(len(name))
	var errno syscall.Errno
	err := raw.Control(func(fd uintptr) {
		_, _, errno = syscall.Syscall6(syscall.SYS_GETSOCKOPT, fd, syscall.SOL_SOCKET, syscall.SO_BINDTODEVICE,
			uintptr(unsafe.Pointer(&name[0])), uintptr(unsafe.Pointer(&size)), 0)
	})
	if err != nil || errno != 0 || size > uint32(len(name)) {
		return ""
	}
	device, _, _ := bytes.Cut(name[:size], []byte{0})
	return string(device)
}
