// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package driverfacts

import "testing"

// NameStem is the word generated account names start from: the optional Alias,
// else the key. Only Gemini CLI declares one; every other tool keeps its key.
func TestNameStem(t *testing.T) {
	want := map[string]string{
		"claude": "claude", "codex": "codex", "grok": "grok", "opencode": "opencode",
		"gemini-cli": "gemini", "ollama": "ollama",
	}
	for _, f := range All() {
		if got := f.NameStem(); got != want[f.Key] {
			t.Errorf("%s stem = %q, want %q", f.Key, got, want[f.Key])
		}
	}
	if got := (Facts{Key: "x", Alias: "y"}).NameStem(); got != "y" {
		t.Errorf("alias stem = %q, want y", got)
	}
	if got := (Facts{Key: "x"}).NameStem(); got != "x" {
		t.Errorf("default stem = %q, want x", got)
	}
}
