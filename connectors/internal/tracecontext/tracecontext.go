// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Package tracecontext is the shared W3C Trace Context extraction and deny-closed
// correlation seam for the Olivares AI network/mesh/gateway connectors.
//
// A mesh or gateway hop — an Envoy ALS entry, an ext_proc request, a Cilium Hubble
// L7 record, an API-gateway access log — propagates W3C Trace Context in request
// HEADERS. This package extracts the standard members (traceparent / tracestate /
// baggage) and hands them to a deny-closed Correlator (default no-op). It is the
// header-side companion of the seam left in connectors/mcp (AIP-09), which
// extracts the same members from an MCP `_meta` map; the field names and the
// hand-off shape are deliberately identical so a future correlator treats a mesh
// hop and an MCP hop the same way.
//
// Scope (honest about what this is NOT, §5):
//   - It does NOT emit traces, spans or /metrics — that is this connector
//     plane only CONSUMES the traceparent the mesh already propagates cross-hop.
//   - It does NOT join the trace to a span tree — that semantic correlation is
//     job; the Correlator default is therefore a no-op (declared, not yet
//     correlated), exactly like the traceCorrelator.
//   - It carries only correlation IDENTIFIERS, never payloads (docs/SECURITY-HARDENING.md), and is
//     stdlib-only so it can never become an exfiltration path.
//
// Standard pinning: the traceparent / tracestate field names and the version-`00`
// traceparent wire format validated here are the STABLE W3C Trace Context Level 1
// Recommendation. W3C Trace Context **Level 2** (w3.org/TR/trace-context-2) is a
// Candidate Recommendation *Draft* (2024-03-28) — NOT a stable standard — so this
// package validates only the stable v00 wire shape and claims no Level-2 semantics
// (pin the version and mark it where the standard is immature).
package tracecontext

import "strings"

// The W3C Trace Context HTTP header / member names. These names are stable across
// the Level 1 Recommendation and the Level 2 draft; only Level 2 semantics differ.
const (
	HeaderTraceParent = "traceparent"
	HeaderTraceState  = "tracestate"
	HeaderBaggage     = "baggage"
)

// TraceContext is the W3C Trace Context a mesh/gateway hop carried, for cross-hop
// trace correlation. It holds only the standard correlation identifiers — never
// payloads (docs/SECURITY-HARDENING.md).
type TraceContext struct {
	TraceParent string
	TraceState  string
	Baggage     string
}

// Present reports whether any trace context was found. traceparent is the
// load-bearing member (it carries the trace-id/parent-id); tracestate and baggage
// are optional vendor context.
func (t TraceContext) Present() bool { return t.TraceParent != "" }

// FromGetter extracts the members using a header accessor. get is called with the
// lowercase header name and must return the header value or "" if absent; an
// http.Header.Get (case-insensitive) or a mesh proto's header lookup both fit.
func FromGetter(get func(name string) string) TraceContext {
	if get == nil {
		return TraceContext{}
	}
	return TraceContext{
		TraceParent: get(HeaderTraceParent),
		TraceState:  get(HeaderTraceState),
		Baggage:     get(HeaderBaggage),
	}
}

// FromHeaderMap extracts the members from a header map, looked up
// case-insensitively (HTTP/2 lowercases header names, but a mesh may surface them
// in any case). It allocates a lowercased view only when a non-lowercase key is
// present, so the common all-lowercase case is allocation-free.
func FromHeaderMap(h map[string]string) TraceContext {
	if len(h) == 0 {
		return TraceContext{}
	}
	get := func(name string) string {
		if v, ok := h[name]; ok { // fast path: header already lowercase
			return v
		}
		for k, v := range h {
			if strings.EqualFold(k, name) {
				return v
			}
		}
		return ""
	}
	return FromGetter(get)
}

// FromMeta mirrors the connectors/mcp `_meta` extraction (AIP-09) so a mesh
// signal that carries trace context in a structured `_meta` map rather than HTTP
// headers is handled identically. A missing map, a non-string value or an absent
// key yields an empty member.
func FromMeta(meta map[string]any) TraceContext {
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
		TraceParent: str(HeaderTraceParent),
		TraceState:  str(HeaderTraceState),
		Baggage:     str(HeaderBaggage),
	}
}

// ValidTraceParent reports whether s is a well-formed W3C Trace Context version-00
// traceparent: "00-<32-hex trace-id>-<16-hex parent-id>-<2-hex flags>", with the
// trace-id and parent-id each non-zero (an all-zero id is invalid per the spec).
// This is the STABLE Level 1 wire shape; it makes NO Level 2 claim. A connector
// uses it to gate a malformed value out before handing the context across the
// seam; the SEMANTIC correlation (joining it to a span tree) is concern.
func ValidTraceParent(s string) bool {
	// version "-" trace-id "-" parent-id "-" flags = 2+1+32+1+16+1+2 = 55 chars.
	parts := strings.Split(s, "-")
	if len(parts) != 4 {
		return false
	}
	version, traceID, parentID, flags := parts[0], parts[1], parts[2], parts[3]
	// Version 0xff is forbidden; this validator only accepts the current "00".
	if version != "00" {
		return false
	}
	if len(traceID) != 32 || !isHex(traceID) || isAllZero(traceID) {
		return false
	}
	if len(parentID) != 16 || !isHex(parentID) || isAllZero(parentID) {
		return false
	}
	if len(flags) != 2 || !isHex(flags) {
		return false
	}
	return true
}

// isHex reports whether s is non-empty and all lowercase-or-digit hex. The W3C
// format mandates lowercase hex, so an uppercase digit is rejected (not normalized)
// to avoid silently accepting an off-spec value.
func isHex(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		isDigit := c >= '0' && c <= '9'
		isLowerHex := c >= 'a' && c <= 'f'
		if !isDigit && !isLowerHex {
			return false
		}
	}
	return s != ""
}

// isAllZero reports whether every character of s is '0'.
func isAllZero(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] != '0' {
			return false
		}
	}
	return true
}

// Correlator is the deny-closed hand-off seam to the trace-correlation path
//. The default (NopCorrelator) is a no-op: trace context is extracted
// and carried, but not yet joined to a span tree. An implementation MUST be
// non-blocking and best-effort — correlation is observability, never a gate on
// observation (docs/SECURITY-HARDENING.md: a collector failure must not break the data path).
type Correlator interface {
	// Correlate hands one hop's observed trace context to the correlation path,
	// keyed by a stable, non-sensitive correlation key (e.g. "<origin>-><fqdn>").
	Correlate(key string, tc TraceContext)
}

// NopCorrelator is the default Correlator: it discards the trace context. It is the
// honest "declared, not correlated" posture until wires a concrete correlator.
type NopCorrelator struct{}

// Correlate does nothing.
func (NopCorrelator) Correlate(string, TraceContext) {}
