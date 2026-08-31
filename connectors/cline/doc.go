// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Package cline is the Olivares AI governance connector for Cline
// (github.com/cline/cline) and Kilo Code (github.com/kilocode/kilo-code) — VSCode
// extensions that provide AI-powered coding assistance with tool use and MCP support.
//
// # Integration level: OBSERVE + CONFIG-WRITE (advisory)
//
// This connector reads VSCode settings.json files (user and workspace layers) for the
// cline.* or kilocode.* namespace (read-only, no network, minimal-data) and emits
// governance observations. It also provides a policy AUTHORING surface (authoring.go)
// that generates a VSCode settings.json fragment from governance rules.
//
// It is NOT inline deny-closed governance: Cline/Kilo Code has no hook-based PEP,
// no admin settings tier, and no system-level override mechanism.
//
// # One connector, two variants
//
// Cline and Kilo Code share the same architecture (Kilo Code is a fork). This connector
// handles both via the "variant" config field: "cline" reads cline.* keys, "kilocode"
// reads kilocode.* keys. The governance model is identical.
//
// # What this connector does
//
//   - OBSERVE: reads VSCode settings to emit posture findings on auto-approve,
//     MCP server allowlists, credential exposure, model pinning, custom instructions
//   - PERMITTED edges: configured MCP servers and allowed tools
//   - INVENTORY: effective-config finding with variant/provider/model/MCP state
//   - COVERAGE: always Medium — Cline has no native OTEL gen_ai.* instrumentation
//   - CONFIG-WRITE: Render generates a settings.json fragment from a governance Policy
//
// # What Cline/Kilo Code supports (verified jun-2026)
//
//   - VSCode settings.json: configuration under cline.*/kilocode.* namespace
//   - Workspace + user layer precedence (standard VSCode: workspace wins)
//   - MCP servers: configurable per-workspace or per-user
//   - Auto-approve: per-operation approval lists, read-only/write flags
//   - API key: can be set in settings (credential exposure risk)
//   - Custom instructions: custom system prompt injection
//   - Tool allowlist: configurable allowed tools list
//
// # Limitations (honest)
//
//   - NO inline PEP: Cline/Kilo Code has no hook mechanism for tool-call interception.
//   - NO admin settings tier: VSCode settings are user-controlled. The generated
//     settings can be edited directly by the user or overridden per-workspace.
//   - NO OTEL: Cline has no native OTEL gen_ai.* instrumentation at all.
//     Observability requires a proxy/wrapper or MCP gateway for gen_ai.* spans.
//   - READ-ONLY + CONFIG-WRITE: the connector reads settings and generates config;
//     it does not enforce compliance at runtime.
//   - NO billing API: Cline tracks cost per-session in its UI but does not expose
//     a programmatic cost/usage API.
//
// # Seams for future integration
//
//   - If Kilo Code adds hook-based tool interception, wire to engine PEP.
//   - If Cline/Kilo Code adds OTEL instrumentation, update coverage assessment.
//   - If Cline/Kilo Code exposes a session/cost API, add cost metering.
//
// The connector imports only the SDK and connectors/internal, never the engine (/core).
package cline
