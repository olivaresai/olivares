// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build unix

package sessions

import (
	"io/fs"
	"os"
	"syscall"
)

// openStateFile opens a conversation file for reading without following a link at
// its last element and without blocking on a pipe or device swapped in for it: a
// file the engine meant to read a few bytes of must never hold the run's operation
// lock open. os.Root already keeps the path inside the home; this keeps the final
// element from being a link inside it.
func openStateFile(root *os.Root, rel string) (*os.File, error) {
	return root.OpenFile(rel, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
}

// singleLink reports whether the file has exactly one name. A second name is how a
// login file could be given a conversation-shaped one inside the tree, and os.Root
// does not see it: the planner and the opened descriptor both refuse it.
func singleLink(fi fs.FileInfo) bool {
	st, ok := fi.Sys().(*syscall.Stat_t)
	return !ok || uint64(st.Nlink) == 1
}
