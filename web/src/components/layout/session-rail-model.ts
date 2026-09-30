// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { sessionNameLadder } from '@/features/home/work-line'
//
// THE SESSION RAIL, AS DATA: three groups in a fixed order, grouped by what each row needs
// from the operator. Pure, so the grouping is tested without a network or a router.
//
// A handoff offered to the operator waits for an answer, so it is in "Needs you", above
// the sessions that ask. The inbox is served without content, so a handoff row names its
// work item and its sender, and its link opens the handoff where Accept and Reject are.
import type { HandoffInboxItem } from '@/features/communications/types'
import { liveRowKey } from '@/features/sessions/provenance'
import { addressOf, SESSION_PARAM } from '@/features/sessions/session-address'
import type { LiveDTO } from '@/features/sessions/types'

export const RAIL_GROUP_IDS = ['needsYou', 'working', 'earlier'] as const
export type RailGroupId = (typeof RAIL_GROUP_IDS)[number]

/** Rows per group. The sidebar shows about eight rows; the rest are one click away. */
export const RAIL_ROWS_PER_GROUP = 8

/** The state a row's glyph and word say. */
export type RailState = 'need' | 'live' | 'ended' | 'idle'

export interface RailRow {
  key: string
  kind: 'session' | 'handoff'
  state: RailState
  /** The engine's own title for the row, or null when it sent none. */
  title: string | null
  /** A reference the row can always be named by: the session or work item id. */
  reference: string
  /** Engine and agent of a session; null when the engine sent neither. */
  meta: string | null
  /** The sender of a handoff. */
  from?: string
  /** Whole minutes since the row last changed (a running session: since it started). */
  minutes: number
  /** Where the row opens: a registry path and the search it carries. */
  to: '/sessions' | '/communications/handoffs'
  search: Record<string, string>
  /** The same address as one string, for a reader and a test. */
  href: string
}

export interface RailGroup {
  id: RailGroupId
  rows: RailRow[]
}

export interface RailSources {
  live: readonly LiveDTO[]
  handoffs: readonly HandoffInboxItem[]
}

function minutesSince(iso: string | undefined, now: number): number {
  const at = iso ? Date.parse(iso) : Number.NaN
  if (Number.isNaN(at)) return 0
  return Math.max(0, Math.floor((now - at) / 60_000))
}

function sessionState(live: LiveDTO): RailState {
  if (live.cc_state === 'silent_evasion' || live.unclaimed === true)
    return 'need'
  if (live.cc_state === 'active') return 'live'
  if (live.cc_state === 'ended') return 'ended'
  return 'idle'
}

function sessionRow(live: LiveDTO, now: number): RailRow {
  const state = sessionState(live)
  const key = liveRowKey(live)
  const search = { [SESSION_PARAM]: addressOf({ key }) }
  const meta = [live.engine, live.agent_ref ?? live.model_ref]
    .filter((part): part is string => !!part)
    .join(' · ')
  return {
    key,
    kind: 'session',
    state,
    title: sessionNameLadder(null, live, '').text || null,
    reference: live.session_ref,
    meta: meta || null,
    minutes: minutesSince(
      state === 'live' ? live.first_event_at : live.last_event_at,
      now,
    ),
    to: '/sessions',
    search,
    href: `/sessions?${new URLSearchParams(search).toString()}`,
  }
}

function handoffRow(item: HandoffInboxItem, now: number): RailRow {
  const search = { handoff: item.carrier.delivery_id }
  return {
    key: `handoff:${item.carrier.delivery_id}`,
    kind: 'handoff',
    state: 'need',
    title: null,
    reference: item.work_item.id,
    meta: null,
    from: item.handoff.from.ref,
    minutes: minutesSince(item.handoff.created_at, now),
    to: '/communications/handoffs',
    search,
    href: `/communications/handoffs?${new URLSearchParams(search).toString()}`,
  }
}

/** The three groups, always all three and always in this order, so the rail keeps its
 * shape while rows move between them. */
export function railGroups(sources: RailSources, now: number): RailGroup[] {
  const needs: RailRow[] = sources.handoffs
    .filter(
      (h) => h.handoff.state === undefined || h.handoff.state === 'offered',
    )
    .map((h) => handoffRow(h, now))
  const working: RailRow[] = []
  const earlier: RailRow[] = []
  for (const live of sources.live) {
    const row = sessionRow(live, now)
    if (row.state === 'need') needs.push(row)
    else if (row.state === 'live') working.push(row)
    else earlier.push(row)
  }
  const cap = (rows: RailRow[]) => rows.slice(0, RAIL_ROWS_PER_GROUP)
  return [
    { id: 'needsYou', rows: cap(needs) },
    { id: 'working', rows: cap(working) },
    { id: 'earlier', rows: cap(earlier) },
  ]
}
