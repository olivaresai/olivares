// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package metrics

import (
	"io"
	"strings"
	"testing"
	"time"
)

func render(r *Registry) string {
	var sb strings.Builder
	r.WritePrometheus(&sb)
	return sb.String()
}

func mustContain(t *testing.T, out, want string) {
	t.Helper()
	if !strings.Contains(out, want) {
		t.Errorf("exposition missing %q\n--- full ---\n%s", want, out)
	}
}

func TestCounterAndLabels(t *testing.T) {
	r := New("v1.2.3", time.Unix(1_700_000_000, 0))
	c := r.CounterVec("olivares_http_requests_total", "Total requests.", "method", "code")
	c.Inc("GET", "200")
	c.Inc("GET", "200")
	c.Add(3, "POST", "500")

	out := render(r)
	mustContain(t, out, "# HELP olivares_http_requests_total Total requests.\n")
	mustContain(t, out, "# TYPE olivares_http_requests_total counter\n")
	mustContain(t, out, `olivares_http_requests_total{method="GET",code="200"} 2`)
	mustContain(t, out, `olivares_http_requests_total{method="POST",code="500"} 3`)
	// build_info carries the version label and is always present.
	mustContain(t, out, `olivares_build_info{version="v1.2.3"} 1`)
	// runtime collector is self-registered.
	mustContain(t, out, "# TYPE go_goroutines gauge\n")
}

func TestCounterIgnoresNegative(t *testing.T) {
	r := New("v", time.Unix(0, 0))
	c := r.Counter("olivares_x_total", "x")
	c.Add(-5)
	c.Add(2)
	mustContain(t, render(r), "olivares_x_total 2")
}

func TestGauge(t *testing.T) {
	r := New("v", time.Unix(0, 0))
	g := r.Gauge("olivares_inflight", "in flight")
	g.Inc()
	g.Inc()
	g.Dec()
	g.Add(0.5)
	out := render(r)
	mustContain(t, out, "# TYPE olivares_inflight gauge\n")
	mustContain(t, out, "olivares_inflight 1.5")
}

func TestHistogram(t *testing.T) {
	r := New("v", time.Unix(0, 0))
	h := r.HistogramVec("olivares_dur_seconds", "dur", []float64{0.1, 0.5, 1}, "method")
	h.Observe(0.05, "GET") // bucket 0.1
	h.Observe(0.3, "GET")  // bucket 0.5
	h.Observe(2.0, "GET")  // only +Inf
	out := render(r)
	mustContain(t, out, "# TYPE olivares_dur_seconds histogram\n")
	// Cumulative: le=0.1 -> 1, le=0.5 -> 2, le=1 -> 2, le=+Inf -> 3.
	mustContain(t, out, `olivares_dur_seconds_bucket{method="GET",le="0.1"} 1`)
	mustContain(t, out, `olivares_dur_seconds_bucket{method="GET",le="0.5"} 2`)
	mustContain(t, out, `olivares_dur_seconds_bucket{method="GET",le="1"} 2`)
	mustContain(t, out, `olivares_dur_seconds_bucket{method="GET",le="+Inf"} 3`)
	mustContain(t, out, `olivares_dur_seconds_count{method="GET"} 3`)
	// +Inf bucket value MUST equal _count (spec requirement).
	mustContain(t, out, `olivares_dur_seconds_sum{method="GET"} 2.35`)
}

func TestLabelValueEscaping(t *testing.T) {
	r := New("v", time.Unix(0, 0))
	c := r.CounterVec("olivares_evt_total", "e", "name")
	c.Inc("a\"b\\c\nd")
	mustContain(t, render(r), `olivares_evt_total{name="a\"b\\c\nd"} 1`)
}

func TestHelpEscapingDoesNotEscapeQuote(t *testing.T) {
	r := New("v", time.Unix(0, 0))
	r.Counter("olivares_q_total", `help with "quotes" and \ backslash`)
	out := render(r)
	// Quotes are NOT escaped in HELP; backslash IS.
	mustContain(t, out, `# HELP olivares_q_total help with "quotes" and \\ backslash`)
}

func TestFuncCollectorAndDeterministicOrder(t *testing.T) {
	r := New("v", time.Unix(0, 0))
	r.RegisterFunc("olivares_store_up", func(w io.Writer) {
		_, _ = w.Write([]byte("# HELP olivares_store_up up\n# TYPE olivares_store_up gauge\nolivares_store_up 1\n"))
	})
	r.Counter("zzz_total", "z").Inc()
	r.Counter("aaa_total", "a").Inc()
	out := render(r)
	// Families must be in sorted name order: aaa before zzz.
	ai := strings.Index(out, "aaa_total")
	zi := strings.Index(out, "zzz_total")
	if ai < 0 || zi < 0 || ai > zi {
		t.Errorf("families not in sorted order: aaa@%d zzz@%d", ai, zi)
	}
	mustContain(t, out, "olivares_store_up 1")
}

func TestContentType(t *testing.T) {
	if ContentType != "text/plain; version=0.0.4; charset=utf-8" {
		t.Fatalf("ContentType = %q", ContentType)
	}
}

func TestFormatFloat(t *testing.T) {
	cases := map[float64]string{
		2:      "2",
		2.5:    "2.5",
		0:      "0",
		1500:   "1500",
		0.0001: "0.0001",
	}
	for in, want := range cases {
		if got := formatFloat(in); got != want {
			t.Errorf("formatFloat(%v) = %q, want %q", in, got, want)
		}
	}
}
