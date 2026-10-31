// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
)

// TestSessionStartSendsThePresetForEveryTool is acceptance: `session start --tool grok
// --permission read-only` was accepted and the preset dropped; the CLI wrote the mode only
// for Claude Code, so Codex, Grok and OpenCode sessions carried an empty mode and the engine
// stored "default". Every tool now gets the chosen preset; the engine applies it in the
// tool's own settings (or refuses it, TestSessionStartPrintsTheEnginesPresetRefusal).
func TestSessionStartSendsThePresetForEveryTool(t *testing.T) {
	for _, tool := range []string{"claude", "codex", "grok", "opencode", "gemini-cli", "cursor"} {
		for _, tc := range []struct{ permission, mode, template string }{
			{"edits-and-commands", "", "tpl-edit-run"}, {"edits-only", "acceptEdits", ""},
			{"read-only", "plan", ""}, {"ask", "default", ""},
			// the regression: the named modes are a view over the same values.
			{"supervised", "default", ""}, {"auto-accept-edits", "acceptEdits", ""},
			{"auto", "auto", ""},
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

// TestSessionStartFullSendsBypassPermissions: the console offers full permissions to an
// administrator, so `session start --permission full` sends the mode the console sends
// (bypassPermissions, no template) for every tool, and the first prompt follows. The
// engine's run-administrator check is the one gate (modules/sessions refuseUnrestrictedFor).
// noTemplates proves the start does not depend on the Edits and commands template.
func TestSessionStartFullSendsBypassPermissions(t *testing.T) {
	for _, tool := range []string{"claude", "codex", "grok", "opencode", "gemini-cli", "cursor"} {
		f := newFakeSessionEngine(t)
		f.noTemplates = true
		_, errb, err := execSessionCLI(t, nil, append([]string{"session", "start", t.TempDir(), "do the task",
			"--tool", tool, "--permission", "full", "-o", "json"}, sessionCreds(f.URL)...)...)
		if err != nil {
			t.Fatalf("%s --permission full: %v\n%s", tool, err, errb)
		}
		runs := f.postsTo(sessionRunsPath)
		if len(runs) != 1 {
			t.Fatalf("%s: %d launches, want 1", tool, len(runs))
		}
		if runs[0]["permission_mode"] != "bypassPermissions" || runs[0]["template_id"] != nil {
			t.Errorf("%s sent mode %q template %v, want bypassPermissions and none",
				tool, runs[0]["permission_mode"], runs[0]["template_id"])
		}
		input := f.postsTo(sessionRunsPath + "/01a0f6dc-0000-7000-8000-000000000009/input")
		if len(input) != 1 || !strings.Contains(input[0]["line"].(string), "do the task") {
			t.Errorf("%s: first prompt not sent after the start: %v", tool, input)
		}
	}
}

// A person without run administration gets the engine's sentence (403), no session is
// created and no prompt is sent. The sentence starts in lower case, so the CLI keeps the
// status around it; the engine's words stay in it whole.
func TestSessionStartFullPrintsTheEnginesRefusal(t *testing.T) {
	f := newFakeSessionEngine(t)
	f.launchRefusal = "only an administrator can run a session with full permissions; you can choose supervised, read only, auto-accept edits, or edits and commands"
	f.launchRefusalStatus = http.StatusForbidden
	_, _, err := execSessionCLI(t, nil, append([]string{"session", "start", t.TempDir(), "do the task",
		"--permission", "full"}, sessionCreds(f.URL)...)...)
	var refusal *apiRefusal
	if err == nil || !errors.As(err, &refusal) || refusal.status != http.StatusForbidden ||
		!strings.Contains(err.Error(), f.launchRefusal) {
		t.Fatalf("err = %v, want the engine's 403 sentence", err)
	}
	if exitcode.From(err) == 0 {
		t.Fatalf("exit code = 0 for a refused start")
	}
	// The refusal answers whatever is posted, so pin what the CLI asked for.
	if runs := f.postsTo(sessionRunsPath); len(runs) != 1 || runs[0]["permission_mode"] != "bypassPermissions" {
		t.Fatalf("launch requests = %v, want one bypassPermissions", runs)
	}
	if input := f.postsTo(sessionRunsPath + "/01a0f6dc-0000-7000-8000-000000000009/input"); len(input) != 0 {
		t.Fatalf("a prompt was sent after the refusal: %v", input)
	}
}

// The unknown-value sentence lists every name, full included, and says the older
// names still work.
func TestSessionStartUnknownPermissionNamesFull(t *testing.T) {
	f := newFakeSessionEngine(t)
	_, _, err := execSessionCLI(t, nil, append([]string{"session", "start", t.TempDir(), "--permission", "everything"}, sessionCreds(f.URL)...)...)
	if err == nil || !strings.Contains(err.Error(),
		"read-only, supervised, auto-accept-edits, edits-and-commands, auto or full") ||
		!strings.Contains(err.Error(), "ask and edits-only also work") {
		t.Fatalf("err = %v, want the sentence to list the names, full included, and the older names", err)
	}
}

// A template's mode wins over a narrower named one in the engine, so full with --template
// could start narrower than asked. The CLI refuses the pair before any effect, as it does
// for edits-and-commands with a template; omitting --permission takes the template's mode.
func TestSessionStartFullWithATemplateIsRefusedBeforeAnyEffect(t *testing.T) {
	f := newFakeSessionEngine(t)
	_, _, err := execSessionCLI(t, nil, append([]string{"session", "start", t.TempDir(),
		"--permission", "full", "--template", "tpl-custom"}, sessionCreds(f.URL)...)...)
	if err == nil || exitcode.From(err) != exitcode.Usage || !strings.Contains(err.Error(), "--permission") {
		t.Fatalf("err = %v, want a usage refusal naming --permission", err)
	}
	if len(f.postsTo(sessionRunsPath)) != 0 || len(f.postsTo(sessionWorkspacesPath)) != 0 {
		t.Fatalf("the refused pair had effects")
	}
	// Without --permission the template supplies its own mode, as before.
	if _, _, err := execSessionCLI(t, nil, append([]string{"session", "start", t.TempDir(), "--template", "tpl-custom"}, sessionCreds(f.URL)...)...); err != nil {
		t.Fatalf("template alone: %v", err)
	}
}

// TestSessionStartPrintsTheEnginesNameRefusal: a --name the engine refuses (a zero-width
// character, over 200 characters) is its 400, exactly as POST /runs answers it
// (modules/sessions TestRuntimeAPILaunchRefusesARunNameTheRenameRefuses). The CLI prints
// the engine's words and exits with the code it gives every 400, 1: the exit-code
// contract has no code for a refused value.
func TestSessionStartPrintsTheEnginesNameRefusal(t *testing.T) {
	f := newFakeSessionEngine(t)
	f.launchRefusal = "name must be at most 200 characters and hold no control or invisible characters"
	f.launchRefusalStatus = http.StatusBadRequest
	f.launchRefusalCode = "module_error"
	_, _, err := execSessionCLI(t, nil, append([]string{"session", "start", t.TempDir(),
		"--name", "zero\u200bwidth"}, sessionCreds(f.URL)...)...)
	var refusal *apiRefusal
	if err == nil || !errors.As(err, &refusal) || refusal.status != http.StatusBadRequest ||
		!strings.Contains(err.Error(), f.launchRefusal) {
		t.Fatalf("err = %v, want the engine's 400 sentence", err)
	}
	if got := exitcode.From(err); got != exitcode.Err {
		t.Fatalf("exit code = %d, want %d", got, exitcode.Err)
	}
	if runs := f.postsTo(sessionRunsPath); len(runs) != 1 || runs[0]["name"] != "zero\u200bwidth" {
		t.Fatalf("the CLI sent %v, want the name as given", runs)
	}
}

// A folder name is arbitrary and up to 255 bytes; the name derived from it stays within
// the 200 characters the engine accepts, with room for the -2 of a taken name.
func TestSessionStartCutsALongFolderNameToWhatTheEngineAccepts(t *testing.T) {
	f := newFakeSessionEngine(t)
	folder := filepath.Join(t.TempDir(), strings.Repeat("n", 250))
	if err := os.Mkdir(folder, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, errb, err := execSessionCLI(t, nil, append([]string{"session", "start", folder}, sessionCreds(f.URL)...)...); err != nil {
		t.Fatalf("start: %v\n%s", err, errb)
	}
	runs := f.postsTo(sessionRunsPath)
	if len(runs) != 1 || utf8.RuneCountInString(str(runs[0], "name")) != 190 {
		t.Fatalf("name sent = %v, want 190 characters", runs)
	}
}
