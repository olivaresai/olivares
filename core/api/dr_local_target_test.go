//go:build linux

// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

const testBackupTarget = "/mnt/olivares-backups/617359a8-818f-4d2d-a901-cc4b7b2911a1"

func TestDRLocalTargetIsPersistedAndRestrictedToManagedDisks(t *testing.T) {
	for _, target := range []string{"/", "/var/lib/olivares", "/mnt/olivares-backups/../other", testBackupTarget + "/child", "/mnt/olivares-backups/not-a-uuid"} {
		if validDRLocalTarget(target) {
			t.Errorf("accepted %q", target)
		}
	}
	if !validDRLocalTarget(testBackupTarget) || !validDRLocalTarget("") {
		t.Fatal("managed target or default refused")
	}
	var stored persistedDRSchedule
	if err := json.Unmarshal([]byte(`{"local_target":"`+testBackupTarget+`"}`), &stored); err != nil {
		t.Fatal(err)
	}
	if stored.LocalTarget != testBackupTarget {
		t.Fatal("target not persisted", stored)
	}
}

func TestDRLocalTargetMustBeAnExt4MountNotTheRootFilesystem(t *testing.T) {
	mount := "31 25 8:16 / " + testBackupTarget + " rw,nosuid,nodev - ext4 /dev/sdb rw\n"
	if drLocalTargetMountID(testBackupTarget, []byte(mount)) != "31" {
		t.Fatal("managed ext4 mount refused")
	}
	for _, data := range []string{"", "25 1 8:1 / / rw - ext4 /dev/sda1 rw\n", "31 25 8:16 / " + testBackupTarget + " rw - btrfs /dev/sdb rw\n", "31 25 8:16 / " + testBackupTarget + " ro - ext4 /dev/sdb ro\n"} {
		if drLocalTargetMountID(testBackupTarget, []byte(data)) != "" {
			t.Fatalf("accepted missing/wrong/read-only disk: %q", data)
		}
	}
}

func TestDRLocalTargetUnavailableNeverFallsBackToTheSystemDisk(t *testing.T) {
	legacy := filepath.Join(t.TempDir(), "backups")
	svc := newDRService(DRConfig{BackupDir: legacy})
	svc.setSchedule(func(s *drSchedule) { s.LocalTarget = testBackupTarget })
	if err := svc.ensureBackupDir(); err == nil {
		t.Fatal("unmounted backup disk accepted")
	}
	if _, err := os.Stat(legacy); !os.IsNotExist(err) {
		t.Fatalf("wrote fallback directory: %v", err)
	}
	if svc.backupDir() != testBackupTarget {
		t.Fatal("selected target lost")
	}
}
