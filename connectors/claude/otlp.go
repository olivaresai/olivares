// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package claude

import (
	"context"
	"io"
	"net/http"
	"strings"
	"time"

	collogspb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	colmetricspb "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	coltracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// maxOTLPBody caps an OTLP/HTTP request body. Claude Code batches are small; this
// guards against a hostile or runaway poster on the receiver socket.
const maxOTLPBody = 16 << 20 // 16 MiB

// receiver turns incoming Claude Code OTLP into parsed events and serves the hook
// endpoint. It owns no sockets — claude.go binds and runs the gRPC/HTTP servers
// around the handlers it exposes — so the receiver itself is trivially testable.
// Every parsed OTEL log record is handed to onOTEL; every hook to onHook.
type receiver struct {
	onOTEL func(claudeEvent)
	// onGenAI handles a vendor-neutral gen_ai.* log record OR trace span (OBS-01,
	// hardened in to ingest spans, the shape frameworks actually emit). It is nil
	// unless the operator opted into the experimental GenAI conventions
	// (semconv_opt_in); when nil, a gen_ai.* record falls through to the
	// claude_code.* path (which feeds liveness but maps no edge/cost).
	onGenAI func(genAIEvent)
	// onGenAIMetric feeds liveness for a vendor-neutral gen_ai.* METRIC batch.
	// It is nil unless the gen_ai profile is on. gen_ai client metrics are aggregates
	// (gen_ai.client.token.usage, …); their values are deliberately NOT costed here
	// (the gen_ai span/log event carries the authoritative per-operation usage —
	// costing the metric too would double-count), but a recognized batch still proves
	// the fleet is alive, so it feeds the silence watchdog keyed by the agent/
	// conversation it carries.
	onGenAIMetric func(key string, at time.Time)
	onHook        func(hookEvent)
	// onHookDecide optionally returns a governed permission decision for a hook
	// (CLA-01). Nil means cooperative-only: hooks are observed, never gated.
	onHookDecide func(hookEvent) hookDecision
	// onSignal receives the session/identity + time of a trace span or metric
	// batch (OBS-03/04). Traces and metrics are mapped for liveness + identity
	// attribution only — STRUCTURAL by construction: span events (tool content,
	// raw API bodies) are never read, honoring the OBS-10 default-redacted posture.
	onSignal func(claudeIdentity, time.Time)
	// onMetric receives the VALUE of one recognized Claude Code adoption metric
	// datapoint. Nil unless the operator left claude_code_metrics on (the
	// default): when nil the receiver stays liveness-only and the values are dropped,
	// exactly as before. It is distinct from onSignal (which feeds liveness from the
	// SAME batch); both run for a metrics export.
	onMetric func(claudeMetric)
	// onAgentSpan receives the subagent identity/hierarchy a claude_code.
	// llm_request or claude_code.tool span carries (agent_id/parent_agent_id
	// are SPAN-ONLY attributes — 2.1.139/2.1.145). Nil = hierarchy not consumed.
	onAgentSpan func(sessionID, agentID, parentAgentID string, at time.Time)
	// labelKeys is the operator's resource_labels allowlist: the
	// OTEL_RESOURCE_ATTRIBUTES keys honored as attribution labels. Empty = off.
	labelKeys []string
	now       func() time.Time
}

// Span names that carry the subagent identity attributes (VERIFIED
// 2026-06-10, monitoring-usage Traces section).
const (
	spanLLMRequest = "claude_code.llm_request"
	spanTool       = "claude_code.tool"
)

// ingestLogs walks an OTLP logs export request and forwards each recognized
// Claude Code event to onOTEL, stamping receive time when the record carried
// none. Resource-level attributes (session/identity) are merged into each record.
func (r *receiver) ingestLogs(req *collogspb.ExportLogsServiceRequest) {
	for _, rl := range req.GetResourceLogs() {
		resAttrs := rl.GetResource().GetAttributes()
		for _, sl := range rl.GetScopeLogs() {
			for _, rec := range sl.GetLogRecords() {
				// OBS-01: when the gen_ai profile is enabled, a vendor-neutral gen_ai.*
				// record is mapped here instead of the claude_code.* path. A record that
				// is NOT gen_ai (every claude_code.* event) returns false and falls
				// through, so the existing path is unchanged.
				if r.onGenAI != nil {
					if gev, ok := parseGenAIRecord(rec, resAttrs); ok {
						if gev.at.IsZero() {
							gev.at = r.now()
						}
						r.onGenAI(gev)
						continue
					}
				}
				ev, ok := parseLogRecord(rec, resAttrs)
				if !ok {
					continue
				}
				if ev.at.IsZero() {
					ev.at = r.now()
				}
				// collect the operator-allowlisted resource-attribute labels
				// here — the allowlist is CONFIG, which the pure parser does not
				// see. Record attrs win over resource attrs (newAttrs order).
				if len(r.labelKeys) > 0 {
					ev.labels = labelsFromAttrs(newAttrs(resAttrs, rec.GetAttributes()), r.labelKeys)
				}
				r.onOTEL(ev)
			}
		}
	}
}

// --- gRPC services -----------------------------------------------------------

// logsService is the OTLP/gRPC LogsService: the one signal this connector maps.
type logsService struct {
	collogspb.UnimplementedLogsServiceServer
	r *receiver
}

// Export ingests a logs batch and acknowledges full success (empty partial).
func (s *logsService) Export(_ context.Context, req *collogspb.ExportLogsServiceRequest) (*collogspb.ExportLogsServiceResponse, error) {
	s.r.ingestLogs(req)
	return &collogspb.ExportLogsServiceResponse{}, nil
}

// metricsService maps OTLP metrics (the claude_code.* operational signals) for
// liveness + identity attribution (OBS-04) AND, opt-in, persists the
// productivity/adoption metric VALUES as MetricSamples. The one value never carried
// is cost.usage: cost rides the authoritative api_request path and a metrics
// consumer, so counting it here too would double-count — the receiver maps the
// non-cost counts (lines/commits/PRs/sessions/tokens/edit-decisions/active-time) only.
type metricsService struct {
	colmetricspb.UnimplementedMetricsServiceServer
	r *receiver
}

// Export maps a metrics batch (liveness/identity) and acknowledges full success.
func (s *metricsService) Export(_ context.Context, req *colmetricspb.ExportMetricsServiceRequest) (*colmetricspb.ExportMetricsServiceResponse, error) {
	s.r.ingestMetrics(req)
	return &colmetricspb.ExportMetricsServiceResponse{}, nil
}

// traceService maps OTLP traces (the Claude Code tracing beta) for liveness +
// identity attribution (OBS-03). It reads only STRUCTURAL span attributes
// (session/org/account/agent + timing); span EVENTS (tool content, raw API
// bodies) are never read, honoring the default-redacted posture (OBS-10).
// Persisting the trace TREE (trace_id/span_id/parent on edges/findings to follow
// the agent→tool→subprocess→MCP chain) needs an additive observation field and is
// coordinated with the observability-interop work.
type traceService struct {
	coltracepb.UnimplementedTraceServiceServer
	r *receiver
}

// Export maps a trace batch (liveness/identity) and acknowledges full success.
func (s *traceService) Export(_ context.Context, req *coltracepb.ExportTraceServiceRequest) (*coltracepb.ExportTraceServiceResponse, error) {
	s.r.ingestTraces(req)
	return &coltracepb.ExportTraceServiceResponse{}, nil
}

// registerGRPC registers the OTLP services on a gRPC server.
func (r *receiver) registerGRPC(g *grpc.Server) {
	collogspb.RegisterLogsServiceServer(g, &logsService{r: r})
	colmetricspb.RegisterMetricsServiceServer(g, &metricsService{r: r})
	coltracepb.RegisterTraceServiceServer(g, &traceService{r: r})
}

// ingestTraces walks an OTLP traces export and feeds each span's session/identity
// to onSignal (liveness + attribution). Only structural span attributes are read;
// span events are never inspected (OBS-10).
func (r *receiver) ingestTraces(req *coltracepb.ExportTraceServiceRequest) {
	for _, rs := range req.GetResourceSpans() {
		resAttrs := rs.GetResource().GetAttributes()
		for _, ss := range rs.GetScopeSpans() {
			for _, span := range ss.GetSpans() {
				// OBS-01: a vendor-neutral gen_ai.* SPAN — the shape LangGraph/
				// LangChain, CrewAI, AutoGen/Microsoft Agent Framework and Google ADK
				// actually emit (they put gen_ai.* on spans, not log records) — maps to
				// the SAME cost/edge pipeline as a gen_ai.* log record when the profile is
				// on. A span that is NOT gen_ai (every claude_code.* span) returns false
				// and falls through to the identity/liveness path below.
				if r.onGenAI != nil {
					if gev, ok := parseGenAISpan(span, resAttrs); ok {
						if gev.at.IsZero() {
							gev.at = r.now()
						}
						r.onGenAI(gev)
						continue
					}
				}
				a := newAttrs(resAttrs, span.GetAttributes())
				id := identityFromAttrs(a, r.labelKeys)
				if id.sessionID == "" {
					continue
				}
				at := spanTime(span.GetStartTimeUnixNano(), r.now())
				if r.onSignal != nil {
					r.onSignal(id, at)
				}
				// the subagent identity/hierarchy rides ONLY these two span
				// names (agent_id absent = the main session — no hierarchy fact).
				if r.onAgentSpan != nil && (span.GetName() == spanLLMRequest || span.GetName() == spanTool) {
					if agentID := a.str(attrAgentID); agentID != "" {
						r.onAgentSpan(id.sessionID, agentID, a.str(attrParentAgentID), at)
					}
				}
			}
		}
	}
}

// ingestMetrics walks an OTLP metrics export and feeds each resource batch's
// session/identity to onSignal. Identity attributes are resource-level (the
// standard attributes Claude Code tags on every record), so one signal per
// resource-metrics batch is enough for liveness + attribution. When onMetric is
// wired the SAME batch's datapoint VALUES are also lifted into MetricSamples
// (ingestMetricValues, metrics.go) — except cost.usage (see metricsService).
func (r *receiver) ingestMetrics(req *colmetricspb.ExportMetricsServiceRequest) {
	for _, rm := range req.GetResourceMetrics() {
		// OBS-01: recognize a vendor-neutral gen_ai.* metric batch
		// (gen_ai.client.token.usage, gen_ai.client.operation.duration, …). It feeds
		// liveness keyed by the agent/conversation it carries, but its token VALUES are
		// deliberately NOT costed — the gen_ai span/log event is the authoritative
		// per-operation usage, so costing the aggregate too would double-count (the same
		// stance the claude_code metric path takes).
		if r.onGenAIMetric != nil && isGenAIMetricBatch(rm) {
			r.onGenAIMetric(genAIMetricKey(rm), r.now())
			continue
		}
		resAttrs := rm.GetResource().GetAttributes()
		id := identityFromAttrs(newAttrs(resAttrs), r.labelKeys)
		if id.sessionID == "" {
			continue
		}
		// Liveness + identity attribution (OBS-04): one signal per resource batch.
		if r.onSignal != nil {
			r.onSignal(id, r.now())
		}
		// persist the metric VALUES (productivity/adoption), per datapoint — the
		// signal the receiver used to discard. Runs alongside the liveness signal above.
		if r.onMetric != nil {
			r.ingestMetricValues(rm, id, resAttrs)
		}
	}
}

// identityFromAttrs extracts the session-scoped Claude identity from an attribute
// view (the standard resource attributes Anthropic tags on every record), plus
// the opt-in app.entrypoint and the operator's allowlisted labels.
func identityFromAttrs(a attrs, labelKeys []string) claudeIdentity {
	return claudeIdentity{
		sessionID:  a.str(attrSessionID),
		orgID:      a.str(attrOrgID),
		accountID:  a.str(attrAccountUUID),
		agentName:  a.str(attrAgentName),
		entrypoint: a.str(attrAppEntrypoint),
		labels:     labelsFromAttrs(a, labelKeys),
	}
}

// spanTime converts a span's start time (unix nanos) to a UTC time, falling back
// to the receiver clock when the producer left it zero.
func spanTime(startNanos uint64, fallback time.Time) time.Time {
	if startNanos == 0 {
		return fallback
	}
	return time.Unix(0, int64(startNanos)).UTC()
}

// --- HTTP handlers -----------------------------------------------------------

// httpHandler builds the OTLP/HTTP mux: /v1/logs, /v1/metrics and /v1/traces (all
// mapped), plus the hook endpoint at hookPath.
func (r *receiver) httpHandler(hookPath string) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/v1/logs", http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		var e collogspb.ExportLogsServiceRequest
		decodeAndAck(w, req, &e, func() { r.ingestLogs(&e) }, &collogspb.ExportLogsServiceResponse{})
	}))
	mux.Handle("/v1/metrics", http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		var e colmetricspb.ExportMetricsServiceRequest
		decodeAndAck(w, req, &e, func() { r.ingestMetrics(&e) }, &colmetricspb.ExportMetricsServiceResponse{})
	}))
	mux.Handle("/v1/traces", http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		var e coltracepb.ExportTraceServiceRequest
		decodeAndAck(w, req, &e, func() { r.ingestTraces(&e) }, &coltracepb.ExportTraceServiceResponse{})
	}))
	mux.Handle(hookPath, hookHandler(r.onHook, r.onHookDecide, r.now))
	return mux
}

// decodeAndAck handles one OTLP/HTTP request: it POST-guards, decodes the body
// (protobuf or JSON) into `into`, runs `ingest`, and answers 200 with `resp` in
// the request's encoding. The JSON path is permissive (DiscardUnknown) so a newer
// Claude Code field never fails ingest.
func decodeAndAck(w http.ResponseWriter, req *http.Request, into proto.Message, ingest func(), resp proto.Message) {
	if req.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(io.LimitReader(req.Body, maxOTLPBody))
	if err != nil {
		http.Error(w, "read error", http.StatusBadRequest)
		return
	}
	isJSON := isJSONContent(req.Header.Get("Content-Type"))
	if derr := unmarshalOTLP(body, isJSON, into); derr != nil {
		http.Error(w, "decode error", http.StatusBadRequest)
		return
	}
	ingest()
	writeOTLP(w, isJSON, resp)
}

// isJSONContent reports whether a Content-Type selects OTLP/JSON (vs protobuf).
func isJSONContent(ct string) bool {
	return strings.HasPrefix(strings.TrimSpace(ct), "application/json")
}

// unmarshalOTLP decodes an OTLP body as JSON or protobuf into msg. The JSON path
// is permissive (DiscardUnknown) so a newer Claude Code field never fails ingest.
func unmarshalOTLP(body []byte, isJSON bool, msg proto.Message) error {
	if isJSON {
		return protojson.UnmarshalOptions{DiscardUnknown: true}.Unmarshal(body, msg)
	}
	return proto.Unmarshal(body, msg)
}

// writeOTLP writes a 200 OTLP response in the matching encoding. A marshal error
// is swallowed to a bare 200 (the body is an empty ack; the client only needs the
// status), so the receiver never fails a successful ingest on response encoding.
func writeOTLP(w http.ResponseWriter, isJSON bool, msg proto.Message) {
	var body []byte
	var err error
	if isJSON {
		w.Header().Set("Content-Type", "application/json")
		body, err = protojson.Marshal(msg)
	} else {
		w.Header().Set("Content-Type", "application/x-protobuf")
		body, err = proto.Marshal(msg)
	}
	w.WriteHeader(http.StatusOK)
	if err == nil {
		_, _ = w.Write(body)
	}
}
