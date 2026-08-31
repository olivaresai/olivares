// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package openlineage

import (
	"testing"
	"time"
)

// TestIsComplete pins the terminal-event gate: only COMPLETE (and an empty
// eventType) is emitted, so a START+RUNNING+COMPLETE run is counted once.
func TestIsComplete(t *testing.T) {
	emit := map[string]bool{
		"COMPLETE": true, "complete": true, " COMPLETE ": true, "": true,
		"START": false, "RUNNING": false, "FAIL": false, "ABORT": false, "OTHER": false,
	}
	for in, want := range emit {
		if got := isComplete(in); got != want {
			t.Errorf("isComplete(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestJoinRef(t *testing.T) {
	cases := []struct {
		ns, name, want string
	}{
		{"airflow://etl", "job.task", "airflow://etl/job.task"},
		{"  airflow://etl  ", "  job.task  ", "airflow://etl/job.task"},
		{"", "job.task", "job.task"},
		{"airflow://etl", "", "airflow://etl"},
		{"", "", ""},
		{"   ", "   ", ""},
	}
	for _, c := range cases {
		if got := joinRef(c.ns, c.name); got != c.want {
			t.Errorf("joinRef(%q,%q) = %q, want %q", c.ns, c.name, got, c.want)
		}
	}
}

func TestParseTime(t *testing.T) {
	cases := []struct {
		in     string
		wantOK bool
		wantUT string // expected UTC RFC3339Nano, only checked when wantOK
	}{
		{"2026-06-03T10:23:47.123Z", true, "2026-06-03T10:23:47.123Z"},
		{"2020-12-09T23:37:31.081Z", true, "2020-12-09T23:37:31.081Z"},
		{"2026-06-03T12:08:00.001+10:00", true, "2026-06-03T02:08:00.001Z"},
		{"2026-06-03T10:23:47Z", true, "2026-06-03T10:23:47Z"},
		{"", false, ""},
		{"03/06/2026 10:23", false, ""},
	}
	for _, c := range cases {
		got, ok := parseTime(c.in)
		if ok != c.wantOK {
			t.Errorf("parseTime(%q) ok = %v, want %v", c.in, ok, c.wantOK)
			continue
		}
		if !ok {
			continue
		}
		if loc := got.Location(); loc != time.UTC {
			t.Errorf("parseTime(%q) location = %v, want UTC", c.in, loc)
		}
		want, _ := time.Parse(time.RFC3339Nano, c.wantUT)
		if !got.Equal(want) {
			t.Errorf("parseTime(%q) = %v, want %v", c.in, got, want.UTC())
		}
	}
}

// TestBuildEdgesSkips covers the non-emitting shapes: a non-terminal event, an
// unparseable timestamp, a missing job, and a complete event with no datasets.
func TestBuildEdgesSkips(t *testing.T) {
	s := New()
	cases := []struct {
		name string
		ev   runEvent
	}{
		{"start is skipped", runEvent{EventType: "START", EventTime: "2026-06-03T10:23:47.123Z", Job: job{Namespace: "ns", Name: "j"}, Inputs: []dataset{{Namespace: "n", Name: "d"}}}},
		{"running is skipped", runEvent{EventType: "RUNNING", EventTime: "2026-06-03T10:23:47.123Z", Job: job{Namespace: "ns", Name: "j"}, Outputs: []dataset{{Namespace: "n", Name: "d"}}}},
		{"unparseable time", runEvent{EventType: "COMPLETE", EventTime: "nope", Job: job{Namespace: "ns", Name: "j"}, Inputs: []dataset{{Namespace: "n", Name: "d"}}}},
		{"no job", runEvent{EventType: "COMPLETE", EventTime: "2026-06-03T10:23:47.123Z", Inputs: []dataset{{Namespace: "n", Name: "d"}}}},
		{"no datasets", runEvent{EventType: "COMPLETE", EventTime: "2026-06-03T10:23:47.123Z", Job: job{Namespace: "ns", Name: "j"}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if edges, ok := s.buildEdges(c.ev); ok || len(edges) != 0 {
				t.Errorf("buildEdges = %+v, ok=%v; want no edges", edges, ok)
			}
		})
	}
}
