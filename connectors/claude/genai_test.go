// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package claude

import (
	"testing"
	"time"

	commonpb "go.opentelemetry.io/proto/otlp/common/v1"

	"github.com/olivaresai/olivares/sdk/model"
)

// genAIToolRecord builds a vendor-neutral gen_ai.* execute_tool log record from a
// non-Claude producer (e.g. an OpenAI-SDK / LangGraph agent).
func genAIToolRecord(ts time.Time) []*commonpb.KeyValue {
	return []*commonpb.KeyValue{
		kvStr(attrGenAIOperation, opExecuteTool),
		kvStr(attrGenAIProvider, "openai"),
		kvStr(attrGenAIReqModel, "gpt-4o"),
		kvInt(attrGenAIInTokens, 100),
		kvInt(attrGenAIOutTokens, 20),
		kvStr(attrGenAIAgentID, "ag-1"),
		kvStr(attrGenAIToolName, "db_query"),
		kvStr(attrGenAIConvID, "conv-1"),
	}
}

func TestGenAIOptInToken(t *testing.T) {
	if !genAIOptIn("something,gen_ai_latest_experimental,other") {
		t.Error("opt-in token must be recognized in a comma list")
	}
	if genAIOptIn("") || genAIOptIn("database_latest_experimental") {
		t.Error("absent/other token must not enable the profile")
	}
}

func TestParseGenAIRecord(t *testing.T) {
	rec := logRecord("gen_ai.client.inference.operation.details", time.Unix(1700000000, 0), genAIToolRecord(time.Time{})...)
	ev, ok := parseGenAIRecord(rec, nil)
	if !ok {
		t.Fatal("a gen_ai.* record must be recognized")
	}
	if ev.operation != opExecuteTool || ev.provider != "openai" || ev.model() != "gpt-4o" {
		t.Errorf("parsed gen_ai event wrong: %+v", ev)
	}
	if ev.inputTokens != 100 || ev.outputTokens != 20 || !ev.hasTokens {
		t.Errorf("token usage not parsed: %+v", ev)
	}
	// A claude_code.* record carries no gen_ai.* attrs -> not a gen_ai record.
	cc := logRecord(evtToolResult, time.Unix(1700000000, 0), kvStr(attrSessionID, "s1"), kvStr(attrToolName, "Read"))
	if _, ok := parseGenAIRecord(cc, nil); ok {
		t.Error("a claude_code.* record must NOT be treated as gen_ai")
	}
}

// TestGenAIProfileProducesEdgeAndCost is the OBS-01 regression: with the profile
// opted in, a non-Claude gen_ai.* producer feeds the SAME edge/cost pipeline — it is
// no longer silently dropped by the case-only routeOTEL switch.
func TestGenAIProfileProducesEdgeAndCost(t *testing.T) {
	c := &collect{}
	dispatch := c.emit
	wd := newWatchdog(time.Minute, dispatch)
	ids := newIdentitySeen()
	s := &Source{cfg: config{genAIProfile: true, correlationWait: time.Second}}

	var onOTELCalled bool
	rcv := &receiver{
		onOTEL:  func(claudeEvent) { onOTELCalled = true },
		onGenAI: s.genAIRouter(wd, ids, dispatch),
		now:     func() time.Time { return time.Unix(1700000000, 0).UTC() },
	}
	rcv.ingestLogs(exportLogs(nil, logRecord("gen_ai.client.inference.operation.details", time.Unix(1700000000, 0), genAIToolRecord(time.Time{})...)))

	if onOTELCalled {
		t.Error("a gen_ai record must route to onGenAI, NOT the claude_code path")
	}
	costs := costObs(c)
	if len(costs) != 1 {
		t.Fatalf("want 1 CostSample, got %d", len(costs))
	}
	if costs[0].ProviderRef != "openai" || costs[0].ModelRef != "gpt-4o" || costs[0].InputTokens != 100 || costs[0].OutputTokens != 20 {
		t.Errorf("CostSample wrong: %+v", costs[0])
	}
	if costs[0].Provenance != model.ProvenanceEstimated {
		t.Errorf("gen_ai cost provenance must be estimated (no billed cost): %v", costs[0].Provenance)
	}
	edges := c.edges()
	var sawTool, sawAgent bool
	for _, e := range edges {
		if e.ResourceKind == resGenAITool && e.ResourceRef == "db_query" && e.OriginRef == "ag-1" {
			sawTool = true
		}
		if e.ResourceKind == resGenAIAgent && e.ResourceRef == "ag-1" && e.OriginRef == "conv-1" {
			sawAgent = true
		}
	}
	if !sawTool {
		t.Errorf("missing the genai.tool access edge: %+v", edges)
	}
	if !sawAgent {
		t.Errorf("missing the conversation->agent attribution edge: %+v", edges)
	}
}

// TestGenAIProfileOffFallsThrough confirms the opt-in gate: with the profile OFF, a
// gen_ai record is not mapped (onGenAI is nil) and flows to the claude_code path,
// which feeds liveness but emits no edge/cost — honest, not silently mapped against
// a Development-status schema.
func TestGenAIProfileOffFallsThrough(t *testing.T) {
	c := &collect{}
	s := &Source{cfg: config{genAIProfile: false}}
	if s.genAIRouter(newWatchdog(time.Minute, c.emit), newIdentitySeen(), c.emit) != nil {
		t.Fatal("genAIRouter must be nil when the profile is off")
	}

	var onOTELCalled bool
	rcv := &receiver{
		onOTEL:  func(claudeEvent) { onOTELCalled = true },
		onGenAI: s.genAIRouter(newWatchdog(time.Minute, c.emit), newIdentitySeen(), c.emit),
		now:     func() time.Time { return time.Unix(1700000000, 0).UTC() },
	}
	rcv.ingestLogs(exportLogs(nil, logRecord("gen_ai.client.inference.operation.details", time.Unix(1700000000, 0), genAIToolRecord(time.Time{})...)))
	if !onOTELCalled {
		t.Error("with the profile off, a gen_ai record must fall through to the claude_code path")
	}
	if len(c.edges()) != 0 || len(costObs(c)) != 0 {
		t.Error("with the profile off, no gen_ai edge/cost must be emitted")
	}
}

// costObs extracts CostSamples from a collect.
func costObs(c *collect) []model.CostSample {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []model.CostSample
	for _, o := range c.obs {
		if cs, ok := o.(model.CostSample); ok {
			out = append(out, cs)
		}
	}
	return out
}
