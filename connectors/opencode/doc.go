// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Package opencode is the Olivares AI governance connector for opencode
// (github.com/sst/opencode), an open-source terminal AI coding agent.
//
// # Integration level: OBSERVE + GOVERN + CONFIG-WRITE
//
// This connector reads local opencode configuration files (opencode.json,
// opencode.jsonc, or legacy config.json paths supplied by the operator) as JSONC:
// line comments and trailing commas are tolerated, matching the published
// opencode JSON Schema's allowComments / allowTrailingCommas settings. The
// connector is read-only at gather time, uses bounded file reads, performs no
// network calls, and imports only the connector SDK plus connector-local helpers.
//
// Values such as {env:VAR} and {file:path} are treated as literal unresolved
// tokens. The connector never resolves environment references, follows file
// references, reads prompts, or emits raw credential material.
//
// opencode has a managed configuration layer unlike Goose, Cline, and OpenHands.
// The connector detects local managed files in the OS-specific managed directory
// (/etc/opencode on Linux, /Library/Application Support/opencode on macOS, and
// %ProgramData%\opencode on Windows) and reports whether that admin layer appears
// present. The report is deliberately qualified: opencode's managed config is a
// per-key last-writer-wins deep merge, not an immutable lock mechanism;
// OPENCODE_PERMISSION can override managed permission at runtime;
// OPENCODE_TEST_MANAGED_CONFIG_DIR can redirect the managed directory; and remote
// organization / Console config cannot be seen by this local-file reader.
//
// The package also provides an authoring surface that renders a managed-dir
// deployable opencode.json fragment for hardening permission, MCP servers, share
// behavior, and native OpenTelemetry.
package opencode
