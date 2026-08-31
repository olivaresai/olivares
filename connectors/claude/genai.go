// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package claude

import (
	"encoding/hex"
	"strings"
	"sync"
	"time"

	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	logspb "go.opentelemetry.io/proto/otlp/logs/v1"
	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"

	"github.com/olivaresai/olivares/sdk/model"
)

// OBS-01 / AIP-06 — OpenTelemetry GenAI semantic-conventions ingest profile.
//
// Before this profile, the OTLP receiver mapped ONLY Anthropic's proprietary
// claude_code.* schema, and the routeOTEL switch had no default — so a vendor-
// neutral producer emitting gen_ai.* (the OpenAI SDK, LangGraph, CrewAI, AutoGen,
// LlamaIndex, Pydantic-AI, Strands, …) fed the silence watchdog but produced NO
// edge/cost: it was effectively dropped. This profile maps gen_ai.* into the SAME
// observation/cost/edge pipeline, so any OTel-instrumented agent feeds the R/RW
// access-map and FinOps, not just Claude Code.
//
// HARDENS this profile against the shapes real agent frameworks ACTUALLY emit,
// because the gen_ai conventions are in Development and their emitters lag the spec:
//
//   - Dual-name ingest (survive the semconv churn). The profile reads BOTH the
//     current keys AND the deprecated predecessors still emitted in the wild:
//     gen_ai.provider.name ← falls back to gen_ai.system (the default emit for
//     v1.36.0-or-prior instrumentations, and what Google ADK tags, e.g.
//     "gcp.vertex.agent"); gen_ai.usage.input_tokens/output_tokens ← fall back to
//     gen_ai.usage.prompt_tokens/completion_tokens (what OpenLLMetry/Traceloop —
//     which instruments LangChain/LangGraph/CrewAI — emits). Reading only the newest
//     names silently read zero tokens / no provider for the majority of fleets.
//   - Span ingest. LangGraph/LangChain, CrewAI, AutoGen/Microsoft Agent Framework
//     and Google ADK put gen_ai.* on trace SPANS, not log records. parseGenAISpan
//     feeds those spans into the same mapper (otlp.go:ingestTraces), so a span-based
//     fleet stops being liveness-only.
//   - W3C-span-id cost de-dup. An operation reported on BOTH its span and its
//     gen_ai.client.inference.operation.details log event shares one span id; cost is
//     counted once (genAICostDedup), so dual signals do not double-bill FinOps.
//   - Metric recognition. gen_ai client metrics (gen_ai.client.token.usage, …) are
//     aggregates: they feed liveness but are NOT costed (the span/log event is the
//     authoritative per-operation usage; costing the metric too would double-count).
//
// WIDENS the profile from dual-name reads to a full MULTI-DIALECT
// NORMALIZER: the three GenAI generations that coexist in 2026 fleets (legacy
// OpenLLMetry, the v1.36-or-prior per-message events, the v1.37+ messages
// generation) are detected per signal and normalized into the same genAIEvent,
// each stamped with the semconv pin of the vocabulary that was read
// (genAIEvent.semconv; see genai_dialect.go). The MCP conventions (v1.39) and
// the invoke_agent client/internal split + invoke_workflow (v1.41) join the
// same pipeline (genai_mcp.go). Message CONTENT is never read from ANY
// generation — content keys are dialect markers only (OBS-10 minimal data).
//
// Pinned to semconv v1.41.1 PLUS an upstream ref, VERIFIED against primary
// sources 2026-07-05. semconv v1.42.0 (2026-06-12, #3696) deprecated/moved
// gen_ai.* (and openai.*/mcp.*) from the main semantic-conventions repo to
// https://github.com/open-telemetry/semantic-conventions-genai; the main repo's
// v1.43.0 model keeps only deprecated "Moved" stubs. v1.41.1 (2026-05-11) is
// therefore the LAST VERSIONED vocabulary label emitted on wire/table surfaces;
// genAISemconvUpstreamRef names the semconv-genai commit whose live docs shape
// this mapper was re-verified against (repo releases page: 0 releases as of
// 2026-07-05; README Schema URL still TODO). Re-verified at main@c321d7e against
// docs/gen-ai/*: gen_ai.provider.name/operation/model/usage/cache keys, client
// token/duration metrics and boundaries, Anthropic provider/cache-token math,
// create_agent/invoke_agent Development spans, and open issue #35 for broader
// agentic semconv. The whole gen_ai area is DEVELOPMENT (not Stable), so the
// profile is OPT-IN by design: it activates only when the operator sets the
// connector's semconv_opt_in to include the exact token the spec defines,
// "gen_ai_latest_experimental" (mirroring OTEL_SEMCONV_STABILITY_OPT_IN). We do
// NOT claim a stability the conventions do not have. The dual-name
// reads and the dialect normalizer are precisely so the ingest does not depend
// on which side of that churn an emitter is on.
//
// Source URLs, all accessed 2026-07-05: releases
// https://github.com/open-telemetry/semantic-conventions-genai/releases; commit
// https://github.com/open-telemetry/semantic-conventions-genai/commit/c321d7eb4443ae1d1d88c2e24eda849f62049008;
// schema TODO https://github.com/open-telemetry/semantic-conventions-genai/blob/c321d7eb4443ae1d1d88c2e24eda849f62049008/README.md;
// mapped docs docs/gen-ai/{gen-ai-spans.md,gen-ai-metrics.md,anthropic.md,gen-ai-agent-spans.md,mcp.md};
// agentic issue https://github.com/open-telemetry/semantic-conventions-genai/issues/35;
// main-repo moved stubs
// https://github.com/open-telemetry/semantic-conventions/blob/v1.43.0/model/gen-ai/deprecated/registry-deprecated.yaml.
const (
	// genAISemconvVersion is the pinned, verified semconv release this profile
	// maps (cited by the contract; also the genAIDialectCurrent pin and the
	// posture self-audit value). It is the last VERSIONED vocabulary label; the
	// live Development repo ref below records the verified upstream shape.
	// MIRRORED (unexported, no live coupling) by modules/observability
	// otelGenAIVersion (ingestion.go) and modules/recording semconvVersion
	// (chain.go): keep all three equal — a drift is a one-grep fix, guarded by a
	// pin test in each package.
	genAISemconvVersion = "1.41.1"
	// genAISemconvUpstreamRepo/genAISemconvUpstreamRef pin the unversioned
	// semconv-genai authority the mapping was re-verified against (accessed
	// 2026-07-05). MIRRORED by modules/observability and modules/recording; no
	// package imports another across the license boundary.
	genAISemconvUpstreamRepo = "open-telemetry/semantic-conventions-genai"
	genAISemconvUpstreamRef  = "main@c321d7e, verified 2026-07-05"
	// genAIOptInToken is the OTEL_SEMCONV_STABILITY_OPT_IN value that selects the
	// latest experimental GenAI conventions (semconv 1.41.1).
	genAIOptInToken = "gen_ai_latest_experimental"
	// genAICostDedupCap bounds how many recent span ids the cost de-dup remembers, so
	// a long-running receiver cannot grow it without limit. Sized for a busy fleet's
	// in-flight operations; the oldest id is evicted past it.
	genAICostDedupCap = 4096
)

// gen_ai.* attribute keys. Each maps a CURRENT semconv key and, where the spec
// renamed it, the DEPRECATED predecessor still emitted by real frameworks — read
// together so the profile survives the Development-status churn.
const (
	// attrGenAIProvider is the current provider key; attrGenAISystem is its deprecated
	// predecessor (gen_ai.system), still the default emit for v1.36.0-or-prior
	// instrumentations and what Google ADK tags (e.g. "gcp.vertex.agent").
	attrGenAIProvider = "gen_ai.provider.name"
	attrGenAISystem   = "gen_ai.system"

	attrGenAIOperation = "gen_ai.operation.name"
	attrGenAIReqModel  = "gen_ai.request.model"
	attrGenAIRespModel = "gen_ai.response.model"

	// attrGenAIInTokens/attrGenAIOutTokens are the current usage keys;
	// attrGenAIPromptTokens/attrGenAICompletionTokens are the deprecated predecessors
	// that OpenLLMetry/Traceloop (LangChain/LangGraph/CrewAI) still emits.
	attrGenAIInTokens         = "gen_ai.usage.input_tokens"
	attrGenAIOutTokens        = "gen_ai.usage.output_tokens"
	attrGenAIPromptTokens     = "gen_ai.usage.prompt_tokens"
	attrGenAICompletionTokens = "gen_ai.usage.completion_tokens"

	attrGenAIAgentID    = "gen_ai.agent.id"
	attrGenAIAgentName  = "gen_ai.agent.name"
	attrGenAIToolName   = "gen_ai.tool.name"
	attrGenAIToolCallID = "gen_ai.tool.call.id"
	attrGenAIConvID     = "gen_ai.conversation.id"
)

// gen_ai.operation.name values this profile distinguishes (semconv 1.41.1 enum).
const opExecuteTool = "execute_tool"

// gen_ai resource kinds for the access/cost/identity edges.
const (
	resGenAITool  = "genai.tool"
	resGenAIAgent = "genai.agent"
)

// genAIOptIn reports whether the operator opted into the experimental gen_ai
// conventions (semconv being in Development, the profile is off unless they did).
func genAIOptIn(semconvOptIn string) bool {
	for _, tok := range strings.Split(semconvOptIn, ",") {
		if strings.TrimSpace(tok) == genAIOptInToken {
			return true
		}
	}
	return false
}

// firstNonEmptyStr returns the first non-empty argument, or "". It lets the dual-name
// reads prefer the current semconv key and fall back to the deprecated one.
func firstNonEmptyStr(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// hexID renders an OTLP trace/span id (raw bytes) as the lowercase-hex W3C Trace
// Context textual form. Empty bytes yield "" — no correlation/de-dup is possible.
func hexID(b []byte) string {
	if len(b) == 0 {
		return ""
	}
	return hex.EncodeToString(b)
}

// genAIEvent is the connector's normalized view of one gen_ai.* signal — a log
// record (a v1.36 per-message event or the v1.37+ operation.details event), a
// trace span (any of the three dialects, plus mcp.* and agent spans), or a
// metric batch — all mapped into the same shape, stamped with the semconv pin
// of the dialect that was read.
type genAIEvent struct {
	at             time.Time
	spanID         string // W3C span id (hex), for cross-signal cost de-dup; "" if absent
	semconv        string // detected-dialect pin: genAIDialect{Current,V136,Legacy}
	spanKind       tracepb.Span_SpanKind
	operation      string
	provider       string
	requestModel   string
	responseModel  string
	inputTokens    int64
	outputTokens   int64
	hasTokens      bool
	agentID        string
	agentName      string
	toolName       string
	toolCallID     string
	conversationID string
	workflowName   string // gen_ai.workflow.name (v1.41 invoke_workflow)
	mcpMethod      string // mcp.method.name (v1.39)
	mcpSession     string // mcp.session.id (v1.39)
	mcpResourceURI string // mcp.resource.uri (v1.39; sanitized before any ref)
	mcpPromptName  string // gen_ai.prompt.name (v1.39 MCP prompts)
	serverAddr     string // server.address[:port] — the remote side of a CLIENT span
}

// model returns the response model if present, else the request model.
func (e genAIEvent) model() string {
	if e.responseModel != "" {
		return e.responseModel
	}
	return e.requestModel
}

// originRef returns the most precise attribution for an edge: the agent (id, then
// name) if named, else the conversation — or the MCP session (v1.39) — as a
// session. Empty ref => not attributable.
func (e genAIEvent) originRef() (kind, ref string) {
	switch {
	case e.agentID != "":
		return "agent", e.agentID
	case e.agentName != "":
		return "agent", e.agentName
	case e.conversationID != "":
		return originSession, e.conversationID
	case e.mcpSession != "":
		return originSession, e.mcpSession
	default:
		return "", ""
	}
}

// livenessKey is the session-ish key the silence watchdog and cost attribution
// use. The MCP session and workflow name are last-resort keys so an
// MCP-only or workflow-only fleet still proves liveness.
func (e genAIEvent) livenessKey() string {
	return firstNonEmptyStr(e.conversationID, e.agentID, e.agentName, e.mcpSession, e.workflowName)
}

// isGenAIRecord reports whether an attribute view carries the gen_ai conventions.
// It is the discriminator that routes a record/span/metric to the gen_ai mapper
// instead of the claude_code.* parser. It is deliberately broad — it accepts the
// CURRENT and the DEPRECATED provider/usage keys, the agent/tool/conversation
// keys, the v1.37+ messages markers, the legacy OpenLLMetry markers, the mcp.*
// conventions (v1.39) and the workflow identity (v1.41) — so a framework that
// emits only gen_ai.system + gen_ai.usage.prompt_tokens (the OpenLLMetry shape),
// only gen_ai.agent.name (an agent-lifecycle span), or only mcp.method.name (an
// MCP client span) is still recognized. Every key tested is gen_ai./mcp./llm./
// traceloop.-prefixed, so a claude_code.* record (session.id / tool_name /
// model / …) never matches.
func isGenAIRecord(a attrs) bool {
	for _, k := range []string{
		attrGenAIOperation, attrGenAIProvider, attrGenAISystem,
		attrGenAIAgentID, attrGenAIAgentName, attrGenAIToolName, attrGenAIConvID,
		attrGenAIInTokens, attrGenAIPromptTokens,
		attrGenAIInputMessages, attrGenAIOutputMessages, attrGenAISystemInstructions,
		attrGenAIWorkflowName, attrMCPMethod,
		attrLegacyRequestType, attrLegacyVendor, attrLegacyTotalTokens, attrTraceloopSpanKind,
	} {
		if a.has(k) {
			return true
		}
	}
	return hasLegacyIndexedContent(a)
}

// isGenAISignal widens isGenAIRecord with the event-name discriminator: a
// v1.36 per-message event carries AT MOST one attribute (gen_ai.system,
// Recommended — i.e. possibly absent), so it is recognizable only by its name;
// the v1.37+ operation.details event is matched by name for the same reason.
func isGenAISignal(a attrs, eventName string) bool {
	return isGenAIPerMessageEvent(eventName) || eventName == evtGenAIOpDetails || isGenAIRecord(a)
}

// genAITokens reads input/output usage, accepting BOTH the current semconv names
// (gen_ai.usage.input_tokens/output_tokens) AND the deprecated predecessors still
// emitted in the wild — gen_ai.usage.prompt_tokens/completion_tokens (OpenLLMetry/
// Traceloop, which instruments LangChain/LangGraph/CrewAI) — so the ingest survives
// the semconv churn instead of silently reading zero tokens.
func genAITokens(a attrs) (in, out int64, has bool) {
	in, okIn := a.intVal(attrGenAIInTokens)
	if !okIn {
		in, okIn = a.intVal(attrGenAIPromptTokens)
	}
	out, okOut := a.intVal(attrGenAIOutTokens)
	if !okOut {
		out, okOut = a.intVal(attrGenAICompletionTokens)
	}
	return in, out, okIn || okOut
}

// genAIEventFromAttrs builds the normalized event from a merged attribute view,
// the signal's timestamp, its W3C span id, and the log-event name ("" for spans
// and metrics). It is shared by the log-record, span and metric paths so all
// three map every dialect identically (dual-name reads included). The provider
// chain reads the v1.37+ key, then the v1.36-or-prior gen_ai.system, then the
// pre-v0.17 OpenLLMetry llm.vendor; a legacy provider value is lowercased
// because OpenLLMetry capitalizes ("OpenAI", "Langchain") and a case-split
// provider would fragment FinOps attribution.
func genAIEventFromAttrs(a attrs, at time.Time, spanID, eventName string) genAIEvent {
	in, out, has := genAITokens(a)
	dialect := detectGenAIDialect(a, eventName)
	provider := firstNonEmptyStr(a.str(attrGenAIProvider), a.str(attrGenAISystem), a.str(attrLegacyVendor))
	if dialect == genAIDialectLegacy {
		provider = strings.ToLower(provider)
	}
	return genAIEvent{
		at:             at,
		spanID:         spanID,
		semconv:        dialect,
		operation:      a.str(attrGenAIOperation),
		provider:       provider,
		requestModel:   a.str(attrGenAIReqModel),
		responseModel:  a.str(attrGenAIRespModel),
		inputTokens:    in,
		outputTokens:   out,
		hasTokens:      has,
		agentID:        a.str(attrGenAIAgentID),
		agentName:      a.str(attrGenAIAgentName),
		toolName:       a.str(attrGenAIToolName),
		toolCallID:     a.str(attrGenAIToolCallID),
		conversationID: a.str(attrGenAIConvID),
		workflowName:   a.str(attrGenAIWorkflowName),
		mcpMethod:      a.str(attrMCPMethod),
		mcpSession:     a.str(attrMCPSession),
		mcpResourceURI: a.str(attrMCPResourceURI),
		mcpPromptName:  a.str(attrGenAIPromptName),
		serverAddr:     serverAddr(a),
	}
}

// parseGenAIRecord parses an OTLP LogRecord that carries the gen_ai conventions into
// a genAIEvent, or reports false if it is not a gen_ai record (so the caller falls
// back to the claude_code.* parse path). This is the gen_ai EVENT path — the v1.37+
// gen_ai.client.inference.operation.details event AND the deprecated v1.36-or-prior
// per-message events (recognized by NAME: their single attribute is optional, so a
// bare event would otherwise be invisible). Event BODIES are never read — a
// per-message event's body is conversation content (OBS-10).
func parseGenAIRecord(rec *logspb.LogRecord, resource []*commonpb.KeyValue) (genAIEvent, bool) {
	if rec == nil {
		return genAIEvent{}, false
	}
	a := newAttrs(resource, rec.GetAttributes())
	name := rec.GetEventName()
	if name == "" {
		name = a.str(attrEventName)
	}
	if !isGenAISignal(a, name) {
		return genAIEvent{}, false
	}
	return genAIEventFromAttrs(a, recordTime(rec), hexID(rec.GetSpanId()), name), true
}

// parseGenAISpan parses an OTLP trace Span that carries the gen_ai conventions, or
// reports false if it is not a gen_ai span (so the caller falls back to the
// claude_code.* identity/liveness path). This is the shape LangGraph/LangChain,
// CrewAI, AutoGen/Microsoft Agent Framework and Google ADK ACTUALLY emit — they put
// gen_ai.* on trace SPANS, not log records — so without this path a framework fleet
// was fed only liveness and produced no edge/cost. The span KIND is preserved: it is
// the v1.41 invoke_agent client/internal discriminator and gates the MCP server-side
// span degradation. A zero start time is left zero for the caller to stamp to
// receive time.
func parseGenAISpan(span *tracepb.Span, resource []*commonpb.KeyValue) (genAIEvent, bool) {
	if span == nil {
		return genAIEvent{}, false
	}
	a := newAttrs(resource, span.GetAttributes())
	if !isGenAISignal(a, "") {
		return genAIEvent{}, false
	}
	ev := genAIEventFromAttrs(a, spanTime(span.GetStartTimeUnixNano(), time.Time{}), hexID(span.GetSpanId()), "")
	ev.spanKind = span.GetKind()
	return ev, true
}

// isGenAIMetricBatch reports whether a resource-metrics batch is vendor-neutral
// gen_ai telemetry — recognized by a gen_ai.* or mcp.* metric instrument name
// (gen_ai.client.token.usage, mcp.client.operation.duration, … — the four MCP
// duration histograms are v1.39) or a gen_ai.*/mcp.* resource attribute — so it
// is mapped by the gen_ai liveness path instead of the claude_code.* identity
// path.
func isGenAIMetricBatch(rm *metricspb.ResourceMetrics) bool {
	if isGenAIRecord(newAttrs(rm.GetResource().GetAttributes())) {
		return true
	}
	for _, sm := range rm.GetScopeMetrics() {
		for _, m := range sm.GetMetrics() {
			if strings.HasPrefix(m.GetName(), "gen_ai.") || strings.HasPrefix(m.GetName(), "mcp.") {
				return true
			}
		}
	}
	return false
}

// genAIMetricKey returns the best liveness key carried at RESOURCE scope by a gen_ai
// metric batch (conversation, then agent id, then agent name, then MCP session), or
// "" when the batch carries no stable id. gen_ai client metrics are aggregates and
// rarely carry a per-conversation id, so an empty key is expected — it simply feeds
// no liveness (it is never fabricated). Metric token VALUES are deliberately not
// read: the span/log event is the authoritative per-operation usage, so costing the
// metric too would double-count.
func genAIMetricKey(rm *metricspb.ResourceMetrics) string {
	a := newAttrs(rm.GetResource().GetAttributes())
	return firstNonEmptyStr(a.str(attrGenAIConvID), a.str(attrGenAIAgentID), a.str(attrGenAIAgentName), a.str(attrMCPSession))
}

// genAICostDedup remembers the W3C span ids whose token usage was already turned
// into a CostSample, so an operation reported on BOTH its span and its
// gen_ai.client.inference.operation.details log event — they share the span id — is
// costed once, not twice. It is bounded (genAICostDedupCap) so a long-running
// receiver cannot grow it without limit. An empty span id is never de-duped (it
// cannot be correlated), so a producer without trace context still costs its records.
// It is safe for concurrent use by the receiver's gRPC/HTTP handlers.
type genAICostDedup struct {
	mu    sync.Mutex
	seen  map[string]struct{}
	order []string
	cap   int
}

// newGenAICostDedup returns a cost de-dup bounded to capacity span ids.
func newGenAICostDedup(capacity int) *genAICostDedup {
	return &genAICostDedup{seen: make(map[string]struct{}), cap: capacity}
}

// first reports whether spanID's cost has not yet been counted, recording it. An
// empty span id is always "first" (uncorrelatable signals each cost once).
func (d *genAICostDedup) first(spanID string) bool {
	if spanID == "" {
		return true
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if _, ok := d.seen[spanID]; ok {
		return false
	}
	d.seen[spanID] = struct{}{}
	d.order = append(d.order, spanID)
	if len(d.order) > d.cap {
		oldest := d.order[0]
		d.order = d.order[1:]
		delete(d.seen, oldest)
	}
	return true
}

// costFromGenAI builds a CostSample from a gen_ai record's usage. gen_ai usage
// carries token counts but NOT a billed cost, so CostMicroUSD is 0 and provenance is
// estimated — the sample is the cross-vendor TOKEN signal for FinOps, attributed to
// the provider/model. Returns false when no usage tokens were reported.
func costFromGenAI(e genAIEvent) (model.CostSample, bool) {
	if !e.hasTokens || (e.inputTokens == 0 && e.outputTokens == 0) {
		return model.CostSample{}, false
	}
	provider := e.provider
	if provider == "" {
		provider = "unknown"
	}
	return model.CostSample{
		ProviderRef:  provider,
		ModelRef:     e.model(),
		SessionRef:   e.livenessKey(),
		InputTokens:  e.inputTokens,
		OutputTokens: e.outputTokens,
		OccurredAt:   e.at,
		Provenance:   model.ProvenanceEstimated,
	}, true
}

// toolEdgeFromGenAI builds the access edge for a tool execution: a gen_ai
// execute_tool operation, OR an MCP tools/call span (v1.39 — the spec merges
// the two when both instrumentations are present, and SHOULD set execute_tool
// on the merged span, but an MCP-only instrumentation may omit the gen_ai
// operation; reading the method too keeps that fleet mapped). A tool executed
// VIA MCP is classified as resMCP ("mcp.tool" — the kind the claude_code
// mcp__server__tool invocations use, so both signal planes land on one
// inventory axis), referenced as "server/tool" when the server endpoint is
// known. The R/W mode is Unknown (gen_ai does not carry it), like the
// MCP-connection edge. Returns false unless the operation is a tool execution
// with a named tool and an attributable origin.
func toolEdgeFromGenAI(e genAIEvent) (model.EdgeObservation, bool) {
	mcpCall := e.mcpMethod == mcpMethodToolsCall
	if (e.operation != opExecuteTool && !mcpCall) || e.toolName == "" {
		return model.EdgeObservation{}, false
	}
	// An MCP SERVER-side span is the server's own view of a call the CLIENT
	// span already attributes; mapping it too would re-state the same fact from
	// the wrong vantage (the same degradation rule as mcpEdgesFromGenAI).
	if mcpCall && e.spanKind == tracepb.Span_SPAN_KIND_SERVER {
		return model.EdgeObservation{}, false
	}
	kind, ref := e.originRef()
	if ref == "" {
		return model.EdgeObservation{}, false
	}
	resKind, resRef := resGenAITool, e.toolName
	if mcpCall {
		resKind = resMCP
		if e.serverAddr != "" {
			resRef = e.serverAddr + "/" + e.toolName
		}
	}
	return model.EdgeObservation{
		OriginKind:   kind,
		OriginRef:    ref,
		ResourceKind: resKind,
		ResourceRef:  resRef,
		Mode:         model.ModeUnknown,
		Source:       model.SignalOTEL,
		Confidence:   model.ConfidenceAttributed,
		ToolRef:      e.toolName,
		ObservedAt:   e.at,
	}, true
}

// agentEdgeFromGenAI links a conversation to the agent that ran it (the gen_ai
// analog of the OBS-09 identity edge), so attribution is per-agent, not just
// per-conversation. Returns false unless both a conversation and an agent are named.
func agentEdgeFromGenAI(e genAIEvent) (model.EdgeObservation, bool) {
	agent := e.agentName
	if agent == "" {
		agent = e.agentID
	}
	if e.conversationID == "" || agent == "" {
		return model.EdgeObservation{}, false
	}
	return model.EdgeObservation{
		OriginKind:   originSession,
		OriginRef:    e.conversationID,
		ResourceKind: resGenAIAgent,
		ResourceRef:  agent,
		Mode:         model.ModeUnknown,
		Source:       model.SignalOTEL,
		Confidence:   model.ConfidenceAttributed,
		ObservedAt:   e.at,
	}, true
}
