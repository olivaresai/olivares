// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build linux || darwin

package tui

import (
	"errors"

	"golang.org/x/sys/unix"
)

// inputPending reports whether fd has a byte ready within ms milliseconds. It
// is how a lone Esc is told apart from the first byte of an arrow key.
func inputPending(fd int, ms int) bool {
	if fd < 0 {
		return false
	}
	fds := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
	for {
		n, err := unix.Poll(fds, ms)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		return err == nil && n > 0 && fds[0].Revents&unix.POLLIN != 0
	}
}
