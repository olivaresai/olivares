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

// acquireCRLStoreLock takes the data directory's CRL transaction lease (CR1 §3). It never
// waits: a held lease is errCRLStoreBusy, and a lock path that is not a regular file is
// refused BEFORE it is opened, so a FIFO, socket, device, directory or symlink planted there
// is neither opened nor followed.
//
// The order is the contract, and each step closes a gap the previous one leaves:
//
//  1. Lstat: an existing non-regular entry is refused without opening it.
//  2. open O_RDWR|O_CREAT|O_NOFOLLOW|O_NONBLOCK, 0600 (Go adds O_CLOEXEC): the path may have
//     changed since step 1, so the final component is still never followed and a special
//     file swapped in is not waited on. No O_TRUNC — the lock's bytes are nobody's business.
//  3. fstat the descriptor: what was opened must itself be a regular file.
//  4. flock LOCK_EX|LOCK_NB on that descriptor. flock belongs to the open file description,
//     so it excludes other processes AND a second descriptor in this one, and the kernel
//     drops it when the holder dies for any reason.
//  5. Lstat again and require os.SameFile with the held descriptor: a lease on an inode the
//     path no longer names would split the domain.
//
// Every failure after step 2 closes the descriptor exactly once. The lock file is never
// unlinked, for the reason upgradelock_unix.go gives: replacing its inode splits exclusion.
// This fences cooperating recorders in an owner-only data directory; it is not a defence
// against a same-UID process that replaces paths while ignoring the lock.
func acquireCRLStoreLock(dataDir string, ops crlPersistOps) (*crlStoreLease, error) {
	path := filepath.Join(dataDir, crlLockFileName)
	if info, err := os.Lstat(path); err == nil {
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("%w: %s is a %s, refused without opening it", errCRLLockUnsafe, path, describeCRLLockType(info.Mode()))
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("%w: inspect %s: %w", errCRLLockUnavailable, path, err)
	}

	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0o600)
	if err != nil {
		if errors.Is(err, syscall.ELOOP) || errors.Is(err, syscall.EMLINK) {
			return nil, fmt.Errorf("%w: %s is a symbolic link, refused without following it", errCRLLockUnsafe, path)
		}
		return nil, fmt.Errorf("%w: open %s: %w", errCRLLockUnavailable, path, err)
	}
	fail := func(cause error) (*crlStoreLease, error) {
		if cerr := ops.closeLock(f); cerr != nil {
			return nil, errors.Join(cause, fmt.Errorf("close %s: %w", path, cerr))
		}
		return nil, cause
	}

	held, err := f.Stat()
	if err != nil {
		return fail(fmt.Errorf("%w: fstat %s: %w", errCRLLockUnavailable, path, err))
	}
	if !held.Mode().IsRegular() {
		return fail(fmt.Errorf("%w: %s opened as a %s", errCRLLockUnsafe, path, describeCRLLockType(held.Mode())))
	}
	if err := flockExclusiveNonblocking(f); err != nil {
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return fail(fmt.Errorf("%w: %s is held by another recorder; this observation was not recorded and a later run records it", errCRLStoreBusy, path))
		}
		return fail(fmt.Errorf("%w: flock %s: %w", errCRLLockUnavailable, path, err))
	}
	named, err := os.Lstat(path)
	if err != nil {
		return fail(fmt.Errorf("%w: re-inspect %s after locking it: %w", errCRLLockUnavailable, path, err))
	}
	if !named.Mode().IsRegular() || !os.SameFile(held, named) {
		return fail(fmt.Errorf("%w: %s was replaced while it was being locked", errCRLLockUnsafe, path))
	}
	return &crlStoreLease{f: f, path: path}, nil
}

func flockExclusiveNonblocking(f *os.File) error {
	rc, err := f.SyscallConn()
	if err != nil {
		return err
	}
	var ferr error
	if err := rc.Control(func(fd uintptr) {
		ferr = syscall.Flock(int(fd), syscall.LOCK_EX|syscall.LOCK_NB)
	}); err != nil {
		return err
	}
	return ferr
}

func describeCRLLockType(m fs.FileMode) string {
	switch {
	case m&fs.ModeSymlink != 0:
		return "symbolic link"
	case m.IsDir():
		return "directory"
	case m&fs.ModeNamedPipe != 0:
		return "FIFO"
	case m&fs.ModeSocket != 0:
		return "socket"
	case m&fs.ModeDevice != 0:
		return "device"
	default:
		return "non-regular file (" + m.Type().String() + ")"
	}
}
