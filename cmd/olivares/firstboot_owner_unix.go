// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build unix

package main

import (
	"fmt"
	"os"
	"syscall"
)

// fileOwner returns the uid and gid that own path, which must be a regular file: the
// engine account controls the data directory, and a symlink there must not choose the
// account root writes as.
func fileOwner(path string) (uid, gid int, known bool, err error) {
	info, err := os.Lstat(path)
	if err != nil {
		return 0, 0, false, err
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !info.Mode().IsRegular() || !ok {
		return 0, 0, false, fmt.Errorf("%s is not a regular file the engine wrote", path)
	}
	return int(st.Uid), int(st.Gid), true, nil
}
