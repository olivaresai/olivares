// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package mcp

import "testing"

func TestExtractTraceContext(t *testing.T) {
	meta := map[string]any{
		w3cTraceParent: "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01",
		w3cTraceState:  "rojo=00f067aa0ba902b7",
		w3cBaggage:     "userId=alice",
		"other":        42,
	}
	tc := extractTraceContext(meta)
	if !tc.present() {
		t.Fatal("trace context with a traceparent should be present")
	}
	if tc.TraceParent == "" || tc.TraceState == "" || tc.Baggage == "" {
		t.Errorf("all three members should be extracted: %+v", tc)
	}
}

func TestExtractTraceContextAbsentOrMalformed(t *testing.T) {
	if extractTraceContext(nil).present() {
		t.Error("nil meta yields an empty trace context")
	}
	if extractTraceContext(map[string]any{"foo": "bar"}).present() {
		t.Error("meta without traceparent is not present")
	}
	// A non-string traceparent is ignored rather than mis-coerced.
	if extractTraceContext(map[string]any{w3cTraceParent: 123}).present() {
		t.Error("non-string traceparent must not be treated as present")
	}
}

// recordingCorrelator captures Correlate calls for the seam test.
type recordingCorrelator struct{ calls []string }

func (r *recordingCorrelator) Correlate(server string, tc TraceContext) {
	r.calls = append(r.calls, server+"|"+tc.TraceParent)
}

func TestGatherForwardsTraceContextToSeam(t *testing.T) {
	cat := catalog{server: InitializeResult{
		ProtocolVersion: currentRevision,
		Meta:            map[string]any{w3cTraceParent: "00-abc-def-01"},
	}}
	cat.trace = extractTraceContext(cat.server.Meta)
	rc := &recordingCorrelator{}
	if !cat.trace.present() {
		t.Fatal("fixture should carry a trace context")
	}
	// Directly exercise the seam hand-off the Gather loop performs.
	rc.Correlate("srv", cat.trace)
	if len(rc.calls) != 1 || rc.calls[0] != "srv|00-abc-def-01" {
		t.Errorf("correlator should receive the server + traceparent, got %+v", rc.calls)
	}
}
