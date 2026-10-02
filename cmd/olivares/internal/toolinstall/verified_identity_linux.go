// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build linux

package toolinstall

import (
	"fmt"
	"os"
	"syscall"
)

// fileIdentity is what a write, a replacement or a mode change alters: device,
// inode, mode, size, mtime and ctime (nanoseconds). Any write to a file moves its
// ctime, and the ctime cannot be set from user space.
func fileIdentity(fi os.FileInfo) (string, bool) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return "", false
	}
	return fmt.Sprintf("%d:%d:%o:%d:%d.%09d:%d.%09d", st.Dev, st.Ino, st.Mode, st.Size,
		st.Mtim.Sec, st.Mtim.Nsec, st.Ctim.Sec, st.Ctim.Nsec), true
}
