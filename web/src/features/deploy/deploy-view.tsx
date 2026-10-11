// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { Link } from '@tanstack/react-router'
import { Plus, RefreshCw, Rocket } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { PageHeader } from '@/components/ui/page-header'
import {
  PagePrimaryAction,
  PageSecondaryActions,
} from '@/components/ui/page-actions'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { EmptyState } from '@/components/ui/empty-state'
import { DataTable, type TableColumn } from '@/components/data/data-table'
import { StatusBadge } from '@/components/data/badges'
import { useAuth } from '@/lib/auth/context'
import { deployApi, deployKeys } from './api'
import { DefinitionDetailSheet } from './definition-detail'
import { DefinitionEditorDialog } from './definition-editor'
import { NoExecutorNotice } from './executor-notice'
import { OperationsTable } from './operations-table'
import { WiringsTable } from './wirings-table'
import './i18n'
import type { DefinitionDTO } from './types'
import { ListTruncationBadge } from '@/features/_intel'

type TabKey = 'definitions' | 'wirings' | 'operations'

/** How an administrator connects a runtime executor, on the module's own docs page. */
const EXECUTOR_GUIDE =
  'https://docs.olivares.ai/reference/modules/vii-deploy/#connect-an-executor'

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
  // Whether Plan and Apply can reach infrastructure at all. Only the engine's explicit
  // "no" changes the screen: a read that failed claims nothing either way.
  const executor = useQuery({
    queryKey: deployKeys.executor(activeTenant),
    queryFn: () => deployApi.executor(),
  })
  const noExecutor = executor.data?.configured === false

  const declare = (
    <Button
      variant={noExecutor ? 'secondary' : 'primary'}
      size="sm"
      onClick={() => setEditorOpen(true)}
    >
      <Plus />
      {t('definitions.declare')}
    </Button>
  )

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
          <Badge variant="outline">
            {t(`editor.subjectKinds.${row.original.subject_kind}`, {
              defaultValue: row.original.subject_kind,
            })}
          </Badge>
          <span className="font-mono text-caption text-muted-foreground">
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
        <span className="font-mono text-caption text-muted-foreground">
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
        <span className="font-mono text-caption tabular-nums text-muted-foreground">
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
          {/* Without an executor the form is not the first thing offered: declaring
              desired state stays possible, as a quieter action. The verb waits for the
              answer so it is placed once. */}
          {canWrite &&
            !executor.isLoading &&
            (noExecutor ? (
              <PageSecondaryActions>{declare}</PageSecondaryActions>
            ) : (
              <PagePrimaryAction>{declare}</PagePrimaryAction>
            ))}
          {noExecutor && (definitions.data?.items?.length ?? 0) > 0 && (
            <NoExecutorNotice />
          )}
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
            // An empty list waits for the executor answer, so its empty state is said
            // once and right; rows never wait for it.
            isLoading={
              definitions.isLoading ||
              (executor.isLoading && !definitions.data?.items?.length)
            }
            error={definitions.error}
            onRetry={() => definitions.refetch()}
            searchable
            searchPlaceholder={t('definitions.search')}
            getRowId={(r) => r.id}
            onRowClick={(r) => {
              setSelected(r.id)
              setDetailOpen(true)
            }}
            empty={
              <EmptyState
                title={
                  noExecutor ? t('noExecutor.title') : t('empty.deploy.title')
                }
                description={
                  noExecutor
                    ? t('noExecutor.description')
                    : t('empty.deploy.description')
                }
                // The screen's own verb is the primary action — the same button the tab
                // offers above the table, on the same right — once an executor can
                // carry it out. The way to a LAUNCHED session is the quieter one: this
                // screen lists definitions, and someone who launched a session from a
                // profile is looking for it somewhere else.
                // Without an executor the way forward is the guide to connecting one.
                action={canWrite && !noExecutor ? declare : undefined}
                secondaryAction={
                  <Button asChild variant="link" size="sm">
                    {noExecutor ? (
                      <a
                        href={EXECUTOR_GUIDE}
                        rel="noopener noreferrer"
                        target="_blank"
                      >
                        {t('noExecutor.guide')}
                      </a>
                    ) : (
                      <Link to={'/agentops' as never}>
                        {t('empty.deploy.sessions')}
                      </Link>
                    )}
                  </Button>
                }
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
          <OperationsTable noExecutor={noExecutor} />
        </TabsContent>
      </Tabs>

      <DefinitionDetailSheet
        definitionId={selected}
        open={detailOpen}
        onOpenChange={setDetailOpen}
        noExecutor={noExecutor}
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
