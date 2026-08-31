// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package inferenceproxy

import (
	"sort"
	"testing"
	"time"
)

var benchmarkDLPDenied []string

// BenchmarkProxyPolicyDLPDecide measures decision-algebra only, not end-to-end p99.
func BenchmarkProxyPolicyDLPDecide(b *testing.B) {
	policy := defaultProxyPolicy()
	classes := []string{
		"pii.contact",
		"secret.credential",
		"unscanned",
		"pii.network",
		"pii.contact",
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		benchmarkDLPDenied = policy.DLPDecide(classes)
	}
}

// BenchmarkProxyDLPDecideLatency measures only the in-memory DLP algebra. For
// this pure function p95/p99 are expected to be approximately p50; their spread
// is timer, GC, and scheduler jitter. The flat distribution shows this algebra
// is not the decision-plane bottleneck; store-backed end-to-end p99 is measured
// separately.
func BenchmarkProxyDLPDecideLatency(b *testing.B) {
	policy := defaultProxyPolicy()
	classes := []string{
		"pii.contact",
		"secret.credential",
		"unscanned",
		"pii.network",
		"pii.contact",
	}
	if denied := policy.DLPDecide(classes); len(denied) == 0 {
		b.Fatal("warm-up DLP decision unexpectedly allowed every class")
	}

	lat := make([]time.Duration, b.N)
	start := time.Now()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		t0 := time.Now()
		benchmarkDLPDenied = policy.DLPDecide(classes)
		lat[i] = time.Since(t0)
	}
	b.StopTimer()
	reportProxyDecisionLatency(b, lat, time.Since(start))
}

func reportProxyDecisionLatency(b *testing.B, lat []time.Duration, elapsed time.Duration) {
	b.Helper()
	if len(lat) == 0 {
		return
	}
	sort.Slice(lat, func(i, j int) bool { return lat[i] < lat[j] })
	ms := func(d time.Duration) float64 { return float64(d.Microseconds()) / 1000.0 }
	q := func(p float64) time.Duration {
		idx := int(p*float64(len(lat)-1) + 0.5)
		return lat[idx]
	}
	b.ReportMetric(float64(len(lat))/elapsed.Seconds(), "decisions/sec")
	b.ReportMetric(ms(q(0.50)), "p50_ms")
	b.ReportMetric(ms(q(0.95)), "p95_ms")
	b.ReportMetric(ms(q(0.99)), "p99_ms")
	b.ReportMetric(ms(lat[len(lat)-1]), "max_ms")
}
