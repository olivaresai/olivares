// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build linux

package portal

import (
	"strings"
	"syscall"
	"unsafe"
)

// keyLabel reads path's security.selinux attribute with lgetxattr(2): it does not follow a
// final symbolic link and never opens the file, and the policy answers it for getattr alone.
// Tests replace it.
var keyLabel = func(path string) (string, error) {
	file, err := syscall.BytePtrFromString(path)
	if err != nil {
		return "", err
	}
	attribute, err := syscall.BytePtrFromString("security.selinux")
	if err != nil {
		return "", err
	}
	for size := 256; ; size *= 2 {
		buf := make([]byte, size)
		n, _, errno := syscall.Syscall6(syscall.SYS_LGETXATTR, uintptr(unsafe.Pointer(file)),
			uintptr(unsafe.Pointer(attribute)), uintptr(unsafe.Pointer(&buf[0])), uintptr(size), 0, 0)
		switch {
		case errno == syscall.ERANGE && size < 4096:
			continue
		case errno != 0:
			return "", errno
		}
		return strings.TrimRight(string(buf[:n]), "\x00"), nil
	}
}
