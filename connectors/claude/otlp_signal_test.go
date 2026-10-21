// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package claude

import (
	"math"
	"testing"
	"time"

	colmetricspb "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	coltracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"

	"github.com/olivaresai/olivares/sdk/model"
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

// A metrics batch is liveness evidence of the instant it reports, not of the instant
// it arrived (#2268). A collector that buffered or replayed a batch, or a seeder that
// back-fills history, used to make every session in it read as active now: 48 sessions
// with one event each, all "seen 43 seconds ago". The datapoints carry their own time
// (the span path and the metric values already read it); the receiver clock stays the
// answer only where a batch carries none, and a time from the future is not believed.
func TestIngestMetricsSignalCarriesTheBatchTime(t *testing.T) {
	dp := func(at time.Time) *metricspb.NumberDataPoint {
		var ns uint64
		if !at.IsZero() {
			ns = uint64(at.UnixNano())
		}
		return &metricspb.NumberDataPoint{
			TimeUnixNano: ns,
			Value:        &metricspb.NumberDataPoint_AsInt{AsInt: 1},
		}
	}
	batch := func(session string, points ...*metricspb.NumberDataPoint) *colmetricspb.ExportMetricsServiceRequest {
		return &colmetricspb.ExportMetricsServiceRequest{ResourceMetrics: []*metricspb.ResourceMetrics{{
			Resource: &resourcepb.Resource{Attributes: []*commonpb.KeyValue{kvStr(attrSessionID, session)}},
			ScopeMetrics: []*metricspb.ScopeMetrics{{Metrics: []*metricspb.Metric{{
				Name: "claude_code.session.count",
				Data: &metricspb.Metric_Sum{Sum: &metricspb.Sum{DataPoints: points}},
			}}}},
		}}}
	}
	earlier := testTime.Add(-72 * time.Hour)
	cases := []struct {
		name string
		req  *colmetricspb.ExportMetricsServiceRequest
		want time.Time
	}{
		{"a replayed batch reports its own time", batch("s-old", dp(earlier)), earlier},
		{"the newest datapoint of the batch is the session's evidence", batch("s-new", dp(earlier), dp(testTime.Add(-time.Hour))), testTime.Add(-time.Hour)},
		{"no datapoint time falls back to the receiver clock", batch("s-none", dp(time.Time{})), testTime},
		{"a time from the future is not believed", batch("s-future", dp(testTime.Add(48*time.Hour))), testTime},
		// A hostile or broken producer: the unsigned instant wraps negative in an int64
		// and would read as 1677-1969, which is "before" any clock.
		{"an instant past the int64 range is not believed", batch("s-wrap", dp(time.Time{})), testTime},
		{"one future datapoint does not discard the valid ones", batch("s-mixed", dp(testTime.Add(48*time.Hour)), dp(earlier)), earlier},
		{"the newest datapoint wins, not the last one", batch("s-order", dp(testTime.Add(-time.Hour)), dp(earlier)), testTime.Add(-time.Hour)},
	}
	// The wrap case needs a raw nanosecond value no time.Time can carry.
	wrapped := batch("s-wrap")
	wrapped.ResourceMetrics[0].ScopeMetrics[0].Metrics[0].GetSum().DataPoints = []*metricspb.NumberDataPoint{
		{TimeUnixNano: math.MaxUint64, Value: &metricspb.NumberDataPoint_AsInt{AsInt: 1}},
	}
	for i := range cases {
		if cases[i].name == "an instant past the int64 range is not believed" {
			cases[i].req = wrapped
		}
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var got []time.Time
			r := &receiver{onSignal: func(_ claudeIdentity, at time.Time) { got = append(got, at) }, now: func() time.Time { return testTime }}
			r.ingestMetrics(c.req)
			if len(got) != 1 {
				t.Fatalf("%d signals for one resource batch, want 1", len(got))
			}
			if !got[0].Equal(c.want) {
				t.Errorf("signal time = %s, want %s", got[0], c.want)
			}
		})
	}
}

// The same wrap guard on the two readers that already trusted sender time.
func TestSenderTimeBeyondInt64FallsBack(t *testing.T) {
	if got := spanTime(math.MaxUint64, testTime); !got.Equal(testTime) {
		t.Errorf("spanTime(MaxUint64) = %s, want the fallback %s", got, testTime)
	}
	dp := &metricspb.NumberDataPoint{TimeUnixNano: math.MaxUint64}
	if got := dpTime(dp, testTime); !got.Equal(testTime) {
		t.Errorf("dpTime(MaxUint64) = %s, want the fallback %s", got, testTime)
	}
}

// A replayed batch must not pull a session's last-OTEL time backwards: a hook that
// fired a minute ago beside an OTEL time of ten minutes ago is the anti-evasion
// signature, and an old batch arriving late is not evasion.
func TestWatchdogLivenessNeverMovesBackwards(t *testing.T) {
	var findings int
	wd := newWatchdog(5*time.Minute, func(model.Observation) { findings++ })
	now := testTime
	wd.observeOTEL("s", now.Add(-30*time.Second))
	wd.observeHook("s", now.Add(-10*time.Second))
	wd.observeOTEL("s", now.Add(-10*time.Minute)) // a replayed batch arriving late
	wd.sweep(now)
	if findings != 0 {
		t.Fatalf("a late old batch produced %d anti-evasion findings, want 0", findings)
	}
	// The real gap still fires: OTEL really silent past the threshold, hooks fresh.
	wd2 := newWatchdog(5*time.Minute, func(model.Observation) { findings++ })
	wd2.observeOTEL("g", now.Add(-10*time.Minute))
	wd2.observeHook("g", now.Add(-10*time.Second))
	wd2.sweep(now)
	if findings != 1 {
		t.Fatalf("a real OTEL gap produced %d findings, want 1", findings)
	}
}

// A sender's time is evidence, not a clock. Every place the receiver turns a time the
// producer sent into the time a signal is recorded at must not believe one after the
// receiver clock, or a span, record or datapoint that claims the year 2262 pins its
// session ahead of the clock (liveness, identity edge, correlator, stored sample).
func TestReceiverNeverRecordsASignalAfterItsClock(t *testing.T) {
	future := time.Date(2262, 1, 1, 0, 0, 0, 0, time.UTC)
	earlier := testTime.Add(-72 * time.Hour)
	session := []*commonpb.KeyValue{kvStr(attrSessionID, "s")}
	newRcv := func() *receiver { return &receiver{now: func() time.Time { return testTime }} }
	nanos := func(at time.Time) uint64 {
		if at.IsZero() {
			return 0
		}
		return uint64(at.UnixNano())
	}
	numberBatch := func(at time.Time) *colmetricspb.ExportMetricsServiceRequest {
		return &colmetricspb.ExportMetricsServiceRequest{ResourceMetrics: []*metricspb.ResourceMetrics{{
			Resource: &resourcepb.Resource{Attributes: session},
			ScopeMetrics: []*metricspb.ScopeMetrics{{Metrics: []*metricspb.Metric{{
				Name: "claude_code.session.count",
				Data: &metricspb.Metric_Sum{Sum: &metricspb.Sum{DataPoints: []*metricspb.NumberDataPoint{{
					TimeUnixNano: nanos(at),
					Value:        &metricspb.NumberDataPoint_AsInt{AsInt: 1},
				}}}},
			}}}},
		}}}
	}
	sites := []struct {
		name string
		run  func(at time.Time) time.Time
	}{
		{"claude_code log record", func(at time.Time) time.Time {
			r := newRcv()
			var got time.Time
			r.onOTEL = func(e claudeEvent) { got = e.at }
			r.ingestLogs(exportLogs(session, logRecord(evtUserPrompt, at)))
			return got
		}},
		{"claude_code span", func(at time.Time) time.Time {
			r := newRcv()
			var got time.Time
			r.onSignal = func(_ claudeIdentity, t time.Time) { got = t }
			r.ingestTraces(exportSpans(session, genAISpan("claude_code.tool", nil, at)))
			return got
		}},
		{"claude_code subagent span", func(at time.Time) time.Time {
			r := newRcv()
			var got time.Time
			r.onAgentSpan = func(_, _, _ string, t time.Time) { got = t }
			r.ingestTraces(exportSpans(append(session, kvStr(attrAgentID, "a1")), genAISpan(spanTool, nil, at)))
			return got
		}},
		{"gen_ai log record", func(at time.Time) time.Time {
			r := newRcv()
			var got time.Time
			r.onGenAI = func(e genAIEvent) { got = e.at }
			r.ingestLogs(exportLogs(nil, logRecord("gen_ai.client.inference.operation.details", at, genAIToolRecord(time.Time{})...)))
			return got
		}},
		{"gen_ai span", func(at time.Time) time.Time {
			r := newRcv()
			var got time.Time
			r.onGenAI = func(e genAIEvent) { got = e.at }
			r.ingestTraces(exportSpans(nil, genAISpan("chat gpt-4o", []byte("12345678"), at, fixtureLangChainSpan()...)))
			return got
		}},
		{"metric value datapoint", func(at time.Time) time.Time {
			r := newRcv()
			var got time.Time
			r.onMetric = func(m claudeMetric) { got = m.at }
			r.ingestMetrics(numberBatch(at))
			return got
		}},
	}
	for _, c := range sites {
		t.Run(c.name+": a future time is the receiver clock", func(t *testing.T) {
			if got := c.run(future); !got.Equal(testTime) {
				t.Errorf("recorded at %s, want the receiver clock %s", got, testTime)
			}
		})
		t.Run(c.name+": a missing time is the receiver clock", func(t *testing.T) {
			if got := c.run(time.Time{}); !got.Equal(testTime) {
				t.Errorf("recorded at %s, want the receiver clock %s", got, testTime)
			}
		})
		t.Run(c.name+": a past time is kept", func(t *testing.T) {
			if got := c.run(earlier); !got.Equal(earlier) {
				t.Errorf("recorded at %s, want its own time %s", got, earlier)
			}
		})
	}
}

// The failure #2272 names, end to end: one span with a start time of the year 2262
// used to set a session's last-OTEL time ahead of the clock, and later real signals
// could not lower it, so the gap finding never fired and the session was never evicted.
func TestFarFutureSpanDoesNotPinSessionLiveness(t *testing.T) {
	var findings int
	clock := testTime
	wd := newWatchdog(5*time.Minute, func(model.Observation) { findings++ })
	r := &receiver{
		now:      func() time.Time { return clock },
		onSignal: func(id claudeIdentity, at time.Time) { wd.observeOTEL(id.sessionID, at) },
	}
	session := []*commonpb.KeyValue{kvStr(attrSessionID, "s")}
	r.ingestTraces(exportSpans(session, genAISpan("claude_code.tool", nil, time.Date(2262, 1, 1, 0, 0, 0, 0, time.UTC))))

	clock = testTime.Add(10 * time.Minute) // OTEL stays silent while hooks keep firing
	wd.observeHook("s", clock.Add(-10*time.Second))
	wd.sweep(clock)
	if findings != 1 {
		t.Fatalf("far-future span then a real OTEL gap produced %d findings, want 1", findings)
	}

	clock = clock.Add(evictionTTL + time.Minute)
	wd.sweep(clock)
	if n := len(wd.sessions); n != 0 {
		t.Fatalf("an idle session is kept after a far-future span: %d sessions, want 0", n)
	}
}

// A hook that arrives late must not lower a session's last-hook time (it would make a
// healthy session look as if its hooks had stopped), and an old OTEL batch must not
// re-arm a gap that was already reported.
func TestWatchdogHooksNeverMoveBackwardsAndOldOTELDoesNotRearm(t *testing.T) {
	var findings int
	wd := newWatchdog(5*time.Minute, func(model.Observation) { findings++ })
	now := testTime
	wd.observeOTEL("s", now.Add(-10*time.Minute))
	wd.observeHook("s", now.Add(-10*time.Second))
	wd.observeHook("s", now.Add(-20*time.Minute)) // a late hook from long ago
	wd.sweep(now)
	if findings != 1 {
		t.Fatalf("a late old hook hid the real gap: %d findings, want 1", findings)
	}
	wd.observeOTEL("s", now.Add(-30*time.Minute)) // an old batch arrives after the report
	wd.sweep(now)
	if findings != 1 {
		t.Fatalf("an old OTEL batch re-armed a reported gap: %d findings, want 1", findings)
	}
	wd.observeOTEL("s", now) // real OTEL is back: the gap can be reported again
	wd.observeHook("s", now.Add(10*time.Minute))
	wd.sweep(now.Add(10 * time.Minute))
	if findings != 2 {
		t.Fatalf("a recovered session that relapsed was not flagged again: %d findings, want 2", findings)
	}
}

// A batch of histograms (the gen_ai client metrics) reports its time like a counter
// does, and so does the gen_ai liveness it feeds.
func TestHistogramBatchReportsItsOwnTime(t *testing.T) {
	earlier := testTime.Add(-72 * time.Hour)
	rm := &metricspb.ResourceMetrics{ScopeMetrics: []*metricspb.ScopeMetrics{{Metrics: []*metricspb.Metric{
		{Name: "gen_ai.client.operation.duration", Data: &metricspb.Metric_Histogram{Histogram: &metricspb.Histogram{
			DataPoints: []*metricspb.HistogramDataPoint{{TimeUnixNano: uint64(earlier.UnixNano())}},
		}}},
	}}}}
	if got := batchTime(rm, testTime); !got.Equal(earlier) {
		t.Errorf("batchTime of a histogram batch = %s, want %s", got, earlier)
	}

	var got time.Time
	r := &receiver{
		onGenAIMetric: func(_ string, at time.Time) { got = at },
		now:           func() time.Time { return testTime },
	}
	r.ingestMetrics(&colmetricspb.ExportMetricsServiceRequest{ResourceMetrics: []*metricspb.ResourceMetrics{rm}})
	if !got.Equal(earlier) {
		t.Errorf("gen_ai metric liveness time = %s, want the batch's %s", got, earlier)
	}
}

// batchTime reads the datapoints of every aggregation, across every metric and scope of
// the batch: the newest one sits alone in the last scope, beside an older counter.
func TestBatchTimeReadsEveryAggregationAndScope(t *testing.T) {
	older := testTime.Add(-96 * time.Hour)
	newest := testTime.Add(-30 * time.Minute)
	ns := uint64(newest.UnixNano())
	data := map[string]any{
		"sum":                   &metricspb.Metric_Sum{Sum: &metricspb.Sum{DataPoints: []*metricspb.NumberDataPoint{{TimeUnixNano: ns}}}},
		"gauge":                 &metricspb.Metric_Gauge{Gauge: &metricspb.Gauge{DataPoints: []*metricspb.NumberDataPoint{{TimeUnixNano: ns}}}},
		"histogram":             &metricspb.Metric_Histogram{Histogram: &metricspb.Histogram{DataPoints: []*metricspb.HistogramDataPoint{{TimeUnixNano: ns}}}},
		"exponential histogram": &metricspb.Metric_ExponentialHistogram{ExponentialHistogram: &metricspb.ExponentialHistogram{DataPoints: []*metricspb.ExponentialHistogramDataPoint{{TimeUnixNano: ns}}}},
		"summary":               &metricspb.Metric_Summary{Summary: &metricspb.Summary{DataPoints: []*metricspb.SummaryDataPoint{{TimeUnixNano: ns}}}},
	}
	for name, d := range data {
		t.Run(name, func(t *testing.T) {
			metric := &metricspb.Metric{Name: "m"}
			switch v := d.(type) {
			case *metricspb.Metric_Sum:
				metric.Data = v
			case *metricspb.Metric_Gauge:
				metric.Data = v
			case *metricspb.Metric_Histogram:
				metric.Data = v
			case *metricspb.Metric_ExponentialHistogram:
				metric.Data = v
			case *metricspb.Metric_Summary:
				metric.Data = v
			}
			rm := &metricspb.ResourceMetrics{ScopeMetrics: []*metricspb.ScopeMetrics{
				{Metrics: []*metricspb.Metric{{Name: "old", Data: &metricspb.Metric_Sum{Sum: &metricspb.Sum{
					DataPoints: []*metricspb.NumberDataPoint{{TimeUnixNano: uint64(older.UnixNano())}},
				}}}}},
				{Metrics: []*metricspb.Metric{metric}},
			}}
			if got := batchTime(rm, testTime); !got.Equal(newest) {
				t.Errorf("batchTime = %s, want the newest datapoint %s", got, newest)
			}
		})
	}
}
