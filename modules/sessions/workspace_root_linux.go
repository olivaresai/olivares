// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build linux

package sessions

import (
	"errors"

	"golang.org/x/sys/unix"
)

// syncRoot reads the root's attributes from the file server (AT_STATX_FORCE_SYNC). A plain
// stat of an NFS mount answers from the client's cache while the server is down. A kernel or
// sandbox without statx only loses that freshness: the stat that follows still reports the root.
func syncRoot(path string) error {
	var stx unix.Statx_t
	err := unix.Statx(unix.AT_FDCWD, path, unix.AT_STATX_FORCE_SYNC, unix.STATX_BASIC_STATS, &stx)
	if errors.Is(err, unix.ENOSYS) || errors.Is(err, unix.EPERM) || errors.Is(err, unix.EINVAL) {
		return nil
	}
	return err
}
