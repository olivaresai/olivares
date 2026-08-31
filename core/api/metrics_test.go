// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestObservabilityEndpoints verifies the OBS-06 surface: /livez, /readyz and the
// Prometheus /metrics scrape are reachable WITHOUT a bearer and BEFORE setup (the
// harness never runs setup here), and that /metrics exposes the real engine series
// in the pinned Prometheus 0.0.4 text format.
func TestObservabilityEndpoints(t *testing.T) {
	h := newHarness(t)

	if r := h.do("GET", "/livez", "", nil, nil); r.code != 200 {
		t.Fatalf("/livez = %d, want 200 (body %s)", r.code, r.raw)
	}

	// readyz pings the in-memory store, which is reachable -> 200.
	r := h.do("GET", "/readyz", "", nil, nil)
	if r.code != 200 {
		t.Fatalf("/readyz = %d, want 200 (body %s)", r.code, r.raw)
	}
	if r.body["store"] != "up" {
		t.Errorf("/readyz store = %v, want up", r.body["store"])
	}

	// Generate a request so the HTTP counters have a sample, then scrape.
	h.do("GET", "/healthz", "", nil, nil)

	m := h.do("GET", "/metrics", "", nil, nil)
	if m.code != 200 {
		t.Fatalf("/metrics = %d, want 200", m.code)
	}
	body := m.raw
	for _, want := range []string{
		`olivares_build_info{version="test"} 1`,
		"# TYPE olivares_http_requests_total counter",
		`olivares_http_requests_total{method="GET",code="200"}`,
		"# TYPE olivares_http_request_duration_seconds histogram",
		"olivares_http_request_duration_seconds_bucket{",
		"olivares_http_requests_in_flight",
		"olivares_store_up 1",
		"go_goroutines",
		"olivares_ingest_observations_total",
		"# TYPE olivares_ingest_duration_seconds histogram",
		"olivares_ingest_rejected_total",
		// SLIs: gRPC family declared (counts at first RPC) and the login
		// counter pre-created at zero for all three outcomes.
		"# TYPE olivares_grpc_requests_total counter",
		"# TYPE olivares_grpc_request_duration_seconds histogram",
		`olivares_auth_login_attempts_total{outcome="success"} 0`,
		`olivares_auth_login_attempts_total{outcome="failed"} 0`,
		`olivares_auth_login_attempts_total{outcome="locked_out"} 0`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("/metrics missing %q\n--- body ---\n%s", want, body)
		}
	}
	// Every exposed line must be a comment, a sample, or blank — a malformed line
	// would break a real Prometheus parse.
	for _, line := range strings.Split(strings.TrimSpace(body), "\n") {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if !strings.Contains(line, " ") {
			t.Errorf("malformed exposition line (no value): %q", line)
		}
	}
}

// TestMetricsContentType pins the scrape Content-Type to the Prometheus 0.0.4 text
// format header a scraper content-negotiates on.
func TestMetricsContentType(t *testing.T) {
	h := newHarness(t)
	rec := httptest.NewRecorder()
	h.srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if ct := rec.Header().Get("Content-Type"); ct != "text/plain; version=0.0.4; charset=utf-8" {
		t.Fatalf("Content-Type = %q", ct)
	}
}
