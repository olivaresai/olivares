// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package localinstall

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

// openRecord opens an install record without following a link at its name and
// without waiting on a FIFO put there: O_NONBLOCK returns at once for a FIFO, which
// Load then refuses as not a regular file.
func openRecord(name string) (*os.File, error) {
	f, err := os.OpenFile(name, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if errors.Is(err, syscall.ELOOP) || errors.Is(err, syscall.EMLINK) {
		return nil, fmt.Errorf("local install manifest must be a regular file, not a link: %s", name)
	}
	if err != nil {
		return nil, fmt.Errorf("read local install manifest: %w", err)
	}
	return f, nil
}

func validateManifestOwner(info os.FileInfo, mode string) error {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("cannot establish local install manifest owner")
	}
	want := uint32(os.Getuid())
	if mode == "system" {
		want = 0
	}
	if stat.Uid != want {
		return fmt.Errorf("local install manifest owner uid is %d, want %d for %s mode", stat.Uid, want, mode)
	}
	if info.Mode().Perm()&0o022 != 0 {
		return fmt.Errorf("local install manifest is writable by group or others (mode %04o)", info.Mode().Perm())
	}
	return nil
}
