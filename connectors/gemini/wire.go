// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package gemini

// This file holds the JSON wire shapes the connector reads. Only the minimal-data
// fields the connector needs are mapped: model identifiers, token COUNTS and
// inventory metadata — never prompts, completions, or key values. The full
// upstream payload may carry more fields; they are ignored.

// modelsResponse is GET /v1beta/models. Pagination follows nextPageToken until it
// is empty.
type modelsResponse struct {
	Models        []modelEntry `json:"models"`
	NextPageToken string       `json:"nextPageToken"`
}

// modelEntry is one model in the models list. Name is the resource path
// ("models/gemini-2.5-pro"); the connector trims the "models/" prefix to the ref.
type modelEntry struct {
	Name                       string   `json:"name"`
	DisplayName                string   `json:"displayName"`
	InputTokenLimit            int64    `json:"inputTokenLimit"`
	OutputTokenLimit           int64    `json:"outputTokenLimit"`
	SupportedGenerationMethods []string `json:"supportedGenerationMethods"`
}

// usageResponse is the operator-wired token-usage export Gather consumes. Gemini
// has NO first-party usage/cost report, so this is the shape of the operator's own
// Vertex / Cloud-Billing token-usage export (the operator points usage_url at it).
// It is intentionally minimal: bucket timestamps and per-model token COUNTS only.
type usageResponse struct {
	Data []usageBucket `json:"data"`
}

// usageBucket is one time bucket with its per-model results.
type usageBucket struct {
	OccurredAt string        `json:"occurred_at"`
	Results    []usageResult `json:"results"`
}

// usageResult is one model's token counts in a bucket: standard input,
// cache-read (cached) input, and output tokens.
type usageResult struct {
	Model             string `json:"model"`
	InputTokens       int64  `json:"input_tokens"`
	OutputTokens      int64  `json:"output_tokens"`
	CachedInputTokens int64  `json:"cached_input_tokens"`
}
