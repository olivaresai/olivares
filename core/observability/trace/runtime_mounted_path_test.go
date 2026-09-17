// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package trace

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	coltracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/protobuf/proto"
)

// Component composition of the tracing seams over real OTLP transport.
//
// SCOPE. The handler, the client and the audit metadata are all owned by this test:
// HTTPMiddleware wraps an httptest handler built here, AnthropicHTTPClient is constructed
// directly, and EnrichAuditMeta is called against an in-memory map. Product authorization
// and the durable audit ledger are outside this fixture, so nothing here is a runtime
// end-to-end path and nothing here is durable evidence.
//
// What it measures: the middleware starts a server span; genAITransport produces a parented
// client gen_ai span for a Messages request; the real exporters carry both to a real OTLP
// receiver; the default is silent; the enrichment helper copies rather than mutates; and no
// payload sentinel leaves.
//
// The production mount points of these components, for orientation:
//
//	core/api/server.go                     trace.HTTPMiddleware
//	cmd/olivares/boot.go                   obstrace.New, tracer.AnthropicHTTPClient
//	cmd/olivares/inferenceproxy.go         tracer.AnthropicHTTPClient for the PEP proxy
//	core/internal/store/sqlstore/audit.go  obstrace.EnrichAuditMeta
//
// LIMIT: no session or tool-call span exists at this source, so no part of this file claims one.

const (
	runtimeService      = "otlp-runtime-mounted-test"
	sentinelPrompt      = "SENTINEL-PROMPT-e1f2a3b4c5d6"
	sentinelAuth        = "SENTINEL-AUTH-0f1e2d3c4b5a"
	sentinelQuery       = "SENTINEL-QUERY-9a8b7c6d5e4f"
	sentinelResponse    = "SENTINEL-RESPONSE-1122334455"
	sentinelSystem      = "SENTINEL-SYSTEM-aabbccddeeff"
	runtimeModel        = "claude-sonnet-4-5"
	runtimeResponseID   = "msg_runtime_mounted_01"
	runtimeUpstreamPath = "/v1/messages"
)

// allSentinels is the census every export is searched for. None is a real credential: each is a
// literal coined here, and the upstream fixture is this process.
func allSentinels() []string {
	return []string{sentinelPrompt, sentinelAuth, sentinelQuery, sentinelResponse, sentinelSystem}
}

// syntheticUpstream answers /v1/messages like the provider would, echoing nothing secret back
// except its own sentinel, and records the traceparent it received.
func syntheticUpstream(t *testing.T, seen *[]string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*seen = append(*seen, r.Header.Get("traceparent"))
		_, _ = io.ReadAll(r.Body)
		w.Header().Set("content-type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":          runtimeResponseID,
			"type":        "message",
			"role":        "assistant",
			"model":       runtimeModel,
			"stop_reason": "end_turn",
			"content":     []map[string]string{{"type": "text", "text": sentinelResponse}},
			"usage":       map[string]int{"input_tokens": 11, "output_tokens": 7},
		})
	}))
	t.Cleanup(srv.Close)
	return srv
}

// messagesRequestBody is an authorized Messages request carrying the prompt sentinel.
func messagesRequestBody() []byte {
	body, _ := json.Marshal(map[string]any{
		"model":      runtimeModel,
		"max_tokens": 16,
		"system":     sentinelSystem,
		"messages":   []map[string]string{{"role": "user", "content": sentinelPrompt}},
	})
	return body
}

// driveComposedSeams runs one request through the real middleware; the handler performs a real
// Messages round trip with the provider's own instrumented client. It returns the map
// EnrichAuditMeta produced — an IN-MEMORY map, not a ledger row. The `decision` key is this
// test's own invention and stands for nothing durable.
func driveComposedSeams(t *testing.T, p *Provider, upstream string, inboundTraceparent string) map[string]any {
	t.Helper()
	var auditMeta map[string]any
	client := p.AnthropicHTTPClient(nil)
	handler := p.HTTPMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The enrichment helper, read from the live request context. In production this is called
		// at the store's audit Append chokepoint; here nothing is appended.
		auditMeta = EnrichAuditMeta(r.Context(), map[string]any{"decision": "allow"})
		out, err := http.NewRequestWithContext(r.Context(), http.MethodPost,
			upstream+runtimeUpstreamPath+"?trace_hint="+sentinelQuery, bytes.NewReader(messagesRequestBody()))
		if err != nil {
			t.Errorf("build the upstream Messages request: %v", err)
			return
		}
		out.Header.Set("content-type", "application/json")
		out.Header.Set("authorization", "Bearer "+sentinelAuth)
		out.Header.Set("anthropic-version", "2023-06-01")
		resp, err := client.Do(out)
		if err != nil {
			t.Errorf("the instrumented Messages round trip failed: %v", err)
			return
		}
		defer func() { _ = resp.Body.Close() }()
		payload, _ := io.ReadAll(resp.Body)
		w.Header().Set("content-type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(payload)
	}))
	req := httptest.NewRequest(http.MethodPost, "/v1/m/gateway/messages?trace_hint="+sentinelQuery, bytes.NewReader(messagesRequestBody()))
	req.Header.Set("authorization", "Bearer "+sentinelAuth)
	if inboundTraceparent != "" {
		req.Header.Set("traceparent", inboundTraceparent)
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("the composed handler answered %d, want 200; a result must survive telemetry", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), runtimeResponseID) {
		t.Fatalf("the handler did not return the upstream result: %q", rec.Body.String())
	}
	return auditMeta
}

// exportedSpans decodes every span the receiver saw, with its resource service name.
type exportedSpan struct {
	name     string
	kind     tracepb.Span_SpanKind
	traceID  string
	spanID   string
	parentID string
	service  string
	attrs    map[string]string
}

func decodeSpans(t *testing.T, r *otlpReceiver) []exportedSpan {
	t.Helper()
	var out []exportedSpan
	for _, q := range r.requests() {
		if q.path != wantTracesPath {
			continue
		}
		var msg coltracepb.ExportTraceServiceRequest
		if err := proto.Unmarshal(q.body, &msg); err != nil {
			t.Fatalf("decode OTLP trace request: %v", err)
		}
		for _, rs := range msg.ResourceSpans {
			service := ""
			for _, kv := range rs.GetResource().GetAttributes() {
				if kv.GetKey() == "service.name" {
					service = kv.GetValue().GetStringValue()
				}
			}
			for _, ss := range rs.ScopeSpans {
				for _, s := range ss.Spans {
					attrs := map[string]string{}
					for _, kv := range s.GetAttributes() {
						attrs[kv.GetKey()] = kv.GetValue().String()
					}
					out = append(out, exportedSpan{
						name: s.GetName(), kind: s.GetKind(),
						traceID:  bytesHex(s.GetTraceId()),
						spanID:   bytesHex(s.GetSpanId()),
						parentID: bytesHex(s.GetParentSpanId()),
						service:  service, attrs: attrs,
					})
				}
			}
		}
	}
	return out
}

func bytesHex(b []byte) string {
	const hextable = "0123456789abcdef"
	out := make([]byte, 0, len(b)*2)
	for _, c := range b {
		out = append(out, hextable[c>>4], hextable[c&0x0f])
	}
	return string(out)
}

// rawExportBytes is every byte the receiver was sent, for the sentinel census. Searching the
// decoded attribute map alone would miss a leak in a span name, an event or a resource value.
func rawExportBytes(r *otlpReceiver) []byte {
	var all []byte
	for _, q := range r.requests() {
		all = append(all, q.body...)
	}
	return all
}

// runtimeConfig is this file's own export configuration. It does not reuse the endpoint
// tests' httpConfig because that helper pins their service name, and the resource assertion
// below is only meaningful against a name this file chose.
func runtimeConfig(endpoint string) Config {
	return Config{
		Enabled: true, Endpoint: endpoint, Protocol: ProtocolHTTP, Insecure: true,
		SampleRatio: 1, ServiceName: runtimeService, ServiceVersion: "test",
	}
}

func newProvider(t *testing.T, cfg Config) *Provider {
	t.Helper()
	p, err := New(context.Background(), cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = p.Shutdown(ctx)
	})
	return p
}

func flush(t *testing.T, p *Provider) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return p.Shutdown(ctx)
}

// DEFAULT POLICY: no endpoint and no explicit enable means nothing leaves, and correlation
// still holds from the inbound traceparent alone.
func TestComposedSeamsExportNothingByDefault(t *testing.T) {
	clearOTLPEnv(t)
	receiver := &otlpReceiver{}
	collector := httptest.NewServer(receiver)
	defer collector.Close()
	upstream := syntheticUpstream(t, new([]string))

	cfg := FromEnv("test") // the production construction, with a cleared environment
	if cfg.Enabled {
		t.Fatalf("FromEnv enabled export with no endpoint and no explicit enable: %+v", cfg)
	}
	p := newProvider(t, cfg)
	if p.Enabled() {
		t.Fatal("the provider is enabled by default; export must be opt-in")
	}

	const inbound = "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"
	meta := driveComposedSeams(t, p, upstream.URL, inbound)
	_ = flush(t, p)

	if got := len(receiver.requests()); got != 0 {
		t.Fatalf("the collector saw %d request(s) with export disabled; default must emit nothing", got)
	}
	// The no-op correlation property: the enriched map still names the INBOUND trace. Whether a
	// ledger row would carry it is NOT measured here.
	if meta[MetaTraceID] != "4bf92f3577b34da6a3ce929d0e0e4736" {
		t.Fatalf("the enriched map did not correlate to the inbound trace: %#v", meta)
	}
	if meta["decision"] != "allow" {
		t.Fatalf("EnrichAuditMeta dropped the caller's own key: %#v", meta)
	}
}

// ENABLED POLICY: the composed seams emit a decoded server span and a decoded gen_ai client
// span, parented, on one trace, with the configured resource — and the enriched map names that
// same trace.
func TestComposedSeamsExportCorrelatedSpansWhenConfigured(t *testing.T) {
	clearOTLPEnv(t)
	receiver := &otlpReceiver{}
	collector := httptest.NewServer(receiver)
	defer collector.Close()
	upstream := syntheticUpstream(t, new([]string))

	p := newProvider(t, runtimeConfig(collector.URL))
	if !p.Enabled() {
		t.Fatal("an explicitly configured endpoint did not enable export")
	}
	meta := driveComposedSeams(t, p, upstream.URL, "")
	if err := flush(t, p); err != nil {
		t.Fatalf("Shutdown/flush: %v", err)
	}

	spans := decodeSpans(t, receiver)
	if len(spans) == 0 {
		t.Fatal("the composed seams exported no span at all")
	}
	var server, client *exportedSpan
	for i := range spans {
		switch {
		case spans[i].kind == tracepb.Span_SPAN_KIND_SERVER && spans[i].name == "HTTP POST":
			server = &spans[i]
		case spans[i].kind == tracepb.Span_SPAN_KIND_CLIENT && strings.HasPrefix(spans[i].name, "chat"):
			client = &spans[i]
		}
	}
	if server == nil {
		t.Fatalf("no SERVER span from the ingress middleware; got %+v", spans)
	}
	if client == nil {
		t.Fatalf("no CLIENT gen_ai span from the instrumented Messages round trip; got %+v", spans)
	}
	if client.name != "chat "+runtimeModel {
		t.Errorf("client span name %q, want %q", client.name, "chat "+runtimeModel)
	}
	if client.traceID != server.traceID {
		t.Errorf("ingress and Messages spans are on different traces: %s vs %s", server.traceID, client.traceID)
	}
	if client.parentID != server.spanID {
		t.Errorf("the Messages span's parent is %q, want the ingress span %q", client.parentID, server.spanID)
	}
	for _, s := range []*exportedSpan{server, client} {
		if s.service != runtimeService {
			t.Errorf("span %q carries resource service.name %q, want %q", s.name, s.service, runtimeService)
		}
	}
	// The gen_ai fields the policy does export.
	for key, want := range map[string]string{
		"gen_ai.provider.name":  "anthropic",
		"gen_ai.operation.name": "chat",
		"gen_ai.request.model":  runtimeModel,
		"gen_ai.response.model": runtimeModel,
		"gen_ai.response.id":    runtimeResponseID,
	} {
		if got, ok := client.attrs[key]; !ok || !strings.Contains(got, want) {
			t.Errorf("client span attribute %s = %q, want it to contain %q", key, got, want)
		}
	}
	for _, key := range []string{"gen_ai.usage.input_tokens", "gen_ai.usage.output_tokens"} {
		if _, ok := client.attrs[key]; !ok {
			t.Errorf("client span is missing %s", key)
		}
	}
	// Correlation: the enriched map names the exported trace. Not a durable-evidence claim.
	if meta[MetaTraceID] != server.traceID {
		t.Errorf("the enriched map trace %v does not name the exported trace %s", meta[MetaTraceID], server.traceID)
	}
	if meta["decision"] != "allow" {
		t.Errorf("the caller's own key was lost: %#v", meta)
	}
}

// THE SENTINEL CENSUS: the five synthetic secrets are in the prompt, the system block, the
// Authorization header, the URL query and the provider response. None may appear in the export.
func TestComposedSeamsExportNoSentinelPayload(t *testing.T) {
	clearOTLPEnv(t)
	receiver := &otlpReceiver{}
	collector := httptest.NewServer(receiver)
	defer collector.Close()
	upstream := syntheticUpstream(t, new([]string))

	p := newProvider(t, runtimeConfig(collector.URL))
	_ = driveComposedSeams(t, p, upstream.URL, "")
	if err := flush(t, p); err != nil {
		t.Fatalf("Shutdown/flush: %v", err)
	}
	raw := rawExportBytes(receiver)
	if len(raw) == 0 {
		t.Fatal("nothing was exported, so the census would be vacuous")
	}
	for _, sentinel := range allSentinels() {
		if bytes.Contains(raw, []byte(sentinel)) {
			t.Errorf("SENTINEL LEAKED into the OTLP export: %s", sentinel)
		}
	}
	// The positive control: the digest fields the policy DOES export are present, so the
	// absence above is a policy result and not an empty payload.
	spans := decodeSpans(t, receiver)
	var sawRequestSHA, sawResponseSHA bool
	for _, s := range spans {
		if _, ok := s.attrs["ai.olivares.inference.request.body_sha256"]; ok {
			sawRequestSHA = true
		}
		if _, ok := s.attrs["ai.olivares.inference.response.body_sha256"]; ok {
			sawResponseSHA = true
		}
	}
	if !sawRequestSHA || !sawResponseSHA {
		t.Errorf("the body digests are absent (request=%v response=%v), so this census proves nothing about policy", sawRequestSHA, sawResponseSHA)
	}
}

// EXPORTER FAILURE must not become the handler's result and must not alter the enriched map.
// This says nothing about SQL durability, which this fixture never touches.
func TestExporterFailureLeavesTheHandlerResultAndTheEnrichedMapIntact(t *testing.T) {
	clearOTLPEnv(t)
	// A collector that refuses every export. The exporter will retry and fail; the request
	// path must not notice.
	refusals := 0
	collector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		refusals++
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer collector.Close()
	upstream := syntheticUpstream(t, new([]string))

	p := newProvider(t, runtimeConfig(collector.URL))
	const inbound = "00-11112222333344445555666677778888-1111222233334444-01"
	meta := driveComposedSeams(t, p, upstream.URL, inbound)
	// Shutdown may report the transport failure; that is the exporter's business.
	shutdownErr := flush(t, p)
	t.Logf("exporter shutdown error (expected, transport refused): %v; refusals=%d", shutdownErr, refusals)

	if meta[MetaTraceID] != "11112222333344445555666677778888" {
		t.Errorf("a failing exporter changed the enriched correlation: %#v", meta)
	}
	if meta["decision"] != "allow" {
		t.Errorf("a failing exporter dropped the caller's own key: %#v", meta)
	}
	if refusals == 0 {
		t.Error("the collector was never contacted, so this case did not exercise an exporter failure")
	}
}

// A STALLED collector: the bounded flush must return rather than hang, and the request path is
// again unaffected. The valid control is the configured case above.
func TestStalledCollectorDoesNotHangTheFlushOrAlterTheEnrichedMap(t *testing.T) {
	clearOTLPEnv(t)
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	collector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		case <-time.After(30 * time.Second):
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer collector.Close()
	upstream := syntheticUpstream(t, new([]string))

	p := newProvider(t, runtimeConfig(collector.URL))
	const inbound = "00-aaaabbbbccccddddeeeeffff00001111-aaaabbbbccccdddd-01"
	meta := driveComposedSeams(t, p, upstream.URL, inbound)

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	start := time.Now()
	err := p.Shutdown(ctx)
	elapsed := time.Since(start)
	t.Logf("bounded shutdown against a stalled collector returned in %s with err=%v", elapsed, err)
	if elapsed > 20*time.Second {
		t.Errorf("the flush did not respect its bound: %s", elapsed)
	}
	if meta[MetaTraceID] != "aaaabbbbccccddddeeeeffff00001111" {
		t.Errorf("a stalled collector changed the enriched correlation: %#v", meta)
	}
	if meta["decision"] != "allow" {
		t.Errorf("a stalled collector dropped the caller's own key: %#v", meta)
	}
}
