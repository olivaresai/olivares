// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build unix

package opgate

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

// flockTry takes the anchor's lock without blocking.
//
// flock rather than an O_EXCL sentinel, for the reason cmd/olivares' upgrade lock
// gives: a lock is only as good as its release, and the kernel drops an flock when
// the holder dies for ANY reason — SIGKILL, an OOM kill, a power loss. A sentinel
// file would need this package to decide whether a lock whose owner is gone is
// stale, and a stale-lock heuristic on a DR fence is a way to publish a store over
// a restore that is still running.
//
// ok=false is EWOULDBLOCK and nothing else: every other errno is returned, because
// "I could not take the lock" and "somebody else holds it" are different answers
// and only the second one is a diagnosis.
func flockTry(f *os.File, exclusive bool) (bool, error) {
	how := syscall.LOCK_SH
	if exclusive {
		how = syscall.LOCK_EX
	}
	err := syscall.Flock(int(f.Fd()), how|syscall.LOCK_NB)
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, syscall.EWOULDBLOCK):
		return false, nil
	default:
		return false, fmt.Errorf("opgate: take the restore control lock %s: %w", f.Name(), err)
	}
}

// openNoFollow makes the lock open refuse a symbolic link in the kernel rather than
// in the check above it. The Lstat check is still there and still runs first: it
// gives the precise ErrSymlink diagnosis, while this flag closes the window between
// that check and the open.
const openNoFollow = syscall.O_NOFOLLOW

// openNonBlock keeps the lock open BOUNDED whatever is at the path.
//
// open(O_RDONLY) on a FIFO blocks until a writer appears, and on some character
// devices it waits on the device itself. The lock open runs while acquireOne holds
// the process registry mutex, so that wait is not one caller's problem: it is every
// caller's. O_NONBLOCK makes those opens return immediately, and what they return
// is then refused by the regular-file check rather than locked.
//
// It is not a substitute for the Lstat check above it — that one gives the precise
// diagnosis — but it is what closes the window if the path is replaced in between.
// On a regular file the flag has no effect at all, and flock() does not consult it.
const openNonBlock = syscall.O_NONBLOCK
