// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package cohere

// wire.go holds the JSON wire shapes the Cohere connector reads. Only the minimal-data
// fields the connector maps are declared — model catalog metadata, never a key value,
// prompt or completion (docs/SECURITY-HARDENING.md).
//
//   - VERIFIED-SHAPE — Models API (GET https://api.cohere.com/v1/models): the response is
//     {"models":[ModelEntry], "next_page_token":"..."} where each entry carries name,
//     endpoints[], context_length, finetuned, is_deprecated, and tokenizer_url. Pagination
//     is cursor-based via page_token / next_page_token. Confirmed against the Cohere API
//     reference (docs.cohere.com/reference/list-models).
//
//   - NO UNVERIFIED-OFFLINE seams: Cohere has no public REST endpoint for usage, billing,
//     org/team, or API-key inventory — all dashboard-only (dashboard.cohere.com).

// --- Models API (VERIFIED-SHAPE) -----------------------------------------------

// modelsResponse is GET /v1/models. Cohere uses "models" (not "data") as the array key,
// and "next_page_token" as the cursor for the next page. An empty next_page_token means
// no more pages.
type modelsResponse struct {
	Models        []modelEntry `json:"models"`
	NextPageToken string       `json:"next_page_token"`
}

// modelEntry is one model the Cohere Models API reports. The name field is the model
// identifier (Cohere uses "name" not "id"). endpoints[] lists the API surfaces the model
// supports (e.g. "chat", "embed", "rerank", "generate"). context_length is the maximum
// input context in tokens. finetuned and is_deprecated are lifecycle flags.
type modelEntry struct {
	Name          string   `json:"name"`
	Endpoints     []string `json:"endpoints"`
	ContextLength int64    `json:"context_length"`
	Finetuned     bool     `json:"finetuned"`
	IsDeprecated  bool     `json:"is_deprecated"`
	TokenizerURL  string   `json:"tokenizer_url"`
}
