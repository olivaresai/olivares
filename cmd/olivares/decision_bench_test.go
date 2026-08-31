// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"sort"
	"testing"
	"time"

	"github.com/olivaresai/olivares/connectors/claude"
)

var (
	benchmarkHookDisposition hookDisposition
	benchmarkHookMatched     bool
)

// BenchmarkHookDecideAlgebra measures only the in-memory policy algebra. For
// this pure function p95/p99 are expected to be approximately p50; their spread
// is timer, GC, and scheduler jitter. That honest flat result demonstrates that
// the in-memory decision plane is not the bottleneck; store-backed end-to-end
// p99 is measured separately.
func BenchmarkHookDecideAlgebra(b *testing.B) {
	pol := hookPolicyDoc{
		Default:        claude.DecisionAllow,
		PathPrecedence: "deny-overrides",
		Rules: []hookPolicyRule{
			{Tool: "Read", ResourceKind: hookResourceKindFile, Paths: []string{"/srv/acme/**"}, Decision: claude.DecisionAllow},
			{Tool: "Read", ResourceKind: hookResourceKindFile, Subtree: "/srv/acme/Finance", Decision: claude.DecisionDeny},
			{Tool: "Write", ResourceKind: hookResourceKindFile, Paths: []string{"/srv/acme/Finance/**"}, Decision: claude.DecisionDeny},
		},
	}
	in := claude.HookDecisionInput{
		Event:        "PreToolUse",
		Tool:         "Read",
		ResourceKind: hookResourceKindFile,
		ResourceRef:  "/srv/acme/Finance/q3.xlsx",
		Mode:         "read",
	}

	disp, matched := evalHookPolicy(pol, in)
	if !matched || disp.decision != claude.DecisionDeny {
		b.Fatalf("unexpected warm-up decision: matched=%v disp=%+v", matched, disp)
	}

	lat := make([]time.Duration, b.N)
	start := time.Now()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		t0 := time.Now()
		benchmarkHookDisposition, benchmarkHookMatched = evalHookPolicy(pol, in)
		lat[i] = time.Since(t0)
	}
	b.StopTimer()
	reportDecisionLatency(b, lat, time.Since(start))
}

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
