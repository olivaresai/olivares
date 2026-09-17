// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package trace

import "testing"

func TestHTTPSignalEndpointURL(t *testing.T) {
	cases := []struct{ in, traces, metrics string }{
		{"http://collector:4318", "http://collector:4318/v1/traces", "http://collector:4318/v1/metrics"},
		{"https://collector:4318/", "https://collector:4318/v1/traces", "https://collector:4318/v1/metrics"},
		{"http://[::1]:4318", "http://[::1]:4318/v1/traces", "http://[::1]:4318/v1/metrics"},
		{"https://collector:4318/?tenant=a#f", "https://collector:4318/v1/traces?tenant=a#f", "https://collector:4318/v1/metrics?tenant=a#f"},
		// An explicit non-root path is kept for both signals.
		{"http://collector:4318/custom", "http://collector:4318/custom", "http://collector:4318/custom"},
		{"http://collector:4318/custom/", "http://collector:4318/custom/", "http://collector:4318/custom/"},
		// Values that do not form a base URL are left to the exporter unchanged.
		{"http://%zz", "http://%zz", "http://%zz"},
		{"http://", "http://", "http://"},
	}
	for _, tc := range cases {
		if got := httpSignalEndpointURL(tc.in, otlpHTTPTracesPath); got != tc.traces {
			t.Errorf("traces URL for %q = %q, want %q", tc.in, got, tc.traces)
		}
		if got := httpSignalEndpointURL(tc.in, otlpHTTPMetricsPath); got != tc.metrics {
			t.Errorf("metrics URL for %q = %q, want %q", tc.in, got, tc.metrics)
		}
	}
}
