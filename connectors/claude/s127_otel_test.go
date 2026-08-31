// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package claude

import (
	"strings"
	"testing"

	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
)

// TestParsePromptID proves prompt.id (the dotted grouping attribute) is captured off a
// tool_result event and threaded onto the correlator's OTEL signal — it was dropped
// before.
func TestParsePromptID(t *testing.T) {
	rec := logRecord(evtToolResult, testTime,
		kvStr(attrToolName, "Read"),
		kvStr(attrToolUseID, "toolu_123"),
		kvStr(attrPromptID, "11111111-2222-4333-8444-555555555555"),
	)
	res := []*commonpb.KeyValue{kvStr(attrSessionID, "s")}
	ev, ok := parseLogRecord(rec, res)
	if !ok {
		t.Fatal("record did not parse")
	}
	if ev.promptID != "11111111-2222-4333-8444-555555555555" {
		t.Fatalf("prompt.id not captured: %q", ev.promptID)
	}
	// It must flow onto the correlator's OTEL signal.
	sig := toolSignalFromOTEL(ev)
	if sig.promptID != ev.promptID {
		t.Errorf("prompt.id not threaded to tool signal: %q", sig.promptID)
	}
}

// TestSubprocessOTELCaveat pins the verified subprocess-env caveat content: the uncovered
// subprocess kinds, the TRACEPARENT nuance, and a non-empty mitigation.
func TestSubprocessOTELCaveat(t *testing.T) {
	c := SubprocessOTELCaveat()
	if !strings.Contains(c.Caveat, "OTEL_*") || !strings.Contains(c.Caveat, "not propagated") && !strings.Contains(c.Caveat, "NOT propagated") {
		t.Errorf("caveat headline unexpected: %q", c.Caveat)
	}
	for _, want := range []string{"Bash tool", "hooks", "MCP servers", "language servers"} {
		found := false
		for _, k := range c.UncoveredKinds {
			if k == want {
				found = true
			}
		}
		if !found {
			t.Errorf("uncovered kinds missing %q: %v", want, c.UncoveredKinds)
		}
	}
	if !c.TraceContextInherited {
		t.Error("TRACEPARENT (W3C trace context) IS inherited — must be modeled true")
	}
	if !strings.Contains(c.Mitigation, "TRACEPARENT") {
		t.Errorf("mitigation should note the TRACEPARENT nuance: %q", c.Mitigation)
	}
	// The returned slice must be a copy (mutating it must not corrupt the package list).
	c.UncoveredKinds[0] = "MUTATED"
	if SubprocessOTELCaveat().UncoveredKinds[0] == "MUTATED" {
		t.Error("caveat must return a defensive copy of the uncovered kinds")
	}
}
