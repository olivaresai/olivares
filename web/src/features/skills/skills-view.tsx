// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The skills catalog (modules/skills): the installed packs; a pack opens its skills,
// revisions and the departments, groups and agents it is pinned to, where it is
// assigned and unassigned. Installing a pack stays in `olivares skills install`.
import { useQuery } from '@tanstack/react-query'
import { GraduationCap } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { DataTable, type TableColumn } from '@/components/data/data-table'
import { EmptyState } from '@/components/ui/empty-state'
import { PageHeader } from '@/components/ui/page-header'
import { ListTruncationBadge } from '@/features/_intel'
import { RelTimeLabel } from '@/features/shared'
import { useAuth } from '@/lib/auth/context'
import { skillsApi, skillsKeys } from './api'
import { PackSheet, PackState } from './pack-sheet'
import type { SkillPack } from './types'
import './i18n'

export function SkillsView() {
  const { t } = useTranslation('skills')
  const { activeTenant } = useAuth()
  const [selectedId, setSelectedId] = useState<string | null>(null)
  const packs = useQuery({
    queryKey: skillsKeys.packs(activeTenant),
    queryFn: () => skillsApi.packs(),
  })
  const items = packs.data?.items ?? []
  // The sheet reads the current row, so a pack retired or revised meanwhile shows as it is.
  const selected = items.find((p) => p.id === selectedId) ?? null

  const columns: TableColumn<SkillPack, unknown>[] = [
    {
      accessorKey: 'name',
      header: t('packs.name'),
      cell: ({ row }) => (
        <span className="font-mono text-caption text-foreground">
          {row.original.name}
        </span>
      ),
    },
    {
      accessorKey: 'state',
      header: t('packs.state'),
      cell: ({ row }) => <PackState state={row.original.state} />,
    },
    {
      accessorKey: 'latest_revision',
      header: t('packs.revision'),
      cell: ({ row }) => `#${row.original.latest_revision}`,
    },
    {
      accessorKey: 'updated_at',
      header: t('packs.updated'),
      cell: ({ row }) => <RelTimeLabel ts={row.original.updated_at} />,
    },
  ]

  return (
    <div className="flex h-full flex-col gap-4">
      <PageHeader
        icon={GraduationCap}
        title={t('title')}
        description={t('subtitle')}
      />
      <ListTruncationBadge
        query={packs}
        label={t('packs.truncated', { n: items.length })}
        hint={t('packs.truncatedHint')}
        className="px-0 pt-0"
        filas={items.length}
      />
      <DataTable
        label={t('title')}
        columns={columns}
        data={items}
        isLoading={packs.isLoading}
        error={packs.error}
        onRetry={() => void packs.refetch()}
        getRowId={(row) => row.id}
        onRowClick={(row) => setSelectedId(row.id)}
        searchable
        searchPlaceholder={t('packs.search')}
        empty={
          <EmptyState
            icon={<GraduationCap />}
            title={t('packs.emptyTitle')}
            description={t('packs.emptyDescription')}
          />
        }
      />
      <PackSheet
        pack={selected}
        onOpenChange={(open) => !open && setSelectedId(null)}
      />
    </div>
  )
}
