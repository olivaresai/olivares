// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build unix

package sessions

import (
	"context"
	"os"
	"strings"
	"testing"
)

// A log with a failed turn, an updated message and a tool call that gained its result
// reads as the conversation the tool itself holds.
func TestReadGeminiConversation_FollowsTheLog(t *testing.T) {
	const log = `{"sessionId":"s","kind":"main"}
{"id":"ctx","type":"user","content":[{"text":"<session_context>\nThis is the Gemini CLI.\n</session_context>"}]}
{"$set":{"lastUpdated":"t"}}
{"id":"u1","type":"user","content":[{"text":"first question"}]}
{"id":"g1","type":"gemini","content":"first answer"}
{"id":"u2","type":"user","content":[{"text":"the turn that failed"}]}
{"$rewindTo":"u2"}
{"id":"u2b","type":"user","content":"a plain string request"}
{"id":"g2","type":"gemini","content":"","toolCalls":[{"name":"run_shell_command","args":{"command":"ls"}}]}
{"id":"g2","type":"gemini","content":"","toolCalls":[{"name":"run_shell_command","args":{"command":"ls"},"result":[{"functionResponse":{"response":{"error":"denied"}}}]}]}
{"id":"f2","type":"user","content":[{"functionResponse":{"response":{"output":"repeats the result"}}}]}
{"id":"i1","type":"info","content":"Request cancelled."}
not json
{"id":"g3","type":"gemini","content":"last answer"}
{"id":"g4","type":"gemini","content":"being wri`
	conv, err := readGeminiConversation(context.Background(), strings.NewReader(log), int64(len(log)))
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, turn := range conv.turns {
		got = append(got, turn.role+": "+turn.text)
	}
	want := []string{
		"user: first question", "assistant: first answer", "user: a plain string request",
		`tool: run_shell_command {"command":"ls"}`, "result: denied", "assistant: last answer",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("turns:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if conv.unreadable != 2 {
		t.Fatalf("unreadable = %d, want the line that is not JSON and the record cut off at the end", conv.unreadable)
	}
}

func TestReadGeminiConversation_RealChat(t *testing.T) {
	data, err := os.ReadFile(geminiTestChat)
	if err != nil {
		t.Fatal(err)
	}
	conv, err := readGeminiConversation(context.Background(), strings.NewReader(string(data)), int64(len(data)))
	if err != nil || conv.unreadable != 0 {
		t.Fatalf("real chat: %v unreadable=%d", err, conv.unreadable)
	}
	var got []string
	for _, turn := range conv.turns {
		got = append(got, turn.role)
	}
	if strings.Join(got, ",") != "user,tool,result,assistant" || conv.turns[0].text != "please list the directory" {
		t.Fatalf("real chat turns = %+v", conv.turns)
	}
}

func TestReadOpenCodeExport_RealExport(t *testing.T) {
	data, err := os.ReadFile(openCodeTestFile)
	if err != nil {
		t.Fatal(err)
	}
	conv, err := readOpenCodeExport(data)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, turn := range conv.turns {
		got = append(got, turn.role)
	}
	if strings.Join(got, ",") != "user,tool,result,assistant" {
		t.Fatalf("real export turns = %+v", conv.turns)
	}
	if _, err := readOpenCodeExport([]byte("not json")); err == nil {
		t.Fatal("an export that is not JSON was read")
	}
}

// A rewind in the middle of the log drops the message and every message after it, and
// an id the log does not hold drops nothing.
func TestReadGeminiConversation_RewindInTheMiddle(t *testing.T) {
	const log = `{"sessionId":"s","kind":"main"}
{"id":"u1","type":"user","content":[{"text":"one"}]}
{"id":"g1","type":"gemini","content":"answer one"}
{"id":"u2","type":"user","content":[{"text":"two"}]}
{"id":"g2","type":"gemini","content":"answer two"}
{"$rewindTo":"u2"}
{"id":"u3","type":"user","content":[{"text":"three"}]}
{"$rewindTo":"not-in-the-log"}
{"id":"g1","type":"gemini","content":"answer one, revised"}
`
	conv, err := readGeminiConversation(context.Background(), strings.NewReader(log), int64(len(log)))
	if err != nil || conv.unreadable != 0 {
		t.Fatalf("read: %v unreadable=%d", err, conv.unreadable)
	}
	var got []string
	for _, turn := range conv.turns {
		got = append(got, turn.role+": "+turn.text)
	}
	want := "user: one\nassistant: answer one, revised\nuser: three"
	if strings.Join(got, "\n") != want {
		t.Fatalf("turns:\n%s\nwant:\n%s", strings.Join(got, "\n"), want)
	}
}

// What an OpenCode export holds that is not the conversation is not carried, a failed
// tool call carries its error, and an answer that is not an export is refused.
func TestReadOpenCodeExport_CarriesTheConversationOnly(t *testing.T) {
	const export = `{"info":{},"messages":[
 {"info":{"role":"user"},"parts":[
   {"type":"text","text":"build it"},
   {"type":"text","text":"injected by the tool","synthetic":true},
   {"type":"text","text":"ignored by the tool","ignored":true}]},
 {"info":{"role":"assistant"},"parts":[
   {"type":"reasoning","text":"thinking"},
   {"type":"tool","tool":"bash","state":{"status":"error","input":{"command":"make"},"error":"exit 2"}},
   {"type":"text","text":"it failed"}]},
 {"info":{"role":"system"},"parts":[{"type":"text","text":"system words"}]}]}`
	conv, err := readOpenCodeExport([]byte(export))
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, turn := range conv.turns {
		got = append(got, turn.role+": "+turn.text)
	}
	want := "user: build it\ntool: bash {\"command\":\"make\"}\nresult: exit 2\nassistant: it failed"
	if strings.Join(got, "\n") != want {
		t.Fatalf("turns:\n%s\nwant:\n%s", strings.Join(got, "\n"), want)
	}
	if _, err := readOpenCodeExport([]byte(`{"messages":[]}`)); err != nil {
		t.Fatalf("a session with no messages is an export: %v", err)
	}
	for _, notAnExport := range []string{`{}`, `{"error":"no such session"}`, `[]`} {
		if _, err := readOpenCodeExport([]byte(notAnExport)); err == nil {
			t.Fatalf("%s was read as a conversation", notAnExport)
		}
	}
}

func TestReadCodexConversationFile_CarriesCustomToolsAndRefusesAnotherThread(t *testing.T) {
	const sid = "5b0c1a6e-1845-4a2b-9c3d-000000000001"
	rollout := `{"type":"session_meta","payload":{"id":"` + sid + `","source":"cli"}}
{"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"patch it"}]}}
{"type":"response_item","payload":{"type":"custom_tool_call","name":"apply_patch","call_id":"p1","input":"*** Begin Patch\n*** End Patch"}}
{"type":"response_item","payload":{"type":"custom_tool_call_output","call_id":"p1","output":"Success. Updated the following files:\nM a.txt"}}
{"type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"patched"}]}}
{"type":"event_msg","payload":{"type":"agent_message","message":"repeats the answer"}}
`
	conv, err := readCodexConversationFile(context.Background(), strings.NewReader(rollout), sid, int64(len(rollout)))
	if err != nil || conv.unreadable != 0 {
		t.Fatalf("read: %v unreadable=%d", err, conv.unreadable)
	}
	var got []string
	for _, turn := range conv.turns {
		got = append(got, turn.role)
	}
	if strings.Join(got, ",") != "user,tool,result,assistant" || !strings.HasPrefix(conv.turns[1].text, "apply_patch ") ||
		!strings.Contains(conv.turns[2].text, "M a.txt") {
		t.Fatalf("turns = %+v", conv.turns)
	}
	if _, err := readCodexConversationFile(context.Background(), strings.NewReader(rollout), "another-thread", int64(len(rollout))); err == nil {
		t.Fatal("the rollout of another thread was read")
	}
}
