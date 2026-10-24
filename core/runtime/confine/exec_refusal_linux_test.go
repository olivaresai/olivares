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

// A program the confined child may not execute is refused with its path and the step named, not a bare errno: the
// Server image showed only "could not be confined: permission denied" for a tool the helper could not execute.
func TestConfinedExecRefusalNamesTheProgram(t *testing.T) {
	if state := Probe(); state.Mode != ModeLandlock {
		t.Fatal("this regression needs Landlock: " + state.Reason)
	}
	root := t.TempDir()
	tool := filepath.Join(root, "tool")
	if err := os.WriteFile(tool, []byte("#!/bin/sh\nexit 0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(t.Context(), tool)
	if _, err := Wrap(cmd, Policy{ReadOnly: []string{"/bin", "/lib", "/lib64", "/usr", root}}); err != nil {
		t.Fatal(err)
	}
	out, err := cmd.CombinedOutput()
	exit, ok := err.(*exec.ExitError)
	if want := "execute " + tool + " in the confined domain: permission denied"; !ok || exit.ExitCode() != 126 || !strings.Contains(string(out), want) {
		t.Fatalf("a non-executable program: %v: %q, want exit 126 naming %q", err, out, want)
	}
}
