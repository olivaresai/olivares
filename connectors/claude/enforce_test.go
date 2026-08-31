// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package claude

import (
	"testing"

	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

func TestParseEnforcementDefaultDisabled(t *testing.T) {
	for _, in := range []string{"", "   "} {
		p, err := parseEnforcement(in)
		if err != nil {
			t.Fatalf("parseEnforcement(%q) err = %v", in, err)
		}
		if p.enabled() {
			t.Errorf("empty policy must be disabled")
		}
	}
}

func TestParseEnforcementInvalidFailsLoud(t *testing.T) {
	// Malformed JSON and an unknown decision must both error (never silently leave
	// the fleet ungoverned).
	if _, err := parseEnforcement(`{ not json`); err == nil {
		t.Error("malformed policy must error")
	}
	if _, err := parseEnforcement(`{"rules":[{"tool":"Bash","decision":"maybe"}]}`); err == nil {
		t.Error("invalid decision must error")
	}
	if _, err := parseEnforcement(`{"rules":[{"tool":"Bash","decision":"deny","bogus":1}]}`); err == nil {
		t.Error("unknown field must error (DisallowUnknownFields)")
	}
}

func TestEnforcementDecide(t *testing.T) {
	p, err := parseEnforcement(`{"rules":[
		{"tool":"Bash","decision":"ask","reason":"shell needs approval"},
		{"resource_kind":"file","mode":"write","decision":"deny","reason":"writes blocked"},
		{"tool":"mcp__*","decision":"ask"}
	]}`)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	// Bash → ask (first rule), with the configured reason.
	d := p.decide(hookEvent{event: hookPreToolUse, toolName: "Bash", input: map[string]any{"command": "ls"}})
	if d.permission != "ask" || d.reason != "shell needs approval" || d.event != hookPreToolUse {
		t.Errorf("Bash decision = %+v", d)
	}

	// A file write → deny (second rule).
	if w := p.decide(hookEvent{event: hookPreToolUse, toolName: "Write", input: map[string]any{"file_path": "/etc/x"}}); w.permission != "deny" {
		t.Errorf("write decision = %+v", w)
	}

	// A file READ matches no rule → empty (allow).
	if r := p.decide(hookEvent{event: hookPreToolUse, toolName: "Read", input: map[string]any{"file_path": "/etc/x"}}); !r.isEmpty() {
		t.Errorf("read should not be gated, got %+v", r)
	}

	// An MCP tool matches the glob rule → ask, with the default reason.
	if m := p.decide(hookEvent{event: hookPermissionRequest, toolName: "mcp__github__create_issue"}); m.permission != "ask" || m.reason == "" {
		t.Errorf("mcp decision = %+v", m)
	}

	// A non-enforcing hook (PostToolUse) is never gated even if a rule would match.
	if pt := p.decide(hookEvent{event: hookPostToolUse, toolName: "Bash"}); !pt.isEmpty() {
		t.Errorf("PostToolUse must not be gated, got %+v", pt)
	}
}

func TestToolGlobMatch(t *testing.T) {
	cases := []struct {
		pattern, s string
		want       bool
	}{
		{"", "Bash", true}, {"*", "Bash", true},
		{"Bash", "Bash", true}, {"Bash", "Read", false},
		{"mcp__*", "mcp__github__x", true}, {"mcp__*", "Bash", false},
	}
	for _, c := range cases {
		if got := toolGlobMatch(c.pattern, c.s); got != c.want {
			t.Errorf("toolGlobMatch(%q,%q) = %v, want %v", c.pattern, c.s, got, c.want)
		}
	}
}

func TestFindingFromEnforcement(t *testing.T) {
	deny := findingFromEnforcement(
		hookEvent{event: hookPreToolUse, sessionID: "s", toolName: "Bash", at: testTime},
		hookDecision{event: hookPreToolUse, permission: "deny", reason: "blocked"},
	)
	if deny.Kind != findingKindEnforcement || deny.Severity != model.SeverityMedium || deny.SubjectRef != "s" {
		t.Errorf("deny finding = %+v", deny)
	}
	ask := findingFromEnforcement(
		hookEvent{event: hookPreToolUse, sessionID: "s", toolName: "Bash", at: testTime},
		hookDecision{event: hookPreToolUse, permission: "ask"},
	)
	if ask.Severity != model.SeverityLow {
		t.Errorf("ask finding severity = %v, want low", ask.Severity)
	}
	if deny.DetailHash == "" {
		t.Error("enforcement finding should carry a detail hash")
	}
}

func TestOpenRejectsBadEnforcementPolicy(t *testing.T) {
	s := New()
	err := s.Open(t.Context(), sdk.Config{Settings: map[string]string{
		cfgEnableGRPC: "false", cfgEnableHTTP: "true", cfgHTTPAddr: "127.0.0.1:0",
		cfgEnforcement: `{"rules":[{"tool":"Bash","decision":"explode"}]}`,
	}})
	if err == nil {
		t.Fatal("Open must reject an invalid enforcement policy (never silently ungoverned)")
	}
}
