// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package trace

import (
	"net/http"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/propagation"
	oteltrace "go.opentelemetry.io/otel/trace"
)

// HTTPMiddleware is the ingress chokepoint extractor (placed after the request-id
// middleware, before authenticate). It Extracts the W3C Trace Context from the
// inbound headers — continuing the caller's / mesh's trace — and, when enabled,
// starts a SERVER span so the request's work (audit events, the engine→Claude hop)
// shares one trace.
//
// Read-first (docs/SECURITY-HARDENING.md): Extract is read-only — it never writes to or mutates the
// inbound headers, so an upstream's tracestate is untouched (the W3C non-mutation
// rule). A malformed/unsupported traceparent never fails the request: it simply
// yields no parent and the request proceeds. Forward-compat is the propagator's
// (W3C Level 2 aware): the L2 random-trace-id flag and future traceparent versions
// are parsed, not rejected. A tracing fault NEVER fails the request (deny-open for
// telemetry only — the opposite of the security path, which is deny-closed).
//
// In no-op mode (no collector) it STILL installs the extracted REMOTE span context
// into the request context, so EnrichAuditMeta can correlate the ledger to the
// inbound trace even though the engine exports nothing of its own.
func (p *Provider) HTTPMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := p.propagator.Extract(r.Context(), propagation.HeaderCarrier(r.Header))
		if !p.enabled {
			next.ServeHTTP(w, r.WithContext(ctx))
			return
		}
		// Low-cardinality span name (method only): the raw path can carry ids/PII and
		// would explode cardinality; the matched chi route is not known this early in
		// the chain. The method attribute follows the OTel HTTP semconv key.
		ctx, span := p.tracer.Start(ctx, "HTTP "+r.Method,
			oteltrace.WithSpanKind(oteltrace.SpanKindServer),
			oteltrace.WithAttributes(attribute.String("http.request.method", r.Method)),
		)
		defer span.End()
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
