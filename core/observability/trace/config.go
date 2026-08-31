// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// Package trace is the engine-side W3C Trace Context + OpenTelemetry GenAI
// observability layer (OBS-03). It does three things, all config-driven and
// fail-open (a tracing fault never breaks a request, docs/SECURITY-HARDENING.md):
//
//   - extracts the W3C Trace Context (traceparent/tracestate) at the API ingress
//     chokepoint WITHOUT mutating an upstream's tracestate or rejecting unknown
//     trace-flags bits (forward-compat with W3C Trace Context Level 2's
//     random-trace-id flag), continuing the client/mesh trace into the engine;
//   - carries the resulting trace_id/span_id into the tamper-evident audit ledger
//     (AuditDraft.Meta) — a trace id is correlation, not payload (docs/SECURITY-HARDENING.md) — so
//     access edges, cost and findings correlate with the caller's trace;
//   - injects the trace context into every engine→Claude hop (so the same
//     traceparent the service mesh observes, rides the engine's own request)
//     and emits an OpenTelemetry GenAI span (gen_ai.provider.name="anthropic") plus
//     the spec's client metrics.
//
// Standards pinning (verified against primary sources jun-2026; honored per
// §5): W3C Trace Context **Level 1** is the stable Recommendation we build
// to (traceparent/tracestate); Level 2 (w3.org/TR/trace-context-2, CR Draft
// 2024-03-28) is NOT a Recommendation — its random-trace-id trace-flags bit is
// treated forward-compatibly (the OTel propagator parses only the sampled bit and
// ignores other flags, never rejecting the header). The OpenTelemetry GenAI
// semantic conventions are status **Development** (not Stable); we mirror the same
// gen_ai.* attribute keys and the prescribed metric ExplicitBucketBoundaries the
// connectors/claude ingest profile pins (semconv 1.41.x line). OTLP (trace+metric
// protocol) is Stable and is the only export format on the critical path — no
// proprietary wire (docs/contracts demand 2).
package trace

import (
	"os"
	"strconv"
	"strings"
)

// defaultServiceName is the OTel resource service.name the engine reports.
const defaultServiceName = "olivares"

// Protocol selects the OTLP transport. Both are Stable OTLP; gRPC (4317) and
// HTTP/protobuf (4318) are the two transports the OTLP spec defines.
type Protocol string

const (
	// ProtocolGRPC is OTLP/gRPC (the default).
	ProtocolGRPC Protocol = "grpc"
	// ProtocolHTTP is OTLP/HTTP with binary protobuf payloads.
	ProtocolHTTP Protocol = "http/protobuf"
)

// Config is the trace/observability provisioning, resolved from the operator's
// environment (FromEnv). With Enabled false (the default — no endpoint configured)
// the Provider is a no-op exporter: it STILL extracts/injects the W3C context (so
// ledger correlation and mesh stitching work from any inbound trace), but starts no
// recording spans and exports nothing. Tracing is opt-in and self-hosted-friendly:
// the client points it at THEIR collector; nothing phones home (docs/SECURITY-HARDENING.md).
type Config struct {
	// Enabled turns on the recording TracerProvider/MeterProvider and the OTLP
	// exporters. It is true when OLIVARES_OTEL_ENABLED is truthy OR an endpoint is set.
	Enabled bool
	// Endpoint is the OTLP collector endpoint (host:port for gRPC; a URL or host:port
	// for HTTP). Empty disables export (no-op mode).
	Endpoint string
	// Protocol is grpc or http/protobuf.
	Protocol Protocol
	// Insecure sends plaintext (no TLS) — for an in-cluster collector on a trusted
	// network. Secure (TLS) is the default, matching the engine's secure-by-default
	// posture (docs/SECURITY-HARDENING.md).
	Insecure bool
	// SampleRatio is the parent-based head sampling ratio in [0,1] (1 = always
	// sample). A sampled parent (the client/mesh decision) is always respected.
	SampleRatio float64
	// ServiceName / ServiceVersion populate the OTel resource.
	ServiceName    string
	ServiceVersion string
	// GenAICompat adds deprecated GenAI span attributes for external backend
	// compatibility. Default off; this is additive dual-emission, not a switch:
	// current Development gen_ai.* names and metrics are still emitted unchanged.
	// As of 2026-07-05, OTel's OTEL_SEMCONV_STABILITY_OPT_IN known values define
	// no GenAI /dup token (https://raw.githubusercontent.com/open-telemetry/
	// opentelemetry-configuration/main/schema/instrumentation.yaml), and
	// semantic-conventions-genai main@c321d7e has no dual-emit migration guidance
	// (https://raw.githubusercontent.com/open-telemetry/
	// semantic-conventions-genai/c321d7e/docs/gen-ai/README.md). This knob is
	// Olivares-specific pending upstream guidance.
	GenAICompat bool
}

// FromEnv resolves the trace Config from the environment. It honors the product's
// OLIVARES_OTEL_* names AND the standard OTEL_EXPORTER_OTLP_* names (so an operator
// who already configured the OTel SDK env gets it for free), with the product names
// taking precedence. version stamps the resource service.version.
func FromEnv(version string) Config {
	cfg := Config{
		Protocol:       ProtocolGRPC,
		SampleRatio:    1.0,
		ServiceName:    defaultServiceName,
		ServiceVersion: version,
	}
	endpoint := firstNonEmpty(os.Getenv("OLIVARES_OTEL_ENDPOINT"), os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"))
	cfg.Endpoint = strings.TrimSpace(endpoint)

	if p := firstNonEmpty(os.Getenv("OLIVARES_OTEL_PROTOCOL"), os.Getenv("OTEL_EXPORTER_OTLP_PROTOCOL")); p != "" {
		switch strings.TrimSpace(strings.ToLower(p)) {
		case "http", "http/protobuf", "httpprotobuf":
			cfg.Protocol = ProtocolHTTP
		default:
			cfg.Protocol = ProtocolGRPC
		}
	}
	if v := os.Getenv("OLIVARES_OTEL_INSECURE"); truthy(v) {
		cfg.Insecure = true
	}
	if v := strings.TrimSpace(os.Getenv("OLIVARES_OTEL_SAMPLE_RATIO")); v != "" {
		if r, err := strconv.ParseFloat(v, 64); err == nil && r >= 0 && r <= 1 {
			cfg.SampleRatio = r
		}
	}
	if v := strings.TrimSpace(os.Getenv("OLIVARES_OTEL_SERVICE_NAME")); v != "" {
		cfg.ServiceName = v
	}
	cfg.GenAICompat = truthy(os.Getenv("OLIVARES_OTEL_GENAI_COMPAT"))
	// Enabled when explicitly turned on, or implicitly when an endpoint is configured.
	cfg.Enabled = truthy(os.Getenv("OLIVARES_OTEL_ENABLED")) || cfg.Endpoint != ""
	return cfg
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func truthy(v string) bool {
	v = strings.TrimSpace(strings.ToLower(v))
	return v == "1" || v == "true" || v == "yes" || v == "on"
}
