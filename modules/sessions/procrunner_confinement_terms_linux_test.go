// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build linux

package sessions

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/modules/sessions/confine"
)

// Both launch terms survive the ABI composition: the held folder is
// readable and its content stays protected when a strict template is selected.
func TestProcRunnerKeepsHeldFoldersWithEitherTruncationChoice(t *testing.T) {
	if state := confine.Probe(); state.Mode != confine.ModeLandlock || state.ABI < 3 {
		t.Fatal("this native composition check needs Landlock ABI 3 or later")
	}
	for _, strict := range []bool{false, true} {
		name := "default"
		if strict {
			name = "strict"
		}
		t.Run(name, func(t *testing.T) {
			folder, tools := t.TempDir(), t.TempDir()
			path := filepath.Join(tools, "synthetic-tool-data")
			if err := os.WriteFile(path, []byte("held-folder-fixture"), 0600); err != nil {
				t.Fatal(err)
			}
			held, err := confine.OpenDirectory(tools)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = held.Close() })
			proc, err := NewProcRunner().Launch(t.Context(), LaunchSpec{
				Program: "/bin/sh", Args: []string{"-c", `/bin/cat "$1" || exit 6
if (printf changed > "$1") 2>/dev/null; then exit 7; fi
`, "sh", path}, Dir: folder, WaitDelay: 2 * time.Second,
				Confinement:      &confine.Policy{ReadOnly: []string{folder}, Sealed: []string{folder}},
				ConfinementFiles: []*os.File{held}, ConfinementRequireTruncateProtection: strict,
			})
			if err != nil {
				t.Fatal(err)
			}
			var output strings.Builder
			for frame := range proc.Output() {
				output.Write(frame.Data)
			}
			if code, err := proc.Wait(); err != nil || code != 0 || !strings.Contains(output.String(), "held-folder-fixture") {
				t.Fatalf("the composed launch lost its held folder: exit=%d err=%v output=%q", code, err, output.String())
			}
			if content, err := os.ReadFile(path); err != nil || string(content) != "held-folder-fixture" {
				t.Fatal("the held folder became writable")
			}
		})
	}
}
