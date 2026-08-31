// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package trace

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

const (
	testTraceID = "4bf92f3577b34da6a3ce929d0e0e4736"
	testSpanID  = "00f067aa0ba902b7"
)

// enabledTestProvider builds a recording Provider backed by in-memory test
// exporters (no collector), so spans/metrics can be asserted directly.
func enabledTestProvider() (*Provider, *tracetest.SpanRecorder, *sdkmetric.ManualReader) {
	sr := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(sr))
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	genai, _ := newGenAIInstruments(mp.Meter(instrumentationName))
	p := &Provider{
		propagator: propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{}),
		tracer:     tp.Tracer(instrumentationName),
		enabled:    true,
		genai:      genai,
		tp:         tp,
		mp:         mp,
	}
	return p, sr, reader
}

// (a) A valid inbound traceparent is CONTINUED: the engine's server span and the
// ledger Meta carry the same trace-id as the caller.
func TestIngressContinuesTrace(t *testing.T) {
	p, sr, _ := enabledTestProvider()

	var ledgerTraceID string
	h := p.HTTPMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		meta := EnrichAuditMeta(r.Context(), map[string]any{"action": "x"})
		ledgerTraceID, _ = meta[MetaTraceID].(string)
		// minimal-data: the original key survives, no payload added
		if meta["action"] != "x" {
			t.Error("EnrichAuditMeta dropped an existing meta key")
		}
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/v1/agents", nil)
	req.Header.Set("traceparent", "00-"+testTraceID+"-"+testSpanID+"-01")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if ledgerTraceID != testTraceID {
		t.Fatalf("ledger trace_id = %q, want continued %q", ledgerTraceID, testTraceID)
	}
	ended := sr.Ended()
	if len(ended) != 1 {
		t.Fatalf("expected 1 server span, got %d", len(ended))
	}
	if got := ended[0].SpanContext().TraceID().String(); got != testTraceID {
		t.Errorf("server span trace-id = %q, want %q (trace not continued)", got, testTraceID)
	}
	if got := ended[0].Parent().SpanID().String(); got != testSpanID {
		t.Errorf("server span parent = %q, want the inbound span-id %q", got, testSpanID)
	}
}

// TestRetrievalHTTPSpanRejectsRawPII attacks the generic ingress span with PII in
// the route, request body, bearer token and retrieved response. Only the method is
// low-cardinality and safe enough to export.
func TestRetrievalHTTPSpanRejectsRawPII(t *testing.T) {
	const (
		pathPII         = "alice.s373@example.com"
		queryCanary     = "QUERY-TRACE-PRIVATE-CANARY"
		retrievedCanary = "RETRIEVED-TRACE-PRIVATE-CANARY"
		bearerCanary    = "BEARER-TRACE-PRIVATE-CANARY"
	)
	p, sr, _ := enabledTestProvider()
	h := p.HTTPMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(retrievedCanary))
	}))
	req := httptest.NewRequest(
		http.MethodPost,
		"/v1/m/knowledge/kbs/"+pathPII+"/query",
		strings.NewReader(`{"query":"`+queryCanary+`"}`),
	)
	req.Header.Set("Authorization", "Bearer "+bearerCanary)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if !strings.Contains(rec.Body.String(), retrievedCanary) {
		t.Fatal("test handler did not return the retrieved canary")
	}

	ended := sr.Ended()
	if len(ended) != 1 {
		t.Fatalf("ended spans = %d, want 1", len(ended))
	}
	span := ended[0]
	serialized := fmt.Sprint(span.Name(), span.Attributes(), span.Events(), span.Status())
	for _, canary := range []string{pathPII, queryCanary, retrievedCanary, bearerCanary} {
		if strings.Contains(serialized, canary) {
			t.Fatalf("retrieval HTTP span leaked raw privacy canary %q: %s", canary, serialized)
		}
	}
	attrs := span.Attributes()
	if len(attrs) != 1 || string(attrs[0].Key) != "http.request.method" || attrs[0].Value.AsString() != http.MethodPost {
		t.Fatalf("retrieval span attributes = %v, want method only", attrs)
	}
}

// (b) A foreign tracestate is PRESERVED across the engine (extract → inject), so
// other vendors' correlation is not broken (W3C non-mutation rule, docs/SECURITY-HARDENING.md).
func TestForeignTracestatePreserved(t *testing.T) {
	p, _, _ := enabledTestProvider()
	const foreign = "vendora=t61rcWkgMzE,vendorb=00f067aa0ba902b7"

	var outTracestate, outTraceparent string
	h := p.HTTPMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Simulate the engine→Claude egress hop: inject the current context.
		out := httptest.NewRequest(http.MethodPost, "https://api.anthropic.com/v1/messages", nil)
		p.Propagator().Inject(r.Context(), propagation.HeaderCarrier(out.Header))
		outTracestate = out.Header.Get("tracestate")
		outTraceparent = out.Header.Get("traceparent")
	}))

	req := httptest.NewRequest(http.MethodGet, "/v1/agents", nil)
	req.Header.Set("traceparent", "00-"+testTraceID+"-"+testSpanID+"-01")
	req.Header.Set("tracestate", foreign)
	h.ServeHTTP(httptest.NewRecorder(), req)

	if outTracestate != foreign {
		t.Errorf("tracestate = %q, want it preserved byte-for-byte as %q", outTracestate, foreign)
	}
	// The traceparent must continue the SAME trace (the engine's child span id differs).
	if !strings.HasPrefix(outTraceparent, "00-"+testTraceID+"-") {
		t.Errorf("egress traceparent = %q, want same trace-id %q", outTraceparent, testTraceID)
	}
	if strings.Contains(outTraceparent, "-"+testSpanID+"-") {
		t.Error("egress traceparent must carry the engine's own span-id, not the inbound one")
	}
}

// (c) Forward-compatibility, verified against the W3C-Level-2-aware OTel propagator:
//   - the L2 random-trace-id flag (trace-flags bit 0x02) is NOT rejected — a
//     version-00 traceparent with flags "03" (sampled+random) still continues; and
//   - a FUTURE higher version (e.g. "01") with extra trailing fields is still
//     parsed and continued (cross-version forward-compat).
//
// (Per the spec, version 00 reserves bits beyond 0x03, so flags like "ff" are
// correctly rejected — that is conformance, not a regression.)
func TestForwardCompatTraceFlagsAndVersion(t *testing.T) {
	p, _, _ := enabledTestProvider()
	continued := func(traceparent string) string {
		var id string
		h := p.HTTPMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id, _, _ = ContextFrom(r.Context())
		}))
		req := httptest.NewRequest(http.MethodGet, "/v1/agents", nil)
		req.Header.Set("traceparent", traceparent)
		h.ServeHTTP(httptest.NewRecorder(), req)
		return id
	}

	t.Run("L2 random-trace-id flag accepted", func(t *testing.T) {
		if got := continued("00-" + testTraceID + "-" + testSpanID + "-03"); got != testTraceID {
			t.Fatalf("trace-id = %q: the L2 random-trace-id flag must not cause rejection", got)
		}
	})
	t.Run("future version with extra fields accepted", func(t *testing.T) {
		if got := continued("01-" + testTraceID + "-" + testSpanID + "-01-extra-future-field"); got != testTraceID {
			t.Fatalf("trace-id = %q: a future traceparent version must be parsed forward-compatibly", got)
		}
	})
}

// (e) With no collector the Provider degrades to no-op and a request NEVER fails for
// tracing reasons (deny-open telemetry).
func TestDisabledProviderNeverBreaksRequest(t *testing.T) {
	p, err := New(context.Background(), Config{Enabled: false})
	if err != nil {
		t.Fatalf("New(disabled): %v", err)
	}
	if p.Enabled() {
		t.Fatal("provider with no endpoint must be disabled (no-op)")
	}
	called := false
	h := p.HTTPMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		// Even disabled, an inbound trace is still correlated for the ledger.
		if id, _, ok := ContextFrom(r.Context()); !ok || id != testTraceID {
			t.Errorf("disabled mode must still extract the inbound trace for ledger correlation, got %q ok=%v", id, ok)
		}
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(http.MethodGet, "/v1/agents", nil)
	req.Header.Set("traceparent", "00-"+testTraceID+"-"+testSpanID+"-01")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if !called {
		t.Fatal("handler not called (disabled tracing broke the request)")
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (tracing must never fail a request)", rec.Code)
	}
}

// New with an endpoint but no live collector must NOT block or fail boot (the OTLP
// exporters connect lazily) — a missing collector cannot delay the engine.
func TestNewWithEndpointDoesNotBlockBoot(t *testing.T) {
	p, err := New(context.Background(), Config{
		Enabled: true, Endpoint: "127.0.0.1:4317", Protocol: ProtocolGRPC, Insecure: true,
		SampleRatio: 1, ServiceName: "test", ServiceVersion: "v",
	})
	if err != nil {
		t.Fatalf("New(enabled, no collector): %v", err)
	}
	if !p.Enabled() {
		t.Fatal("a configured endpoint must enable the provider")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2_000_000_000) // 2s
	defer cancel()
	_ = p.Shutdown(ctx) // must not hang/panic with no collector
}

func TestFromEnvDefaultsDisabled(t *testing.T) {
	t.Setenv("OLIVARES_OTEL_ENABLED", "")
	t.Setenv("OLIVARES_OTEL_ENDPOINT", "")
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	t.Setenv("OLIVARES_OTEL_GENAI_COMPAT", "")
	cfg := FromEnv("v1.2.3")
	if cfg.Enabled {
		t.Error("with no endpoint and no enable flag, tracing must default OFF (opt-in)")
	}
	if cfg.GenAICompat {
		t.Error("GenAI compat must default OFF")
	}
	if cfg.ServiceName != defaultServiceName || cfg.ServiceVersion != "v1.2.3" {
		t.Errorf("resource defaults wrong: name=%q version=%q", cfg.ServiceName, cfg.ServiceVersion)
	}
	t.Setenv("OLIVARES_OTEL_ENDPOINT", "collector:4317")
	if !FromEnv("v").Enabled {
		t.Error("a configured endpoint must enable tracing")
	}
}

func TestFromEnvGenAICompat(t *testing.T) {
	t.Setenv("OLIVARES_OTEL_ENABLED", "")
	t.Setenv("OLIVARES_OTEL_ENDPOINT", "")
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	t.Setenv("OTEL_SEMCONV_STABILITY_OPT_IN", "gen_ai_latest_experimental")
	t.Setenv("OLIVARES_OTEL_GENAI_COMPAT", "")
	if FromEnv("v").GenAICompat {
		t.Fatal("OTEL_SEMCONV_STABILITY_OPT_IN=gen_ai_latest_experimental must not enable deprecated dual-emit compat")
	}

	t.Setenv("OLIVARES_OTEL_GENAI_COMPAT", "on")
	if !FromEnv("v").GenAICompat {
		t.Fatal("OLIVARES_OTEL_GENAI_COMPAT=on must enable GenAI deprecated dual-emit compat")
	}
}
