// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package claude

import (
	"strings"
	"testing"
	"time"

	coltracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"

	"github.com/olivaresai/olivares/sdk/model"
)

// s148_otel_test.go proves the 2.1.17x OTel parity (VERIFIED 2026-06-10):
// subagent hierarchy from span-only agent_id/parent_agent_id, app.entrypoint
// attribution, OTEL_RESOURCE_ATTRIBUTES as allowlisted labels, and the enriched
// tool_decision denial finding.

// agentSpanReq builds a trace export with one claude_code span carrying the
// subagent identity attributes.
func agentSpanReq(spanName, session, agentID, parentID string) *coltracepb.ExportTraceServiceRequest {
	attrs := []*commonpb.KeyValue{}
	if agentID != "" {
		attrs = append(attrs, kvStr(attrAgentID, agentID))
	}
	if parentID != "" {
		attrs = append(attrs, kvStr(attrParentAgentID, parentID))
	}
	return &coltracepb.ExportTraceServiceRequest{ResourceSpans: []*tracepb.ResourceSpans{{
		Resource: &resourcepb.Resource{Attributes: []*commonpb.KeyValue{kvStr(attrSessionID, session)}},
		ScopeSpans: []*tracepb.ScopeSpans{{Spans: []*tracepb.Span{
			{Name: spanName, Attributes: attrs, StartTimeUnixNano: uint64(testTime.UnixNano())},
		}}},
	}}}
}

func TestS148SubagentHierarchyFromSpans(t *testing.T) {
	var got []struct{ session, agent, parent string }
	r := &receiver{
		onAgentSpan: func(s, a, p string, _ time.Time) {
			got = append(got, struct{ session, agent, parent string }{s, a, p})
		},
		now: func() time.Time { return testTime },
	}

	// llm_request (2.1.139) and tool (2.1.145) spans both carry the identity.
	r.ingestTraces(agentSpanReq(spanLLMRequest, "sess-1", "agent-child", "agent-parent"))
	r.ingestTraces(agentSpanReq(spanTool, "sess-1", "agent-solo", ""))
	// A span without agent_id is the MAIN session: no hierarchy fact.
	r.ingestTraces(agentSpanReq(spanLLMRequest, "sess-1", "", ""))
	// Other span names never carry the attributes per the verified doc; even if
	// one did, the ingest only reads them from the two documented span names.
	r.ingestTraces(agentSpanReq("claude_code.interaction", "sess-1", "agent-x", ""))

	if len(got) != 2 {
		t.Fatalf("agent-span signals = %d, want 2 (main session and non-carrier spans excluded)", len(got))
	}
	if got[0].agent != "agent-child" || got[0].parent != "agent-parent" || got[1].agent != "agent-solo" {
		t.Errorf("agent-span identities = %+v", got)
	}
}

func TestS148SubagentEdges(t *testing.T) {
	// With a parent: membership (session→child) + hierarchy (parent→child).
	edges := subagentEdges("sess-1", "agent-child", "agent-parent", testTime)
	if len(edges) != 2 {
		t.Fatalf("want 2 edges, got %d", len(edges))
	}
	m, h := edges[0], edges[1]
	if m.OriginKind != originSession || m.OriginRef != "sess-1" ||
		m.ResourceKind != resIdentitySubagent || m.ResourceRef != "agent-child" {
		t.Errorf("membership edge = %+v", m)
	}
	if h.OriginKind != originAgent || h.OriginRef != "agent-parent" ||
		h.ResourceKind != resIdentitySubagent || h.ResourceRef != "agent-child" {
		t.Errorf("hierarchy edge = %+v", h)
	}

	// Spawned directly from the main session (no parent_agent_id): membership only.
	if edges := subagentEdges("sess-1", "agent-solo", "", testTime); len(edges) != 1 {
		t.Errorf("parentless subagent must yield only the membership edge, got %+v", edges)
	}
	// No agent = the main session: nothing.
	if edges := subagentEdges("sess-1", "", "", testTime); edges != nil {
		t.Errorf("main session must yield no hierarchy edges, got %+v", edges)
	}
}

func TestS148RouteAgentSpanDedupes(t *testing.T) {
	s := New()
	var obs []model.Observation
	route := s.routeAgentSpan(func(o model.Observation) { obs = append(obs, o) })
	route("sess-1", "agent-a", "parent-1", testTime)
	route("sess-1", "agent-a", "parent-1", testTime) // re-delivery: deduped
	route("sess-1", "agent-b", "", testTime)
	if len(obs) != 3 { // 2 (a: membership+hierarchy) + 1 (b: membership)
		t.Errorf("deduped hierarchy emissions = %d, want 3", len(obs))
	}
}

func TestS148AppEntrypointRidesIdentityEdges(t *testing.T) {
	id := claudeIdentity{sessionID: "sess-1", orgID: "org-1", entrypoint: "claude-vscode"}
	var sawEntry bool
	for _, e := range identityEdges(id, testTime) {
		if e.ResourceKind == resEntrypoint {
			sawEntry = true
			if e.ResourceRef != "claude-vscode" || e.OriginRef != "sess-1" {
				t.Errorf("entrypoint edge = %+v", e)
			}
		}
	}
	if !sawEntry {
		t.Error("app.entrypoint must yield a session topology edge when present")
	}
	// Opt-in attribute absent (the default — OTEL_METRICS_INCLUDE_ENTRYPOINT=false):
	// no edge, never a fabricated one.
	for _, e := range identityEdges(claudeIdentity{sessionID: "sess-1", orgID: "org-1"}, testTime) {
		if e.ResourceKind == resEntrypoint {
			t.Errorf("absent entrypoint must not fabricate an edge: %+v", e)
		}
	}
}

func TestS148AppEntrypointParsedFromEvents(t *testing.T) {
	rec := logRecord(evtUserPrompt, testTime, kvStr(attrAppEntrypoint, "sdk-ts"))
	ev, ok := parseLogRecord(rec, nil)
	if !ok || ev.entrypoint != "sdk-ts" {
		t.Errorf("app.entrypoint not parsed from a log event: %+v (ok=%v)", ev, ok)
	}
}

func TestS148ResourceLabels(t *testing.T) {
	a := newAttrs([]*commonpb.KeyValue{
		kvStr("team", "payments"),
		kvStr("cost_center", "cc-42 token sk-ant-api03-SECRETSECRETSECRET"),
		kvStr("department", "eng"),
		kvStr(attrSessionID, "sess-1"),
	})

	// Allowlist off (the default): nil — arbitrary operator labels are NOT ingested.
	if got := labelsFromAttrs(a, nil); got != nil {
		t.Errorf("labels with no allowlist = %v, want nil", got)
	}

	got := labelsFromAttrs(a, []string{"team", "cost_center", "missing", attrSessionID})
	if got["team"] != "payments" {
		t.Errorf("team label = %q", got["team"])
	}
	// A built-in standard attribute never becomes a label, even if allowlisted.
	if _, has := got[attrSessionID]; has {
		t.Error("built-in attribute keys must be skipped in labels")
	}
	// Non-allowlisted keys are dropped.
	if _, has := got["department"]; has {
		t.Error("non-allowlisted key must not become a label")
	}
	// Values are scrubbed: the embedded credential never survives (negative-space).
	if v := got["cost_center"]; strings.Contains(v, "SECRETSECRET") {
		t.Errorf("label value not scrubbed: %q", v)
	}
}

func TestS148LabelsRideIdentityEdgesAndCost(t *testing.T) {
	labels := map[string]string{"team": "payments"}
	for _, e := range identityEdges(claudeIdentity{sessionID: "s", orgID: "o", labels: labels}, testTime) {
		if e.Labels["team"] != "payments" {
			t.Errorf("identity edge missing labels: %+v", e)
		}
	}
	ev := claudeEvent{name: evtAPIRequest, sessionID: "s", at: testTime, model: "claude-opus-4-8",
		costUSD: 0.5, hasCost: true, labels: labels}
	cs, ok := costFromEvent(ev, "")
	if !ok || cs.Labels["team"] != "payments" {
		t.Errorf("cost sample missing labels: %+v (ok=%v)", cs, ok)
	}
}

func TestS148LabelsCollectedAtReceiver(t *testing.T) {
	var got []claudeEvent
	r := &receiver{
		onOTEL:    func(ev claudeEvent) { got = append(got, ev) },
		labelKeys: []string{"team"},
		now:       func() time.Time { return testTime },
	}
	// The label arrives as a RESOURCE attribute (the OTEL_RESOURCE_ATTRIBUTES
	// carrier) and must reach the parsed event.
	r.ingestLogs(exportLogs(
		[]*commonpb.KeyValue{kvStr(attrSessionID, "s"), kvStr("team", "payments")},
		logRecord(evtUserPrompt, testTime),
	))
	if len(got) != 1 || got[0].labels["team"] != "payments" {
		t.Errorf("receiver-collected labels = %+v", got)
	}
}

func TestS148ToolDecisionDenialCarriesSanitizedRef(t *testing.T) {
	// Denied Bash with tool_parameters (2.1.157): the finding names the PROGRAM
	// (shellProgram strips arguments, which may carry secrets), and its hash
	// differs from a detail-less denial of the same tool.
	withDetail := claudeEvent{name: evtToolDecision, sessionID: "s", at: testTime,
		toolName: "Bash", decision: "reject", decisionSource: "hook",
		toolInput: map[string]any{"command": "psql postgres://u:hunter2@db/prod -c 'drop table x'"}}
	f1, ok := findingFromToolDecision(withDetail)
	if !ok {
		t.Fatal("denied decision must yield a finding")
	}
	if !strings.Contains(f1.Title, "shell:psql") {
		t.Errorf("denial title must name the sanitized resource, got %q", f1.Title)
	}
	if strings.Contains(f1.Title, "hunter2") || strings.Contains(f1.Title, "drop table") {
		t.Errorf("denial title leaked raw arguments: %q", f1.Title)
	}

	bare := withDetail
	bare.toolInput = nil
	f2, ok := findingFromToolDecision(bare)
	if !ok {
		t.Fatal("detail-less denial must still yield a finding")
	}
	if strings.Contains(f2.Title, "->") {
		t.Errorf("detail-less denial must keep the pre-2.1.157 title shape: %q", f2.Title)
	}
	if f1.DetailHash == f2.DetailHash {
		t.Error("distinct denials (with/without resource detail) must hash distinctly")
	}
}
