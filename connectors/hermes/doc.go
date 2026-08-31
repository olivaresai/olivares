// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Package hermes is the Olivares AI governance connector for Hermes Agent
// (github.com/NousResearch/hermes-agent, MIT), a self-hosted personal assistant
// agent with messaging gateways, skills, MCP servers, model routing and host-side
// code execution.
//
// # Status: pinned local governance surface
//
// The schema is pinned to the self-hosted agents-surface contract
// (2026-07-04 snapshot, Hermes Agent v0.18.0). The connector reads the local
// $HERMES_HOME state tree (default ~/.hermes), config.yaml, .env key names, and
// the managed scope at /etc/hermes or $HERMES_MANAGED_DIR. Managed config is
// leaf-merged over user config before evaluation.
//
// # Integration level: OBSERVE + COST METERING (advisory)
//
// This connector is read-only and minimal-data. It emits posture findings from
// the catalog, config-declared channel/skill/model/MCP edges, an inventory
// finding, and a coverage finding for the opt-in Langfuse plugin. It also exports
// a Meter helper for pricing wrapped model calls from declared list pricing when
// the provider is Anthropic.
//
// It is NOT inline deny-closed governance: no upstream hook mechanism was verified
// for tool-call interception. Coverage is config-only; endpoint reachability and
// live Langfuse ingestion are not probed. Hermes has no native OTEL exporter.
//
// # What this connector does
//
//   - DISCOVER: $HERMES_HOME, ~/.hermes and ~/.hermes/profiles/* installs
//   - RESOLVE: config.yaml with managed-scope leaf overrides from /etc/hermes
//   - OBSERVE: terminal isolation, approvals, skills, channel authorization,
//     dashboard/API exposure, credentials, managed-scope presence and migration state
//   - EDGES: channels from platforms.* and documented credential env-key presence;
//     skills from state/config; models from model/fallback/custom providers; MCP servers
//   - INVENTORY: version, backend, approval mode, counts, managed scope, AGENTS.md,
//     memories and OpenClaw migration presence
//   - COVERAGE: Langfuse plugin key presence; local trajectory logs are noted, but
//     native OTEL is not claimed
//   - COST: Meter function emits estimated samples, with zero-cost ok=false for
//     non-Anthropic providers because no non-Claude pricing table is declared here
//
// # Limitations (honest)
//
//   - NO inline PEP: Hermes has no verified hook for tool-call interception.
//   - CONFIG-ONLY coverage: the connector does not connect to Langfuse or Hermes.
//   - NO native OTEL: OTEL_EXPORTER_OTLP_ENDPOINT is not a Hermes coverage signal.
//   - NO billing API: cost is estimated from declared pricing when available.
//   - TRUST MODEL: upstream SECURITY.md states, "The only security boundary against
//     an adversarial LLM is the operating system."
//
// The connector imports only the SDK and connectors/internal, never the engine (/core).
package hermes
