// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"errors"
	"net/http"
	"testing"
)

// TestSessionStartSendsThePresetForEveryTool is HU 043: `session start --tool grok
// --permission read-only` was accepted and the preset dropped; the CLI wrote the mode only
// for Claude Code, so Codex, Grok and OpenCode sessions carried an empty mode and the engine
// stored "default". Every tool now gets the chosen preset; the engine applies it in the
// tool's own settings (or refuses it, TestSessionStartPrintsTheEnginesPresetRefusal).
func TestSessionStartSendsThePresetForEveryTool(t *testing.T) {
	for _, tool := range []string{"claude", "codex", "grok", "opencode"} {
		for _, tc := range []struct{ permission, mode, template string }{
			{"edits-and-commands", "", "tpl-edit-run"}, {"edits-only", "acceptEdits", ""},
			{"read-only", "plan", ""}, {"ask", "default", ""},
		} {
			f := newFakeSessionEngine(t)
			_, errb, err := execSessionCLI(t, nil, append([]string{"session", "start", t.TempDir(), "--tool", tool,
				"--permission", tc.permission}, sessionCreds(f.URL)...)...)
			if err != nil {
				t.Fatalf("%s --permission %s: %v\n%s", tool, tc.permission, err, errb)
			}
			runs := f.postsTo(sessionRunsPath)
			if len(runs) != 1 {
				t.Fatalf("%s --permission %s: %d launches", tool, tc.permission, len(runs))
			}
			template, _ := runs[0]["template_id"].(string)
			if runs[0]["permission_mode"] != tc.mode || template != tc.template {
				t.Errorf("%s --permission %s sent mode %q template %q, want %q %q",
					tool, tc.permission, runs[0]["permission_mode"], template, tc.mode, tc.template)
			}
		}
	}
}

// TestSessionStartPrintsTheEnginesPresetRefusal: a preset the tool cannot honour is the
// engine's to refuse, in one sentence; the CLI prints it as it is.
func TestSessionStartPrintsTheEnginesPresetRefusal(t *testing.T) {
	f := newFakeSessionEngine(t)
	f.launchRefusal = "Grok Build cannot work in read-only mode. Choose edits-and-commands or ask."
	_, _, err := execSessionCLI(t, nil, append([]string{"session", "start", t.TempDir(), "--tool", "grok",
		"--permission", "read-only"}, sessionCreds(f.URL)...)...)
	var refusal *apiRefusal
	if err == nil || err.Error() != f.launchRefusal || !errors.As(err, &refusal) || refusal.status != http.StatusUnprocessableEntity {
		t.Fatalf("err = %v, want the engine's sentence alone (422)", err)
	}
}
