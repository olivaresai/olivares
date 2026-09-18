// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// WHAT THE WORK RAIL IS SORTED BY — pure, so every rule below can be driven
// directly and none of them can drift into a component.
//
// The reference's rail groups chat threads by what the PERSON has to do with them.
// Ours groups sessions the same way, and that is the only thing taken: the thread model
// is not ours and the sections are recomputed from facts the engine sent, never from a
// label anybody typed.
//
// ⛔ EVERY MEMBERSHIP RULE IS A FIELD THE ENGINE SENDS. There is no heuristic here and
//    no clock arithmetic: `cc_state` is DERIVED BY THE BACKEND at read time
//    (`features/sessions/types.ts`: *"Never fabricate or infer it on the client"*), the
//    run states are the runtime's stored lifecycle, `unclaimed` is the connector's own
//    discrepancy signal and `provider_auth_state` is what the PROVIDER reported. A rail
//    that decided "this one needs you" from elapsed time would be inventing the one
//    thing the operator is reading it for.
//
// ⛔ AND "WAITING FOR YOU" IS NOT "SOMETHING IS WRONG". A failed run and a provider
//    asking to be logged in are both work FOR A PERSON; a session that went quiet
//    within tolerance is not. Collapsing the two would make the group the operator is
//    supposed to trust the one they learn to ignore.
import type { RunDTO } from '@/features/agentops/types'
import { isLiveRun, type UnifiedSession } from './provenance'

/** The three sections of the rail, in the order they are rendered. */
export const WORK_GROUPS = ['active', 'attention', 'settled'] as const
export type WorkGroupId = (typeof WORK_GROUPS)[number]

/** One rendered section: its id and the rows in it, already ordered. */
export interface WorkGroup {
  id: WorkGroupId
  sessions: UnifiedSession[]
}

/** A run is asking for a person: it failed, or its provider says it needs a login. */
function runWantsAPerson(run: RunDTO): boolean {
  return run.state === 'failed' || run.provider_auth_state === 'required'
}

/**
 * Which section a session belongs to. Checked in THIS order, because a session can be
 * both working and asking — and when it is, the operator needs to see the asking.
 */
export function groupOf(s: UnifiedSession): WorkGroupId {
  if (
    s.live?.cc_state === 'silent_evasion' ||
    s.live?.unclaimed === true ||
    s.runs.some(runWantsAPerson)
  )
    return 'attention'
  if (s.live?.cc_state === 'active' || s.runs.some(isLiveRun)) return 'active'
  return 'settled'
}

/**
 * The rail, in reading order.
 *
 * PINNED FIRST, WITHIN THE SECTION AND NOT ABOVE IT. A pin that lifted a settled
 * session over the ones asking for attention would quietly turn a bookmark into a
 * priority — the operator pinned it to keep it findable, not to say it matters more
 * than a failed run. Inside a section the order is the one the join already produced:
 * most recent activity first.
 *
 * Sections with no rows are RETURNED, empty. The rail renders each one's own empty
 * sentence, and a section that vanished when it emptied would make the rail change
 * shape under the operator every time a session settled.
 */
export function groupSessions(
  sessions: readonly UnifiedSession[],
  pinned: ReadonlySet<string> = new Set(),
): WorkGroup[] {
  const byGroup = new Map<WorkGroupId, UnifiedSession[]>(
    WORK_GROUPS.map((id) => [id, []]),
  )
  for (const s of sessions) byGroup.get(groupOf(s))?.push(s)
  return WORK_GROUPS.map((id) => ({
    id,
    // A STABLE sort, so the incoming order (most recent activity first) survives it.
    sessions: [...(byGroup.get(id) ?? [])].sort(
      (a, b) => Number(pinned.has(b.key)) - Number(pinned.has(a.key)),
    ),
  }))
}

/** Every row in rail order, which is what an arrow key walks. */
export function railOrder(groups: readonly WorkGroup[]): UnifiedSession[] {
  return groups.flatMap((g) => g.sessions)
}
