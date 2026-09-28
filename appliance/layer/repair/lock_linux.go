// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build linux

package repair

import (
	"context"
	"errors"
	"os"
	"syscall"
	"time"
)

// ofdGetLock is F_OFD_GETLK (Linux 3.15), the same number on every architecture. Unlike F_GETLK it
// reports a conflicting lock this very process holds through another open file description, and it
// reports the process-owned locks a package manager takes as well.
const ofdGetLock = 36

// lockHeld reports whether another holder has a lock on the regular file at path that a write lock
// would conflict with. It opens the file read-only, never creates it and takes no lock.
func lockHeld(path string) (bool, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return false, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return false, errors.New("not a lock file")
	}
	query := syscall.Flock_t{Type: syscall.F_WRLCK}
	if err := syscall.FcntlFlock(f.Fd(), ofdGetLock, &query); err != nil {
		return false, err
	}
	return query.Type != syscall.F_UNLCK, nil
}

// exclusiveWithin takes f's flock exclusively, retrying without blocking until wait has passed or
// ctx ends, so no admission waits on a holder for longer than that.
func exclusiveWithin(ctx context.Context, f *os.File, wait time.Duration) error {
	deadline := time.Now().Add(wait)
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) || time.Now().After(deadline) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(20 * time.Millisecond):
		}
	}
}

// unlock releases f's flock.
func unlock(f *os.File) error { return syscall.Flock(int(f.Fd()), syscall.LOCK_UN) }
