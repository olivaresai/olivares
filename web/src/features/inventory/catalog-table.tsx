// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useInfiniteQuery } from '@tanstack/react-query'
import { useMemo } from 'react'
import { useTranslation } from 'react-i18next'
import { DataTable, type TableColumn } from '@/components/data/data-table'
import { Badge } from '@/components/ui/badge'
import { EmptyState } from '@/components/ui/empty-state'
import { RelTimeLabel } from '@/features/shared'
import { useAuth } from '@/lib/auth/context'
import { formatInt } from '@/lib/format'
import { useWorkspaceFilter } from '@/lib/hooks/use-workspace-filter'
import { cn } from '@/lib/utils'
import { inventoryApi, inventoryKeys } from './api'
import { Box, ENTITY_ICON } from './entity-icons'
import { InvStatus } from './status'
import type { CatalogEntry } from './types'

const PAGE = 50

/**
 * CatalogTable — the navigable estate, on the shared DataTable primitive (so it
 * reuses sort / search / density / load-more). Kind & status are server facets;
 * free-text search runs client-side over the loaded rows. Stale rows are tinted so
 * a gone-quiet entity reads as a signal (docs/SECURITY-HARDENING.md), not as missing data.
 */
export function CatalogTable({
  kind,
  status,
  onSelect,
}: {
  kind?: string
  status?: string
  onSelect: (entry: CatalogEntry) => void
}) {
  const { t } = useTranslation('inventory')
  const { activeTenant } = useAuth()
  const { workspaceId, queryKey: wsKey } = useWorkspaceFilter()

  const query = useInfiniteQuery({
    queryKey: [
      ...inventoryKeys.entities(activeTenant, { kind, status }),
      wsKey,
    ],
    queryFn: ({ pageParam }) =>
      inventoryApi.entities({
        kind,
        status,
        workspace_id: workspaceId,
        limit: PAGE,
        cursor: pageParam,
      }),
    initialPageParam: undefined as string | undefined,
    getNextPageParam: (last) => (last.has_more ? last.cursor : undefined),
  })

  const rows = useMemo(
    () => query.data?.pages.flatMap((p) => p.items) ?? [],
    [query.data],
  )

  const columns = useMemo<TableColumn<CatalogEntry>[]>(
    () => [
      {
        id: 'name',
        accessorKey: 'name',
        header: t('cols.name'),
        cell: ({ row }) => {
          const e = row.original
          const Icon = ENTITY_ICON[e.kind] ?? Box
          return (
            <div className="flex items-center gap-2">
              <Icon className="size-4 shrink-0 text-muted-foreground" />
              <div className="min-w-0">
                <div className="truncate font-medium text-foreground">
                  {e.name || e.ref || e.entity_id.slice(0, 8)}
                </div>
                {e.ref && e.ref !== e.name && (
                  <div className="truncate font-mono text-xs text-muted-foreground">
                    {e.ref}
                  </div>
                )}
              </div>
            </div>
          )
        },
      },
      {
        accessorKey: 'kind',
        header: t('cols.kind'),
        cell: ({ getValue }) => (
          <Badge variant="outline">
            {t(`kinds.${getValue<string>()}`, {
              defaultValue: getValue<string>(),
            })}
          </Badge>
        ),
      },
      {
        accessorKey: 'status',
        header: t('cols.status'),
        cell: ({ getValue }) => <InvStatus status={getValue<string>()} />,
      },
      {
        id: 'signals',
        accessorFn: (e) => e.signal_sources.join(','),
        header: t('cols.signals'),
        enableSorting: false,
        cell: ({ row }) => (
          <div className="flex flex-wrap gap-1">
            {row.original.signal_sources.slice(0, 3).map((s) => (
              <Badge key={s} variant="neutral" className="font-mono">
                {s}
              </Badge>
            ))}
            {row.original.signal_sources.length > 3 && (
              <Badge variant="outline">
                +{row.original.signal_sources.length - 3}
              </Badge>
            )}
          </div>
        ),
      },
      {
        id: 'hosts',
        accessorFn: (e) => e.hosts?.length ?? 0,
        header: t('cols.hosts'),
        cell: ({ row }) => {
          const hosts = row.original.hosts ?? []
          if (hosts.length === 0)
            return <span className="text-muted-foreground">—</span>
          return (
            <span className="font-mono text-xs" title={hosts.join(', ')}>
              {hosts.length === 1
                ? hosts[0]
                : t('hostCount', { count: hosts.length })}
            </span>
          )
        },
      },
      {
        accessorKey: 'last_seen',
        header: t('cols.lastSeen'),
        cell: ({ getValue }) => <RelTimeLabel ts={getValue<string>()} />,
      },
      {
        accessorKey: 'occurrence_count',
        header: t('cols.occurrences'),
        cell: ({ getValue }) => (
          <span className="font-mono tabular-nums text-muted-foreground">
            {formatInt(getValue<number>())}
          </span>
        ),
      },
    ],
    [t],
  )

  return (
    <DataTable
      columns={columns}
      data={rows}
      isLoading={query.isLoading}
      error={query.error}
      onRetry={() => void query.refetch()}
      getRowId={(r) => `${r.kind}:${r.entity_id}`}
      onRowClick={onSelect}
      searchable
      searchPlaceholder={t('searchPlaceholder')}
      stickyHeader
      hasMore={query.hasNextPage}
      onLoadMore={() => void query.fetchNextPage()}
      isFetchingMore={query.isFetchingNextPage}
      empty={
        <EmptyState
          title={t('empty.catalog.title')}
          description={t('empty.catalog.description')}
        />
      }
    />
  )
}

/** A row classname helper exported for the topology view's reuse of the tint. */
export function staleTint(status: string): string {
  return cn(status === 'stale' && 'opacity-70')
}
