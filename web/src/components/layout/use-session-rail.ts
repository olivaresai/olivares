// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Each rail source follows its own journey's read authority.
import type { ListResponse } from '@/lib/api/types'
import { useQuery } from '@tanstack/react-query'
import { useRouterState } from '@tanstack/react-router'
import { useEffect, useState } from 'react'
import { useAuth } from '@/lib/auth/context'
import { isGlobalAccount } from '@/lib/auth/rbac'
import { useWorkspaceStore } from '@/stores/workspace'
import { useAuthBoundary } from '@/features/agentops/auth-boundary'
import { APPROVAL_READ } from '@/features/governance/use-pending-approvals'
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
import { attentionSnapshots, type AttentionSnapshot } from './session-attention'
import {
  RAIL_ROWS_PER_GROUP,
  aliveSessions,
  openSessions,
  railGroups,
  type RailGroup,
} from './session-rail-model'

/** Counts use all pages. Row caps belong only to the display model. */
const RAIL_LIVE_PARAMS = { limit: 200, pagination: 'cursor' } as const

async function allSessionPages<T>(
  load: (cursor?: string) => Promise<ListResponse<T>>,
  signal: AbortSignal,
): Promise<ListResponse<T>> {
  const items: T[] = []
  const seen = new Set<string>()
  let cursor: string | undefined
  for (;;) {
    signal.throwIfAborted()
    const page = await load(cursor)
    items.push(...page.items)
    if (!page.has_more) return { items, has_more: false }
    if (!page.cursor || seen.has(page.cursor))
      throw new Error('Session page cursor did not advance')
    cursor = page.cursor
    seen.add(cursor)
  }
}
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
  /** False when this principal may read no source: no rail is drawn. */
  visible: boolean
  groups: RailGroup[]
  attention: AttentionSnapshot[]
  attentionReady: boolean
  /** live: Needs you and Working; running: every session Stop all would end, idle and
   * archived ones too; open: those the list shows that have not ended (the Sessions badge
   * and the status bar's "N sessions open"). */
  sessionCounts: {
    live: number
    needsYou: number
    running: number
    open: number
  }
  status: RailStatus
  allTo: '/sessions' | '/agentops' | '/communications/handoffs'
}

/** Whether a waiting session's row may link to its approval: the view opens and the
 * principal reads approvals. One rule for the rail and for Now's queue. */
export function useCanOpenApprovals(): boolean {
  const { navigable } = useViewAccess()
  const { can } = useAuth()
  const approvalsView = viewById('permissions')
  return !!approvalsView && navigable(approvalsView) && can(APPROVAL_READ)
}

/** Observed sessions, operated runs and handoffs offered to the operator. */
export function useSessionRail(background = false): SessionRailData {
  const { navigable } = useViewAccess()
  const boundary = useAuthBoundary()
  const now = useMinuteClock()
  const canOpenApprovals = useCanOpenApprovals()

  const sessionsView = viewById('sessions')
  const runsView = viewById('agentops')
  const readSessions =
    !!boundary.tenant && !!sessionsView && navigable(sessionsView)
  const readRuns = !!boundary.tenant && !!runsView && navigable(runsView)

  const live = useQuery({
    queryKey: sessionsKeys.liveScoped(
      boundary.tenant,
      boundary.epoch,
      RAIL_LIVE_PARAMS,
    ),
    queryFn: ({ signal }) =>
      allSessionPages(
        (cursor) =>
          sessionsApi.live(
            { ...RAIL_LIVE_PARAMS, cursor },
            { tenant: boundary.tenant, signal },
          ),
        signal,
      ),
    enabled: readSessions,
    refetchInterval: RAIL_REFRESH_MS,
    refetchIntervalInBackground: background,
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
      allSessionPages(
        (cursor) =>
          agentOpsApi.listRuns(
            { ...RAIL_LIVE_PARAMS, cursor },
            { tenant: boundary.tenant, signal },
          ),
        signal,
      ),
    enabled: readRuns,
    refetchIntervalInBackground: background,
    refetchInterval: (query) =>
      query.state.data?.items.some((r) => ACTIVE_RUN_STATES.has(r.state))
        ? RAIL_ACTIVE_REFRESH_MS
        : RAIL_REFRESH_MS,
    staleTime: RAIL_ACTIVE_REFRESH_MS / 2,
  })
  // A session just started carries session=run:<ref> before the next poll:
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
      readRuns &&
      opened?.startsWith('run:') &&
      runsData &&
      !runsData.items.some((r) => `run:${r.run_ref}` === opened)
    )
      void refetchRuns()
  }, [readRuns, opened, runsData, refetchRuns])
  const { readHandoffs, handoffs } = useOfferedHandoffs()

  const reads = [
    readSessions ? live : null,
    readRuns ? runs : null,
    readHandoffs ? handoffs : null,
  ].filter((read) => read !== null)
  const status: RailStatus = {
    loading: reads.some((read) => read.isPending),
    error: reads.some((read) => read.isError),
  }
  const sources = {
    live: readSessions && !live.isError ? (live.data?.items ?? []) : [],
    handoffs:
      readHandoffs && !handoffs.isError ? (handoffs.data?.items ?? []) : [],
    runs: readRuns && !runs.isError ? (runs.data?.items ?? []) : [],
  }
  const groups = railGroups(sources, now, {
    sessionTo: readSessions ? '/sessions' : '/agentops',
    canOpenApprovals,
  })
  const allTo = readSessions
    ? '/sessions'
    : readRuns
      ? '/agentops'
      : '/communications/handoffs'
  return {
    visible: readSessions || readRuns || readHandoffs,
    groups,
    // The age of a turn end is measured at the poll that read it, not at the minute tick.
    attention: attentionSnapshots(
      sources,
      Math.max(live.dataUpdatedAt, runs.dataUpdatedAt),
    ),
    attentionReady:
      (!readSessions || live.isSuccess) && (!readRuns || runs.isSuccess),
    sessionCounts: {
      live: groups
        .filter((g) => g.id === 'needsYou' || g.id === 'working')
        .reduce((n, g) => n + g.sessionTotal, 0),
      needsYou: groups.find((g) => g.id === 'needsYou')?.sessionTotal ?? 0,
      running: aliveSessions(sources),
      open: openSessions(sources),
    },
    status,
    allTo,
  }
}

/**
 * The handoffs offered to this operator in the selected workspace: the rail's inbox read,
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
  // is not asked for that account (the handoffs page says the same). A superadmin with
  // a grant in this tenant is a member and is asked (#503).
  const readHandoffs =
    !!boundary.tenant &&
    !!handoffsView &&
    navigable(handoffsView) &&
    !!workspace &&
    !isGlobalAccount(principal, boundary.tenant)
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
    enabled: readHandoffs,
    refetchInterval: RAIL_REFRESH_MS,
    staleTime: RAIL_REFRESH_MS / 2,
  })

  return { readHandoffs, handoffs }
}
