// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// ADM-IDN-02 (part 1) — NHI roster (consumes the REAL governance roster).
// NHI of Anthropic (api_key / service_account / federation_issuer) and the estate
// (ldap/idp/vault/spiffe) converge by `external_id` (= row `ref`, NOT a secret) —
// the SAME id the observed/FinOps side uses, so the access map can diff PERMITTED
// vs OBSERVED. Each row deep-links into the access map focused on that external_id.
import { useInfiniteQuery } from '@tanstack/react-query'
import { useNavigate } from '@tanstack/react-router'
import { Network } from 'lucide-react'
import { useEffect, useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { SelfAuditNotice } from '@/features/_intel'
import { StatusBadge } from '@/components/data/badges'
import { DataTable, type TableColumn } from '@/components/data/data-table'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { KvList, KvRow } from '@/components/ui/kv'
import { EmptyState } from '@/components/ui/empty-state'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
} from '@/components/ui/sheet'
import { useAuth } from '@/lib/auth/context'
import { identityApi, identityKeys } from './api'
import type { IdentityDTO } from './types'

const PRINCIPAL_TYPES = ['nhi', 'human'] as const
const PAGE = 50
const NHI_KINDS = [
  'api_key',
  'service_account',
  'federation_issuer',
  'secret_store',
] as const

function principalVariant(pt?: string) {
  if (pt === 'nhi') return 'accent' as const
  if (pt === 'human') return 'info' as const
  return 'neutral' as const
}

export function NhiRosterTab() {
  const { t } = useTranslation(['identity', 'common'])
  const { activeTenant } = useAuth()
  const [principalType, setPrincipalType] = useState<string>('all')
  const [kind, setKind] = useState<string>('all')
  const [selected, setSelected] = useState<IdentityDTO | null>(null)

  const params = useMemo(
    () => ({
      ...(kind !== 'all' ? { kind } : {}),
      limit: PAGE,
    }),
    [kind],
  )
  const roster = useInfiniteQuery({
    queryKey: identityKeys.identities(activeTenant, params),
    queryFn: ({ pageParam }) =>
      identityApi.identities({ ...params, cursor: pageParam }),
    initialPageParam: undefined as string | undefined,
    getNextPageParam: (last) => (last.has_more ? last.cursor : undefined),
    retry: false,
  })
  const {
    fetchNextPage,
    hasNextPage,
    isFetchNextPageError,
    isFetchingNextPage,
  } = roster
  const loadedPageCount = roster.data?.pages.length ?? 0

  const rows = useMemo(() => {
    const items = roster.data?.pages.flatMap((page) => page.items) ?? []
    return principalType === 'all'
      ? items
      : items.filter((i) => i.principal_type === principalType)
  }, [roster.data, principalType])

  useEffect(() => {
    if (
      principalType !== 'all' &&
      loadedPageCount > 0 &&
      rows.length === 0 &&
      hasNextPage &&
      !isFetchingNextPage &&
      !isFetchNextPageError
    ) {
      void fetchNextPage()
    }
  }, [
    fetchNextPage,
    hasNextPage,
    isFetchNextPageError,
    isFetchingNextPage,
    loadedPageCount,
    principalType,
    rows.length,
  ])

  const columns = useMemo<TableColumn<IdentityDTO>[]>(
    () => [
      {
        accessorKey: 'name',
        header: t('roster.col.name'),
        cell: ({ row }) => (
          <span className="font-medium">
            {row.original.name ?? row.original.ref}
          </span>
        ),
      },
      {
        accessorKey: 'ref',
        header: t('roster.col.externalId'),
        cell: ({ row }) => (
          <span className="font-mono text-xs break-all">
            {row.original.ref}
          </span>
        ),
      },
      {
        accessorKey: 'kind',
        header: t('roster.col.kind'),
        cell: ({ row }) =>
          row.original.kind ? (
            <Badge variant="outline">{row.original.kind}</Badge>
          ) : (
            <span className="text-muted-foreground">—</span>
          ),
      },
      {
        accessorKey: 'principal_type',
        header: t('roster.col.principalType'),
        cell: ({ row }) => (
          <Badge variant={principalVariant(row.original.principal_type)}>
            {t(`roster.principal.${row.original.principal_type ?? 'unknown'}`, {
              defaultValue: row.original.principal_type ?? '—',
            })}
          </Badge>
        ),
      },
      {
        accessorKey: 'source',
        header: t('roster.col.source'),
        cell: ({ row }) => (
          <span className="text-xs text-muted-foreground">
            {row.original.source ?? '—'}
          </span>
        ),
      },
      {
        id: 'state',
        header: t('roster.col.state'),
        enableSorting: false,
        cell: ({ row }) => (
          <StatusBadge status={row.original.disabled ? 'inactive' : 'active'} />
        ),
      },
    ],
    [t],
  )

  return (
    <div className="flex flex-col gap-4">
      <SelfAuditNotice />
      <DataTable
        columns={columns}
        data={rows}
        isLoading={roster.isLoading}
        error={roster.error}
        onRetry={() => void roster.refetch()}
        getRowId={(r) => r.id}
        onRowClick={(r) => setSelected(r)}
        searchable
        searchPlaceholder={t('roster.search')}
        label={t('roster.label')}
        hasMore={roster.hasNextPage}
        onLoadMore={() => void roster.fetchNextPage()}
        isFetchingMore={roster.isFetchingNextPage}
        toolbar={
          <div className="flex items-center gap-2">
            <Select value={principalType} onValueChange={setPrincipalType}>
              <SelectTrigger
                className="h-8 w-36"
                aria-label={t('roster.filter.principalType')}
              >
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="all">{t('roster.filter.all')}</SelectItem>
                {PRINCIPAL_TYPES.map((p) => (
                  <SelectItem key={p} value={p}>
                    {t(`roster.principal.${p}`)}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
            <Select value={kind} onValueChange={setKind}>
              <SelectTrigger
                className="h-8 w-44"
                aria-label={t('roster.filter.kind')}
              >
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="all">{t('roster.filter.all')}</SelectItem>
                {NHI_KINDS.map((k) => (
                  <SelectItem key={k} value={k}>
                    {k}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>
        }
        empty={
          <EmptyState
            title={t('empty.nhiRoster.title')}
            description={t('empty.nhiRoster.description')}
          />
        }
      />
      <RosterDetailSheet
        identity={selected}
        onClose={() => setSelected(null)}
      />
    </div>
  )
}

function RosterDetailSheet({
  identity,
  onClose,
}: {
  identity: IdentityDTO | null
  onClose: () => void
}) {
  const { t } = useTranslation(['identity', 'common'])
  const navigate = useNavigate()
  if (!identity) return null
  return (
    <Sheet open={!!identity} onOpenChange={(o) => !o && onClose()}>
      <SheetContent>
        <SheetHeader>
          <SheetTitle>{identity.name ?? identity.ref}</SheetTitle>
          <SheetDescription>
            {t(`roster.principal.${identity.principal_type ?? 'unknown'}`, {
              defaultValue: identity.principal_type ?? '',
            })}
          </SheetDescription>
        </SheetHeader>
        <div className="flex flex-col gap-4 px-4 pb-4">
          <KvList>
            <KvRow label={t('roster.col.externalId')} mono align="start">
              {identity.ref}
            </KvRow>
            <KvRow label={t('roster.col.kind')}>{identity.kind ?? '—'}</KvRow>
            <KvRow label={t('roster.col.source')}>
              {identity.source ?? '—'}
            </KvRow>
            <KvRow label={t('roster.col.state')}>
              <StatusBadge status={identity.disabled ? 'inactive' : 'active'} />
            </KvRow>
          </KvList>
          <p className="text-xs text-muted-foreground">
            {t('roster.convergenceNote')}
          </p>
          <div>
            <Button
              variant="outline"
              size="sm"
              onClick={() =>
                // Feature routes are generated dynamically from the registry, so
                // their paths are not in the statically-typed route tree; navigate
                // with a loose cast. The target reads `focus` from the URL query
                // (access-map-view.tsx) — no route/shell change needed.
                navigate({
                  to: '/access-map',
                  search: { focus: identity.ref },
                } as never)
              }
            >
              <Network className="size-4" aria-hidden />
              {t('roster.openInAccessMap')}
            </Button>
          </div>
        </div>
      </SheetContent>
    </Sheet>
  )
}
