// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// This file ingests the Enterprise Analytics API (family #3 — the gap
// A2/A3/A4/A5 flagged as the not-yet-implemented family). It is the org-level
// subscription-seat engagement roll-up: daily/weekly/monthly active users (DAU/WAU/MAU)
// across Chat / Claude Code / Cowork, plus seat utilization (assigned seats, pending
// invites) and the Cowork-specific active-user roll-up. It feeds module XXI dashboards
// and the FinOps subscription-attribution map.
//
// FOUR FACTS keep this honest and correctly decomposed:
//
//   - DISTINCT CREDENTIAL. Enterprise Analytics authenticates with an x-api-key carrying
//     the read:analytics scope (Enterprise-plan Primary-Owner/Admin) — a DIFFERENT key
//     and question than the Admin key (Usage&Cost/Claude Code) or the Compliance key.
//     The connector keeps it in its OWN slot (analytics_key) and is deny-closed: no
//     analytics_key ⇒ no ingest, an honest absence, never a fabricated zero. Conflating
//     it with the Admin key is exactly the mistake made.
//   - NO COST EMITTED. The Enterprise Analytics cost/usage endpoints OVERLAP the Usage &
//     Cost API (the user_cost_report would double-count what cost_report already bills),
//     so this ingests only the ENGAGEMENT roll-up (active users / seats) as evidence —
//     never a CostSample. Subscription seat spend stays attributed via BillingSourceOf,
//     not metered here.
//   - SCHEMA IS VERIFIED. The official Admin API reference documents the summaries
//     envelope and per-day ActivitySummary shape (verified 2026-07-04 against
//     platform.claude.com/docs/en/api/admin/analytics and
//     /docs/en/manage-claude/analytics-api). Optional product splits are still read
//     defensively: absent/null fields read as 0 / "not reported", never inferred.
//   - DIRECT-SURFACE ONLY. Enterprise Analytics is an Anthropic-operated (Enterprise
//     plan) surface; it does not exist on Bedrock/Vertex/Foundry. On a non-Anthropic-
//     operated surface the ingest is skipped (the key would not be provisioned there).
//
// Authority (verified 2026-07-04): platform.claude.com/docs/en/api/admin/analytics
// and platform.claude.com/docs/en/manage-claude/analytics-api.
package claudeapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/olivaresai/olivares/connectors/internal/redact"
	"github.com/olivaresai/olivares/connectors/modelprovider"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

// Enterprise Analytics constants (verified schema, AsOf analyticsAsOf).
const (
	analyticsSummariesPath     = "/v1/organizations/analytics/summaries"
	readAnalyticsScope         = "read:analytics"
	subjectEnterpriseAnalytics = "anthropic.enterprise_analytics"

	// Emits the official org-level Claude Code active-user series under this
	// new adoption wire name. The adoption module registers the name in until
	// then these bus samples are ignored by the module filter, which is the intended
	// additive rollout.
	metricClaudeCodeActiveUsers = "claude_code.active_users"
)

// enterpriseSummaryResponse is the verified envelope of GET
// /v1/organizations/analytics/summaries. It returns one ActivitySummary per day in the
// [starting_date, ending_date) window; extra future product splits are ignored.
type enterpriseSummaryResponse struct {
	Summaries []enterpriseActivitySummary `json:"summaries"`
}

// enterpriseActivitySummary is one per-day Enterprise Analytics activity roll-up.
// Counts are aggregate org-level values, not per-user rows. The documented optional
// Chat / Claude Code product splits are presence-tracked so the connector can tell
// "breakdown absent" from a present zero; the untracked product families
// (claude_design_*, office_agent_*, science_*) are deliberately ignored.
type enterpriseActivitySummary struct {
	StartingAt string `json:"starting_at"`
	EndingAt   string `json:"ending_at"`

	AssignedSeatCount  int64 `json:"assigned_seat_count"`
	PendingInviteCount int64 `json:"pending_invite_count"`

	DailyActiveUserCount   int64 `json:"daily_active_user_count"`
	WeeklyActiveUserCount  int64 `json:"weekly_active_user_count"`
	MonthlyActiveUserCount int64 `json:"monthly_active_user_count"`

	DailyAdoptionRate   float64 `json:"daily_adoption_rate"`
	WeeklyAdoptionRate  float64 `json:"weekly_adoption_rate"`
	MonthlyAdoptionRate float64 `json:"monthly_adoption_rate"`

	CoworkDailyActiveUserCount   int64 `json:"cowork_daily_active_user_count"`
	CoworkWeeklyActiveUserCount  int64 `json:"cowork_weekly_active_user_count"`
	CoworkMonthlyActiveUserCount int64 `json:"cowork_monthly_active_user_count"`

	ChatDailyActiveUserCount   int64 `json:"chat_daily_active_user_count"`
	ChatWeeklyActiveUserCount  int64 `json:"chat_weekly_active_user_count"`
	ChatMonthlyActiveUserCount int64 `json:"chat_monthly_active_user_count"`

	ClaudeCodeDailyActiveUserCount   int64 `json:"claude_code_daily_active_user_count"`
	ClaudeCodeWeeklyActiveUserCount  int64 `json:"claude_code_weekly_active_user_count"`
	ClaudeCodeMonthlyActiveUserCount int64 `json:"claude_code_monthly_active_user_count"`

	hasChatDailyActiveUserCount         bool
	hasChatWeeklyActiveUserCount        bool
	hasChatMonthlyActiveUserCount       bool
	hasClaudeCodeDailyActiveUserCount   bool
	hasClaudeCodeWeeklyActiveUserCount  bool
	hasClaudeCodeMonthlyActiveUserCount bool
}

func (s *enterpriseActivitySummary) UnmarshalJSON(b []byte) error {
	type alias enterpriseActivitySummary
	var a alias
	if err := json.Unmarshal(b, &a); err != nil {
		return err
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	*s = enterpriseActivitySummary(a)
	s.hasChatDailyActiveUserCount = hasJSONField(raw, "chat_daily_active_user_count")
	s.hasChatWeeklyActiveUserCount = hasJSONField(raw, "chat_weekly_active_user_count")
	s.hasChatMonthlyActiveUserCount = hasJSONField(raw, "chat_monthly_active_user_count")
	s.hasClaudeCodeDailyActiveUserCount = hasJSONField(raw, "claude_code_daily_active_user_count")
	s.hasClaudeCodeWeeklyActiveUserCount = hasJSONField(raw, "claude_code_weekly_active_user_count")
	s.hasClaudeCodeMonthlyActiveUserCount = hasJSONField(raw, "claude_code_monthly_active_user_count")
	return nil
}

func hasJSONField(raw map[string]json.RawMessage, key string) bool {
	_, ok := raw[key]
	return ok
}

// analyticsClient builds the read-only client for the Enterprise Analytics API, keyed
// by the DISTINCT read:analytics credential (never the Admin key). nil when no
// analytics_key is configured (deny-closed: the caller then emits nothing).
func (s *Source) analyticsClient() *modelprovider.Client {
	if s.analyticsKey == "" {
		return nil
	}
	return modelprovider.NewClient(s.baseURL, s.doer, modelprovider.AuthAnthropicKey, s.analyticsKey,
		map[string]string{"anthropic-version": s.version})
}

// gatherEnterpriseAnalytics pulls the Enterprise Analytics summaries roll-up and emits
// it as engagement EVIDENCE (never cost). It is deny-closed: with no analytics_key it
// returns immediately (honest absence). It runs only on an Anthropic-operated surface
// (Enterprise Analytics does not exist on Bedrock/Vertex/Foundry). The API's freshness
// model guarantees today's date is empty, so the connector asks for a trailing 7-day
// window and lets the server default ending_date to the newest available day.
func (s *Source) gatherEnterpriseAnalytics(ctx context.Context, sink sdk.Sink) error {
	client := s.analyticsClient()
	if client == nil {
		return nil // deny-closed: distinct credential not provisioned
	}
	if !s.surface().Supports("admin") {
		// Anthropic-operated surfaces only; on a partner surface the key would not exist.
		return nil
	}
	start := s.clock().UTC().AddDate(0, 0, -7).Format(claudeCodeDateLayout)
	q := url.Values{}
	q.Set("starting_date", start)
	var resp enterpriseSummaryResponse
	if err := client.GetJSON(ctx, analyticsSummariesPath, q, &resp); err != nil {
		return err
	}
	if len(resp.Summaries) == 0 {
		return nil // honest absence: freshness lag or no available day, never a zero roll-up
	}
	for _, summary := range resp.Summaries {
		for _, sample := range enterpriseClaudeCodeActiveUserSamples(summary) {
			if err := sink.Emit(ctx, sample); err != nil {
				return err
			}
		}
	}
	latest, ok := latestEnterpriseSummary(resp.Summaries)
	if !ok {
		return nil
	}
	at := parseTime(latest.StartingAt)
	if at.IsZero() {
		at = s.clock().UTC()
	}
	return sink.Emit(ctx, enterpriseSummaryFinding(latest, at))
}

// enterpriseSummaryFinding maps the summaries roll-up to one engagement-evidence
// FindingReport (Info — analytics, not an alert). The active-user/seat counts ride the
// Title (the number surface dashboards read, mirroring the rate-limit count finding);
// the full tuple — including adoption rates and optional Chat / Claude Code splits — is
// folded into the one-way DetailHash. It carries NO cost (double-count boundary) and no
// PII (the summaries endpoint returns aggregate counts, not per-user rows).
func enterpriseSummaryFinding(r enterpriseActivitySummary, at time.Time) model.FindingReport {
	day := enterpriseSummaryDay(r)
	detail := enterpriseSummaryDetail(r, day)
	title := fmt.Sprintf(
		"Enterprise Analytics roll-up (%s): DAU=%d WAU=%d MAU=%d, adoption %.1f%%/%.1f%%/%.1f%%, Cowork DAU=%d, seats assigned=%d pending invites=%d",
		day, r.DailyActiveUserCount, r.WeeklyActiveUserCount, r.MonthlyActiveUserCount,
		r.DailyAdoptionRate, r.WeeklyAdoptionRate, r.MonthlyAdoptionRate,
		r.CoworkDailyActiveUserCount, r.AssignedSeatCount, r.PendingInviteCount)
	var splits []string
	if r.hasChatDailyActiveUserCount {
		splits = append(splits, fmt.Sprintf("Chat DAU=%d", r.ChatDailyActiveUserCount))
	}
	if r.hasClaudeCodeDailyActiveUserCount {
		splits = append(splits, fmt.Sprintf("Claude Code DAU=%d", r.ClaudeCodeDailyActiveUserCount))
	}
	if len(splits) > 0 {
		title += " (" + strings.Join(splits, ", ") + ")"
	}
	return model.FindingReport{
		Kind:        "analytics",
		Severity:    model.SeverityInfo,
		SubjectKind: subjectEnterpriseAnalytics,
		SubjectRef:  "organization",
		Title:       title,
		DetailHash:  redact.Hash(detail),
		OccurredAt:  at,
	}
}

func enterpriseSummaryDay(r enterpriseActivitySummary) string {
	if at := parseTime(r.StartingAt); !at.IsZero() {
		return at.Format(claudeCodeDateLayout)
	}
	return r.StartingAt
}

func enterpriseSummaryDetail(r enterpriseActivitySummary, day string) string {
	parts := []string{
		fmt.Sprintf("enterprise_analytics_summary|date=%s|starting_at=%s|ending_at=%s", day, r.StartingAt, r.EndingAt),
		fmt.Sprintf("dau=%d|wau=%d|mau=%d", r.DailyActiveUserCount, r.WeeklyActiveUserCount, r.MonthlyActiveUserCount),
		fmt.Sprintf("seats_assigned=%d|invites_pending=%d", r.AssignedSeatCount, r.PendingInviteCount),
		fmt.Sprintf("daily_adoption_rate=%.4f|weekly_adoption_rate=%.4f|monthly_adoption_rate=%.4f", r.DailyAdoptionRate, r.WeeklyAdoptionRate, r.MonthlyAdoptionRate),
		fmt.Sprintf("cowork_dau=%d|cowork_wau=%d|cowork_mau=%d", r.CoworkDailyActiveUserCount, r.CoworkWeeklyActiveUserCount, r.CoworkMonthlyActiveUserCount),
		fmt.Sprintf("schema=verified(read:analytics;asof=%s)", analyticsAsOf),
	}
	if r.hasChatDailyActiveUserCount || r.hasChatWeeklyActiveUserCount || r.hasChatMonthlyActiveUserCount {
		parts = append(parts, fmt.Sprintf("chat_dau=%d|chat_wau=%d|chat_mau=%d", r.ChatDailyActiveUserCount, r.ChatWeeklyActiveUserCount, r.ChatMonthlyActiveUserCount))
	}
	if r.hasClaudeCodeDailyActiveUserCount || r.hasClaudeCodeWeeklyActiveUserCount || r.hasClaudeCodeMonthlyActiveUserCount {
		parts = append(parts, fmt.Sprintf("claude_code_dau=%d|claude_code_wau=%d|claude_code_mau=%d", r.ClaudeCodeDailyActiveUserCount, r.ClaudeCodeWeeklyActiveUserCount, r.ClaudeCodeMonthlyActiveUserCount))
	}
	return strings.Join(parts, "|")
}

func latestEnterpriseSummary(summaries []enterpriseActivitySummary) (enterpriseActivitySummary, bool) {
	if len(summaries) == 0 {
		return enterpriseActivitySummary{}, false
	}
	latest := summaries[len(summaries)-1]
	latestAt := parseTime(latest.StartingAt)
	for _, summary := range summaries[:len(summaries)-1] {
		at := parseTime(summary.StartingAt)
		if !at.IsZero() && (latestAt.IsZero() || at.After(latestAt)) {
			latest = summary
			latestAt = at
		}
	}
	return latest, true
}

func enterpriseClaudeCodeActiveUserSamples(r enterpriseActivitySummary) []model.MetricSample {
	at := parseTime(r.StartingAt)
	if at.IsZero() {
		return nil
	}
	var out []model.MetricSample
	add := func(window string, value int64, present bool) {
		if !present || value <= 0 {
			return
		}
		out = append(out, model.MetricSample{
			Name:        metricClaudeCodeActiveUsers,
			Value:       value,
			Unit:        "users",
			Additive:    false,
			SubjectKind: "organization",
			SubjectRef:  "organization",
			OccurredAt:  at,
			Dimensions:  map[string]string{"window": window, "plane": "official_enterprise"},
		})
	}
	add("daily", r.ClaudeCodeDailyActiveUserCount, r.hasClaudeCodeDailyActiveUserCount)
	add("weekly", r.ClaudeCodeWeeklyActiveUserCount, r.hasClaudeCodeWeeklyActiveUserCount)
	add("monthly", r.ClaudeCodeMonthlyActiveUserCount, r.hasClaudeCodeMonthlyActiveUserCount)
	return out
}
