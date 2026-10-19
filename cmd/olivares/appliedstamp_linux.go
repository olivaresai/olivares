// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build linux

package main

import (
	"fmt"
	"os"
	"syscall"
)

// fileIdentity names a file as the filesystem knows it: device, inode, size, mtime and ctime. Any
// write to the file, any replacement of it and any chmod changes it, and the ctime is not one a
// process can set back (size and mtime are: `cp -p` and `touch -r` do), so an identity that still
// matches is the file that was identified. It trusts the filesystem's attributes: a network
// filesystem that serves stale ones weakens it. "" means the file cannot be identified.
func fileIdentity(fi os.FileInfo) string {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return ""
	}
	return fmt.Sprintf("%d:%d:%d:%d.%d:%d.%d", st.Dev, st.Ino, st.Size, st.Mtim.Sec, st.Mtim.Nsec, st.Ctim.Sec, st.Ctim.Nsec)
}

// ownedPrivately reports a file owned by this process's user that no one else can read or write.
func ownedPrivately(fi os.FileInfo) bool {
	st, ok := fi.Sys().(*syscall.Stat_t)
	return ok && int(st.Uid) == os.Geteuid() && fi.Mode().Perm()&0o077 == 0
}
