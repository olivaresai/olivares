// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Package goose is the Olivares AI governance connector for Goose by Block
// (github.com/block/goose) — an open-source AI developer agent that runs locally
// with profile-based configuration and extension (MCP server) support.
//
// # Integration level: OBSERVE + CONFIG-WRITE (advisory)
//
// This connector reads the LOCAL profiles.yaml + environment overrides (read-only,
// no network, minimal-data) and emits governance observations. It also provides a
// policy AUTHORING surface (authoring.go) that generates a profiles.yaml document
// from governance rules for the operator to distribute.
//
// It is NOT inline deny-closed governance: Goose has no hook-based PEP, no admin
// settings tier, and no system-level override mechanism.
//
// # What this connector does
//
//   - OBSERVE: reads profiles.yaml to emit posture findings on provider/model pinning,
//     tool approval, extension allowlists, code isolation, telemetry
//   - PERMITTED edges: configured extensions (MCP servers) and allowed tools
//   - INVENTORY: effective-config finding with profile/provider/model/extension state
//   - COVERAGE: whether OTEL endpoint is configured (honest: Goose has limited native
//     gen_ai.* support — the finding documents this blind spot)
//   - CONFIG-WRITE: Render generates a profiles.yaml from a governance Policy
//
// # What Goose supports (verified jun-2026)
//
//   - profiles.yaml: YAML-based profile configuration in ~/.config/goose/
//   - GOOSE_PROFILE env: selects the active profile (default: "default")
//   - Extensions: MCP server integrations (type/command/URL per extension)
//   - Tool approval: toolshim.require_approval + tool allowlist
//   - Limited OTEL: some gen_ai.* support but not the full semconv profile
//
// # Limitations (honest)
//
//   - NO inline PEP: Goose has no hook mechanism for tool-call interception.
//   - NO admin/system settings tier: profiles.yaml is user-controlled. The generated
//     config can be edited directly by the user. There is no enforced override.
//   - NO code sandbox: Goose executes code directly in the user's environment.
//     This is by design (Goose does not provide a sandboxed runtime).
//   - LIMITED OTEL: Goose has limited native gen_ai.* instrumentation; observability
//     may require a proxy/wrapper for full gen_ai.* span generation.
//   - FILE-BASED + POLLING: integration is file-based, not streaming.
//     Session discovery would require polling the Goose sessions directory.
//
// # Seams for future integration
//
//   - If Goose adds a hook/plugin system, wire to engine PEP.
//   - If Goose improves OTEL gen_ai.* instrumentation, update coverage assessment.
//   - If Goose adds an admin/system config tier, verify drift like managedsettings.
//
// The connector imports only the SDK and connectors/internal, never the engine (/core).
package goose
