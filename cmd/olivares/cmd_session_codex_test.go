// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
)

// codexRecordedFollow is `olivares session follow <name> -o json` on a Codex session,
// recorded through the product: engine, Codex driver and the frames it streams, with
// FH's codex stub as the peer (no real OpenAI account exists in this environment).
// Two turns, recorded 2026-10-01T15:02Z on R1 refresh 02 (5cb0943a). Only the home
// path and the stub's account email were replaced.
const codexRecordedFollow = "testdata/codex-follow-recorded.ndjson"

func codexRecordedLines(t *testing.T) []string {
	t.Helper()
	raw, err := os.ReadFile(codexRecordedFollow)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSpace(string(raw)), "\n")
}

// TestSessionViewRendersARecordedCodexTurn is HU 016: `session follow` printed a Codex
// session's protocol frames raw. It must read like a Claude session: the reply text,
// the turn's end, and none of the driver's handshake.
func TestSessionViewRendersARecordedCodexTurn(t *testing.T) {
	lines := codexRecordedLines(t)
	var b strings.Builder
	v := newSessionView(&b)
	v.driver = "codex"
	ends := 0
	for _, line := range lines {
		if v.render(line) {
			ends++
		}
	}
	out := b.String()
	if ends != 2 || strings.Count(out, "— turn finished\n") != 2 {
		t.Fatalf("turn ends = %d, want the 2 recorded turns:\n%s", ends, out)
	}
	for _, want := range codexRecordedReplies(t, lines) {
		if !strings.Contains(out, want+"\n") {
			t.Fatalf("the reply %q is not shown:\n%s", want, out)
		}
	}
	for _, raw := range []string{"{", "item/completed", "turn/completed", "codexHome", "thread/"} {
		if strings.Contains(out, raw) {
			t.Fatalf("follow shows protocol (%q):\n%s", raw, out)
		}
	}
}

// codexRecordedReplies reads the agentMessage texts out of the recording, so the test
// cannot pass on a recording with no reply in it.
func codexRecordedReplies(t *testing.T, lines []string) []string {
	t.Helper()
	var texts []string
	for _, line := range lines {
		var frame map[string]any
		if json.Unmarshal([]byte(line), &frame) != nil || str(frame, "method") != "item/completed" {
			continue
		}
		params, _ := frame["params"].(map[string]any)
		item, _ := params["item"].(map[string]any)
		if str(item, "type") == "agentMessage" {
			texts = append(texts, strings.TrimSpace(str(item, "text")))
		}
	}
	if len(texts) != 2 {
		t.Fatalf("the recording has %d replies, want 2", len(texts))
	}
	return texts
}

// TestSessionViewShowsCodexToolCallsOneLineEach: the item shapes of the Codex 0.153.4
// schema (ThreadItem) that are tool calls, one line each, as Claude's tool_use is.
func TestSessionViewShowsCodexToolCallsOneLineEach(t *testing.T) {
	var b strings.Builder
	v := newSessionView(&b)
	v.driver = "codex"
	for _, line := range []string{
		`{"method":"item/started","params":{"item":{"type":"commandExecution","id":"c1","command":"ls -la"}}}`,
		`{"method":"item/commandExecution/outputDelta","params":{"delta":"README\n"}}`,
		`{"method":"item/completed","params":{"item":{"type":"commandExecution","id":"c1","command":"ls -la","exitCode":0}}}`,
		`{"method":"item/completed","params":{"item":{"type":"commandExecution","id":"c2","command":"go test ./...","exitCode":1}}}`,
		`{"method":"item/completed","params":{"item":{"type":"fileChange","id":"f1","changes":[{"path":"todo.txt","kind":"update"}]}}}`,
		`{"method":"item/completed","params":{"item":{"type":"mcpToolCall","id":"m1","server":"files","tool":"read_file","arguments":{"path":"todo.txt"}}}}`,
		`{"method":"item/completed","params":{"item":{"type":"webSearch","id":"w1","query":"golang slices"}}}`,
		`{"method":"item/completed","params":{"item":{"type":"reasoning","id":"r1","summary":[]}}}`,
		`{"id":7,"method":"item/commandExecution/requestApproval","params":{"command":"rm -rf build"}}`,
		`{"method":"serverRequest/resolved","params":{"requestId":7}}`,
		`{"method":"item/reasoning/summaryPartAdded","params":{}}`,
		`{"method":"thread/tokenUsage/updated","params":{}}`,
		`{"method":"error","params":{"error":{"message":"stream disconnected"},"willRetry":true}}`,
		`{"method":"warning","params":{"message":"AGENTS.md is larger than the limit; it was cut"}}`,
		`{"method":"item/completed","params":{"item":{"type":"agentMessage","id":"a1","text":"Added eggs."}}}`,
	} {
		if v.render(line) {
			t.Fatalf("%s ended the turn", line)
		}
	}
	v.render(`{"method":"error","params":{"error":{"message":"usage limit reached"},"willRetry":false}}`)
	if !v.render(`{"method":"turn/completed","params":{"turn":{"id":"t1","status":"failed","error":{"message":"usage limit reached"}}}}`) || !v.failed {
		t.Fatal("a failed turn/completed must end the turn as a failure")
	}
	want := "→ command ls -la\n→ command go test ./...\n  ✗ exit 1\n→ edit todo.txt\n→ files.read_file todo.txt\n" +
		"→ web search golang slices\n! Codex is waiting for approval to run rm -rf build\n! stream disconnected (retrying)\n" +
		"! AGENTS.md is larger than the limit; it was cut\nAdded eggs.\nusage limit reached\n— turn failed\n"
	if got := b.String(); got != want {
		t.Fatalf("view =\n%s\nwant\n%s", got, want)
	}
}

// TestSessionViewKeepsCodexStartupQuiet: the first frames the real Codex 0.153.4
// app-server wrote (an internal design note (not shipped)), with the home,
// the server name and the installation id replaced. The handshake response and the
// remote-control status are quiet; the configuration warning is the operator's.
func TestSessionViewKeepsCodexStartupQuiet(t *testing.T) {
	var b strings.Builder
	v := newSessionView(&b)
	v.driver = "codex"
	for _, line := range []string{
		`{"id":1,"result":{"userAgent":"olivares_local_contract_probe/0.153.4 (Debian 12.0.0; x86_64) dumb (olivares_local_contract_probe; 0.0.0)","codexHome":"/home/ana/.codex","platformFamily":"unix","platformOs":"linux"}}`,
		`{"method":"configWarning","params":{"summary":"Codex could not find bubblewrap on PATH. Install bubblewrap with your OS package manager. See the sandbox prerequisites: https://developers.openai.com/codex/concepts/sandboxing#prerequisites. Codex will use the bundled bubblewrap in the meantime.","details":null},"emittedAtMs":1788618849219}`,
		`{"method":"remoteControl/status/changed","params":{"status":"disabled","serverName":"workstation","installationId":"00000000-0000-0000-0000-000000000000","environmentId":null},"emittedAtMs":1788618849219}`,
		`{"id":2,"error":{"code":-32601,"message":"thread/start is not available"}}`,
	} {
		v.render(line)
	}
	want := "! Codex could not find bubblewrap on PATH. Install bubblewrap with your OS package manager. See the sandbox " +
		"prerequisites: https://developers.openai.com/codex/concepts/sandboxing#prerequisites. Codex will use the bundled " +
		"bubblewrap in the meantime.\n✗ thread/start is not available\n"
	if got := b.String(); got != want {
		t.Fatalf("view =\n%q\nwant\n%q", got, want)
	}
}

// TestSessionSendFollowsACodexTurn: `session send` to a Codex session shows the reply
// until turn/completed, as it does for Claude, and sends the text the driver takes.
func TestSessionSendFollowsACodexTurn(t *testing.T) {
	lines := codexRecordedLines(t)
	last := 0
	for i, line := range lines[:len(lines)-1] {
		if strings.Contains(line, `"turn/completed"`) {
			last = i + 1
		}
	}
	f := newFakeSessionEngine(t)
	f.runs = []map[string]any{{"run_ref": "r1", "name": "notes", "state": "running", "provider_driver": "codex", "transport": "app-server"}}
	f.replay, f.reply = lines[:last], lines[last:]
	out, errb, err := execSessionCLI(t, nil, append([]string{"session", "send", "notes", "now list the files"}, sessionCreds(f.URL)...)...)
	if err != nil {
		t.Fatalf("send: %v\n%s", err, errb)
	}
	if input := f.postsTo(sessionRunsPath + "/r1/input"); len(input) != 1 || input[0]["text"] != "now list the files" {
		t.Fatalf("input body = %v, want the text", input)
	}
	replies := codexRecordedReplies(t, lines)
	if !strings.Contains(out, replies[1]) || strings.Count(out, "— turn finished") != 1 || strings.Contains(out, replies[0]) {
		t.Fatalf("send shows\n%s\nwant only the second turn's reply %q and its end", out, replies[1])
	}
}

// TestSessionInterruptNamesWhatWorks: Codex and Claude Code turns can be interrupted
// (Claude through its stream-json control request). Only a run the engine says has no
// interruption (a relayed, remote-control run) is told to stop instead, and that
// sentence names the session, not the tool. Claude's refusal (409) is its own
// sentence; no answer in time (504) is reported as unknown (exit 8), never as done.
func TestSessionInterruptNamesWhatWorks(t *testing.T) {
	f := newFakeSessionEngine(t)
	f.runs = []map[string]any{
		{"run_ref": "r1", "name": "notes", "state": "running", "provider_driver": "codex"},
		{"run_ref": "r2", "name": "docs", "state": "running", "provider_driver": "claude", "transport": "stream-json"},
		{"run_ref": "r3", "name": "web", "state": "running", "provider_driver": "claude", "transport": "remote-control"},
		{"run_ref": "r4", "name": "api", "state": "running", "provider_driver": "claude", "transport": "stream-json", "interrupt": "refuse"},
		{"run_ref": "r5", "name": "jobs", "state": "running", "provider_driver": "claude", "transport": "stream-json", "interrupt": "silent"},
	}
	interrupt := func(name string) (string, error) {
		out, _, err := execSessionCLI(t, nil, append([]string{"session", "interrupt", name}, sessionCreds(f.URL)...)...)
		return strings.TrimSpace(out), err
	}
	for _, name := range []string{"notes", "docs"} {
		if out, err := interrupt(name); err != nil || out != "Interrupted the current turn of "+name+"." {
			t.Fatalf("%s: out=%q err=%v", name, out, err)
		}
	}
	for _, tc := range []struct {
		name, want string
		code       int
	}{
		{"web", "Session web cannot interrupt a turn. Stop it instead: olivares session stop web", exitcode.Conflict},
		{"api", "Claude Code refused the interrupt: a tool call is running. The turn goes on; to end it: olivares session stop api",
			exitcode.Conflict},
		{"jobs", "Claude Code did not confirm the interrupt in time; the turn may still be running. See it: olivares session " +
			"follow jobs; to end it: olivares session stop jobs", exitcode.Indeterminate},
	} {
		_, err := interrupt(tc.name)
		if exitcode.From(err) != tc.code || err == nil || err.Error() != tc.want {
			t.Fatalf("%s: err = %v (exit %d), want %q (exit %d)", tc.name, err, exitcode.From(err), tc.want, tc.code)
		}
	}
}

// TestSessionInterruptWithNoTurnRunningIsNotAFailure is J7 on refresh 03b: interrupting
// an idle Codex session printed the engine's 409 "no active provider turn" raw (exit 5).
// Nothing is running, so there is nothing to interrupt: one line, exit 0.
func TestSessionInterruptWithNoTurnRunningIsNotAFailure(t *testing.T) {
	f := newFakeSessionEngine(t)
	f.runs = []map[string]any{{"run_ref": "r1", "name": "notes", "state": "idle", "provider_driver": "codex"}}
	out, errb, err := execSessionCLI(t, nil, append([]string{"session", "interrupt", "notes"}, sessionCreds(f.URL)...)...)
	if err != nil || strings.TrimSpace(out) != "No turn is running in notes; nothing to interrupt." {
		t.Fatalf("out=%q err=%v stderr=%q", out, err, errb)
	}
}
