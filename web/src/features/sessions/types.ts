// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// DTOs for the live-operation view (module II) — a 1:1 mirror of
// modules/sessions/dto.go. A session is a unit of agent work; the module reconstructs
// its live state from the ingest stream and the web RENDERS it (ARCHITECTURE.md) — it never
// computes cost, tokens, or the derived `cc_state`. Minimal-data (docs/SECURITY-HARDENING.md): only
// references, classifications and counters cross the wire — never SQL, payloads,
// secrets or PII. `goal`/`summary` are optional and frequently ABSENT (carries no
// objective channel yet); when missing the view shows an honest placeholder, never a
// fabricated goal.

/**
 * The session control-plane state, DERIVED by the backend at read time. Never
 * fabricate or infer it on the client — it is the engine's signal:
 *  - `active`           working now (events within cadence)
 *  - `idle`            quiet but within tolerance
 *  - `ended`           finished/closed
 *  - `silent_evasion`  gone silent INSIDE its expected cadence — a possible-evasion
 *                      signal worth the operator's eye (docs/SECURITY-HARDENING.md), not a UI error.
 */
export type CcState =
  'active' | 'idle' | 'ended' | 'silent_evasion' | (string & {})

/** One live session snapshot (GET /live, /live/{ref}, and the /stream frames). */
export interface LiveDTO {
  session_ref: string
  agent_ref?: string
  cc_state: CcState
  current_action?: string
  current_resource?: string
  current_mode?: string
  model_ref?: string
  input_tokens: number
  output_tokens: number
  cost_micro_usd: number
  event_count: number
  tool_call_count: number
  first_event_at: string
  last_event_at: string
  duration_seconds: number
  /** The session objective — often ABSENT (no objective channel in).*/
  goal?: string
  /** A short running summary — often ABSENT (no summary channel in).*/
  summary?: string

  // The three fields below were already on the wire (modules/sessions/dto.go:44-56)
  // and the console did not read ANY of them until. They are the observed half
  // of "what can I actually do with this session", so a card that omits them can only
  // describe a session by its telemetry.
  /** Activity seen from a session holding no live claim (SG-02). Sticky, and
   * deliberately NOT folded into `cc_state`: silent_evasion means the connector caught
   * a discrepancy; unclaimed is a different fact and collapsing them would leave an
   * operator unable to tell which of the two they are looking at. */
  unclaimed?: boolean
  unclaimed_at?: string
  /** The agent engine driving the session ("claude", "codex"). ABSENT when no
   * connector declared one — render nothing, never a default. */
  engine?: string
  /** How firmly the session is governed, as declared by the producing connector:
   * `enforced` (its tool calls are policed in line) or `observed` (they are not). The
   * engine folds the WEAKEST value seen. ABSENT when unknown — and a blank badge tells
   * the truth where "enforced" by default would not. */
  posture?: 'enforced' | 'observed' | (string & {})
}

/** One reconstructible timeline entry (GET /live/{ref}/timeline), chronological. */
export interface TimelineDTO {
  at: string
  /** The entry class: a tool call, an MCP call, a cost event, or a finding. */
  kind: 'tool' | 'mcp' | 'cost' | 'finding' | (string & {})
  tool_ref?: string
  resource_ref?: string
  mode?: string
  source?: string
  title?: string
}
