//go:build linux

// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import (
	"path/filepath"

	"golang.org/x/sys/unix"
)

func drTargetFreeBytes(path string) *uint64 {
	var stat unix.Statfs_t
	// A fresh install has not made its backups directory yet.
	if err := unix.Statfs(path, &stat); err != nil {
		if err != unix.ENOENT || unix.Statfs(filepath.Dir(path), &stat) != nil {
			return nil
		}
	}
	free := stat.Bavail * uint64(stat.Bsize)
	return &free
}
