// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package openai

// This file holds the JSON wire shapes of the OpenAI org API responses the
// connector reads. Only the minimal-data fields the connector needs are mapped:
// token COUNTS, model/project/key identifiers and metadata — never prompts,
// completions, or key values. The full upstream payload may carry more fields;
// they are ignored.

// usageResponse is /v1/organization/usage/completions. Buckets carry per-model
// token counts; pagination follows next_page when has_more is true.
type usageResponse struct {
	Data     []usageBucket `json:"data"`
	HasMore  bool          `json:"has_more"`
	NextPage string        `json:"next_page"`
}

// usageBucket is one time bucket with its per-group results. Times are Unix seconds.
type usageBucket struct {
	StartTime int64         `json:"start_time"`
	EndTime   int64         `json:"end_time"`
	Results   []usageResult `json:"results"`
}

// usageResult is one grouped usage row. OpenAI's input_tokens INCLUDES cached
// input; input_cached_tokens is the cached portion, so uncached input is the
// difference.
type usageResult struct {
	Model             string `json:"model"`
	ProjectID         string `json:"project_id"`
	APIKeyID          string `json:"api_key_id"`
	ServiceTier       string `json:"service_tier"`
	InputTokens       int64  `json:"input_tokens"`
	OutputTokens      int64  `json:"output_tokens"`
	InputCachedTokens int64  `json:"input_cached_tokens"`
}

// usageModerationsResponse is /v1/organization/usage/moderations (safety
// posture). Its only load-bearing field is num_model_requests — the count of
// moderation calls in a bucket, which tells us whether the org uses moderation.
// Moderation is free, so this usage signal (not the costs report) is the only
// API-derived indication of moderation activity.
type usageModerationsResponse struct {
	Data     []usageModerationsBucket `json:"data"`
	HasMore  bool                     `json:"has_more"`
	NextPage string                   `json:"next_page"`
}

// usageModerationsBucket is one time bucket of moderation usage results.
type usageModerationsBucket struct {
	Results []usageModerationsResult `json:"results"`
}

// usageModerationsResult is one moderation usage row. We read only the request count
// (the posture signal is whether the org calls moderation, not how much), so the
// other fields the payload carries are intentionally not mapped (minimal-data).
type usageModerationsResult struct {
	NumModelRequests int64 `json:"num_model_requests"`
}

// modelsResponse is /v1/models. The endpoint returns the full list with no
// pagination.
type modelsResponse struct {
	Object string       `json:"object"`
	Data   []modelEntry `json:"data"`
}

// modelEntry is one model in the models list. Created is Unix seconds.
type modelEntry struct {
	ID      string `json:"id"`
	Created int64  `json:"created"`
	OwnedBy string `json:"owned_by"`
	Object  string `json:"object"`
}

// adminKeysResponse is /v1/organization/admin_api_keys. The value is never
// returned by the API; only redacted_value (a masked suffix) is, which is safe to
// display. Cursor pagination via last_id + has_more.
type adminKeysResponse struct {
	Data    []adminKeyEntry `json:"data"`
	HasMore bool            `json:"has_more"`
	LastID  string          `json:"last_id"`
}

// adminKeyEntry is one admin API key's inventory metadata.
type adminKeyEntry struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	Status        string `json:"status"`
	RedactedValue string `json:"redacted_value"`
	CreatedAt     int64  `json:"created_at"`
}

// projectsResponse is /v1/organization/projects. Cursor pagination via last_id +
// has_more.
type projectsResponse struct {
	Data    []projectEntry `json:"data"`
	HasMore bool           `json:"has_more"`
	LastID  string         `json:"last_id"`
}

// projectEntry is one project's inventory metadata. Status "archived" marks an
// archived project.
type projectEntry struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Status    string `json:"status"`
	CreatedAt int64  `json:"created_at"`
}

// --- Org Users (GET /v1/organization/users) ---

// orgUsersResponse is the org user list with cursor pagination via last_id +
// has_more.
type orgUsersResponse struct {
	Data    []orgUserEntry `json:"data"`
	HasMore bool           `json:"has_more"`
	LastID  string         `json:"last_id"`
}

// orgUserEntry is one org user's inventory metadata. AddedAt is Unix seconds.
type orgUserEntry struct {
	ID      string `json:"id"`
	Email   string `json:"email"`
	Role    string `json:"role"`
	Name    string `json:"name"`
	AddedAt int64  `json:"added_at"`
}

// --- Audit Logs (GET /v1/organization/audit_logs) ---

// orgAuditLogsResponse is the org audit-log list with cursor pagination via
// last_id + has_more. It follows the same shape as the codex connector's
// auditLogsResponse (the same underlying OpenAI org API).
type orgAuditLogsResponse struct {
	Data    []orgAuditLogEntry `json:"data"`
	HasMore bool               `json:"has_more"`
	LastID  string             `json:"last_id"`
}

// orgAuditLogEntry is one audit-log record. EffectiveAt is Unix seconds; Type is
// the event type (e.g. "api_key.created", "login.succeeded"); the actor's nested
// user email / ip live under Session and are hashed, never surfaced.
type orgAuditLogEntry struct {
	ID          string          `json:"id"`
	Type        string          `json:"type"`
	EffectiveAt int64           `json:"effective_at"`
	Actor       orgAuditActor   `json:"actor"`
	Project     orgAuditProject `json:"project"`
}

// orgAuditActor is the audit-log actor union. Type is "session" or "api_key".
type orgAuditActor struct {
	Type    string          `json:"type"`
	Session orgAuditSession `json:"session"`
	APIKey  orgAuditAPIKey  `json:"api_key"`
}

type orgAuditSession struct {
	User      orgAuditUser `json:"user"`
	IPAddress string       `json:"ip_address"`
}

type orgAuditUser struct {
	ID    string `json:"id"`
	Email string `json:"email"`
}

type orgAuditAPIKey struct {
	ID string `json:"id"`
}

type orgAuditProject struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// --- Data Retention (GET /v1/organization/data_retention) ---

// dataRetentionResponse is the org-level or project-level data retention policy.
// Shape verified 2026-07-04 against developers.openai.com/api/docs/api-reference/administration.
type dataRetentionResponse struct {
	Object          string `json:"object"`
	Type            string `json:"type"`
	ZDR             bool   `json:"zero_data_retention"`
	RetentionDays   int    `json:"retention_days"`
	AbuseMonitoring string `json:"abuse_monitoring"`
}

// spendAlertsResponse is /v1/organization/spend_alerts and
// /v1/organization/projects/{project_id}/spend_alerts.
// Shape verified 2026-07-04 against developers.openai.com/api/docs/api-reference/administration.
type spendAlertsResponse struct {
	Object  string            `json:"object"`
	Data    []spendAlertEntry `json:"data"`
	FirstID string            `json:"first_id"`
	LastID  string            `json:"last_id"`
	HasMore bool              `json:"has_more"`
}

// spendAlertEntry is one organization or project spend alert.
// Shape verified 2026-07-04 against developers.openai.com/api/docs/api-reference/administration.
type spendAlertEntry struct {
	ID                  string                  `json:"id"`
	Object              string                  `json:"object"`
	ThresholdAmount     int64                   `json:"threshold_amount"`
	Currency            string                  `json:"currency"`
	Interval            string                  `json:"interval"`
	NotificationChannel spendAlertNotifyChannel `json:"notification_channel"`
}

// spendAlertNotifyChannel is the spend alert notification target metadata.
// Shape verified 2026-07-04 against developers.openai.com/api/docs/api-reference/administration.
type spendAlertNotifyChannel struct {
	Type          string   `json:"type"`
	Recipients    []string `json:"recipients"`
	SubjectPrefix string   `json:"subject_prefix"`
}

// modelPermissionsResponse is
// /v1/organization/projects/{project_id}/model_permissions.
// Shape verified 2026-07-04 against developers.openai.com/api/docs/api-reference/administration.
type modelPermissionsResponse struct {
	Object   string   `json:"object"`
	Mode     string   `json:"mode"`
	ModelIDs []string `json:"model_ids"`
}

// hostedToolPermissionsResponse is
// /v1/organization/projects/{project_id}/hosted_tool_permissions.
// Shape verified 2026-07-04 against developers.openai.com/api/docs/api-reference/administration.
type hostedToolPermissionsResponse struct {
	FileSearch      hostedToolPermission `json:"file_search"`
	WebSearch       hostedToolPermission `json:"web_search"`
	ImageGeneration hostedToolPermission `json:"image_generation"`
	MCP             hostedToolPermission `json:"mcp"`
	CodeInterpreter hostedToolPermission `json:"code_interpreter"`
}

// hostedToolPermission is one hosted tool enablement flag.
// Shape verified 2026-07-04 against developers.openai.com/api/docs/api-reference/administration.
type hostedToolPermission struct {
	Enabled bool `json:"enabled"`
}

// orgGroupsResponse is /v1/organization/groups.
// Shape verified 2026-07-04 against developers.openai.com/api/docs/api-reference/administration.
type orgGroupsResponse struct {
	Object  string          `json:"object"`
	Data    []orgGroupEntry `json:"data"`
	HasMore bool            `json:"has_more"`
	Next    string          `json:"next"`
}

// orgGroupEntry is one OpenAI organization group.
// Shape verified 2026-07-04 against developers.openai.com/api/docs/api-reference/administration.
type orgGroupEntry struct {
	Object        string `json:"object"`
	ID            string `json:"id"`
	Name          string `json:"name"`
	CreatedAt     int64  `json:"created_at"`
	IsSCIMManaged bool   `json:"is_scim_managed"`
	GroupType     string `json:"group_type"`
}

// orgRolesResponse is /v1/organization/roles.
// Shape verified 2026-07-04 against developers.openai.com/api/docs/api-reference/administration.
type orgRolesResponse struct {
	Object  string         `json:"object"`
	Data    []orgRoleEntry `json:"data"`
	HasMore bool           `json:"has_more"`
	Next    string         `json:"next"`
}

// orgRoleEntry is one OpenAI organization role.
// Shape verified 2026-07-04 against developers.openai.com/api/docs/api-reference/administration.
type orgRoleEntry struct {
	Object         string   `json:"object"`
	ID             string   `json:"id"`
	Name           string   `json:"name"`
	Description    string   `json:"description"`
	Permissions    []string `json:"permissions"`
	ResourceType   string   `json:"resource_type"`
	PredefinedRole bool     `json:"predefined_role"`
}

// --- Costs API (GET /v1/organization/costs) ---

// orgCostsResponse is the bucketed Costs page. Pagination follows the org-API
// convention (has_more + next_page). This type is prefixed org to avoid
// colliding with wire types in other packages.
type orgCostsResponse struct {
	Data     []orgCostsBucket `json:"data"`
	HasMore  bool             `json:"has_more"`
	NextPage string           `json:"next_page"`
}

// orgCostsBucket is one daily cost bucket (the Costs API supports 1d
// granularity only).
type orgCostsBucket struct {
	StartTime int64            `json:"start_time"`
	EndTime   int64            `json:"end_time"`
	Results   []orgCostsResult `json:"results"`
}

// orgCostsResult is one billed cost line. Amount is in MAJOR currency units
// (dollars for USD); LineItem is the billed product line; ProjectID is the
// grouping echo when group_by includes it.
type orgCostsResult struct {
	Amount    float64  `json:"amount"`
	LineItem  string   `json:"line_item"`
	ProjectID string   `json:"project_id"`
	APIKeyID  string   `json:"api_key_id"`
	Quantity  *float64 `json:"quantity"`
}
