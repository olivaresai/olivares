// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package claude

import (
	"strings"
	"testing"

	"github.com/olivaresai/olivares/sdk/model"
)

func TestEdgeFromTool(t *testing.T) {
	e, ok := edgeFromTool("sess-1", "Read", map[string]any{"file_path": "/x"}, testTime, model.ConfidenceAttributed)
	if !ok {
		t.Fatal("edge not built")
	}
	if e.OriginKind != originSession || e.OriginRef != "sess-1" {
		t.Errorf("origin = %s/%s", e.OriginKind, e.OriginRef)
	}
	if e.ResourceKind != resFile || e.ResourceRef != "/x" || e.Mode != model.ModeRead {
		t.Errorf("resource = %s/%s/%s", e.ResourceKind, e.ResourceRef, e.Mode)
	}
	if e.Source != model.SignalOTEL || e.Confidence != model.ConfidenceAttributed || e.ToolRef != "Read" {
		t.Errorf("provenance = %s/%s/%s", e.Source, e.Confidence, e.ToolRef)
	}
}

func TestEdgeFromToolNoSession(t *testing.T) {
	if _, ok := edgeFromTool("", "Read", nil, testTime, model.ConfidenceAttributed); ok {
		t.Error("edge without a session must not be built")
	}
}

func TestCostFromEvent(t *testing.T) {
	ev := claudeEvent{name: evtAPIRequest, sessionID: "s", model: "claude-opus-4-8", costUSD: 0.0123, hasCost: true, inputTokens: 1000, outputTokens: 200, at: testTime}
	cs, ok := costFromEvent(ev, "")
	if !ok {
		t.Fatal("cost not built")
	}
	if cs.ProviderRef != providerAnthropic || cs.ModelRef != "claude-opus-4-8" || cs.SessionRef != "s" {
		t.Errorf("cost provenance = %+v", cs)
	}
	if cs.CostMicroUSD != 12300 {
		t.Errorf("CostMicroUSD = %d, want 12300", cs.CostMicroUSD)
	}
	if cs.InputTokens != 1000 || cs.OutputTokens != 200 {
		t.Errorf("tokens = %+v", cs)
	}
	// A bare claude-* id with no configured surface resolves to direct.
	if cs.Gateway != model.GatewayDirect {
		t.Errorf("gateway = %q, want direct", cs.Gateway)
	}
}

func TestCostFromEventGuards(t *testing.T) {
	if _, ok := costFromEvent(claudeEvent{name: evtToolResult, hasCost: true}, ""); ok {
		t.Error("non-api event must not produce cost")
	}
	if _, ok := costFromEvent(claudeEvent{name: evtAPIRequest, hasCost: false}, ""); ok {
		t.Error("api event without cost must not produce a zero sample")
	}
}

func TestCostFromEventGateway(t *testing.T) {
	// Configured Vertex surface tags the sample (the model id cannot reveal Vertex).
	ev := claudeEvent{name: evtAPIRequest, sessionID: "s", model: "claude-opus-4-6", costUSD: 1, hasCost: true, at: testTime}
	cs, _ := costFromEvent(ev, model.GatewayVertex)
	if cs.Gateway != model.GatewayVertex {
		t.Errorf("configured gateway = %q, want vertex", cs.Gateway)
	}
	// A Bedrock CRIS model id wins over a (mistaken) direct config — hard evidence.
	ev2 := claudeEvent{name: evtAPIRequest, sessionID: "s", model: "us.anthropic.claude-opus-4-8", costUSD: 1, hasCost: true, at: testTime}
	cs2, _ := costFromEvent(ev2, model.GatewayDirect)
	if cs2.Gateway != model.GatewayBedrockLegacy {
		t.Errorf("CRIS id gateway = %q, want bedrock-legacy", cs2.Gateway)
	}
	// A bare anthropic.* id is bedrock-mantle.
	ev3 := claudeEvent{name: evtAPIRequest, sessionID: "s", model: "anthropic.claude-opus-4-8", costUSD: 1, hasCost: true, at: testTime}
	cs3, _ := costFromEvent(ev3, "")
	if cs3.Gateway != model.GatewayBedrockMantle {
		t.Errorf("mantle id gateway = %q, want bedrock-mantle", cs3.Gateway)
	}
}

func TestMicroUSD(t *testing.T) {
	cases := map[float64]int64{0: 0, -1: 0, 0.0123: 12300, 1: 1_000_000, 0.0000005: 1}
	for in, want := range cases {
		if got := microUSD(in); got != want {
			t.Errorf("microUSD(%v) = %d, want %d", in, got, want)
		}
	}
}

func TestEdgeFromMCPConnection(t *testing.T) {
	ev := claudeEvent{name: evtMCPConnection, sessionID: "s", mcpServer: "github", mcpStatus: "connected", at: testTime}
	e, ok := edgeFromMCPConnection(ev)
	if !ok || e.ResourceKind != resMCPServer || e.ResourceRef != "github" || e.Mode != model.ModeUnknown {
		t.Errorf("mcp edge = %+v ok=%v", e, ok)
	}
	// Unnamed server (detail off) → no edge.
	if _, ok := edgeFromMCPConnection(claudeEvent{name: evtMCPConnection, sessionID: "s", mcpStatus: "connected"}); ok {
		t.Error("unnamed server must not produce an edge")
	}
	// Failed connection → no observed edge.
	if _, ok := edgeFromMCPConnection(claudeEvent{name: evtMCPConnection, sessionID: "s", mcpServer: "github", mcpStatus: "failed"}); ok {
		t.Error("failed connection must not produce an observed edge")
	}
}

func TestFindingFromMCPConnection(t *testing.T) {
	f, ok := findingFromMCPConnection(claudeEvent{name: evtMCPConnection, sessionID: "s", mcpServer: "github", mcpStatus: "failed", at: testTime})
	if !ok || f.Kind != "health" || f.SubjectRef != "github" || f.Severity != model.SeverityLow {
		t.Errorf("finding = %+v ok=%v", f, ok)
	}
	if f.DetailHash == "" {
		t.Error("finding should carry a detail hash")
	}
	if _, ok := findingFromMCPConnection(claudeEvent{name: evtMCPConnection, mcpStatus: "connected"}); ok {
		t.Error("a healthy connection is not a finding")
	}
}

func TestFindingFromToolDecision(t *testing.T) {
	// A DENIED decision becomes a low-severity policy finding carrying the source.
	f, ok := findingFromToolDecision(claudeEvent{
		name: evtToolDecision, sessionID: "s", toolName: "Bash", decision: "reject",
		decisionSource: "hook", at: testTime,
	})
	if !ok || f.Kind != "policy_decision" || f.Severity != model.SeverityLow || f.SubjectRef != "s" {
		t.Errorf("denied finding = %+v ok=%v", f, ok)
	}
	if f.DetailHash == "" {
		t.Error("denied finding should carry a detail hash")
	}
	// An ACCEPTED decision is the guardrail working — not a finding.
	if _, ok := findingFromToolDecision(claudeEvent{name: evtToolDecision, sessionID: "s", toolName: "Bash", decision: "accept", at: testTime}); ok {
		t.Error("an accepted decision must not produce a finding")
	}
	// No session → nothing to attribute to.
	if _, ok := findingFromToolDecision(claudeEvent{name: evtToolDecision, toolName: "Bash", decision: "reject"}); ok {
		t.Error("a decision without a session must not produce a finding")
	}
}

func TestFindingFromPermissionMode(t *testing.T) {
	// Escalation INTO bypassPermissions is the high-severity governance signal.
	f, ok := findingFromPermissionMode(claudeEvent{
		name: evtPermissionMode, sessionID: "s", fromMode: "default", toMode: "bypassPermissions",
		modeTrigger: "shift_tab", at: testTime,
	})
	if !ok || f.Kind != findingKindPolicyChange || f.Severity != model.SeverityHigh || f.SubjectRef != "s" {
		t.Errorf("bypass escalation finding = %+v ok=%v", f, ok)
	}
	if f.DetailHash == "" {
		t.Error("permission-mode finding should carry a detail hash")
	}
	// A move back to plan/default is recorded at low severity, not dropped.
	low, ok := findingFromPermissionMode(claudeEvent{name: evtPermissionMode, sessionID: "s", fromMode: "auto", toMode: "plan", at: testTime})
	if !ok || low.Severity != model.SeverityLow {
		t.Errorf("de-escalation finding = %+v ok=%v", low, ok)
	}
	// acceptEdits is a medium-severity friction reduction.
	if med, ok := findingFromPermissionMode(claudeEvent{name: evtPermissionMode, sessionID: "s", toMode: "acceptEdits", at: testTime}); !ok || med.Severity != model.SeverityMedium {
		t.Errorf("acceptEdits finding = %+v ok=%v", med, ok)
	}
	// No session or no target mode → nothing.
	if _, ok := findingFromPermissionMode(claudeEvent{name: evtPermissionMode, toMode: "auto"}); ok {
		t.Error("mode change without a session must not produce a finding")
	}
}

func TestFindingFromAuth(t *testing.T) {
	yes, no := true, false
	// A successful login is a low-severity audit signal.
	f, ok := findingFromAuth(claudeEvent{name: evtAuth, sessionID: "s", authAction: "login", authMethod: "oauth", success: &yes, at: testTime})
	if !ok || f.Kind != findingKindAuth || f.Severity != model.SeverityLow {
		t.Errorf("login finding = %+v ok=%v", f, ok)
	}
	// A failed auth is medium severity.
	if fail, ok := findingFromAuth(claudeEvent{name: evtAuth, sessionID: "s", authAction: "login", success: &no, at: testTime}); !ok || fail.Severity != model.SeverityMedium {
		t.Errorf("failed-auth finding = %+v ok=%v", fail, ok)
	}
	// No action → nothing.
	if _, ok := findingFromAuth(claudeEvent{name: evtAuth, sessionID: "s"}); ok {
		t.Error("auth event without an action must not produce a finding")
	}
}

func TestFindingFromAPIError(t *testing.T) {
	// A single api_error is low severity; the raw message is never in the title.
	f, ok := findingFromAPIError(claudeEvent{name: evtAPIError, sessionID: "s", model: "claude-opus-4-8", statusCode: 529, errMessage: "overloaded: secret-leaky detail", at: testTime})
	if !ok || f.Kind != findingKindHealth || f.Severity != model.SeverityLow {
		t.Errorf("api_error finding = %+v ok=%v", f, ok)
	}
	if got := f.Title; got == "" || containsSubstr(got, "secret-leaky") {
		t.Errorf("api_error title leaks raw message or is empty: %q", got)
	}
	// retries-exhausted is medium severity.
	if r, ok := findingFromAPIError(claudeEvent{name: evtAPIRetriesGone, sessionID: "s", attempt: 5, statusCode: 529, at: testTime}); !ok || r.Severity != model.SeverityMedium {
		t.Errorf("retries-exhausted finding = %+v ok=%v", r, ok)
	}
	// A non-error event or no session → nothing.
	if _, ok := findingFromAPIError(claudeEvent{name: evtAPIRequest, sessionID: "s"}); ok {
		t.Error("non-error event must not produce an api-error finding")
	}
	if _, ok := findingFromAPIError(claudeEvent{name: evtAPIError}); ok {
		t.Error("api error without a session must not produce a finding")
	}
}

// containsSubstr is a tiny helper so the redaction assertion reads clearly.
func containsSubstr(s, sub string) bool { return strings.Contains(s, sub) }
