// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
)

func moveEngine(t *testing.T) *fakeSessionEngine {
	t.Helper()
	f := newFakeSessionEngine(t)
	f.runs = []map[string]any{{"run_ref": "r1", "name": "demo", "state": "stopped", "provider_driver": "claude", "provider_profile_ref": "ppf_a", "claude_session_id": "sid-1"}}
	f.profiles = []map[string]any{
		{"profile_ref": "ppf_b", "driver": "claude", "state": "active"},
		{"profile_ref": "ppf_codex", "driver": "codex", "state": "active"},
	}
	return f
}

// `session move` names the session by its name and the account by its reference,
// posts the move, and starts nothing: the next step is the ordinary resume.
func TestSessionMoveMovesTheSessionAndStartsNothing(t *testing.T) {
	f := moveEngine(t)
	out, _, err := execSessionCLI(t, nil, append([]string{"session", "move", "demo", "--account", "ppf_b"}, sessionCreds(f.URL)...)...)
	if err != nil {
		t.Fatal(err)
	}
	body := f.postsTo(sessionRunsPath + "/r1/move")
	if len(body) != 1 || body[0]["provider_profile_ref"] != "ppf_b" {
		t.Fatalf("move body = %v", body)
	}
	if !strings.Contains(out, "Moved demo to account ppf_b. Resume it: olivares session resume demo") {
		t.Fatalf("output = %q", out)
	}
	if len(f.postsTo(sessionRunsPath+"/r1/resume")) != 0 {
		t.Fatal("move resumed the session without --resume")
	}
}

func TestSessionMoveWithResumeResumesAfterTheMove(t *testing.T) {
	f := moveEngine(t)
	out, _, err := execSessionCLI(t, nil, append([]string{"session", "move", "demo", "--account", "ppf_b", "--resume"}, sessionCreds(f.URL)...)...)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.postsTo(sessionRunsPath+"/r1/move")) != 1 || len(f.postsTo(sessionRunsPath+"/r1/resume")) != 1 {
		t.Fatalf("hits = %v", f.hits)
	}
	if !strings.Contains(out, "Moved demo to account ppf_b and resumed it.") {
		t.Fatalf("output = %q", out)
	}
}

func TestSessionMoveNeedsAnAccountAndSaysWhatTheEngineRefused(t *testing.T) {
	f := moveEngine(t)
	_, _, err := execSessionCLI(t, nil, append([]string{"session", "move", "demo"}, sessionCreds(f.URL)...)...)
	if exitcode.From(err) != exitcode.Usage || !strings.Contains(err.Error(), "--account") {
		t.Fatalf("err = %v, want a usage error naming --account", err)
	}
	if len(f.postsTo(sessionRunsPath+"/r1/move")) != 0 {
		t.Fatal("a move without an account reached the engine")
	}
	f.moveRefusal = "the target profile belongs to another tool; use a handoff to continue in it"
	_, _, err = execSessionCLI(t, nil, append([]string{"session", "move", "demo", "--account", "ppf_codex"}, sessionCreds(f.URL)...)...)
	if exitcode.From(err) != exitcode.Conflict || !strings.Contains(err.Error(), "use a handoff") {
		t.Fatalf("err = %v, want the engine's conflict sentence", err)
	}
}

// Without an account `handoff` is the review step: it prints the generated summary
// and starts nothing.
func TestSessionHandoffWithoutAnAccountOnlyShowsTheSummary(t *testing.T) {
	f := moveEngine(t)
	f.handoffPreview = map[string]any{"summary": "The task: migrate the schema.", "source_driver": "claude", "turns": 4, "transcript_bytes": 2048}
	out, _, err := execSessionCLI(t, nil, append([]string{"session", "handoff", "demo"}, sessionCreds(f.URL)...)...)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"The task: migrate the schema.", "4 requests, 2048 bytes", "olivares session handoff demo --account <account>"} {
		if !strings.Contains(out, want) {
			t.Fatalf("output lacks %q:\n%s", want, out)
		}
	}
	if len(f.postsTo(sessionRunsPath+"/r1/handoff")) != 0 {
		t.Fatal("the review step started a session")
	}
}

func TestSessionHandoffStartsTheLinkedSessionWithTheReviewedSummary(t *testing.T) {
	f := moveEngine(t)
	file := filepath.Join(t.TempDir(), "summary.txt")
	if err := os.WriteFile(file, []byte("  Reviewed: continue the schema migration.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, _, err := execSessionCLI(t, nil, append([]string{"session", "handoff", "demo", "--account", "ppf_codex",
		"--summary-file", file, "--name", "demo-codex"}, sessionCreds(f.URL)...)...)
	if err != nil {
		t.Fatal(err)
	}
	body := f.postsTo(sessionRunsPath + "/r1/handoff")
	if len(body) != 1 || body[0]["provider_profile_ref"] != "ppf_codex" || body[0]["name"] != "demo-codex" ||
		body[0]["summary"] != "Reviewed: continue the schema migration." {
		t.Fatalf("handoff body = %v", body)
	}
	if !strings.Contains(out, "Started demo-codex on account ppf_codex from demo.") {
		t.Fatalf("output = %q", out)
	}
	if strings.Contains(out, "did not reach") {
		t.Fatalf("a delivered summary was reported as lost: %q", out)
	}
}

// A session that started without its first message is not a clean success: the
// output says what to do and the exit says so, and nothing is sent twice.
func TestSessionHandoffSaysWhenTheFirstTurnDidNotArrive(t *testing.T) {
	for state, want := range map[string]string{
		"not_delivered": "Its first message did not arrive. The text is in the -o json output (first_turn)",
		"unknown":       "Its first message may have arrived. Look at olivares session follow",
	} {
		f := moveEngine(t)
		f.handoffState = state
		out, _, err := execSessionCLI(t, nil, append([]string{"session", "handoff", "demo", "--account", "ppf_codex"}, sessionCreds(f.URL)...)...)
		if exitcode.From(err) != exitcode.Conflict {
			t.Fatalf("%s: err = %v, want a conflict exit", state, err)
		}
		if !strings.Contains(out, want) {
			t.Fatalf("%s: output = %q", state, out)
		}
		if body := f.postsTo(sessionRunsPath + "/r1/handoff"); len(body) != 1 || body[0]["summary"] != nil {
			t.Fatalf("%s: without a file the engine generates the summary: %v", state, body)
		}
	}
}

// A launch that waits for an approval is an ordinary governed state, not a failure,
// and it is not described as a first message that did not arrive.
func TestSessionHandoffWaitingForAnApprovalIsNotAFailure(t *testing.T) {
	f := moveEngine(t)
	f.handoffState = "waits_for_approval"
	out, _, err := execSessionCLI(t, nil, append([]string{"session", "handoff", "demo", "--account", "ppf_codex"}, sessionCreds(f.URL)...)...)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(out, "waits for an approval") || strings.Contains(out, "did not arrive") {
		t.Fatalf("output = %q", out)
	}
}

// The reviewed summary may come from standard input.
func TestSessionHandoffReadsTheSummaryFromStandardInput(t *testing.T) {
	f := moveEngine(t)
	_, _, err := execSessionCLI(t, strings.NewReader("  Reviewed on stdin.  \n"),
		append([]string{"session", "handoff", "demo", "--account", "ppf_codex", "--summary-file", "-"}, sessionCreds(f.URL)...)...)
	if err != nil {
		t.Fatal(err)
	}
	if body := f.postsTo(sessionRunsPath + "/r1/handoff"); len(body) != 1 || body[0]["summary"] != "Reviewed on stdin." {
		t.Fatalf("handoff body = %v", body)
	}
}

// A session with no conversation yet is moved honestly: nothing was carried, and
// the sentence says it starts again.
func TestSessionMoveOfASessionWithNoConversationSaysSo(t *testing.T) {
	f := moveEngine(t)
	f.runs = []map[string]any{{"run_ref": "r1", "name": "demo", "state": "stopped", "provider_driver": "claude", "provider_profile_ref": "ppf_a"}}
	out, _, err := execSessionCLI(t, nil, append([]string{"session", "move", "demo", "--account", "ppf_b"}, sessionCreds(f.URL)...)...)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "It has no conversation yet, so it starts again there.") {
		t.Fatalf("output = %q", out)
	}
}

func TestSessionHandoffRefusesAnEmptyOrOversizedSummaryFile(t *testing.T) {
	f := moveEngine(t)
	dir := t.TempDir()
	empty, big := filepath.Join(dir, "empty.txt"), filepath.Join(dir, "big.txt")
	if err := os.WriteFile(empty, []byte(" \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(big, []byte(strings.Repeat("x", handoffSummaryFileMax+1)), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, file := range map[string]string{"empty": empty, "oversized": big, "missing": filepath.Join(dir, "none.txt")} {
		_, _, err := execSessionCLI(t, nil, append([]string{"session", "handoff", "demo", "--account", "ppf_codex", "--summary-file", file}, sessionCreds(f.URL)...)...)
		if exitcode.From(err) != exitcode.Usage {
			t.Fatalf("%s summary file: err = %v, want a usage error", name, err)
		}
	}
	if len(f.postsTo(sessionRunsPath+"/r1/handoff")) != 0 {
		t.Fatal("a refused summary file still started a session")
	}
}
