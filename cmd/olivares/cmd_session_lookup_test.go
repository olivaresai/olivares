// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"strings"
	"testing"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
)

// `session ls` cut a long name with "…" and showed no id, and `session show
// "List the files"` answered "No session is named". The id `session ls` now shows,
// the start of the name, or the cut cell as copied, names the session; a start two
// sessions share is refused with the way out.
func TestSessionIsNamedByTheStartOfItsNameOrItsID(t *testing.T) {
	const files, tests = "0199a1b2-c3d4-7e5f-8a9b-0c1d2e3f4a5b", "0199a1b2-c3d4-7e5f-8a9b-ffeeddccbbaa"
	f := newFakeSessionEngine(t)
	f.runs = []map[string]any{
		{"run_ref": files, "name": "List the files in this folder and say in one sentence what it is", "state": "running", "created_at": "2026-10-02T18:27:00Z"},
		{"run_ref": tests, "name": "List the tests", "state": "stopped", "created_at": "2026-10-02T18:00:00Z"},
	}
	out, errb, err := execSessionCLI(t, nil, append([]string{"session", "ls"}, sessionCreds(f.URL)...)...)
	if err != nil || !strings.Contains(out, "0c1d2e3f4a5b") || !strings.Contains(out, "ffeeddccbbaa") {
		t.Fatalf("ls: %v %s\n%s, want each session's id", err, errb, out)
	}
	for _, arg := range []string{"List the files", "List the files in this folder and say in one sentence what …", "0c1d2e3f4a5b", "…0c1d2e3f4a5b", "f4a5b"} {
		before := len(f.postsTo(sessionRunsPath + "/" + files + "/stop"))
		if _, errb, err := execSessionCLI(t, nil, append([]string{"session", "stop", arg}, sessionCreds(f.URL)...)...); err != nil {
			t.Fatalf("stop %q: %v %s", arg, err, errb)
		}
		if len(f.postsTo(sessionRunsPath+"/"+files+"/stop")) != before+1 {
			t.Fatalf("stop %q did not reach the session it names: %v", arg, f.hits)
		}
	}
	_, _, err = execSessionCLI(t, nil, append([]string{"session", "show", "List the"}, sessionCreds(f.URL)...)...)
	if exitcode.From(err) != exitcode.Usage || !strings.Contains(err.Error(), "2 sessions match") || !strings.Contains(err.Error(), "by its id") {
		t.Fatalf("a shared start = %v, want the two-match refusal naming the id", err)
	}
}

// TestSessionFollowShowsThePersonsMessages: follow printed only the agent's replies, so
// a transcript read as a monologue. Claude Code replays each message the person sent
// (--replay-user-messages, isReplay); follow shows it above its reply, "[image]" for an
// attachment, and leaves out the tool's own synthetic user frames.
func TestSessionFollowShowsThePersonsMessages(t *testing.T) {
	f := newFakeSessionEngine(t)
	f.runs = []map[string]any{{"run_ref": "r1", "name": "demo", "state": "running", "provider_driver": "claude", "transport": "stream-json"}}
	f.replay = []string{
		`{"type":"user","isReplay":true,"message":{"role":"user","content":[{"type":"text","text":"What did I ask in my first message?"}]}}`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"You asked me to create hola.txt."}]}}`,
		`{"type":"user","isReplay":true,"message":{"role":"user","content":[{"type":"text","text":"Describe this image"}]},"attachments":[{"id":"att_1","media_type":"image/png"}]}`,
		`{"type":"user","message":{"role":"user","content":[{"type":"text","text":"[Request interrupted by user for tool use]"}]}}`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"A screenshot."}]}}`,
	}
	f.endAfterInput = true
	close(f.sent) // no input: the stream ends after the replay
	out, errb, err := execSessionCLI(t, nil, append([]string{"session", "follow", "demo"}, sessionCreds(f.URL)...)...)
	if err != nil {
		t.Fatalf("follow: %v\n%s", err, errb)
	}
	want := "› What did I ask in my first message?\nYou asked me to create hola.txt.\n› Describe this image [image]\nA screenshot.\n"
	if out != want {
		t.Fatalf("follow =\n%s\nwant\n%s", out, want)
	}
}

// Two sessions that hold one name (#2978: the engine used to accept it) are refused
// by name, never resolved to the newer: `session stop api` must reach neither
// unasked. A cleaned session holds nothing, so the live one is the one it names.
func TestSessionNameHeldTwiceIsRefusedNotResolvedToTheNewer(t *testing.T) {
	const older, newer, gone = "0199a1b2-c3d4-7e5f-8a9b-000000000001", "0199a1b2-c3d4-7e5f-8a9b-000000000002", "0199a1b2-c3d4-7e5f-8a9b-000000000003"
	f := newFakeSessionEngine(t)
	f.runs = []map[string]any{
		{"run_ref": older, "name": "api", "state": "running", "created_at": "2026-10-02T18:00:00Z"},
		{"run_ref": newer, "name": "api", "state": "running", "created_at": "2026-10-02T18:30:00Z"},
	}
	_, _, err := execSessionCLI(t, nil, append([]string{"session", "stop", "api"}, sessionCreds(f.URL)...)...)
	if exitcode.From(err) != exitcode.Usage || !strings.Contains(err.Error(), "2 sessions match") || !strings.Contains(err.Error(), "by its id") {
		t.Fatalf("stop of a name two live sessions hold = %v, want the refusal naming the id", err)
	}
	for _, ref := range []string{older, newer} {
		if n := len(f.postsTo(sessionRunsPath + "/" + ref + "/stop")); n != 0 {
			t.Fatalf("stop reached %s %d times", ref, n)
		}
	}

	// The newer namesake is cleaned: only the older one holds the name, so the name
	// picks it, not the newer cleaned row the sort used to choose.
	f.runs = []map[string]any{
		{"run_ref": older, "name": "api", "state": "running", "created_at": "2026-10-02T18:00:00Z"},
		{"run_ref": gone, "name": "api", "state": "cleaned", "created_at": "2026-10-02T19:00:00Z"},
	}
	before := len(f.postsTo(sessionRunsPath + "/" + older + "/stop"))
	if _, errb, err := execSessionCLI(t, nil, append([]string{"session", "stop", "api"}, sessionCreds(f.URL)...)...); err != nil {
		t.Fatalf("stop api with a cleaned namesake: %v %s", err, errb)
	}
	if len(f.postsTo(sessionRunsPath+"/"+older+"/stop")) != before+1 {
		t.Fatalf("stop api with a cleaned namesake did not reach the live session: %v", f.hits)
	}

	// No holder is live: the name resolves to the newest row, the fallthrough the
	// live-count rule leaves (the engine refuses what a cleaned row cannot take).
	f.runs = []map[string]any{
		{"run_ref": older, "name": "api", "state": "cleaned", "created_at": "2026-10-02T18:00:00Z"},
		{"run_ref": gone, "name": "api", "state": "cleaned", "created_at": "2026-10-02T19:00:00Z"},
	}
	before = len(f.postsTo(sessionRunsPath + "/" + gone + "/stop"))
	if _, errb, err := execSessionCLI(t, nil, append([]string{"session", "stop", "api"}, sessionCreds(f.URL)...)...); err != nil {
		t.Fatalf("stop api with no live holder: %v %s", err, errb)
	}
	if len(f.postsTo(sessionRunsPath+"/"+gone+"/stop")) != before+1 {
		t.Fatalf("stop api with no live holder did not reach the newest row: %v", f.hits)
	}
}
