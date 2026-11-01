// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build linux

package sessions

import (
	"errors"
	"fmt"
	"strings"

	"golang.org/x/sys/unix"
)

// selinuxFileType reads the type field of path's own label (security.selinux, not following a link).
var lgetxattr = unix.Lgetxattr

func selinuxFileType(path string) (string, error) {
	buf := make([]byte, 256)
	n, err := lgetxattr(path, "security.selinux", buf)
	if errors.Is(err, unix.ENODATA) || errors.Is(err, unix.ENOTSUP) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if n <= 0 || n > len(buf) {
		return "", fmt.Errorf("security.selinux on %s has invalid length %d", path, n)
	}
	fields := strings.SplitN(strings.TrimRight(string(buf[:n]), "\x00"), ":", 4)
	if len(fields) < 3 {
		return "", fmt.Errorf("security.selinux on %s has no type", path)
	}
	return fields[2], nil
}
