// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Package kongagw is the Olivares AI governance connector for the Kong AI Gateway
// CONFIG POSTURE — a read-only, minimal-data source that turns Kong's declared AI
// configuration into governed posture and policy drift. It is the sibling of, and
// distinct from, connectors/kong-audit: that connector ingests Kong's Admin API
// AUDIT stream (who changed config, when); this one reads the DECLARED CONFIG
// itself (which AI plugins guard which route) and answers "is the AI data path
// configured safely, and does its policy agree with ours?".
//
// Olivares is NOT a gateway (product doctrine): the customer's Kong AI
// Gateway is a SURFACE to govern. The differentiation lives above the wire.
//
// # What it reads (read-only, never the Admin API)
//
// An operator-exported snapshot of Kong's config, as a file or a directory of
// *.json / *.yaml / *.yml. Two shapes are accepted (both decode through one path):
//   - a decK declarative config (services/routes/plugins/consumers, plugins may be
//     nested under a service or route), e.g. `deck gateway dump -o kong.yaml`;
//   - the Admin API entity JSON (a top-level object with services[], routes[],
//     plugins[], consumers[] arrays; plugins carry {route,service,consumer} refs).
//
// It never calls the Kong Admin API, never opens a listener, never mutates config,
// and never reads a prompt, a completion, or a credential value.
//
// # Verified schema (anti-fabrication) — Kong AI Gateway 3.14
//
// Plugin/entity field names verified against developer.konghq.com 2026-07-12:
//   - generic plugin: {name, enabled, config, route, service, consumer}.
//   - ai-proxy / ai-proxy-advanced: config.model.{name,provider}, and (advanced)
//     config.targets[].model.{name,provider}, config.vectordb, config.embeddings.
//   - ai-rate-limiting-advanced: config.llm_providers[]{name, limit, window_size},
//     config.window_type, config.tokens_count_strategy.
//   - ai-mcp-proxy (MCP), ai-prompt-guard / ai-sanitizer (content guards).
//
// Every field is optional (tolerant decode); a renamed field degrades to fewer
// findings, never a fabricated one. A plugin ref may be an object ({id}/{name}) or a
// bare string, or null — all three are handled.
//
// # What it governs (findings + edges)
//
//   - A scope (route/service/global) that runs an AI proxy (ai-proxy /
//     ai-proxy-advanced) with NO ai-rate-limiting-advanced covering it → Medium: an
//     uncapped AI data path (no token/request rate limit).
//   - A scope that runs ai-mcp-proxy with NO ai-prompt-guard / ai-sanitizer → Medium:
//     ungoverned MCP tool traffic (no prompt guard, no DLP sanitizer).
//   - A proxied model (config.model / config.targets[].model) outside the operator's
//     declared model-access allowlist → High drift (only when approved_models is set).
//   - A disabled AI plugin (enabled=false) → Low: a guard that is present but off.
//   - Edges scope→model (SignalConfig), so the estate map sees which Kong route can
//     reach which provider/model.
//
// Minimal data: only plugin names, the enabled flag, model/provider labels and the
// scope identity are read — never a credential; a negative test asserts an embedded
// secret never reaches an observation. It imports only the SDK and
// connectors/internal — never the engine (/core).
package kongagw
