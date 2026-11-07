// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"
)

// attachHistory attaches to the fixture's run as the console does on a fresh view
// and returns the line of each history event, in order.
func attachHistory(t *testing.T, f *moveFixture) []string {
	t.Helper()
	res := f.h.do(http.MethodGet, "/v1/m/sessions/runs/"+f.runRef+"/attach?from=0&history=1", f.admin, f.hdr)
	if res.code != http.StatusOK {
		t.Fatalf("attach status=%d %s", res.code, res.raw)
	}
	var lines []string
	for _, event := range strings.Split(res.raw, "event: history\ndata: ")[1:] {
		data, _, _ := strings.Cut(event, "\n")
		var history struct {
			Line string `json:"line"`
		}
		if err := json.Unmarshal([]byte(data), &history); err != nil || history.Line == "" {
			t.Fatalf("history event %q: %v", data, err)
		}
		lines = append(lines, history.Line)
	}
	return lines
}

// After an engine restart or upgrade the engine holds no output of an OpenCode
// session: the view is rebuilt from OpenCode's own export, as the updates its own
// session/load replay sends, each prompt and answer once and in order.
func TestAttachHTTPOpenCodeSessionShowsItsStoredConversationAfterARestart(t *testing.T) {
	f, runner := openCodeExportFixture(t, 0)
	f.stop(t)
	f.m.rt.dropLive(f.tenant, f.runRef) // what an engine restart leaves of the run

	var updates []string
	for _, line := range attachHistory(t, f) {
		var frame struct {
			Method string `json:"method"`
			Params struct {
				SessionID string          `json:"sessionId"`
				Update    json.RawMessage `json:"update"`
			} `json:"params"`
		}
		if err := json.Unmarshal([]byte(line), &frame); err != nil || frame.Method != "session/update" || frame.Params.SessionID != openCodeTestSID {
			t.Fatalf("history line %q is not an update of the session (%v)", line, err)
		}
		updates = append(updates, string(frame.Params.Update))
	}
	want := []string{
		`{"sessionUpdate":"user_message_chunk","messageId":"msg_142b13f6e001D2aNwL1kMIzDzy","content":{"type":"text","text":"please list the directory"}}`,
		`{"sessionUpdate":"tool_call_update","toolCallId":"call_1","title":"ls","status":"completed","rawInput":{"command":"ls","description":"list files"},"content":[{"type":"content","content":{"type":"text","text":"(no output)"}}]}`,
		`{"sessionUpdate":"agent_message_chunk","messageId":"msg_142b14285001EnsirBLXUg933k","content":{"type":"text","text":"ok, done"}}`,
	}
	if !slices.Equal(updates, want) {
		t.Fatalf("the view after a restart shows\n%s\nwant\n%s", strings.Join(updates, "\n"), strings.Join(want, "\n"))
	}
	// Read the way the handoff reads it: OpenCode's own export, without its plugins, offline.
	if len(runner.specs) != 1 {
		t.Fatalf("export launches = %d, want 1", len(runner.specs))
	}
	if spec := runner.specs[0]; !slices.Equal(spec.Args, []string{"--pure", "export", openCodeTestSID}) || spec.NetworkPolicy == nil || !spec.NetworkPolicy.Offline {
		t.Fatalf("export launch = %+v", spec)
	}
}
