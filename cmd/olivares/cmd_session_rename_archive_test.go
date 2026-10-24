// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"strings"
	"testing"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
)

// #2795: `session rename` names a session by its name or id and makes one PUT; an
// empty name clears it, and the session is shown by its id again.
func TestSessionRenameSetsAndClearsTheName(t *testing.T) {
	f := newFakeSessionEngine(t)
	f.runs = []map[string]any{{"run_ref": "r1", "name": "demo", "state": "running"}}

	out, errb, err := execSessionCLI(t, nil, append([]string{"session", "rename", "demo", "release notes"}, sessionCreds(f.URL)...)...)
	if err != nil || out != "Renamed demo to release notes.\n" {
		t.Fatalf("rename: out=%q err=%v stderr=%q", out, err, errb)
	}
	puts := f.postsTo(sessionRunsPath + "/r1/name")
	if len(puts) != 1 || puts[0]["name"] != "release notes" {
		t.Fatalf("PUT bodies = %v, want one {name: release notes}", puts)
	}
	js, _, err := execSessionCLI(t, nil, append([]string{"session", "rename", "release", "notes v2", "-o", "json"}, sessionCreds(f.URL)...)...)
	if err != nil {
		t.Fatal(err)
	}
	if got := decodeSessionBody(t, js); got["run_ref"] != "r1" || got["name"] != "notes v2" {
		t.Fatalf("json = %v, want the run", got)
	}
	out, _, err = execSessionCLI(t, nil, append([]string{"session", "rename", "notes v2", ""}, sessionCreds(f.URL)...)...)
	if err != nil || out != "Cleared the name of notes v2.\n" {
		t.Fatalf("clear: out=%q err=%v", out, err)
	}
	if puts = f.postsTo(sessionRunsPath + "/r1/name"); len(puts) != 3 || puts[2]["name"] != "" {
		t.Fatalf("clear body = %v, want {name: \"\"}", puts)
	}
}

// A name must pick one session: `rename` refuses a name another session that is not
// cleaned carries, as `start` does, and takes one only a cleaned session left behind.
func TestSessionRenameRefusesANameAnotherSessionHolds(t *testing.T) {
	f := newFakeSessionEngine(t)
	f.runs = []map[string]any{
		{"run_ref": "r1", "name": "a", "state": "running", "created_at": "2026-10-22T10:00:00Z"},
		{"run_ref": "00000000-0000-4000-8000-000000000002", "name": "b", "state": "stopped", "created_at": "2026-10-22T09:00:00Z"},
		{"run_ref": "r3", "name": "c", "state": "cleaned", "created_at": "2026-10-22T08:00:00Z"},
	}

	_, errb, err := execSessionCLI(t, nil, append([]string{"session", "rename", "a", " b "}, sessionCreds(f.URL)...)...)
	if code := exitcode.From(err); code != exitcode.Conflict || err == nil || !strings.Contains(err.Error(), `Another session is already named "b"`) {
		t.Fatalf("rename onto a held name: code=%d err=%v stderr=%q", code, err, errb)
	}
	if puts := f.postsTo(sessionRunsPath + "/r1/name"); len(puts) != 0 {
		t.Fatalf("PUT bodies = %v, want none", puts)
	}
	out, errb, err := execSessionCLI(t, nil, append([]string{"session", "rename", "a", "c"}, sessionCreds(f.URL)...)...)
	if err != nil || out != "Renamed a to c.\n" {
		t.Fatalf("rename onto a cleaned session's name: out=%q err=%v stderr=%q", out, err, errb)
	}
	// The name a session already has is never refused, even where an older session
	// shares it from before.
	f.runs = append(f.runs, map[string]any{"run_ref": "r4", "name": "b", "state": "stopped", "created_at": "2026-10-22T07:00:00Z"})
	if _, errb, err := execSessionCLI(t, nil, append([]string{"session", "rename", "00000000-0000-4000-8000-000000000002", "b"}, sessionCreds(f.URL)...)...); err != nil {
		t.Fatalf("rename to its own name: err=%v stderr=%q", err, errb)
	}
}

// `session archive` sets the session's archived mark and --undo clears it, one call
// each; the JSON answer is the run.
func TestSessionArchiveAndUndoMakeOneCallEach(t *testing.T) {
	f := newFakeSessionEngine(t)
	f.runs = []map[string]any{{"run_ref": "r1", "name": "demo", "state": "stopped"}}

	out, _, err := execSessionCLI(t, nil, append([]string{"session", "archive", "demo"}, sessionCreds(f.URL)...)...)
	if err != nil || out != "Archived demo.\n" {
		t.Fatalf("archive: out=%q err=%v", out, err)
	}
	js, _, err := execSessionCLI(t, nil, append([]string{"session", "archive", "demo", "--undo", "-o", "json"}, sessionCreds(f.URL)...)...)
	if err != nil {
		t.Fatal(err)
	}
	if got := decodeSessionBody(t, js); got["run_ref"] != "r1" || got["archived_at"] != nil {
		t.Fatalf("json = %v, want the run without archived_at", got)
	}
	out, _, err = execSessionCLI(t, nil, append([]string{"session", "archive", "demo", "--undo"}, sessionCreds(f.URL)...)...)
	if err != nil || out != "Unarchived demo.\n" {
		t.Fatalf("undo: out=%q err=%v", out, err)
	}
	hits := strings.Join(f.hits, "\n")
	if strings.Count(hits, "POST "+sessionRunsPath+"/r1/archive") != 1 || strings.Count(hits, "DELETE "+sessionRunsPath+"/r1/archive") != 2 {
		t.Fatalf("hits:\n%s", hits)
	}
}

// `session ls` hides archived sessions, and --archived lists only those; the
// released ones stay behind --all either way.
func TestSessionListHidesArchivedUnlessAsked(t *testing.T) {
	f := newFakeSessionEngine(t)
	f.runs = []map[string]any{
		{"run_ref": "r-kept", "name": "kept", "state": "stopped"},
		{"run_ref": "r-shelved", "name": "shelved", "state": "stopped", "archived_at": "2026-10-21T10:00:00Z"},
		{"run_ref": "r-gone", "name": "gone", "state": "cleaned", "archived_at": "2026-10-21T10:00:01Z"},
	}
	for _, tc := range []struct {
		args []string
		want string
	}{
		{nil, "r-kept"},
		// --all is every session, and a state asked by name is every session in it:
		// a script that reads `ls --state stopped` does not lose one someone archived.
		{[]string{"--all"}, "r-kept,r-shelved,r-gone"},
		{[]string{"--state", "stopped"}, "r-kept,r-shelved"},
		{[]string{"--archived"}, "r-shelved"},
		{[]string{"--archived", "--all"}, "r-shelved,r-gone"},
		{[]string{"--archived", "--state", "stopped"}, "r-shelved"},
	} {
		args := append(append([]string{"session", "ls"}, tc.args...), "-o", "json")
		out, _, err := execSessionCLI(t, nil, append(args, sessionCreds(f.URL)...)...)
		if err != nil {
			t.Fatal(err)
		}
		if got := listedRunRefs(t, out); got != tc.want {
			t.Fatalf("ls %v = %q, want %q", tc.args, got, tc.want)
		}
	}
	out, _, err := execSessionCLI(t, nil, append([]string{"session", "ls"}, sessionCreds(f.URL)...)...)
	if err != nil || !strings.Contains(out, "kept") || strings.Contains(out, "shelved") {
		t.Fatalf("ls table = %q err=%v", out, err)
	}
	// Every session archived: the plain list says so, and where they are.
	f.runs = f.runs[1:2]
	out, _, err = execSessionCLI(t, nil, append([]string{"session", "ls"}, sessionCreds(f.URL)...)...)
	if err != nil || !strings.Contains(out, "No sessions in the list; 1 archived. List them: olivares session ls --archived") {
		t.Fatalf("ls with every session archived = %q err=%v", out, err)
	}
	f.runs = []map[string]any{{"run_ref": "r-kept", "name": "kept", "state": "stopped"}}
	out, _, err = execSessionCLI(t, nil, append([]string{"session", "ls", "--archived"}, sessionCreds(f.URL)...)...)
	if err != nil || !strings.Contains(out, "No archived sessions.") {
		t.Fatalf("ls --archived with none = %q err=%v", out, err)
	}
}

// The delete alias refuses a running session as rm does, and deletes nothing.
func TestSessionDeleteRefusesARunningSession(t *testing.T) {
	f := newFakeSessionEngine(t)
	f.runs = []map[string]any{{"run_ref": "r1", "name": "demo", "state": "running"}}
	_, _, err := execSessionCLI(t, nil, append([]string{"session", "delete", "demo"}, sessionCreds(f.URL)...)...)
	if exitcode.From(err) != exitcode.Conflict || !strings.Contains(err.Error(), "Session demo is running. Stop it first: olivares session stop demo") {
		t.Fatalf("err = %v, want a conflict that names stop", err)
	}
	if hits := strings.Join(f.hits, "\n"); strings.Contains(hits, "DELETE ") || strings.Contains(hits, "/cleanup") {
		t.Fatalf("a running session was released or deleted:\n%s", hits)
	}
}
