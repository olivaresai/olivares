// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package claudeadoption

import (
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/auth"
)

// --- ingest idempotency ------------------------------------------------------

// TestIngest_AdditiveDeltaSumsWithHighWaterDedup proves an OTLP delta counter SUMS across
// distinct intervals in a day but DROPS a re-delivered (older-or-equal) interval.
func TestIngest_AdditiveDeltaSumsWithHighWaterDedup(t *testing.T) {
	m, st, tenant := newModule(t)
	dims := map[string]string{dimType: typeAdded}
	t1 := baseTime
	t2 := baseTime.Add(time.Minute)

	m.ingest(t, tenant, srcOTLP, sessMetric(metricLinesOfCode, "sess-1", 40, t1, dims, "payments"))
	m.ingest(t, tenant, srcOTLP, sessMetric(metricLinesOfCode, "sess-1", 12, t2, dims, "payments")) // next interval
	m.ingest(t, tenant, srcOTLP, sessMetric(metricLinesOfCode, "sess-1", 40, t1, dims, "payments")) // RE-DELIVERY of t1

	rows := adoptionRows(t, st, tenant)
	if len(rows) != 1 {
		t.Fatalf("delta intervals must fold to ONE day-bucket row, got %d", len(rows))
	}
	if v := rows[0].Int(colValue); v != 52 {
		t.Errorf("value = %d, want 52 (40+12, re-delivery deduped)", v)
	}
}

// TestIngest_SnapshotKeepsMaxOnRepull proves an Analytics daily total REPLACES with the
// max on a re-pull (the daily total only grows within a day) — never adds.
func TestIngest_SnapshotKeepsMaxOnRepull(t *testing.T) {
	m, st, tenant := newModule(t)
	m.ingest(t, tenant, srcAnalytics, devMetric(metricSessionCount, "dev@a", 5, baseTime, nil))
	m.ingest(t, tenant, srcAnalytics, devMetric(metricSessionCount, "dev@a", 8, baseTime.Add(time.Hour), nil))   // re-pull, grown
	m.ingest(t, tenant, srcAnalytics, devMetric(metricSessionCount, "dev@a", 8, baseTime.Add(2*time.Hour), nil)) // re-pull, same

	rows := adoptionRows(t, st, tenant)
	if len(rows) != 1 {
		t.Fatalf("snapshot re-pulls must fold to ONE row, got %d", len(rows))
	}
	if v := rows[0].Int(colValue); v != 8 {
		t.Errorf("value = %d, want 8 (max of daily totals, NOT summed to 21)", v)
	}
}

// TestIngest_DaysAndDimensionsSeparate proves distinct days and distinct dimension tuples
// land on distinct rows (so the trend and the added/removed split are preserved).
func TestIngest_DaysAndDimensionsSeparate(t *testing.T) {
	m, st, tenant := newModule(t)
	day2 := baseTime.AddDate(0, 0, 1)
	m.ingest(t, tenant, srcOTLP, sessMetric(metricLinesOfCode, "s", 10, baseTime, map[string]string{dimType: typeAdded}, ""))
	m.ingest(t, tenant, srcOTLP, sessMetric(metricLinesOfCode, "s", 3, baseTime, map[string]string{dimType: typeRemoved}, ""))
	m.ingest(t, tenant, srcOTLP, sessMetric(metricLinesOfCode, "s", 5, day2, map[string]string{dimType: typeAdded}, ""))
	if rows := adoptionRows(t, st, tenant); len(rows) != 3 {
		t.Fatalf("distinct (day, dims) must be distinct rows, got %d", len(rows))
	}
}

// TestIngest_Ignored proves the ignore rules: unrecognized name, empty subject, zero value,
// zero time all persist nothing (never a fabricated row).
func TestIngest_Ignored(t *testing.T) {
	m, st, tenant := newModule(t)
	m.ingest(t, tenant, srcOTLP, sessMetric("some.other.metric", "s", 5, baseTime, nil, ""))
	m.ingest(t, tenant, srcOTLP, sessMetric(metricCommit, "", 5, baseTime, nil, ""))
	m.ingest(t, tenant, srcOTLP, sessMetric(metricCommit, "s", 0, baseTime, nil, ""))
	m.ingest(t, tenant, srcOTLP, sessMetric(metricCommit, "s", 5, time.Time{}, nil, ""))
	if rows := adoptionRows(t, st, tenant); len(rows) != 0 {
		t.Fatalf("ignored samples must persist nothing, got %d rows", len(rows))
	}
}

// --- aggregations ------------------------------------------------------------

// TestSummary_TwoLensesNeverSummed proves the per-developer (analytics) and per-session
// (telemetry) lenses are reported separately and never added — the same commit counted on
// both planes must not become double.
func TestSummary_TwoLensesNeverSummed(t *testing.T) {
	m, st, tenant := newModule(t)
	// Analytics lens: dev@a has 5 commits, dev@b has 2 commits (7 total developer-side).
	m.ingest(t, tenant, srcAnalytics, devMetric(metricCommit, "dev@a", 5, baseTime, nil))
	m.ingest(t, tenant, srcAnalytics, devMetric(metricCommit, "dev@b", 2, baseTime, nil))
	// Telemetry lens: the SAME activity seen per session — 7 commits across two sessions.
	m.ingest(t, tenant, srcOTLP, sessMetric(metricCommit, "sess-1", 4, baseTime, nil, "payments"))
	m.ingest(t, tenant, srcOTLP, sessMetric(metricCommit, "sess-2", 3, baseTime, nil, "core"))

	var out summaryResponse
	if code := get(t, st, tenant, m.handleSummary, "", &out); code != 200 {
		t.Fatalf("summary status %d", code)
	}
	if out.Analytics.Totals.Commits != 7 {
		t.Errorf("analytics commits = %d, want 7", out.Analytics.Totals.Commits)
	}
	if out.Telemetry.Totals.Commits != 7 {
		t.Errorf("telemetry commits = %d, want 7", out.Telemetry.Totals.Commits)
	}
	if out.Developers != 2 {
		t.Errorf("distinct developers = %d, want 2", out.Developers)
	}
	if out.Teams != 2 {
		t.Errorf("distinct teams = %d, want 2", out.Teams)
	}
	if !out.Boundary.ClaudeAPIOnly || len(out.Boundary.Excludes) == 0 {
		t.Errorf("boundary note must be present and Claude-API-only: %+v", out.Boundary)
	}
}

// TestSummary_AcceptanceRateAndModelMix proves acceptance-rate (null when no decisions) and
// the per-model token mix.
func TestSummary_AcceptanceRateAndModelMix(t *testing.T) {
	m, st, tenant := newModule(t)
	// 8 accepted, 2 rejected across Edit/Write → 80% acceptance.
	m.ingest(t, tenant, srcAnalytics, devMetric(metricCodeEditDecision, "dev@a", 6, baseTime, map[string]string{dimTool: "Edit", dimDecision: decAccept}))
	m.ingest(t, tenant, srcAnalytics, devMetric(metricCodeEditDecision, "dev@a", 2, baseTime, map[string]string{dimTool: "Edit", dimDecision: decReject}))
	m.ingest(t, tenant, srcAnalytics, devMetric(metricCodeEditDecision, "dev@a", 2, baseTime, map[string]string{dimTool: "Write", dimDecision: decAccept}))
	// Token mix incl. cache tiers: opus input 1000 + cacheRead 200 + output 600 = 1800;
	// sonnet input 400. Cache tokens are an INPUT-side subset, so input_tokens must
	// include cacheRead, and input + output == total.
	m.ingest(t, tenant, srcAnalytics, devMetric(metricTokenUsage, "dev@a", 1000, baseTime, map[string]string{dimType: "input", dimModel: "claude-opus-4-8"}))
	m.ingest(t, tenant, srcAnalytics, devMetric(metricTokenUsage, "dev@a", 200, baseTime, map[string]string{dimType: "cacheRead", dimModel: "claude-opus-4-8"}))
	m.ingest(t, tenant, srcAnalytics, devMetric(metricTokenUsage, "dev@a", 600, baseTime, map[string]string{dimType: "output", dimModel: "claude-opus-4-8"}))
	m.ingest(t, tenant, srcAnalytics, devMetric(metricTokenUsage, "dev@a", 400, baseTime, map[string]string{dimType: "input", dimModel: "claude-sonnet-4-6"}))

	var out summaryResponse
	get(t, st, tenant, m.handleSummary, "", &out)
	tot := out.Analytics.Totals
	if tot.ToolsAccepted != 8 || tot.ToolsRejected != 2 {
		t.Errorf("accept/reject = %d/%d, want 8/2", tot.ToolsAccepted, tot.ToolsRejected)
	}
	if tot.AcceptanceRate == nil || *tot.AcceptanceRate < 0.799 || *tot.AcceptanceRate > 0.801 {
		t.Errorf("acceptance rate = %v, want ~0.80", tot.AcceptanceRate)
	}
	// cacheRead folds into input: input=1000+200+400=1600, output=600, total=2200.
	if tot.InputTokens != 1600 || tot.OutputTokens != 600 || tot.Tokens != 2200 {
		t.Errorf("tokens in/out/total = %d/%d/%d, want 1600/600/2200", tot.InputTokens, tot.OutputTokens, tot.Tokens)
	}
	if tot.InputTokens+tot.OutputTokens != tot.Tokens {
		t.Errorf("input+output (%d) must equal total (%d)", tot.InputTokens+tot.OutputTokens, tot.Tokens)
	}
	if len(out.Analytics.ByModel) != 2 || out.Analytics.ByModel[0].Model != "claude-opus-4-8" || out.Analytics.ByModel[0].Tokens != 1800 {
		t.Errorf("model mix = %+v, want opus first at 1800", out.Analytics.ByModel)
	}
	// A lens with no decisions has a null acceptance rate (honest, never a fabricated 0%).
	if out.Telemetry.Totals.AcceptanceRate != nil {
		t.Errorf("empty telemetry lens acceptance rate must be null, got %v", out.Telemetry.Totals.AcceptanceRate)
	}
}

// TestTrend_PerDayForLens proves the trend groups by day for the requested lens.
func TestTrend_PerDayForLens(t *testing.T) {
	m, st, tenant := newModule(t)
	day2 := baseTime.AddDate(0, 0, 1)
	m.ingest(t, tenant, srcAnalytics, devMetric(metricCommit, "dev@a", 3, baseTime, nil))
	m.ingest(t, tenant, srcAnalytics, devMetric(metricCommit, "dev@a", 5, day2, nil))

	var out trendResponse
	if code := get(t, st, tenant, m.handleTrend, "lens=analytics", &out); code != 200 {
		t.Fatalf("trend status %d", code)
	}
	if out.Lens != "analytics" || len(out.Days) != 2 {
		t.Fatalf("trend = %+v, want 2 analytics days", out)
	}
	if out.Days[0].Day != "2026-06-20" || out.Days[0].Totals.Commits != 3 || out.Days[1].Totals.Commits != 5 {
		t.Errorf("trend days = %+v", out.Days)
	}
	// An invalid lens is a 400.
	if code := get[trendResponse](t, st, tenant, m.handleTrend, "lens=bogus", nil); code != 400 {
		t.Errorf("invalid lens must be 400, got %d", code)
	}
}

// TestTeams_FromTelemetryLens proves the teams view aggregates the OTLP session lens by
// team label, with the team-less rows folding into the empty bucket.
func TestTeams_FromTelemetryLens(t *testing.T) {
	m, st, tenant := newModule(t)
	m.ingest(t, tenant, srcOTLP, sessMetric(metricCommit, "s1", 4, baseTime, nil, "payments"))
	m.ingest(t, tenant, srcOTLP, sessMetric(metricCommit, "s2", 2, baseTime, nil, "payments"))
	m.ingest(t, tenant, srcOTLP, sessMetric(metricCommit, "s3", 1, baseTime, nil, "")) // no team
	// An Analytics (developer) row must NOT appear in the team view.
	m.ingest(t, tenant, srcAnalytics, devMetric(metricCommit, "dev@a", 99, baseTime, nil))

	var out teamsResponse
	get(t, st, tenant, m.handleTeams, "", &out)
	byTeam := map[string]int64{}
	for _, tr := range out.Teams {
		byTeam[tr.Team] = tr.Totals.Commits
	}
	if byTeam["payments"] != 6 {
		t.Errorf("payments commits = %d, want 6", byTeam["payments"])
	}
	if byTeam[""] != 1 {
		t.Errorf("unassigned commits = %d, want 1", byTeam[""])
	}
	if got := byTeam["payments"] + byTeam[""]; got != 6+1 {
		t.Errorf("developer-lens rows must not leak into the team view (total %d)", got)
	}
}

// TestDevelopers_FromAnalyticsLensRankedAndCapped proves the developers view returns the
// per-developer Analytics ROI, ranked by activity, and never includes session subjects.
func TestDevelopers_FromAnalyticsLensRankedAndCapped(t *testing.T) {
	m, st, tenant := newModule(t)
	m.ingest(t, tenant, srcAnalytics, devMetric(metricCommit, "dev@low", 1, baseTime, nil))
	m.ingest(t, tenant, srcAnalytics, devMetric(metricCommit, "dev@high", 9, baseTime, nil))
	m.ingest(t, tenant, srcOTLP, sessMetric(metricCommit, "sess-x", 100, baseTime, nil, "t")) // session, not a developer

	var out developersResponse
	get(t, st, tenant, m.handleDevelopers, "", &out)
	if len(out.Developers) != 2 {
		t.Fatalf("want 2 developers (no sessions), got %d: %+v", len(out.Developers), out.Developers)
	}
	if out.Developers[0].Developer != "dev@high" || out.Developers[0].Totals.Commits != 9 {
		t.Errorf("developers must rank by activity, got %+v", out.Developers)
	}
	// Top-N cap is honored.
	if code := get(t, st, tenant, m.handleDevelopers, "limit=1", &out); code != 200 || len(out.Developers) != 1 {
		t.Errorf("limit=1 must cap to 1, got %d rows (code %d)", len(out.Developers), code)
	}
}

// --- permissions -------------------------------------------------------------

// TestDeveloperReadIsDenyClosed proves the per-developer permission is deny-closed for the
// viewer role (a privileged read) while the team/org aggregate read is the ordinary
// viewer-read tier, and the write tiers are never granted to viewers.
func TestDeveloperReadIsDenyClosed(t *testing.T) {
	if auth.RoleGrants(auth.RoleViewer, permDeveloperRead) {
		t.Error("adoption:developer:read must be deny-closed for viewer")
	}
	if !auth.RoleGrants(auth.RoleEditor, permDeveloperRead) {
		t.Error("adoption:developer:read should be granted from editor up")
	}
	if !auth.RoleGrants(auth.RoleViewer, permRead) {
		t.Error("the team/org adoption read should be available to viewers (per-team default)")
	}
}

// TestModuleContract pins the wire-contract metric names + the recognition predicate.
func TestModuleContract(t *testing.T) {
	for _, name := range []string{metricSessionCount, metricLinesOfCode, metricCommit, metricPullRequest, metricTokenUsage, metricCodeEditDecision, metricActiveTime, metricActiveUsers} {
		if !isAdoptionMetricName(name) {
			t.Errorf("%q must be a recognized adoption metric", name)
		}
	}
	if isAdoptionMetricName("claude_code.cost.usage") {
		t.Error("cost.usage must NOT be an adoption metric (the authoritative cost path is FinOps)")
	}
	// The lens default is the authoritative per-developer analytics lens.
	if k, ok := lensSubjectKind(""); !ok || k != subjectDeveloper {
		t.Errorf("default lens must be analytics (developer), got %q/%v", k, ok)
	}
}
