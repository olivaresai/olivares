// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Package litellm is the Olivares AI governance connector for the LiteLLM proxy —
// a read-only, minimal-data source that governs LiteLLM's virtual keys, teams and
// budgets as a surface. LiteLLM is a popular self-hosted LLM gateway (keys → teams →
// budgets over many providers); Olivares is NOT a gateway (product doctrine),
// so the customer's LiteLLM deployment is a SURFACE to govern, not to replace.
//
// Its distinct value here is IDENTITY CORRELATION and BUDGET DRIFT: it correlates a
// LiteLLM virtual key with the Olivares identity that owns it (an access-map edge),
// and it flags where LiteLLM's own budget for an identity CONTRADICTS the budget
// declared in Olivares — two sources of truth for the same spend cap is drift.
//
// # What it reads (read-only, never the API)
//
// An operator-exported snapshot of LiteLLM's management responses, as a file or a
// directory of *.json / *.jsonl. Accepted shapes (all decode through one path):
//   - a combined object {keys:[...], teams:[...], users:[...]};
//   - a bare array of key objects (e.g. the /key/list "keys" array saved directly);
//   - JSON-lines, one key object per line.
//
// The honest ingest path: the operator dumps /key/list, /team/list, /user/list and
// points "path" at the file(s). The connector never calls the LiteLLM API, never
// opens a listener, and never reads the raw virtual-key secret (only the key_alias,
// or an opaque hash of the token when there is no alias).
//
// # Verified schema (anti-fabrication) — LiteLLM proxy management API
//
// Field names verified against docs.litellm.ai/docs/proxy 2026-07-12: key {key_alias,
// token, spend, max_budget, budget_duration, models, tpm_limit, rpm_limit, team_id,
// user_id, blocked, metadata}; team {team_id, team_alias, spend, max_budget, models};
// user {user_id, spend, max_budget, models}. Every field is optional (tolerant
// decode) — a renamed/absent field degrades to fewer findings, never a fabricated one.
//
// # What it governs (findings + edges) — NO cost double-counting
//
//   - A virtual key / team / user with NO max_budget → Medium: unbounded spend cap.
//   - A key/team/user whose LiteLLM max_budget CONTRADICTS the Olivares-declared
//     budget for that identity → High drift (only when declared_budgets is set).
//   - A key with an empty models list (all-models access), or a model outside the
//     declared model-access allowlist → High drift (only when approved_models is set).
//   - A key with no owner (no user_id, team_id, or alias) → Medium: unattributed key.
//   - A blocked key still present in the export → Info.
//   - Edges owner-identity→virtual-key (SignalConfig).
//
// It NEVER emits a CostSample: `spend` is read only to compare against the declared
// budget, so LiteLLM-routed cost is not double-counted against the provider
// connectors (a DoD constraint). It reads only structural fields and the spend/budget
// numbers — never a prompt, a completion, or the raw key; a negative test asserts an
// embedded secret never reaches an observation. It imports only the SDK and
// connectors/internal — never the engine (/core).
package litellm
