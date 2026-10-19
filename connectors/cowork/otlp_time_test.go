// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package cowork

import (
	"math"
	"net/http"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"
)

// A producer-supplied TimeUnixNano is evidence of when something happened, not a clock:
// zero, a value past MaxInt64 (it wraps negative in an int64 and reads as 1677-1969) and a
// time after the receiver clock all record the receiver clock; a past time is kept. A
// believable ObservedTimeUnixNano stands in for an unbelievable TimeUnixNano.
func TestIngestLogsRecordsSenderTimeOnlyWhenItIsBelievable(t *testing.T) {
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	past := now.Add(-72 * time.Hour)
	pastNs := uint64(past.UnixNano())
	future := uint64(time.Date(2262, 4, 1, 0, 0, 0, 0, time.UTC).UnixNano())
	for _, c := range []struct {
		name     string
		ns, seen uint64
		want     time.Time
	}{
		{"unset", 0, 0, now},
		{"past is kept", pastNs, 0, past},
		{"equal to the clock is kept", uint64(now.UnixNano()), 0, now},
		{"after the clock", uint64(now.Add(time.Second).UnixNano()), 0, now},
		{"year 2262", future, 0, now},
		{"MaxInt64 plus one wraps", math.MaxInt64 + 1, 0, now},
		{"MaxUint64 wraps", math.MaxUint64, 0, now},
		{"wrapped time, believable observed time", math.MaxUint64, pastNs, past},
		{"both believable: time wins over observed", pastNs, uint64(now.Add(-time.Hour).UnixNano()), past},
		{"year 2262 observed time", 0, future, now},
		{"MaxUint64 observed time", 0, math.MaxUint64, now},
	} {
		t.Run(c.name, func(t *testing.T) {
			var got []coworkEvent
			r := &receiver{
				onEvent:        func(ev coworkEvent) { got = append(got, ev) },
				requireService: serviceNameCowork,
				now:            func() time.Time { return now },
			}
			rec := logRecord(evtUserPrompt, time.Time{})
			rec.TimeUnixNano, rec.ObservedTimeUnixNano = c.ns, c.seen
			r.ingestLogs(exportLogs(coworkRes(), rec))
			if len(got) != 1 {
				t.Fatalf("ingested %d events, want 1", len(got))
			}
			if !got[0].at.Equal(c.want) {
				t.Errorf("at = %v, want %v", got[0].at, c.want)
			}
		})
	}
}

// A record that claims the year 2262, or a wrapped time, posted over the real OTLP/HTTP
// listener must not put any edge, cost sample or finding ahead of the clock (or before 2000)
// on the way to the sink.
func TestGatherNeverRecordsAnObservationOutsideTheClock(t *testing.T) {
	base, sink, stop := openGather(t, nil)
	defer stop()

	year2262 := time.Date(2262, 4, 1, 0, 0, 0, 0, time.UTC)
	wrapped := logRecord(evtAPIRequest, time.Time{}, kvStr(attrModel, "claude-opus-4-8"), kvDouble(attrCostUSD, 0.05), kvInt(attrInputTokens, 900))
	wrapped.TimeUnixNano = math.MaxUint64
	body, err := proto.Marshal(exportLogs(
		coworkRes(),
		logRecord(evtToolResult, year2262, kvStr(attrToolName, "Write"), kvStr(attrDecisionSource, srcConfig),
			kvObj(attrToolInput, kvStr("file_path", "/etc/app.conf"))),
		wrapped,
		logRecord(evtToolDecision, year2262, kvStr(attrToolName, "Bash"), kvStr(attrDecision, decisionReject), kvStr(attrSource, "user_reject")),
	))
	if err != nil {
		t.Fatal(err)
	}
	resp := postRetry(t, base+"/v1/logs", "application/x-protobuf", body, "", "")
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST status = %d, want 200", resp.StatusCode)
	}
	waitFor(t, 2*time.Second, func() bool {
		return len(sink.edges()) >= 3 && len(sink.costs()) == 1 && len(sink.findingsOfKind(findingKindPolicyDecision)) == 1
	})

	latest := time.Now()
	earliest := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	check := func(kind string, at time.Time) {
		if at.After(latest) || at.Before(earliest) {
			t.Errorf("%s recorded at %v, want a time between %v and %v", kind, at, earliest, latest)
		}
	}
	for _, e := range sink.edges() {
		check("edge "+e.ResourceKind, e.ObservedAt)
	}
	for _, c := range sink.costs() {
		check("cost sample", c.OccurredAt)
	}
	for _, f := range sink.findings() {
		check("finding "+f.Kind, f.OccurredAt)
	}
}
