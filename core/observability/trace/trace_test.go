// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package trace

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/baggage"
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

// TestFromEnvGenAILatestOptIn pins the standard semconv opt-in switch (issue
// #202): OTEL_SEMCONV_STABILITY_OPT_IN is a comma-separated list per the
// instrumentation.yaml convention, and only the exact genai_latest_experimental
// token switches GenAI emission to the pending conventions (#374, #440).
func TestFromEnvGenAILatestOptIn(t *testing.T) {
	t.Setenv("OLIVARES_OTEL_ENABLED", "")
	t.Setenv("OLIVARES_OTEL_ENDPOINT", "")
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	t.Setenv("OTEL_SEMCONV_STABILITY_OPT_IN", "")
	if FromEnv("v").GenAILatest {
		t.Fatal("GenAI latest-experimental emission must default OFF (published form only)")
	}
	t.Setenv("OTEL_SEMCONV_STABILITY_OPT_IN", "gen_ai_latest_experimental")
	if !FromEnv("v").GenAILatest {
		t.Fatal("OTEL_SEMCONV_STABILITY_OPT_IN=gen_ai_latest_experimental must switch to the latest form")
	}
	t.Setenv("OTEL_SEMCONV_STABILITY_OPT_IN", "http/dup, gen_ai_latest_experimental ,dup")
	if !FromEnv("v").GenAILatest {
		t.Fatal("a comma-separated list containing the token must switch to the latest form")
	}
	t.Setenv("OTEL_SEMCONV_STABILITY_OPT_IN", "gen_ai_latest_experimental_old")
	if FromEnv("v").GenAILatest {
		t.Fatal("a token that merely contains the value as a substring must not switch")
	}
	t.Setenv("OTEL_SEMCONV_STABILITY_OPT_IN", "GEN_AI_LATEST_EXPERIMENTAL")
	if FromEnv("v").GenAILatest {
		t.Fatal("the token is matched exactly, not case-insensitively")
	}
}

// TestResolveKeepsSemconvOptInWithSavedSettings: the opt-in is environment-only,
// so saved tracing settings must never mask it, and its presence must surface as
// a recognized override.
func TestResolveKeepsSemconvOptInWithSavedSettings(t *testing.T) {
	t.Setenv("OLIVARES_OTEL_ENABLED", "")
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	t.Setenv("OTEL_SEMCONV_STABILITY_OPT_IN", "gen_ai_latest_experimental")
	s := DefaultSettings()
	s.Enabled = true
	s.Endpoint = "collector.internal:4317"
	cfg, overrides := s.Resolve("v")
	if !cfg.GenAILatest {
		t.Fatal("saved settings must not mask the environment opt-in")
	}
	found := false
	for _, k := range overrides {
		if k == "OTEL_SEMCONV_STABILITY_OPT_IN" {
			found = true
		}
	}
	if !found {
		t.Fatalf("overrides = %v, want OTEL_SEMCONV_STABILITY_OPT_IN reported", overrides)
	}
}

func TestProviderBaggagePolicyConfiguration(t *testing.T) {
	const valid = `[{"origin":"https://api.anthropic.com","key":"deployment.environment","values":["canary"]}]`
	const marker = "D09-CONFIG-PRIVATE-SENTINEL"
	rule := ProviderBaggageRule{Origin: "https://api.anthropic.com", Key: "deployment.environment", Values: []string{"canary"}}
	encode := func(rules []ProviderBaggageRule) string {
		out, err := json.Marshal(rules)
		if err != nil {
			t.Fatal(err)
		}
		return string(out)
	}
	cases := []struct {
		name, input, reason string
		allowed             bool
	}{
		{name: "unset"},
		{name: "empty array", input: "[]"},
		{name: "valid", input: valid, allowed: true},
		{name: "invalid JSON", input: marker, reason: "invalid_json"},
		{name: "null is not an array", input: "null", reason: "invalid_json"},
		{name: "unknown field", input: `[{"origin":"https://api.anthropic.com","key":"deployment.environment","values":["canary"],"` + marker + `":true}]`, reason: "invalid_json"},
		{name: "trailing JSON", input: valid + "[]", reason: "invalid_json"},
		{name: "input bound", input: strings.Repeat(" ", 16385) + valid, reason: "over_limit"},
		{name: "rule bound", input: encode(make([]ProviderBaggageRule, 33)), reason: "over_limit"},
		{name: "duplicate normalized rule", input: encode([]ProviderBaggageRule{rule, {Origin: "https://API.ANTHROPIC.COM:443/", Key: rule.Key, Values: rule.Values}}), reason: "duplicate_rule"},
		{name: "wildcard key", input: strings.Replace(valid, "deployment.environment", "deployment.*", 1), reason: "invalid_rule"},
		{name: "wildcard value", input: strings.Replace(valid, "canary", "*", 1), reason: "invalid_rule"},
		{name: "empty values", input: strings.Replace(valid, `["canary"]`, `[]`, 1), reason: "invalid_rule"},
		{name: "value count bound", input: encode([]ProviderBaggageRule{{Origin: rule.Origin, Key: rule.Key, Values: make([]string, 9)}}), reason: "over_limit"},
		{name: "empty value", input: strings.Replace(valid, "canary", "", 1), reason: "invalid_rule"},
		{name: "empty key", input: strings.Replace(valid, rule.Key, "", 1), reason: "invalid_rule"},
		{name: "non ASCII key", input: strings.Replace(valid, rule.Key, "déployment", 1), reason: "invalid_rule"},
		{name: "non ASCII value", input: strings.Replace(valid, "canary", "cánary", 1), reason: "invalid_rule"},
		{name: "non ASCII origin", input: strings.Replace(valid, "api.anthropic.com", "éxample.com", 1), reason: "invalid_rule"},
		{name: "origin path", input: strings.Replace(valid, "api.anthropic.com", "api.anthropic.com/messages", 1), reason: "invalid_rule"},
		{name: "origin userinfo", input: strings.Replace(valid, "api.anthropic.com", marker+"@api.anthropic.com", 1), reason: "invalid_rule"},
		{name: "origin query", input: strings.Replace(valid, "api.anthropic.com", "api.anthropic.com?"+marker, 1), reason: "invalid_rule"},
		{name: "origin fragment", input: strings.Replace(valid, "api.anthropic.com", "api.anthropic.com#"+marker, 1), reason: "invalid_rule"},
		{name: "origin port", input: strings.Replace(valid, "api.anthropic.com", "api.anthropic.com:65536", 1), reason: "invalid_rule"},
		{name: "key bound", input: strings.Replace(valid, "deployment.environment", strings.Repeat("k", 65), 1), reason: "over_limit"},
		{name: "value bound", input: strings.Replace(valid, "canary", strings.Repeat("v", 65), 1), reason: "over_limit"},
		{name: "invalid control value", input: strings.Replace(valid, "canary", marker+"/private", 1), reason: "invalid_rule"},
		{name: "deny whole policy", input: strings.TrimSuffix(valid, "]") + `,{"origin":"` + marker + `","key":"other","values":["control"]}]`, reason: "invalid_rule"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("OLIVARES_OTEL_PROVIDER_BAGGAGE_ALLOWLIST", tc.input)
			cfg := FromEnv("test")
			cfg.Enabled, cfg.Endpoint = false, ""
			checkProviderBaggageDiagnostic(t, cfg, tc.reason, tc.allowed)
		})
	}
	t.Run("typed invalid config", func(t *testing.T) {
		checkProviderBaggageDiagnostic(t, Config{ProviderBaggageAllowlist: []ProviderBaggageRule{{
			Origin: "https://api.anthropic.com", Key: "deployment.environment", Values: []string{"*"},
		}}}, "invalid_rule", false)
	})
}

// The public error handler observes invalid policy without accessing private
// compilation state. Empty/default and valid policies emit no rejection.
func checkProviderBaggageDiagnostic(t *testing.T, cfg Config, reason string, allowed bool) {
	t.Helper()
	var mu sync.Mutex
	var diagnostics []string
	previous := otel.GetErrorHandler()
	otel.SetErrorHandler(otel.ErrorHandlerFunc(func(err error) {
		mu.Lock()
		defer mu.Unlock()
		diagnostics = append(diagnostics, err.Error())
	}))
	t.Cleanup(func() { otel.SetErrorHandler(previous) })
	p, err := New(context.Background(), cfg)
	if err != nil {
		t.Fatalf("invalid telemetry policy must not fail inference construction: %v", err)
	}
	member, err := baggage.NewMemberRaw("deployment.environment", "canary")
	if err != nil {
		t.Fatal(err)
	}
	bag, err := baggage.New(member)
	if err != nil {
		t.Fatal(err)
	}
	ctx := baggage.ContextWithBaggage(context.Background(), bag)
	client := p.AnthropicHTTPClient(propagationRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		want := ""
		if allowed {
			want = "deployment.environment=canary"
		}
		if got := r.Header.Get("baggage"); got != want {
			t.Errorf("baggage = %q, want %q", got, want)
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("{}"))}, nil
	}))
	for range 2 {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.anthropic.com/v1/messages", nil)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
	}
	mu.Lock()
	defer mu.Unlock()
	if reason == "" {
		if len(diagnostics) != 0 {
			t.Errorf("valid/default policy reported a rejection: %v", diagnostics)
		}
		return
	}
	want := "trace: provider baggage policy rejected (" + reason + "); baggage propagation disabled"
	if len(diagnostics) != 1 || diagnostics[0] != want {
		t.Errorf("diagnostics = %v, want only %q once at construction", diagnostics, want)
	}
}
