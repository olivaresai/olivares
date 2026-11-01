// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build unix

package confine

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

// lockChildTemp opens path (never through a symbolic link) and takes an exclusive flock on it without waiting. The
// returned file holds the lock until the process ends; flock rather than a sentinel file, so a dead engine's lock
// is gone with it. When another engine holds it, the error is ErrChildTempHeld and the file is returned still open,
// so the caller can wait for the lock on it.
func lockChildTemp(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open the child temporary root's lock: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return f, ErrChildTempHeld
		}
		_ = f.Close()
		return nil, fmt.Errorf("lock the child temporary root: %w", err)
	}
	return f, nil
}

// waitChildTemp blocks until this process holds f's exclusive flock.
func waitChildTemp(f *os.File) error {
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX)
		if !errors.Is(err, syscall.EINTR) {
			return err
		}
	}
}
