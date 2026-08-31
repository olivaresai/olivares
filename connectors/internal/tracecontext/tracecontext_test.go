// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package tracecontext_test

import (
	"testing"

	tc "github.com/olivaresai/olivares/connectors/internal/tracecontext"
)

const sampleTraceParent = "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"

func TestFromHeaderMapLowercase(t *testing.T) {
	got := tc.FromHeaderMap(map[string]string{
		"traceparent": sampleTraceParent,
		"tracestate":  "rojo=00f067aa0ba902b7",
		"baggage":     "tenant=acme",
	})
	if got.TraceParent != sampleTraceParent {
		t.Fatalf("TraceParent = %q", got.TraceParent)
	}
	if got.TraceState != "rojo=00f067aa0ba902b7" {
		t.Fatalf("TraceState = %q", got.TraceState)
	}
	if got.Baggage != "tenant=acme" {
		t.Fatalf("Baggage = %q", got.Baggage)
	}
	if !got.Present() {
		t.Fatal("Present() = false")
	}
}

func TestFromHeaderMapCaseInsensitive(t *testing.T) {
	got := tc.FromHeaderMap(map[string]string{"TraceParent": sampleTraceParent})
	if got.TraceParent != sampleTraceParent {
		t.Fatalf("mixed-case header not matched: %q", got.TraceParent)
	}
}

func TestFromHeaderMapEmpty(t *testing.T) {
	if got := tc.FromHeaderMap(nil); got.Present() {
		t.Fatal("nil header map should yield no trace context")
	}
	if got := tc.FromHeaderMap(map[string]string{"x": "y"}); got.Present() {
		t.Fatal("absent traceparent should yield no trace context")
	}
}

func TestFromGetter(t *testing.T) {
	if got := tc.FromGetter(nil); got.Present() {
		t.Fatal("nil getter should yield empty")
	}
	got := tc.FromGetter(func(name string) string {
		if name == tc.HeaderTraceParent {
			return sampleTraceParent
		}
		return ""
	})
	if got.TraceParent != sampleTraceParent || got.TraceState != "" {
		t.Fatalf("getter extraction wrong: %+v", got)
	}
}

func TestFromMeta(t *testing.T) {
	if got := tc.FromMeta(nil); got.Present() {
		t.Fatal("nil meta should yield empty")
	}
	got := tc.FromMeta(map[string]any{
		"traceparent": sampleTraceParent,
		"tracestate":  42, // non-string is ignored, never panics
	})
	if got.TraceParent != sampleTraceParent {
		t.Fatalf("meta traceparent = %q", got.TraceParent)
	}
	if got.TraceState != "" {
		t.Fatalf("non-string tracestate should be ignored, got %q", got.TraceState)
	}
}

func TestValidTraceParent(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want bool
	}{
		{"valid", sampleTraceParent, true},
		{"valid sampled flag", "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-00", true},
		{"all-zero trace-id", "00-00000000000000000000000000000000-00f067aa0ba902b7-01", false},
		{"all-zero parent-id", "00-4bf92f3577b34da6a3ce929d0e0e4736-0000000000000000-01", false},
		{"forbidden version ff", "ff-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01", false},
		{"unsupported version 01", "01-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01", false},
		{"short trace-id", "00-4bf92f3577-00f067aa0ba902b7-01", false},
		{"uppercase hex rejected", "00-4BF92F3577B34DA6A3CE929D0E0E4736-00f067aa0ba902b7-01", false},
		{"non-hex", "00-zzf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01", false},
		{"too few segments", "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7", false},
		{"empty", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := tc.ValidTraceParent(c.in); got != c.want {
				t.Fatalf("ValidTraceParent(%q) = %v, want %v", c.in, got, c.want)
			}
		})
	}
}

func TestNopCorrelatorIsInert(t *testing.T) {
	// Must not panic and must accept any input.
	tc.NopCorrelator{}.Correlate("k", tc.TraceContext{TraceParent: sampleTraceParent})
	var c tc.Correlator = tc.NopCorrelator{}
	c.Correlate("", tc.TraceContext{})
}
