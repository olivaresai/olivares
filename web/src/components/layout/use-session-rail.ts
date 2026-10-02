// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE SESSION RAIL'S TWO READS: the sessions list, when the Sessions journey is open to
// this principal, and the handoffs offered to the operator, when the handoffs page is.
// A principal who may read neither gets no rail.
import { useQuery } from '@tanstack/react-query'
import { useRouterState } from '@tanstack/react-router'
import { useEffect, useState } from 'react'
import { useAuth } from '@/lib/auth/context'
import { useWorkspaceStore } from '@/stores/workspace'
import { useAuthBoundary } from '@/features/agentops/auth-boundary'
import {
  communicationsKeys,
  listHandoffInbox,
} from '@/features/communications/api'
import { useViewAccess } from '@/features/navigation/authorization'
import { viewById } from '@/features/navigation/model'
import { sessionsApi, sessionsKeys } from '@/features/sessions/api'
import { SESSION_PARAM } from '@/features/sessions/session-address'
import { agentOpsApi, agentOpsKeys } from '@/features/agentops/api'
import type { RailStatus } from './session-rail'
import {
  RAIL_ROWS_PER_GROUP,
  railGroups,
  type RailGroup,
} from './session-rail-model'

/** How many sessions the rail asks for: at most thirty rows are drawn (§3.19). */
const RAIL_LIVE_PARAMS = { limit: 30 } as const
/** The rail follows the stream at the pace of a glance, not of a work surface. */
const RAIL_REFRESH_MS = 20_000
/** While a run works or waits, its row changes state soon: follow it closely. */
const RAIL_ACTIVE_REFRESH_MS = 5_000
const ACTIVE_RUN_STATES = new Set([
  'pending',
  'running',
  'idle',
  'waiting_approval',
])

/** A clock that ticks once a minute, so elapsed times move without a refetch. */
export function useMinuteClock(): number {
  const [now, setNow] = useState(() => Date.now())
  useEffect(() => {
    const id = window.setInterval(() => setNow(Date.now()), 60_000)
    return () => window.clearInterval(id)
  }, [])
  return now
}

export interface SessionRailData {
  /** False when this principal may read neither source: no rail is drawn. */
  visible: boolean
  groups: RailGroup[]
  status: RailStatus
}

/** The rail's two reads: the sessions list and the handoffs offered to the operator. */
export function useSessionRail(): SessionRailData {
  const { navigable } = useViewAccess()
  const boundary = useAuthBoundary()
  const now = useMinuteClock()

  const sessionsView = viewById('sessions')
  const readSessions = !!sessionsView && navigable(sessionsView)

  const live = useQuery({
    queryKey: sessionsKeys.liveScoped(
      boundary.tenant,
      boundary.epoch,
      RAIL_LIVE_PARAMS,
    ),
    queryFn: ({ signal }) =>
      sessionsApi.live(RAIL_LIVE_PARAMS, { tenant: boundary.tenant, signal }),
    enabled: readSessions && !!boundary.tenant,
    refetchInterval: RAIL_REFRESH_MS,
    staleTime: RAIL_REFRESH_MS / 2,
  })
  // The sessions this engine operates: what a person started from New session.
  const runs = useQuery({
    queryKey: [
      ...agentOpsKeys.runsScoped(
        boundary.tenant,
        boundary.epoch,
        RAIL_LIVE_PARAMS,
      ),
      'shell-rail',
    ],
    queryFn: ({ signal }) =>
      agentOpsApi.listRuns(RAIL_LIVE_PARAMS, {
        tenant: boundary.tenant,
        signal,
      }),
    enabled: readSessions && !!boundary.tenant,
    refetchInterval: (query) =>
      query.state.data?.items.some((r) => ACTIVE_RUN_STATES.has(r.state))
        ? RAIL_ACTIVE_REFRESH_MS
        : RAIL_REFRESH_MS,
    staleTime: RAIL_ACTIVE_REFRESH_MS / 2,
  })
  // A session just started opens at /sessions?session=run:<ref> before the next poll:
  // read the runs again so its row is in the rail at once.
  const opened = useRouterState({
    select: (s): string | null => {
      const v = (s.location.search as Record<string, unknown>)[SESSION_PARAM]
      return typeof v === 'string' ? v : null
    },
  })
  const { data: runsData, refetch: refetchRuns } = runs
  useEffect(() => {
    if (
      opened?.startsWith('run:') &&
      runsData &&
      !runsData.items.some((r) => `run:${r.run_ref}` === opened)
    )
      void refetchRuns()
  }, [opened, runsData, refetchRuns])
  const { readHandoffs, handoffs } = useOfferedHandoffs()

  const status: RailStatus =
    live.isError && handoffs.isError
      ? 'error'
      : (readSessions && live.isPending && live.fetchStatus !== 'idle') ||
          (readHandoffs &&
            handoffs.isPending &&
            handoffs.fetchStatus !== 'idle')
        ? 'loading'
        : 'ready'
  const groups = railGroups(
    {
      live: live.data?.items ?? [],
      handoffs: handoffs.data?.items ?? [],
      runs: runs.data?.items ?? [],
    },
    now,
  )
  return { visible: readSessions || readHandoffs, groups, status }
}

/**
 * The handoffs offered to this operator in the selected workspace: the rail's second read,
 * also used by Now's "Needs you" queue (features/home/now-queue.tsx), which takes its
 * sessions from the page Now already holds instead of reading them a second time.
 */
export function useOfferedHandoffs() {
  const { navigable } = useViewAccess()
  const { principal } = useAuth()
  const boundary = useAuthBoundary()
  const workspace = useWorkspaceStore((s) => s.activeWorkspace)
  const handoffsView = viewById('communicationsHandoffs')
  // A global administrator is never the recipient of a personal handoff, so the inbox
  // is not asked for that account (the handoffs page says the same).
  const readHandoffs =
    !!handoffsView &&
    navigable(handoffsView) &&
    !!workspace &&
    principal?.superadmin !== true
  const handoffs = useQuery({
    queryKey: [
      ...communicationsKeys.workspaceScope(
        boundary.tenant,
        boundary.epoch,
        workspace ?? '',
      ),
      'shell-rail',
      'handoffs-offered',
    ],
    queryFn: ({ signal }) =>
      listHandoffInbox(
        {
          workspace_id: workspace ?? '',
          state: 'offered',
          limit: RAIL_ROWS_PER_GROUP,
        },
        { tenant: boundary.tenant },
        signal,
      ),
    enabled: readHandoffs && !!boundary.tenant,
    refetchInterval: RAIL_REFRESH_MS,
    staleTime: RAIL_REFRESH_MS / 2,
  })

  return { readHandoffs, handoffs }
}
