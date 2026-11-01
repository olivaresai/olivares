// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Models > Available, the first tab: what each tool and provider can run now, and which of it
// the live sessions use. It adds no engine route. The reads are the ones the New session form,
// AI tools, model availability and sessions already serve (shared query keys where a screen
// uses the same entry), and `available.ts` joins them.
import { useEffect, useId, useRef, useState } from 'react'
import { useQueries, useQuery } from '@tanstack/react-query'
import { Link } from '@tanstack/react-router'
import { ChevronDown, ChevronRight, Download, Plus } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { StatusBadge } from '@/components/data/badges'
import { XScroll } from '@/components/data/scroll-edges'
import { StaticTable } from '@/components/data/static-table'
import { StepUpRequiredState } from '@/components/layout/step-up-state'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { EmptyState } from '@/components/ui/empty-state'
import { Skeleton } from '@/components/ui/skeleton'
import { agentToolsApi, OLLAMA_AVAILABLE_KEY } from '@/features/agent-tools/api'
import { agentOpsApi, agentOpsKeys } from '@/features/agentops/api'
import { useAuthBoundary } from '@/features/agentops/auth-boundary'
import { toolName } from '@/features/agentops/engine-tools'
import {
  PROVIDER_PAGE,
  providersApi,
  providerKeys,
} from '@/features/providers/api'
import { ListTruncationBadge, SectionCard } from '@/features/_intel'
import { isLiveRun, LIVE_RUN_STATES } from '@/features/sessions/provenance'
import {
  addressFromTarget,
  SESSION_PARAM,
} from '@/features/sessions/session-address'
import { ApiError } from '@/lib/api/errors'
import { useAuth } from '@/lib/auth/context'
import { modelsApi, modelsKeys } from './api'
import {
  buildAvailableRows,
  providerNames,
  type AvailableRow,
  type ToolCatalog,
} from './available'
import './i18n'

/** The engine caches a profile's model list for two minutes; asking sooner repeats it. */
const CATALOG_STALE_MS = 120_000
/** Sessions read per live state. A state with more says so under the table. */
const RUNS_PAGE = 200
/** "Now" is a live reading: the session counts follow the runs. */
const RUNS_POLL_MS = 15_000
/** A provider whose first model read is pending is asked at that pace for this long only. */
const CHECKING_POLL_WINDOW_MS = 180_000

type SourceName = 'tools' | 'ollama' | 'availability' | 'runs'

/** A refusal ends what this page may show until a fresh answer succeeds. A 5xx does not. */
function isRefusal(error: unknown): boolean {
  return (
    error instanceof ApiError &&
    (error.isUnauthenticated || error.isForbidden || error.isStepUpRequired)
  )
}

function isStepUp(error: unknown): boolean {
  return error instanceof ApiError && error.isStepUpRequired
}

// ponytail: one model-list read per active profile (the engine caches each for two minutes),
// the profiles read at the 200 the other screens use; availability is unpaginated and each
// source is bounded by the engine. A tenant past the profile page is told here.
function useAvailable() {
  const { activeTenant, can, isSuperadmin } = useAuth()
  const boundary = useAuthBoundary()
  const [refusals, setRefusals] = useState<ReadonlySet<string>>(new Set())
  /** The `dataUpdatedAt` of the first availability answer with a source still checking. */
  const checkingSince = useRef<number | null>(null)
  const canProfiles = can('sessions:profile:read')
  const canRuns = can('sessions:run:read')
  const canAvailability = can('models:catalog:read')
  const canProviders = can('sessions:provider:read') && canAvailability
  const markRefusal = (key: string, error: unknown) => {
    if (!isRefusal(error)) return
    setRefusals((previous) => {
      if (previous.has(key)) return previous
      return new Set(previous).add(key)
    })
  }
  const clearRefusal = (key: string) => {
    setRefusals((previous) => {
      if (!previous.has(key)) return previous
      const next = new Set(previous)
      next.delete(key)
      return next
    })
  }

  const profilesQ = useQuery({
    queryKey: agentOpsKeys.profiles(activeTenant, boundary.epoch, {
      state: 'active',
    }),
    queryFn: async ({ signal }) => {
      try {
        const result = await agentOpsApi.listProfiles(
          { state: 'active', limit: 200 },
          { signal },
        )
        clearRefusal('profiles')
        return result
      } catch (error) {
        markRefusal('profiles', error)
        throw error
      }
    },
    enabled: canProfiles,
  })
  // A profile of another machine is shown elsewhere as such and never launched here.
  // A permission bit or a refusal hides the cached answer NOW, not on the next poll.
  const profilesRefused = refusals.has('profiles') || isRefusal(profilesQ.error)
  const profiles =
    canProfiles && !profilesRefused
      ? (profilesQ.data?.items ?? []).filter(
          (p) => p.local_environment !== false,
        )
      : []
  const catalogQs = useQueries({
    queries: profiles.map((p) => ({
      queryKey: agentOpsKeys.profileModels(
        activeTenant,
        boundary.epoch,
        p.profile_ref,
      ),
      queryFn: async ({ signal }: { signal: AbortSignal }) => {
        try {
          const result = await agentOpsApi.profileModels(p.profile_ref, {
            tenant: activeTenant,
            signal,
          })
          clearRefusal(`catalog:${p.profile_ref}`)
          return result
        } catch (error) {
          markRefusal(`catalog:${p.profile_ref}`, error)
          throw error
        }
      },
      enabled: canProfiles,
      staleTime: CATALOG_STALE_MS,
      refetchOnMount: 'always' as const,
      refetchInterval: (query: { state: { error: unknown } }) =>
        isRefusal(query.state.error) ? false : CATALOG_STALE_MS,
      retry: false,
    })),
  })
  // The Ollama routes admit a system administrator only, so nobody else asks. A 403 still
  // answers "not yours" (an administrator confined to one workspace): no data, no failure.
  // A step-up requirement is also a 403, but it is a refusal to retry after elevating, not
  // "not yours". The key sits under the auth boundary (a boundary move ends it) and is not
  // the AI tools page's own entry, which keeps a different shape; Start, Stop and a finished
  // download refresh it (OLLAMA_AVAILABLE_KEY). A slow poll also follows a pull whose
  // service panel was unmounted before it finished.
  const ollamaQ = useQuery({
    queryKey: [
      ...agentOpsKeys.boundaryScope(activeTenant, boundary.epoch),
      OLLAMA_AVAILABLE_KEY,
    ],
    queryFn: async ({ signal }) => {
      try {
        const result = await agentToolsApi.ollama(signal)
        clearRefusal('ollama')
        return result
      } catch (error) {
        if (
          error instanceof ApiError &&
          error.isForbidden &&
          !error.isStepUpRequired
        )
          return null
        markRefusal('ollama', error)
        throw error
      }
    },
    enabled: isSuperadmin,
    refetchInterval: (query) =>
      isRefusal(query.state.error)
        ? false
        : query.state.data?.state === 'starting'
          ? 1000
          : RUNS_POLL_MS,
  })
  const availabilityQ = useQuery({
    queryKey: modelsKeys.availability(activeTenant, boundary.epoch),
    queryFn: async ({ signal }) => {
      try {
        const result = await modelsApi.availability({
          tenant: activeTenant,
          signal,
        })
        clearRefusal('availability')
        return result
      } catch (error) {
        markRefusal('availability', error)
        throw error
      }
    },
    enabled: canAvailability,
    retry: false,
    refetchInterval: (query) => {
      if (isRefusal(query.state.error)) return false
      const advertised = Math.max(
        RUNS_POLL_MS,
        (query.state.data?.refresh_interval_seconds ?? 0) * 1000,
      )
      const checkingModels = query.state.data?.items.some(
        (source) =>
          source.provider_ref &&
          source.state === 'stale' &&
          source.models.length === 0,
      )
      if (!checkingModels) {
        checkingSince.current = null
        return advertised
      }
      // Each read is audited: the fast poll ends a few minutes after the first answer
      // that had a source still checking, and the advertised interval takes over.
      const since = (checkingSince.current ??= query.state.dataUpdatedAt)
      return query.state.dataUpdatedAt - since < CHECKING_POLL_WINDOW_MS
        ? RUNS_POLL_MS
        : advertised
    },
  })
  // Provider records supply only display names and endpoint identity. Availability renders
  // without them when this optional read is not allowed; no unproven dedupe is done then.
  const providersQ = useQuery({
    queryKey: [
      ...providerKeys.list(activeTenant, boundary.epoch, {
        state: 'active',
        limit: PROVIDER_PAGE,
      }),
      'models-available',
    ],
    queryFn: async ({ signal }) => {
      try {
        const read = (cursor?: string) =>
          providersApi.list(
            {
              state: 'active',
              limit: PROVIDER_PAGE,
              ...(cursor ? { cursor } : {}),
            },
            { signal },
          )
        let page = await read()
        const items = [...page.items]
        while (page.has_more) {
          const cursor = page.cursor
          if (!cursor)
            throw new Error('Provider list pagination cursor missing')
          page = await read(cursor)
          if (page.has_more && page.cursor === cursor)
            throw new Error('Provider list pagination did not advance')
          items.push(...page.items)
        }
        clearRefusal('providers')
        return { ...page, items }
      } catch (error) {
        markRefusal('providers', error)
        throw error
      }
    },
    enabled: canProviders,
    refetchOnMount: 'always',
  })
  const runsQ = useQuery({
    queryKey: [
      ...agentOpsKeys.runsScoped(activeTenant, boundary.epoch),
      'models-available',
    ],
    queryFn: async ({ signal }) => {
      try {
        const reads = await Promise.all(
          LIVE_RUN_STATES.map((state) =>
            agentOpsApi.listRuns(
              { state, limit: RUNS_PAGE },
              { tenant: activeTenant, signal },
            ),
          ),
        )
        // pending -> running -> idle can straddle the independent reads: one run, once.
        const items = [
          ...new Map(
            reads
              .flatMap((r) => r.items)
              .map((run) => [run.run_ref, run] as const),
          ).values(),
        ]
        clearRefusal('runs')
        return {
          items,
          has_more: reads.some((r) => r.has_more),
        }
      } catch (error) {
        markRefusal('runs', error)
        throw error
      }
    },
    enabled: canRuns,
    refetchInterval: (query) =>
      isRefusal(query.state.error) ? false : RUNS_POLL_MS,
  })

  // A source the person may not read is marked refused, so the answer cached before is not
  // painted again when the permission returns, only a fresh one. The cache itself is left
  // alone: other screens read some of the same entries.
  useEffect(() => {
    const mark = (key: string) =>
      setRefusals((previous) =>
        previous.has(key) ? previous : new Set(previous).add(key),
      )
    if (!canProfiles) {
      mark('profiles')
      for (const profile of profilesQ.data?.items ?? [])
        mark(`catalog:${profile.profile_ref}`)
    }
    if (!canRuns) mark('runs')
    if (!canAvailability) mark('availability')
    if (!canProviders) mark('providers')
    if (!isSuperadmin) mark('ollama')
  }, [
    canProfiles,
    canRuns,
    canAvailability,
    canProviders,
    isSuperadmin,
    profilesQ.data,
  ])
  const providersRefused =
    refusals.has('providers') || isRefusal(providersQ.error)
  // Every source also reads its refusal from the query's own error: the marks above are
  // lost when the tab closes, and a query that kept its data (or is not stale yet) would
  // paint the answer cached before the refusal again.
  const availabilityRefused =
    refusals.has('availability') || isRefusal(availabilityQ.error)
  const runsRefused = refusals.has('runs') || isRefusal(runsQ.error)
  const ollamaRefused = refusals.has('ollama') || isRefusal(ollamaQ.error)

  const anyLoading = [
    profilesQ,
    ollamaQ,
    availabilityQ,
    runsQ,
    ...catalogQs,
  ].some((q) => q.isLoading)
  const failed: SourceName[] = [
    ...(canProfiles &&
    ((profilesQ.isError && !isStepUp(profilesQ.error)) ||
      catalogQs.some((q) => q.isError && !isStepUp(q.error)))
      ? (['tools'] as const)
      : []),
    ...(isSuperadmin && ollamaQ.isError && !isStepUp(ollamaQ.error)
      ? (['ollama'] as const)
      : []),
    ...(canAvailability &&
    availabilityQ.isError &&
    !isStepUp(availabilityQ.error)
      ? (['availability'] as const)
      : []),
    ...(canRuns && runsQ.isError && !isStepUp(runsQ.error)
      ? (['runs'] as const)
      : []),
  ]
  const stepUpQueries = [
    ...(canProfiles ? [profilesQ, ...catalogQs] : []),
    ...(isSuperadmin ? [ollamaQ] : []),
    ...(canAvailability ? [availabilityQ] : []),
    ...(canRuns ? [runsQ] : []),
  ].filter((q) => isStepUp(q.error))
  // A tool whose list could not be read still gets its row, in the failed state; one whose
  // read is still on its way has none yet.
  // ponytail: a tool with one account that lists and another whose list is `failed` shows
  // the models of the first only; say it per account when a tenant needs that.
  const providers =
    canProviders && !providersRefused ? (providersQ.data?.items ?? []) : []
  const liveRuns = canRuns && !runsRefused ? (runsQ.data?.items ?? []) : []
  const rows = buildAvailableRows({
    catalogs: profiles.map((p, i): ToolCatalog => {
      const q = catalogQs[i]
      const base = {
        driver: p.driver,
        profileRef: p.profile_ref,
        providerRef: p.provider_record_ref,
        viaProvider: !!p.provider_record_ref,
      }
      if (refusals.has(`catalog:${p.profile_ref}`) || isRefusal(q?.error))
        return { ...base, catalog: undefined }
      if (q?.data) return { ...base, catalog: q.data }
      if (q?.isError) return { ...base, catalog: null }
      return { ...base, catalog: undefined, loading: !!q?.isLoading }
    }),
    ollama:
      isSuperadmin && !ollamaRefused ? (ollamaQ.data ?? undefined) : undefined,
    availability:
      canAvailability && !availabilityRefused
        ? (availabilityQ.data?.items ?? [])
        : [],
    providers,
    runs: liveRuns,
    profilesSettled:
      !canProfiles ||
      (!profilesRefused && (profilesQ.isSuccess || profilesQ.isError)),
  })
  const liveSessions = liveRuns.filter(isLiveRun).length
  const onRows = rows.reduce((n, r) => n + r.sessions.length, 0)
  const profilesTruncated =
    canProfiles && !profilesRefused && profilesQ.data?.has_more === true
  const names = providerNames(providers)
  const providerFailures =
    canAvailability && !availabilityRefused
      ? (availabilityQ.data?.items ?? [])
          .filter((source) => source.provider_ref && source.state === 'failed')
          .map((source) => ({
            key: source.source_ref,
            name: names.get(source.provider_ref ?? '') || source.source_ref,
          }))
      : []
  return {
    rows,
    // Rows show as soon as any source has answered; the skeleton is for before that.
    loading: anyLoading && rows.length === 0,
    failed,
    stepUpQueries,
    providerFailures,
    // Counts are a fact only while the runs read answered: not when it is not allowed,
    // still on its way, refused, or failed.
    countsKnown: canRuns && !runsRefused && runsQ.isSuccess,
    runsAllowed: canRuns && !runsRefused,
    runsQuery: runsQ,
    profilesQuery: profilesQ,
    profilesTruncated,
    // Sessions waiting for a tool list that is still loading are not "unlisted" yet.
    unmatched:
      profilesTruncated || catalogQs.some((q) => q.isLoading)
        ? 0
        : Math.max(0, liveSessions - onRows),
  }
}

function ModelCell({ row }: { row: AvailableRow }) {
  const { t } = useTranslation('models')
  if (row.source === 'ollama' && row.model === '')
    return (
      <span className="flex flex-col gap-1">
        <span className="text-muted-foreground">
          {t('available.ollamaModels')}
        </span>
        <Link
          to={'/agent-tools' as never}
          className="text-accent-text underline underline-offset-2"
        >
          {t('available.openAiTools')}
        </Link>
      </span>
    )
  if (row.source === 'provider' && row.model === '')
    return (
      <span className="text-muted-foreground">
        {t('available.providerModels')}
      </span>
    )
  if (row.model === '')
    return (
      <span className="text-muted-foreground">{t('available.ownDefault')}</span>
    )
  return (
    <span className="font-mono text-caption text-foreground" title={row.model}>
      {row.label}
    </span>
  )
}

/** The word a provider row says for its source state, or undefined for the engine's own badge. */
function providerWord(row: AvailableRow): string | undefined {
  if (row.state === 'fresh') return row.model === '' ? 'listsNone' : 'ready'
  if (row.state === 'stale' && row.model === '') return 'checkingModels'
  return undefined
}

function StateCell({ row }: { row: AvailableRow }) {
  const { t } = useTranslation('models')
  const state = row.source === 'provider' ? providerWord(row) : row.state
  if (state === undefined) return <StatusBadge status={row.state} />
  const tone =
    state === 'ready'
      ? 'success'
      : state === 'failed' || state === 'ollamaFailed'
        ? 'danger'
        : 'warning'
  return <Badge variant={tone}>{t(`available.state.${state}`)}</Badge>
}

/** One row. The sessions cell opens the sessions that run the model, each as a link. */
function ModelRow({
  row,
  countsKnown,
}: {
  row: AvailableRow
  countsKnown: boolean
}) {
  const { t } = useTranslation('models')
  const [open, setOpen] = useState(false)
  const count = row.sessions.length
  const listId = useId()
  const Chevron = open ? ChevronDown : ChevronRight
  const owner =
    row.source === 'tool' || row.source === 'session'
      ? toolName(row.owner)
      : row.source === 'ollama'
        ? 'Ollama'
        : row.owner
  // A tool that lists no models has one row with no model name: say that instead.
  const modelName = row.label || row.model || t('available.ownDefault')
  return (
    <>
      <tr>
        <td>
          <ModelCell row={row} />
          {/* On a phone the tool or provider sits under the model, so the sessions stay in view. */}
          <span className="mt-0.5 block text-caption text-muted-foreground sm:hidden">
            {owner}
          </span>
        </td>
        <td className="hidden text-muted-foreground sm:table-cell">{owner}</td>
        <td>
          <StateCell row={row} />
        </td>
        <td className="tabular-nums">
          {!countsKnown ? (
            <span className="text-muted-foreground">
              <span aria-hidden="true" title={t('available.sessionsUnknown')}>
                —
              </span>
              <span className="sr-only">{t('available.sessionsUnknown')}</span>
            </span>
          ) : count === 0 ? (
            <span className="text-muted-foreground">0</span>
          ) : (
            <Button
              variant="ghost"
              size="sm"
              aria-expanded={open}
              aria-controls={open ? listId : undefined}
              aria-label={`${t('available.sessions', { count })}, ${modelName}`}
              onClick={() => setOpen((v) => !v)}
            >
              <Chevron />
              {t('available.sessions', { count })}
            </Button>
          )}
        </td>
        <td className="text-muted-foreground">
          {t(
            `available.source.${row.viaProvider ? 'providerList' : row.source}`,
          )}
        </td>
      </tr>
      {open && count > 0 ? (
        <tr>
          <td colSpan={5} className="bg-muted/40">
            <ul
              id={listId}
              aria-label={t('available.openSessions', { model: modelName })}
              className="flex flex-col gap-1 py-1"
            >
              {row.sessions.map((run) => (
                <li key={run.run_ref}>
                  <Link
                    to={'/sessions' as never}
                    search={
                      {
                        [SESSION_PARAM]: addressFromTarget({
                          runRef: run.run_ref,
                        }),
                      } as never
                    }
                    className="text-accent-text underline underline-offset-2"
                  >
                    {run.name || run.run_ref}
                  </Link>
                </li>
              ))}
            </ul>
          </td>
        </tr>
      ) : null}
    </>
  )
}

export function AvailableTab() {
  const { t } = useTranslation('models')
  const { can, isSuperadmin } = useAuth()
  const {
    rows,
    loading,
    failed,
    stepUpQueries,
    providerFailures,
    countsKnown,
    runsAllowed,
    runsQuery,
    profilesQuery,
    profilesTruncated,
    unmatched,
  } = useAvailable()
  // Each way out is offered only to someone who can take it: a link to a page that
  // answers 403 invites a dead end.
  // Installing a model is the system administrator's (the AI tools routes admit no one else).
  const canInstall = isSuperadmin && can('sessions:account:read')
  const canAddProvider = can('sessions:provider:write')
  const sourcesFailed =
    failed.some((source) => source !== 'runs') || providerFailures.length > 0
  // Nothing here may be read: "install a model" would be advice nobody can follow.
  const noSource =
    !isSuperadmin &&
    !can('sessions:profile:read') &&
    !can('sessions:run:read') &&
    !can('models:catalog:read')

  return (
    <SectionCard
      title={t('available.title')}
      description={t('available.description')}
      noPadding
    >
      {stepUpQueries.length > 0 ? (
        <StepUpRequiredState
          action="generic"
          className="m-3"
          onElevated={() => {
            for (const query of stepUpQueries) void query.refetch()
          }}
        />
      ) : null}
      {failed.length > 0 || providerFailures.length > 0 ? (
        <div role="alert" className="flex flex-col gap-1 px-3 pt-3">
          {failed.map((source) => (
            <p key={source} className="text-caption text-danger">
              {source === 'runs'
                ? t('available.runsFailed')
                : t('available.sourceFailed', {
                    source: t(`available.sources.${source}`),
                  })}
            </p>
          ))}
          {providerFailures.map((failure) => (
            <p key={failure.key} className="text-caption text-danger">
              {t('available.sourceFailed', { source: failure.name })}
            </p>
          ))}
        </div>
      ) : null}
      {noSource ? (
        <EmptyState
          title={t('available.noAccess')}
          description={t('available.noAccessHint')}
        />
      ) : loading ? (
        <Skeleton className="m-3 h-40" />
      ) : rows.length > 0 ? (
        <XScroll
          contentKey={rows.length}
          accessibleLabel={t('available.title')}
        >
          <StaticTable>
            <thead>
              <tr>
                <th>{t('available.columns.model')}</th>
                <th className="hidden sm:table-cell">
                  {t('available.columns.runsOn')}
                </th>
                <th>{t('available.columns.state')}</th>
                <th>{t('available.columns.sessions')}</th>
                <th>{t('available.columns.source')}</th>
              </tr>
            </thead>
            <tbody>
              {rows.map((row) => (
                <ModelRow key={row.key} row={row} countsKnown={countsKnown} />
              ))}
            </tbody>
          </StaticTable>
        </XScroll>
      ) : !sourcesFailed && !profilesTruncated && stepUpQueries.length === 0 ? (
        <EmptyState
          title={t('available.empty')}
          description={t('available.emptyHint')}
          action={
            canInstall ? (
              <Button asChild variant="primary">
                <Link to={'/agent-tools' as never}>
                  <Download />
                  {t('available.installModel')}
                </Link>
              </Button>
            ) : undefined
          }
          secondaryAction={
            canAddProvider ? (
              <Button asChild variant="outline">
                <Link to={'/providers' as never}>
                  <Plus />
                  {t('available.addProvider')}
                </Link>
              </Button>
            ) : undefined
          }
        />
      ) : null}
      {countsKnown && unmatched > 0 ? (
        <p className="px-3 py-2 text-caption text-muted-foreground">
          {t('available.unmatched', { count: unmatched })}
        </p>
      ) : null}
      {profilesTruncated ? (
        <ListTruncationBadge
          query={profilesQuery}
          filas={profilesQuery.data?.items.length ?? 0}
          label={t('available.profilesTruncated', { n: 200 })}
          hint={t('available.profilesTruncated', { n: 200 })}
        />
      ) : null}
      {runsAllowed ? (
        <ListTruncationBadge
          query={runsQuery}
          filas={runsQuery.data?.items.length ?? 0}
          label={t('available.runsTruncated', { n: RUNS_PAGE })}
          hint={t('available.runsTruncated', { n: RUNS_PAGE })}
          className="pb-3"
        />
      ) : null}
    </SectionCard>
  )
}
