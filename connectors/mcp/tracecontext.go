// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package mcp

// AIP-09 — forward seam for W3C Trace Context correlation.
//
// The imminent MCP 2026-07-28 Release Candidate (locked 2026-05-21, final
// 2026-07-28) standardizes W3C Trace Context propagation in the `_meta` field of
// every request/result, locking down the traceparent / tracestate / baggage key
// names for OpenTelemetry correlation (SEP-414). This connector is built to the
// current stable revision (2025-11-25) and must NOT hard-code RC specifics — but it
// CAN prepare the seam now, because:
//   - `_meta` is an existing MCP concept (present since early revisions), and
//   - W3C Trace Context is a stable W3C Recommendation, not RC-specific.
// So extracting these keys from a result's `_meta` is safe and forward-compatible:
// a server already emitting them (or one speaking the RC) is correlated; one that
// is not yields an empty TraceContext and nothing happens.
//
// The actual OTLP/trace correlation belongs to the observability layer, which
// owns W3C Trace Context — so the hand-off is a deny-closed seam
// (traceCorrelator), defaulting to a no-op, exactly like the orchestration
// module's ApprovalGate/Dispatcher seams. The concrete correlator is wired when
// it lands; until then the extraction is live and the hand-off is inert.

// w3cTraceParent / w3cTraceState / w3cBaggage are the W3C Trace Context member
// names. The RC standardizes their placement under MCP `_meta`; the names
// themselves are the stable W3C Recommendation field names.
const (
	w3cTraceParent = "traceparent"
	w3cTraceState  = "tracestate"
	w3cBaggage     = "baggage"
)

// TraceContext is the W3C Trace Context a server carried in a result's `_meta`,
// for cross-SDK/gateway trace correlation. It carries only the standard
// correlation identifiers — never payloads (docs/SECURITY-HARDENING.md).
type TraceContext struct {
	TraceParent string
	TraceState  string
	Baggage     string
}

// present reports whether any trace-context member was found (traceparent is the
// load-bearing one; the others are optional context).
func (t TraceContext) present() bool { return t.TraceParent != "" }

// extractTraceContext reads the W3C Trace Context members from a result's `_meta`
// map. It is tolerant: a missing `_meta`, a non-string value, or absent keys yield
// an empty TraceContext (present()==false). It does not parse or validate the
// traceparent format — that is the correlator's concern; the connector only
// carries it across the seam.
func extractTraceContext(meta map[string]any) TraceContext {
	if meta == nil {
		return TraceContext{}
	}
	str := func(k string) string {
		if v, ok := meta[k].(string); ok {
			return v
		}
		return ""
	}
	return TraceContext{
		TraceParent: str(w3cTraceParent),
		TraceState:  str(w3cTraceState),
		Baggage:     str(w3cBaggage),
	}
}

// traceCorrelator is the deny-closed hand-off seam to the OTLP/trace correlation
// path. The default is a no-op; the observability layer supplies a concrete
// correlator that ties an MCP server's observed trace context into the span tree.
type traceCorrelator interface {
	// Correlate hands one server's observed W3C Trace Context to the correlation
	// path. It must be non-blocking and best-effort: correlation is observability,
	// never a gate on introspection.
	Correlate(server string, tc TraceContext)
}

// nopTraceCorrelator is the default: trace context is extracted but not yet wired
// anywhere (honest "declared, not correlated" until lands the correlator).
type nopTraceCorrelator struct{}

func (nopTraceCorrelator) Correlate(string, TraceContext) {}
