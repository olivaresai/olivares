// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package helperclient

import (
	"os"
	"syscall"
)

// ownedBy reports whether uid owns the file info describes.
func ownedBy(info os.FileInfo, uid uint32) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Uid == uid
}
