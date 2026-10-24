// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE SESSION RAIL, AS DATA: three groups in a fixed order, grouped by what each row needs
// from the operator. Pure, so the grouping is tested without a network or a router.
//
// A handoff offered to the operator waits for an answer, so it is in "Needs you", above
// the sessions that ask. The inbox is served without content, so a handoff row names its
// work item and its sender, and its link opens the handoff where Accept and Reject are.
import type { HandoffInboxItem } from '@/features/communications/types'
import {
  mergeSessions,
  primaryRun,
  runAwaitedApproval,
  sessionListName,
  sessionReference,
  sessionShortId,
  sharedNames,
  type UnifiedSession,
} from '@/features/sessions/provenance'
import { addressOf, SESSION_PARAM } from '@/features/sessions/session-address'
import type { LiveDTO } from '@/features/sessions/types'
import type { RunDTO } from '@/features/agentops/types'
import { runFolder } from '@/features/sessions/folder'
import { sessionDisplayState } from '@/features/sessions/session-display-state'
import {
  isArchived,
  isMarkedDone,
  isSnoozed,
} from '@/features/sessions/session-groups'
import type { RailState } from '@/features/sessions/session-display-state'

export const RAIL_GROUP_IDS = [
  'needsYou',
  'working',
  'earlier',
  'snoozed',
  'archived',
] as const
export type RailGroupId = (typeof RAIL_GROUP_IDS)[number]

/** Rows per group. The sidebar shows about eight rows; the rest are one click away. */
export const RAIL_ROWS_PER_GROUP = 8

/** The state a row's glyph and word say. */
export type { RailState } from '@/features/sessions/session-display-state'

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
  /** Where the row opens: a registry path and the search it carries. A session waiting
   * on an approval opens the request itself. */
  to: '/sessions' | '/agentops' | '/communications/handoffs' | '/permissions'
  search: Record<string, string>
  /** The same address as one string, for a reader and a test. */
  href: string
}

export interface RailGroup {
  id: RailGroupId
  /** The first RAIL_ROWS_PER_GROUP rows of the group. */
  rows: RailRow[]
  /** How many rows the group holds before the cap: what a count must say. */
  total: number
  /** Sessions only, excluding offered handoffs, before the display cap. */
  sessionTotal: number
}

export interface RailSources {
  live: readonly LiveDTO[]
  handoffs: readonly HandoffInboxItem[]
  /** The sessions this console's engine operates (launched runs). */
  runs?: readonly RunDTO[]
}

interface RailDestinations {
  sessionTo?: '/sessions' | '/agentops'
  canOpenApprovals?: boolean
}

export function approvalDestination(approval: string) {
  return { to: '/permissions', search: { tab: 'approvals', approval } } as const
}

function minutesSince(iso: string | undefined, now: number): number {
  const at = iso ? Date.parse(iso) : Number.NaN
  if (Number.isNaN(at)) return 0
  return Math.max(0, Math.floor((now - at) / 60_000))
}

/** Distinguish repeated titles with the short session identifier. */
function distinctTitle(
  title: string | null,
  s: UnifiedSession,
  shared: ReadonlySet<string>,
): string | null {
  return title && shared.has(title) ? `${title} · ${sessionShortId(s)}` : title
}

/** The state a session's row says: the one place this file asks the classifier. */
function railState(s: UnifiedSession): RailState {
  return sessionDisplayState(s).rail
}

/** One row per session: a launched run and what was observed of it are one session
 * (`mergeSessions`, the join the Sessions page and Now use), so one launch is one row. */
function sessionRow(
  s: UnifiedSession,
  now: number,
  shared: ReadonlySet<string> = new Set(),
  destinations: RailDestinations = {},
): RailRow {
  const run = primaryRun(s.runs)
  const live = s.live
  const state = railState(s)
  const waitingOn =
    state === 'need' && destinations.canOpenApprovals !== false && run
      ? runAwaitedApproval(run)
      : undefined
  const { to, search }: Pick<RailRow, 'to' | 'search'> = waitingOn
    ? approvalDestination(waitingOn)
    : {
        to: destinations.sessionTo ?? '/sessions',
        search: { [SESSION_PARAM]: addressOf(s) },
      }
  // Tool and folder (Warp concept) for a launched session; engine and agent otherwise.
  const named = sessionListName(s, '')
  const meta = (
    run
      ? [run.provider_driver || 'claude', runFolder(run)]
      : [live?.engine, live?.agent_ref ?? live?.model_ref]
  )
    .filter((part): part is string => !!part)
    .join(' · ')
  const started = run?.started_at ?? run?.created_at ?? live?.first_event_at
  const last = run?.last_activity_at ?? live?.last_event_at ?? run?.created_at
  return {
    key: s.key,
    kind: 'session',
    state,
    title: distinctTitle(named.text || null, s, shared),
    reference: sessionReference(s),
    // A title that IS the tool and folder already says what the caption would.
    meta: named.from === 'place' ? null : meta || null,
    minutes: minutesSince(state === 'live' ? started : last, now),
    to,
    search,
    href: `${to}?${new URLSearchParams(search).toString()}`,
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

/** The groups, always all of them and always in this order, so the rail keeps its
 * shape while rows move between them: it draws Needs you always, at the top, and Working
 * and Earlier when they hold rows. Snoozed holds what the person snoozed (#2480), and
 * Archived the archived sessions (#2795): no count and no list shows them, and the
 * command menu still finds them there. */
/** The sessions a stop of every agent would end: each one that has not ended, idle and
 * waiting ones too, counted as Now counts its running sessions. */
export function aliveSessions(sources: RailSources): number {
  return mergeSessions([...sources.live], [...(sources.runs ?? [])]).filter(
    (s) => railState(s) !== 'ended',
  ).length
}

export function railGroups(
  sources: RailSources,
  now: number,
  destinations: RailDestinations = {},
): RailGroup[] {
  const needs: RailRow[] = sources.handoffs
    .filter(
      (h) => h.handoff.state === undefined || h.handoff.state === 'offered',
    )
    .map((h) => handoffRow(h, now))
  const working: RailRow[] = []
  const earlier: RailRow[] = []
  const snoozed: RailRow[] = []
  const archived: RailRow[] = []
  // The person's own marks (#2480), the rule the Sessions rail sorts by
  // (`groupSessions`): a snoozed session is out of the list until its moment, and one
  // marked done is filed under Earlier whatever it is doing.
  const sessions = mergeSessions([...sources.live], [...(sources.runs ?? [])])
  const shared = sharedNames(sessions, '')
  for (const s of sessions) {
    const row = sessionRow(s, now, shared, destinations)
    if (isArchived(s)) archived.push(row)
    else if (isSnoozed(s)) snoozed.push(row)
    else if (isMarkedDone(s)) earlier.push(row)
    else if (row.state === 'need') needs.push(row)
    else if (row.state === 'live') working.push(row)
    else earlier.push(row)
  }
  const cap = (rows: RailRow[]) => rows.slice(0, RAIL_ROWS_PER_GROUP)
  return [
    {
      id: 'needsYou',
      rows: cap(needs),
      total: needs.length,
      sessionTotal: needs.filter((row) => row.kind === 'session').length,
    },
    {
      id: 'working',
      rows: cap(working),
      total: working.length,
      sessionTotal: working.length,
    },
    {
      id: 'earlier',
      rows: cap(earlier),
      total: earlier.length,
      sessionTotal: earlier.length,
    },
    {
      id: 'snoozed',
      rows: cap(snoozed),
      total: snoozed.length,
      sessionTotal: snoozed.length,
    },
    {
      id: 'archived',
      rows: cap(archived),
      total: archived.length,
      sessionTotal: archived.length,
    },
  ]
}
