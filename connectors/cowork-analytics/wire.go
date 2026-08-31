// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package coworkanalytics

// wire.go holds the minimal JSON projection of the Claude Enterprise Analytics API
// responses this connector reads. Field names are VERBATIM from the analytics
// reference (claude.com/docs + the Claude Help Center analytics API reference guide,
// support.claude.com article 13703965 — "REF"; AsOf 2026-06-10): the dotted
// documented keys (user.id, cowork_metrics.distinct_session_count,
// cowork_daily_active_user_count, …) map onto the nested objects below. Only the
// fields the connector maps are declared; the upstream payload may carry more and
// they are ignored. user.email_address is modeled so a forward-compatible decode is
// total, but it is NEVER read into an observation (PII; docs/SECURITY-HARDENING.md).
//
// The list ENVELOPE (data / has_more / next_page) and the cursor query param (page)
// are the verified shape of the org-analytics API family (mirrors
// connectors/claude-api usage_report/claude_code, same API surface); data_refreshed_at
// is the documented freshness watermark. For the /skills and /connectors feeds REF
// documents cursor pagination via next_page (null when exhausted) but does not
// spell out data/has_more/data_refreshed_at field-by-field — those envelopes are
// decoded tolerantly (absent keys decode to zero values) and pagination loops on
// next_page alone.

// summariesResponse is GET /v1/organizations/analytics/summaries — org-wide activity
// summaries including the Cowork DAU/WAU/MAU counts.
type summariesResponse struct {
	Data            []summaryRow `json:"data"`
	HasMore         bool         `json:"has_more"`
	NextPage        string       `json:"next_page"`
	DataRefreshedAt string       `json:"data_refreshed_at"`
}

// summaryRow is one activity-summary bucket. The cowork_* fields are the Cowork
// active-user counts; the un-prefixed counts are org-wide across every Claude surface.
// REF (AsOf 2026-06-10) documents the row keyed by starting_date + ending_date
// (ending_date EXCLUSIVE, UTC); Date is kept for tolerance against an older/shifted
// emitter, and the day-match in fetchSummary tries starting_date first, then date.
type summaryRow struct {
	StartingDate                 string `json:"starting_date"`
	EndingDate                   string `json:"ending_date"`
	Date                         string `json:"date"`
	DailyActiveUserCount         int64  `json:"daily_active_user_count"`
	WeeklyActiveUserCount        int64  `json:"weekly_active_user_count"`
	MonthlyActiveUserCount       int64  `json:"monthly_active_user_count"`
	AssignedSeatCount            int64  `json:"assigned_seat_count"`
	PendingInviteCount           int64  `json:"pending_invite_count"`
	CoworkDailyActiveUserCount   int64  `json:"cowork_daily_active_user_count"`
	CoworkWeeklyActiveUserCount  int64  `json:"cowork_weekly_active_user_count"`
	CoworkMonthlyActiveUserCount int64  `json:"cowork_monthly_active_user_count"`
}

// usersResponse is GET /v1/organizations/analytics/users — per-user engagement,
// including the cowork_metrics object this connector aggregates.
type usersResponse struct {
	Data            []userRow `json:"data"`
	HasMore         bool      `json:"has_more"`
	NextPage        string    `json:"next_page"`
	DataRefreshedAt string    `json:"data_refreshed_at"`
}

// userRow is one user's engagement for the requested period. user.id is the shared
// account identifier (the same namespace as Cowork OTEL user.id / Compliance
// actor.user_id) — the OTEL↔Compliance correlation key; user.email_address is PII
// and never read.
type userRow struct {
	User          userIdentity  `json:"user"`
	CoworkMetrics coworkMetrics `json:"cowork_metrics"`
}

// userIdentity is the per-user identity block.
type userIdentity struct {
	ID           string `json:"id"`
	EmailAddress string `json:"email_address"` // PII — never read into an observation
}

// coworkMetrics is the per-user Cowork engagement breakdown. All EIGHT fields are
// verified verbatim against REF (AsOf 2026-06-10): distinct_session_count,
// message_count ("Total user messages sent in Cowork"), action_count,
// dispatch_turn_count, skills_used_count, distinct_skills_used_count,
// connectors_used_count, distinct_connectors_used_count. Any other engagement field
// the API returns is ignored by the decoder (forward-compatible) rather than
// declared with an unverified name.
type coworkMetrics struct {
	DistinctSessionCount        int64 `json:"distinct_session_count"`
	MessageCount                int64 `json:"message_count"`
	ActionCount                 int64 `json:"action_count"`
	DispatchTurnCount           int64 `json:"dispatch_turn_count"`
	SkillsUsedCount             int64 `json:"skills_used_count"`
	DistinctSkillsUsedCount     int64 `json:"distinct_skills_used_count"`
	ConnectorsUsedCount         int64 `json:"connectors_used_count"`
	DistinctConnectorsUsedCount int64 `json:"distinct_connectors_used_count"`
}

// active reports whether the row shows ANY Cowork activity (so an all-zero user is
// not counted as an active Cowork user). It gates on every verified countable
// metric — including message_count, which REF (AsOf 2026-06-10) documents as a
// Cowork metric ("Total user messages sent in Cowork"), so a user whose only
// activity was sending messages IS Cowork-active. (An earlier revision excluded
// message_count because its name was not yet verified verbatim; it now is.)
func (m coworkMetrics) active() bool {
	return m.DistinctSessionCount > 0 || m.MessageCount > 0 || m.ActionCount > 0 ||
		m.DispatchTurnCount > 0 || m.SkillsUsedCount > 0 || m.DistinctSkillsUsedCount > 0 ||
		m.ConnectorsUsedCount > 0 || m.DistinctConnectorsUsedCount > 0
}

// skillsResponse is GET /v1/organizations/analytics/skills — per-skill usage across
// every Claude surface; this connector reads only the Cowork slice. Envelope decoded
// tolerantly (see the file header): pagination loops on next_page alone.
type skillsResponse struct {
	Data            []skillRow `json:"data"`
	HasMore         bool       `json:"has_more"`
	NextPage        string     `json:"next_page"`
	DataRefreshedAt string     `json:"data_refreshed_at"`
}

// skillRow is one skill's usage for the requested day. skill_name is the only
// identifier REF documents (no id field exists);
// cowork_metrics.distinct_session_skill_used_count is "Number of distinct Cowork
// sessions in which this skill was used at least once" (REF, AsOf 2026-06-10). The
// row also carries chat/claude_code/office metric blocks this connector does not
// read. The cowork_metrics block is always present (all-zero without Cowork usage).
type skillRow struct {
	SkillName         string `json:"skill_name"`
	DistinctUserCount int64  `json:"distinct_user_count"`
	CoworkMetrics     struct {
		DistinctSessionSkillUsedCount int64 `json:"distinct_session_skill_used_count"`
	} `json:"cowork_metrics"`
}

// connectorsResponse is GET /v1/organizations/analytics/connectors — per-connector
// usage; same envelope caveat as skillsResponse.
type connectorsResponse struct {
	Data            []connectorRow `json:"data"`
	HasMore         bool           `json:"has_more"`
	NextPage        string         `json:"next_page"`
	DataRefreshedAt string         `json:"data_refreshed_at"`
}

// connectorRow is one connector's usage for the requested day. connector_name is
// "The normalized name of the connector" (REF, AsOf 2026-06-10) — an org-level
// product identifier, not PII; cowork_metrics.distinct_session_connector_used_count
// is the distinct-Cowork-session use count. The cowork_metrics block is always
// present (all-zero without Cowork usage).
type connectorRow struct {
	ConnectorName     string `json:"connector_name"`
	DistinctUserCount int64  `json:"distinct_user_count"`
	CoworkMetrics     struct {
		DistinctSessionConnectorUsedCount int64 `json:"distinct_session_connector_used_count"`
	} `json:"cowork_metrics"`
}
