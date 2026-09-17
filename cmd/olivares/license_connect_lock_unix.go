// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build unix

package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
)

// acquireConnectLease takes the connected client's exclusive lease on <connect-dir>/.lock. It
// follows the CR1 lease order (acquireCRLStoreLock): Lstat refuses a non-regular entry without
// opening it, O_NOFOLLOW|O_NONBLOCK open, fstat, non-blocking flock, and a final Lstat+SameFile.
// A held lease is errConnectBusy: two invocations on one data directory (a timer run and an
// operator) never interleave their persisted operations. The lock file is never unlinked.
func acquireConnectLease(connectDir string) (*os.File, error) {
	path := filepath.Join(connectDir, connectLockFileName)
	if info, err := os.Lstat(path); err == nil {
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("%w: %s is a %s", errConnectStateUnsafe, path, describeCRLLockType(info.Mode()))
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("inspect %s: %w", path, err)
	}
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0o600)
	if err != nil {
		if errors.Is(err, syscall.ELOOP) || errors.Is(err, syscall.EMLINK) {
			return nil, fmt.Errorf("%w: %s is a symbolic link", errConnectStateUnsafe, path)
		}
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	fail := func(cause error) (*os.File, error) {
		if cerr := f.Close(); cerr != nil {
			return nil, errors.Join(cause, fmt.Errorf("close %s: %w", path, cerr))
		}
		return nil, cause
	}
	held, err := f.Stat()
	if err != nil {
		return fail(fmt.Errorf("fstat %s: %w", path, err))
	}
	if !held.Mode().IsRegular() {
		return fail(fmt.Errorf("%w: %s opened as a %s", errConnectStateUnsafe, path, describeCRLLockType(held.Mode())))
	}
	if err := flockExclusiveNonblocking(f); err != nil {
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return fail(fmt.Errorf("%w: %s", errConnectBusy, path))
		}
		return fail(fmt.Errorf("flock %s: %w", path, err))
	}
	named, err := os.Lstat(path)
	if err != nil || !named.Mode().IsRegular() || !os.SameFile(held, named) {
		return fail(fmt.Errorf("%w: %s was replaced while it was being locked", errConnectStateUnsafe, path))
	}
	return f, nil
}
