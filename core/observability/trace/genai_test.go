// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package trace

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// roundTripFunc is a fake base RoundTripper returning a canned response.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// a canned Anthropic Messages response with a cache split.
const cannedMessagesResponse = `{
  "id":"msg_01ABC",
  "model":"claude-opus-4-8-20260101",
  "stop_reason":"end_turn",
  "usage":{"input_tokens":100,"output_tokens":20,"cache_read_input_tokens":50,
    "cache_creation_input_tokens":248,
    "cache_creation":{"ephemeral_5m_input_tokens":148,"ephemeral_1h_input_tokens":100}}
}`

func attrOf(span interface {
	Attributes() []attribute.KeyValue
}, key string) (attribute.Value, bool) {
	for _, kv := range span.Attributes() {
		if string(kv.Key) == key {
			return kv.Value, true
		}
	}
	return attribute.Value{}, false
}

func assertNoAttr(t *testing.T, span interface {
	Attributes() []attribute.KeyValue
}, key string) {
	t.Helper()
	if v, ok := attrOf(span, key); ok {
		t.Fatalf("%s = %v, want absent", key, v.AsInterface())
	}
}

func doCannedMessagesCall(t *testing.T, p *Provider) string {
	t.Helper()
	var injectedTraceparent string
	base := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		injectedTraceparent = r.Header.Get("traceparent")
		// the request model must still be readable by the real client downstream
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), `"claude-opus-4-8"`) {
			t.Errorf("request body not preserved for the real client: %q", body)
		}
		return &http.Response{
			StatusCode: 200,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(cannedMessagesResponse)),
		}, nil
	})

	client := p.AnthropicHTTPClient(base)
	req, _ := http.NewRequest(http.MethodPost, "https://api.anthropic.com/v1/messages",
		strings.NewReader(`{"model":"claude-opus-4-8","max_tokens":16}`))
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	// The caller can still read the full response body (instrumentation restored it).
	got, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(got), `"msg_01ABC"`) {
		t.Fatalf("response body not intact for the caller: %q", got)
	}
	return injectedTraceparent
}

// (d) An engine→Claude Messages call emits a gen_ai span with
// gen_ai.provider.name="anthropic" and token usage whose input total INCLUDES the
// cache tokens (matching CostSample.InputTokens' inclusive semantics), plus the two
// client histograms — and the response body remains intact for the caller.
func TestGenAISpanAndAccounting(t *testing.T) {
	p, sr, reader := enabledTestProvider()

	injectedTraceparent := doCannedMessagesCall(t, p)

	// the engine→Claude hop carries our gen_ai client span's traceparent (mesh stitch).
	if !strings.HasPrefix(injectedTraceparent, "00-") {
		t.Errorf("traceparent not injected into the egress hop: %q", injectedTraceparent)
	}

	ended := sr.Ended()
	if len(ended) != 1 {
		t.Fatalf("expected 1 gen_ai span, got %d", len(ended))
	}
	span := ended[0]
	if v, ok := attrOf(span, attrGenAIProvider); !ok || v.AsString() != providerAnthropic {
		t.Errorf("gen_ai.provider.name = %v ok=%v, want %q", v.AsString(), ok, providerAnthropic)
	}
	if v, ok := attrOf(span, attrGenAIOperation); !ok || v.AsString() != opChat {
		t.Errorf("gen_ai.operation.name = %v, want %q", v.AsString(), opChat)
	}
	if v, ok := attrOf(span, attrGenAIRequestModel); !ok || v.AsString() != "claude-opus-4-8" {
		t.Errorf("gen_ai.request.model = %v", v.AsString())
	}
	if v, ok := attrOf(span, attrGenAIResponseModel); !ok || v.AsString() != "claude-opus-4-8-20260101" {
		t.Errorf("gen_ai.response.model = %v", v.AsString())
	}
	if v, ok := attrOf(span, attrGenAIResponseID); !ok || v.AsString() != "msg_01ABC" {
		t.Errorf("gen_ai.response.id = %v", v.AsString())
	}
	assertNoAttr(t, span, attrGenAICompatSystem)
	assertNoAttr(t, span, attrGenAICompatPromptTokens)
	assertNoAttr(t, span, attrGenAICompatCompletionTokens)

	// Centralized accounting: OTel input total = raw input + cache_read + cache_creation
	// = 100 + 50 + 248 = 398 (the inclusive total CostSample.InputTokens documents).
	wantInput := AnthropicUsage{InputTokens: 100, OutputTokens: 20, CacheReadInputTokens: 50, CacheCreationInputTokens: 248}.OTelInputTokens()
	if wantInput != 398 {
		t.Fatalf("accounting helper drifted: OTelInputTokens()=%d, want 398", wantInput)
	}
	if v, ok := attrOf(span, attrGenAIInputTokens); !ok || v.AsInt64() != 398 {
		t.Errorf("gen_ai.usage.input_tokens = %d, want 398 (inclusive of cache, matching CostSample)", v.AsInt64())
	}
	if v, ok := attrOf(span, attrGenAIOutputTokens); !ok || v.AsInt64() != 20 {
		t.Errorf("gen_ai.usage.output_tokens = %d, want 20", v.AsInt64())
	}
	if v, ok := attrOf(span, attrGenAICacheRead); !ok || v.AsInt64() != 50 {
		t.Errorf("cache_read.input_tokens = %d, want 50", v.AsInt64())
	}
	if v, ok := attrOf(span, attrGenAICacheCreation); !ok || v.AsInt64() != 248 {
		t.Errorf("cache_creation.input_tokens = %d, want 248", v.AsInt64())
	}

	// Both client metrics were recorded with the spec bucket boundaries.
	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("collect metrics: %v", err)
	}
	tokenPoints, durationPoints, tokenBuckets := inspectGenAIMetrics(t, &rm)
	if tokenPoints != 2 { // one input + one output
		t.Errorf("gen_ai.client.token.usage data points = %d, want 2 (input+output)", tokenPoints)
	}
	if durationPoints != 1 {
		t.Errorf("gen_ai.client.operation.duration data points = %d, want 1", durationPoints)
	}
	if len(tokenBuckets) != len(tokenUsageBuckets) {
		t.Errorf("token.usage bucket count = %d, want the spec's %d", len(tokenBuckets), len(tokenUsageBuckets))
	}
	// The default emits ONLY the two current instruments: a dual-emission
	// regression (opt-in counters leaking into the default) fails here.
	assertOnlyCurrentGenAIMetrics(t, &rm)
}

// TestNewAppliesGenAILatestOptIn pins the wiring from Config to the running
// Provider: the transport test sets the field by hand, so without this a dropped
// line in New would leave the env opt-in a silent no-op in the real binary.

// TestGenAITransportErrorUnderLatestOptIn: a failed request under the opt-in
// records duration with error.type=transport and NO token instrument of either
// form — no counters, no histogram, never a guessed zero.
func TestGenAITransportErrorUnderLatestOptIn(t *testing.T) {
	p, sr, reader := enabledTestProvider()
	p.genAILatest = true
	base := roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, fmt.Errorf("dial stub: connection refused")
	})
	client := p.AnthropicHTTPClient(base)
	req, _ := http.NewRequest(http.MethodPost, "https://api.anthropic.com/v1/messages",
		strings.NewReader(`{"model":"claude-opus-4-8","max_tokens":16}`))
	if _, err := client.Do(req); err == nil {
		t.Fatal("expected the transport error to reach the caller")
	}
	if len(sr.Ended()) != 1 {
		t.Fatalf("ended spans = %d, want 1", len(sr.Ended()))
	}
	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("collect metrics: %v", err)
	}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			switch m.Name {
			case metricOpDuration:
			default:
				t.Errorf("metric %q recorded for a failed request; only duration belongs", m.Name)
			}
		}
	}
}

// TestGenAISpanRejectsRawInferenceContent attacks the OTel transport with raw PII
// and secrets in both bodies. The transport may parse model/id/usage metadata, but
// it must never attach prompt, completion or whole-body values to the span.
func TestGenAISpanRejectsRawInferenceContent(t *testing.T) {
	const (
		requestCanary  = "alice.s373@example.com secret=REQUEST-TRACE-CANARY"
		responseCanary = "SSN 078-05-1120 RESPONSE-TRACE-CANARY"
	)
	p, sr, _ := enabledTestProvider()
	responseBody := `{"id":"msg_privacy","model":"claude-opus-4-8-20260101",` +
		`"stop_reason":"end_turn","content":[{"type":"text","text":"` + responseCanary + `"}],` +
		`"usage":{"input_tokens":7,"output_tokens":3}}`
	base := roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(responseBody)),
		}, nil
	})
	client := p.AnthropicHTTPClient(base)
	requestBody := `{"model":"claude-opus-4-8","max_tokens":16,"messages":[{"role":"user","content":"` + requestCanary + `"}]}`
	req, err := http.NewRequest(http.MethodPost, "https://api.anthropic.com/v1/messages", strings.NewReader(requestBody))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()

	ended := sr.Ended()
	if len(ended) != 1 {
		t.Fatalf("ended spans = %d, want 1", len(ended))
	}
	span := ended[0]
	serialized := fmt.Sprint(span.Name(), span.Attributes(), span.Events(), span.Status())
	for _, canary := range []string{requestCanary, responseCanary} {
		if strings.Contains(serialized, canary) {
			t.Fatalf("GenAI span leaked raw inference content %q: %s", canary, serialized)
		}
	}

	allowed := map[string]bool{
		attrGenAIProvider: true, attrGenAIOperation: true, attrGenAIRequestModel: true,
		attrGenAIResponseModel: true, attrGenAIResponseID: true, attrGenAIFinishReasons: true,
		attrGenAIInputTokens: true, attrGenAIOutputTokens: true,
		attrGenAICacheRead: true, attrGenAICacheCreation: true,
		attrOlivaresRequestSHA: true, attrOlivaresResponseSHA: true,
	}
	for _, kv := range span.Attributes() {
		if !allowed[string(kv.Key)] {
			t.Errorf("unexpected GenAI span attribute %q=%v", kv.Key, kv.Value.AsInterface())
		}
	}
	for key, raw := range map[string]string{
		attrOlivaresRequestSHA: requestBody, attrOlivaresResponseSHA: responseBody,
	} {
		sum := sha256.Sum256([]byte(raw))
		want := hex.EncodeToString(sum[:])
		got, ok := attrOf(span, key)
		if !ok || got.AsString() != want {
			t.Errorf("%s = %q ok=%v, want SHA-256 %q", key, got.AsString(), ok, want)
		}
	}
}

func TestGenAICompatDualEmitSpanOnly(t *testing.T) {
	p, sr, reader := enabledTestProvider()
	p.genAICompat = true

	doCannedMessagesCall(t, p)
	ended := sr.Ended()
	if len(ended) != 1 {
		t.Fatalf("expected 1 gen_ai span, got %d", len(ended))
	}
	span := ended[0]

	if v, ok := attrOf(span, attrGenAIProvider); !ok || v.AsString() != providerAnthropic {
		t.Fatalf("current provider attr = %q ok=%v, want %q", v.AsString(), ok, providerAnthropic)
	}
	if v, ok := attrOf(span, attrGenAICompatSystem); !ok || v.AsString() != providerAnthropic {
		t.Fatalf("compat gen_ai.system = %q ok=%v, want %q", v.AsString(), ok, providerAnthropic)
	}

	currentInput, ok := attrOf(span, attrGenAIInputTokens)
	if !ok {
		t.Fatal("missing current gen_ai.usage.input_tokens")
	}
	currentOutput, ok := attrOf(span, attrGenAIOutputTokens)
	if !ok {
		t.Fatal("missing current gen_ai.usage.output_tokens")
	}
	if currentInput.AsInt64() != 398 {
		t.Fatalf("current input tokens = %d, want cache-inclusive 398", currentInput.AsInt64())
	}
	legacyPrompt, ok := attrOf(span, attrGenAICompatPromptTokens)
	if !ok {
		t.Fatal("missing compat gen_ai.usage.prompt_tokens")
	}
	legacyCompletion, ok := attrOf(span, attrGenAICompatCompletionTokens)
	if !ok {
		t.Fatal("missing compat gen_ai.usage.completion_tokens")
	}
	if legacyPrompt.AsInt64() != currentInput.AsInt64() {
		t.Fatalf("compat prompt_tokens = %d, want current input_tokens %d",
			legacyPrompt.AsInt64(), currentInput.AsInt64())
	}
	if legacyCompletion.AsInt64() != currentOutput.AsInt64() {
		t.Fatalf("compat completion_tokens = %d, want current output_tokens %d",
			legacyCompletion.AsInt64(), currentOutput.AsInt64())
	}

	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("collect metrics: %v", err)
	}
	tokenPoints, durationPoints, _ := inspectGenAIMetrics(t, &rm)
	if tokenPoints != 2 {
		t.Errorf("gen_ai.client.token.usage data points = %d, want unchanged 2", tokenPoints)
	}
	if durationPoints != 1 {
		t.Errorf("gen_ai.client.operation.duration data points = %d, want unchanged 1", durationPoints)
	}
	assertOnlyCurrentGenAIMetrics(t, &rm)
}

// TestGenAILatestExperimentalOptInSwitchesTokenMetrics pins the pending semconv
// form behind the standard opt-in (issue #202): with GenAILatest on, the transport
// emits the gen_ai.client.inference.usage COUNTERS (the #374 replacement for the
// gen_ai.client.token.usage histogram and its gen_ai.token.type attribute) with
// the required gen_ai.token.modality, renames the span's cache-creation attribute
// to cache_write (#440), and does not emit the replaced names. Default emission
// stays pinned by TestGenAISpanAndAccounting; the counters ride the SAME meter as
// the duration histogram (one metrics pipeline).
func TestGenAILatestExperimentalOptInSwitchesTokenMetrics(t *testing.T) {
	p, sr, reader := enabledTestProvider()
	p.genAILatest = true

	doCannedMessagesCall(t, p)
	ended := sr.Ended()
	if len(ended) != 1 {
		t.Fatalf("expected 1 gen_ai span, got %d", len(ended))
	}
	span := ended[0]

	// #440: the span attribute is cache_write, and the old spelling is gone.
	if v, ok := attrOf(span, attrGenAICacheWrite); !ok || v.AsInt64() != 248 {
		t.Fatalf("gen_ai.usage.cache_write.input_tokens = %v ok=%v, want 248", v.AsInt64(), ok)
	}
	assertNoAttr(t, span, attrGenAICacheCreation)
	// The input/output token attributes keep their inclusive values.
	if v, ok := attrOf(span, attrGenAIInputTokens); !ok || v.AsInt64() != 398 {
		t.Fatalf("gen_ai.usage.input_tokens = %v ok=%v, want 398", v.AsInt64(), ok)
	}
	if v, ok := attrOf(span, attrGenAIOutputTokens); !ok || v.AsInt64() != 20 {
		t.Fatalf("gen_ai.usage.output_tokens = %v ok=%v, want 20", v.AsInt64(), ok)
	}
	if v, ok := attrOf(span, attrGenAICacheRead); !ok || v.AsInt64() != 50 {
		t.Fatalf("gen_ai.usage.cache_read.input_tokens = %v ok=%v, want 50", v.AsInt64(), ok)
	}

	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("collect metrics: %v", err)
	}
	counters := map[string]int64{}
	seen := map[string]bool{}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			seen[m.Name] = true
			switch m.Name {
			case metricUsageInput, metricUsageOutput, metricUsageCacheRead, metricUsageCacheWrite:
				s, ok := m.Data.(metricdata.Sum[int64])
				if !ok {
					t.Fatalf("%s is not an int64 counter: %T", m.Name, m.Data)
				}
				if len(s.DataPoints) != 1 {
					t.Fatalf("%s data points = %d, want 1", m.Name, len(s.DataPoints))
				}
				dp := s.DataPoints[0]
				counters[m.Name] = dp.Value
				for _, kv := range dp.Attributes.ToSlice() {
					if string(kv.Key) == attrGenAITokenType {
						t.Fatalf("metric attribute %q emitted under the latest form; #374 replaced it", kv.Key)
					}
				}
				assertMetricAttr(t, dp.Attributes, attrGenAITokenModality, modalityText)
				assertMetricAttr(t, dp.Attributes, attrGenAIProvider, providerAnthropic)
				assertMetricAttr(t, dp.Attributes, attrGenAIOperation, opChat)
				assertMetricAttr(t, dp.Attributes, attrGenAIRequestModel, "claude-opus-4-8")
			case metricOpDuration:
				h, ok := m.Data.(metricdata.Histogram[float64])
				if !ok || len(h.DataPoints) != 1 {
					t.Fatalf("operation.duration = %T with %d points, want 1 histogram point", m.Data, len(h.DataPoints))
				}
			default:
				t.Fatalf("unexpected metric instrument %q under the latest form", m.Name)
			}
		}
	}
	// input includes the cache tokens (398), output 20, cache_read 50 and the
	// whole 248-token cache write (148×5m + 100×1h) — each a subset of input.
	want := map[string]int64{
		metricUsageInput:      398,
		metricUsageOutput:     20,
		metricUsageCacheRead:  50,
		metricUsageCacheWrite: 248,
	}
	for name, v := range want {
		if got, ok := counters[name]; !ok || got != v {
			t.Errorf("%s = %d ok=%v, want %d", name, got, ok, v)
		}
	}
	if seen[metricTokenUsage] {
		t.Errorf("%s emitted under the latest form; #374 replaced it", metricTokenUsage)
	}
	if len(seen) != len(want)+1 { // four counters + duration
		t.Fatalf("metric instruments = %v, want the four inference.usage counters and operation.duration", seen)
	}
}

// assertMetricAttr fails unless attrs carries key=value.
func assertMetricAttr(t *testing.T, attrs attribute.Set, key, value string) {
	t.Helper()
	for _, kv := range attrs.ToSlice() {
		if string(kv.Key) == key {
			if kv.Value.AsString() != value {
				t.Fatalf("metric attribute %s = %q, want %q", key, kv.Value.AsString(), value)
			}
			return
		}
	}
	t.Fatalf("metric attribute %s missing", key)
}

// refusingCounterMeter passes histograms through to the real meter but refuses
// every counter, simulating a meter that cannot create the opt-in instruments.
type refusingCounterMeter struct{ metric.Meter }

func (refusingCounterMeter) Int64Counter(string, ...metric.Int64CounterOption) (metric.Int64Counter, error) {
	return nil, fmt.Errorf("stub meter: counter refused")
}

// TestGenAICounterCreationFailureDegradesNotFails: an instrument the OPT-IN form
// needs must not take the default telemetry down with it — the provider keeps
// the default instruments, reports one diagnostic, and the transport records
// the default form even with the opt-in set.
func TestGenAICounterCreationFailureDegradesNotFails(t *testing.T) {
	var diags []string
	otel.SetErrorHandler(otel.ErrorHandlerFunc(func(err error) {
		diags = append(diags, err.Error())
	}))
	defer otel.SetErrorHandler(otel.GetErrorHandler())

	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	genai, err := newGenAIInstruments(refusingCounterMeter{mp.Meter(instrumentationName)})
	if err != nil {
		t.Fatalf("counter refusal must not fail instrument creation: %v", err)
	}
	if genai == nil || genai.tokenUsage == nil || genai.duration == nil {
		t.Fatal("the default instruments must survive a counter refusal")
	}
	if genai.latestUsage != nil {
		t.Fatal("the refused opt-in counters must stay nil so recording degrades")
	}
	if len(diags) != 1 || !strings.Contains(diags[0], "latest-form counters unavailable") {
		t.Fatalf("diagnostics = %q, want exactly one latest-form diagnostic", diags)
	}

	// End to end: with the opt-in set but the counters unavailable, a Messages
	// call still records the default token.usage form.
	sr := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(sr))
	p := &Provider{
		propagator:  propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{}),
		tracer:      tp.Tracer(instrumentationName),
		enabled:     true,
		genai:       genai,
		genAILatest: true,
		tp:          tp,
		mp:          mp,
	}
	doCannedMessagesCall(t, p)
	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("collect metrics: %v", err)
	}
	tokenPoints, _, _ := inspectGenAIMetrics(t, &rm)
	if tokenPoints != 2 {
		t.Fatalf("default token.usage data points = %d, want 2 (input+output) after degradation", tokenPoints)
	}
}

// inspectGenAIMetrics returns the token.usage point count, duration point count and
// the token.usage histogram's bucket boundaries.
func inspectGenAIMetrics(t *testing.T, rm *metricdata.ResourceMetrics) (tokenPoints, durationPoints int, tokenBuckets []float64) {
	t.Helper()
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			switch m.Name {
			case metricTokenUsage:
				h, ok := m.Data.(metricdata.Histogram[int64])
				if !ok {
					t.Fatalf("token.usage is not an int64 histogram: %T", m.Data)
				}
				tokenPoints = len(h.DataPoints)
				if len(h.DataPoints) > 0 {
					tokenBuckets = h.DataPoints[0].Bounds
				}
			case metricOpDuration:
				h, ok := m.Data.(metricdata.Histogram[float64])
				if !ok {
					t.Fatalf("operation.duration is not a float64 histogram: %T", m.Data)
				}
				durationPoints = len(h.DataPoints)
			}
		}
	}
	return tokenPoints, durationPoints, tokenBuckets
}

func assertOnlyCurrentGenAIMetrics(t *testing.T, rm *metricdata.ResourceMetrics) {
	t.Helper()
	seen := map[string]bool{}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			seen[m.Name] = true
			switch m.Name {
			case metricTokenUsage:
				h, ok := m.Data.(metricdata.Histogram[int64])
				if !ok {
					t.Fatalf("token.usage is not an int64 histogram: %T", m.Data)
				}
				for _, dp := range h.DataPoints {
					assertNoLegacyMetricAttr(t, dp.Attributes)
				}
			case metricOpDuration:
				h, ok := m.Data.(metricdata.Histogram[float64])
				if !ok {
					t.Fatalf("operation.duration is not a float64 histogram: %T", m.Data)
				}
				for _, dp := range h.DataPoints {
					assertNoLegacyMetricAttr(t, dp.Attributes)
				}
			default:
				t.Fatalf("unexpected metric instrument %q", m.Name)
			}
		}
	}
	if !seen[metricTokenUsage] || !seen[metricOpDuration] || len(seen) != 2 {
		t.Fatalf("metric instruments = %v, want only %q and %q", seen, metricTokenUsage, metricOpDuration)
	}
}

func assertNoLegacyMetricAttr(t *testing.T, attrs attribute.Set) {
	t.Helper()
	for _, kv := range attrs.ToSlice() {
		switch string(kv.Key) {
		case attrGenAICompatSystem, attrGenAICompatPromptTokens, attrGenAICompatCompletionTokens:
			t.Fatalf("metric attribute %q emitted under compat gate; legacy attrs must stay span-only", kv.Key)
		}
	}
}

// TestAnthropicTokenMathMatchesSemconv pins the provider discriminator and the
// cache-token accounting to the dedicated semconv Anthropic page (re-verified
// 2026-06-21: open-telemetry/semantic-conventions-genai docs/gen-ai/anthropic.md).
// The page mandates gen_ai.provider.name = "anthropic" and, in note [15]: "Anthropic
// input_tokens excludes cached tokens. Compute: gen_ai.usage.input_tokens =
// input_tokens + cache_read_input_tokens + cache_creation_input_tokens". This test
// uses the page's OWN example attribute values (input 100, cache_read 50,
// cache_creation 25 → 175) so a drift in either the discriminator or the math fails.
func TestAnthropicTokenMathMatchesSemconv(t *testing.T) {
	if providerAnthropic != "anthropic" {
		t.Fatalf("providerAnthropic = %q, want %q (anthropic.md: gen_ai.provider.name MUST be \"anthropic\")",
			providerAnthropic, "anthropic")
	}
	// anthropic.md example values for the cache-split usage block.
	u := AnthropicUsage{InputTokens: 100, OutputTokens: 180, CacheReadInputTokens: 50, CacheCreationInputTokens: 25}
	if got := u.OTelInputTokens(); got != 175 {
		t.Fatalf("OTelInputTokens() = %d, want 175 (input 100 + cache_read 50 + cache_creation 25 per anthropic.md [15])", got)
	}
	// The OTel value must INCLUDE the cache tokens — it is never the raw input alone
	// (Anthropic's raw input_tokens excludes cache; reading it as the OTel value
	// understates FinOps cost). This guards the inclusive semantics, not just the sum.
	if u.OTelInputTokens() == u.InputTokens {
		t.Fatalf("OTel input must INCLUDE cache tokens, not equal raw input %d", u.InputTokens)
	}
}

// A disabled provider's client passes the request through (inject only) and never
// dereferences the nil gen_ai instruments.
func TestGenAIDisabledPassthrough(t *testing.T) {
	p, err := New(context.Background(), Config{Enabled: false})
	if err != nil {
		t.Fatal(err)
	}
	base := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(cannedMessagesResponse)), Header: http.Header{}}, nil
	})
	client := p.AnthropicHTTPClient(base)
	req, _ := http.NewRequest(http.MethodPost, "https://api.anthropic.com/v1/messages", strings.NewReader(`{"model":"x"}`))
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("disabled client must pass through: %v", err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d", resp.StatusCode)
	}
}
