// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"fmt"
	"os"
	"path/filepath"
)

// notifyLicenseChanged publishes no licence material. The appliance watches this
// public inode because PID 1 must not read the private engine files to arm inotify.
// Replace it atomically so every successful change generates an event, even when
// the previous notification is empty too. The public directory's SELinux label
// is inherited by the staging file, including on first activation.
func notifyLicenseChanged(dataDir string) error {
	dir := filepath.Join(dataDir, "public")
	if err := os.Mkdir(dir, 0o755); err != nil && !os.IsExist(err) {
		return fmt.Errorf("licence changed but notification directory failed: %w", err)
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return fmt.Errorf("licence changed but notification directory failed: %w", err)
	}
	if !info.IsDir() || info.Mode().Perm()&0o022 != 0 {
		return fmt.Errorf("licence changed but public notification directory is unsafe")
	}
	if err := writeKeyFile(filepath.Join(dir, "license.changed"), nil, 0o600, true, "licence notification"); err != nil {
		return fmt.Errorf("licence changed but notification failed: %w", err)
	}
	return nil
}
