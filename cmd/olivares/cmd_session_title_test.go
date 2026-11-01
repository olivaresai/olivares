// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"strings"
	"testing"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
)

// `session title` asks the engine to cut the session's title from its first message:
// one POST, with the style only when one was given, and the answer is the run.
func TestSessionTitleAsksTheEngineForOne(t *testing.T) {
	f := newFakeSessionEngine(t)
	f.runs = []map[string]any{{"run_ref": "r1", "name": "demo", "state": "running"}}

	out, errb, err := execSessionCLI(t, nil, append([]string{"session", "title", "demo"}, sessionCreds(f.URL)...)...)
	if err != nil || out != "Titled demo: made sentence.\n" {
		t.Fatalf("title: out=%q err=%v stderr=%q", out, err, errb)
	}
	if posts := f.postsTo(sessionRunsPath + "/r1/title"); len(posts) != 1 || len(posts[0]) != 0 {
		t.Fatalf("POST bodies = %v, want one empty body: the engine's default style", posts)
	}
	out, _, err = execSessionCLI(t, nil, append([]string{"session", "title", "demo", "--style", "short"}, sessionCreds(f.URL)...)...)
	if err != nil || out != "Titled demo: made short.\n" {
		t.Fatalf("title --style: out=%q err=%v", out, err)
	}
	if posts := f.postsTo(sessionRunsPath + "/r1/title"); len(posts) != 2 || posts[1]["style"] != "short" {
		t.Fatalf("POST bodies = %v, want the second to carry style short", posts)
	}
	js, _, err := execSessionCLI(t, nil, append([]string{"session", "title", "demo", "-o", "json"}, sessionCreds(f.URL)...)...)
	if err != nil {
		t.Fatal(err)
	}
	if got := decodeSessionBody(t, js); got["run_ref"] != "r1" || got["title"] != "made sentence" || got["name"] != "demo" {
		t.Fatalf("json = %v, want the run with its title and its own name", got)
	}
	// show names the session by its name and prints the title beside it.
	shown, _, err := execSessionCLI(t, nil, append([]string{"session", "show", "demo"}, sessionCreds(f.URL)...)...)
	if err != nil || !strings.Contains(shown, "demo") || !strings.Contains(shown, "TITLE") || !strings.Contains(shown, "made sentence") {
		t.Fatalf("show: out=%q err=%v, want the name and a title row", shown, err)
	}
}

// A style the engine does not have is refused here, before a call is made.
func TestSessionTitleRefusesAnUnknownStyle(t *testing.T) {
	f := newFakeSessionEngine(t)
	f.runs = []map[string]any{{"run_ref": "r1", "name": "demo", "state": "running"}}

	_, _, err := execSessionCLI(t, nil, append([]string{"session", "title", "demo", "--style", "poetic"}, sessionCreds(f.URL)...)...)
	if code := exitcode.From(err); code != exitcode.Usage || err == nil || !strings.Contains(err.Error(), "short, sentence or long") {
		t.Fatalf("title --style poetic: code=%d err=%v", code, err)
	}
	if posts := f.postsTo(sessionRunsPath + "/r1/title"); len(posts) != 0 {
		t.Fatalf("POST bodies = %v, want none", posts)
	}
}

// The list names a session with no name by its title. The title is not an address, so
// what the command line tells a person to type for the session stays its id, and a
// name wins over a title.
func TestSessionListNamesAnUnnamedSessionByItsTitle(t *testing.T) {
	f := newFakeSessionEngine(t)
	f.runs = []map[string]any{
		{"run_ref": "run-aaaa1111", "title": "Fix the login bug", "state": "stopped"},
		{"run_ref": "run-bbbb2222", "name": "demo", "title": "Plan the migration", "state": "stopped"},
		{"run_ref": "run-cccc3333", "state": "stopped"},
	}
	out, errb, err := execSessionCLI(t, nil, append([]string{"session", "ls"}, sessionCreds(f.URL)...)...)
	if err != nil {
		t.Fatalf("ls: %v stderr=%q", err, errb)
	}
	if !strings.Contains(out, "Fix the login bug") || !strings.Contains(out, "demo") {
		t.Fatalf("ls = %q, want the title of the unnamed session and the name of the named one", out)
	}
	if strings.Contains(out, "Plan the migration") {
		t.Fatalf("ls = %q: a session with a name shows the name, not its title", out)
	}
	if label := sessionLabel(f.runs[0]); label != "run-aaaa1111" {
		t.Fatalf("sessionLabel = %q: a title is not an address, so what is told to type stays the id", label)
	}
}
