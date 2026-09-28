// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build !(aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris)

package localinstall

import (
	"fmt"
	"os"
)

// openRecord refuses a link or a non-regular file by name before it opens the
// record. This platform has no owner check for a live removal, so Load refuses
// every system record here anyway.
func openRecord(name string) (*os.File, error) {
	info, err := os.Lstat(name)
	if err != nil {
		return nil, fmt.Errorf("read local install manifest: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("local install manifest must be a regular file, not a link: %s", name)
	}
	f, err := os.Open(name)
	if err != nil {
		return nil, fmt.Errorf("read local install manifest: %w", err)
	}
	return f, nil
}

func validateManifestOwner(_ os.FileInfo, _ string) error {
	return fmt.Errorf("cannot establish local install manifest ownership on this platform")
}
