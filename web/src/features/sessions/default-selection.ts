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
//   4 · It is only ever the signed-in person's OWN session: every run of it acts for
//       them (`acts_for`, from the run's launcher). An admin may read and steer other
//       people's sessions by choosing their row; a default must never put someone
//       else's live session, with its Stop, Esc and composer armed, in front of them.
//       A session with no launch record (observed only) is nobody's for this rule.
//
// A real selection is never this function's business: the moment an operator opens a
// row, the address is theirs and the view reads that instead.
import type { RunDTO } from '@/features/agentops/types'
import { addressOf } from './session-address'

interface DefaultCandidate {
  key: string
  runs: readonly Pick<RunDTO, 'acts_for'>[]
}

/** Every run of the session acts for this person (rule 4). */
function isOwnSession(s: DefaultCandidate, userId: string): boolean {
  return (
    s.runs.length > 0 &&
    s.runs.every(
      (r) => r.acts_for?.kind === 'user' && r.acts_for.user_id === userId,
    )
  )
}

export interface DefaultSelectionInput {
  /** True while either half of the list is still loading. */
  settling: boolean
  /** The rail's own reading order — needs attention, working, settled. */
  order: readonly DefaultCandidate[]
  /** An address this episode retired, which must never be defaulted to. */
  retiredAddress: string | null
  /** What this surface defaulted to already, if anything. */
  latched: string | null
  /** The signed-in person's account; null while unknown, and then nothing is theirs. */
  userId: string | null
}

export function chooseDefaultAddress({
  settling,
  order,
  retiredAddress,
  latched,
  userId,
}: DefaultSelectionInput): string | null {
  if (settling || userId === null) return null
  const own = order.filter((s) => isOwnSession(s, userId))
  const holds =
    latched !== null &&
    latched !== retiredAddress &&
    own.some((s) => addressOf(s) === latched)
  if (holds) return latched
  const first = own.find((s) => addressOf(s) !== retiredAddress)
  return first ? addressOf(first) : null
}
