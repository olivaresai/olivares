// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useInfiniteQuery, useQueryClient } from '@tanstack/react-query'
import { useEffect, useRef, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { StepUpRequiredState } from '@/components/layout/step-up-state'
import { Button } from '@/components/ui/button'
import { ErrorState, ForbiddenState } from '@/components/ui/error-state'
import { KvList, KvRow } from '@/components/ui/kv'
import { Skeleton } from '@/components/ui/skeleton'
import { CaveatNotice } from '@/features/_intel'
import { ApiError, NetworkError } from '@/lib/api/errors'
import { formatInt } from '@/lib/format'
import { inventoryApi, inventoryKeys, type ObservationItem } from './api'

const HISTORY_POLICY = {
  staleTime: 0,
  gcTime: 0,
  retry: false as const,
  refetchOnWindowFocus: false,
  refetchOnReconnect: false,
}

/**
 * ObservationHistory — the C3 consumer for one selected catalog entity.
 *
 * Interface: tenant + kind + id. The caller mounts this only while that
 * selection is live and unmounts it on close, tenant change or permission
 * withdrawal. Implementation owns the closed page of 25, cursor-as-pageParam
 * transport, local page guards, error-over-rows, and presentation. Catalog
 * freshness is a different read; this list is not a coverage figure.
 */
export function ObservationHistory({
  tenant,
  kind,
  id,
}: {
  tenant: string | null
  kind: string
  id: string
}) {
  const { t } = useTranslation('inventory')
  const queryClient = useQueryClient()
  const queryKey = inventoryKeys.observations(tenant, kind, id)
  const errorBoxRef = useRef<HTMLDivElement>(null)

  const query = useInfiniteQuery({
    queryKey,
    queryFn: ({ pageParam, signal }) =>
      inventoryApi.observations(kind, id, {
        tenant,
        signal,
        ...(typeof pageParam === 'string' && pageParam !== ''
          ? { cursor: pageParam }
          : {}),
      }),
    initialPageParam: undefined as string | undefined,
    getNextPageParam: (last) =>
      last.has_more === true ? last.cursor : undefined,
    ...HISTORY_POLICY,
  })

  const restart = () => {
    void queryClient.resetQueries({ queryKey })
  }

  useEffect(() => {
    if (!query.isError) return
    errorBoxRef.current?.querySelector('button')?.focus()
  }, [query.isError])

  const pendingFirst =
    query.isLoading ||
    (query.isFetching && !query.isFetchingNextPage && query.data === undefined)

  if (query.isError) {
    return (
      <HistoryFrame title={t('history.title')}>
        <div ref={errorBoxRef}>
          <HistoryFailure error={query.error} onRestart={restart} />
        </div>
      </HistoryFrame>
    )
  }

  if (pendingFirst) {
    return (
      <HistoryFrame title={t('history.title')}>
        <div role="status" aria-busy="true">
          <span className="sr-only">{t('history.loading')}</span>
          <Skeleton className="h-24 w-full" />
        </div>
      </HistoryFrame>
    )
  }

  const items = query.data?.pages.flatMap((p) => p.items) ?? []
  const lastPage = query.data?.pages.at(-1)
  const canLoadMore =
    lastPage?.has_more === true && usable(lastPage.cursor) !== undefined
  const loadMorePending = query.isFetchingNextPage

  return (
    <HistoryFrame title={t('history.title')}>
      <CaveatNotice tone="info">{t('history.coverageUnknown')}</CaveatNotice>
      {items.length === 0 ? (
        <p role="status" className="mt-3 text-body text-muted-foreground">
          {t('history.empty')}
        </p>
      ) : (
        <>
          <p role="status" className="mt-3 text-caption text-muted-foreground">
            {t('history.loaded', { count: items.length })}
          </p>
          <ul className="mt-2 flex flex-col gap-3">
            {items.map((item) => (
              <li key={item.receipt_id}>
                <ObservationCard item={item} />
              </li>
            ))}
          </ul>
        </>
      )}
      {loadMorePending ? (
        <p role="status" aria-busy="true" className="mt-3 text-caption">
          {t('history.loadingMore')}
        </p>
      ) : null}
      <div className="mt-3 flex flex-wrap gap-2">
        <Button
          type="button"
          variant="secondary"
          size="sm"
          onClick={restart}
          disabled={query.isFetching}
        >
          {t('history.refresh')}
        </Button>
        {canLoadMore ? (
          <Button
            type="button"
            variant="secondary"
            size="sm"
            onClick={() => {
              if (query.isFetchingNextPage || query.isFetching) return
              void query.fetchNextPage()
            }}
            disabled={query.isFetchingNextPage || query.isFetching}
          >
            {t('history.loadMore')}
          </Button>
        ) : null}
      </div>
    </HistoryFrame>
  )
}

function HistoryFrame({
  title,
  children,
}: {
  title: string
  children: ReactNode
}) {
  const headingId = 'inventory-observation-history'
  return (
    <section className="mt-4 min-w-0" aria-labelledby={headingId}>
      <h3
        id={headingId}
        className="mb-2 text-caption font-semibold uppercase tracking-wide text-muted-foreground"
      >
        {title}
      </h3>
      {children}
    </section>
  )
}

function HistoryFailure({
  error,
  onRestart,
}: {
  error: unknown
  onRestart: () => void
}) {
  const { t } = useTranslation(['inventory', 'errors', 'common'])
  if (error instanceof ApiError && error.isNotFound) {
    return (
      <p role="status" className="text-caption text-muted-foreground">
        {t('inventory:detail.gone')}
      </p>
    )
  }
  if (error instanceof ApiError && error.isStepUpRequired) {
    return (
      <StepUpRequiredState action="generic" onElevated={() => onRestart()} />
    )
  }
  if (error instanceof ApiError && error.isForbidden) {
    return (
      <div className="flex flex-col items-center gap-3">
        <ForbiddenState
          title={t('errors:forbidden.title')}
          description={t('errors:forbidden.description')}
        />
        <Button type="button" variant="secondary" size="sm" onClick={onRestart}>
          {t('common:actions.retry')}
        </Button>
      </div>
    )
  }
  const isNetwork = error instanceof NetworkError
  return (
    <ErrorState
      title={
        isNetwork ? t('errors:network.title') : t('errors:serverError.title')
      }
      description={
        isNetwork
          ? t('errors:network.description')
          : t('errors:serverError.description')
      }
      retry={onRestart}
      requestId={error instanceof ApiError ? error.requestId : undefined}
    />
  )
}

function ObservationCard({ item }: { item: ObservationItem }) {
  const { t } = useTranslation('inventory')
  const eventLabel =
    item.event_type === 'edge.observed'
      ? t('history.eventTypeEdge')
      : item.event_type === 'cost.sampled'
        ? t('history.eventTypeCost')
        : item.event_type
  const registration = item.registration
  return (
    <article className="min-w-0 rounded-md border border-border p-3">
      <h4 className="text-body font-medium text-foreground">
        {t('history.receipt')}
      </h4>
      <KvList>
        <KvRow label={t('history.receipt')} mono align="start">
          <span className="break-all">{item.receipt_id}</span>
        </KvRow>
        <KvRow label={t('history.eventType')} align="start">
          {eventLabel}
        </KvRow>
        <KvRow label={t('history.registration')} align="start">
          <RegistrationBlock registration={registration} />
        </KvRow>
        <KvRow label={t('history.sourceOccurred')} align="start">
          <EvidenceTime
            ts={item.source_occurred_at}
            unknown={t('history.sourceOccurredUnknown')}
          />
        </KvRow>
        <KvRow label={t('history.firstReceived')} align="start">
          <EvidenceTime
            ts={item.first_received_at}
            unknown={t('history.timeUnknown')}
          />
        </KvRow>
        <KvRow label={t('history.lastReceived')} align="start">
          <EvidenceTime
            ts={item.last_received_at}
            unknown={t('history.timeUnknown')}
          />
        </KvRow>
        <KvRow label={t('history.deliveries')} mono>
          {formatInt(item.deliveries)}
        </KvRow>
        <KvRow label={t('history.conflict')} align="start">
          <span>
            {item.conflicting_redelivery
              ? t('history.conflictYes')
              : t('history.conflictNo')}
          </span>
        </KvRow>
      </KvList>
    </article>
  )
}

function RegistrationBlock({
  registration,
}: {
  registration: ObservationItem['registration']
}) {
  const { t } = useTranslation('inventory')
  if (registration.registration_state === 'registered_snapshot') {
    return (
      <div className="flex min-w-0 flex-col gap-1">
        <span>{t('history.registeredSnapshot')}</span>
        <span className="break-all font-mono text-caption">
          {t('history.sourceId')}: {registration.source_id}
        </span>
        <span className="break-all font-mono text-caption">
          {t('history.sourceRevision')}:{' '}
          {formatInt(registration.source_revision)}
        </span>
        <span className="break-all font-mono text-caption">
          {t('history.environmentRef')}: {registration.environment_ref}
        </span>
      </div>
    )
  }
  if (registration.registration_state === 'invalid') {
    return <span>{t('history.invalid')}</span>
  }
  return <span>{t('history.unattributed')}</span>
}

/** Visible absolute time with zone and original RFC3339. Missing/invalid values
 *  are not replaced by reception time or the clock. */
export function EvidenceTime({
  ts,
  unknown,
}: {
  ts?: string
  unknown: string
}) {
  const { i18n } = useTranslation()
  if (ts === undefined || ts === '') {
    return <span>{unknown}</span>
  }
  const d = new Date(ts)
  if (Number.isNaN(d.getTime())) {
    return (
      <time dateTime={ts} className="break-all">
        {ts}
      </time>
    )
  }
  let abs: string
  try {
    abs = new Intl.DateTimeFormat(i18n.language, {
      year: 'numeric',
      month: 'short',
      day: 'numeric',
      hour: 'numeric',
      minute: '2-digit',
      second: '2-digit',
      timeZoneName: 'short',
    }).format(d)
  } catch {
    abs = ts
  }
  return (
    <time dateTime={ts} className="min-w-0 break-words">
      <span>{abs}</span>
      <span className="mt-0.5 block break-all text-caption text-muted-foreground">
        {ts}
      </span>
    </time>
  )
}

function usable(value: unknown): string | undefined {
  return typeof value === 'string' && value !== '' ? value : undefined
}
