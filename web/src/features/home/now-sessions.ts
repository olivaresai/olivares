// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// WHAT NOW SAYS ABOUT THE SESSIONS IT ALREADY HOLDS: which are live (a card each) and what
// the sessions that worked today cost. Pure, over the merged page `home-view` reads, so a
// test drives it without rendering. No second read and no figure the engine did not send.
import { isLiveRun, type UnifiedSession } from '@/features/sessions/provenance'
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
  /** Sessions with activity since local midnight. */
  sessions: number
  /** The sum of the costs they reported; `null` when none reported one. */
  costMicroUsd: number | null
  /** Sessions among them whose cost is unknown or only partly reported. */
  unpriced: number
}

/**
 * The cost of the sessions that worked today. A session's cost is its own total, so one
 * that began yesterday counts whole: the figure is what today's sessions cost, and the
 * tile says so. A session that reported no cost is counted as unpriced, never as $0.
 */
export function todaysCost(
  sessions: readonly UnifiedSession[],
  now: number,
): TodaysCost {
  const midnight = new Date(now)
  midnight.setHours(0, 0, 0, 0)
  const today = sessions.filter((s) => s.lastActivityMs >= midnight.getTime())
  let costMicroUsd: number | null = null
  let unpriced = 0
  for (const s of today) {
    const usage = sessionUsage(s.runs, s.live)
    if (usage?.costMicroUsd != null)
      costMicroUsd = (costMicroUsd ?? 0) + usage.costMicroUsd
    if (usage?.costMicroUsd == null || usage.costPartial) unpriced++
  }
  return { sessions: today.length, costMicroUsd, unpriced }
}

/**
 * A page with rows beyond it makes today's figure a floor only while every row it holds
 * worked today: once it reaches a row from before midnight, the rows beyond it are older
 * still. A row whose time is unknown counts as today, so the doubt marks the floor.
 */
export function todayIsFloor(
  hasMore: boolean | undefined,
  activityMs: readonly number[],
  now: number,
): boolean {
  if (hasMore !== true) return false
  const midnight = new Date(now)
  midnight.setHours(0, 0, 0, 0)
  return activityMs.every((ms) => !(ms < midnight.getTime()))
}
