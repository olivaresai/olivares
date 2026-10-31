//go:build !linux

// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import "errors"

func pinDRBackupDirectory(path string, mounted bool) (drPinnedDirectory, error) {
	if mounted {
		return drPinnedDirectory{}, errors.New("managed backup disks require Linux")
	}
	return drPinnedDirectory{Path: path, Target: path}, nil
}
