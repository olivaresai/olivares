// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The skills catalog (modules/skills): the installed packs; a pack opens its skills,
// revisions and the departments, groups and agents it is pinned to, where it is
// assigned and unassigned. Built-in and Git packs share the immutable import API.
import { useQuery } from '@tanstack/react-query'
import { GraduationCap } from 'lucide-react'
import { useEffect } from 'react'
import { useTranslation } from 'react-i18next'
import { DataTable, type TableColumn } from '@/components/data/data-table'
import { EmptyState } from '@/components/ui/empty-state'
import { PageHeader } from '@/components/ui/page-header'
import { ListTruncationBadge } from '@/features/_intel'
import { RelTimeLabel } from '@/features/shared'
import { useAuth } from '@/lib/auth/context'
import { useUrlState } from '@/lib/hooks/use-url-state'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { QueryErrorState } from '@/components/layout/query-error-state'
import { skillsApi, skillsKeys } from './api'
import { InstallPack } from './install-pack'
import { PackSheet, PackState } from './pack-sheet'
import { SkillCatalog } from './skill-catalog'
import type { SkillPack } from './types'
import './i18n'

/** The skill-pack catalog. `titled` is its own page header; on MCP & skills' Skills tab
 * (#3388) the page's title already names it. */
export function SkillsView({ titled = true }: { titled?: boolean } = {}) {
  const { t } = useTranslation('skills')
  const { activeTenant } = useAuth()
  // `?pack=<id>` is the open pack and `?skill=<name>` the skill inside it (AI tools >
  // Installed here links each skill here), `?catalog=packs` the pack list; a link, a
  // reload and Back all show them. Opening one is a place Back can leave (push); closing
  // it and switching the list replace, so Back never reopens a sheet the person dismissed.
  const [url, patchUrl] = useUrlState(['pack', 'skill', 'catalog'])
  const open = (pack: string, skill?: string) =>
    patchUrl({ pack, skill }, { history: 'push' })
  const packs = useQuery({
    queryKey: skillsKeys.packs(activeTenant),
    queryFn: () => skillsApi.packs(),
  })
  const items = packs.data?.items ?? []
  // The sheet reads the current row, so a pack retired or revised meanwhile shows as it is.
  const selected = items.find((p) => p.id === url.pack) ?? null
  // A pack the catalog does not hold (retired, another tenant) opens nothing and must not
  // stay in a link: clear it once the list has answered, never while it loads or fails.
  const packGone = !!url.pack && packs.isSuccess && selected === null
  useEffect(() => {
    if (packGone) patchUrl({ pack: undefined, skill: undefined })
  }, [packGone, patchUrl])

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
      {titled ? (
        <PageHeader
          icon={GraduationCap}
          title={t('title')}
          description={t('subtitle')}
        />
      ) : null}
      <InstallPack key={activeTenant} packs={items} />
      <ListTruncationBadge
        query={packs}
        label={t('packs.truncated', { n: items.length })}
        hint={t('packs.truncatedHint')}
        className="px-0 pt-0"
        filas={items.length}
      />
      {packs.error ? (
        <QueryErrorState
          error={packs.error}
          retry={() => void packs.refetch()}
        />
      ) : (
        <Tabs
          value={url.catalog === 'packs' ? 'packs' : 'skills'}
          onValueChange={(catalog) =>
            patchUrl({ catalog }, { history: 'replace' })
          }
          className="flex min-h-0 flex-1 flex-col"
        >
          <TabsList aria-label={t('title')}>
            <TabsTrigger value="skills">{t('catalog.skills')}</TabsTrigger>
            <TabsTrigger value="packs">{t('catalog.packs')}</TabsTrigger>
          </TabsList>
          <TabsContent value="skills" className="min-h-0 flex-1">
            <SkillCatalog packs={items} onOpen={open} />
          </TabsContent>
          <TabsContent value="packs" className="min-h-0 flex-1">
            <DataTable
              label={t('title')}
              columns={columns}
              data={items}
              isLoading={packs.isLoading}
              error={packs.error}
              onRetry={() => void packs.refetch()}
              getRowId={(row) => row.id}
              onRowClick={(row) => open(row.id)}
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
          </TabsContent>
        </Tabs>
      )}
      <PackSheet
        pack={selected}
        skill={url.skill}
        onOpenChange={(open) =>
          !open && patchUrl({ pack: undefined, skill: undefined })
        }
      />
    </div>
  )
}
