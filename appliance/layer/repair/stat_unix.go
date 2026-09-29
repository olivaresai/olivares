// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build unix

package repair

import (
	"os"
	"syscall"
)

// fileFacts returns a file's owner and link count as the kernel states them.
func fileFacts(info os.FileInfo) (uid uint32, links uint64, ok bool) {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, 0, false
	}
	return st.Uid, uint64(st.Nlink), true
}
