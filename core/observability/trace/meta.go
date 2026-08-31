// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package trace

import (
	"context"

	oteltrace "go.opentelemetry.io/otel/trace"
)

// Ledger correlation keys. A trace id / span id is CORRELATION, not payload — it
// carries no secret or PII (docs/SECURITY-HARDENING.md), so it rides the already-redacted,
// minimal-data AuditDraft.Meta. (dashboards) and any SIEM (via the existing
// audit export) read these to correlate a ledger event with the caller's trace.
const (
	// MetaTraceID is the W3C trace-id (32 lowercase hex chars) in AuditDraft.Meta.
	MetaTraceID = "trace_id"
	// MetaSpanID is the W3C span-id (16 lowercase hex chars) of the engine span
	// that produced the event.
	MetaSpanID = "span_id"
)

// ContextFrom returns the W3C trace-id/span-id of the span context in ctx, if
// one is present and valid. It works whether the context carries a recording engine
// span (live exporter) or only the extracted REMOTE span context from the inbound
// traceparent (no exporter configured) — so ledger correlation holds even when the
// engine exports nothing of its own. It imports only the OTel trace API (no SDK), so
// callers on the hot write path stay light.
func ContextFrom(ctx context.Context) (traceID, spanID string, ok bool) {
	sc := oteltrace.SpanContextFromContext(ctx)
	if !sc.HasTraceID() {
		return "", "", false
	}
	return sc.TraceID().String(), sc.SpanID().String(), true
}

// EnrichAuditMeta returns meta with the trace_id/span_id of ctx's span context
// added, or meta unchanged when there is no trace context. It NEVER mutates the
// caller's map (it copies), so an AuditDraft's Meta is not aliased into the ledger
// with surprise keys. When meta is nil and a trace context exists, it returns a new
// two-key map. This is the single point that correlates the ledger to the trace,
// called once from the store's audit Append chokepoint.
func EnrichAuditMeta(ctx context.Context, meta map[string]any) map[string]any {
	traceID, spanID, ok := ContextFrom(ctx)
	if !ok {
		return meta
	}
	out := make(map[string]any, len(meta)+2)
	for k, v := range meta {
		out[k] = v
	}
	out[MetaTraceID] = traceID
	out[MetaSpanID] = spanID
	return out
}
