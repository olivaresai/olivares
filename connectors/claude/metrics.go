// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package claude

import (
	"time"

	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"
)

// This file maps the VALUES of the Claude Code productivity/adoption metrics.
// The OTLP receiver already recognized the 8 documented metrics by name for liveness +
// identity attribution (otlp.go), but DISCARDED their values — the gap #12 this closes.
// The values are now lifted into MetricSamples (claude.go: metricValueRouter) so the
// adoption module can persist an internal design note (not shipped) accept-reject/
// per-model tokens by session/team/day. It is structural by construction: counts and
// dimension labels, never prompt text, tool bodies, cost, or the developer email Claude
// Code exports on OAuth (OBS-10; the OTLP subject is the SESSION, not the developer).

// subjectSession is the SubjectKind of an OTLP-sourced adoption MetricSample: the
// Claude Code session the datapoint is attributed to. The OTLP plane never reads the
// developer email (minimal-data), so the session — the operational unit the connector
// already attributes to (OBS-09) — is the subject; per-developer ROI rides the admin
// Analytics feed (connectors/claude-api), the accepted attribution exception. The
// operator's allowlisted team/project labels ride the sample's Labels.
const subjectSession = "session"

// claudeMetric is the connector's normalized view of ONE Claude Code OTLP metric
// datapoint whose VALUE is persisted: the metric name, its integer value, the per-
// datapoint breakdown dimensions, the session it is attributed to, the operator labels,
// the delta/snapshot semantics, and the datapoint instant.
type claudeMetric struct {
	name     string
	value    int64
	unit     string
	additive bool // a delta counter (sum per day) vs a snapshot (latest/max)
	session  string
	dims     map[string]string
	labels   map[string]string
	at       time.Time
}

// isAdoptionMetric reports whether a metric name is one whose VALUE the connector
// persists as an adoption signal: one of the 8 documented claude_code.* metrics EXCEPT
// cost.usage, which is deliberately excluded — the authoritative cost path is the
// api_request stream, so persisting cost.usage here too would double-count it
// (the separation otlp.go documents). token.usage IS persisted (model-mix is an
// adoption signal, not a cost — the cost figure never rides it here).
func isAdoptionMetric(name string) bool {
	if name == metricCostUsage {
		return false
	}
	return IsClaudeCodeMetric(name)
}

// adoptionMetricUnit returns the display unit for a persisted adoption metric, or "".
func adoptionMetricUnit(name string) string {
	switch name {
	case metricSessionCount:
		return "sessions"
	case metricLinesOfCode:
		return "lines"
	case metricCommit:
		return "commits"
	case metricPullRequest:
		return "pull_requests"
	case metricTokenUsage:
		return "tokens"
	case metricCodeEditDecision:
		return "decisions"
	case metricActiveTime:
		return "ms"
	}
	return ""
}

// adoptionMetricDimensions extracts the per-datapoint breakdown axes a given metric
// carries — and ONLY those: the high-cardinality dimensions some metrics also carry
// (query_source/effort/agent.name/skill.name/plugin.name/mcp_*) are deliberately NOT
// persisted (minimal-data; they explode the adoption read-model without serving the
// dashboard). Returns nil when the metric has no relevant breakdown.
func adoptionMetricDimensions(name string, a attrs) map[string]string {
	out := map[string]string{}
	put := func(key, attr string) {
		if v := a.str(attr); v != "" {
			out[key] = v
		}
	}
	// The persisted dimension set is deliberately ALIGNED with the Analytics feed
	// (connectors/claude-api/productivity.go) so both sources fold into one uniform
	// read-model, and kept minimal/bounded: the per-edit `language` and the session
	// `start_type` axes Claude Code also carries are NOT persisted (high cardinality on
	// the busiest metric / not on the Analytics feed), and `model` rides token.usage
	// (the model-mix signal) but not lines_of_code (which Analytics reports model-less).
	switch name {
	case metricLinesOfCode:
		put("type", attrType) // added | removed
	case metricCodeEditDecision:
		put("tool", attrToolName)     // Edit | Write | NotebookEdit
		put("decision", attrDecision) // accept | reject
	case metricTokenUsage:
		put("type", attrType) // input | output | cacheRead | cacheCreation
		put("model", attrModel)
	case metricActiveTime:
		put("type", attrType) // user | cli
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// numberDataPoints returns the metric's NumberDataPoints for the two aggregations the
// claude_code.* metrics use — Sum (every documented counter) and Gauge (defensive).
// Histogram/Summary carry no single scalar value and are skipped.
func numberDataPoints(m *metricspb.Metric) []*metricspb.NumberDataPoint {
	switch d := m.GetData().(type) {
	case *metricspb.Metric_Sum:
		return d.Sum.GetDataPoints()
	case *metricspb.Metric_Gauge:
		return d.Gauge.GetDataPoints()
	default:
		return nil
	}
}

// metricIsAdditive reports whether a metric's datapoints are DELTA increments (each one
// added into the day bucket) vs a snapshot/level. Claude Code exports its counters with
// DELTA temporality by default (VERIFIED 2026-06-20, monitoring-usage docs); an operator
// who set OTEL_EXPORTER_OTLP_METRICS_TEMPORALITY_PREFERENCE=cumulative, or a Gauge, is a
// snapshot the consumer keeps as the latest/max — never silently summed into a runaway.
func metricIsAdditive(m *metricspb.Metric) bool {
	if s, ok := m.GetData().(*metricspb.Metric_Sum); ok {
		return s.Sum.GetAggregationTemporality() == metricspb.AggregationTemporality_AGGREGATION_TEMPORALITY_DELTA
	}
	return false
}

// datapointValue returns a datapoint's value as the metric's natural INTEGER unit, and
// false if the datapoint carries no recognized value. active_time.total is a seconds
// DOUBLE — converted to integer MILLISECONDS so the plane keeps integer measures
// (sub-second precision is immaterial for a daily adoption view, and ms sum losslessly).
// The other metrics are integer counters; a value that arrives as a double (some
// exporters emit int counters that way) is rounded.
func datapointValue(name string, dp *metricspb.NumberDataPoint) (int64, bool) {
	switch dp.GetValue().(type) {
	case *metricspb.NumberDataPoint_AsInt:
		if name == metricActiveTime {
			return dp.GetAsInt() * 1000, true // seconds(int) -> ms
		}
		return dp.GetAsInt(), true
	case *metricspb.NumberDataPoint_AsDouble:
		f := dp.GetAsDouble()
		if name == metricActiveTime {
			return roundToInt(f * 1000), true // seconds(double) -> ms
		}
		return roundToInt(f), true
	default:
		return 0, false
	}
}

// roundToInt rounds a non-negative measure to the nearest int64 (the adoption metrics
// are counts/durations, never negative).
func roundToInt(f float64) int64 {
	if f <= 0 {
		return 0
	}
	return int64(f + 0.5)
}

// dpTime returns the datapoint's instant (TimeUnixNano) as UTC, falling back to the
// receiver clock when the producer left it zero.
func dpTime(dp *metricspb.NumberDataPoint, fallback time.Time) time.Time {
	if t := dp.GetTimeUnixNano(); t != 0 {
		return time.Unix(0, int64(t)).UTC()
	}
	return fallback
}

// ingestMetricValues walks a resource-metrics batch and feeds each recognized adoption
// metric datapoint's VALUE to onMetric. Per-datapoint attributes are merged over
// the resource attributes (record attrs win), so the breakdown dimensions and the
// operator labels reflect both. It is a no-op when onMetric is nil (the flag is off).
func (r *receiver) ingestMetricValues(rm *metricspb.ResourceMetrics, id claudeIdentity, resAttrs []*commonpb.KeyValue) {
	for _, sm := range rm.GetScopeMetrics() {
		for _, m := range sm.GetMetrics() {
			name := m.GetName()
			if !isAdoptionMetric(name) {
				continue
			}
			additive := metricIsAdditive(m)
			unit := adoptionMetricUnit(name)
			for _, dp := range numberDataPoints(m) {
				val, ok := datapointValue(name, dp)
				if !ok {
					continue
				}
				merged := newAttrs(resAttrs, dp.GetAttributes())
				r.onMetric(claudeMetric{
					name:     name,
					value:    val,
					unit:     unit,
					additive: additive,
					session:  id.sessionID,
					dims:     adoptionMetricDimensions(name, merged),
					labels:   labelsFromAttrs(merged, r.labelKeys),
					at:       dpTime(dp, r.now()),
				})
			}
		}
	}
}
