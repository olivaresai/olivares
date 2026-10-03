// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"os"
	"strings"
	"testing"
)

// openCodeRecordedFollow is `olivares session follow <name> -o json` on an OpenCode
// session running a local Ollama model, recorded by HU2 through the product on the
// candidate (2026-10-02, HU2/captures/B1-cli/09-follow-json.txt), unchanged: the ACP
// handshake, two session/prompt results and the second turn's reply as
// agent_message_chunk frames.
const openCodeRecordedFollow = "testdata/opencode-follow-recorded.ndjson"

func openCodeRecordedLines(t *testing.T) []string {
	t.Helper()
	raw, err := os.ReadFile(openCodeRecordedFollow)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSpace(string(raw)), "\n")
}

// TestSessionViewRendersARecordedOpenCodeTurn is HU2-01: on the no-account path
// (OpenCode + Ollama, and Grok Build) `session follow` printed only "· session/update"
// and the person never saw the answer. The reply is the joined chunks, each turn ends
// with its footer, and no protocol is shown.
func TestSessionViewRendersARecordedOpenCodeTurn(t *testing.T) {
	var b strings.Builder
	v := newSessionView(&b)
	v.driver = "opencode"
	ends := 0
	for _, line := range openCodeRecordedLines(t) {
		if v.render(line) {
			ends++
		}
	}
	want := "— turn finished · 2103 tokens\nHello, how are you today?\n— turn finished · 2058 tokens\n"
	if got := b.String(); ends != 2 || got != want {
		t.Fatalf("turn ends = %d, view =\n%s\nwant\n%s", ends, got, want)
	}
}

// The other ACP shapes OpenCode and Grok Build send: the person's own message (on a
// replayed conversation), a tool call one line, a permission request, a failed tool
// call's error, quiet reasoning and bookkeeping, an unknown update kept as one quiet
// line, an interrupted turn, and a prompt answered with an error.
func TestSessionViewShowsACPToolCallsAndThePersonsMessage(t *testing.T) {
	var b strings.Builder
	v := newSessionView(&b)
	v.driver = "opencode"
	upd := func(u string) string {
		return `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"ses_1","update":` + u + `}}`
	}
	for _, line := range []string{
		upd(`{"sessionUpdate":"user_message_chunk","messageId":"u1","content":{"type":"text","text":"list the "}}`),
		upd(`{"sessionUpdate":"user_message_chunk","messageId":"u1","content":{"type":"text","text":"files"}}`),
		upd(`{"sessionUpdate":"agent_thought_chunk","content":{"type":"text","text":"the person wants ls"}}`),
		upd(`{"sessionUpdate":"agent_message_chunk","messageId":"m1","content":{"type":"text","text":"I'll look."}}`),
		upd(`{"sessionUpdate":"tool_call","toolCallId":"call_1","title":"ls -la","kind":"execute","status":"pending","rawInput":{"command":"ls -la"}}`),
		upd(`{"sessionUpdate":"tool_call_update","toolCallId":"call_1","status":"completed"}`),
		`{"jsonrpc":"2.0","id":5,"method":"session/request_permission","params":{"sessionId":"ses_1","toolCall":{"toolCallId":"call_2","title":"rm -rf build"},"options":[{"optionId":"allow","name":"Allow","kind":"allow_once"}]}}`,
		upd(`{"sessionUpdate":"tool_call_update","toolCallId":"call_2","status":"failed","content":[{"type":"content","content":{"type":"text","text":"permission denied"}}]}`),
		upd(`{"sessionUpdate":"plan","entries":[{"content":"list","priority":"high","status":"completed"}]}`),
		upd(`{"sessionUpdate":"future_update"}`),
		upd(`{"sessionUpdate":"agent_message_chunk","messageId":"m2","content":{"type":"text","text":"Done."}}`),
	} {
		if v.render(line) {
			t.Fatalf("%s ended the turn", line)
		}
	}
	if !v.render(`{"jsonrpc":"2.0","id":6,"result":{"stopReason":"cancelled"}}`) || v.failed {
		t.Fatal("a cancelled prompt result must end the turn, not as a failure")
	}
	v.driver = "grok"
	if !v.render(`{"jsonrpc":"2.0","id":7,"error":{"code":-32603,"message":"model unavailable"}}`) || !v.failed {
		t.Fatal("a prompt answered with an error must end the turn as a failure")
	}
	want := "› list the files\nI'll look.\n→ ls -la\n! OpenCode is waiting for approval to run rm -rf build\n" +
		"  ✗ permission denied\n· future update\nDone.\n— turn interrupted\n✗ model unavailable\n— turn failed\n"
	if got := b.String(); got != want {
		t.Fatalf("view =\n%s\nwant\n%s", got, want)
	}
}

// TestSessionSendFollowsAnOpenCodeTurn: `session send` to an OpenCode session shows
// the reply until the prompt's result, as it does for Claude and Codex, instead of
// "See the reply: olivares session follow".
func TestSessionSendFollowsAnOpenCodeTurn(t *testing.T) {
	lines := openCodeRecordedLines(t)
	f := newFakeSessionEngine(t)
	f.runs = []map[string]any{{"run_ref": "r1", "name": "hello", "state": "running", "provider_driver": "opencode", "transport": "acp"}}
	f.replay, f.reply = lines[:4], lines[4:]
	out, errb, err := execSessionCLI(t, nil, append([]string{"session", "send", "hello", "Hello?"}, sessionCreds(f.URL)...)...)
	if err != nil {
		t.Fatalf("send: %v\n%s", err, errb)
	}
	if input := f.postsTo(sessionRunsPath + "/r1/input"); len(input) != 1 || input[0]["text"] != "Hello?" {
		t.Fatalf("input body = %v, want the text", input)
	}
	if out != "Hello, how are you today?\n— turn finished · 2058 tokens\n" {
		t.Fatalf("send shows\n%s\nwant the reply and its turn's end", out)
	}
}

// SR2C on cfa80204: a send whose stream ends before the turn's footer shows the reply
// it received and still fails as an unfinished turn.
func TestSessionSendShowsTheReceivedACPReplyWhenTheStreamEndsEarly(t *testing.T) {
	lines := openCodeRecordedLines(t)
	f := newFakeSessionEngine(t)
	f.runs = []map[string]any{{"run_ref": "r1", "name": "hello", "state": "running", "provider_driver": "opencode", "transport": "acp"}}
	f.replay, f.reply = lines[:4], lines[4:len(lines)-1]
	f.endAfterInput = true
	out, _, err := execSessionCLI(t, nil, append([]string{"session", "send", "hello", "Hello?"}, sessionCreds(f.URL)...)...)
	if err == nil || !strings.Contains(err.Error(), "ended before the turn finished") {
		t.Fatalf("send = %v, want the unfinished-turn failure", err)
	}
	if out != "Hello, how are you today?\n" {
		t.Fatalf("send shows %q, want the reply it received", out)
	}
}
