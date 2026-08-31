// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package claude

import (
	"context"
	"strings"
	"testing"
	"time"

	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"

	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

// these tests pin the multi-dialect normalizer against the THREE GenAI
// generations real 2026 fleets emit, plus the mcp.* conventions (v1.39) and
// the invoke_agent client/internal split (v1.41). Every fixture mirrors a
// primary-source-verified emitter shape (openllmetry tags v0.40.14/v0.54.0,
// semconv tags v1.36.0/v1.37.0/v1.41.1, autogen-core/_telemetry/_genai.py,
// adk-python telemetry, 2026-06-11).

// kindedSpan builds a span with an explicit SpanKind (the v1.41 invoke_agent
// discriminator and the MCP client/server gate).
func kindedSpan(name string, kind tracepb.Span_SpanKind, spanID []byte, attrs ...*commonpb.KeyValue) *tracepb.Span {
	return &tracepb.Span{
		Name: name, Kind: kind, Attributes: attrs, SpanId: spanID,
		StartTimeUnixNano: uint64(testTime.UnixNano()),
	}
}

// edgesOfKind filters collected edges by ResourceKind.
func edgesOfKind(c *collect, kind string) []model.EdgeObservation {
	var out []model.EdgeObservation
	for _, e := range c.edges() {
		if e.ResourceKind == kind {
			out = append(out, e)
		}
	}
	return out
}

// --- Generation 1: legacy OpenLLMetry (pre-semconv) --------------------------

// fixtureOpenLLMetryLegacySpan is an openllmetry ≤v0.54 LangChain/OpenAI chat
// span: indexed prompt/completion content ON SPAN ATTRIBUTES, deprecated token
// names plus llm.usage.total_tokens, CAPITALIZED gen_ai.system, and the
// Traceloop markers (all verified at the v0.40.14 tag).
func fixtureOpenLLMetryLegacySpan() []*commonpb.KeyValue {
	return []*commonpb.KeyValue{
		kvStr(attrLegacyRequestType, "chat"),
		kvStr(attrGenAISystem, "OpenAI"), // capitalized — the legacy emit
		kvStr(attrGenAIReqModel, "gpt-4o"),
		kvStr(attrGenAIRespModel, "gpt-4o-2024-08-06"),
		kvInt(attrGenAIPromptTokens, 1200),
		kvInt(attrGenAICompletionTokens, 350),
		kvInt(attrLegacyTotalTokens, 1550),
		kvStr("gen_ai.prompt.0.role", "user"),
		kvStr("gen_ai.prompt.0.content", "SECRET-PROMPT-CONTENT"),
		kvStr("gen_ai.completion.0.role", "assistant"),
		kvStr("gen_ai.completion.0.content", "SECRET-COMPLETION-CONTENT"),
		kvStr("gen_ai.completion.0.finish_reason", "stop"),
		kvStr(attrTraceloopWorkflowName, "support-pipeline"),
		kvStr(attrGenAIConvID, "lc-thread-9"),
	}
}

func TestDialectLegacyOpenLLMetry(t *testing.T) {
	rcv, c := genAIReceiver(t)
	rcv.ingestTraces(exportSpans(nil, kindedSpan("openai.chat", tracepb.Span_SPAN_KIND_CLIENT, []byte{1}, fixtureOpenLLMetryLegacySpan()...)))

	cs := oneCost(t, c)
	if cs.InputTokens != 1200 || cs.OutputTokens != 350 {
		t.Errorf("legacy token names not read: %+v", cs)
	}
	// The capitalized legacy provider must be normalized, or FinOps splits
	// "OpenAI" and "openai" into two providers.
	if cs.ProviderRef != "openai" {
		t.Errorf("legacy provider must be lowercased, got %q", cs.ProviderRef)
	}
	// Content NEVER leaves the parser — no observation may carry the prompt.
	for _, e := range c.edges() {
		if strings.Contains(e.ResourceRef, "SECRET") || strings.Contains(e.OriginRef, "SECRET") {
			t.Errorf("legacy span content leaked into an edge: %+v", e)
		}
	}
}

func TestDialectDetection(t *testing.T) {
	cases := []struct {
		name      string
		eventName string
		attrs     []*commonpb.KeyValue
		want      string
	}{
		{"openllmetry indexed content", "", fixtureOpenLLMetryLegacySpan(), genAIDialectLegacy},
		// openllmetry v0.54: CURRENT token names next to legacy indexed prompts
		// and llm.usage.total_tokens on the same span (verified mixed shape).
		{"openllmetry v0.54 mixed", "", []*commonpb.KeyValue{
			kvStr(attrGenAISystem, "Langchain"),
			kvInt(attrGenAIInTokens, 10), kvInt(attrGenAIOutTokens, 2),
			kvInt(attrLegacyTotalTokens, 12),
			kvStr("gen_ai.prompt.0.role", "user"),
		}, genAIDialectLegacy},
		{"pre-v0.17 llm.vendor", "", []*commonpb.KeyValue{
			kvStr(attrLegacyVendor, "OpenAI"), kvStr(attrLegacyRequestType, "chat"),
		}, genAIDialectLegacy},
		{"v1.36 span (gen_ai.system + current tokens)", "", []*commonpb.KeyValue{
			kvStr(attrGenAISystem, "openai"), kvInt(attrGenAIInTokens, 5),
		}, genAIDialectV136},
		{"v1.36 per-message event by name", evtGenAIUserMessage, nil, genAIDialectV136},
		{"v1.37+ provider.name", "", []*commonpb.KeyValue{
			kvStr(attrGenAIProvider, "anthropic"),
		}, genAIDialectCurrent},
		{"v1.37+ messages marker wins over legacy stragglers", "", []*commonpb.KeyValue{
			kvStr(attrGenAIInputMessages, `[{"role":"user"}]`),
			kvStr(attrTraceloopSpanKind, "task"), // openllmetry ≥v0.55 keeps traceloop.*
		}, genAIDialectCurrent},
		{"operation.details event by name", evtGenAIOpDetails, []*commonpb.KeyValue{
			kvStr(attrGenAIOperation, "chat"),
		}, genAIDialectCurrent},
		// gen_ai.prompt.name is the v1.39 MCP prompt attribute, NOT the legacy
		// indexed gen_ai.prompt.{i}.* shape.
		{"mcp prompt name is not legacy", "", []*commonpb.KeyValue{
			kvStr(attrMCPMethod, "prompts/get"), kvStr(attrGenAIPromptName, "analyze-code"),
		}, genAIDialectCurrent},
		// ADK invoke_agent spans carry neither gen_ai.system nor provider.name:
		// generation-ambiguous keys are normalized under the current pin.
		{"ambiguous keys normalize under current pin", "", []*commonpb.KeyValue{
			kvStr(attrGenAIOperation, opInvokeAgent), kvStr(attrGenAIAgentName, "helper"),
			kvStr(attrGenAIConvID, "s-1"),
		}, genAIDialectCurrent},
		// The deprecated token names ALONE discriminate legacy — the shape the
		// canonical framework fixtures model (gen_ai.system + prompt/completion
		// tokens, no other legacy marker).
		{"deprecated token names alone are legacy", "", []*commonpb.KeyValue{
			kvStr(attrGenAISystem, "openai"), kvInt(attrGenAIPromptTokens, 7),
		}, genAIDialectLegacy},
		{"frameworks fixture: LangChain/OpenLLMetry", "", fixtureLangChainSpan(), genAIDialectLegacy},
		{"frameworks fixture: CrewAI/OpenLLMetry", "", fixtureCrewAISpan(), genAIDialectLegacy},
	}
	for _, tc := range cases {
		if got := detectGenAIDialect(newAttrs(tc.attrs), tc.eventName); got != tc.want {
			t.Errorf("%s: dialect = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// TestLegacyProviderFromLLMVendor pins the pre-v0.17 OpenLLMetry provider leg:
// such a fleet carries ONLY llm.vendor (no gen_ai.system), and the value is
// capitalized — the normalized event must read and lowercase it, or cost
// samples ship with an empty/fragmented ProviderRef.
func TestLegacyProviderFromLLMVendor(t *testing.T) {
	ev := genAIEventFromAttrs(newAttrs([]*commonpb.KeyValue{
		kvStr(attrLegacyVendor, "OpenAI"),
		kvStr(attrLegacyRequestType, "chat"),
		kvStr(attrGenAIReqModel, "gpt-4"),
	}), testTime, "", "")
	if ev.semconv != genAIDialectLegacy {
		t.Errorf("dialect = %q, want legacy", ev.semconv)
	}
	if ev.provider != "openai" {
		t.Errorf("provider = %q, want %q (read from llm.vendor, lowercased)", ev.provider, "openai")
	}
}

// TestFrameworkFixturesStampLegacyDialect pins what silently changed about
// the framework fixtures: the OpenLLMetry-shaped ones (deprecated token
// names) now classify — and drift-report — as the legacy dialect.
func TestFrameworkFixturesStampLegacyDialect(t *testing.T) {
	rcv, c := genAIReceiver(t)
	rcv.ingestTraces(exportSpans(nil, kindedSpan("chat", tracepb.Span_SPAN_KIND_CLIENT, []byte{40}, fixtureLangChainSpan()...)))
	var drift []model.FindingReport
	for _, f := range c.findings() {
		if f.SubjectKind == "genai.dialect" {
			drift = append(drift, f)
		}
	}
	if len(drift) != 1 || drift[0].SubjectRef != genAIDialectLegacy {
		t.Fatalf("want one legacy drift finding for the OpenLLMetry fixture, got %+v", drift)
	}
}

// --- Generation 2: v1.36-or-prior per-message events --------------------------

func TestV136PerMessageEventsRecognizedAndNeverRead(t *testing.T) {
	// A per-message event with its ONE (Recommended) attribute…
	rec := logRecord(evtGenAIUserMessage, testTime, kvStr(attrGenAISystem, "openai"))
	ev, ok := parseGenAIRecord(rec, []*commonpb.KeyValue{kvStr(attrGenAIConvID, "conv-36")})
	if !ok {
		t.Fatal("a v1.36 per-message event must be recognized")
	}
	if ev.semconv != genAIDialectV136 || ev.provider != "openai" || ev.conversationID != "conv-36" {
		t.Errorf("v1.36 event normalized wrong: %+v", ev)
	}
	// …and a BARE one (gen_ai.system absent): recognizable only by NAME.
	bare := logRecord(evtGenAIChoice, testTime)
	if _, ok := parseGenAIRecord(bare, nil); !ok {
		t.Fatal("a bare per-message event (no attributes) must be recognized by name")
	}
	// …and one delivered with the LEGACY "event.name" ATTRIBUTE instead of the
	// first-class EventName proto field (pre-v1.5.0 OTLP emitters — exactly the
	// deprecated-generation fleets this dialect targets). Without the fallback
	// the record carries no gen_ai.* attribute at all and would be DROPPED.
	legacyDelivery := logRecord("", testTime, kvStr(attrEventName, evtGenAIChoice))
	lev, ok := parseGenAIRecord(legacyDelivery, nil)
	if !ok {
		t.Fatal("a per-message event named via the event.name attribute must be recognized")
	}
	if lev.semconv != genAIDialectV136 {
		t.Errorf("event.name-delivered event dialect = %q, want %q", lev.semconv, genAIDialectV136)
	}

	// Through the router: liveness + the dialect drift finding, but NO cost and
	// NO edge — the events carry no usage; their bodies (content) are not read.
	rcv, c := genAIReceiver(t)
	rcv.ingestLogs(exportLogs([]*commonpb.KeyValue{kvStr(attrGenAIConvID, "conv-36")}, rec))
	if len(costObs(c)) != 0 || len(c.edges()) != 0 {
		t.Errorf("a per-message event must map no cost/edge, got %d obs", c.count())
	}
}

// --- Generation 3: v1.37+ messages -------------------------------------------

func TestV137MessagesSpanNormalizesWithoutReadingContent(t *testing.T) {
	rcv, c := genAIReceiver(t)
	span := kindedSpan("chat claude-sonnet-4-6", tracepb.Span_SPAN_KIND_CLIENT, []byte{2},
		kvStr(attrGenAIOperation, "chat"),
		kvStr(attrGenAIProvider, "anthropic"),
		kvStr(attrGenAIReqModel, "claude-sonnet-4-6"),
		kvInt(attrGenAIInTokens, 900),
		kvInt(attrGenAIOutTokens, 120),
		kvStr(attrGenAIInputMessages, `[{"role":"user","parts":[{"type":"text","content":"SECRET-INPUT"}]}]`),
		kvStr(attrGenAIOutputMessages, `[{"role":"assistant","parts":[{"type":"text","content":"SECRET-OUTPUT"}]}]`),
		kvStr(attrGenAISystemInstructions, `[{"type":"text","content":"SECRET-SYSTEM"}]`),
		kvStr(attrGenAIConvID, "conv-37"),
		kvStr(attrGenAIAgentName, "planner"),
	)
	rcv.ingestTraces(exportSpans(nil, span))

	cs := oneCost(t, c)
	if cs.ProviderRef != "anthropic" || cs.InputTokens != 900 || cs.OutputTokens != 120 {
		t.Errorf("v1.37+ span not normalized: %+v", cs)
	}
	for _, e := range c.edges() {
		if strings.Contains(e.ResourceRef, "SECRET") {
			t.Errorf("messages content leaked into an edge ref: %+v", e)
		}
	}
}

// --- Dialect pin + drift findings ---------------------------------------------

func TestDialectDriftFindingOncePerRun(t *testing.T) {
	rcv, c := genAIReceiver(t)
	legacy := fixtureOpenLLMetryLegacySpan()
	// Two legacy spans + one v1.36 event + one current span.
	rcv.ingestTraces(exportSpans(nil,
		kindedSpan("openai.chat", tracepb.Span_SPAN_KIND_CLIENT, []byte{3}, legacy...),
		kindedSpan("openai.chat", tracepb.Span_SPAN_KIND_CLIENT, []byte{4}, legacy...),
	))
	rcv.ingestLogs(exportLogs(nil, logRecord(evtGenAIUserMessage, testTime, kvStr(attrGenAISystem, "openai"))))
	rcv.ingestTraces(exportSpans(nil, kindedSpan("chat m", tracepb.Span_SPAN_KIND_CLIENT, []byte{5},
		kvStr(attrGenAIProvider, "anthropic"), kvInt(attrGenAIInTokens, 1))))

	var legacyN, v136N, currentN int
	for _, f := range c.findings() {
		if f.SubjectKind != "genai.dialect" {
			continue
		}
		switch f.SubjectRef {
		case genAIDialectLegacy:
			legacyN++
		case genAIDialectV136:
			v136N++
		default:
			currentN++
		}
		if f.Kind != "drift" || f.Severity != model.SeverityInfo {
			t.Errorf("dialect finding wrong kind/severity: %+v", f)
		}
	}
	if legacyN != 1 || v136N != 1 {
		t.Errorf("want exactly one drift finding per deprecated dialect, got legacy=%d v136=%d", legacyN, v136N)
	}
	if currentN != 0 {
		t.Errorf("the current dialect must not produce a drift finding, got %d", currentN)
	}
}

func TestGenAIPostureFinding(t *testing.T) {
	f := genAIPostureFinding(testTime)
	if f.Kind != "posture" || f.SubjectKind != "genai.semconv" || f.SubjectRef != genAISemconvVersion {
		t.Errorf("posture self-audit wrong: %+v", f)
	}
	if !strings.Contains(f.Title, genAISemconvVersion) || !strings.Contains(f.Title, "Development") {
		t.Errorf("posture title must carry the pin and the Development status: %q", f.Title)
	}
}

// TestGatherEmitsGenAIPostureFinding runs the REAL Gather with the profile
// opted in and asserts the once-per-run semconv self-audit is dispatched — the
// auditor-provable record of what this run could normalize. The constructor
// test above cannot catch the call site being deleted; this one does.
func TestGatherEmitsGenAIPostureFinding(t *testing.T) {
	s := New()
	cfg := sdk.Config{Settings: map[string]string{
		cfgEnableGRPC:   "false",
		cfgEnableHTTP:   "true",
		cfgHTTPAddr:     "127.0.0.1:0",
		cfgSemconvOptIn: genAIOptInToken,
	}}
	if err := s.Open(t.Context(), cfg); err != nil {
		t.Fatalf("open: %v", err)
	}
	sink := &fakeSink{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Gather(ctx, sink) }()

	waitFor(t, 2*time.Second, func() bool {
		for _, f := range sink.findings() {
			if f.SubjectKind == "genai.semconv" {
				return true
			}
		}
		return false
	})
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Gather did not stop")
	}
	var posture []model.FindingReport
	for _, f := range sink.findings() {
		if f.SubjectKind == "genai.semconv" {
			posture = append(posture, f)
		}
	}
	if len(posture) != 1 || posture[0].Kind != "posture" || posture[0].SubjectRef != genAISemconvVersion {
		t.Fatalf("want exactly one genai.semconv posture finding from Gather, got %+v", posture)
	}
}

// --- mcp.* (v1.39) -------------------------------------------------------------

func TestMCPToolCallSpanMapsServerAndToolEdges(t *testing.T) {
	rcv, c := genAIReceiver(t)
	span := kindedSpan("tools/call read_file", tracepb.Span_SPAN_KIND_CLIENT, []byte{6},
		kvStr(attrMCPMethod, mcpMethodToolsCall),
		kvStr(attrGenAIOperation, opExecuteTool), // the spec-merged shape
		kvStr(attrGenAIToolName, "read_file"),
		kvStr(attrMCPSession, "mcp-sess-1"),
		kvStr(attrServerAddress, "localhost"),
		kvInt(attrServerPort, 3001),
		kvStr(attrMCPProtocolVersion, "2025-06-18"),
	)
	rcv.ingestTraces(exportSpans(nil, span))

	srv := edgesOfKind(c, resMCPServer)
	if len(srv) != 1 || srv[0].ResourceRef != "localhost:3001" || srv[0].OriginRef != "mcp-sess-1" || srv[0].OriginKind != originSession {
		t.Errorf("MCP server edge wrong: %+v", srv)
	}
	tools := edgesOfKind(c, resMCP)
	if len(tools) != 1 || tools[0].ResourceRef != "localhost:3001/read_file" || tools[0].ToolRef != "read_file" {
		t.Errorf("MCP tool edge wrong: %+v", tools)
	}
	// The plain genai.tool kind must NOT double-emit for the same call.
	if n := len(edgesOfKind(c, resGenAITool)); n != 0 {
		t.Errorf("an MCP tool call must classify as mcp.tool, not genai.tool (%d)", n)
	}
}

func TestMCPToolCallWithoutGenAIOperation(t *testing.T) {
	// An MCP-only instrumentation may omit gen_ai.operation.name; the method +
	// gen_ai.tool.name must still map the execution.
	rcv, c := genAIReceiver(t)
	rcv.ingestTraces(exportSpans(nil, kindedSpan("tools/call q", tracepb.Span_SPAN_KIND_CLIENT, []byte{7},
		kvStr(attrMCPMethod, mcpMethodToolsCall),
		kvStr(attrGenAIToolName, "query_db"),
		kvStr(attrMCPSession, "mcp-sess-2"),
	)))
	tools := edgesOfKind(c, resMCP)
	if len(tools) != 1 || tools[0].ResourceRef != "query_db" {
		t.Errorf("MCP-only tool call not mapped: %+v", tools)
	}
}

func TestMCPResourceReadIsAReadModeEdge(t *testing.T) {
	rcv, c := genAIReceiver(t)
	rcv.ingestTraces(exportSpans(nil, kindedSpan("resources/read", tracepb.Span_SPAN_KIND_CLIENT, []byte{8},
		kvStr(attrMCPMethod, mcpMethodResourcesRead),
		kvStr(attrMCPResourceURI, "postgres://user:hunter2@database/customers/schema?token=abc"),
		kvStr(attrMCPSession, "mcp-sess-3"),
	)))
	res := edgesOfKind(c, resMCPResource)
	if len(res) != 1 || res[0].Mode != model.ModeRead {
		t.Fatalf("resources/read must map a read-mode mcp.resource edge: %+v", res)
	}
	if strings.Contains(res[0].ResourceRef, "hunter2") || strings.Contains(res[0].ResourceRef, "token=abc") {
		t.Errorf("resource URI not sanitized: %q", res[0].ResourceRef)
	}
}

func TestMCPPromptsGetMapsPromptEdge(t *testing.T) {
	rcv, c := genAIReceiver(t)
	rcv.ingestTraces(exportSpans(nil, kindedSpan("prompts/get analyze-code", tracepb.Span_SPAN_KIND_CLIENT, []byte{9},
		kvStr(attrMCPMethod, mcpMethodPromptsGet),
		kvStr(attrGenAIPromptName, "analyze-code"),
		kvStr(attrMCPSession, "mcp-sess-4"),
	)))
	p := edgesOfKind(c, resMCPPrompt)
	if len(p) != 1 || p[0].ResourceRef != "analyze-code" || p[0].Mode != model.ModeRead {
		t.Errorf("prompts/get must map a read-mode mcp.prompt edge: %+v", p)
	}
}

func TestMCPServerSideSpanDegradesToLiveness(t *testing.T) {
	// A SERVER-kind MCP span is the server's own view; its origin is the remote
	// client — not an identity this connector can attribute. Liveness only.
	rcv, c := genAIReceiver(t)
	rcv.ingestTraces(exportSpans(nil, kindedSpan("tools/call x", tracepb.Span_SPAN_KIND_SERVER, []byte{10},
		kvStr(attrMCPMethod, mcpMethodToolsCall),
		kvStr(attrGenAIToolName, "x"),
		kvStr(attrMCPSession, "mcp-sess-5"),
	)))
	if n := len(c.edges()); n != 0 {
		t.Errorf("a SERVER-kind MCP span must map no edges, got %d", n)
	}
}

func TestMCPMetricBatchFeedsLiveness(t *testing.T) {
	rm := &metricspb.ResourceMetrics{
		Resource: &resourcepb.Resource{Attributes: []*commonpb.KeyValue{kvStr(attrMCPSession, "mcp-sess-6")}},
		ScopeMetrics: []*metricspb.ScopeMetrics{{Metrics: []*metricspb.Metric{
			{Name: "mcp.client.operation.duration"},
		}}},
	}
	if !isGenAIMetricBatch(rm) {
		t.Fatal("an mcp.* metric batch must be recognized")
	}
	if genAIMetricKey(rm) != "mcp-sess-6" {
		t.Errorf("mcp.session.id must key metric liveness, got %q", genAIMetricKey(rm))
	}
}

// --- invoke_agent client/internal split (v1.41) --------------------------------

// fixtureAutoGenInvokeAgent mirrors autogen-core's _genai.py verbatim: span
// "invoke_agent {name}" HARD-CODED to SpanKind CLIENT for an IN-PROCESS agent,
// gen_ai.system="autogen" (the deprecated key), no server.address.
func fixtureAutoGenInvokeAgent() *tracepb.Span {
	return kindedSpan("invoke_agent researcher", tracepb.Span_SPAN_KIND_CLIENT, []byte{11},
		kvStr(attrGenAIOperation, opInvokeAgent),
		kvStr(attrGenAISystem, "autogen"),
		kvStr(attrGenAIAgentName, "researcher"),
		kvStr(attrGenAIConvID, "team-run-1"),
	)
}

func TestInvokeAgentClientWithoutAddressIsNotRemote(t *testing.T) {
	// AutoGen (and Microsoft Agent Framework) hard-code CLIENT for in-process
	// agents — the spec's kind alone would misclassify them as remote. The
	// discriminating fact is server.address.
	rcv, c := genAIReceiver(t)
	rcv.ingestTraces(exportSpans(nil, fixtureAutoGenInvokeAgent()))
	if n := len(edgesOfKind(c, resGenAIAgentRemote)); n != 0 {
		t.Errorf("an in-process CLIENT invoke_agent must not map a remote edge (%d)", n)
	}
	// The conversation→agent attribution edge still captures the invocation.
	if !hasEdge(c, resGenAIAgent, "researcher", "team-run-1") {
		t.Error("conversation→agent attribution edge missing")
	}
}

func TestInvokeAgentClientWithAddressIsRemote(t *testing.T) {
	rcv, c := genAIReceiver(t)
	rcv.ingestTraces(exportSpans(nil, kindedSpan("invoke_agent assistant-1", tracepb.Span_SPAN_KIND_CLIENT, []byte{12},
		kvStr(attrGenAIOperation, opInvokeAgent),
		kvStr(attrGenAIProvider, "openai"),
		kvStr(attrGenAIAgentName, "assistant-1"),
		kvStr(attrGenAIConvID, "conv-r1"),
		kvStr(attrServerAddress, "agents.example.com"),
		kvInt(attrServerPort, 443),
	)))
	remote := edgesOfKind(c, resGenAIAgentRemote)
	if len(remote) != 1 {
		t.Fatalf("want one remote-agent edge, got %d", len(remote))
	}
	e := remote[0]
	if e.OriginKind != originSession || e.OriginRef != "conv-r1" || e.ResourceRef != "assistant-1" {
		t.Errorf("remote-agent edge wrong: %+v", e)
	}
}

func TestInvokeAgentInternalADKShape(t *testing.T) {
	// Google ADK: default (INTERNAL) kind, no provider/system, no inference
	// fields (it anticipates their v1.41 removal), conversation.id set.
	rcv, c := genAIReceiver(t)
	rcv.ingestTraces(exportSpans(nil, kindedSpan("invoke_agent helper", tracepb.Span_SPAN_KIND_INTERNAL, []byte{13},
		kvStr(attrGenAIOperation, opInvokeAgent),
		kvStr(attrGenAIAgentName, "helper"),
		kvStr("gen_ai.agent.description", "task helper"),
		kvStr(attrGenAIConvID, "adk-sess-1"),
	)))
	if n := len(edgesOfKind(c, resGenAIAgentRemote)); n != 0 {
		t.Errorf("an INTERNAL invoke_agent must not map a remote edge (%d)", n)
	}
	if !hasEdge(c, resGenAIAgent, "helper", "adk-sess-1") {
		t.Error("conversation→agent attribution edge missing for the ADK shape")
	}
}

// TestAgentEdgeSurvivesChildFirstExportOrder pins the dedup fix: in real
// OTLP export order child spans END — and export — before their parent, so an
// agentless chat span of the conversation arrives BEFORE the invoke_agent span
// naming the agent. The once-per-conversation slot must not be consumed by the
// agentless signal (the in-process invoke_agent degradation contract leans on
// this edge existing), and a conversation running SEVERAL agents must
// attribute each one.
func TestAgentEdgeSurvivesChildFirstExportOrder(t *testing.T) {
	rcv, c := genAIReceiver(t)
	// 1) The child chat span exports first: conversation id, NO agent identity.
	rcv.ingestTraces(exportSpans(nil, kindedSpan("chat m", tracepb.Span_SPAN_KIND_CLIENT, []byte{20},
		kvStr(attrGenAIOperation, "chat"),
		kvStr(attrGenAIProvider, "openai"),
		kvStr(attrGenAIConvID, "thread-1"),
	)))
	// 2) The parent invoke_agent span exports after, naming the agent.
	rcv.ingestTraces(exportSpans(nil, kindedSpan("invoke_agent researcher", tracepb.Span_SPAN_KIND_INTERNAL, []byte{21},
		kvStr(attrGenAIOperation, opInvokeAgent),
		kvStr(attrGenAIAgentName, "researcher"),
		kvStr(attrGenAIConvID, "thread-1"),
	)))
	if !hasEdge(c, resGenAIAgent, "researcher", "thread-1") {
		t.Fatal("the conversation→agent edge must survive an agentless signal arriving first")
	}
	// 3) A second, distinct agent on the SAME conversation is also attributed…
	rcv.ingestTraces(exportSpans(nil, kindedSpan("invoke_agent critic", tracepb.Span_SPAN_KIND_INTERNAL, []byte{22},
		kvStr(attrGenAIOperation, opInvokeAgent),
		kvStr(attrGenAIAgentName, "critic"),
		kvStr(attrGenAIConvID, "thread-1"),
	)))
	if !hasEdge(c, resGenAIAgent, "critic", "thread-1") {
		t.Fatal("a second agent on the same conversation must also be attributed")
	}
	// …while a re-emission of the first agent stays deduped (once per pair).
	rcv.ingestTraces(exportSpans(nil, kindedSpan("invoke_agent researcher", tracepb.Span_SPAN_KIND_INTERNAL, []byte{23},
		kvStr(attrGenAIOperation, opInvokeAgent),
		kvStr(attrGenAIAgentName, "researcher"),
		kvStr(attrGenAIConvID, "thread-1"),
	)))
	n := 0
	for _, e := range edgesOfKind(c, resGenAIAgent) {
		if e.ResourceRef == "researcher" {
			n++
		}
	}
	if n != 1 {
		t.Errorf("the (conversation, agent) pair must be attributed exactly once, got %d", n)
	}
}

// TestServerAddrWithoutPort pins the port-less server.address shape (an HTTPS
// endpoint without an explicit port): the MCP server edge and the remote
// invoke_agent classification both gate on serverAddr being non-empty.
func TestServerAddrWithoutPort(t *testing.T) {
	rcv, c := genAIReceiver(t)
	rcv.ingestTraces(exportSpans(nil, kindedSpan("invoke_agent remote-helper", tracepb.Span_SPAN_KIND_CLIENT, []byte{24},
		kvStr(attrGenAIOperation, opInvokeAgent),
		kvStr(attrGenAIAgentName, "remote-helper"),
		kvStr(attrGenAIConvID, "conv-np"),
		kvStr(attrServerAddress, "agents.example.com"), // no server.port
	)))
	remote := edgesOfKind(c, resGenAIAgentRemote)
	if len(remote) != 1 || remote[0].ResourceRef != "remote-helper" {
		t.Fatalf("a port-less server.address must still classify remote: %+v", remote)
	}
}

// TestMCPSessionOnlyOrigin pins callerRef's mcp.session.id leg: a remote
// invocation attributed only by its MCP session still maps, and a span with NO
// attributable origin at all degrades to liveness (no edge, never a guess).
func TestMCPSessionOnlyOrigin(t *testing.T) {
	rcv, c := genAIReceiver(t)
	rcv.ingestTraces(exportSpans(nil, kindedSpan("invoke_agent a", tracepb.Span_SPAN_KIND_CLIENT, []byte{25},
		kvStr(attrGenAIOperation, opInvokeAgent),
		kvStr(attrGenAIAgentName, "a"),
		kvStr(attrMCPSession, "mcp-only-1"), // no conversation id
		kvStr(attrServerAddress, "h"), kvInt(attrServerPort, 1),
	)))
	remote := edgesOfKind(c, resGenAIAgentRemote)
	if len(remote) != 1 || remote[0].OriginRef != "mcp-only-1" || remote[0].OriginKind != originSession {
		t.Fatalf("the MCP session must serve as the caller origin: %+v", remote)
	}

	// No origin at all → no remote edge and no workflow edge (clean degradation).
	rcv2, c2 := genAIReceiver(t)
	rcv2.ingestTraces(exportSpans(nil,
		kindedSpan("invoke_agent b", tracepb.Span_SPAN_KIND_CLIENT, []byte{26},
			kvStr(attrGenAIOperation, opInvokeAgent),
			kvStr(attrServerAddress, "h"), kvInt(attrServerPort, 1)),
		kindedSpan("invoke_workflow w", tracepb.Span_SPAN_KIND_INTERNAL, []byte{27},
			kvStr(attrGenAIOperation, opInvokeWorkflow),
			kvStr(attrGenAIWorkflowName, "w")),
	))
	if n := len(edgesOfKind(c2, resGenAIAgentRemote)) + len(edgesOfKind(c2, resGenAIWorkflow)); n != 0 {
		t.Errorf("origin-less delegation spans must map no edges, got %d", n)
	}
}

func TestInvokeWorkflowMapsWorkflowEdge(t *testing.T) {
	rcv, c := genAIReceiver(t)
	rcv.ingestTraces(exportSpans(nil, kindedSpan("invoke_workflow research-crew", tracepb.Span_SPAN_KIND_INTERNAL, []byte{14},
		kvStr(attrGenAIOperation, opInvokeWorkflow),
		kvStr(attrGenAIProvider, "openai"),
		kvStr(attrGenAIWorkflowName, "research-crew"),
		kvStr(attrGenAIConvID, "crew-run-2"),
	)))
	wf := edgesOfKind(c, resGenAIWorkflow)
	if len(wf) != 1 || wf[0].ResourceRef != "research-crew" || wf[0].OriginRef != "crew-run-2" {
		t.Errorf("invoke_workflow edge wrong: %+v", wf)
	}
	// The workflow attribute WITHOUT the operation (a child span) must not re-emit.
	rcv2, c2 := genAIReceiver(t)
	rcv2.ingestTraces(exportSpans(nil, kindedSpan("chat m", tracepb.Span_SPAN_KIND_CLIENT, []byte{15},
		kvStr(attrGenAIOperation, "chat"),
		kvStr(attrGenAIProvider, "openai"),
		kvStr(attrGenAIWorkflowName, "research-crew"),
	)))
	if n := len(edgesOfKind(c2, resGenAIWorkflow)); n != 0 {
		t.Errorf("a child span carrying the workflow attr must not emit a workflow edge (%d)", n)
	}
}
