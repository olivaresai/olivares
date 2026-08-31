// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
)

// Family tests for `olivares claude-agents`.

func TestClaudeAgentsVerbsReachTheRoutesTheEngineRegisters(t *testing.T) {
	for _, tc := range []struct {
		argv       []string
		wantMethod string
		wantPath   string
	}{
		{[]string{"claude-agents", "sessions", "events", "sess-1"},
			"GET", "/v1/m/claude-agents/sessions/sess-1/events"},
		{[]string{"claude-agents", "sessions", "tool-confirmation", "sess-1",
			"--tool-use-id", "tu-1", "--result", "allow"},
			"POST", "/v1/m/claude-agents/sessions/sess-1/tool-confirmation"},
	} {
		t.Run(strings.Join(tc.argv[:3], "-"), func(t *testing.T) {
			srv := newLot3Server(t, lot3OK(`{"items":[],"has_more":false,"ok":true}`))
			if _, _, err := execRoot(t, lot3Args(srv.URL, tc.argv...)...); err != nil {
				t.Fatalf("verb failed: %v", err)
			}
			if got, _ := srv.method.Load().(string); got != tc.wantMethod {
				t.Errorf("method = %s, want %s", got, tc.wantMethod)
			}
			if got := srv.lastPath(); got != tc.wantPath {
				t.Errorf("path = %s, want %s", got, tc.wantPath)
			}
		})
	}
}

// TestClaudeAgentsEmptyAndUnknownAreDifferentAnswers is the whole point of this
// family's read. "No source is wired" is a fact; "the source could not answer" is
// an outage, and reading the second as the first tells a reviewer the agent did
// nothing when nobody knows what it did.
func TestClaudeAgentsEmptyAndUnknownAreDifferentAnswers(t *testing.T) {
	// EMPTY: exit 0, and the note says WHY it might be empty.
	empty := newLot3Server(t, lot3OK(`{"items":[],"has_more":false}`))
	out, _, err := execRoot(t, lot3Args(empty.URL, "claude-agents", "sessions", "events", "sess-1")...)
	if err != nil {
		t.Fatalf("an honest empty answer must exit 0, got %v", err)
	}
	if !strings.Contains(out, "no source is wired") {
		t.Errorf("the empty note must explain the two ways it can be empty, got: %q", out)
	}

	// UNKNOWN: the engine's 502 must NOT become an empty list.
	broken := newLot3Server(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = io.WriteString(w,
			`{"error":{"message":"the thread-event source could not answer — events are UNKNOWN right now, not absent"}}`)
	})
	bout, _, berr := execRoot(t, lot3Args(broken.URL, "claude-agents", "sessions", "events", "sess-1")...)
	if berr == nil {
		t.Fatal("an upstream outage must not exit 0")
	}
	if got := exitcode.From(berr); got != exitcode.Server {
		t.Fatalf("exit = %d, want %d (server)", got, exitcode.Server)
	}
	if strings.TrimSpace(bout) != "" {
		t.Errorf("an outage must print no events at all, got:\n%s", bout)
	}
	if !strings.Contains(berr.Error(), "UNKNOWN") {
		t.Errorf("the engine's distinction must survive, got: %v", berr)
	}
}

// TestClaudeAgentsEventsRenderTheirRows: the control that the empty test above is
// not passing because the renderer is broken.
func TestClaudeAgentsEventsRenderTheirRows(t *testing.T) {
	srv := newLot3Server(t, lot3OK(`{"items":[{"id":"e1","type":"tool_use","agent_ref":"agent-a",
		"tool_name":"Bash","tool_use_id":"tu-1","created_at":"2026-08-16T10:00:00Z"}],"has_more":false}`))
	out, _, err := execRoot(t, lot3Args(srv.URL, "claude-agents", "sessions", "events", "sess-1")...)
	if err != nil {
		t.Fatalf("the read must succeed, got %v", err)
	}
	for _, want := range []string{"tool_use", "Bash", "tu-1"} {
		if !strings.Contains(out, want) {
			t.Errorf("the event row is missing %q:\n%s", want, out)
		}
	}
}

// TestClaudeAgentsToolConfirmationRejectsAContradictoryInvocation. A deny message
// on an ALLOW records an explanation for a refusal that never happened; dropping
// half the invocation silently is the alternative, and it is worse.
func TestClaudeAgentsToolConfirmationRejectsAContradictoryInvocation(t *testing.T) {
	srv := newLot3Server(t, lot3OK(`{"ok":true}`))
	for _, bad := range [][]string{
		{"claude-agents", "sessions", "tool-confirmation", "sess-1", "--tool-use-id", "tu-1", "--result", "maybe"},
		{"claude-agents", "sessions", "tool-confirmation", "sess-1", "--tool-use-id", "tu-1",
			"--result", "allow", "--deny-message", "because"},
	} {
		_, _, err := execRoot(t, lot3Args(srv.URL, bad...)...)
		if err == nil || exitcode.From(err) != exitcode.Usage {
			t.Fatalf("%v must exit %d, got %v", bad, exitcode.Usage, err)
		}
	}
	if n := srv.calls.Load(); n != 0 {
		t.Fatalf("%d contradictory confirmation(s) were sent", n)
	}

	// THE CONTROL: both coherent decisions travel, with exactly what was typed.
	if _, _, err := execRoot(t, lot3Args(srv.URL, "claude-agents", "sessions", "tool-confirmation",
		"sess-1", "--tool-use-id", "tu-1", "--result", "allow")...); err != nil {
		t.Fatalf("an allow must succeed, got %v", err)
	}
	if body := srv.lastBody(); !strings.Contains(body, `"result":"allow"`) || strings.Contains(body, "deny_message") {
		t.Fatalf("the allow body is wrong: %s", body)
	}
	if _, _, err := execRoot(t, lot3Args(srv.URL, "claude-agents", "sessions", "tool-confirmation",
		"sess-1", "--tool-use-id", "tu-1", "--result", "deny", "--deny-message", "not in scope")...); err != nil {
		t.Fatalf("a deny must succeed, got %v", err)
	}
	if body := srv.lastBody(); !strings.Contains(body, `"result":"deny"`) || !strings.Contains(body, "not in scope") {
		t.Fatalf("the deny body is wrong: %s", body)
	}
}

// TestClaudeAgentsSystemTokenRefusalIsExplained: the remedy for "a system token
// cannot confirm a tool" is a HUMAN credential, not a broader role, and a bare
// exit 3 sends an operator to widen a grant that will not help.
func TestClaudeAgentsSystemTokenRefusalIsExplained(t *testing.T) {
	srv := newLot3Server(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w,
			`{"error":{"message":"a stable user identity is required to confirm a tool; a system token cannot confirm"}}`)
	})
	_, _, err := execRoot(t, lot3Args(srv.URL, "claude-agents", "sessions", "tool-confirmation",
		"sess-1", "--tool-use-id", "tu-1", "--result", "allow")...)
	if err == nil || exitcode.From(err) != exitcode.Auth {
		t.Fatalf("a system-token refusal must exit %d, got %v", exitcode.Auth, err)
	}
	if !strings.Contains(err.Error(), "system token cannot confirm") {
		t.Errorf("the engine's own reason must survive, got: %v", err)
	}
}
