// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// Package hookpep is the GOVERNED Claude Code hooks PEP: it binds the connector's
// protocol shell (connectors/claude.HookPEP) to the AGPL plane the connector may not
// import — the live PDP (governance Cedar/ABAC), the firm-identity plane (via the
// authenticated principal), the ApprovalGate→HITL bridge, and the tamper-evident
// ledger. It is the sibling of the inline MCP PEP (cmd/olivares/mcpgateway.go):
// same pattern — connector owns the protocol + deny-closed defaults, this package owns
// the governed decision. The composition root only loads the operator config, sets the
// engine's planes on a Decider and binds the listener.
//
// Transport (by design): HTTP local to the engine on a
// dedicated loopback socket, exactly like the HITL receiver and the agent gateway.
// The managed hook command POSTs the (already-redacted) tool-call to this endpoint; the
// endpoint runs the governed decision SERVER-SIDE and returns the Claude Code
// hookSpecificOutput the agent enforces. Rationale: coherent with server-side
// PEP, keeps /core out of the connector, and keeps the agent isolated from engine
// internals (it sees only a localhost decision endpoint). No genuinely-open design
// question remained, so per the prompt we follow the pattern.
//
// Mounted for every engine on its own ephemeral loopback socket.
// Unconfigured tenants use allow with audit.
package hookpep
