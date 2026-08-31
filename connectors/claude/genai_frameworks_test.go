// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package claude

import (
	"testing"
	"time"

	colmetricspb "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	coltracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"

	"github.com/olivaresai/olivares/sdk/model"
)

// These tests pin the ingest against the gen_ai.* shapes the four target agent
// frameworks ACTUALLY emit (verified jun-2026 against the OTel GenAI semconv and
// each framework's instrumentation). The recurring theme is the Development-status
// CHURN: emitters straddle the semconv rename (gen_ai.system → gen_ai.provider.name,
// gen_ai.usage.prompt_tokens/completion_tokens → input_tokens/output_tokens) and put
// gen_ai.* on trace SPANS, not log records. The ingest must read both name sets and
// both signals, or a real framework fleet maps to nothing.

// --- OTLP span/metric construction helpers ----------------------------------

func exportSpans(resAttrs []*commonpb.KeyValue, spans ...*tracepb.Span) *coltracepb.ExportTraceServiceRequest {
	return &coltracepb.ExportTraceServiceRequest{ResourceSpans: []*tracepb.ResourceSpans{{
		Resource:   &resourcepb.Resource{Attributes: resAttrs},
		ScopeSpans: []*tracepb.ScopeSpans{{Spans: spans}},
	}}}
}

func genAISpan(name string, spanID []byte, start time.Time, attrs ...*commonpb.KeyValue) *tracepb.Span {
	sp := &tracepb.Span{Name: name, Attributes: attrs, SpanId: spanID}
	if !start.IsZero() {
		sp.StartTimeUnixNano = uint64(start.UnixNano())
	}
	return sp
}

func exportMetrics(resAttrs []*commonpb.KeyValue, names ...string) *colmetricspb.ExportMetricsServiceRequest {
	ms := make([]*metricspb.Metric, 0, len(names))
	for _, n := range names {
		ms = append(ms, &metricspb.Metric{Name: n})
	}
	return &colmetricspb.ExportMetricsServiceRequest{ResourceMetrics: []*metricspb.ResourceMetrics{{
		Resource:     &resourcepb.Resource{Attributes: resAttrs},
		ScopeMetrics: []*metricspb.ScopeMetrics{{Metrics: ms}},
	}}}
}

// genAIReceiver builds a receiver with the gen_ai profile ON, returning the
// collector. onOTEL/onSignal fail the test if hit: a gen_ai signal must route to the
// gen_ai pipeline, never the claude_code path.
func genAIReceiver(t *testing.T) (*receiver, *collect) {
	t.Helper()
	c := &collect{}
	s := &Source{cfg: config{genAIProfile: true, correlationWait: time.Second}}
	wd := newWatchdog(time.Minute, c.emit)
	return &receiver{
		onOTEL:        func(claudeEvent) { t.Error("a gen_ai signal must not route to the claude_code log path") },
		onGenAI:       s.genAIRouter(wd, newIdentitySeen(), c.emit),
		onGenAIMetric: s.genAIMetricLiveness(wd),
		onSignal: func(claudeIdentity, time.Time) {
			t.Error("a gen_ai signal must not feed the claude_code identity signal")
		},
		now: func() time.Time { return testTime },
	}, c
}

// oneCost returns the single CostSample in c or fails.
func oneCost(t *testing.T, c *collect) model.CostSample {
	t.Helper()
	costs := costObs(c)
	if len(costs) != 1 {
		t.Fatalf("want exactly 1 CostSample, got %d", len(costs))
	}
	return costs[0]
}

// hasEdge reports whether c saw an edge with the given resource kind/ref and origin.
func hasEdge(c *collect, resKind, resRef, originRef string) bool {
	for _, e := range c.edges() {
		if e.ResourceKind == resKind && e.ResourceRef == resRef && e.OriginRef == originRef {
			return true
		}
	}
	return false
}

// --- Framework fixtures (real gen_ai.* shapes) ------------------------------

// fixtureLangChainSpan is an OpenLLMetry/Traceloop LLM span — the instrumentation
// LangChain/LangGraph fleets use. It carries the DEPRECATED gen_ai.system and
// gen_ai.usage.prompt_tokens/completion_tokens names Traceloop still emits.
func fixtureLangChainSpan() []*commonpb.KeyValue {
	return []*commonpb.KeyValue{
		kvStr(attrGenAIOperation, "chat"),
		kvStr(attrGenAISystem, "openai"),
		kvStr(attrGenAIReqModel, "gpt-4o"),
		kvStr(attrGenAIRespModel, "gpt-4o-2024-08-06"),
		kvInt(attrGenAIPromptTokens, 1200),
		kvInt(attrGenAICompletionTokens, 350),
		kvStr(attrGenAIConvID, "lc-thread-1"),
	}
}

// fixtureCrewAISpan is a CrewAI invoke_agent span (OTel export): an agent runs inside
// a crew conversation. Token names are the deprecated ones (OpenLLMetry).
func fixtureCrewAISpan() []*commonpb.KeyValue {
	return []*commonpb.KeyValue{
		kvStr(attrGenAIOperation, "invoke_agent"),
		kvStr(attrGenAISystem, "openai"),
		kvStr(attrGenAIReqModel, "gpt-4o-mini"),
		kvInt(attrGenAIPromptTokens, 800),
		kvInt(attrGenAICompletionTokens, 120),
		kvStr(attrGenAIAgentName, "researcher"),
		kvStr(attrGenAIConvID, "crew-run-7"),
	}
}

// fixtureMSAgentFrameworkSpan is a Microsoft Agent Framework (AutoGen successor)
// execute_tool span: it uses the CURRENT semconv names (provider.name, input/output
// tokens) and the full agent/tool/conversation identity.
func fixtureMSAgentFrameworkSpan() []*commonpb.KeyValue {
	return []*commonpb.KeyValue{
		kvStr(attrGenAIOperation, opExecuteTool),
		kvStr(attrGenAIProvider, "az.ai.inference"),
		kvStr(attrGenAIReqModel, "gpt-4o"),
		kvInt(attrGenAIInTokens, 640),
		kvInt(attrGenAIOutTokens, 75),
		kvStr(attrGenAIAgentID, "asst_123"),
		kvStr(attrGenAIAgentName, "weather-agent"),
		kvStr(attrGenAIToolName, "get_weather"),
		kvStr(attrGenAIToolCallID, "call_abc"),
		kvStr(attrGenAIConvID, "thread_xyz"),
	}
}

// fixtureGoogleADKSpan is a Google ADK call_llm span: ADK tags the provider via the
// DEPRECATED gen_ai.system (e.g. "gcp.gemini") and uses the current token names.
func fixtureGoogleADKSpan() []*commonpb.KeyValue {
	return []*commonpb.KeyValue{
		kvStr(attrGenAIOperation, "chat"),
		kvStr(attrGenAISystem, "gcp.gemini"),
		kvStr(attrGenAIReqModel, "gemini-2.0-flash"),
		kvInt(attrGenAIInTokens, 2048),
		kvInt(attrGenAIOutTokens, 512),
		kvStr(attrGenAIAgentName, "weather_agent"),
		kvStr(attrGenAIConvID, "adk-session-1"),
	}
}

// --- Framework span mapping tests -------------------------------------------

// TestGenAILangChainSpanDualName proves the dual-name ingest: a Traceloop span using
// the deprecated gen_ai.system + prompt/completion token names maps to a CostSample,
// where reading only the current names would have read zero tokens / no provider.
func TestGenAILangChainSpanDualName(t *testing.T) {
	rcv, c := genAIReceiver(t)
	rcv.ingestTraces(exportSpans(nil, genAISpan("chat gpt-4o", []byte("lcspan01"), testTime, fixtureLangChainSpan()...)))

	cs := oneCost(t, c)
	if cs.ProviderRef != "openai" {
		t.Errorf("provider must come from gen_ai.system fallback: got %q", cs.ProviderRef)
	}
	if cs.ModelRef != "gpt-4o-2024-08-06" {
		t.Errorf("model must prefer response model: got %q", cs.ModelRef)
	}
	if cs.InputTokens != 1200 || cs.OutputTokens != 350 {
		t.Errorf("tokens must come from the deprecated prompt/completion names: in=%d out=%d", cs.InputTokens, cs.OutputTokens)
	}
	if cs.SessionRef != "lc-thread-1" {
		t.Errorf("session ref must be the conversation id: %q", cs.SessionRef)
	}
	if cs.Provenance != model.ProvenanceEstimated {
		t.Errorf("gen_ai cost is estimated (token signal, no billed cost): %v", cs.Provenance)
	}
}

// TestGenAICrewAISpan proves a CrewAI invoke_agent span maps cost AND the
// conversation→agent attribution edge.
func TestGenAICrewAISpan(t *testing.T) {
	rcv, c := genAIReceiver(t)
	rcv.ingestTraces(exportSpans(nil, genAISpan("invoke_agent researcher", []byte("crewspn1"), testTime, fixtureCrewAISpan()...)))

	cs := oneCost(t, c)
	if cs.ProviderRef != "openai" || cs.InputTokens != 800 || cs.OutputTokens != 120 {
		t.Errorf("crewai cost wrong: %+v", cs)
	}
	if !hasEdge(c, resGenAIAgent, "researcher", "crew-run-7") {
		t.Errorf("missing conversation→agent edge: %+v", c.edges())
	}
}

// TestGenAIMSAgentFrameworkSpan proves the current-name path: an execute_tool span
// maps cost, the agent→tool access edge, and the conversation→agent edge.
func TestGenAIMSAgentFrameworkSpan(t *testing.T) {
	rcv, c := genAIReceiver(t)
	rcv.ingestTraces(exportSpans(nil, genAISpan("execute_tool get_weather", []byte("msaf0001"), testTime, fixtureMSAgentFrameworkSpan()...)))

	cs := oneCost(t, c)
	if cs.ProviderRef != "az.ai.inference" || cs.ModelRef != "gpt-4o" || cs.InputTokens != 640 || cs.OutputTokens != 75 {
		t.Errorf("MS Agent Framework cost wrong: %+v", cs)
	}
	// execute_tool → the agent (preferred over the conversation) touched the tool.
	if !hasEdge(c, resGenAITool, "get_weather", "asst_123") {
		t.Errorf("missing agent→tool access edge: %+v", c.edges())
	}
	// conversation → agent (name preferred over id) attribution.
	if !hasEdge(c, resGenAIAgent, "weather-agent", "thread_xyz") {
		t.Errorf("missing conversation→agent edge: %+v", c.edges())
	}
}

// TestGenAIGoogleADKSpan proves ADK's provider (carried on the deprecated
// gen_ai.system as "gcp.gemini") and Gemini token usage map correctly.
func TestGenAIGoogleADKSpan(t *testing.T) {
	rcv, c := genAIReceiver(t)
	rcv.ingestTraces(exportSpans(nil, genAISpan("call_llm", []byte("adkspan1"), testTime, fixtureGoogleADKSpan()...)))

	cs := oneCost(t, c)
	if cs.ProviderRef != "gcp.gemini" || cs.ModelRef != "gemini-2.0-flash" {
		t.Errorf("ADK provider/model wrong: %+v", cs)
	}
	if cs.InputTokens != 2048 || cs.OutputTokens != 512 {
		t.Errorf("ADK tokens wrong: %+v", cs)
	}
}

// TestGenAISpanStampsReceiveTime proves a span with no start time is stamped to the
// receiver clock (so cost/edges are never zero-timed).
func TestGenAISpanStampsReceiveTime(t *testing.T) {
	rcv, c := genAIReceiver(t)
	rcv.ingestTraces(exportSpans(nil, genAISpan("chat", []byte("notime01"), time.Time{}, fixtureGoogleADKSpan()...)))
	if cs := oneCost(t, c); !cs.OccurredAt.Equal(testTime) {
		t.Errorf("a zero-start span must be stamped to receive time: %v", cs.OccurredAt)
	}
}

// --- Trace Context cost de-dup ----------------------------------------------

// TestGenAICostDedupAcrossSpanAndEvent is the double-count regression: when the same
// operation arrives on BOTH its trace span and its gen_ai.client.inference.operation.
// details log event (they share one W3C span id), the token usage is costed ONCE.
// Edges may re-emit (the engine de-dups them by natural key); cost is the summed
// quantity that must not double.
func TestGenAICostDedupAcrossSpanAndEvent(t *testing.T) {
	rcv, c := genAIReceiver(t)
	sid := []byte("dedup001")

	// The span carries the usage.
	rcv.ingestTraces(exportSpans(nil, genAISpan("execute_tool get_weather", sid, testTime, fixtureMSAgentFrameworkSpan()...)))
	// The matching operation.details log event repeats the usage under the SAME span id.
	rec := logRecord("gen_ai.client.inference.operation.details", testTime, fixtureMSAgentFrameworkSpan()...)
	rec.SpanId = sid
	rcv.ingestLogs(exportLogs(nil, rec))

	if costs := costObs(c); len(costs) != 1 {
		t.Fatalf("the same operation on a span AND its log event must cost once, got %d cost samples", len(costs))
	}
	// A different span id is a different operation: it costs again.
	rec2 := logRecord("gen_ai.client.inference.operation.details", testTime, fixtureMSAgentFrameworkSpan()...)
	rec2.SpanId = []byte("dedup002")
	rcv.ingestLogs(exportLogs(nil, rec2))
	if costs := costObs(c); len(costs) != 2 {
		t.Fatalf("a distinct span id must cost again, got %d", len(costs))
	}
}

// TestGenAICostNoSpanIDNotDeduped proves an operation without trace context is never
// suppressed: two tokenful records with no span id each cost (they are not
// correlatable, so de-dup must not silently drop the second).
func TestGenAICostNoSpanIDNotDeduped(t *testing.T) {
	rcv, c := genAIReceiver(t)
	rcv.ingestLogs(exportLogs(nil, logRecord("gen_ai.client.inference.operation.details", testTime, fixtureLangChainSpan()...)))
	rcv.ingestLogs(exportLogs(nil, logRecord("gen_ai.client.inference.operation.details", testTime, fixtureLangChainSpan()...)))
	if costs := costObs(c); len(costs) != 2 {
		t.Fatalf("records with no span id must each cost, got %d", len(costs))
	}
}

// --- Metric recognition (liveness only, never cost) -------------------------

// TestGenAIMetricsRecognizedNeverCosted proves a gen_ai client metric batch is
// recognized as gen_ai (not the claude_code identity path) and its token aggregate is
// NEVER turned into cost — the span/log event is authoritative, so costing the metric
// too would double-count.
func TestGenAIMetricsRecognizedNeverCosted(t *testing.T) {
	c := &collect{}
	s := &Source{cfg: config{genAIProfile: true}}
	wd := newWatchdog(time.Minute, c.emit)
	claudeSignals := 0
	rcv := &receiver{
		onGenAI:       s.genAIRouter(wd, newIdentitySeen(), c.emit),
		onGenAIMetric: s.genAIMetricLiveness(wd),
		onSignal:      func(claudeIdentity, time.Time) { claudeSignals++ },
		now:           func() time.Time { return testTime },
	}
	rcv.ingestMetrics(exportMetrics(
		[]*commonpb.KeyValue{kvStr(attrGenAIAgentName, "weather-agent")},
		"gen_ai.client.token.usage", "gen_ai.client.operation.duration",
	))
	if claudeSignals != 0 {
		t.Error("a gen_ai metric batch must not feed the claude_code identity signal")
	}
	if n := len(costObs(c)); n != 0 {
		t.Fatalf("gen_ai metrics must never be costed (no double-count), got %d", n)
	}
	// A claude_code metric batch still takes the identity path (unchanged behavior).
	rcv.ingestMetrics(&colmetricspb.ExportMetricsServiceRequest{ResourceMetrics: []*metricspb.ResourceMetrics{{
		Resource:     &resourcepb.Resource{Attributes: []*commonpb.KeyValue{kvStr(attrSessionID, "sess-1")}},
		ScopeMetrics: []*metricspb.ScopeMetrics{{Metrics: []*metricspb.Metric{{Name: "claude_code.session.count"}}}},
	}}})
	if claudeSignals != 1 {
		t.Errorf("a claude_code metric batch must feed the identity signal, got %d", claudeSignals)
	}
}

// --- Unit coverage for the dual-name primitives -----------------------------

func TestGenAITokensDualName(t *testing.T) {
	cur := newAttrs([]*commonpb.KeyValue{kvInt(attrGenAIInTokens, 10), kvInt(attrGenAIOutTokens, 3)})
	if in, out, has := genAITokens(cur); !has || in != 10 || out != 3 {
		t.Errorf("current names: in=%d out=%d has=%v", in, out, has)
	}
	dep := newAttrs([]*commonpb.KeyValue{kvInt(attrGenAIPromptTokens, 11), kvInt(attrGenAICompletionTokens, 4)})
	if in, out, has := genAITokens(dep); !has || in != 11 || out != 4 {
		t.Errorf("deprecated names: in=%d out=%d has=%v", in, out, has)
	}
	// Current names win when both are present (a transitional emitter).
	both := newAttrs([]*commonpb.KeyValue{
		kvInt(attrGenAIInTokens, 99), kvInt(attrGenAIPromptTokens, 1),
		kvInt(attrGenAIOutTokens, 88), kvInt(attrGenAICompletionTokens, 2),
	})
	if in, out, _ := genAITokens(both); in != 99 || out != 88 {
		t.Errorf("current names must win over deprecated: in=%d out=%d", in, out)
	}
	if _, _, has := genAITokens(newAttrs(nil)); has {
		t.Error("no token attrs must report has=false")
	}
}

func TestIsGenAIRecordDeprecatedOnly(t *testing.T) {
	// A record carrying ONLY deprecated keys is still recognized as gen_ai.
	if !isGenAIRecord(newAttrs([]*commonpb.KeyValue{kvStr(attrGenAISystem, "openai")})) {
		t.Error("gen_ai.system alone must be recognized as gen_ai")
	}
	if !isGenAIRecord(newAttrs([]*commonpb.KeyValue{kvInt(attrGenAIPromptTokens, 5)})) {
		t.Error("gen_ai.usage.prompt_tokens alone must be recognized as gen_ai")
	}
	// A claude_code record is not gen_ai.
	if isGenAIRecord(newAttrs([]*commonpb.KeyValue{kvStr(attrSessionID, "s1"), kvStr(attrToolName, "Read")})) {
		t.Error("a claude_code record must not be recognized as gen_ai")
	}
}

func TestGenAIMetricKeyResourceScope(t *testing.T) {
	rm := &metricspb.ResourceMetrics{Resource: &resourcepb.Resource{Attributes: []*commonpb.KeyValue{
		kvStr(attrGenAIAgentName, "weather-agent"),
	}}}
	if k := genAIMetricKey(rm); k != "weather-agent" {
		t.Errorf("metric liveness key = %q", k)
	}
	// Conversation id is preferred when present.
	rm.Resource.Attributes = append(rm.Resource.Attributes, kvStr(attrGenAIConvID, "conv-9"))
	if k := genAIMetricKey(rm); k != "conv-9" {
		t.Errorf("conversation id must be the preferred liveness key, got %q", k)
	}
	// No gen_ai id → empty key (never fabricated).
	if k := genAIMetricKey(&metricspb.ResourceMetrics{}); k != "" {
		t.Errorf("absent id must yield empty key, got %q", k)
	}
}

func TestGenAIMetricLivenessNilWhenOff(t *testing.T) {
	off := &Source{cfg: config{genAIProfile: false}}
	if off.genAIMetricLiveness(newWatchdog(time.Minute, func(model.Observation) {})) != nil {
		t.Error("metric liveness must be nil when the profile is off")
	}
	on := &Source{cfg: config{genAIProfile: true}}
	if on.genAIMetricLiveness(newWatchdog(time.Minute, func(model.Observation) {})) == nil {
		t.Error("metric liveness must be non-nil when the profile is on")
	}
}
