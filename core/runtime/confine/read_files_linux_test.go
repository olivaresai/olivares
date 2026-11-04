// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build linux

package confine

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// A file the caller checked and then named in ReadFiles is judged again when the grant is made: a same-user
// process that swaps it between the two steps (for a link, a hard link, a folder, or a link in the folder it
// lies in) must not widen what the child reads. The file the caller checked is still granted.
func TestReadFilesAreCheckedAgainWhenGranted(t *testing.T) {
	state := Probe()
	if state.Mode != ModeLandlock {
		t.Fatal(state.Reason)
	}
	const secret = "secret-fixture"
	for _, tc := range []struct {
		name    string
		swap    func(t *testing.T, root, file string) (read string)
		refused bool
	}{
		{name: "the checked file is granted", swap: func(*testing.T, string, string) string { return "" }},
		{name: "a link in place of the file", refused: true, swap: func(t *testing.T, root, file string) string {
			must(t, os.Remove(file))
			must(t, os.Symlink(filepath.Join(root, "secret"), file))
			return file
		}},
		{name: "a link in place of the folder it lies in", refused: true, swap: func(t *testing.T, root, file string) string {
			dir := filepath.Dir(file)
			other := filepath.Join(root, "other")
			must(t, os.Mkdir(other, 0o700))
			must(t, os.WriteFile(filepath.Join(other, filepath.Base(file)), []byte(secret), 0o600))
			must(t, os.Rename(dir, dir+".checked"))
			must(t, os.Symlink(other, dir))
			return file
		}},
		{name: "a second name for another file", refused: true, swap: func(t *testing.T, root, file string) string {
			must(t, os.Remove(file))
			must(t, os.Link(filepath.Join(root, "secret"), file))
			return file
		}},
		{name: "a folder in place of the file", refused: true, swap: func(t *testing.T, root, file string) string {
			must(t, os.Remove(file))
			must(t, os.Mkdir(file, 0o700))
			must(t, os.WriteFile(filepath.Join(file, "inner"), []byte(secret), 0o600))
			return filepath.Join(file, "inner")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			must(t, os.WriteFile(filepath.Join(root, "secret"), []byte(secret), 0o600))
			dir := filepath.Join(root, "broker")
			must(t, os.Mkdir(dir, 0o700))
			file := filepath.Join(dir, "service")
			must(t, os.WriteFile(file, []byte("service-fixture"), 0o600))
			policy := Policy{
				ReadOnly:  []string{"/bin", "/lib", "/lib64"},
				ReadFiles: []string{file},
				Devices:   []Device{{Path: "/dev/null", Write: true}},
			}
			read := tc.swap(t, root, file)
			if read == "" {
				read = file
			}
			cmd := exec.CommandContext(t.Context(), "/bin/cat", read)
			if _, err := Wrap(cmd, policy); err != nil {
				t.Fatal(err)
			}
			out, err := cmd.CombinedOutput()
			if strings.Contains(string(out), secret) {
				t.Fatalf("the child read what the swapped name leads to: %q", out)
			}
			if !tc.refused {
				if err != nil || string(out) != "service-fixture" {
					t.Fatalf("the checked file was not granted: %v: %q", err, out)
				}
				return
			}
			if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 126 {
				t.Fatalf("the swapped file did not refuse the launch: %v: %q", err, out)
			}
		})
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
