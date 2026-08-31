// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// Package sessions is module II of the control plane (README.md): the live
// operation of every agent session — what it is doing right now, its objective,
// its live tokens/cost, its Claude Code state, and a reconstructable timeline.
//
// It is a bus-driven AGPL module, sibling to module I (inventory). Where
// inventory materializes the durable estate, sessions keeps a live operational
// overlay per session, keyed by the session's external reference, in its own
// registered entities (sessions.live, sessions.timeline). It subscribes to the
// same observation stream (edge.observed / cost.sampled / finding.reported) and:
//
//   - tracks the current action (the last tool used), the live token and cost
//     totals (read from cost samples — the canonical CostRecord/FinOps is module
//     XI/here only the live figure is shown), and an activity summary;
//   - derives the Claude Code state from the available signals (active/idle/ended
//     by recency; silent-evasion when the connector raises that finding
//     §2.4) — never fabricated;
//   - appends every event to a per-session timeline that can be replayed; and
//   - streams live updates to API clients over server-sent events, the channel
//     the web renders the live operation from.
//
// Honest limits (docs/SECURITY-HARDENING.md): the cooperative observation stream is minimal-data
// and does NOT carry a session's goal or task list (they are redacted at the
// connector). The live record models those fields so the contract and UI are
// ready and any future metadata channel populates them, but the module never
// invents them — what provides is current action, state, tokens/cost and the
// timeline, and that is what is shown.
package sessions
