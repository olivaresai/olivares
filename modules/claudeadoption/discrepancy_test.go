// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package claudeadoption

import (
	"strings"
	"testing"

	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

// TestDiscrepancyEndpoint_Materiality proves the read endpoint recomputes daily
// official-vs-observed comparisons from the read-model, including both non-zero
// directions, both plane-absent variants, and the floor gate for small values.
func TestDiscrepancyEndpoint_Materiality(t *testing.T) {
	m, st, tenant := newModule(t)
	day1 := baseTime
	day2 := baseTime.AddDate(0, 0, 1)
	day3 := baseTime.AddDate(0, 0, 2)

	m.ingest(t, tenant, srcAnalytics, devMetric(metricSessionCount, "dev@a", 40, day1, nil))
	m.ingest(t, tenant, srcOTLP, sessMetric(metricSessionCount, "sess-a", 9, day1, nil, ""))
	m.ingest(t, tenant, srcAnalytics, devMetric(metricTokenUsage, "dev@a", 90_000, day1, nil))

	m.ingest(t, tenant, srcAnalytics, devMetric(metricCommit, "dev@b", 12, day2, nil))
	m.ingest(t, tenant, srcOTLP, sessMetric(metricCommit, "sess-b", 30, day2, nil, ""))
	m.ingest(t, tenant, srcOTLP, sessMetric(metricLinesOfCode, "sess-b", 600, day2, map[string]string{dimType: typeAdded}, ""))
	m.ingest(t, tenant, srcOTLP, sessMetric(metricLinesOfCode, "sess-b", 99, day2, map[string]string{dimType: typeRemoved}, ""))

	m.ingest(t, tenant, srcAnalytics, devMetric(metricPullRequest, "dev@c", 6, day3, nil))

	var out discrepancyResponse
	code := get(t, st, tenant, m.handleDiscrepancy,
		"since=2026-06-20T00:00:00Z&until=2026-06-22T23:59:59Z", &out)
	if code != 200 {
		t.Fatalf("discrepancy status %d", code)
	}
	if out.Thresholds.Ratio != discrepancyRatioThreshold || out.Thresholds.Floors[metricSessionCount] != 10 {
		t.Fatalf("thresholds = %+v", out.Thresholds)
	}
	if !out.Boundary.ClaudeAPIOnly {
		t.Fatalf("boundary must be present: %+v", out.Boundary)
	}
	if len(out.Days) != 3 {
		t.Fatalf("days = %+v, want 3 non-zero days", out.Days)
	}

	byDay := map[string]discrepancyDay{}
	for _, day := range out.Days {
		byDay[day.Day] = day
	}
	sessions := metricByName(byDay["2026-06-20"].Metrics, metricSessionCount)
	if !sessions.Material || sessions.Direction != directionOfficialExceedsTelemetry || sessions.Ratio < 0.77 || sessions.Ratio > 0.78 {
		t.Errorf("sessions comparison = %+v, want official_exceeds material ratio ~0.775", sessions)
	}
	tokens := metricByName(byDay["2026-06-20"].Metrics, metricTokenUsage)
	if tokens.Material || tokens.Ratio != 0 {
		t.Errorf("below-floor tokens must be non-material with ratio 0, got %+v", tokens)
	}
	commits := metricByName(byDay["2026-06-21"].Metrics, metricCommit)
	if !commits.Material || commits.Direction != directionTelemetryExceedsOfficial || commits.Ratio < 0.59 || commits.Ratio > 0.61 {
		t.Errorf("commits comparison = %+v, want telemetry_exceeds material ratio ~0.60", commits)
	}
	lines := metricByName(byDay["2026-06-21"].Metrics, metricLinesOfCode)
	if !lines.Material || lines.Direction != directionOfficialPlaneAbsent || lines.Analytics != 0 || lines.Telemetry != 600 {
		t.Errorf("added lines comparison = %+v, want official plane absent", lines)
	}
	prs := metricByName(byDay["2026-06-22"].Metrics, metricPullRequest)
	if !prs.Material || prs.Direction != directionTelemetryPlaneAbsent || prs.Analytics != 6 || prs.Telemetry != 0 {
		t.Errorf("PR comparison = %+v, want telemetry plane absent", prs)
	}
}

// TestDiscrepancyIngestTriggeredFinding proves an Analytics sample for day D evaluates
// D-1 exactly once, emits one hashed FindingReport for a material day, and uses the
// marker row to suppress duplicate publications from later same-day Analytics samples.
func TestDiscrepancyIngestTriggeredFinding(t *testing.T) {
	m, _, tenant := newModule(t)
	host := &fakeHost{}
	m.host = host
	m.log = host.Logger()

	prev := baseTime
	current := baseTime.AddDate(0, 0, 1)
	m.ingest(t, tenant, srcAnalytics, devMetric(metricSessionCount, "dev@a", 9, prev, nil))
	m.ingest(t, tenant, srcOTLP, sessMetric(metricSessionCount, "sess-a", 40, prev, nil, ""))

	m.ingest(t, tenant, srcAnalytics, devMetric(metricCommit, "trigger@corp.example", 1, current, nil))
	findings := host.findings()
	if len(findings) != 1 {
		t.Fatalf("findings = %+v, want one discrepancy finding", findings)
	}
	f := findings[0]
	if f.Kind != discrepancyFindingKind || f.Severity != sdkmodel.SeverityMedium {
		t.Fatalf("finding kind/severity = %s/%s", f.Kind, f.Severity)
	}
	if f.SubjectKind != discrepancySubjectKind || f.SubjectRef != "2026-06-20" {
		t.Fatalf("finding subject = %s/%s", f.SubjectKind, f.SubjectRef)
	}
	if !strings.Contains(f.Title, "1 of 5 shared metrics material") || !strings.Contains(f.Title, "sessions 9 vs 40") {
		t.Fatalf("finding title = %q", f.Title)
	}
	if f.DetailHash == "" {
		t.Fatal("finding detail hash must be populated")
	}

	m.ingest(t, tenant, srcAnalytics, devMetric(metricPullRequest, "trigger@corp.example", 1, current, nil))
	if findings := host.findings(); len(findings) != 1 {
		t.Fatalf("duplicate Analytics samples must not republish the day finding, got %+v", findings)
	}
}
