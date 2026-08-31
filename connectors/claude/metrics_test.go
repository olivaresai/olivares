// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package claude

import (
	"testing"
	"time"

	colmetricspb "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"

	"github.com/olivaresai/olivares/sdk/model"
)

// --- metric construction helpers --------------------------------------------

// intDP / doubleDP build a NumberDataPoint with the given value, time and attributes.
func intDP(v int64, ts time.Time, attrs ...*commonpb.KeyValue) *metricspb.NumberDataPoint {
	return &metricspb.NumberDataPoint{
		Value:        &metricspb.NumberDataPoint_AsInt{AsInt: v},
		TimeUnixNano: uint64(ts.UnixNano()),
		Attributes:   attrs,
	}
}

func doubleDP(v float64, ts time.Time, attrs ...*commonpb.KeyValue) *metricspb.NumberDataPoint {
	return &metricspb.NumberDataPoint{
		Value:        &metricspb.NumberDataPoint_AsDouble{AsDouble: v},
		TimeUnixNano: uint64(ts.UnixNano()),
		Attributes:   attrs,
	}
}

// sumMetric builds a Sum metric with the given temporality and datapoints.
func sumMetric(name string, delta bool, dps ...*metricspb.NumberDataPoint) *metricspb.Metric {
	temp := metricspb.AggregationTemporality_AGGREGATION_TEMPORALITY_CUMULATIVE
	if delta {
		temp = metricspb.AggregationTemporality_AGGREGATION_TEMPORALITY_DELTA
	}
	return &metricspb.Metric{
		Name: name,
		Data: &metricspb.Metric_Sum{Sum: &metricspb.Sum{
			AggregationTemporality: temp,
			IsMonotonic:            true,
			DataPoints:             dps,
		}},
	}
}

// metricsReq wraps metrics under one resource batch with the given resource attrs.
func metricsReq(resAttrs []*commonpb.KeyValue, ms ...*metricspb.Metric) *colmetricspb.ExportMetricsServiceRequest {
	return &colmetricspb.ExportMetricsServiceRequest{ResourceMetrics: []*metricspb.ResourceMetrics{{
		Resource:     &resourcepb.Resource{Attributes: resAttrs},
		ScopeMetrics: []*metricspb.ScopeMetrics{{Metrics: ms}},
	}}}
}

// metricReceiver builds a receiver that records emitted metric values, with the
// resource-label allowlist honored (so the team label is collected).
func metricReceiver(allow ...string) (*receiver, *[]claudeMetric) {
	var got []claudeMetric
	r := &receiver{
		onMetric:  func(cm claudeMetric) { got = append(got, cm) },
		labelKeys: allow,
		now:       func() time.Time { return testTime },
	}
	return r, &got
}

// --- tests -------------------------------------------------------------------

func TestIngestMetricValuesDeltaWithDimensions(t *testing.T) {
	r, got := metricReceiver("team")
	res := []*commonpb.KeyValue{kvStr(attrSessionID, "sess-1"), kvStr("team", "payments")}
	req := metricsReq(res,
		sumMetric(metricLinesOfCode, true,
			intDP(40, testTime, kvStr(attrType, "added"), kvStr(attrModel, "claude-opus-4-8")),
			intDP(12, testTime, kvStr(attrType, "removed"), kvStr(attrModel, "claude-opus-4-8")),
		),
	)
	r.ingestMetrics(req)
	if len(*got) != 2 {
		t.Fatalf("want 2 datapoints, got %d", len(*got))
	}
	added := (*got)[0]
	if added.name != metricLinesOfCode || added.value != 40 || !added.additive {
		t.Errorf("added sample = %+v", added)
	}
	if added.session != "sess-1" || added.unit != "lines" {
		t.Errorf("attribution/unit = %+v", added)
	}
	// lines_of_code persists only `type` (model is aligned out — see adoptionMetricDimensions).
	if added.dims["type"] != "added" || len(added.dims) != 1 {
		t.Errorf("dims = %v (want only type=added)", added.dims)
	}
	if added.labels["team"] != "payments" {
		t.Errorf("operator team label must ride the sample, got %v", added.labels)
	}
	if (*got)[1].value != 12 || (*got)[1].dims["type"] != "removed" {
		t.Errorf("removed sample = %+v", (*got)[1])
	}
}

func TestIngestMetricValuesEditDecisionDims(t *testing.T) {
	r, got := metricReceiver()
	req := metricsReq([]*commonpb.KeyValue{kvStr(attrSessionID, "s")},
		sumMetric(metricCodeEditDecision, true,
			intDP(3, testTime, kvStr(attrToolName, "Edit"), kvStr(attrDecision, "accept"), kvStr("language", "Go")),
			intDP(1, testTime, kvStr(attrToolName, "Write"), kvStr(attrDecision, "reject"), kvStr("language", "Go")),
		),
	)
	r.ingestMetrics(req)
	if len(*got) != 2 {
		t.Fatalf("want 2, got %d", len(*got))
	}
	// tool + decision are persisted; language is recognized but deliberately NOT (it is
	// not on the Analytics feed and explodes cardinality on the busiest metric).
	if (*got)[0].dims["tool"] != "Edit" || (*got)[0].dims["decision"] != "accept" {
		t.Errorf("accept dims = %v", (*got)[0].dims)
	}
	if _, ok := (*got)[0].dims["language"]; ok {
		t.Errorf("language must NOT be persisted, got %v", (*got)[0].dims)
	}
	if (*got)[1].dims["decision"] != "reject" {
		t.Errorf("reject dims = %v", (*got)[1].dims)
	}
}

func TestIngestMetricValuesExcludesCostUsage(t *testing.T) {
	r, got := metricReceiver()
	req := metricsReq([]*commonpb.KeyValue{kvStr(attrSessionID, "s")},
		sumMetric(metricCostUsage, true, doubleDP(1.23, testTime, kvStr(attrModel, "claude-opus-4-8"))),
		sumMetric(metricTokenUsage, true, intDP(500, testTime, kvStr(attrType, "input"), kvStr(attrModel, "claude-opus-4-8"))),
	)
	r.ingestMetrics(req)
	// cost.usage is dropped (authoritative cost path is); token.usage IS persisted.
	if len(*got) != 1 {
		t.Fatalf("want 1 (cost.usage excluded), got %d: %+v", len(*got), *got)
	}
	if (*got)[0].name != metricTokenUsage || (*got)[0].value != 500 || (*got)[0].unit != "tokens" {
		t.Errorf("token sample = %+v", (*got)[0])
	}
	if (*got)[0].dims["type"] != "input" || (*got)[0].dims["model"] != "claude-opus-4-8" {
		t.Errorf("token dims = %v", (*got)[0].dims)
	}
}

func TestIngestMetricValuesActiveTimeSecondsToMillis(t *testing.T) {
	r, got := metricReceiver()
	req := metricsReq([]*commonpb.KeyValue{kvStr(attrSessionID, "s")},
		sumMetric(metricActiveTime, true, doubleDP(12.5, testTime, kvStr(attrType, "cli"))),
	)
	r.ingestMetrics(req)
	if len(*got) != 1 {
		t.Fatalf("want 1, got %d", len(*got))
	}
	if (*got)[0].value != 12500 || (*got)[0].unit != "ms" {
		t.Errorf("active_time 12.5s must become 12500ms, got value=%d unit=%q", (*got)[0].value, (*got)[0].unit)
	}
	if (*got)[0].dims["type"] != "cli" {
		t.Errorf("active_time dims = %v", (*got)[0].dims)
	}
}

func TestIngestMetricValuesCumulativeNotAdditive(t *testing.T) {
	r, got := metricReceiver()
	req := metricsReq([]*commonpb.KeyValue{kvStr(attrSessionID, "s")},
		sumMetric(metricCommit, false, intDP(7, testTime)),
	)
	r.ingestMetrics(req)
	if len(*got) != 1 || (*got)[0].additive {
		t.Fatalf("cumulative counter must be non-additive (snapshot), got %+v", *got)
	}
	if (*got)[0].value != 7 || (*got)[0].unit != "commits" || (*got)[0].dims != nil {
		t.Errorf("commit sample = %+v (dims should be nil)", (*got)[0])
	}
}

func TestIngestMetricValuesSessionAndLivenessTogether(t *testing.T) {
	// The SAME batch feeds BOTH the liveness signal and the metric values.
	var sigs int
	var got []claudeMetric
	r := &receiver{
		onSignal: func(claudeIdentity, time.Time) { sigs++ },
		onMetric: func(cm claudeMetric) { got = append(got, cm) },
		now:      func() time.Time { return testTime },
	}
	req := metricsReq([]*commonpb.KeyValue{kvStr(attrSessionID, "s"), kvStr(attrAccountUUID, "user_01")},
		sumMetric(metricSessionCount, true, intDP(1, testTime, kvStr("start_type", "fresh"))),
	)
	r.ingestMetrics(req)
	if sigs != 1 {
		t.Errorf("liveness must still fire once per batch, got %d", sigs)
	}
	// session.count carries no persisted dimensions (start_type recognized, not stored).
	if len(got) != 1 || got[0].unit != "sessions" || got[0].dims != nil {
		t.Errorf("session.count sample = %+v (dims should be nil)", got)
	}
}

func TestIngestMetricValuesSessionlessAndNilSafe(t *testing.T) {
	r, got := metricReceiver()
	// No session id on the resource → no metric attribution.
	r.ingestMetrics(metricsReq(nil, sumMetric(metricCommit, true, intDP(3, testTime))))
	if len(*got) != 0 {
		t.Errorf("session-less batch must persist no metric, got %d", len(*got))
	}
	// A receiver with no onMetric (flag off) must not panic and emits nothing extra.
	bare := &receiver{now: func() time.Time { return testTime }}
	bare.ingestMetrics(metricsReq([]*commonpb.KeyValue{kvStr(attrSessionID, "s")},
		sumMetric(metricCommit, true, intDP(3, testTime))))
}

func TestMetricValueRouterOffWhenFlagDisabled(t *testing.T) {
	s := &Source{cfg: config{claudeCodeMetrics: false}}
	if s.metricValueRouter(func(model.Observation) {}) != nil {
		t.Error("metricValueRouter must be nil when claude_code_metrics is off")
	}
	on := &Source{cfg: config{claudeCodeMetrics: true}}
	if on.metricValueRouter(func(model.Observation) {}) == nil {
		t.Error("metricValueRouter must be wired when claude_code_metrics is on")
	}
}

func TestMetricValueRouterMapsToMetricSample(t *testing.T) {
	var got []model.Observation
	s := &Source{cfg: config{claudeCodeMetrics: true}}
	route := s.metricValueRouter(func(o model.Observation) { got = append(got, o) })
	route(claudeMetric{
		name: metricPullRequest, value: 2, unit: "pull_requests", additive: true,
		session: "sess-9", at: testTime, labels: map[string]string{"team": "core"},
	})
	// A session-less / nameless sample is dropped.
	route(claudeMetric{name: metricCommit, value: 1, session: ""})
	if len(got) != 1 {
		t.Fatalf("want 1 MetricSample, got %d", len(got))
	}
	ms, ok := got[0].(model.MetricSample)
	if !ok {
		t.Fatalf("want MetricSample, got %T", got[0])
	}
	if ms.SubjectKind != subjectSession || ms.SubjectRef != "sess-9" || ms.Value != 2 || !ms.Additive {
		t.Errorf("mapped sample = %+v", ms)
	}
	if ms.Labels["team"] != "core" || !ms.OccurredAt.Equal(testTime) {
		t.Errorf("labels/time = %+v", ms)
	}
}
