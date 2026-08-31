// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Package managedsettings governs Claude Code's managed-settings.json — the ONLY
// user-non-overridable policy layer Anthropic ships for Claude Code (CLA-05). A
// control plane that markets "govern Claude" but can neither EMIT nor VERIFY this
// layer is observation-only for the fleet: it cannot actually stop a developer
// from using bypassPermissions, an unapproved MCP server, or an unknown plugin
// marketplace.
//
// This package provides both halves:
//
//   - AUTHORING (render.go): Render turns a governance-authored Policy into the
//     exact managed-settings.json an operator distributes to the OS policy paths
//     (/Library/Application Support/ClaudeCode, /etc/claude-code,
//     C:\Program Files\ClaudeCode). Pure, no I/O.
//
//   - VERIFICATION (source.go + verify.go + dropin.go): a read-only SourceConnector
//     reads the LIVE managed-settings.json on a host — deep-merging the
//     managed-settings.d/ drop-in fragments so it verifies the host's EFFECTIVE
//     policy, not the base file alone — and emits PERMITTED policy edges (the managed
//     grants/constraints feeding module III) plus policy_drift findings when the live
//     config diverges from the governance-authored intent — the PERMITTED-policy vs
//     OBSERVED-config diff. An absent or invalid file is itself a high-severity finding
//     (the host is ungoverned).
//
//   - DISTRIBUTION surfaces (hooksenv.go): the managed `hooks` and `env`
//     blocks. PEPHook renders the PreToolUse Policy-Enforcement-Point hook as a
//     managed (non-overridable) hook — turning "observe" into "govern"; TelemetryEnv
//     renders the sanctioned OTEL observation env (CLAUDE_CODE_ENABLE_TELEMETRY +
//     OTEL_*), the plan-UNGATED way to OBSERVE subscription use without proxying it.
//     Both are drift-checked (an undistributed PEP hook is HIGH) and validated
//     deny-closed against inline credentials in a plaintext managed file. Strengthens
//     the telemetry drift to assert the live managed env not only ENABLES telemetry but
//     points OTEL at the AUTHORED Olivares collector (a divergent endpoint is drift), and
//     adds an authorized-gateway base-URL drift: a live ANTHROPIC_BASE_URL diverging from
//     Policy.AuthorizedGatewayBaseURL is HIGH (a non-default base-URL bypasses
//     server-managed-settings entirely).
//
// The connector is read-only and imports only the SDK (Apache-2.0): it never
// writes to a host and never imports /core or /modules. Distribution of the
// rendered file is a deploy concern (VII), not this connector's.
//
// Key/precedence reference (verified 2026-06-05 against the live docs):
// https://code.claude.com/docs/en/settings · https://code.claude.com/docs/en/permissions
// · https://code.claude.com/docs/en/server-managed-settings
package managedsettings
