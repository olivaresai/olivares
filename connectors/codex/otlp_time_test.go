// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package codex

import (
	"math"
	"testing"
	"time"

	collogspb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	logspb "go.opentelemetry.io/proto/otlp/logs/v1"
)

// A producer-supplied TimeUnixNano is evidence of when something happened, not a clock:
// zero, a value past MaxInt64 (it wraps negative in an int64 and reads as 1677-1969) and a
// time after the receiver clock all record the receiver clock; a past time is kept.
func TestIngestRecordsSenderTimeOnlyWhenItIsBelievable(t *testing.T) {
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	past := now.Add(-72 * time.Hour)
	for _, c := range []struct {
		name string
		ns   uint64
		want time.Time
	}{
		{"unset", 0, now},
		{"past is kept", uint64(past.UnixNano()), past},
		{"equal to the clock is kept", uint64(now.UnixNano()), now},
		{"after the clock", uint64(now.Add(time.Second).UnixNano()), now},
		{"year 2262", uint64(time.Date(2262, 4, 1, 0, 0, 0, 0, time.UTC).UnixNano()), now},
		{"MaxInt64 plus one wraps", math.MaxInt64 + 1, now},
		{"MaxUint64 wraps", math.MaxUint64, now},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := newOTLPReceiver("/", 10, func() time.Time { return now })
			r.ingest(&collogspb.ExportLogsServiceRequest{ResourceLogs: []*logspb.ResourceLogs{{
				ScopeLogs: []*logspb.ScopeLogs{{LogRecords: []*logspb.LogRecord{{
					TimeUnixNano: c.ns,
					Attributes:   []*commonpb.KeyValue{kv("event.name", "codex.user_prompt")},
				}}}},
			}}})
			evs, _ := r.drain()
			if len(evs) != 1 {
				t.Fatalf("ingested %d events, want 1", len(evs))
			}
			if !evs[0].At.Equal(c.want) {
				t.Errorf("At = %v, want %v", evs[0].At, c.want)
			}
		})
	}
}
