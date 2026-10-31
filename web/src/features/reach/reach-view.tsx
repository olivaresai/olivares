// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The Reach view (issue #2553): what the tools reach — folders, MCP servers and
// provider endpoints — read from the EXISTING access map, agents list, live
// sessions and pending approval queue (no new mechanism). Waiting destinations
// come ONLY from pending approvals; without the approval read the waiting column
// is honestly absent, and when the approval read fails the meta counts
// destinations without a waiting number rather than pretending zero requests are
// pending.
import { useMemo } from 'react'
import { useQueries } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { PageHeader } from '@/components/ui/page-header'
import { Button } from '@/components/ui/button'
import { UrlStateNotice } from '@/features/shared'
import { useAuth } from '@/lib/auth/context'
import {
  type UrlState,
  type UrlStateDecoded,
  useValidatedUrlState,
} from '@/lib/hooks/use-url-state'
import { moduleEnabled, useModulesStore } from '@/stores/modules'
import { reachApi, reachKeys } from './api'
import { buildReach } from './model'
import { ReachGraph } from './reach-graph'
import { ReachTable } from './reach-table'
import './i18n'

const REACH_READ = 'accessmap:graph:read'
const APPROVAL_READ = 'governance:approval:read'
const SESSIONS_READ = 'sessions:live:read'

type ReachMode = 'graph' | 'table'

/** `?view=table` is the table; no key is the graph. Anything else is refused and said. */
function decodeReachView(raw: UrlState): UrlStateDecoded<ReachMode> {
  if (raw.view === undefined) return { value: 'graph', issues: [] }
  if (raw.view === 'table') return { value: 'table', issues: [] }
  return { value: 'graph', issues: ['view'] }
}

export default function ReachView() {
  const auth = useAuth()
  const { t } = useTranslation('reach')
  const off = useModulesStore((state) => state.off)
  // Graph or table is part of the view, so it lives in the address: a pasted link opens
  // the same one, and Access map keeps its own selection and filters there too.
  const [mode, setView, viewIssues] = useValidatedUrlState(
    ['view'],
    decodeReachView,
  )
  const setMode = (next: ReachMode) =>
    setView({ view: next === 'table' ? 'table' : undefined })
  const canReadApprovals = auth.can(APPROVAL_READ)
  const canReadSessions = auth.can(SESSIONS_READ)
  const on = moduleEnabled(off, REACH_READ, 'reach')

  const [graphQuery, agentsQuery, sessionsQuery, approvalsQuery] = useQueries({
    queries: [
      {
        queryKey: reachKeys.graph(auth.activeTenant),
        queryFn: () => reachApi.graph(),
        enabled: on,
        retry: false,
      },
      {
        queryKey: reachKeys.agents(auth.activeTenant),
        queryFn: () => reachApi.agents(),
        enabled: on,
        retry: false,
      },
      {
        queryKey: reachKeys.sessions(auth.activeTenant),
        queryFn: () => reachApi.sessions(),
        enabled: canReadSessions && on,
        retry: false,
      },
      {
        queryKey: reachKeys.approvals(auth.activeTenant),
        queryFn: () => reachApi.pendingApprovals(),
        enabled: canReadApprovals && on,
        retry: false,
        // The dash means "still waiting": read the queue again on every visit, so a
        // request approved a moment ago is not redrawn from cache.
        staleTime: 0,
      },
    ],
  })

  const model = useMemo(
    () =>
      buildReach(
        graphQuery.data ?? { edges: [], has_more: false },
        agentsQuery.data?.items ?? [],
        canReadApprovals ? (approvalsQuery.data?.items ?? []) : [],
        canReadSessions ? (sessionsQuery.data?.items ?? []) : [],
      ),
    [
      graphQuery.data,
      agentsQuery.data,
      approvalsQuery.data,
      sessionsQuery.data,
      canReadApprovals,
      canReadSessions,
    ],
  )

  if (!on) return <p role="status">{t('off')}</p>

  const waitingCount = model.destinations.filter((d) => d.waiting).length
  const waitingKnown = canReadApprovals && approvalsQuery.isSuccess
  const waitingFailed = canReadApprovals && approvalsQuery.isError
  const graphFailed = graphQuery.isError
  // The names are part of the picture: drawing before they arrive shows raw refs.
  const loading =
    graphQuery.isLoading ||
    agentsQuery.isLoading ||
    (canReadSessions && sessionsQuery.isLoading) ||
    (canReadApprovals && approvalsQuery.isLoading)
  const empty = !loading && !graphFailed && model.destinations.length === 0

  return (
    <div className="space-y-5">
      <PageHeader
        title={t('title')}
        description={t('subtitle')}
        actions={
          <div className="flex items-center gap-1">
            <Button
              size="sm"
              variant={mode === 'graph' ? 'secondary' : 'ghost'}
              aria-pressed={mode === 'graph'}
              onClick={() => setMode('graph')}
            >
              {t('toggleGraph')}
            </Button>
            <Button
              size="sm"
              variant={mode === 'table' ? 'secondary' : 'ghost'}
              aria-pressed={mode === 'table'}
              onClick={() => setMode('table')}
            >
              {t('toggleTable')}
            </Button>
          </div>
        }
      />
      <UrlStateNotice issues={viewIssues} />
      {!loading && !graphFailed && (
        <p className="text-caption text-text-2">
          {waitingKnown
            ? t('meta', {
                count: model.destinations.length,
                waiting: waitingCount,
              })
            : t('metaCountOnly', { count: model.destinations.length })}
        </p>
      )}
      {loading && <p role="status">{t('loading')}</p>}
      {graphFailed && <p role="alert">{t('error')}</p>}
      {!loading && !graphFailed && waitingFailed && (
        <p role="alert">{t('approvalsError')}</p>
      )}
      {!loading && !graphFailed && !canReadApprovals && (
        <p role="status">{t('waitingHidden')}</p>
      )}
      {empty && <p role="status">{t('empty')}</p>}
      {!loading && !graphFailed && !empty && (
        <>
          {mode === 'graph' ? (
            <ReachGraph model={model} />
          ) : (
            <ReachTable model={model} />
          )}
        </>
      )}
      {model.hasMore && <p role="status">{t('partial')}</p>}
      {!loading &&
        !graphFailed &&
        (agentsQuery.isError ||
          agentsQuery.data?.has_more ||
          model.sessionsByRef) && <p role="status">{t('namesUnavailable')}</p>}
      {!loading &&
        !graphFailed &&
        waitingKnown &&
        approvalsQuery.data?.has_more && (
          <p role="status">{t('waitingPartial')}</p>
        )}
    </div>
  )
}
