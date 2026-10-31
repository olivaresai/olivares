// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE RECENT SESSIONS NOW AND USAGE & COST BOTH COUNT: the live page and the run page, each
// read only when the principal holds its permission, merged into one row per session. One
// read with one cache key, so the two pages never say two different things about a day.
import { useEffect, useMemo } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useAuth } from '@/lib/auth/context'
import { useMinuteClock } from '@/components/layout/use-session-rail'
import { currentAnswer } from '@/features/executive/derive'
import { agentOpsApi, agentOpsKeys } from '@/features/agentops/api'
import { sessionsApi, sessionsKeys } from '@/features/sessions/api'
import { mergeSessions } from '@/features/sessions/provenance'
import { dayIsFloor } from './now-sessions'

/** How many runs are read for the recent sessions (Now shows the newest few). */
const RECENT_RUN_PARAMS = { limit: 30 } as const

export function useRecentSessions() {
  const { activeTenant, can } = useAuth()
  const queryClient = useQueryClient()
  // Live sessions are tenant-wide (GET /v1/m/sessions/live takes no workspace selector).
  const canSessions = can('sessions:live:read')
  const sessionsQ = useQuery({
    queryKey: sessionsKeys.live(activeTenant),
    queryFn: () => sessionsApi.live(),
    enabled: canSessions,
    // The clock ticks the figures between polls; this cadence is what certifies
    // new usage (the costs are the engine's numbers, never extrapolated).
    refetchInterval: 15_000,
  })
  // The sessions Olivares operates, so a launched session is on Estate by its name and
  // state (one row with what was observed of it, merged below), with its session facts.
  const canRuns = can('sessions:run:read')
  const runsQ = useQuery({
    queryKey: [...agentOpsKeys.runs(activeTenant, RECENT_RUN_PARAMS), 'home'],
    queryFn: () => agentOpsApi.listRuns(RECENT_RUN_PARAMS),
    enabled: canRuns,
  })

  // ⛔ A PAUSED READ OUTLIVES THE PERMISSION THAT DISPATCHED IT unless it is cancelled.
  //    `enabled: false` stops a query from STARTING, not one already dispatched and
  //    paused (query-core continues every paused retryer when the client comes back
  //    online). Cancelling with TanStack's default `revert` puts it back to pending/idle,
  //    where a later re-grant starts it afresh. Scoped to the exact key.
  useEffect(() => {
    if (canSessions) return
    void queryClient.cancelQueries({
      queryKey: sessionsKeys.live(activeTenant),
      exact: true,
    })
  }, [canSessions, activeTenant, queryClient])

  // Only CURRENT, permitted answers (`currentAnswer`): a read the role lost, or one whose
  // refetch failed, keeps its old rows on the query, and those must not paint a card or
  // count toward a figure.
  const liveAnswer = currentAnswer(canSessions, sessionsQ)
  const runsAnswer = currentAnswer(canRuns, runsQ)
  const recentSessions = useMemo(
    () =>
      liveAnswer || runsAnswer
        ? mergeSessions(liveAnswer?.items ?? [], runsAnswer?.items ?? [])
        : undefined,
    [liveAnswer, runsAnswer],
  )
  // The minute clock moves "today" past midnight without a reload.
  const now = useMinuteClock()
  // A page cut short hides a day's sessions only if all it holds is from that day on.
  const activity = {
    live: (liveAnswer?.items ?? []).map((l) => Date.parse(l.last_event_at)),
    runs: (runsAnswer?.items ?? []).map((r) =>
      Date.parse(r.last_activity_at ?? r.started_at ?? r.created_at ?? ''),
    ),
  }
  const floor = (daysAgo: number) =>
    dayIsFloor(liveAnswer?.has_more, activity.live, now, daysAgo) ||
    dayIsFloor(runsAnswer?.has_more, activity.runs, now, daysAgo)
  return {
    canSessions,
    canRuns,
    sessionsQ,
    runsQ,
    liveAnswer,
    runsAnswer,
    recentSessions,
    now,
    floor,
  }
}
