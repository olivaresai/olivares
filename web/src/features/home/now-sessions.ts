// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// WHAT OVERVIEW SAYS ABOUT THE SESSIONS IT ALREADY HOLDS: which are live (a Running row
// each), what the sessions that worked on a day cost, and how many failed today. Pure, over
// the merged page `home-view` reads, so a test drives it without rendering. No second
// read and no figure the engine did not send.
import { isLiveRun, type UnifiedSession } from '@/features/sessions/provenance'
import { sessionDisplayState } from '@/features/sessions/session-display-state'
import { sessionUsage } from '@/features/sessions/session-usage'

/** The observed states the Live sessions tile counts (`deriveUsage.liveNow`). */
const LIVE_OBSERVED = ['active', 'idle', 'silent_evasion']

/**
 * A session that has not ended: a run the plane holds a process for (or whose launch
 * waits on an approval), else an observed row in a state the Live sessions tile counts.
 * Close to the tile's rule, not identical: the tile counts the observed page alone, so a
 * held launch has a card and no tile count, and the e2e check allows fewer cards.
 */
export function isLiveSession(s: UnifiedSession): boolean {
  if (s.runs.length > 0)
    return s.runs.some((r) => isLiveRun(r) || r.state === 'waiting_approval')
  return !!s.live && LIVE_OBSERVED.includes(s.live.cc_state)
}

export interface TodaysCost {
  /** Sessions whose last work fell on the day. */
  sessions: number
  /** The sum of the costs they reported; `null` when none reported one. */
  costMicroUsd: number | null
  /** Sessions among them whose cost is unknown or only partly reported. */
  unpriced: number
  /** The tokens they reported, in and out; `null` when none reported any. */
  inputTokens: number | null
  outputTokens: number | null
  /** Sessions among them that reported no tokens: the token sum leaves them out. */
  tokensUnreported: number
}

/** Sessions whose run failed and that last worked today: Overview's "N failed today". */
export function failedToday(
  sessions: readonly UnifiedSession[],
  now: number,
): number {
  const from = midnightOf(now, 0)
  return sessions.filter(
    (s) =>
      s.lastActivityMs >= from && sessionDisplayState(s).status === 'failed',
  ).length
}

/** Local midnight `daysAgo` days before `now`'s day. */
function midnightOf(now: number, daysAgo: number): number {
  const day = new Date(now)
  day.setHours(0, 0, 0, 0)
  day.setDate(day.getDate() - daysAgo)
  return day.getTime()
}

/**
 * The cost of the sessions whose last work fell on a day: today (`daysAgo` 0) or an
 * earlier one. A session's cost is its own total and it belongs to the day it last
 * worked, so one that began yesterday and worked today counts whole for today and not
 * for yesterday. A session that reported no cost is counted as unpriced, never as $0,
 * and one that reported no tokens is counted apart, never as 0 tokens.
 */
export function dayCost(
  sessions: readonly UnifiedSession[],
  now: number,
  daysAgo = 0,
): TodaysCost {
  const from = midnightOf(now, daysAgo)
  const until = daysAgo === 0 ? Infinity : midnightOf(now, daysAgo - 1)
  const day = sessions.filter(
    (s) => s.lastActivityMs >= from && s.lastActivityMs < until,
  )
  let costMicroUsd: number | null = null
  let unpriced = 0
  let inputTokens: number | null = null
  let outputTokens: number | null = null
  let tokensUnreported = 0
  for (const s of day) {
    const usage = sessionUsage(s.runs, s.live)
    if (usage?.costMicroUsd != null)
      costMicroUsd = (costMicroUsd ?? 0) + usage.costMicroUsd
    if (usage?.costMicroUsd == null || usage.costPartial) unpriced++
    if (usage?.input != null && usage.output != null) {
      inputTokens = (inputTokens ?? 0) + usage.input
      outputTokens = (outputTokens ?? 0) + usage.output
    } else tokensUnreported++
  }
  return {
    sessions: day.length,
    costMicroUsd,
    unpriced,
    inputTokens,
    outputTokens,
    tokensUnreported,
  }
}

/**
 * A page with rows beyond it makes a day's figure a floor only while every row it holds
 * worked on that day or later: once it reaches a row from before that day, the rows
 * beyond it are older still. A row whose time is unknown counts as recent, so the doubt
 * marks the floor.
 */
export function dayIsFloor(
  hasMore: boolean | undefined,
  activityMs: readonly number[],
  now: number,
  daysAgo = 0,
): boolean {
  if (hasMore !== true) return false
  const from = midnightOf(now, daysAgo)
  return activityMs.every((ms) => !(ms < from))
}

/** One page of a half of the list, as far as the floor rule reads it. */
interface DayPage<T> {
  has_more?: boolean
  items: readonly T[]
}

/**
 * Whether the loaded pages make a day's figure a floor: either half was cut short while
 * every row it holds worked on that day or later (`dayIsFloor`). An observed row counts at
 * its last event, a run at its last activity, else its start, else its creation. A half not
 * read is no page and hides nothing.
 */
export function dayIsFloorOf(
  live: DayPage<{ last_event_at: string }> | undefined,
  runs:
    | DayPage<{
        last_activity_at?: string
        started_at?: string
        created_at?: string
      }>
    | undefined,
  now: number,
  daysAgo = 0,
): boolean {
  return (
    dayIsFloor(
      live?.has_more,
      (live?.items ?? []).map((l) => Date.parse(l.last_event_at)),
      now,
      daysAgo,
    ) ||
    dayIsFloor(
      runs?.has_more,
      (runs?.items ?? []).map((r) =>
        Date.parse(r.last_activity_at ?? r.started_at ?? r.created_at ?? ''),
      ),
      now,
      daysAgo,
    )
  )
}
