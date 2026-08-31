// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package claude

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestParseHook(t *testing.T) {
	body := []byte(`{"session_id":"s","hook_event_name":"PostToolUse","tool_name":"Read","tool_use_id":"tu_1","tool_input":{"file_path":"/x"}}`)
	ev, ok := parseHook(body, testTime)
	if !ok {
		t.Fatal("parse failed")
	}
	if ev.sessionID != "s" || ev.event != hookPostToolUse || ev.toolName != "Read" || ev.toolUseID != "tu_1" {
		t.Errorf("bad hook: %+v", ev)
	}
	if ev.input["file_path"] != "/x" {
		t.Errorf("tool_input not parsed: %v", ev.input)
	}
}

func TestParseHookRejectsGarbage(t *testing.T) {
	if _, ok := parseHook([]byte("not json"), testTime); ok {
		t.Error("invalid JSON must be rejected")
	}
	if _, ok := parseHook([]byte(`{}`), testTime); ok {
		t.Error("empty payload (no session, no tool) must be rejected")
	}
}

func TestHookHandlerAlwaysAcks(t *testing.T) {
	var got *hookEvent
	h := hookHandler(func(e hookEvent) { got = &e }, nil, func() time.Time { return testTime })

	// POST a valid hook → 200 "{}" and the callback fires.
	body := `{"session_id":"s","hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"ls"}}`
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/hooks", strings.NewReader(body)))
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d", rec.Code)
	}
	if out, _ := io.ReadAll(rec.Body); strings.TrimSpace(string(out)) != "{}" {
		t.Errorf("body = %q", out)
	}
	if got == nil || got.toolName != "Bash" {
		t.Errorf("callback not invoked correctly: %+v", got)
	}

	// A GET must not invoke the callback but still answer 200 (never block the agent).
	got = nil
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/hooks", nil))
	if rec.Code != http.StatusOK || got != nil {
		t.Errorf("GET handled wrong: code=%d got=%+v", rec.Code, got)
	}
}

func TestHookHandlerReturnsDecision(t *testing.T) {
	// A decider that denies Bash returns the Claude Code permissionDecision JSON,
	// while still forwarding the event on the telemetry path.
	var observed *hookEvent
	decide := func(e hookEvent) hookDecision {
		if e.toolName == "Bash" {
			return hookDecision{event: e.event, permission: "deny", reason: "shell blocked"}
		}
		return hookDecision{}
	}
	h := hookHandler(func(e hookEvent) { observed = &e }, decide, func() time.Time { return testTime })

	rec := httptest.NewRecorder()
	body := `{"session_id":"s","hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"rm -rf /"}}`
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/hooks", strings.NewReader(body)))
	out, _ := io.ReadAll(rec.Body)
	if !strings.Contains(string(out), `"permissionDecision":"deny"`) || !strings.Contains(string(out), `"hookEventName":"PreToolUse"`) {
		t.Errorf("decision body = %q", out)
	}
	if observed == nil || observed.toolName != "Bash" {
		t.Errorf("telemetry path not invoked: %+v", observed)
	}

	// A non-matching tool gets the neutral "{}" (cooperative).
	rec = httptest.NewRecorder()
	allow := `{"session_id":"s","hook_event_name":"PreToolUse","tool_name":"Read","tool_input":{"file_path":"/x"}}`
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/hooks", strings.NewReader(allow)))
	if out, _ := io.ReadAll(rec.Body); strings.TrimSpace(string(out)) != "{}" {
		t.Errorf("non-matching tool should get {}, got %q", out)
	}
}

func TestHookDecisionJSON(t *testing.T) {
	if got := (hookDecision{}).json(); string(got) != "{}" {
		t.Errorf("empty decision must render {}, got %q", got)
	}
	d := hookDecision{event: "PreToolUse", permission: "ask", reason: "needs approval"}
	got := string(d.json())
	for _, want := range []string{`"hookSpecificOutput"`, `"hookEventName":"PreToolUse"`, `"permissionDecision":"ask"`, `"permissionDecisionReason":"needs approval"`} {
		if !strings.Contains(got, want) {
			t.Errorf("decision JSON %q missing %q", got, want)
		}
	}
}
