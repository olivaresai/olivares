// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"testing"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
)

// notLoggedIn is the reason refresh 08b recorded for a Claude session on a profile with
// no login: the tool exited before its first turn.
const notLoggedIn = "exit 1: Not logged in · Please run /login"

// TestSendToASessionThatEndsAtOnceSaysWhy is J7 on refresh 08b: `session start . "hello"`
// on a profile with no login printed "Started notes", then "The session's output has
// ended, so it cannot take a message. Resume it first." The engine had recorded why the
// session failed, and resuming it failed the same way. The CLI reads the session again
// and says what the engine recorded; it sends nothing.
func TestSendToASessionThatEndsAtOnceSaysWhy(t *testing.T) {
	f := newFakeSessionEngine(t)
	f.runs = []map[string]any{{"run_ref": "01a0f9d4-e0fd-72a6-b388-a35c2a2a90c9", "name": "notes", "state": "running",
		"provider_driver": "claude", "transport": "stream-json"}}
	f.failOnAttach = notLoggedIn
	_, _, err := execSessionCLI(t, nil, append([]string{"session", "send", "notes", "hello"}, sessionCreds(f.URL)...)...)
	want := "Session notes failed: " + notLoggedIn + ". Resume it once that is fixed: olivares session resume notes"
	if exitcode.From(err) != exitcode.Conflict || err == nil || err.Error() != want {
		t.Fatalf("err = %v (exit %d)\nwant %q", err, exitcode.From(err), want)
	}
	if n := len(f.postsTo(sessionRunsPath + "/01a0f9d4-e0fd-72a6-b388-a35c2a2a90c9/input")); n != 0 {
		t.Fatalf("%d inputs sent to a session that had ended", n)
	}
}

// TestSendToAFailedSessionSaysWhy: a session that is already failed gave "Session notes
// is failed. Resume it first". It says the engine's reason, and resume comes after the
// cause is fixed; a stopped session keeps "Resume it first"
// (TestSessionSendToAStoppedSessionSaysToResume).
func TestSendToAFailedSessionSaysWhy(t *testing.T) {
	f := newFakeSessionEngine(t)
	f.runs = []map[string]any{{"run_ref": "r1", "name": "notes", "state": "failed", "reason": notLoggedIn,
		"provider_driver": "claude", "transport": "stream-json"}}
	_, _, err := execSessionCLI(t, nil, append([]string{"session", "send", "notes", "hello"}, sessionCreds(f.URL)...)...)
	want := "Session notes failed: " + notLoggedIn + ". Resume it once that is fixed: olivares session resume notes"
	if exitcode.From(err) != exitcode.Conflict || err == nil || err.Error() != want {
		t.Fatalf("err = %v (exit %d)\nwant %q", err, exitcode.From(err), want)
	}
	// Without a recorded reason, the CLI does not guess one.
	f.runs[0]["reason"] = ""
	_, _, err = execSessionCLI(t, nil, append([]string{"session", "send", "notes", "hello"}, sessionCreds(f.URL)...)...)
	if want := "Session notes failed. See why: olivares session show notes"; err == nil || err.Error() != want {
		t.Fatalf("err = %v, want %q", err, want)
	}
}
