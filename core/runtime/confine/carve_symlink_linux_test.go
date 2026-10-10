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

func TestCarvingSkipsDanglingSymlinksWithoutWeakeningNamedGrants(t *testing.T) {
	state := Probe()
	if state.Mode != ModeLandlock || state.ABI < 3 {
		t.Fatal("this named native regression needs Landlock ABI 3 or later")
	}
	t.Logf("native Landlock ABI %d", state.ABI)
	t.Run("held directory path stays strict", func(t *testing.T) {
		link := filepath.Join(t.TempDir(), "held-link")
		if err := os.Symlink("missing-directory", link); err != nil {
			t.Fatal(err)
		}
		file, err := OpenDirectory(link)
		if file != nil {
			_ = file.Close()
			t.Fatal("dangling held directory returned a handle")
		}
		if err == nil {
			t.Fatal("dangling held directory was accepted")
		}
	})
	for _, tc := range []struct {
		name                      string
		held, named, loop, starts bool
	}{
		{name: "carved dangling without handles", starts: true},
		{name: "carved dangling with held handle", held: true, starts: true},
		{name: "ordinary named missing path skipped", named: true, starts: true},
		{name: "carved symlink loop still refused", loop: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			visible := filepath.Join(root, "visible")
			secret := filepath.Join(root, "protected-canary")
			missing := filepath.Join(root, "vconsole.conf")
			if err := os.WriteFile(visible, []byte("visible-fixture"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(secret, []byte("protected-fixture"), 0600); err != nil {
				t.Fatal(err)
			}
			target := "default/keyboard"
			if tc.loop {
				target = "vconsole.conf"
			}
			if err := os.Symlink(target, missing); err != nil {
				t.Fatal(err)
			}
			policy := Policy{ReadOnly: []string{"/bin", "/lib", "/lib64", root}, Protect: []string{secret}, Devices: []Device{{Path: "/dev/null", Write: true}}}
			if tc.named {
				policy.ReadOnly = []string{"/bin", "/lib", "/lib64", visible, missing}
			}
			if tc.held {
				held, err := OpenDirectory(t.TempDir())
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = held.Close() })
				policy.SealedFiles = []*os.File{held}
			}
			cmd := exec.CommandContext(t.Context(), "/bin/sh", "-c", `
/bin/cat "$1" || exit 2
if /bin/cat "$2" >/dev/null 2>&1; then echo protected-leak; exit 3; fi
if /bin/cat "$3" >/dev/null 2>&1; then echo missing-target-readable; exit 4; fi
printf '\nchild-ran'
`, "sh", visible, secret, missing)
			if _, err := Wrap(cmd, policy); err != nil {
				t.Fatal(err)
			}
			out, err := cmd.CombinedOutput()
			if tc.starts {
				if err != nil || string(out) != "visible-fixture\nchild-ran" {
					t.Fatalf("owned dangling carve prevented child/read/protected denial: %v: %s", err, out)
				}
			} else {
				exit, ok := err.(*exec.ExitError)
				if !ok || exit.ExitCode() != 126 || strings.Contains(string(out), "child-ran") || !strings.Contains(string(out), "without symlinks") {
					t.Fatalf("symlink-loop grant was not refused before execution: %v: %s", err, out)
				}
			}
			if content, err := os.ReadFile(secret); err != nil || string(content) != "protected-fixture" {
				t.Fatal("protected fixture changed")
			}
		})
	}
}
