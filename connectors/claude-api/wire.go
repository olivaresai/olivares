// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package claudeapi

// This file holds the JSON wire shapes of the Anthropic Admin API responses the
// connector reads. Only the minimal-data fields the connector needs are mapped:
// token COUNTS, model/workspace/key identifiers and metadata — never prompts,
// completions, or key values. The full upstream payload may carry more fields;
// they are ignored.

// usageResponse is /v1/organizations/usage_report/messages. Buckets carry per-
// model token counts; pagination follows next_page when has_more is true.
type usageResponse struct {
	Data     []usageBucket `json:"data"`
	HasMore  bool          `json:"has_more"`
	NextPage string        `json:"next_page"`
}

// usageBucket is one time bucket with its per-group results.
type usageBucket struct {
	StartingAt string        `json:"starting_at"`
	EndingAt   string        `json:"ending_at"`
	Results    []usageResult `json:"results"`
}

// usageResult is one grouped usage row. The token fields mirror the Messages
// usage report: uncached input, cache-creation (write, nested per TTL),
// cache-read, output. The grouping-echo fields (workspace_id, api_key_id,
// service_tier, context_window, inference_geo, account_id, service_account_id) are
// null in the response unless that dimension is in group_by; the connector requests
// the full set so cost is attributable per the dimensions finance allocates on.
type usageResult struct {
	Model            string `json:"model"`
	WorkspaceID      string `json:"workspace_id"`
	APIKeyID         string `json:"api_key_id"`
	AccountID        string `json:"account_id"`
	ServiceAccountID string `json:"service_account_id"`
	// ServiceTier is an open string. The documented usage vocabulary is
	// standard|batch|flex|flex_discount|priority|priority_on_demand; do not validate
	// it here because new assigned/billing tiers can appear before this connector revs.
	ServiceTier   string `json:"service_tier"`
	ContextWindow string `json:"context_window"`
	// InferenceGeo is documented as global|us|not_available. not_available is returned
	// for models that do not support the inference_geo request param (pre-Feb-2026
	// models); it is still a reported residency dimension, not a parse failure.
	InferenceGeo         string              `json:"inference_geo"`
	UncachedInputTokens  int64               `json:"uncached_input_tokens"`
	CacheCreation        cacheCreationTokens `json:"cache_creation"`
	CacheReadInputTokens int64               `json:"cache_read_input_tokens"`
	OutputTokens         int64               `json:"output_tokens"`
	ServerToolUse        serverToolUse       `json:"server_tool_use"`
}

// serverToolUse carries server-side tool counts the usage report exposes (the only
// server-tool signal the usage endpoint returns; the priced server-tool spend lives
// in cost_report's cost_type dimension).
type serverToolUse struct {
	WebSearchRequests int64 `json:"web_search_requests"`
}

// cacheCreationTokens is the NESTED cache-write breakdown the Admin API returns,
// split by prompt-cache TTL. The two TTLs list at different rates (1h = 2× base
// input, 5m = 1.25× base input); the connector now carries them DISTINCTLY onto the
// CostSample (CacheCreation1hTokens/CacheCreation5mTokens) and prices each at its own
// tier, instead of summing them at the 5m rate (which under-counted 1h writes). The
// API has no flat `cache_creation_input_tokens` field on this endpoint — only this
// nested object — so each TTL must be read explicitly.
type cacheCreationTokens struct {
	Ephemeral1hInputTokens int64 `json:"ephemeral_1h_input_tokens"`
	Ephemeral5mInputTokens int64 `json:"ephemeral_5m_input_tokens"`
}

// costReportResponse is /v1/organizations/cost_report. It returns BILLED cost (the
// authoritative figure to reconcile against the Anthropic invoice), in daily buckets
// only. Pagination follows next_page when has_more is true.
type costReportResponse struct {
	Data     []costReportBucket `json:"data"`
	HasMore  bool               `json:"has_more"`
	NextPage string             `json:"next_page"`
}

// costReportBucket is one daily cost bucket with its per-group results.
type costReportBucket struct {
	StartingAt string             `json:"starting_at"`
	EndingAt   string             `json:"ending_at"`
	Results    []costReportResult `json:"results"`
}

// costReportResult is one grouped cost row. amount is the billed cost in the
// currency's LOWEST units (cents for USD) as a DECIMAL STRING (e.g. "123.45" cents =
// $1.2345); currency is always "USD" today. The description-parsed fields (model,
// service_tier, cost_type, token_type, context_window, inference_geo) are populated
// only when grouping by description. Usage service_tier is now the open vocabulary
// standard|batch|flex|flex_discount|priority|priority_on_demand, but Priority Tier is
// NOT billed in cost_report (tracked via the usage endpoint).
type costReportResult struct {
	Amount        string `json:"amount"`
	Currency      string `json:"currency"`
	WorkspaceID   string `json:"workspace_id"`
	Description   string `json:"description"`
	Model         string `json:"model"`
	ServiceTier   string `json:"service_tier"`
	CostType      string `json:"cost_type"`
	TokenType     string `json:"token_type"`
	ContextWindow string `json:"context_window"`
	InferenceGeo  string `json:"inference_geo"`
}

// claudeCodeResponse is /v1/organizations/usage_report/claude_code — the free
// Claude Code Analytics feed (daily, per-developer). It is the per-actor ROI/cost
// surface; this connector consumes the COST side (per-model estimated_cost per actor)
// as CostSamples. The productivity metrics (lines of code, commits, tool accept/
// reject) are exec-dashboard data (module XXI), not a cost observation, so they are
// not mapped here.
type claudeCodeResponse struct {
	Data     []claudeCodeRecord `json:"data"`
	HasMore  bool               `json:"has_more"`
	NextPage string             `json:"next_page"`
}

// claudeCodeRecord is one actor's Claude Code activity for a single day.
// CustomerType is "api" (pay-as-you-go API key) or "subscription" (Pro/Team). API
// Claude Code spend is ALSO reported by usage_report/messages (per api_key_id), so
// counting its estimated_cost here too would double-count the estimated stream — the
// connector therefore emits the cost sample only for subscription customers (whose
// Claude Code usage is NOT API-key metered and so is absent from usage_report).
type claudeCodeRecord struct {
	Date           string                `json:"date"`
	CustomerType   string                `json:"customer_type"`
	OrganizationID string                `json:"organization_id"`
	TerminalType   string                `json:"terminal_type"`
	Actor          claudeCodeActor       `json:"actor"`
	CoreMetrics    claudeCodeCoreMetrics `json:"core_metrics"`
	ToolActions    claudeCodeToolActions `json:"tool_actions"`
	ModelBreakdown []claudeCodeModelSpan `json:"model_breakdown"`
}

// claudeCodeCoreMetrics is the per-actor/day productivity roll-up the feed reports
// (family #2 depth): distinct sessions, net lines of code, and the commits/PRs
// authored via Claude Code. These are NON-cost ROI metrics (module XXI dashboards),
// carried as evidence, never as a CostSample (cost rides model_breakdown).
type claudeCodeCoreMetrics struct {
	NumSessions  int64             `json:"num_sessions"`
	LinesOfCode  claudeCodeLOCSpan `json:"lines_of_code"`
	Commits      int64             `json:"commits_by_claude_code"`
	PullRequests int64             `json:"pull_requests_by_claude_code"`
}

// claudeCodeLOCSpan is the lines-of-code added/removed by Claude Code for an actor/day.
type claudeCodeLOCSpan struct {
	Added   int64 `json:"added"`
	Removed int64 `json:"removed"`
}

// claudeCodeToolActions is the per-tool accept/reject tally — the governance-relevant
// "how much of what Claude proposed did the developer accept" signal (family #2).
// Each tool the feed reports carries an accepted + rejected count.
type claudeCodeToolActions struct {
	Edit         claudeCodeToolTally `json:"edit_tool"`
	MultiEdit    claudeCodeToolTally `json:"multi_edit_tool"`
	Write        claudeCodeToolTally `json:"write_tool"`
	NotebookEdit claudeCodeToolTally `json:"notebook_edit_tool"`
}

// claudeCodeToolTally is one tool's accepted/rejected action counts.
type claudeCodeToolTally struct {
	Accepted int64 `json:"accepted"`
	Rejected int64 `json:"rejected"`
}

// accepted/rejected sum the tool tallies across all reported tools.
func (t claudeCodeToolActions) accepted() int64 {
	return t.Edit.Accepted + t.MultiEdit.Accepted + t.Write.Accepted + t.NotebookEdit.Accepted
}

func (t claudeCodeToolActions) rejected() int64 {
	return t.Edit.Rejected + t.MultiEdit.Rejected + t.Write.Rejected + t.NotebookEdit.Rejected
}

// claudeCodeActor identifies the developer (user_actor → email) or automation
// (api_actor → key name). ref() returns the actor's chargeback IDENTITY in clear
// (developer email — org-internal PII — or api-key name), never a credential/secret.
// This is the deliberate, accepted exception to minimal-data for attribution; it is
// NOT masked (cf. the masked partial_key_hint), so it is not redaction-safe by itself.
type claudeCodeActor struct {
	Type         string `json:"type"`
	EmailAddress string `json:"email_address"`
	APIKeyName   string `json:"api_key_name"`
}

// ref returns the actor's chargeback identity (developer email or api-key name).
func (a claudeCodeActor) ref() string {
	if a.EmailAddress != "" {
		return a.EmailAddress
	}
	return a.APIKeyName
}

// claudeCodeModelSpan is one model's tokens + estimated cost for an actor/day.
type claudeCodeModelSpan struct {
	Model         string             `json:"model"`
	Tokens        claudeCodeTokens   `json:"tokens"`
	EstimatedCost claudeCodeCostSpan `json:"estimated_cost"`
}

// claudeCodeTokens is the per-model token split (cache_creation is untiered here).
type claudeCodeTokens struct {
	Input         int64 `json:"input"`
	Output        int64 `json:"output"`
	CacheRead     int64 `json:"cache_read"`
	CacheCreation int64 `json:"cache_creation"`
}

// claudeCodeCostSpan is the estimated cost: amount in minor units (cents for USD) as
// an integer (e.g. 186 = $1.86), currency code.
type claudeCodeCostSpan struct {
	Amount   int64  `json:"amount"`
	Currency string `json:"currency"`
}

// modelsResponse is /v1/models. Cursor pagination via last_id + has_more.
type modelsResponse struct {
	Data    []modelEntry `json:"data"`
	HasMore bool         `json:"has_more"`
	LastID  string       `json:"last_id"`
}

// modelEntry is one model in the models list. The capabilities/limits fields
// (ANT2-16) are the live source-of-truth that supersedes the hardcoded catalog: a
// model's effort tiers, thinking modes, context-management strategies and token
// windows are read from the API rather than guessed. They are absent on surfaces
// without the Models API (the connector then uses the declared catalog).
type modelEntry struct {
	ID             string         `json:"id"`
	DisplayName    string         `json:"display_name"`
	CreatedAt      string         `json:"created_at"`
	MaxInputTokens int64          `json:"max_input_tokens"`
	MaxTokens      int64          `json:"max_tokens"`
	Capabilities   *modelCapsWire `json:"capabilities,omitempty"`
}

// modelCapsWire is the /v1/models capabilities object (ANT2-16). The effort, thinking
// and context_management knobs are modeled as the documented value lists; batch and
// structured_outputs are booleans. The exact upstream JSON may carry richer objects —
// extra fields are ignored (DiscardUnknown), and an absent field means "not reported",
// never "unsupported" (ARCHITECTURE.md).
type modelCapsWire struct {
	Batch             bool     `json:"batch"`
	StructuredOutputs bool     `json:"structured_outputs"`
	Effort            []string `json:"effort"`
	Thinking          []string `json:"thinking"`
	ContextManagement []string `json:"context_management"`
}

// apiKeysResponse is /v1/organizations/api_keys. The value is never returned by
// the API; only partial_key_hint (a masked suffix) is, which is safe to display.
type apiKeysResponse struct {
	Data    []apiKeyEntry `json:"data"`
	HasMore bool          `json:"has_more"`
	LastID  string        `json:"last_id"`
}

// apiKeyEntry is one API key's inventory metadata. expires_at + created_by + principal
// are the key-lifecycle/rotation governance signals: a key with no expiry or one
// long unrotated is a hygiene posture finding; created_by attributes who minted it;
// principal (VERIFIED 2026-07-20 against platform.claude.com) attributes WHAT the key
// authenticates AS — a service_account vs a user — which sharpens rotation expectations
// (a long-lived service-account key is the higher rotation risk). The value is NEVER
// returned by the API — only partial_key_hint (a masked suffix). status is one of
// active|inactive|archived|expired (the "expired" system-set state was added alongside
// key expiration on 2026-07-08; VERIFIED 2026-07-20).
type apiKeyEntry struct {
	ID             string         `json:"id"`
	Name           string         `json:"name"`
	WorkspaceID    string         `json:"workspace_id"`
	Status         string         `json:"status"`
	PartialKeyHint string         `json:"partial_key_hint"`
	CreatedAt      string         `json:"created_at"`
	ExpiresAt      string         `json:"expires_at"`
	CreatedBy      apiKeyActorRef `json:"created_by"`
	Principal      apiKeyActorRef `json:"principal"`
}

// apiKeyActorRef is a principal reference on an API key (created_by / principal). Only
// the reference id + type are read — attribution metadata, never a credential
// (docs/SECURITY-HARDENING.md). For principal, type ∈ {service_account, user}.
type apiKeyActorRef struct {
	ID   string `json:"id"`
	Type string `json:"type"`
}

// workspacesResponse is /v1/organizations/workspaces.
type workspacesResponse struct {
	Data    []workspaceEntry `json:"data"`
	HasMore bool             `json:"has_more"`
	LastID  string           `json:"last_id"`
}

// workspaceEntry is one workspace's inventory metadata. The governance fields
// (ANT2-06) are the data-residency policy, the customer-managed-key (CMEK) reference
// (external_key_id — a write-once ekey_ ref, NEVER the key material), the cloud-KMS
// compartment the CMEK key-policy is scoped to, the immutable home region
// (workspace_geo, today only "us") and operator tags. They are null on surfaces
// without the governance object; the connector reads them as inventory + posture.
type workspaceEntry struct {
	ID            string            `json:"id"`
	Name          string            `json:"name"`
	ArchivedAt    string            `json:"archived_at"`
	CreatedAt     string            `json:"created_at"`
	DataResidency *dataResidency    `json:"data_residency,omitempty"`
	ExternalKeyID string            `json:"external_key_id"`
	CompartmentID string            `json:"compartment_id"`
	WorkspaceGeo  string            `json:"workspace_geo"`
	Tags          map[string]string `json:"tags,omitempty"`
}

// dataResidency is the workspace's residency policy (ANT2-06): permitted inference
// geos and the default. It is the basis of the per-request residency check (a
// CostSample.InferenceGeo outside allowed_inference_geos is a violation).
type dataResidency struct {
	AllowedInferenceGeos []string `json:"allowed_inference_geos"`
	DefaultInferenceGeo  string   `json:"default_inference_geo"`
}

// externalKeysResponse is /v1/organizations/external_keys (ANT2-04). It lists the
// customer-managed encryption keys (CMEK) registered for the org. The API returns
// the ekey_ REFERENCE and the cloud-KMS provider config metadata — NEVER the key
// material; this shape mirrors that (no field can hold key bytes — docs/SECURITY-HARDENING.md).
type externalKeysResponse struct {
	Data    []externalKeyEntry `json:"data"`
	HasMore bool               `json:"has_more"`
	LastID  string             `json:"last_id"`
}

// externalKeyEntry is one external (CMEK) key's inventory metadata. provider_config
// names the cloud KMS (aws_kms|gcp_kms|azure_keyvault) but carries no secret; status
// reflects the last validate round-trip (the documented encrypt/decrypt check).
type externalKeyEntry struct {
	ID              string              `json:"id"`
	Name            string              `json:"name"`
	Status          string              `json:"status"`
	InUse           bool                `json:"in_use"`
	ProviderConfig  externalKeyProvider `json:"provider_config"`
	LastValidatedAt string              `json:"last_validated_at"`
	CreatedAt       string              `json:"created_at"`
}

// externalKeyProvider is the cloud-KMS provider descriptor of an external key. Only
// the provider TYPE is read (the routing/audit signal); the key arn/uri and any
// credential are deliberately NOT mapped (minimal-data, docs/SECURITY-HARDENING.md).
type externalKeyProvider struct {
	Type string `json:"type"` // aws_kms | gcp_kms | azure_keyvault
}

// rateLimitsResponse is /v1/organizations/rate_limits and
// /workspaces/{id}/rate_limits (ANT2-05, verified 2026-07-04 against
// platform.claude.com/docs/en/manage-claude/rate-limits-api). Pagination is by the
// page query parameter and next_page response field; these endpoints do NOT return
// has_more/last_id.
type rateLimitsResponse struct {
	Data     []rateLimitEntry `json:"data"`
	NextPage string           `json:"next_page"`
}

// rateLimitEntry is one rate-limit GROUP the read-only API reports. group_type
// partitions the group (model_group|batch|token_count|files|skills|web_search, open
// vocabulary). model_group rows carry every model id and alias in models; non-model
// groups carry null. Workspace rows carry OVERRIDES ONLY and may echo org_limit per
// limiter; absence means inherit the org value, NOT unlimited. Managed Agents are NOT
// covered by this API (documented caveat, not a fabricated zero).
type rateLimitEntry struct {
	Type      string                `json:"type"`
	GroupType string                `json:"group_type"`
	Models    []string              `json:"models"`
	Limits    []rateLimitValueEntry `json:"limits"`
}

// rateLimitValueEntry is one limiter in a rate-limit group. org_limit is nullable on
// workspace endpoints; keep the pointer so null folds to the catalog contract's
// 0-means-not-reported without pretending it is a hard zero.
type rateLimitValueEntry struct {
	Type     string `json:"type"`
	Value    int64  `json:"value"`
	OrgLimit *int64 `json:"org_limit"`
}
