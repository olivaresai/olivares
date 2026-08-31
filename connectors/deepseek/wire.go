// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package deepseek

// wire.go holds the JSON wire shapes the DeepSeek connector reads. Only the minimal-data
// fields the connector maps are declared — model catalog metadata and account-balance
// posture only, never a key value, prompt or completion (docs/SECURITY-HARDENING.md). Balance surface
// currency was verified against api-docs.deepseek.com on 2026-07-04. Verification tier:
//
//   - VERIFIED-SHAPE — Models API (GET https://api.deepseek.com/models): the response is
//     {object:"list", data:[{id, object, created, owned_by}]} — OpenAI-compatible format.
//     Confirmed against the DeepSeek API documentation and live API response. Unlike
//     OpenAI/Mistral, the DeepSeek models response does NOT carry per-model capability
//     booleans or max_context_length; those are sourced from the declared family table.

// --- Models API (VERIFIED-SHAPE) -----------------------------------------------

// modelsResponse is GET /models. The API returns the full list in one data array (no
// pagination). object is "list".
type modelsResponse struct {
	Object string       `json:"object"`
	Data   []modelEntry `json:"data"`
}

// modelEntry is one model the Models API reports. The DeepSeek models response is
// OpenAI-compatible but minimal: id, object, created (unix epoch), owned_by. No
// capability booleans, no context window, no deprecation — capabilities and context
// are sourced from the declared family table (catalog.go).
type modelEntry struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	OwnedBy string `json:"owned_by"`
}

// balanceResponse is GET /user/balance (verified 2026-07-04), the only documented
// account-level DeepSeek surface. Balance amounts are decimal STRINGS and stay hashed
// into DetailHash only, never surfaced in titles.
type balanceResponse struct {
	IsAvailable  bool          `json:"is_available"`
	BalanceInfos []balanceInfo `json:"balance_infos"`
}

// balanceInfo is one currency bucket from GET /user/balance. Decimal amounts remain
// strings to avoid float precision loss and to preserve minimal-data hashing.
type balanceInfo struct {
	Currency        string `json:"currency"`
	TotalBalance    string `json:"total_balance"`
	GrantedBalance  string `json:"granted_balance"`
	ToppedUpBalance string `json:"topped_up_balance"`
}
