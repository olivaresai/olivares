// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package openrouter

// wire.go holds the JSON wire shapes the OpenRouter connector reads. Only the
// minimal-data fields the connector maps are declared — model catalog metadata,
// per-token list pricing, and account usage/limit posture — never a key value,
// prompt or completion (docs/SECURITY-HARDENING.md). Verification tiers:
//
//   - VERIFIED-SHAPE — Models API (GET /api/v1/models): the response is
//     {data:[{id, canonical_slug, name, created, context_length, description,
//     architecture{...}, pricing{prompt, completion, request, image, ...},
//     top_provider{context_length, max_completion_tokens, is_moderated},
//     supported_parameters[...]}]}. Pricing values are per-TOKEN USD strings
//     (e.g. "0.000003"); the connector multiplies by 1e6 to store USD/MTok.
//   - VERIFIED-SHAPE — Key API (GET /api/v1/auth/key): {data:{label, usage,
//     limit, limit_remaining, is_free_tier, rate_limit{requests, interval}}} —
//     the stable account-posture surface (usage/limit in USD; limit* nullable).
//
// The beta grouped-analytics endpoint (GET /api/v1/analytics/activity, mgmt key,
// last 30 UTC days) is NOT read here: its response shape is beta and could not be
// pinned to a stable schema offline, so per-model batch usage is left to the
// Meter path (the caller that drove the inference reports the model + tokens) —
// never a fabricated shape (ARCHITECTURE.md).

// --- Models API (VERIFIED-SHAPE) -----------------------------------------------

type modelsResponse struct {
	Data []modelEntry `json:"data"`
}

type modelEntry struct {
	ID                  string        `json:"id"`
	CanonicalSlug       string        `json:"canonical_slug"`
	Name                string        `json:"name"`
	Created             int64         `json:"created"`
	ContextLength       int64         `json:"context_length"`
	Architecture        *architecture `json:"architecture"`
	Pricing             *pricing      `json:"pricing"`
	TopProvider         *topProvider  `json:"top_provider"`
	SupportedParameters []string      `json:"supported_parameters"`
}

type architecture struct {
	Modality        string   `json:"modality"`
	InputModalities []string `json:"input_modalities"`
	Tokenizer       string   `json:"tokenizer"`
}

// pricing values are per-TOKEN USD, serialized as strings ("0" when free).
type pricing struct {
	Prompt     string `json:"prompt"`
	Completion string `json:"completion"`
	Request    string `json:"request"`
	Image      string `json:"image"`
}

type topProvider struct {
	ContextLength       int64 `json:"context_length"`
	MaxCompletionTokens int64 `json:"max_completion_tokens"`
	IsModerated         bool  `json:"is_moderated"`
}

// --- Key API (VERIFIED-SHAPE) --------------------------------------------------

// keyResponse is GET /api/v1/auth/key — the account usage/limit posture for the
// configured key. usage and limit are USD; limit/limit_remaining are nullable
// (an uncapped key reports null limit).
type keyResponse struct {
	Data keyData `json:"data"`
}

type keyData struct {
	Label          string     `json:"label"`
	Usage          float64    `json:"usage"`
	Limit          *float64   `json:"limit"`
	LimitRemaining *float64   `json:"limit_remaining"`
	IsFreeTier     bool       `json:"is_free_tier"`
	RateLimit      *rateLimit `json:"rate_limit"`
}

type rateLimit struct {
	Requests int64  `json:"requests"`
	Interval string `json:"interval"`
}
