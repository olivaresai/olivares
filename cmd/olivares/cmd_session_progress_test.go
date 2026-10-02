// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"strings"
	"testing"
)

// TestSessionViewKeepsALongToolCallReadable: while Claude Code runs a tool it reports
// `tool_progress` every few seconds, and an interrupt is answered with `control_response`.
// `session follow` and `send` printed a line for each, 30 a minute. The tool call is shown
// once; its progress and the protocol answer stay quiet, as in the console; the task's end
// is one line. Frames shaped like Claude Code's (FH's stub of it, "long turn").
func TestSessionViewKeepsALongToolCallReadable(t *testing.T) {
	var b strings.Builder
	v := newSessionView(&b)
	v.driver = "claude"
	frames := []string{
		`{"type":"assistant","session_id":"s1","message":{"role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"Bash","input":{"command":"python3 -c 'import time; time.sleep(60)'","description":"Run the long command"}}]}}`,
		`{"type":"system","subtype":"task_started","task_id":"b1","tool_use_id":"toolu_1","session_id":"s1"}`,
		`{"type":"tool_progress","tool_use_id":"toolu_1","tool_name":"Bash","parent_tool_use_id":null,"elapsed_time_seconds":2,"task_id":"b1","session_id":"s1"}`,
		`{"type":"tool_progress","tool_use_id":"toolu_1","tool_name":"Bash","parent_tool_use_id":null,"elapsed_time_seconds":4,"task_id":"b1","session_id":"s1"}`,
		`{"type":"tool_progress","tool_use_id":"toolu_9","tool_name":"Grep","parent_tool_use_id":"toolu_0","elapsed_time_seconds":2,"session_id":"s1"}`,
		`{"type":"tool_progress","tool_use_id":"toolu_9","tool_name":"Grep","parent_tool_use_id":"toolu_0","elapsed_time_seconds":4,"session_id":"s1"}`,
		`{"type":"control_response","response":{"subtype":"success","request_id":"r1"}}`,
		`{"type":"system","subtype":"task_notification","task_id":"b1","status":"killed","session_id":"s1"}`,
		`{"type":"user","session_id":"s1","message":{"role":"user","content":[{"type":"text","text":"[Request interrupted by user for tool use]"}]}}`,
	}
	for _, f := range frames {
		if v.render(f) {
			t.Fatalf("%s ended the turn", f)
		}
	}
	if !v.render(`{"type":"result","subtype":"success","is_error":false,"session_id":"s1","result":"interrupted","num_turns":1,"duration_ms":4100,"total_cost_usd":0}`) {
		t.Fatal("the result frame must end the turn")
	}
	want := "→ Bash python3 -c 'import time; time.sleep(60)'\n· Grep is running\n· task killed\n— turn finished · 4.1s\n"
	if got := b.String(); got != want {
		t.Fatalf("view =\n%s\nwant\n%s", got, want)
	}
}
