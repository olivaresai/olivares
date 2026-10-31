// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func assertLicenseNotification(t *testing.T, dir string, previous os.FileInfo) os.FileInfo {
	t.Helper()
	path := filepath.Join(dir, "public", "license.changed")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("licence change did not publish its notification: %v", err)
	}
	if info.Size() != 0 {
		t.Fatal("notification must contain no licence material")
	}
	if previous != nil && os.SameFile(previous, info) {
		t.Fatal("licence change did not replace the notification")
	}
	return info
}

func TestLicenseNotificationsFromConsoleAndCLI(t *testing.T) {
	t.Setenv("OLIVARES_LICENSE", "")
	t.Setenv("OLIVARES_LICENSE_PATH", "")
	svc, _, _ := newTestLicenseService(t)
	blob := signTestLicence(t, "Notifications", time.Now().Add(24*time.Hour))
	var previous os.FileInfo
	for i := 0; i < 2; i++ {
		if _, err := svc.InstallLicense(context.Background(), blob, false); err != nil {
			t.Fatal(err)
		}
		previous = assertLicenseNotification(t, svc.dataDir, previous)
	}
	if _, err := svc.UninstallLicense(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	previous = assertLicenseNotification(t, svc.dataDir, previous)
	for i := 0; i < 2; i++ {
		if out, err := installLicence(t, blob, "--data-dir", svc.dataDir); err != nil {
			t.Fatalf("%v: %s", err, out)
		}
		previous = assertLicenseNotification(t, svc.dataDir, previous)
	}
	root := newRootCmd()
	root.SetArgs([]string{"license", "uninstall", "--yes", "--data-dir", svc.dataDir})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	assertLicenseNotification(t, svc.dataDir, previous)
}

func TestLicenseNotificationDoesNotFollowExistingMarker(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "public"), 0o755); err != nil {
		t.Fatal(err)
	}
	private := filepath.Join(dir, "private.key")
	if err := os.WriteFile(private, []byte("private"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(private, filepath.Join(dir, "public", "license.changed")); err != nil {
		t.Fatal(err)
	}
	if err := notifyLicenseChanged(dir); err != nil {
		t.Fatal(err)
	}
	if raw, err := os.ReadFile(private); err != nil || string(raw) != "private" {
		t.Fatal("notification modified a symlink target")
	}
	assertLicenseNotification(t, dir, nil)
}

func TestLicenseNotificationRefusesUnsafePublicDirectory(t *testing.T) {
	for _, symlink := range []bool{false, true} {
		dir, target := t.TempDir(), t.TempDir()
		public := filepath.Join(dir, "public")
		if symlink {
			if err := os.Symlink(target, public); err != nil {
				t.Fatal(err)
			}
		} else {
			if err := os.Mkdir(public, 0o777); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(public, 0o777); err != nil {
				t.Fatal(err)
			}
		}
		if err := notifyLicenseChanged(dir); err == nil {
			t.Fatal("unsafe public directory accepted")
		}
		if entries, err := os.ReadDir(target); err != nil || len(entries) != 0 {
			t.Fatal("notification followed a symlink")
		}
	}
}
