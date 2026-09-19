// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useInfiniteQuery } from '@tanstack/react-query'
import { Link } from '@tanstack/react-router'
import { useMemo } from 'react'
import { useTranslation } from 'react-i18next'
import { DataTable, type TableColumn } from '@/components/data/data-table'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { EmptyState } from '@/components/ui/empty-state'
import { NamedRef, RelTimeLabel, useSessionNames } from '@/features/shared'
import { useAuth } from '@/lib/auth/context'
import { formatInt } from '@/lib/format'
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
 *
 * Read TENANT-WIDE: no `workspace_id`, no workspace segment in the key — the engine
 * ignores the parameter and the catalog has no workspace lineage (api.ts). The
 * DataTable renders a 403 as a calm forbidden state and any other failure with a
 * retry, never as an empty estate.
 */
export function CatalogTable({
  kind,
  status,
  onSelect,
}: {
  kind?: string
  status?: string
  onSelect: (entry: CatalogEntry, launcher?: HTMLElement) => void
}) {
  const { t } = useTranslation('inventory')
  const { t: tShared } = useTranslation('shared')
  const { activeTenant } = useAuth()
  // What a session WAS DOING, from the live page the front door already reads.
  const sessionNames = useSessionNames()

  const query = useInfiniteQuery({
    queryKey: inventoryKeys.entities(activeTenant, { kind, status }),
    queryFn: ({ pageParam }) =>
      inventoryApi.entities({
        kind,
        status,
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
        // ⛔ A SESSION IS NOT ITS REFERENCE. Every other entity here carries a name a
        //    person chose — `agent-claude-coder-7`, `appdb.public.orders`. A session
        //    materialises from the ingest stream with no name at all, so this column
        //    painted `sess-coder-7a3f`: the raw identifier the census measured on this
        //    route. What a session HAS is what it was doing, which the live page
        //    reports and the front door already tells as a sentence.
        //
        //    A session the live page does not carry, or a reader without
        //    `sessions:live:read`, gets the honest "Untitled session" with the
        //    reference beside it — never a reference dressed as a name.
        cell: ({ row }) => {
          const e = row.original
          const Icon = ENTITY_ICON[e.kind] ?? Box
          const reference = e.ref || e.name || ''
          const session =
            e.kind === 'session' ? sessionNames.nameOf(reference) : null
          const label = e.kind === 'session' ? session : e.name || e.ref
          const shown = label || (e.kind === 'session' ? '' : t('unnamed'))
          const title = [e.name, e.ref, e.entity_id]
            .filter((part) => part && part !== shown)
            .join(' · ')
          return (
            <div className="flex min-w-0 items-center gap-2">
              <Icon className="size-4 shrink-0 text-muted-foreground" />
              <NamedRef
                className="font-medium text-foreground"
                name={label}
                reference={reference}
                title={title || reference || undefined}
                fallback={
                  e.kind === 'session'
                    ? tShared('names.untitledSession')
                    : t('unnamed')
                }
              />
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
        cell: ({ row }) => {
          const sources = row.original.signal_sources
          if (sources.length === 0) {
            return <span className="text-muted-foreground">—</span>
          }
          return (
            <div className="flex items-center gap-1" title={sources.join(', ')}>
              {sources.slice(0, 2).map((s) => (
                <Badge key={s} variant="neutral" className="font-mono">
                  {s}
                </Badge>
              ))}
              {sources.length > 2 && (
                <Badge variant="outline">+{sources.length - 2}</Badge>
              )}
            </div>
          )
        },
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
            <span className="font-mono text-caption" title={hosts.join(', ')}>
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
    [t, tShared, sessionNames],
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
          action={
            <Button asChild size="sm">
              <Link to={'/capabilities' as never}>
                {t('empty.catalog.action')}
              </Link>
            </Button>
          }
        />
      }
    />
  )
}

/** A row classname helper exported for the topology view's reuse of the tint. */
export function staleTint(status: string): string {
  return cn(status === 'stale' && 'opacity-70')
}
