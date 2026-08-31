// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package claudeadoption

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/sdk/event"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

const (
	// discrepancyRatioThreshold is deliberately code, not config: the detector is a
	// stable governance signal, not a tuneable alert threshold. A pair is material when
	// at least one side reaches its metric floor and the relative difference is >25%.
	discrepancyRatioThreshold = 0.25

	directionAligned                  = "aligned"
	directionTelemetryExceedsOfficial = "telemetry_exceeds_official"
	directionOfficialExceedsTelemetry = "official_exceeds_telemetry"
	directionOfficialPlaneAbsent      = "official_plane_absent"
	directionTelemetryPlaneAbsent     = "telemetry_plane_absent"

	discrepancyFindingKind  = "adoption_discrepancy"
	discrepancySubjectKind  = "claude_code_adoption"
	discrepancyBoundaryNote = "BOUNDARY: the official Analytics feed covers only Claude-API-served usage; a 3P-surface fleet on Claude Platform on AWS, Microsoft Foundry, Amazon Bedrock or Vertex AI can legitimately show telemetry-only usage. This finding signals divergence for review; it does not conclude misuse."
)

type discrepancySpec struct {
	name  string
	label string
	floor int64
}

var discrepancySpecs = []discrepancySpec{
	{name: metricSessionCount, label: "sessions", floor: 10},
	{name: metricTokenUsage, label: "tokens", floor: 100_000},
	{name: metricLinesOfCode, label: "lines added", floor: 500},
	{name: metricCommit, label: "commits", floor: 10},
	{name: metricPullRequest, label: "pull requests", floor: 5},
}

type discrepancyPlaneTotals map[string]map[string]int64

type discrepancyVerdictSummary struct {
	Day           string             `json:"day"`
	Material      bool               `json:"material"`
	MaterialCount int                `json:"material_count"`
	MetricCount   int                `json:"metric_count"`
	WorstMetric   string             `json:"worst_metric,omitempty"`
	WorstRatio    float64            `json:"worst_ratio,omitempty"`
	Severity      sdkmodel.Severity  `json:"severity,omitempty"`
	Directions    map[string]int     `json:"directions,omitempty"`
	Thresholds    discrepancySummary `json:"thresholds"`
}

type discrepancySummary struct {
	Ratio float64          `json:"ratio"`
	Floor map[string]int64 `json:"floor"`
}

// evaluatePreviousDiscrepancyDay is the deterministic ingest hook: an Analytics
// developer/day snapshot for D evaluates D-1, because both planes should then be complete.
// The extra work is guarded by one marker row per tenant/day and reads only this module's
// two adoption.metric lenses (developer and session) under bounded scans. A racing marker
// create means the other writer won; this ingest then skips the secondary check.
func (m *Module) evaluatePreviousDiscrepancyDay(ctx context.Context, tenant model.TenantID, currentDay time.Time) {
	day := previousDay(currentDay)
	evaluatedAt := model.NewTimestamp(time.Now()).String()
	var out discrepancyDay
	var severity sdkmodel.Severity
	var shouldPublish bool
	err := m.data.Mutate(ctx, tenant, func(sc store.Scope) error {
		markerRepo, err := sc.Ext(adoptionDiscrepancyCheckKind)
		if err != nil {
			return err
		}
		if _, err := markerRepo.Create(ctx, model.Record{
			colCheckDay:       day,
			colCheckEvaluated: evaluatedAt,
			colCheckVerdict:   "{}",
		}); err != nil {
			if errors.Is(err, store.ErrConflict) {
				return nil
			}
			return err
		}
		dayOut, trunc, err := compareDiscrepancyDay(ctx, sc, day)
		if err != nil {
			return err
		}
		out = dayOut
		severity = discrepancySeverity(dayOut)
		shouldPublish = dayOut.Material
		summary := markerSummary(dayOut, severity)
		if trunc {
			summary.Directions["truncated"] = 1
		}
		b, err := json.Marshal(summary)
		if err != nil {
			return err
		}
		rows, _, err := markerRepo.List(ctx, model.Query{Filters: []model.Filter{eq(colCheckDay, day)}, Limit: 1})
		if err != nil || len(rows) == 0 {
			return err
		}
		rows[0][colCheckVerdict] = string(b)
		_, err = markerRepo.Update(ctx, rows[0])
		return err
	})
	if err != nil {
		m.debugf("claudeadoption: discrepancy check failed", "tenant", tenant.String(), "day", day, "err", err)
		return
	}
	if shouldPublish {
		m.emitDiscrepancyFinding(ctx, tenant, out, severity, time.Now().UTC())
	}
}

func compareDiscrepancyDay(ctx context.Context, sc store.Scope, day string) (discrepancyDay, bool, error) {
	days, trunc, err := compareDiscrepancyWindow(ctx, sc, []model.Filter{eq(colDay, day)})
	if err != nil || len(days) == 0 {
		return discrepancyDay{Day: day, Metrics: buildDiscrepancyMetrics(day, nil, nil)}, trunc, err
	}
	return days[0], trunc, nil
}

func compareDiscrepancyWindow(ctx context.Context, sc store.Scope, filters []model.Filter) ([]discrepancyDay, bool, error) {
	analytics, truncA, err := aggregateDiscrepancyPlane(ctx, sc, appendSubject(filters, subjectDeveloper))
	if err != nil {
		return nil, false, err
	}
	telemetry, truncT, err := aggregateDiscrepancyPlane(ctx, sc, appendSubject(filters, subjectSession))
	if err != nil {
		return nil, false, err
	}
	daySet := map[string]struct{}{}
	for d := range analytics {
		daySet[d] = struct{}{}
	}
	for d := range telemetry {
		daySet[d] = struct{}{}
	}
	days := make([]string, 0, len(daySet))
	for d := range daySet {
		days = append(days, d)
	}
	sort.Strings(days)
	out := make([]discrepancyDay, 0, len(days))
	for _, day := range days {
		metrics := buildDiscrepancyMetrics(day, analytics[day], telemetry[day])
		if !hasDiscrepancyActivity(metrics) {
			continue
		}
		row := discrepancyDay{Day: day, Metrics: metrics}
		for _, metric := range metrics {
			if metric.Material {
				row.Material = true
				break
			}
		}
		out = append(out, row)
	}
	return out, truncA || truncT, nil
}

func aggregateDiscrepancyPlane(ctx context.Context, sc store.Scope, filters []model.Filter) (discrepancyPlaneTotals, bool, error) {
	out := discrepancyPlaneTotals{}
	trunc, err := scanAdoption(ctx, sc, filters, func(r model.Record) {
		name, ok := discrepancyMetricName(r)
		if !ok {
			return
		}
		day := r.String(colDay)
		if day == "" {
			return
		}
		byMetric := out[day]
		if byMetric == nil {
			byMetric = map[string]int64{}
			out[day] = byMetric
		}
		byMetric[name] += r.Int(colValue)
	})
	return out, trunc, err
}

func discrepancyMetricName(r model.Record) (string, bool) {
	name := r.String(colMetricName)
	switch name {
	case metricSessionCount, metricTokenUsage, metricCommit, metricPullRequest:
		return name, true
	case metricLinesOfCode:
		return name, r.String(colDimType) == typeAdded
	default:
		return "", false
	}
}

func buildDiscrepancyMetrics(_ string, analytics, telemetry map[string]int64) []discrepancyMetric {
	out := make([]discrepancyMetric, 0, len(discrepancySpecs))
	for _, spec := range discrepancySpecs {
		a := analytics[spec.name]
		t := telemetry[spec.name]
		out = append(out, compareDiscrepancyMetric(spec.name, spec.floor, a, t))
	}
	return out
}

func compareDiscrepancyMetric(name string, floor, analytics, telemetry int64) discrepancyMetric {
	row := discrepancyMetric{Name: name, Analytics: analytics, Telemetry: telemetry, Direction: directionAligned}
	if telemetry > analytics {
		row.Direction = directionTelemetryExceedsOfficial
	}
	if analytics > telemetry {
		row.Direction = directionOfficialExceedsTelemetry
	}
	if analytics == 0 && telemetry >= floor {
		row.Direction = directionOfficialPlaneAbsent
		row.Ratio = 1
		row.Material = true
		return row
	}
	if telemetry == 0 && analytics >= floor {
		row.Direction = directionTelemetryPlaneAbsent
		row.Ratio = 1
		row.Material = true
		return row
	}
	if max64(analytics, telemetry) < floor {
		return row
	}
	if analytics > 0 && telemetry > 0 {
		row.Ratio = math.Abs(float64(analytics-telemetry)) / float64(max64(analytics, telemetry))
		row.Material = row.Ratio > discrepancyRatioThreshold
	}
	return row
}

func hasDiscrepancyActivity(metrics []discrepancyMetric) bool {
	for _, metric := range metrics {
		if metric.Analytics != 0 || metric.Telemetry != 0 {
			return true
		}
	}
	return false
}

// discrepancySeverity keeps the finding grade intentionally simple: plane-absent is the
// strongest signal and is medium; telemetry materially exceeding official by >50% is also
// medium because it can mean Claude Code use invisible to the official org feed. Other
// material divergences remain low so the finding asks for review without over-claiming.
func discrepancySeverity(day discrepancyDay) sdkmodel.Severity {
	for _, metric := range day.Metrics {
		if !metric.Material {
			continue
		}
		if metric.Direction == directionOfficialPlaneAbsent || metric.Direction == directionTelemetryPlaneAbsent {
			return sdkmodel.SeverityMedium
		}
		if metric.Direction == directionTelemetryExceedsOfficial && metric.Ratio > 0.5 {
			return sdkmodel.SeverityMedium
		}
	}
	return sdkmodel.SeverityLow
}

func (m *Module) emitDiscrepancyFinding(ctx context.Context, tenant model.TenantID, day discrepancyDay, severity sdkmodel.Severity, at time.Time) {
	if m.host == nil {
		return
	}
	report := sdkmodel.FindingReport{
		Kind:        discrepancyFindingKind,
		Severity:    severity,
		SubjectKind: discrepancySubjectKind,
		SubjectRef:  day.Day,
		Title:       discrepancyTitle(day),
		DetailHash:  hashDiscrepancyDetail(day),
		OccurredAt:  at,
	}
	if err := m.host.Publish(ctx, event.FromObservation(tenant.String(), Name, report)); err != nil {
		m.debugf("claudeadoption: publish discrepancy finding failed", "day", day.Day, "err", err)
	}
}

func discrepancyTitle(day discrepancyDay) string {
	worst := worstDiscrepancyMetric(day)
	material := 0
	for _, metric := range day.Metrics {
		if metric.Material {
			material++
		}
	}
	label := metricLabel(worst.Name)
	return fmt.Sprintf("Claude Code planes diverge on %s: %d of %d shared metrics material (worst: %s %d vs %d)",
		day.Day, material, len(discrepancySpecs), label, worst.Analytics, worst.Telemetry)
}

func hashDiscrepancyDetail(day discrepancyDay) string {
	body := struct {
		Boundary   string              `json:"boundary"`
		Day        string              `json:"day"`
		Metrics    []discrepancyMetric `json:"metrics"`
		Thresholds discrepancySummary  `json:"thresholds"`
	}{
		Boundary:   discrepancyBoundaryNote,
		Day:        day.Day,
		Metrics:    day.Metrics,
		Thresholds: discrepancySummary{Ratio: discrepancyRatioThreshold, Floor: discrepancyFloorMap()},
	}
	b, _ := json.Marshal(body)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func markerSummary(day discrepancyDay, severity sdkmodel.Severity) discrepancyVerdictSummary {
	material := 0
	directions := map[string]int{}
	for _, metric := range day.Metrics {
		if metric.Material {
			material++
			directions[metric.Direction]++
		}
	}
	worst := worstDiscrepancyMetric(day)
	return discrepancyVerdictSummary{
		Day: day.Day, Material: day.Material, MaterialCount: material, MetricCount: len(discrepancySpecs),
		WorstMetric: worst.Name, WorstRatio: worst.Ratio, Severity: severity, Directions: directions,
		Thresholds: discrepancySummary{Ratio: discrepancyRatioThreshold, Floor: discrepancyFloorMap()},
	}
}

func worstDiscrepancyMetric(day discrepancyDay) discrepancyMetric {
	var worst discrepancyMetric
	for _, metric := range day.Metrics {
		if !metric.Material {
			continue
		}
		if worst.Name == "" || metric.Ratio > worst.Ratio ||
			(metric.Ratio == worst.Ratio && max64(metric.Analytics, metric.Telemetry) > max64(worst.Analytics, worst.Telemetry)) {
			worst = metric
		}
	}
	if worst.Name != "" {
		return worst
	}
	if len(day.Metrics) > 0 {
		return day.Metrics[0]
	}
	return discrepancyMetric{Name: metricSessionCount}
}

func discrepancyThresholdDTO() discrepancyThresholds {
	return discrepancyThresholds{Ratio: discrepancyRatioThreshold, Floors: discrepancyFloorMap()}
}

func discrepancyFloorMap() map[string]int64 {
	out := make(map[string]int64, len(discrepancySpecs))
	for _, spec := range discrepancySpecs {
		out[spec.name] = spec.floor
	}
	return out
}

func metricLabel(name string) string {
	for _, spec := range discrepancySpecs {
		if spec.name == name {
			return spec.label
		}
	}
	return name
}

func appendSubject(filters []model.Filter, subject string) []model.Filter {
	out := append([]model.Filter(nil), filters...)
	out = append(out, subjectFilter(subject))
	return out
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
