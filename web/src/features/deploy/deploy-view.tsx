// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { Plus, RefreshCw, Rocket } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { PageHeader } from '@/components/ui/page-header'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { EmptyState } from '@/components/ui/empty-state'
import { DataTable, type TableColumn } from '@/components/data/data-table'
import { StatusBadge } from '@/components/data/badges'
import { useAuth } from '@/lib/auth/context'
import { deployApi, deployKeys } from './api'
import { DefinitionDetailSheet } from './definition-detail'
import { DefinitionEditorDialog } from './definition-editor'
import { OperationsTable } from './operations-table'
import { WiringsTable } from './wirings-table'
import './i18n'
import type { DefinitionDTO } from './types'
import { ListTruncationBadge } from '@/features/_intel'

type TabKey = 'definitions' | 'wirings' | 'operations'

export default function DeployView() {
  const { t } = useTranslation(['deploy', 'common'])
  const { activeTenant, can } = useAuth()
  const queryClient = useQueryClient()
  const canWrite = can('deploy:deployment:write')
  const canReadWiring = can('deploy:wiring:read')

  const [tab, setTab] = useState<TabKey>('definitions')
  const [selected, setSelected] = useState<string | null>(null)
  const [detailOpen, setDetailOpen] = useState(false)
  const [editorOpen, setEditorOpen] = useState(false)

  const definitions = useQuery({
    queryKey: deployKeys.definitions(activeTenant),
    queryFn: () => deployApi.listDefinitions(),
    enabled: tab === 'definitions',
    refetchInterval: tab === 'definitions' ? 30_000 : false,
  })

  function refresh() {
    void queryClient.invalidateQueries({
      queryKey: deployKeys.all(activeTenant),
    })
  }

  const columns: TableColumn<DefinitionDTO, unknown>[] = [
    {
      accessorKey: 'name',
      header: t('definitions.name'),
      cell: ({ row }) => (
        <span className="font-medium text-foreground">{row.original.name}</span>
      ),
    },
    {
      id: 'subject',
      header: t('definitions.subject'),
      cell: ({ row }) => (
        <span className="flex items-center gap-1.5">
          <Badge variant="outline">{row.original.subject_kind}</Badge>
          <span className="font-mono text-xs text-muted-foreground">
            {row.original.subject_ref}
          </span>
        </span>
      ),
    },
    {
      accessorKey: 'environment',
      header: t('definitions.environment'),
      cell: ({ row }) => (
        <Badge variant="neutral">{row.original.environment}</Badge>
      ),
    },
    {
      accessorKey: 'target',
      header: t('definitions.target'),
      cell: ({ row }) => (
        <span className="font-mono text-xs text-muted-foreground">
          {row.original.target}
        </span>
      ),
    },
    {
      accessorKey: 'desired_status',
      header: t('definitions.status'),
      cell: ({ row }) => <StatusBadge status={row.original.desired_status} />,
    },
    {
      id: 'versions',
      header: t('definitions.versions'),
      cell: ({ row }) => (
        <span className="font-mono text-xs tabular-nums text-muted-foreground">
          {t('definitions.versionsLabel', {
            applied: row.original.applied_version,
            current: row.original.current_version,
          })}
        </span>
      ),
    },
    {
      id: 'sync',
      header: t('definitions.sync'),
      cell: ({ row }) => <SyncCell definition={row.original} />,
    },
  ]

  return (
    <div className="flex flex-col gap-5 pb-10">
      <PageHeader
        title={t('title')}
        description={t('subtitle')}
        icon={Rocket}
        actions={
          <Button variant="ghost" size="sm" onClick={refresh}>
            <RefreshCw />
            {t('common:actions.refresh')}
          </Button>
        }
      />

      <Tabs value={tab} onValueChange={(v) => setTab(v as TabKey)}>
        <TabsList>
          <TabsTrigger value="definitions">{t('tabs.definitions')}</TabsTrigger>
          {canReadWiring && (
            <TabsTrigger value="wirings">{t('tabs.wirings')}</TabsTrigger>
          )}
          <TabsTrigger value="operations">{t('tabs.operations')}</TabsTrigger>
        </TabsList>

        <TabsContent value="definitions">
          {/* Si el motor recortó, la tabla es una PARTE y no lo diría: una definición que no
              sale se lee como una definición que no existe. */}
          <ListTruncationBadge
            query={definitions}
            label={t('definitions.truncated', {
              n: definitions.data?.items?.length ?? 0,
            })}
            hint={t('truncatedHint')}
          />
          <DataTable
            columns={columns}
            data={definitions.data?.items ?? []}
            isLoading={definitions.isLoading}
            error={definitions.error}
            onRetry={() => definitions.refetch()}
            searchable
            searchPlaceholder={t('definitions.search')}
            getRowId={(r) => r.id}
            onRowClick={(r) => {
              setSelected(r.id)
              setDetailOpen(true)
            }}
            toolbar={
              canWrite ? (
                <Button
                  variant="primary"
                  size="sm"
                  onClick={() => setEditorOpen(true)}
                >
                  <Plus />
                  {t('definitions.declare')}
                </Button>
              ) : undefined
            }
            empty={
              <EmptyState
                title={t('empty.deploy.title')}
                description={t('empty.deploy.description')}
              />
            }
          />
        </TabsContent>

        {canReadWiring && (
          <TabsContent value="wirings">
            <WiringsTable />
          </TabsContent>
        )}

        <TabsContent value="operations">
          <OperationsTable />
        </TabsContent>
      </Tabs>

      <DefinitionDetailSheet
        definitionId={selected}
        open={detailOpen}
        onOpenChange={setDetailOpen}
      />

      {/* Declare a new definition. */}
      <DefinitionEditorDialog
        open={editorOpen}
        onOpenChange={setEditorOpen}
        definition={null}
      />
    </div>
  )
}

function SyncCell({ definition }: { definition: DefinitionDTO }) {
  const { t } = useTranslation('deploy')
  if (definition.applied_version === 0) {
    return (
      <Badge variant="warning" title={t('sync.neverAppliedHint')}>
        {t('sync.neverApplied')}
      </Badge>
    )
  }
  if (!definition.up_to_date) {
    return (
      <Badge variant="warning" title={t('sync.pendingHint')}>
        {t('sync.pending')}
      </Badge>
    )
  }
  return <Badge variant="success">{t('sync.upToDate')}</Badge>
}
