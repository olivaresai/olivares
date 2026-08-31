// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// Package orchestration is module IV — communication & orchestration between
// agents (README.md §IV). It is the OBSERVE-AND-GOVERN plane for how agents
// coordinate: it does NOT reimplement an agent framework (LangGraph/CrewAI/
// AutoGen), it does not run an agent, and it never spawns a process.
//
// What it does:
//
//   - Derives the live COMMUNICATION & DELEGATION graph (who delegates to whom —
//     supervisor→worker, swarms — and who talks to whom) from the only signal
//     that exists on the bus today: edge.observed. Claude Code's Task tool emits
//     a session→agent.task delegation edge; MCP topology emits session→mcp.server/
//     mcp.tool edges. The graph is a DERIVED view (sibling to the access-map R/RW
//     graph and the live operation view), not a re-ingested copy.
//
//   - Governs SCHEDULED / AUTONOMOUS agents (cron / event-driven / trigger). A
//     schedule is a governed DESIRED-STATE declaration. FIRING one is a privileged,
//     production-affecting action: it is two-phase and HITL-gated (the
//     ApprovalGate seam, deny-closed), bound to a plan_hash (anti-TOCTOU), audited
//     to the REAL principal, and recorded to an APPEND-ONLY decision ledger. The
//     module never parses a cron expression to self-fire and never actuates: the
//     act of running an agent leaves through the deny-closed Dispatcher seam (a
//     future adapter to deploy / the core runtime). Absent a dispatcher the
//     safe state is "declared, not fired".
//
//   - Flags ANTI-EVASION (docs/SECURITY-HARDENING.md): a scheduled, active, recurring agent that
//     stops emitting versus its declared cadence is a SIGNAL → an
//     orchestration_cadence_miss Finding. A one-shot/event-driven/paused schedule
//     that simply finished is normal silence and emits nothing. The check is
//     read-time and event-driven over the request's pinned tenant only — a module
//     cannot enumerate tenants, so it never runs a cross-tenant background scan.
//
// RED LINE — minimal data (docs/SECURITY-HARDENING.md). This module persists communication
// RELATIONS and metadata (who↔who, counts, timing, redacted refs) and governance
// evidence — NEVER the A2A message payloads, prompts, tool arguments, or any
// secret. No such column exists. Sensitive refs are hashed in code with hashHex
// BEFORE persistence: FieldSpec.Redact is validated only for field kind by the
// engine (it is not enforced on the write path), so it is documentation here, not
// a guarantee — the hashing is the guarantee.
//
// HONEST COVERAGE. There is no A2A protocol connector yet (connectors are Fase B,
// closed). The graph therefore covers Claude Code Task delegation + MCP topology
// only; peer-to-peer A2A, swarm cross-talk and non-Task frameworks are ABSENT,
// not zero. Every /graph response carries a coverage descriptor that says so; the
// module never presents the graph as complete agent communications.
//
// COMPOSITION ROOT (wired on-demand at boot; see the dated note below): the boot wires the real
// ApprovalGate and the real Dispatcher (deploy / core runtime) via the
// WithApprovalGate / WithDispatcher options. Until then the deny-closed defaults
// keep every privileged action safe, and Start() warns once per un-wired seam.
//
// Update 2026-06-07 (Fase K): the composition root now wires both —
// the ApprovalGate via the OUTBOUND HITL bridge and the Dispatcher
// (cmd/olivares/wire.go). The runtime *fire* route REUSES the same
// executor.Executor the deploy module acts through (shared, never re-implemented);
// the A2A route uses connectors/a2a. The deny-closed defaults above are PRESERVED:
// without OLIVARES_APPROVAL_BRIDGE_CONFIG / OLIVARES_ORCH_DISPATCH_CONFIG each seam
// keeps its deny-closed default and an approved fire is honestly "declared, not
// fired". This paragraph is kept as design history.
package orchestration
