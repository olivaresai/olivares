// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build linux

package confine

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

// The carve of a granted folder that holds a protected path grants its directories and regular files, a nested
// directory recursively when it holds the protected path, never a protected folder (/etc/olivares), and passes over the sockets and FIFOs it finds there
// without refusing the launch.
func TestCarvingGrantsFilesAndFoldersAndNoSpecialFiles(t *testing.T) {
	if state := Probe(); state.Mode != ModeLandlock {
		t.Fatal("this regression needs Landlock: " + state.Reason)
	}
	root := t.TempDir()
	nested := filepath.Join(root, "nested")
	paths := map[string]string{
		filepath.Join(root, "visible"):              "visible-fixture",
		filepath.Join(root, "folder", "inside"):     "folder-fixture",
		filepath.Join(nested, "beside"):             "nested-fixture",
		filepath.Join(nested, "protected-canary"):   "protected-fixture",
		filepath.Join(nested, "deeper", "far-file"): "deeper-fixture",
		filepath.Join(root, "config", "secret"):     "protected-folder-fixture",
	}
	for path, data := range paths {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := unix.Mkfifo(filepath.Join(root, "fifo"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := unix.Mknod(filepath.Join(root, "sock"), unix.S_IFSOCK|0o600, 0); err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(t.Context(), "/bin/sh", "-c", `
/bin/cat "$1/visible" "$1/folder/inside" "$1/nested/beside" "$1/nested/deeper/far-file" || exit 2
if /bin/cat "$1/nested/protected-canary" >/dev/null 2>&1; then echo protected-leak; exit 3; fi
if /bin/cat "$1/config/secret" >/dev/null 2>&1; then echo protected-folder-leak; exit 4; fi
printf '|child-ran'
`, "sh", root)
	policy := Policy{ReadOnly: []string{"/bin", "/lib", "/lib64", "/usr", root}, Protect: []string{filepath.Join(nested, "protected-canary"), filepath.Join(root, "config")},
		Devices: []Device{{Path: "/dev/null", Write: true}}}
	if _, err := Wrap(cmd, policy); err != nil {
		t.Fatal(err)
	}
	out, err := cmd.CombinedOutput()
	if want := "visible-fixturefolder-fixturenested-fixturedeeper-fixture|child-ran"; err != nil || string(out) != want {
		t.Fatalf("carve of a folder with sockets, a FIFO and a nested protected file: %v: %q, want %q", err, out, want)
	}
}
