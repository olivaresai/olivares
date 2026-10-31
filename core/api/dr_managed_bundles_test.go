// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A data-volume reset that keeps fstab leaves the schedule row absent while the
// selected disk still holds bundles. A new key would make them unrecoverable.
func TestApplianceRecoveryKeyIsNotRegeneratedWhileAManagedDiskHoldsBundles(t *testing.T) {
	root := t.TempDir()
	disk := filepath.Join(root, "managed", "617359a8-818f-4d2d-a901-cc4b7b2911a1")
	if err := os.MkdirAll(disk, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(disk, "nightly.drbundle"), []byte("bundle"), 0o600); err != nil {
		t.Fatal(err)
	}
	previous := drManagedBundleGlob
	drManagedBundleGlob = filepath.Join(root, "managed", "*", "*.drbundle")
	t.Cleanup(func() { drManagedBundleGlob = previous })

	data := filepath.Join(root, "data")
	if err := os.MkdirAll(data, 0o700); err != nil {
		t.Fatal(err)
	}
	s := &Server{drSvc: newDRService(DRConfig{DataDir: data, Appliance: true})}

	err := s.prepareApplianceRecoveryKey(true)
	if err == nil || !strings.Contains(err.Error(), "backups exist") {
		t.Fatalf("a key was created beside bundles on a managed disk: %v", err)
	}
	if _, statErr := os.Stat(s.drSvc.cfg.PassphraseFile); !os.IsNotExist(statErr) {
		t.Fatalf("recovery key was published: %v", statErr)
	}
}
