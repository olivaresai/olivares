// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useInfiniteQuery } from '@tanstack/react-query'
import { MessagesSquare, Plus, RefreshCcw } from 'lucide-react'
import { useId, useMemo, useState, type FormEvent } from 'react'
import { useTranslation } from 'react-i18next'
import { DataTable, type TableColumn } from '@/components/data/data-table'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { EmptyState } from '@/components/ui/empty-state'
import { Input } from '@/components/ui/input'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { ListTruncationBadge, SectionCard } from '@/features/_intel'
import { formatDateTime } from '@/lib/format'
import { communicationsKeys, listChannels } from './api'
import type { CommunicationsScope } from './boundary'
import { AccessBadges } from './channel-sheet'
import { Mono } from './content-blocks'
import {
  PAGE_LIMIT_DEFAULT,
  PAGE_LIMITS,
  type ChannelCatalogItem,
} from './types'

const ID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i

/**
 * ChannelCatalog — the visible page of `GET /channels`, with an EXPLICIT page size
 * and "load more" on the server's own continuation. It is not a search over the
 * whole catalog and it says so; the table filter narrows the loaded rows only.
 */
export function ChannelCatalog({
  scope,
  canChannelRead,
  canChannelWrite,
  onOpenChannel,
  onOpenById,
  onCreate,
}: {
  scope: CommunicationsScope
  canChannelRead: boolean
  canChannelWrite: boolean
  onOpenChannel: (item: ChannelCatalogItem) => void
  onOpenById: (id: string) => void
  onCreate: () => void
}) {
  const { t, i18n } = useTranslation('communications')
  const idp = useId()
  const workspace = scope.workspace ?? ''
  const tenant = scope.tenant
  const [limit, setLimit] = useState<number>(PAGE_LIMIT_DEFAULT)
  const [byId, setById] = useState('')

  const query = useInfiniteQuery({
    queryKey: communicationsKeys.catalog(tenant, scope.epoch, workspace, {
      limit,
    }),
    queryFn: ({ signal, pageParam }) =>
      listChannels(
        { workspace_id: workspace, limit, continuation: pageParam },
        { tenant },
        signal,
      ),
    initialPageParam: undefined as string | undefined,
    getNextPageParam: (last) =>
      last.has_more && last.continuation ? last.continuation : undefined,
    enabled: canChannelRead && workspace !== '',
  })

  const rows = useMemo(
    () => query.data?.pages.flatMap((p) => p.items) ?? [],
    [query.data],
  )
  const lastPage = query.data?.pages[query.data.pages.length - 1]

  const columns = useMemo<TableColumn<ChannelCatalogItem>[]>(
    () => [
      {
        id: 'name',
        accessorKey: 'name',
        header: t('catalog.columns.name'),
        cell: ({ row }) => (
          <span className="font-medium">{row.original.name}</span>
        ),
      },
      {
        id: 'slug',
        accessorKey: 'slug',
        header: t('catalog.columns.slug'),
        cell: ({ row }) => <Mono>{row.original.slug}</Mono>,
      },
      {
        id: 'kind',
        accessorKey: 'kind',
        header: t('catalog.columns.kind'),
        cell: ({ row }) => (
          <Badge variant="outline">
            {t(`kind.${row.original.kind}`, {
              defaultValue: row.original.kind,
            })}
          </Badge>
        ),
      },
      {
        id: 'state',
        accessorKey: 'state',
        header: t('catalog.columns.state'),
        cell: ({ row }) => (
          <Badge variant="neutral">{row.original.state}</Badge>
        ),
      },
      {
        id: 'protection',
        accessorKey: 'content_protection',
        header: t('catalog.columns.protection'),
        cell: ({ row }) =>
          t(`protection.${row.original.content_protection}`, {
            defaultValue: row.original.content_protection,
          }),
      },
      {
        id: 'access',
        header: t('catalog.columns.access'),
        cell: ({ row }) => <AccessBadges access={row.original.my_access} />,
      },
      {
        id: 'updated',
        accessorKey: 'updated_at',
        header: t('catalog.columns.updated'),
        cell: ({ row }) =>
          formatDateTime(row.original.updated_at, i18n.language),
      },
    ],
    [t, i18n.language],
  )

  const openById = (e: FormEvent) => {
    e.preventDefault()
    const id = byId.trim()
    if (ID.test(id)) onOpenById(id)
  }

  return (
    <SectionCard
      title={t('catalog.title')}
      description={t('catalog.description')}
      noPadding
      actions={
        <div className="flex flex-wrap items-center gap-2">
          <Select
            value={String(limit)}
            onValueChange={(v) => setLimit(Number(v))}
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
            onClick={() => void query.refetch()}
            disabled={!canChannelRead}
          >
            <RefreshCcw className="size-4" aria-hidden="true" />
            {t('actions.refresh')}
          </Button>
          {canChannelWrite ? (
            <Button size="sm" onClick={onCreate}>
              <Plus className="size-4" aria-hidden="true" />
              {t('actions.newChannel')}
            </Button>
          ) : null}
        </div>
      }
    >
      <form
        onSubmit={openById}
        className="flex flex-wrap items-end gap-2 px-3 pt-3"
      >
        <label
          htmlFor={`${idp}-by-id`}
          className="text-caption text-muted-foreground"
        >
          {t('catalog.openById')}
        </label>
        <Input
          id={`${idp}-by-id`}
          value={byId}
          onChange={(e) => setById(e.target.value)}
          className="w-80"
          autoComplete="off"
          mono
        />
        <Button
          type="submit"
          variant="outline"
          size="sm"
          disabled={!ID.test(byId.trim()) || !canChannelRead}
        >
          {t('actions.open')}
        </Button>
        <span className="basis-full text-caption text-muted-foreground">
          {t('catalog.openByIdHint')}
        </span>
      </form>
      <ListTruncationBadge
        query={{ data: lastPage, error: query.error }}
        label={t('catalog.truncated')}
        hint={t('catalog.truncatedHint')}
        filas={rows.length}
      />
      <DataTable
        columns={columns}
        data={rows}
        isLoading={query.isLoading}
        error={query.error}
        onRetry={() => void query.refetch()}
        getRowId={(r) => r.id}
        onRowClick={onOpenChannel}
        searchable
        label={t('catalog.title')}
        hasMore={query.hasNextPage}
        onLoadMore={() => void query.fetchNextPage()}
        isFetchingMore={query.isFetchingNextPage}
        empty={
          <EmptyState
            icon={<MessagesSquare />}
            title={t('catalog.emptyTitle')}
            description={t('catalog.emptyBody')}
          />
        }
      />
    </SectionCard>
  )
}
