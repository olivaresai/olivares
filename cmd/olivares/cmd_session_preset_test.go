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
	for _, tool := range []string{"claude", "codex", "grok", "opencode", "gemini-cli"} {
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

// A saved stricter read-only template reaches the same launch route as the console.
func TestSessionStartUsesTheSelectedTemplate(t *testing.T) {
	f := newFakeSessionEngine(t)
	_, stderr, err := execSessionCLI(t, nil, append([]string{"session", "start", t.TempDir(), "--permission", "read-only", "--template", "tpl-strict-read-only"}, sessionCreds(f.URL)...)...)
	if err != nil {
		t.Fatalf("selected template: %v\n%s", err, stderr)
	}
	runs := f.postsTo(sessionRunsPath)
	if len(runs) != 1 || runs[0]["template_id"] != "tpl-strict-read-only" || runs[0]["permission_mode"] != "plan" {
		t.Fatalf("selected template launch=%v", runs)
	}
}

func TestSessionTemplateKeepsTheEditsAndCommandsContract(t *testing.T) {
	t.Run("template supplies its own mode", func(t *testing.T) {
		f := newFakeSessionEngine(t)
		_, stderr, err := execSessionCLI(t, nil, append([]string{"session", "start", t.TempDir(), "--template", "tpl-strict-read-only"}, sessionCreds(f.URL)...)...)
		if err != nil {
			t.Fatalf("template default: %v\n%s", err, stderr)
		}
		runs := f.postsTo(sessionRunsPath)
		if len(runs) != 1 || runs[0]["template_id"] != "tpl-strict-read-only" || runs[0]["permission_mode"] != "" {
			t.Fatalf("template default launch=%v", runs)
		}
	})
	t.Run("explicit enforcing preset cannot be replaced", func(t *testing.T) {
		f := newFakeSessionEngine(t)
		_, _, err := execSessionCLI(t, nil, append([]string{"session", "start", t.TempDir(), "--permission", "edits-and-commands", "--template", "tpl-custom"}, sessionCreds(f.URL)...)...)
		if err == nil || len(f.postsTo(sessionRunsPath)) != 0 || len(f.postsTo(sessionWorkspacesPath)) != 0 {
			t.Fatalf("conflicting template had effects: %v", err)
		}
	})
}
