// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useInfiniteQuery } from '@tanstack/react-query'
import { Handshake, RefreshCcw } from 'lucide-react'
import { useId, useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { DataTable, type TableColumn } from '@/components/data/data-table'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { EmptyState } from '@/components/ui/empty-state'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { ListTruncationBadge, SectionCard } from '@/features/_intel'
import { formatDateTime } from '@/lib/format'
import { communicationsKeys, listHandoffInbox } from './api'
import type { CommunicationsScope } from './boundary'
import { Mono } from './content-blocks'
import {
  HANDOFF_STATE_FILTERS,
  PAGE_LIMIT_DEFAULT,
  PAGE_LIMITS,
  type HandoffInboxItem,
  type HandoffStateFilter,
} from './types'

/**
 * The offers addressed to this recipient, and nothing they contain.
 *
 * There is no summary column: the engine serves this collection content-free, and
 * the protected summary, next action and risk are readable only through the
 * carrier Delivery's own authorized read. A row shows the WorkItem reference,
 * sender, recipient, state, deadline and observation time; opening it opens the
 * content.
 *
 * `deadline_elapsed` is not an expiry. The engine measures it against database
 * time and this read never runs the reaper, so a row can be persisted `offered`
 * past its deadline. The badge says the window elapsed; only `state` says expired.
 *
 * The filter is a single server state, `offered` first. Changing it, the page
 * size, the workspace or the authority resets the continuation chain: the engine
 * mints continuations per filter domain and refuses a token minted for another.
 *
 * Global administrators cannot be recipients of personal handoffs. Show account
 * guidance without fetching or rendering cached member rows for that account.
 */
export function HandoffInbox({
  scope,
  canDeliveryRead,
  globalSuperadminAccount,
  onOpenHandoff,
}: {
  scope: CommunicationsScope
  canDeliveryRead: boolean
  /** True only when the current authenticated principal explicitly says so. */
  globalSuperadminAccount: boolean
  onOpenHandoff: (deliveryId: string) => void
}) {
  const { t, i18n } = useTranslation('communications')
  const idp = useId()
  const workspace = scope.workspace ?? ''
  const tenant = scope.tenant
  const [state, setState] = useState<HandoffStateFilter>('offered')
  const [limit, setLimit] = useState<number>(PAGE_LIMIT_DEFAULT)

  const queryKey = communicationsKeys.handoffs(tenant, scope.epoch, workspace, {
    state,
    limit,
  })
  const query = useInfiniteQuery({
    queryKey,
    queryFn: ({ signal, pageParam }) =>
      listHandoffInbox(
        { workspace_id: workspace, state, limit, continuation: pageParam },
        { tenant },
        signal,
      ),
    initialPageParam: undefined as string | undefined,
    getNextPageParam: (last) =>
      last.has_more && last.continuation ? last.continuation : undefined,
    enabled: canDeliveryRead && workspace !== '' && !globalSuperadminAccount,
  })

  const pages = useMemo(() => query.data?.pages ?? [], [query.data])
  const rows = useMemo(() => pages.flatMap((p) => p.items), [pages])
  const lastPage = pages[pages.length - 1]

  const columns = useMemo<TableColumn<HandoffInboxItem>[]>(
    () => [
      {
        id: 'workItem',
        accessorFn: (r) => r.work_item.id,
        header: t('handoff.inbox.columns.workItem'),
        cell: ({ row }) => <Mono>{row.original.work_item.id}</Mono>,
      },
      {
        id: 'from',
        accessorFn: (r) => `${r.handoff.from.kind}:${r.handoff.from.ref}`,
        header: t('handoff.inbox.columns.from'),
        cell: ({ row }) => (
          <Mono>
            {row.original.handoff.from.kind}:{row.original.handoff.from.ref}
          </Mono>
        ),
      },
      {
        id: 'to',
        accessorFn: (r) => `${r.handoff.to.kind}:${r.handoff.to.ref}`,
        header: t('handoff.inbox.columns.to'),
        cell: ({ row }) => (
          <Mono>
            {row.original.handoff.to.kind}:{row.original.handoff.to.ref}
          </Mono>
        ),
      },
      {
        id: 'state',
        accessorFn: (r) => r.handoff.state,
        header: t('handoff.inbox.columns.state'),
        cell: ({ row }) => (
          <span className="flex flex-wrap items-center gap-1">
            <Badge variant="neutral">
              {t(`handoff.state.${row.original.handoff.state}`, {
                defaultValue: row.original.handoff.state,
              })}
            </Badge>
            {row.original.deadline_elapsed ? (
              <Badge variant="warning" title={t('handoff.inbox.elapsedHint')}>
                {t('handoff.inbox.elapsed')}
              </Badge>
            ) : null}
          </span>
        ),
      },
      {
        id: 'deadline',
        accessorFn: (r) => r.handoff.ack_deadline,
        header: t('handoff.inbox.columns.deadline'),
        cell: ({ row }) =>
          formatDateTime(row.original.handoff.ack_deadline, i18n.language),
      },
      {
        id: 'observed',
        accessorFn: (r) => r.observed_at,
        header: t('handoff.inbox.columns.observed'),
        cell: ({ row }) =>
          formatDateTime(row.original.observed_at, i18n.language),
      },
      {
        id: 'delivery',
        accessorFn: (r) => r.carrier.delivery_id,
        header: t('handoff.inbox.columns.delivery'),
        cell: ({ row }) => <Mono>{row.original.carrier.delivery_id}</Mono>,
      },
    ],
    [t, i18n.language],
  )

  return (
    <SectionCard
      title={t('handoff.inbox.title')}
      description={t('handoff.inbox.description')}
      noPadding
      actions={
        <div className="flex flex-wrap items-center gap-2">
          <Select
            value={state}
            onValueChange={(v) => setState(v as HandoffStateFilter)}
            disabled={globalSuperadminAccount}
          >
            <SelectTrigger
              className="w-36"
              aria-label={t('handoff.inbox.filter')}
              id={`${idp}-state`}
            >
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {HANDOFF_STATE_FILTERS.map((s) => (
                <SelectItem key={s} value={s}>
                  {t(`handoff.state.${s}`)}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
          <Select
            value={String(limit)}
            onValueChange={(v) => setLimit(Number(v))}
            disabled={globalSuperadminAccount}
          >
            <SelectTrigger
              className="w-28"
              aria-label={t('actions.pageSize')}
              id={`${idp}-limit`}
            >
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {PAGE_LIMITS.map((n) => (
                <SelectItem key={n} value={String(n)}>
                  {n}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
          <Button
            variant="outline"
            size="sm"
            // Manual refetch ignores the query's enabled flag.
            onClick={() => {
              if (globalSuperadminAccount) return
              void query.refetch()
            }}
            disabled={!canDeliveryRead || globalSuperadminAccount}
          >
            <RefreshCcw className="size-4" aria-hidden="true" />
            {t('actions.refresh')}
          </Button>
        </div>
      }
    >
      {globalSuperadminAccount ? (
        // Unmount the table so cached member rows cannot be displayed or opened.
        <div
          data-slot="handoff-account-guidance"
          role="status"
          aria-live="polite"
          className="space-y-1 px-3 py-6"
        >
          <p className="text-body font-medium text-foreground">
            {t('handoff.inbox.accountGuidanceTitle')}
          </p>
          <p className="max-w-prose text-body text-muted-foreground">
            {t('handoff.inbox.accountGuidanceBody')}
          </p>
          <p className="max-w-prose text-caption text-muted-foreground">
            {t('handoff.inbox.accountGuidanceHint')}
          </p>
        </div>
      ) : (
        <>
          <ListTruncationBadge
            query={{ data: lastPage, error: query.error }}
            label={t('handoff.inbox.truncated')}
            hint={t('handoff.inbox.truncatedHint')}
            filas={rows.length}
          />
          <DataTable
            columns={columns}
            data={rows}
            isLoading={query.isLoading}
            error={query.error}
            onRetry={() => void query.refetch()}
            unavailableTitle={t('handoff.inbox.unavailableTitle')}
            unavailableDescription={t('handoff.inbox.unavailableBody')}
            getRowId={(r) => r.carrier.delivery_id}
            onRowClick={(r) => onOpenHandoff(r.carrier.delivery_id)}
            label={t('handoff.inbox.title')}
            hasMore={query.hasNextPage}
            onLoadMore={() => void query.fetchNextPage()}
            isFetchingMore={query.isFetchingNextPage}
            empty={
              <EmptyState
                icon={<Handshake />}
                title={t('handoff.inbox.emptyTitle', {
                  state: t(`handoff.state.${state}`),
                })}
                description={t('handoff.inbox.emptyBody')}
              />
            }
          />
          <p className="border-t border-border px-3 py-2 text-caption text-muted-foreground">
            {t('handoff.inbox.contentFree')}
          </p>
        </>
      )}
    </SectionCard>
  )
}
