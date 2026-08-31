// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package cowork

import (
	"testing"

	"github.com/olivaresai/olivares/sdk/model"
)

func TestEdgeFromTool(t *testing.T) {
	e, ok := edgeFromTool("sess-1", "Write", map[string]any{"file_path": "/x"}, testTime)
	if !ok {
		t.Fatal("edge not built")
	}
	if e.OriginKind != originSession || e.OriginRef != "sess-1" {
		t.Errorf("origin = %s/%s", e.OriginKind, e.OriginRef)
	}
	if e.ResourceKind != resFile || e.ResourceRef != "/x" || e.Mode != model.ModeWrite {
		t.Errorf("resource = %s/%s/%s", e.ResourceKind, e.ResourceRef, e.Mode)
	}
	if e.Source != model.SignalOTEL || e.Confidence != model.ConfidenceAttributed || e.ToolRef != "Write" {
		t.Errorf("provenance = %s/%s/%s", e.Source, e.Confidence, e.ToolRef)
	}
	if !e.ObservedAt.Equal(testTime) {
		t.Errorf("observedAt = %v", e.ObservedAt)
	}
	if _, ok := edgeFromTool("", "Read", nil, testTime); ok {
		t.Error("an edge with no session must not be built")
	}
}

func TestEdgeFromMCPServer(t *testing.T) {
	e, ok := edgeFromMCPServer(coworkEvent{sessionID: "sess-1", mcpServerScope: "github", at: testTime})
	if !ok || e.ResourceKind != resMCPServer || e.ResourceRef != "github" || e.Mode != model.ModeUnknown {
		t.Errorf("edge = %+v ok=%v", e, ok)
	}
	if _, ok := edgeFromMCPServer(coworkEvent{sessionID: "sess-1", at: testTime}); ok {
		t.Error("no edge without an mcp_server_scope")
	}
}

func TestCostFromAPIRequest(t *testing.T) {
	ev := coworkEvent{
		name: evtAPIRequest, sessionID: "s", accountID: "user_01ACC", model: "claude-opus-4-8",
		costUSD: 0.05, hasCost: true, inputTokens: 900, outputTokens: 100, cacheReadTokens: 40, cacheCreationTokens: 10, at: testTime,
	}
	cs, ok := costFromAPIRequest(ev, "")
	if !ok {
		t.Fatal("cost not built")
	}
	if cs.ProviderRef != providerAnthropic || cs.ModelRef != "claude-opus-4-8" || cs.SessionRef != "s" {
		t.Errorf("provenance = %+v", cs)
	}
	if cs.Actor != "user_01ACC" {
		t.Errorf("Actor = %q, want the shared account id (per-user FinOps attribution)", cs.Actor)
	}
	if cs.CostMicroUSD != 50000 {
		t.Errorf("CostMicroUSD = %d, want 50000", cs.CostMicroUSD)
	}
	// InputTokens folds uncached + cache-read + cache-creation: 900+40+10.
	if cs.InputTokens != 950 || cs.OutputTokens != 100 || cs.CacheReadTokens != 40 || cs.CacheCreation5mTokens != 10 {
		t.Errorf("tokens = %+v", cs)
	}
	if cs.Gateway != model.GatewayDirect || cs.Provenance != model.ProvenanceEstimated {
		t.Errorf("gateway/provenance = %q/%q", cs.Gateway, cs.Provenance)
	}
	// No cost figure => no sample (never a fabricated zero).
	if _, ok := costFromAPIRequest(coworkEvent{name: evtAPIRequest, sessionID: "s"}, ""); ok {
		t.Error("a request without cost must not emit a sample")
	}
}

func TestCostGatewayBedrockDetection(t *testing.T) {
	ev := coworkEvent{name: evtAPIRequest, sessionID: "s", model: "us.anthropic.claude-opus-4-8", costUSD: 0.01, hasCost: true, at: testTime}
	cs, _ := costFromAPIRequest(ev, "")
	if cs.Gateway != model.GatewayBedrockLegacy {
		t.Errorf("a geo-prefixed inference-profile id must resolve to bedrock-legacy, got %q", cs.Gateway)
	}
}

// TestFindingFromAutoApprovedAction is the central governance signal: a
// high-risk action approved AUTOMATICALLY (config/hook) emits a high-severity
// finding; a manually-approved one or a low-risk one does NOT.
func TestFindingFromAutoApprovedAction(t *testing.T) {
	base := func(tool, src string, input map[string]any) coworkEvent {
		return coworkEvent{name: evtToolResult, sessionID: "sess-1", toolName: tool, decisionSource: src, toolInput: input, promptID: "turn-1", at: testTime}
	}

	// auto-approved write → finding.
	f, ok := findingFromAutoApprovedAction(base("Write", srcConfig, map[string]any{"file_path": "/etc/app.conf"}))
	if !ok || f.Kind != findingKindAutoApproved || f.Severity != model.SeverityHigh || f.SubjectRef != "sess-1" {
		t.Fatalf("auto-approved write should be a high finding, got %+v ok=%v", f, ok)
	}
	if f.DetailHash == "" {
		t.Error("finding must carry a detail hash")
	}
	if len(f.OWASPLLM) == 0 || len(f.OWASPASI) == 0 {
		t.Error("auto-approved excessive agency should carry OWASP LLM/ASI references")
	}

	// auto-approved (hook) shell → finding (shell is high-risk even at unknown mode).
	if _, ok := findingFromAutoApprovedAction(base("Bash", srcHook, map[string]any{"command": "rm -rf /tmp/x"})); !ok {
		t.Error("auto-approved shell should be a finding")
	}

	// MANUALLY approved write → NOT a finding (the guardrail working).
	if _, ok := findingFromAutoApprovedAction(base("Write", "user_permanent", map[string]any{"file_path": "/x"})); ok {
		t.Error("a manually-approved action must NOT be flagged")
	}

	// auto-approved READ (low-risk) → NOT a finding.
	if _, ok := findingFromAutoApprovedAction(base("Read", srcConfig, map[string]any{"file_path": "/x"})); ok {
		t.Error("an auto-approved low-risk read must NOT be flagged")
	}

	// unknown source → NOT a finding (fail-safe).
	if _, ok := findingFromAutoApprovedAction(base("Write", "", map[string]any{"file_path": "/x"})); ok {
		t.Error("an unknown decision source must NOT be treated as auto-approved")
	}

	// wrong event type → not a finding.
	if _, ok := findingFromAutoApprovedAction(coworkEvent{name: evtToolDecision, sessionID: "s", toolName: "Write", decisionSource: srcConfig}); ok {
		t.Error("auto-approved finding is keyed off tool_result only")
	}
}

func TestFindingFromToolDecision(t *testing.T) {
	f, ok := findingFromToolDecision(coworkEvent{name: evtToolDecision, sessionID: "s", toolName: "Bash", decision: decisionReject, decisionSource: "user_reject", at: testTime})
	if !ok || f.Kind != findingKindPolicyDecision || f.Severity != model.SeverityLow || f.SubjectRef != "s" {
		t.Fatalf("denied tool decision should be a low finding, got %+v ok=%v", f, ok)
	}
	if f.DetailHash == "" {
		t.Error("finding must carry a detail hash")
	}
	// an ACCEPTED decision is the guardrail working, not a finding.
	if _, ok := findingFromToolDecision(coworkEvent{name: evtToolDecision, sessionID: "s", decision: decisionAccept}); ok {
		t.Error("an accepted decision must NOT be a finding")
	}
}

func TestFindingFromAPIError(t *testing.T) {
	f, ok := findingFromAPIError(coworkEvent{name: evtAPIError, sessionID: "s", model: "claude-opus-4-8", statusCode: 529, errMessage: "overloaded", at: testTime})
	if !ok || f.Kind != findingKindHealth || f.Severity != model.SeverityLow {
		t.Fatalf("api_error should be a low health finding, got %+v ok=%v", f, ok)
	}
	if f.DetailHash == "" {
		t.Error("finding must carry a detail hash")
	}
}

func TestSelfAuditFinding(t *testing.T) {
	f := selfAuditFinding(false, testTime)
	if f.Kind != findingKindSelfAudit || f.Severity != model.SeverityInfo {
		t.Errorf("self-audit = %+v", f)
	}
	if f.SubjectRef != Name {
		t.Errorf("self-audit subject = %q, want %q", f.SubjectRef, Name)
	}
}

func TestIdentityEdgesAndAccountRef(t *testing.T) {
	id := coworkIdentity{sessionID: "sess-1", orgID: "org-9", accountID: "user_01ACC", accountUUID: "uuid-1"}
	edges := identityEdges(id, testTime)
	var acct, org *model.EdgeObservation
	for i := range edges {
		switch edges[i].ResourceKind {
		case resIdentityAccount:
			acct = &edges[i]
		case resIdentityOrg:
			org = &edges[i]
		}
	}
	if acct == nil || acct.ResourceRef != "user_01ACC" || acct.OriginRef != "sess-1" || acct.Mode != model.ModeUnknown {
		t.Errorf("account edge = %+v", acct)
	}
	if org == nil || org.ResourceRef != "org-9" {
		t.Errorf("org edge = %+v", org)
	}
	if acct.Source != model.SignalOTEL || acct.Confidence != model.ConfidenceAttributed {
		t.Errorf("account edge provenance = %s/%s", acct.Source, acct.Confidence)
	}
	// AccountRef precedence: account_id wins over uuid/user.
	if got := AccountRef("user_01ACC", "uuid-1", "user_01U"); got != "user_01ACC" {
		t.Errorf("AccountRef = %q, want user_01ACC", got)
	}
	if got := AccountRef("", "uuid-1", "user_01U"); got != "uuid-1" {
		t.Errorf("AccountRef fallback = %q, want uuid-1", got)
	}
	if got := AccountRef("", "", "user_01U"); got != "user_01U" {
		t.Errorf("AccountRef fallback = %q, want user_01U", got)
	}
	if identityEdges(coworkIdentity{}, testTime) != nil {
		t.Error("no session => no identity edges")
	}
}
