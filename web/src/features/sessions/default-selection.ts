// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// WHICH SESSION THE WORK SURFACE OPENS ON WHEN THE ADDRESS NAMES NONE.
//
// ⛔ WHY THIS IS A FUNCTION AND NOT FOUR LINES INSIDE THE VIEW. It started as four
//    lines inside the view, and `task -x console:walk` against the seeded engine
//    reported the only finding of its 77 screens on this screen:
//
//        200    /v1/m/sessions/live/sess-coder-9c21
//        FAILED /v1/m/sessions/stream?ref=sess-coder-9c21   net::ERR_ABORTED
//        200    /v1/m/sessions/live/by-id/01a0b46a-b277-…
//
//    The run half of the list answered first, the default resolved to a run-keyed
//    address, the work pane subscribed to that session's stream — and then the observed
//    half arrived, the merged row changed address shape, and the subscription was torn
//    down mid-open.
//
//    The aborted stream is the cheap half of the cost. The expensive half is that the
//    surface MOVES under whoever is reading it: a default that follows the list lets the
//    newest session steal the pane from the one an operator opened the screen for.
//
// THE RULE, therefore, in three clauses that are each a test below:
//
//   1 · Nothing is defaulted while either half of the list is still loading. "The
//       session that most needs a person" cannot be decided from half a list.
//   2 · Once chosen, the default is LATCHED. A list that reorders underneath does not
//       move it.
//   3 · It moves only when its row leaves the list or is retired — and a retired address
//       is never chosen, which is the R2 invariant (a read bit coming back cannot revive
//       a target the operator never re-chose) holding by construction.
//
// A real selection is never this function's business: the moment an operator opens a
// row, the address is theirs and the view reads that instead.
import { addressOf } from './session-address'

export interface DefaultSelectionInput {
  /** True while either half of the list is still loading. */
  settling: boolean
  /** The rail's own reading order — needs attention, working, settled. */
  order: readonly { key: string }[]
  /** An address this episode retired, which must never be defaulted to. */
  retiredAddress: string | null
  /** What this surface defaulted to already, if anything. */
  latched: string | null
}

export function chooseDefaultAddress({
  settling,
  order,
  retiredAddress,
  latched,
}: DefaultSelectionInput): string | null {
  if (settling) return null
  const holds =
    latched !== null &&
    latched !== retiredAddress &&
    order.some((s) => addressOf(s) === latched)
  if (holds) return latched
  const first = order.find((s) => addressOf(s) !== retiredAddress)
  return first ? addressOf(first) : null
}
