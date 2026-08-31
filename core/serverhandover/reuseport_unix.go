// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build linux || darwin || freebsd

package serverhandover

import (
	"syscall"

	"golang.org/x/sys/unix"
)

const reuseSupported = true

// controlReusePort sets SO_REUSEPORT on the raw socket before bind, so multiple
// processes may listen on the same address for an overlapping handover. The
// constant is sourced from x/sys/unix (the std syscall package does not export
// SO_REUSEPORT on Linux), which carries the correct per-platform value.
func controlReusePort(_, _ string, c syscall.RawConn) error {
	var opErr error
	if err := c.Control(func(fd uintptr) {
		opErr = unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_REUSEPORT, 1)
	}); err != nil {
		return err
	}
	return opErr
}
