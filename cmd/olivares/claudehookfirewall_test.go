// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"testing"

	"github.com/olivaresai/olivares/connectors/claude"
	claudeapi "github.com/olivaresai/olivares/connectors/claude-api"
	"github.com/olivaresai/olivares/core/model"
)

// fakeHookInspector is a stand-in for the commercial hookhardening.Inspector. It records the
// channels it saw and returns a configurable verdict.
type fakeHookInspector struct {
	forward     bool
	gotChannels int
	gotTexts    []string
}

func (f *fakeHookInspector) Inspect(_ context.Context, in claudeapi.ContentInspectionInput) claudeapi.ContentInspectionDecision {
	f.gotChannels = len(in.Channels)
	for _, ch := range in.Channels {
		f.gotTexts = append(f.gotTexts, ch.Text)
	}
	meter := claudeapi.ContentInspectionMeter{Inspections: 1, Channels: len(in.Channels)}
	if f.forward {
		return claudeapi.ContentInspectionDecision{Forward: true, Meter: meter}
	}
	return claudeapi.ContentInspectionDecision{
		Forward: false, Status: 403, ErrorType: "permission_error",
		Reason: "blocked by test hook firewall", Meter: meter,
		Findings: []claudeapi.ContentInspectionFinding{{Kind: "hook_firewall_secret_credential", Severity: "high", Detector: "secret.credential"}},
	}
}

func TestHookFirewall_FurtherRestrictsAllowedCall(t *testing.T) {
	h := newHarness(t)
	tok := h.firmAgentToken(t, "agent-fw-deny@e2e.test")
	// Policy WOULD allow the call; the firewall denies it (a secret in the argument).
	f := newHookPEPFixture(t, h, hookPolicyDoc{Default: "allow"}, false, fixedEval{allow: true}, false)
	insp := &fakeHookInspector{forward: false}
	f.dec.hookInspector = insp

	out := f.call(t, "Write", map[string]any{"file_path": "/app/c.env", "content": "AKIAIOSFODNN7EXAMPLE"}, tok, h.tenantA)
	if got := decisionOf(out); got != claude.DecisionDeny {
		t.Fatalf("firewall must further-restrict an allowed call to deny, got %q (%v)", got, out)
	}
	if insp.gotChannels == 0 {
		t.Fatalf("the inspector must have received the extracted tool_input channels")
	}
}

func TestHookFirewall_CleanVerdictDoesNotWidenDisposition(t *testing.T) {
	h := newHarness(t)
	tok := h.firmAgentToken(t, "agent-fw-nowiden@e2e.test")
	// Disposition DENY; a clean (forwarding) firewall must NOT turn it into an allow.
	f := newHookPEPFixture(t, h, hookPolicyDoc{Default: "deny"}, false, fixedEval{allow: true}, false)
	f.dec.hookInspector = &fakeHookInspector{forward: true}

	out := f.call(t, "Read", map[string]any{"file_path": "/repo/x"}, tok, h.tenantA)
	if got := decisionOf(out); got != claude.DecisionDeny {
		t.Fatalf("a clean firewall must not widen a deny disposition, got %q (%v)", got, out)
	}
}

func TestHookFirewall_AllowsWhenCleanAndPermitted(t *testing.T) {
	h := newHarness(t)
	tok := h.firmAgentToken(t, "agent-fw-allow@e2e.test")
	f := newHookPEPFixture(t, h, hookPolicyDoc{Default: "allow"}, false, fixedEval{allow: true}, false)
	insp := &fakeHookInspector{forward: true}
	f.dec.hookInspector = insp

	out := f.call(t, "Bash", map[string]any{"command": "ls -la /repo"}, tok, h.tenantA)
	if got := decisionOf(out); got != claude.DecisionAllow {
		t.Fatalf("a clean firewall + permit policy must allow, got %q (%v)", got, out)
	}
	if len(insp.gotTexts) == 0 || insp.gotTexts[0] != "ls -la /repo" {
		t.Fatalf("the Bash command must have been extracted as a channel, got %v", insp.gotTexts)
	}
}

func TestHookFirewall_NilInspectorIsInert(t *testing.T) {
	// The default AGPL build path: a nil inspector is a clean pass — the firewall changes nothing.
	d := &claudeHookDecider{}
	dec := d.runHookFirewall(context.Background(), model.TenantID("t_x"), "actor", claude.HookDecisionInput{
		Tool: "Bash", Event: "PreToolUse",
	})
	if !dec.Forward {
		t.Fatalf("nil inspector must be an inert clean pass")
	}
}
