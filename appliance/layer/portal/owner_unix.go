// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package portal

import (
	"os"
	"syscall"
)

// fileOwner reports the uid that owns the file info describes, and false when info
// records none. Tests replace it to describe another owner without root.
var fileOwner = func(info os.FileInfo) (uint32, bool) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return stat.Uid, true
}
