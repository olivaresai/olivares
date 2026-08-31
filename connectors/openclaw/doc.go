// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Package openclaw is the Olivares AI governance connector for OpenClaw
// (github.com/openclaw/openclaw, MIT) — a self-hosted personal agent with a gateway,
// multi-channel messaging, skills, plugins and code execution.
//
// # Status: pinned surface
//
// The schema is pinned to the self-hosted agents-surface contract, the
// 2026-07-04 OpenClaw v2026.6.11 surface snapshot; the §7 depth pass (2026-07-12)
// adds MCP parity, the skill supply-chain scanner, a systemd fleet signal and
// per-agent meter attribution. The connector reads ~/.openclaw/openclaw.json JSON5,
// resolves confined $include files, and supports env/default/legacy/profile install
// discovery without importing upstream code.
//
// # Integration level: OBSERVE + COST METERING (advisory)
//
// This connector reads local config/state only and emits governance observations:
// posture findings, config-declared channel/skill/model/MCP edges, per-MCP-server
// posture, a per-skill supply-chain grade (skills.go — SKILL.md posture,
// metadata.openclaw risks, a content digest matched against a signed
// connectors/threatfeed deny-list, drift vs an approved baseline and an authorized
// allowlist), a systemd always-on-service inventory signal, and diagnostics coverage.
// It exports Meter / MeterForAgent for pricing (and attributing) token usage.
//
// # Limitations (honest)
//
//   - NO inline PEP: no verified upstream hook mechanism for tool-call interception.
//   - CONFIG-ONLY coverage: diagnostics.otel.enabled is read, endpoints are not probed.
//   - Single-operator trust model upstream: OpenClaw is not a hostile multi-tenant boundary.
//   - NO billing API: cost is estimated from declared Claude list pricing when known.
//   - Containment is honest posture, not an infallible block; a discovered agent is not
//     auto-promoted to a governed NHI; per-agent FinOps ceilings need a runtime proxy to
//     attribute (see the contract §7.5).
//
// The connector imports only the SDK, connectors/internal and connectors/threatfeed —
// never the engine (/core).
package openclaw
