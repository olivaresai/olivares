// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build unix

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// lockPolicyOut takes an exclusive flock on .<name>.olivares-lock beside out and waits for it,
// so two pulls of one managed file run their read, check and write one after the other. flock
// belongs to the open file description, so it also excludes a second descriptor in this
// process, and the kernel drops it when the holder dies. As upgradelock_unix.go explains, the
// lock file is never removed: unlinking it would let a waiter and a newcomer lock different
// inodes. The final component is never followed and must be a regular file, so a link or FIFO
// planted there is neither written through nor waited on.
func lockPolicyOut(out string) (func(), error) {
	dir := filepath.Dir(out)
	// A fresh host has no /etc/opencode or /etc/codex yet.
	if err := os.MkdirAll(dir, 0o755); err != nil { //nolint:gosec // managed policy directories are world-readable by design
		return nil, err
	}
	path := filepath.Join(dir, "."+filepath.Base(out)+".olivares-lock")
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0o600) //nolint:gosec // beside the path the operator named
	if err != nil {
		return nil, fmt.Errorf("open the pull lock %s: %w", path, err)
	}
	if info, err := f.Stat(); err != nil || !info.Mode().IsRegular() {
		_ = f.Close()
		return nil, fmt.Errorf("the pull lock %s is not a regular file", path)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("lock %s: %w", path, err)
	}
	return func() { _ = f.Close() }, nil
}
