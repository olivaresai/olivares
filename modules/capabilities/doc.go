// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// Package capabilities is module V — visual management of MCP servers, skills,
// plugins/subagents and their capabilities (ARCHITECTURE.md, README.md). It governs
// the tools and capabilities of agents: which MCP server exposes which tool, what
// its transport/scope/configuration is, which agent is wired to which capability,
// its version history and its basic connection health.
//
// It is a MANAGEMENT/GOVERNANCE overlay built ON TOP of the passive discovery of
// module I (inventory) and the introspection of the connectors. It
// does NOT re-implement the MCP client, and it does NOT re-materialize the core
// entities inventory already owns (MCPServer/Skill/Tool/Resource): if it also
// find-or-created them it would race inventory's own materializer — each bus
// subscriber drains its own goroutine (S02 §4), so two concurrent writers to the
// un-uniquely-keyed core tables could duplicate an entity (inventory/entities.go
// documents this single-writer assumption). So this module reads the core
// entities and stores only its OWN overlays, keyed by the connectors'
// already-redacted NATURAL references, resolving them to core entities at read
// time.
//
// Owned entities (capabilities.<entity>):
//
//   - mcp_config: the managed configuration of an MCP server — transport, scope,
//     endpoint REFERENCE and SECRET REFERENCES, never secret values (docs/SECURITY-HARDENING.md).
//     There is no column that can hold a usable credential.
//   - config_revision: an append-only snapshot per config version — the version
//     history, immutable by construction (docs/SECURITY-HARDENING.md).
//   - wiring: the capability-connection graph "what is connected to whom" — an
//     origin (session/agent/mcp_server) → capability (mcp_server/tool/skill/
//     resource) edge. This is DISTINCT from module III's R/RW access graph:
//     that records access to a RESOURCE and whether it is read or written; this
//     records which CAPABILITY an agent is connected to. It stores natural refs,
//     never core entity ids, so it never races inventory's create.
//   - health: the basic connection-health overlay of a capability, fed by the
//     connectors' health FindingReports (§3). The formal health/SLA/
//     uptime module is here it is just the last connection signal.
//
// UNTRUSTED annotations (ARCHITECTURE.md, docs/SECURITY-HARDENING.md): a tool's readOnlyHint/
// destructiveHint are a DECLARED hint from the server, which the MCP spec says
// clients MUST treat as untrusted. This module surfaces them as a signal flagged
// untrusted, never as truth — every tool projection carries that flag explicitly.
//
// Privileged & audited (docs/SECURITY-HARDENING.md): reading the catalog is RBAC-gated; changing
// an MCP server's configuration (and the secrets it references) is a privileged
// change recorded in the append-only, hash-chained ledger attributed to the real
// principal — exactly as the core entity handlers and module X's key governance do.
//
// Layout: capabilities.go (lifecycle, bus, dispatch) · schema.go (owned entities)
// · reactor.go (edge→wiring, finding→health) · servers.go + wiring.go (the live
// catalog reads) · config.go (the audited config + versioning) · dto.go + api.go
// (HTTP + the UI data contract).
package capabilities
