// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build unix

package toolinstall

import (
	"fmt"
	"os"
	"syscall"
)

// lockFile takes a non-blocking exclusive flock on an already opened file. The
// kernel releases it when the descriptor closes or the process dies, so there is
// no stale-lock heuristic to get wrong. The file is never unlinked (see LockFile).
func lockFile(f *os.File, what string) (func(), error) {
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		if err == syscall.EWOULDBLOCK {
			return nil, refuse(KindLocked, "another install holds %s; two concurrent installs are serialized so neither can observe the other's half-written staging (re-run when it finishes)", what)
		}
		return nil, fmt.Errorf("lock %s: %w", what, err)
	}
	_ = f.Truncate(0)
	_, _ = f.WriteAt([]byte(fmt.Sprintf("pid %d\n", os.Getpid())), 0)
	return func() { _ = f.Close() }, nil
}
