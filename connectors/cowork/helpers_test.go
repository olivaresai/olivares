// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package cowork

import (
	"bytes"
	"context"
	"net/http"
	"sync"
	"testing"
	"time"

	collogspb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	logspb "go.opentelemetry.io/proto/otlp/logs/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"

	"github.com/olivaresai/olivares/sdk/model"
)

// testTime is the canonical deterministic clock for every test (never time.Now).
var testTime = time.Date(2026, 6, 9, 12, 0, 0, 0, time.UTC)

// --- OTLP log fixture builders (built in-code; no golden files) -----------------

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

// coworkRes is the standard Cowork resource-attribute set used by most fixtures.
func coworkRes(extra ...*commonpb.KeyValue) []*commonpb.KeyValue {
	base := []*commonpb.KeyValue{
		kvStr(attrServiceName, serviceNameCowork),
		kvStr(attrSessionID, "sess-1"),
		kvStr(attrOrgID, "org-9"),
		kvStr(attrAccountID, "user_01ACC"),
		kvStr(attrAccountUUID, "uuid-acc-1"),
	}
	return append(base, extra...)
}

// --- fake Sink (concurrency-safe for -race) -------------------------------------

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
		if e, ok := o.(model.FindingReport); ok {
			out = append(out, e)
		}
	}
	return out
}

func (f *fakeSink) costs() []model.CostSample {
	var out []model.CostSample
	for _, o := range f.all() {
		if e, ok := o.(model.CostSample); ok {
			out = append(out, e)
		}
	}
	return out
}

// findingsOfKind returns the findings whose Kind matches.
func (f *fakeSink) findingsOfKind(kind string) []model.FindingReport {
	var out []model.FindingReport
	for _, fr := range f.findings() {
		if fr.Kind == kind {
			out = append(out, fr)
		}
	}
	return out
}

// --- HTTP drivers for the e2e test ----------------------------------------------

func postRetry(t *testing.T, url, contentType string, body []byte, header, token string) *http.Response {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
		if err != nil {
			t.Fatalf("build request: %v", err)
		}
		req.Header.Set("Content-Type", contentType)
		if token != "" {
			req.Header.Set(header, token)
		}
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			return resp
		}
		if time.Now().After(deadline) {
			t.Fatalf("POST %s failed: %v", url, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func waitFor(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
}
