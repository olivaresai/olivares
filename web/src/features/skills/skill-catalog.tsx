// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useQueries } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { DataTable, type TableColumn } from '@/components/data/data-table'
import { QueryErrorState } from '@/components/layout/query-error-state'
import { EmptyState } from '@/components/ui/empty-state'
import { useAuth } from '@/lib/auth/context'
import { useAuthBoundary } from '@/features/agentops/auth-boundary'
import { skillsApi, skillsKeys } from './api'
import { useDirectory } from './use-directory'
import { pinHolds, type SkillMember, type SkillPack } from './types'
import './i18n'

interface CatalogRow extends SkillMember {
  pack: SkillPack
  revision: number
}

export function SkillCatalog({
  packs,
  onOpen,
}: {
  packs: SkillPack[]
  onOpen: (pack: string, skill?: string) => void
}) {
  const { t } = useTranslation('skills')
  const { activeTenant } = useAuth()
  const boundary = useAuthBoundary()
  const directory = useDirectory(activeTenant)
  const details = useQueries({
    queries: packs.map((pack) => ({
      queryKey: skillsKeys.pack(activeTenant, pack.id),
      queryFn: () => skillsApi.pack(pack.id),
    })),
  })
  const assignments = useQueries({
    queries: packs.map((pack) => ({
      queryKey: skillsKeys.packAssignments(
        activeTenant,
        pack.id,
        boundary.epoch,
      ),
      queryFn: ({ signal }: { signal: AbortSignal }) =>
        skillsApi.packAssignments(pack.id, { signal }),
    })),
  })
  const rows: CatalogRow[] = details.flatMap((q, i) => {
    const latest = q.data?.revisions.find(
      (r) => r.id === packs[i].latest_revision_id,
    )
    return (latest?.members ?? []).map((member) => ({
      ...member,
      pack: packs[i],
      revision: latest!.number,
    }))
  })
  const columns: TableColumn<CatalogRow>[] = [
    {
      accessorKey: 'name',
      header: t('catalog.name'),
      cell: ({ row }) => (
        <span className="break-words font-medium">{row.original.name}</span>
      ),
    },
    {
      accessorKey: 'description',
      header: t('catalog.description'),
      cell: ({ row }) => (
        <span className="whitespace-normal break-words">
          {row.original.description}
        </span>
      ),
    },
    {
      id: 'kind',
      accessorFn: (row) => row.kind ?? 'skill',
      header: t('catalog.kind'),
      cell: ({ row }) => t(`catalog.${row.original.kind ?? 'skill'}`),
    },
    {
      id: 'pack',
      accessorFn: (row) => row.pack.name,
      header: t('packs.name'),
      cell: ({ row }) => (
        <button
          type="button"
          className="text-accent-text underline underline-offset-4"
          onClick={(event) => {
            event.stopPropagation()
            onOpen(row.original.pack.id)
          }}
        >
          {row.original.pack.name}
        </button>
      ),
    },
    {
      accessorKey: 'revision',
      header: t('packs.revision'),
      cell: ({ row }) => `#${row.original.revision}`,
    },
    {
      id: 'uses',
      header: t('uses.title'),
      cell: ({ row }) => {
        const i = packs.findIndex((pack) => pack.id === row.original.pack.id)
        const query = assignments[i]
        if (query.isPending) return t('catalog.loading')
        if (query.isError)
          return (
            <QueryErrorState
              error={query.error}
              retry={() => void query.refetch()}
            />
          )
        const pins = query.data.items.filter((pin) =>
          pinHolds(pin, details[i].data?.revisions ?? [], row.original.name),
        )
        return (
          <div className="flex flex-col gap-1 whitespace-normal break-words">
            {pins.length === 0 &&
            !query.data.has_more &&
            !details[i].data?.has_more_revisions
              ? t('catalog.unassigned')
              : pins.map((pin) => (
                  <span key={pin.id}>
                    {t(`kind.${pin.target_kind}`)}:{' '}
                    {directory[pin.target_kind]?.items.find(
                      (item) => item.id === pin.target_id,
                    )?.name ?? pin.target_id}{' '}
                    (
                    {t('detail.revision', {
                      n:
                        details[i].data?.revisions.find(
                          (r) => r.id === pin.pack_revision_id,
                        )?.number ?? '?',
                    })}
                    )
                  </span>
                ))}
            {query.data.has_more && (
              <span role="status">
                {t('uses.truncated', { n: query.data.items.length })}
              </span>
            )}
            {details[i].data?.has_more_revisions && (
              <span role="status">{t('catalog.incomplete')}</span>
            )}
          </div>
        )
      },
    },
  ]
  const failed = details.find((q) => q.isError)
  return (
    <div className="flex min-h-0 flex-1 flex-col gap-3">
      {details.some((q) => q.data?.has_more_revisions) && (
        <p role="status" className="text-caption text-muted-foreground">
          {t('catalog.incomplete')}
        </p>
      )}
      {failed && (
        <QueryErrorState
          error={failed.error}
          retry={() => {
            for (const q of details) if (q.isError) void q.refetch()
          }}
        />
      )}
      <DataTable
        label={t('detail.skills')}
        columns={columns}
        data={rows}
        empty={
          <EmptyState
            title={t('common:states.empty')}
            description={t('packs.emptyDescription')}
          />
        }
        isLoading={details.some((q) => q.isPending)}
        searchable
        searchPlaceholder={t('catalog.search')}
        getRowId={(row) => `${row.pack.id}/${row.name}`}
        onRowClick={(row) => onOpen(row.pack.id, row.name)}
      />
    </div>
  )
}
