// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
)

// An empty --permission names no permission and is not a template's "use my mode": it is
// refused as it always was, not turned into a session that asks.
func TestSessionStartRefusesAnEmptyPermission(t *testing.T) {
	f := newFakeSessionEngine(t)
	_, _, err := execSessionCLI(t, nil, append([]string{"session", "start", t.TempDir(), "--permission", ""}, sessionCreds(f.URL)...)...)
	if err == nil || exitcode.From(err) != exitcode.Usage || !strings.Contains(err.Error(), "Unknown permission") {
		t.Fatalf("err = %v, want the usage refusal for an unknown permission", err)
	}
	if len(f.postsTo(sessionRunsPath)) != 0 || len(f.postsTo(sessionWorkspacesPath)) != 0 {
		t.Fatalf("the refused start had effects")
	}
}

// A tool with no automatic mode asks instead, and the engine stores that mode. The person
// who asked for Auto is told, in the words of the tool they started.
func TestSessionStartSaysWhenAutoAsksInstead(t *testing.T) {
	for _, tc := range []struct {
		driver, stored string
		wantNote       bool
	}{
		{"codex", "default", true},
		{"claude", "auto", false},
	} {
		f := newFakeSessionEngine(t, func(f *fakeSessionEngine) { f.launchDriver, f.launchRunMode = tc.driver, tc.stored })
		_, stderr, err := execSessionCLI(t, nil, append([]string{"session", "start", t.TempDir(), "--tool", tc.driver, "--permission", "auto"}, sessionCreds(f.URL)...)...)
		if err != nil {
			t.Fatalf("%s: %v\n%s", tc.driver, err, stderr)
		}
		note := tc.driver + " has no automatic mode: it asks before each action instead (supervised)"
		if got := strings.Contains(stderr, note); got != tc.wantNote {
			t.Errorf("%s: note said = %v, want %v\n%s", tc.driver, got, tc.wantNote, stderr)
		}
	}
}

// the regression: a folder stores the permission its sessions start under. `session start`
// takes it when the person named no permission and no template; the flag and a
// template still win, and the line on stderr says where the permission came from.
func TestSessionStartUsesTheFoldersDefaultPermission(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	withDefault := func(name string) func(*fakeSessionEngine) {
		return func(f *fakeSessionEngine) {
			ws := map[string]any{"workspace_ref": "ws-1", "root_path": root, "state": "active"}
			if name != "" {
				ws["default_permission"] = name
			}
			f.workspaces = []map[string]any{ws}
		}
	}
	for _, tc := range []struct {
		name         string
		stored       string
		args         []string
		wantMode     string
		wantTemplate string
		wantNote     string
	}{
		{"the folder default applies", "supervised", nil, "default", "", "Using permission supervised (this folder's default)"},
		{"a default that needs the built-in template finds it", "edits-and-commands", nil, "", "tpl-edit-run", "Using permission edits-and-commands (this folder's default)"},
		// A script cannot see a prompt, so a stored Full is never taken from the folder: it
		// takes the flag, and the line says what to type.
		{"a default of Full is not used without the flag", "full", nil, "", "tpl-edit-run", "This folder's default is full; pass --permission full to use it"},
		{"the flag may choose Full in a folder whose default is Full", "full", []string{"--permission", "full"}, "bypassPermissions", "", ""},
		// Auto lets the tool act without asking per call, as Full does: the same rule.
		{"a default of Auto is not used without the flag", "auto", nil, "", "tpl-edit-run", "This folder's default is auto; pass --permission auto to use it"},
		{"the flag may choose Auto in a folder whose default is Auto", "auto", []string{"--permission", "auto"}, "auto", "", ""},
		{"the flag wins", "supervised", []string{"--permission", "read-only"}, "plan", "", ""},
		{"the flag wins even when it says the usual default", "supervised", []string{"--permission", "edits-and-commands"}, "", "tpl-edit-run", ""},
		{"a template wins", "supervised", []string{"--template", "tpl-custom"}, "", "tpl-custom", ""},
		{"a folder with no default starts as it always did", "", nil, "", "tpl-edit-run", ""},
	} {
		f := newFakeSessionEngine(t, withDefault(tc.stored))
		args := append([]string{"session", "start", root}, tc.args...)
		_, stderr, err := execSessionCLI(t, nil, append(args, sessionCreds(f.URL)...)...)
		if err != nil {
			t.Fatalf("%s: %v\n%s", tc.name, err, stderr)
		}
		runs := f.postsTo(sessionRunsPath)
		if len(runs) != 1 {
			t.Fatalf("%s: %d launches", tc.name, len(runs))
		}
		template, _ := runs[0]["template_id"].(string)
		if runs[0]["permission_mode"] != tc.wantMode || template != tc.wantTemplate {
			t.Errorf("%s: sent mode %q template %q, want %q %q", tc.name, runs[0]["permission_mode"], template, tc.wantMode, tc.wantTemplate)
		}
		if tc.wantNote == "" && strings.Contains(stderr, "this folder's default") {
			t.Errorf("%s: said the folder default applied: %s", tc.name, stderr)
		}
		if (tc.stored == "full" || tc.stored == "auto") && tc.args == nil && strings.Contains(stderr, "Using permission "+tc.stored) {
			t.Errorf("%s: took %s from the folder: %s", tc.name, tc.stored, stderr)
		}
		if tc.wantNote != "" && !strings.Contains(stderr, tc.wantNote) {
			t.Errorf("%s: stderr = %q, want it to say %q", tc.name, stderr, tc.wantNote)
		}
		if got := f.postsTo(sessionWorkspacesPath); len(got) != 0 {
			t.Errorf("%s: registered a folder that already was: %v", tc.name, got)
		}
	}
}
