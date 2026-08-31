<!-- SPDX-FileCopyrightText: 2026 Olivares.AI -->
<!-- SPDX-License-Identifier: AGPL-3.0-only -->

# You already have an AI gateway — Olivares governs it

Olivares is **not** an AI gateway. It does not route traffic between models, load-balance,
fail over, or sit on the inference hot path as a proxy (that decision was settled early — the
inference PEP is a governance point, not a router). If you already run Envoy AI Gateway, Kong AI
Gateway, LiteLLM, or Cloudflare AI Gateway, you keep them. Olivares treats each as a **surface to
govern**: it ingests the gateway's declared configuration and its usage, correlates the traffic and
cost with the identities in your estate, and reports where the gateway's own policy **drifts** from
the policy you declared in Olivares.

The value lives *above the wire*: an identity-bound access map and an offline-verifiable audit
ledger that span every gateway at once, rather than one request-scoped view per gateway.

## What each connector ingests (read-only, minimal-data)

Every connector below reads an **operator-exported artifact** — it never calls the gateway's API,
never opens a listener, never proxies a request, and never reads a prompt, a completion, or a
secret. It imports no engine code (Apache-2.0 boundary).

| Connector | Reads | Emits | Complements |
|---|---|---|---|
| `envoy-ai-gateway` | Applied Envoy AI Gateway CRDs (`AIGatewayRoute`, `AIServiceBackend`, `BackendSecurityPolicy`, `MCPRoute`, `QuotaPolicy`) exported as JSON/YAML | Unauthenticated backend (no `BackendSecurityPolicy`), MCP passthrough with no `securityPolicy`/`toolSelector`, model-access drift, no-cost/no-quota FinOps blind spot; route→backend edges | `ai-gateway` (usage/cost), `envoy` (L7 mesh) |
| `kong-agent-gateway` | Kong config (decK export or Admin-API entity JSON): `ai-proxy(-advanced)`, `ai-rate-limiting-advanced`, `ai-mcp-proxy`, `ai-prompt-guard`, `ai-sanitizer` per route/service | Uncapped AI proxy (no rate limiting), ungoverned MCP (no guard/sanitizer), model-access drift, disabled guard; scope→model edges | `kong-audit` (Admin-API audit stream) |
| `litellm` | Exported LiteLLM key/team/user snapshot (`/key/list`, `/team/list`, `/user/list`) | Unbounded budget, **budget drift** vs the Olivares-declared budget (two sources of truth), model-access drift, unattributed key, retained blocked key; owner-identity→virtual-key edges. **No `CostSample`** — spend is read only to compare budgets, so gateway-routed cost is never double-counted | the provider connectors (DeepSeek, OpenRouter, …) |
| `cloudflare-ai-gateway` | Cloudflare AI Gateway usage/cost export | Per-request FinOps + shadow-MCP posture | — |

## The drift signal

Two systems declaring the same policy — "which models may this identity reach", "what is this
identity's budget" — is two sources of truth, and two sources of truth drift. The gateway connectors
take **your** declared allowlist/budget (`approved_models`, `declared_budgets`) and diff it against
what the gateway is *actually configured to allow*. A model reachable through the gateway but outside
your allowlist, or a LiteLLM budget that contradicts the one you declared, is surfaced as a finding —
not as a claim that the gateway is wrong, but as a reconciliation point you own.

No FUD: these gateways are good tools and Olivares complements them. It is the layer that asks, across
all of them at once, *"what can the whole estate actually reach, and is that what we intended?"*
