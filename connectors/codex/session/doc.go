// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Package session is the LIVE-SESSION half of the Codex integration (SG-01-Codex).
//
// It is deliberately a different package from its parent connectors/codex, because they
// are different things and conflating them is how "Codex is integrated" became a claim
// nobody could check:
//
//   - connectors/codex is an ENTERPRISE BATCH source: four read-only organization APIs
//     (Analytics, Compliance Logs, Audit Logs, Costs) pulled in 24h windows. It never
//     sets a SessionRef, so nothing it emits can reach the live-session view.
//   - this package is a RECEIVER: it takes one Codex hook call at a time, at the moment
//     of the decision, resolves it to a canonical session identity, and emits per-tool-call
//     observations that DO carry a SessionRef.
//
// They never carry the same fact. One is an org aggregate over a day; the other is one
// tool call with its own tool_use_id.
//
// # Why the hook channel and not the rollout
//
// Measured on codex-cli 0.145.0 (an internal design note (not shipped)),
// with a control run in both directions:
//
//   - --ephemeral turns the rollout JSONL off entirely (control: +1 file without the flag,
//     0 with it) and does NOT affect hooks (6 events either way). A signal the observed
//     party disables with a flag is a posture, not a control.
//   - the hook payload carries session_id AND tool_use_id, so identity and the precise
//     correlation join both come for free. No FIFO pairing is designed, because none is
//     needed.
//   - the hook is the only one of the three surfaces that can IMPEND, not merely watch.
//
// # The license boundary is the mechanism, not an aspiration
//
// This package is Apache-2.0 and may not import /core (scripts/check-boundary.sh fails the
// build otherwise). That is exactly why it cannot write to the ledger, and that is exactly
// what makes the R-01 rule ("the ledger entry is written by ONE party") enforceable rather
// than a convention: the governed decider in cmd/olivares owns every anchor, this package
// owns the wire protocol and the deny-closed defaults, and no amount of future carelessness
// here can produce a second entry.
//
// # What this package will NOT see
//
// Declared, because a governance surface that hides its blind spots is worse than none:
// any Codex process whose host has no hooks.json installed; any process whose hook is not
// in trust (MEASURED: zero events fire, and NOTHING says so); the experimental app-server /
// remote-control / exec-server surfaces (not measured); and the tool paths that Codex's own
// documentation says can opt out of the hook path.
package session
