//go:build linux

// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestDRBackupDirectoryHandleCannotWriteIntoAReplacedMountpoint(t *testing.T) {
	base := t.TempDir()
	mountpoint := filepath.Join(base, "mounted")
	detached := filepath.Join(base, "detached")
	if err := os.Mkdir(mountpoint, 0700); err != nil {
		t.Fatal(err)
	}
	pin, err := pinDRBackupDirectory(mountpoint, false)
	if err != nil {
		t.Fatal(err)
	}
	defer pin.Close()
	if err := os.Rename(mountpoint, detached); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(mountpoint, 0700); err != nil {
		t.Fatal(err)
	}
	// A pg_dump child cannot inherit the close-on-exec handle, so the path
	// must refer to its parent's held mount rather than /proc/self/fd.
	cmd := exec.Command("/bin/sh", "-c", `printf 'backup data' > "$1/snapshot"`, "snapshot", pin.Path)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("snapshot child: %v: %s", err, output)
	}
	if _, err := os.Stat(filepath.Join(mountpoint, "snapshot")); !os.IsNotExist(err) {
		t.Fatalf("wrote the replacement directory: %v", err)
	}
	if _, err := os.Stat(filepath.Join(detached, "snapshot")); err != nil {
		t.Fatalf("lost the selected filesystem: %v", err)
	}
}
