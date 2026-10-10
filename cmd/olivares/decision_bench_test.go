// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"sort"
	"testing"
	"time"
)

func reportDecisionLatency(b *testing.B, lat []time.Duration, elapsed time.Duration) {
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
