// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// This file maps the PRODUCTIVITY side of the Claude Code Analytics feed (family
// #2 depth). The connector already emits the COST side (per-model estimated_cost as
// CostSamples, gatherClaudeCode); this adds the per-developer/day ROI metrics the same
// /v1/organizations/usage_report/claude_code record carries: distinct sessions, net
// lines of code, commits/PRs authored via Claude Code, and the per-tool ACCEPT/REJECT
// tally (Edit/MultiEdit/Write/NotebookEdit) — the "how much of what Claude proposed did
// the developer keep" governance signal.
//
// It is EVIDENCE, not cost: a model.FindingReport (the sealed observation set is
// Edge/Cost/Finding), Info severity, that the ledger keeps and module XXI dashboards
// render — so it is emitted for EVERY developer regardless of billing source (api or
// subscription), with no double-count concern (cost stays subscription-only in
// gatherClaudeCode). Minimal data (docs/SECURITY-HARDENING.md): the structural counts ride the Title;
// the developer identity is the chargeback/ROI subject (the same accepted attribution
// exception the cost path makes for Actor — an org-internal email/key-name, never a
// credential), and the full tuple is folded into the one-way DetailHash.
package claudeapi

import (
	"fmt"
	"time"

	"github.com/olivaresai/olivares/connectors/internal/redact"
	"github.com/olivaresai/olivares/sdk/model"
)

// subjectClaudeCodeDeveloper is the FindingReport subject for a per-developer Claude
// Code productivity record.
const subjectClaudeCodeDeveloper = "claude_code_developer"

// subjectDeveloper is the SubjectKind of an Analytics-sourced adoption MetricSample: the
// developer (email) the daily productivity is attributed to — the ROI/chargeback subject
// (the same accepted attribution exception the cost path makes for Actor). The adoption
// module gates the per-developer READ behind a permission; the team/org aggregates do
// not expose it (per-team default, per-developer opt-in).
const subjectDeveloper = "developer"

// Claude Code adoption metric names — the WIRE CONTRACT shared with the OTLP receiver
// (connectors/claude/events.go) and the adoption module (modules/claudeadoption). They
// MUST stay byte-identical across the three so the Analytics feed and the OTLP plane
// fold into ONE adoption read-model keyed by metric name. The Analytics feed carries no
// active_time and never carries cost here. Source: code.claude.com/docs/en/monitoring-usage.
const (
	metricSessionCount     = "claude_code.session.count"
	metricLinesOfCode      = "claude_code.lines_of_code.count"
	metricCommit           = "claude_code.commit.count"
	metricPullRequest      = "claude_code.pull_request.count"
	metricTokenUsage       = "claude_code.token.usage"
	metricCodeEditDecision = "claude_code.code_edit_tool.decision"
)

// claudeCodeMetricSamples decomposes one developer/day Claude Code Analytics record into
// the queryable adoption MetricSamples the dashboard aggregates: per-developer
// sessions, net lines of code (added/removed), commits, PRs, the per-tool accept/reject
// tally and the per-model token split. They are DAILY SNAPSHOTS (Additive=false): the
// feed reports the day's running total, so a re-pull REPLACES the same (developer, day,
// name, dims) bucket (the module keeps the max — the total only grows within a day). It
// returns only NON-ZERO measures (an empty record is never a fabricated zero) and nil
// when the record carries no ROI subject. It is EVIDENCE/ROI data, emitted for EVERY
// developer regardless of billing source — NEVER cost (cost stays subscription-only in
// gatherClaudeCode), so there is no double-count concern.
func claudeCodeMetricSamples(rec claudeCodeRecord, at time.Time) []model.MetricSample {
	subject := rec.Actor.ref()
	if subject == "" {
		return nil
	}
	var out []model.MetricSample
	add := func(name string, value int64, unit string, dims map[string]string) {
		if value <= 0 {
			return
		}
		out = append(out, model.MetricSample{
			Name: name, Value: value, Unit: unit, Additive: false,
			SubjectKind: subjectDeveloper, SubjectRef: subject, OccurredAt: at,
			Dimensions: dims,
		})
	}
	cm := rec.CoreMetrics
	add(metricSessionCount, cm.NumSessions, "sessions", nil)
	add(metricLinesOfCode, cm.LinesOfCode.Added, "lines", map[string]string{"type": "added"})
	add(metricLinesOfCode, cm.LinesOfCode.Removed, "lines", map[string]string{"type": "removed"})
	add(metricCommit, cm.Commits, "commits", nil)
	add(metricPullRequest, cm.PullRequests, "pull_requests", nil)
	addTool := func(tool string, t claudeCodeToolTally) {
		add(metricCodeEditDecision, t.Accepted, "decisions", map[string]string{"tool": tool, "decision": "accept"})
		add(metricCodeEditDecision, t.Rejected, "decisions", map[string]string{"tool": tool, "decision": "reject"})
	}
	addTool("Edit", rec.ToolActions.Edit)
	addTool("MultiEdit", rec.ToolActions.MultiEdit)
	addTool("Write", rec.ToolActions.Write)
	addTool("NotebookEdit", rec.ToolActions.NotebookEdit)
	for _, mb := range rec.ModelBreakdown {
		t := mb.Tokens
		add(metricTokenUsage, t.Input, "tokens", map[string]string{"type": "input", "model": mb.Model})
		add(metricTokenUsage, t.Output, "tokens", map[string]string{"type": "output", "model": mb.Model})
		add(metricTokenUsage, t.CacheRead, "tokens", map[string]string{"type": "cacheRead", "model": mb.Model})
		add(metricTokenUsage, t.CacheCreation, "tokens", map[string]string{"type": "cacheCreation", "model": mb.Model})
	}
	return out
}

// claudeCodeProductivityFinding builds the per-developer/day productivity evidence
// finding for one feed record. ok is false when the record carries NO productivity
// signal (no sessions, no LOC, no commits/PRs, no tool actions) — an empty record is
// not ledgered as a fabricated zero. The Title summarizes the ROI counts and the tool
// accept-rate; the DetailHash binds the full tuple so an auditor can re-derive it
// without the values leaving the connector.
func claudeCodeProductivityFinding(rec claudeCodeRecord, at time.Time) (model.FindingReport, bool) {
	cm := rec.CoreMetrics
	accepted := rec.ToolActions.accepted()
	rejected := rec.ToolActions.rejected()
	if cm.NumSessions == 0 && cm.LinesOfCode.Added == 0 && cm.LinesOfCode.Removed == 0 &&
		cm.Commits == 0 && cm.PullRequests == 0 && accepted == 0 && rejected == 0 {
		return model.FindingReport{}, false
	}
	actor := rec.Actor.ref() // chargeback/ROI identity (email or api-key name)
	detail := fmt.Sprintf(
		"claude_code_productivity|actor=%s|date=%s|customer_type=%s|terminal=%s|sessions=%d|loc_added=%d|loc_removed=%d|commits=%d|prs=%d|tools_accepted=%d|tools_rejected=%d|edit=%d/%d|multi_edit=%d/%d|write=%d/%d|notebook=%d/%d",
		actor, rec.Date, rec.CustomerType, rec.TerminalType,
		cm.NumSessions, cm.LinesOfCode.Added, cm.LinesOfCode.Removed, cm.Commits, cm.PullRequests,
		accepted, rejected,
		rec.ToolActions.Edit.Accepted, rec.ToolActions.Edit.Rejected,
		rec.ToolActions.MultiEdit.Accepted, rec.ToolActions.MultiEdit.Rejected,
		rec.ToolActions.Write.Accepted, rec.ToolActions.Write.Rejected,
		rec.ToolActions.NotebookEdit.Accepted, rec.ToolActions.NotebookEdit.Rejected,
	)
	return model.FindingReport{
		Kind:        "analytics",
		Severity:    model.SeverityInfo,
		SubjectKind: subjectClaudeCodeDeveloper,
		SubjectRef:  nonEmptyRef(actor),
		Title: fmt.Sprintf(
			"Claude Code productivity: %d session(s), +%d/-%d LOC, %d commit(s), %d PR(s); %d of %d tool action(s) accepted",
			cm.NumSessions, cm.LinesOfCode.Added, cm.LinesOfCode.Removed, cm.Commits, cm.PullRequests,
			accepted, accepted+rejected),
		DetailHash: redact.Hash(detail),
		OccurredAt: at,
	}, true
}

// nonEmptyRef returns the actor ref, or a stable placeholder when the feed omitted any
// actor identity — so a finding always has a subject (never an empty SubjectRef).
func nonEmptyRef(actor string) string {
	if actor == "" {
		return "claude-code-unknown-actor"
	}
	return actor
}
