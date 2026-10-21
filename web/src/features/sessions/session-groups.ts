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
// ⛔ AND "NEEDS YOU" IS NOT "SOMETHING IS WRONG". A failed run and a provider
//    asking to be logged in are both work FOR A PERSON; a session that went quiet
//    within tolerance is not. Collapsing the two would make the group the operator is
//    supposed to trust the one they learn to ignore.
import type { UnifiedSession } from './provenance'
import { sessionDisplayState } from './session-display-state'

/**
 * The three sections a session falls into by what it is doing, in the order they are
 * rendered: what asks for the person (Needs you), what is working, what is idle.
 */
export const WORK_GROUPS = ['attention', 'active', 'settled'] as const
export type WorkGroupId = (typeof WORK_GROUPS)[number]

/** The rail's sections: the person's own pinned sessions first, then the three above. */
export type RailGroupId = 'pinned' | WorkGroupId

/** One rendered section: its id and the rows in it, already ordered. */
export interface WorkGroup {
  id: RailGroupId
  sessions: UnifiedSession[]
}

/**
 * Which section a session belongs to. Checked in THIS order, because a session can be
 * both working and asking — and when it is, the operator needs to see the asking.
 */
export function groupOf(s: UnifiedSession): WorkGroupId {
  return sessionDisplayState(s).group
}

/**
 * The page size of each half (observed rows, runs) that the Sessions view reads and
 * the dashboard's Active agents card joins, so the card counts over the rows the list
 * lists. A page with more behind it is flagged on both (`has_more`), never silently
 * cut at a size one of them chose alone.
 */
export const WORKING_SET_PAGE_LIMIT = 200

/**
 * How many sessions are Working. The ONE figure the Sessions header ("N working") and
 * the dashboard's Active agents card both print, so the card and the list cannot
 * disagree: it is the size of the Working section, and nothing else.
 */
export function workingCount(sessions: readonly UnifiedSession[]): number {
  return sessions.filter((s) => groupOf(s) === 'active').length
}

/**
 * The rail, in reading order.
 *
 * PINNED FIRST, AND ABOVE EVERY SECTION. A person pins a session so it is always the
 * first thing in their list, whatever else is going on (#2130), so the pinned rows are
 * their own section at the top, in the order they were pinned (the set's own order, the
 * engine's `pinned_at`). A pin does not hide what a row needs: the row keeps its state
 * badge, and an unpinned session that asks for attention is still the first of the
 * sections below. The other three keep the order the join produced, most recent
 * activity first, and a session is in exactly one section.
 *
 * Sections with no rows are RETURNED, empty. The rail draws only the ones that have rows,
 * so nothing is drawn for "Pinned" until something is pinned.
 */
export function groupSessions(
  sessions: readonly UnifiedSession[],
  pinned: ReadonlySet<string> = new Set(),
): WorkGroup[] {
  const pinOrder = new Map([...pinned].map((key, index) => [key, index]))
  const pinnedRows: UnifiedSession[] = []
  const byGroup = new Map<WorkGroupId, UnifiedSession[]>(
    WORK_GROUPS.map((id) => [id, []]),
  )
  for (const s of sessions) {
    if (pinned.has(s.key)) pinnedRows.push(s)
    else byGroup.get(groupOf(s))?.push(s)
  }
  const rank = (s: UnifiedSession) => pinOrder.get(s.key) ?? 0
  return [
    // A STABLE sort: two rows of one rank keep the incoming order.
    { id: 'pinned', sessions: pinnedRows.sort((a, b) => rank(a) - rank(b)) },
    ...WORK_GROUPS.map((id) => ({ id, sessions: byGroup.get(id) ?? [] })),
  ]
}

/** Every row in rail order, which is what an arrow key walks. */
export function railOrder(groups: readonly WorkGroup[]): UnifiedSession[] {
  return groups.flatMap((g) => g.sessions)
}
