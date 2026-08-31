// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Package openhands is the Olivares AI governance connector for OpenHands
// (github.com/All-Hands-AI/OpenHands, formerly OpenDevin) — an open-source AI software
// engineer that runs in a sandboxed Docker/E2B container with LLM-driven code generation.
//
// # Integration level: OBSERVE + CONFIG-WRITE (advisory)
//
// This connector reads the LOCAL config.toml + environment variable overrides (read-only,
// no network, minimal-data — docs/SECURITY-HARDENING.md-3) and emits governance observations. It also
// provides a policy AUTHORING surface (authoring.go) that generates a config.toml
// fragment from governance rules for the operator to distribute.
//
// It is NOT inline deny-closed governance: OpenHands has no hook-based PEP equivalent
// to Claude Code's managed-settings + PreToolUse hooks. The generated config.toml is
// ADVISORY — the user can override it with environment variables or a local config.
//
// # What this connector does
//
//   - OBSERVE: reads config.toml and environment overrides to emit posture findings
//     on sandbox type, model pinning, credential exposure, telemetry, iteration limits
//   - PERMITTED edges: configured MCP servers and action plugins
//   - INVENTORY: effective-config finding with model/sandbox/OTEL/MCP state
//   - COVERAGE: whether live OTEL gen_ai.* activity reaches the control-plane collector
//   - COST: Meter function prices token usage from OTEL gen_ai.usage.* attributes
//     against declared Claude list pricing (provenance=estimated, CostType="openhands")
//   - CONFIG-WRITE: Render generates a config.toml fragment from a governance Policy
//
// # What OpenHands supports (verified jun-2026)
//
//   - OTEL gen_ai.* semconv spans: OpenHands has the best OSS OTEL gen_ai.* story;
//     it emits vendor-neutral gen_ai.* spans including token usage attributes
//   - config.toml: TOML-based configuration with [llm], [sandbox], [core], [mcp] sections
//   - Environment overrides: LLM_MODEL, LLM_PROVIDER, LLM_API_KEY, SANDBOX_TYPE,
//     MAX_ITERATIONS, OTEL_EXPORTER_OTLP_ENDPOINT override every TOML key
//   - MCP servers: configurable via [mcp.servers] TOML section
//   - Sandbox types: docker, e2b, remote (hardened); others are flagged
//
// # Limitations (honest)
//
//   - NO inline PEP: OpenHands has no PreToolUse/PostToolUse hook mechanism.
//     The connector cannot deny or gate tool calls in real time.
//   - NO managed-settings tier: there is no admin-enforced, user-non-overridable
//     config layer. The generated config.toml can be overridden by the user.
//   - ADVISORY only: the config-write surface generates a recommendation file;
//     it does not enforce compliance. Environment variables override TOML.
//   - NO billing API: cost is estimated from OTEL token counts against declared
//     list pricing. If the operator has negotiated rates, they must override.
//   - OTEL gen_ai.* is opt-in: the operator must configure OTEL_EXPORTER_OTLP_ENDPOINT
//     for live activity to reach the control-plane collector.
//
// # Seams for future integration
//
//   - If OpenHands adds a hook/plugin system for tool-call interception, the connector
//     should wire it to the engine's PEP (like connectors/claude/enforce.go).
//   - If OpenHands adds a managed-config tier, the connector should verify drift
//     (like connectors/managedsettings/verify.go).
//   - If OpenHands publishes a usage/billing API, the Meter function should prefer
//     billed amounts over estimated pricing.
//
// The connector imports only the SDK and connectors/internal, never the engine (/core).
package openhands
