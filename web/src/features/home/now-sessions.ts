// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// WHAT ESTATE SAYS ABOUT THE SESSIONS IT ALREADY HOLDS: which are live (a card each),
// what the sessions that worked on a day cost, and whom the live ones act for. Pure, over
// the merged page `home-view` reads, so a test drives it without rendering. No second
// read and no figure the engine did not send.
import {
  isLiveRun,
  primaryRun,
  type UnifiedSession,
} from '@/features/sessions/provenance'
import { sessionUsage } from '@/features/sessions/session-usage'

/** How many live sessions Now shows as cards. The Sessions page holds the rest. */
export const LIVE_CARDS = 6

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

/**
 * How many people the sessions act for, each once; a session a work item or an agent
 * dispatched acts for nobody. `null` while one session does not say whom it acts for (an
 * engine older than the facts, an observed session with no launch record, a session
 * that predates the record): a count over the others would be a guess.
 */
export function actsForPeople(
  sessions: readonly UnifiedSession[],
): number | null {
  const people = new Set<string>()
  for (const s of sessions) {
    const actsFor = primaryRun(s.runs)?.acts_for
    if (!actsFor || actsFor.kind === 'unknown') return null
    if (actsFor.kind !== 'user') continue
    if (!actsFor.user_id) return null
    people.add(actsFor.user_id)
  }
  return people.size
}
