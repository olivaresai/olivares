// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package trace

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/propagation"
	oteltrace "go.opentelemetry.io/otel/trace"
)

// OpenTelemetry GenAI semantic-convention attribute keys (status: Development).
// Verified verbatim against the dedicated open-telemetry/semantic-conventions-genai
// repo (docs/gen-ai/gen-ai-spans.md), the home these conventions moved to after
// semconv v1.42.0 (2026-06-12, #3696) deprecated and REMOVED gen_ai.* from the main
// semantic-conventions repo; re-verified at main@c321d7e on 2026-07-05.
// gen_ai.provider.name REPLACED the deprecated gen_ai.system. NOTE the cache-token
// keys use DOTS before input_tokens (gen_ai.usage.cache_read.input_tokens) — the
// underscore forms are Anthropic's RAW API field names, not OTel attribute keys.
const (
	attrGenAIProvider      = "gen_ai.provider.name"
	attrGenAIOperation     = "gen_ai.operation.name"
	attrGenAIRequestModel  = "gen_ai.request.model"
	attrGenAIResponseModel = "gen_ai.response.model"
	attrGenAIResponseID    = "gen_ai.response.id"
	attrGenAIFinishReasons = "gen_ai.response.finish_reasons"
	attrGenAIInputTokens   = "gen_ai.usage.input_tokens"
	attrGenAIOutputTokens  = "gen_ai.usage.output_tokens"
	attrGenAICacheRead     = "gen_ai.usage.cache_read.input_tokens"
	attrGenAICacheCreation = "gen_ai.usage.cache_creation.input_tokens"
	attrGenAITokenType     = "gen_ai.token.type"
	// Product keys live under the reserved reverse-DNS namespace ai.olivares.*
	// (freeze); the gen_ai.* keys above are OTel semconv, not ours to move.
	attrOlivaresRequestSHA  = "ai.olivares.inference.request.body_sha256"
	attrOlivaresResponseSHA = "ai.olivares.inference.response.body_sha256"

	// Deprecated compat-only predecessors, emitted only behind
	// OLIVARES_OTEL_GENAI_COMPAT while the GenAI conventions remain Development.
	attrGenAICompatSystem           = "gen_ai.system"
	attrGenAICompatPromptTokens     = "gen_ai.usage.prompt_tokens"
	attrGenAICompatCompletionTokens = "gen_ai.usage.completion_tokens"

	// providerAnthropic is the REQUIRED gen_ai.provider.name value for Claude
	// (semantic-conventions-genai docs/gen-ai/anthropic.md: gen_ai.provider.name
	// MUST be set to "anthropic"; re-verified at main@c321d7e on 2026-07-05).
	providerAnthropic = "anthropic"
	// opChat is the gen_ai.operation.name for a Messages call.
	opChat = "chat"
)

// Metric instrument names + the spec's prescribed ExplicitBucketBoundaries (verified
// verbatim against open-telemetry/semantic-conventions-genai docs/gen-ai/
// gen-ai-metrics.md — the dedicated repo gen_ai.* moved to after semconv v1.42.0;
// re-verified at main@c321d7e on 2026-07-05, boundaries unchanged. Status
// Development — advisory SHOULD boundaries, pinned here as the single source).
const (
	metricTokenUsage = "gen_ai.client.token.usage"
	metricOpDuration = "gen_ai.client.operation.duration"
)

var (
	// tokenUsageBuckets: power-of-4 from 1 token to ~64M (14 boundaries).
	tokenUsageBuckets = []float64{1, 4, 16, 64, 256, 1024, 4096, 16384, 65536, 262144, 1048576, 4194304, 16777216, 67108864}
	// operationDurationBuckets: power-of-2 from 10ms to 81.92s (14 boundaries).
	operationDurationBuckets = []float64{0.01, 0.02, 0.04, 0.08, 0.16, 0.32, 0.64, 1.28, 2.56, 5.12, 10.24, 20.48, 40.96, 81.92}
)

// maxGenAIBody caps how much of a request/response body the transport buffers to
// parse model/usage. Control-plane inference (judge/screen/embeddings) payloads are
// bounded; beyond the cap the transport degrades gracefully (skips that field) and
// never holds an unbounded body in memory.
const maxGenAIBody = 4 << 20 // 4 MiB

// genAIInstruments are the two OTel GenAI client metrics.
type genAIInstruments struct {
	tokenUsage metric.Int64Histogram
	duration   metric.Float64Histogram
}

func newGenAIInstruments(m metric.Meter) (*genAIInstruments, error) {
	tu, err := m.Int64Histogram(metricTokenUsage,
		metric.WithUnit("{token}"),
		metric.WithDescription("Number of input and output tokens used."),
		metric.WithExplicitBucketBoundaries(tokenUsageBuckets...),
	)
	if err != nil {
		return nil, err
	}
	d, err := m.Float64Histogram(metricOpDuration,
		metric.WithUnit("s"),
		metric.WithDescription("GenAI operation duration."),
		metric.WithExplicitBucketBoundaries(operationDurationBuckets...),
	)
	if err != nil {
		return nil, err
	}
	return &genAIInstruments{tokenUsage: tu, duration: d}, nil
}

// AnthropicUsage is the raw token accounting from an Anthropic Messages response —
// the SINGLE input to the centralized GenAI/cost math. Its field meanings match the
// connector's MessageUsage (claude-api): InputTokens is Anthropic's raw value, which
// EXCLUDES cached tokens.
type AnthropicUsage struct {
	InputTokens              int64
	OutputTokens             int64
	CacheReadInputTokens     int64
	CacheCreationInputTokens int64
}

// OTelInputTokens is the centralized accounting helper: the OTel
// gen_ai.usage.input_tokens value, which INCLUDES the cache tokens — total = raw
// input + cache_read + cache_creation. Per the dedicated semconv Anthropic page
// (semantic-conventions-genai docs/gen-ai/anthropic.md, note [15], re-verified
// 2026-06-21): "Anthropic input_tokens excludes cached tokens. Compute:
// gen_ai.usage.input_tokens = input_tokens + cache_read_input_tokens +
// cache_creation_input_tokens". This equals the inclusive total
// CostSample.InputTokens documents (sdk/model/observation.go), so the trace metric
// and FinOps cost never contradict each other.
func (u AnthropicUsage) OTelInputTokens() int64 {
	return u.InputTokens + u.CacheReadInputTokens + u.CacheCreationInputTokens
}

// AnthropicHTTPClient wraps base in an *http.Client that (1) injects W3C Trace
// Context into every outbound request (so the engine→Claude hop carries the same
// traceparent the service mesh observes) and (2) emits an OTel GenAI span +
// client metrics for Anthropic Messages calls. The returned client satisfies the
// connectors' Doer interface, so the composition root injects it as the inference
// client's transport. base nil uses http.DefaultTransport.
func (p *Provider) AnthropicHTTPClient(base http.RoundTripper) *http.Client {
	if base == nil {
		base = http.DefaultTransport
	}
	return &http.Client{Transport: &genAITransport{base: base, p: p}}
}

// genAITransport is the instrumenting RoundTripper behind AnthropicHTTPClient.
type genAITransport struct {
	base http.RoundTripper
	p    *Provider
}

func (t *genAITransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// Non-Messages request (e.g. the WIF /v1/oauth/token exchange, or any other
	// Anthropic call): propagate the trace context and pass through, no gen_ai span.
	if !isAnthropicMessages(req) || !t.p.enabled {
		t.p.propagator.Inject(req.Context(), propagation.HeaderCarrier(req.Header))
		return t.base.RoundTrip(req)
	}

	reqMeta := readRequestMetadata(req)
	reqModel := reqMeta.model
	spanName := opChat
	if reqModel != "" {
		spanName = opChat + " " + reqModel
	}
	ctx, span := t.p.tracer.Start(req.Context(), spanName, oteltrace.WithSpanKind(oteltrace.SpanKindClient))
	defer span.End()
	span.SetAttributes(
		attribute.String(attrGenAIProvider, providerAnthropic),
		attribute.String(attrGenAIOperation, opChat),
	)
	if t.p.genAICompat {
		span.SetAttributes(attribute.String(attrGenAICompatSystem, providerAnthropic))
	}
	if reqModel != "" {
		span.SetAttributes(attribute.String(attrGenAIRequestModel, reqModel))
	}
	if reqMeta.bodySHA != "" {
		// Body content never becomes telemetry. A bounded SHA-256 fingerprint is
		// enough to correlate the span with the proxy's signed ledger evidence.
		span.SetAttributes(attribute.String(attrOlivaresRequestSHA, reqMeta.bodySHA))
	}

	// Inject AFTER starting the span so the traceparent carries THIS client span —
	// the same hop identity the mesh (Envoy/Hubble) sees on the engine→Claude leg.
	req = req.WithContext(ctx)
	t.p.propagator.Inject(ctx, propagation.HeaderCarrier(req.Header))

	start := time.Now()
	resp, err := t.base.RoundTrip(req)
	elapsed := time.Since(start).Seconds()

	baseAttrs := []attribute.KeyValue{
		attribute.String(attrGenAIProvider, providerAnthropic),
		attribute.String(attrGenAIOperation, opChat),
	}
	if reqModel != "" {
		baseAttrs = append(baseAttrs, attribute.String(attrGenAIRequestModel, reqModel))
	}

	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "anthropic request failed")
		t.p.genai.duration.Record(ctx, elapsed, metric.WithAttributes(append(baseAttrs, attribute.String("error.type", "transport"))...))
		return resp, err
	}

	parsed := readResponseUsage(resp)
	metricAttrs := append([]attribute.KeyValue(nil), baseAttrs...)
	if parsed.bodySHA != "" {
		span.SetAttributes(attribute.String(attrOlivaresResponseSHA, parsed.bodySHA))
	}
	if parsed.model != "" {
		span.SetAttributes(attribute.String(attrGenAIResponseModel, parsed.model))
		metricAttrs = append(metricAttrs, attribute.String(attrGenAIResponseModel, parsed.model))
	}
	if parsed.id != "" {
		span.SetAttributes(attribute.String(attrGenAIResponseID, parsed.id))
	}
	if parsed.stopReason != "" {
		span.SetAttributes(attribute.StringSlice(attrGenAIFinishReasons, []string{parsed.stopReason}))
	}
	if parsed.hasUsage {
		otelInput := parsed.usage.OTelInputTokens()
		span.SetAttributes(
			attribute.Int64(attrGenAIInputTokens, otelInput),
			attribute.Int64(attrGenAIOutputTokens, parsed.usage.OutputTokens),
		)
		if t.p.genAICompat {
			// Deprecated legacy token keys mirror the same inclusive values as the
			// current Development keys so both vocabularies can never disagree on a
			// span. We do not emit Anthropic's raw cache-exclusive input_tokens under
			// the legacy prompt_tokens name.
			span.SetAttributes(
				attribute.Int64(attrGenAICompatPromptTokens, otelInput),
				attribute.Int64(attrGenAICompatCompletionTokens, parsed.usage.OutputTokens),
			)
		}
		if parsed.usage.CacheReadInputTokens > 0 {
			span.SetAttributes(attribute.Int64(attrGenAICacheRead, parsed.usage.CacheReadInputTokens))
		}
		if parsed.usage.CacheCreationInputTokens > 0 {
			span.SetAttributes(attribute.Int64(attrGenAICacheCreation, parsed.usage.CacheCreationInputTokens))
		}
		// Metrics intentionally stay current-only: no deprecated instrument names or
		// legacy attributes are emitted because duplicate metric streams add
		// cardinality/cost with no consumer demand while GenAI remains Development.
		// token.usage histogram is recorded once per token type (semconv). Each call
		// builds a FRESH attribute slice (never appends onto the shared metricAttrs
		// backing array) so the two records can never alias each other's token.type.
		recordTokens := func(tokType string, n int64) {
			attrs := make([]attribute.KeyValue, 0, len(metricAttrs)+1)
			attrs = append(attrs, metricAttrs...)
			attrs = append(attrs, attribute.String(attrGenAITokenType, tokType))
			t.p.genai.tokenUsage.Record(ctx, n, metric.WithAttributes(attrs...))
		}
		recordTokens("input", otelInput)
		recordTokens("output", parsed.usage.OutputTokens)
	}
	t.p.genai.duration.Record(ctx, elapsed, metric.WithAttributes(metricAttrs...))
	return resp, nil
}

// isAnthropicMessages reports whether req is an Anthropic Messages create call (the
// gen_ai "chat" operation). It is a POST to a /v1/messages path; batches/files/other
// endpoints are not gen_ai chat operations.
func isAnthropicMessages(req *http.Request) bool {
	if req.Method != http.MethodPost || req.URL == nil {
		return false
	}
	return strings.HasSuffix(strings.TrimRight(req.URL.Path, "/"), "/v1/messages") ||
		strings.HasSuffix(strings.TrimRight(req.URL.Path, "/"), "/messages")
}

type anthropicRequest struct {
	model   string
	bodySHA string
}

// readRequestMetadata buffers (capped) and restores the request body to read the
// model and compute a correlation fingerprint. On an over-cap/read error it returns
// no metadata and leaves the body intact for the real request. Raw content is never
// returned or attached to telemetry.
func readRequestMetadata(req *http.Request) anthropicRequest {
	if req.Body == nil {
		return anthropicRequest{}
	}
	buf, ok := drainAndRestore(&req.Body)
	if !ok {
		return anthropicRequest{}
	}
	var body struct {
		Model string `json:"model"`
	}
	_ = json.Unmarshal(buf, &body)
	sum := sha256.Sum256(buf)
	return anthropicRequest{model: body.Model, bodySHA: hex.EncodeToString(sum[:])}
}

// anthropicResponse is the minimal projection of a Messages response the transport
// reads for the GenAI span/metrics. Content is never decoded or exposed; the whole
// bounded body contributes only a SHA-256 correlation fingerprint.
type anthropicResponse struct {
	id         string
	model      string
	stopReason string
	bodySHA    string
	usage      AnthropicUsage
	hasUsage   bool
}

// readResponseUsage buffers (capped) and restores the response body to read id,
// model, stop_reason and usage. A streaming (SSE) response is skipped (no buffering).
func readResponseUsage(resp *http.Response) anthropicResponse {
	var out anthropicResponse
	if resp == nil || resp.Body == nil {
		return out
	}
	if ct := resp.Header.Get("Content-Type"); strings.Contains(ct, "text/event-stream") {
		return out // streaming: do not buffer; usage rides the final SSE event, out of scope
	}
	buf, ok := drainAndRestore(&resp.Body)
	if !ok {
		return out
	}
	sum := sha256.Sum256(buf)
	out.bodySHA = hex.EncodeToString(sum[:])
	var body struct {
		ID         string `json:"id"`
		Model      string `json:"model"`
		StopReason string `json:"stop_reason"`
		Usage      *struct {
			InputTokens              int64 `json:"input_tokens"`
			OutputTokens             int64 `json:"output_tokens"`
			CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
			CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
			CacheCreation            *struct {
				Ephemeral5m int64 `json:"ephemeral_5m_input_tokens"`
				Ephemeral1h int64 `json:"ephemeral_1h_input_tokens"`
			} `json:"cache_creation"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(buf, &body); err != nil {
		return out
	}
	out.id, out.model, out.stopReason = body.ID, body.Model, body.StopReason
	if body.Usage != nil {
		out.hasUsage = true
		cacheCreate := body.Usage.CacheCreationInputTokens
		if cacheCreate == 0 && body.Usage.CacheCreation != nil {
			cacheCreate = body.Usage.CacheCreation.Ephemeral5m + body.Usage.CacheCreation.Ephemeral1h
		}
		out.usage = AnthropicUsage{
			InputTokens:              body.Usage.InputTokens,
			OutputTokens:             body.Usage.OutputTokens,
			CacheReadInputTokens:     body.Usage.CacheReadInputTokens,
			CacheCreationInputTokens: cacheCreate,
		}
	}
	return out
}

// drainAndRestore reads up to maxGenAIBody from *bodyp, replaces *bodyp with a
// reader that re-serves what was read, and returns the buffered bytes. It returns
// ok=false (and restores the original body) when the body exceeds the cap or a read
// fails, so the real request/response is never corrupted by instrumentation.
func drainAndRestore(bodyp *io.ReadCloser) (buf []byte, ok bool) {
	orig := *bodyp
	limited := io.LimitReader(orig, maxGenAIBody+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		// Restore whatever could not be fully read by chaining the read prefix back.
		*bodyp = &restoredBody{Reader: bytes.NewReader(data), orig: orig}
		return nil, false
	}
	if int64(len(data)) > maxGenAIBody {
		// Over cap: do not parse; re-serve the read prefix followed by the remaining body.
		*bodyp = &restoredBody{Reader: bytes.NewReader(data), orig: orig}
		return nil, false
	}
	_ = orig.Close()
	*bodyp = io.NopCloser(bytes.NewReader(data))
	return data, true
}

// restoredBody re-serves an already-read prefix and then the remainder of the
// original body, so an over-cap or partially-read body is delivered intact.
type restoredBody struct {
	io.Reader
	orig io.ReadCloser
}

func (r *restoredBody) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	if err == io.EOF {
		// Prefix exhausted; continue with the remaining original body.
		r.Reader = r.orig
		if n > 0 {
			return n, nil
		}
		return r.orig.Read(p)
	}
	return n, err
}

func (r *restoredBody) Close() error { return r.orig.Close() }

// compile-time: the instrumented client is a connectors Doer (Do(*http.Request)).
var _ interface {
	Do(*http.Request) (*http.Response, error)
} = (*http.Client)(nil)
