// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package claudeadoption

// The Claude Code adoption metric NAMES — the consumer side of the wire contract the two
// Claude connectors emit MetricSamples under (connectors/claude/events.go and
// connectors/claude-api/productivity.go). They MUST stay byte-identical across the three:
// this module folds the OTLP plane and the admin Analytics feed into ONE read-model keyed
// by metric name. Source: code.claude.com/docs/en/monitoring-usage (Metrics).
const (
	metricSessionCount     = "claude_code.session.count"
	metricLinesOfCode      = "claude_code.lines_of_code.count"
	metricCommit           = "claude_code.commit.count"
	metricPullRequest      = "claude_code.pull_request.count"
	metricTokenUsage       = "claude_code.token.usage"
	metricCodeEditDecision = "claude_code.code_edit_tool.decision"
	metricActiveTime       = "claude_code.active_time.total"
	// metricActiveUsers is the org-level OFFICIAL active-user series emitted by the
	// claude-api connector's Enterprise Analytics ingest. It is subject_kind
	// "organization", carries dims window=daily|weekly|monthly and
	// plane=official_enterprise, folds as a snapshot/max, and is NEVER part of the
	// developer/session lenses or discrepancy comparison: its unit is users, not
	// activity volume.
	metricActiveUsers = "claude_code.active_users"
)

// adoptionMetricNames is the recognized set: a MetricSample whose name is outside it is
// not an adoption signal (e.g. a future MetricSample from another producer) and is
// ignored — the module never persists a measure it cannot interpret.
var adoptionMetricNames = map[string]struct{}{
	metricSessionCount: {}, metricLinesOfCode: {}, metricCommit: {}, metricPullRequest: {},
	metricTokenUsage: {}, metricCodeEditDecision: {}, metricActiveTime: {}, metricActiveUsers: {},
}

func isAdoptionMetricName(name string) bool {
	_, ok := adoptionMetricNames[name]
	return ok
}

// Dimension keys (the breakdown axes the producers tag, mirrored here).
const (
	dimType     = "type"
	dimTool     = "tool"
	dimDecision = "decision"
	dimModel    = "model"
)

// Dimension VALUES the aggregations special-case.
const (
	typeAdded   = "added"
	typeRemoved = "removed"
	decAccept   = "accept"
	decReject   = "reject"
)

// Subject kinds (mirrors the producers): a developer (email — the Analytics ROI subject,
// gated at read) or a session (the OTLP operational unit, never the developer email).
const (
	subjectDeveloper = "developer"
	subjectSession   = "session"
)

// labelTeam is the operator label promoted to the team dimension (OTEL_RESOURCE_ATTRIBUTES).
const labelTeam = "team"
