// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package claude

import (
	"context"
	"sync"
	"time"

	collogspb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	logspb "go.opentelemetry.io/proto/otlp/logs/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"

	"github.com/olivaresai/olivares/sdk/model"
)

// --- OTLP construction helpers ----------------------------------------------

func kvStr(k, v string) *commonpb.KeyValue {
	return &commonpb.KeyValue{Key: k, Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: v}}}
}

func kvInt(k string, v int64) *commonpb.KeyValue {
	return &commonpb.KeyValue{Key: k, Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_IntValue{IntValue: v}}}
}

func kvBool(k string, v bool) *commonpb.KeyValue {
	return &commonpb.KeyValue{Key: k, Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_BoolValue{BoolValue: v}}}
}

func kvDouble(k string, v float64) *commonpb.KeyValue {
	return &commonpb.KeyValue{Key: k, Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_DoubleValue{DoubleValue: v}}}
}

func kvObj(k string, fields ...*commonpb.KeyValue) *commonpb.KeyValue {
	return &commonpb.KeyValue{Key: k, Value: &commonpb.AnyValue{
		Value: &commonpb.AnyValue_KvlistValue{KvlistValue: &commonpb.KeyValueList{Values: fields}},
	}}
}

func logRecord(eventName string, ts time.Time, attrs ...*commonpb.KeyValue) *logspb.LogRecord {
	rec := &logspb.LogRecord{EventName: eventName, Attributes: attrs}
	if !ts.IsZero() {
		rec.TimeUnixNano = uint64(ts.UnixNano())
	}
	return rec
}

func exportLogs(resAttrs []*commonpb.KeyValue, records ...*logspb.LogRecord) *collogspb.ExportLogsServiceRequest {
	return &collogspb.ExportLogsServiceRequest{
		ResourceLogs: []*logspb.ResourceLogs{{
			Resource:  &resourcepb.Resource{Attributes: resAttrs},
			ScopeLogs: []*logspb.ScopeLogs{{LogRecords: records}},
		}},
	}
}

// --- fakeSink ----------------------------------------------------------------

// fakeSink is a concurrency-safe sdk.Sink that records every emitted
// observation, so a test can assert on what a connector produced.
type fakeSink struct {
	mu  sync.Mutex
	obs []model.Observation
}

func (f *fakeSink) Emit(_ context.Context, o model.Observation) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.obs = append(f.obs, o)
	return nil
}

func (f *fakeSink) all() []model.Observation {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]model.Observation(nil), f.obs...)
}

func (f *fakeSink) edges() []model.EdgeObservation {
	var out []model.EdgeObservation
	for _, o := range f.all() {
		if e, ok := o.(model.EdgeObservation); ok {
			out = append(out, e)
		}
	}
	return out
}

func (f *fakeSink) findings() []model.FindingReport {
	var out []model.FindingReport
	for _, o := range f.all() {
		if r, ok := o.(model.FindingReport); ok {
			out = append(out, r)
		}
	}
	return out
}

func (f *fakeSink) costs() []model.CostSample {
	var out []model.CostSample
	for _, o := range f.all() {
		if c, ok := o.(model.CostSample); ok {
			out = append(out, c)
		}
	}
	return out
}

func (f *fakeSink) metrics() []model.MetricSample {
	var out []model.MetricSample
	for _, o := range f.all() {
		if m, ok := o.(model.MetricSample); ok {
			out = append(out, m)
		}
	}
	return out
}

// collect is a simple func(model.Observation) sink for the in-memory components
// (correlator, watchdog) that records observations without the SDK interface.
type collect struct {
	mu  sync.Mutex
	obs []model.Observation
}

func (c *collect) emit(o model.Observation) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.obs = append(c.obs, o)
}

func (c *collect) edges() []model.EdgeObservation {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []model.EdgeObservation
	for _, o := range c.obs {
		if e, ok := o.(model.EdgeObservation); ok {
			out = append(out, e)
		}
	}
	return out
}

func (c *collect) findings() []model.FindingReport {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []model.FindingReport
	for _, o := range c.obs {
		if r, ok := o.(model.FindingReport); ok {
			out = append(out, r)
		}
	}
	return out
}

func (c *collect) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.obs)
}
