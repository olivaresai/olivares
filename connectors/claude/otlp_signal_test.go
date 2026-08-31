// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package claude

import (
	"testing"
	"time"

	colmetricspb "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	coltracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
)

func TestIngestTracesFeedsSignal(t *testing.T) {
	var got []claudeIdentity
	r := &receiver{
		onSignal: func(id claudeIdentity, _ time.Time) { got = append(got, id) },
		now:      func() time.Time { return testTime },
	}
	req := &coltracepb.ExportTraceServiceRequest{
		ResourceSpans: []*tracepb.ResourceSpans{{
			Resource: &resourcepb.Resource{Attributes: []*commonpb.KeyValue{
				kvStr(attrSessionID, "sess-trace"), kvStr(attrOrgID, "org-9"), kvStr(attrAgentName, "builder"),
			}},
			ScopeSpans: []*tracepb.ScopeSpans{{Spans: []*tracepb.Span{
				{Name: "claude_code.tool", StartTimeUnixNano: uint64(testTime.UnixNano())},
				{Name: "claude_code.llm_request"},
			}}},
		}},
	}
	r.ingestTraces(req)
	if len(got) != 2 {
		t.Fatalf("want 2 span signals (liveness), got %d", len(got))
	}
	if got[0].sessionID != "sess-trace" || got[0].orgID != "org-9" || got[0].agentName != "builder" {
		t.Errorf("span identity = %+v", got[0])
	}
}

func TestIngestTracesSkipsSpanWithoutSession(t *testing.T) {
	n := 0
	r := &receiver{onSignal: func(claudeIdentity, time.Time) { n++ }, now: func() time.Time { return testTime }}
	req := &coltracepb.ExportTraceServiceRequest{ResourceSpans: []*tracepb.ResourceSpans{{
		ScopeSpans: []*tracepb.ScopeSpans{{Spans: []*tracepb.Span{{Name: "claude_code.interaction"}}}},
	}}}
	r.ingestTraces(req)
	if n != 0 {
		t.Errorf("a span without a session must not feed a signal, got %d", n)
	}
}

func TestIngestMetricsFeedsSignal(t *testing.T) {
	var got []claudeIdentity
	r := &receiver{onSignal: func(id claudeIdentity, _ time.Time) { got = append(got, id) }, now: func() time.Time { return testTime }}
	req := &colmetricspb.ExportMetricsServiceRequest{ResourceMetrics: []*metricspb.ResourceMetrics{{
		Resource: &resourcepb.Resource{Attributes: []*commonpb.KeyValue{
			kvStr(attrSessionID, "sess-metric"), kvStr(attrAccountUUID, "user_01X"),
		}},
		ScopeMetrics: []*metricspb.ScopeMetrics{{Metrics: []*metricspb.Metric{{Name: "claude_code.session.count"}}}},
	}}}
	r.ingestMetrics(req)
	if len(got) != 1 || got[0].sessionID != "sess-metric" || got[0].accountID != "user_01X" {
		t.Errorf("metric signal = %+v", got)
	}
	// A resource-metrics batch without a session feeds nothing.
	got = nil
	r.ingestMetrics(&colmetricspb.ExportMetricsServiceRequest{ResourceMetrics: []*metricspb.ResourceMetrics{{}}})
	if len(got) != 0 {
		t.Errorf("session-less metrics must feed nothing, got %d", len(got))
	}
}

func TestIngestNilSignalSafe(t *testing.T) {
	// A cooperative receiver without the signal route must not panic on traces/metrics.
	r := &receiver{now: func() time.Time { return testTime }}
	r.ingestTraces(&coltracepb.ExportTraceServiceRequest{ResourceSpans: []*tracepb.ResourceSpans{{
		ScopeSpans: []*tracepb.ScopeSpans{{Spans: []*tracepb.Span{{Name: "x"}}}},
	}}})
	r.ingestMetrics(&colmetricspb.ExportMetricsServiceRequest{ResourceMetrics: []*metricspb.ResourceMetrics{{}}})
}

func TestSpanTimeFallback(t *testing.T) {
	if got := spanTime(0, testTime); !got.Equal(testTime) {
		t.Errorf("zero start time must fall back to the receiver clock, got %v", got)
	}
	if got := spanTime(uint64(testTime.UnixNano()), time.Time{}); got.UnixNano() != testTime.UnixNano() {
		t.Errorf("span time = %v", got)
	}
}
