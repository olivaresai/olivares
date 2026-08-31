// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { Plug, Plus, RefreshCw } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Badge } from '@/components/ui/badge'
import { StepUpRequiredState } from '@/components/layout/step-up-state'
import { Button } from '@/components/ui/button'
import { PageHeader } from '@/components/ui/page-header'
import { Skeleton } from '@/components/ui/skeleton'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { DataTable, type TableColumn } from '@/components/data/data-table'
import { StatusBadge } from '@/components/data/badges'
import { SecretRef } from '@/components/data/secret-ref'
import { ErrorState, ForbiddenState } from '@/components/ui/error-state'
import { EmptyState } from '@/components/ui/empty-state'
import { ApiError } from '@/lib/api/errors'
import { useAuth } from '@/lib/auth/context'
import { ToolAnnotations } from './annotations'
import { capabilitiesApi, capabilitiesKeys } from './api'
import { ConfigEditorDialog } from './config-editor'
import { ServerDetailSheet } from './server-detail'
import { ToolPinsTab } from './tool-pins'
import { WiringGraph } from './wiring-graph'
import './i18n'
import type { ConfigDTO, ServerDTO, SkillDTO, ToolDTO } from './types'
import { ListTruncationBadge } from '@/features/_intel'

type TabKey =
  'servers' | 'tools' | 'tool-pins' | 'skills' | 'wiring' | 'configs'

export default function CapabilitiesView() {
  const { t } = useTranslation(['capabilities', 'common', 'intel'])
  const { activeTenant, can } = useAuth()
  const queryClient = useQueryClient()
  const canWriteConfig = can('capabilities:config:write')
  const canReadConfig = can('capabilities:config:read')

  const [tab, setTab] = useState<TabKey>('servers')
  const [selectedServer, setSelectedServer] = useState<string | null>(null)
  const [detailOpen, setDetailOpen] = useState(false)
  const [editorOpen, setEditorOpen] = useState(false)
  const [editingConfig, setEditingConfig] = useState<ConfigDTO | null>(null)

  const servers = useQuery({
    queryKey: capabilitiesKeys.servers(activeTenant),
    queryFn: () => capabilitiesApi.listServers(),
    enabled: tab === 'servers',
    refetchInterval: tab === 'servers' ? 30_000 : false,
  })
  const tools = useQuery({
    queryKey: capabilitiesKeys.tools(activeTenant),
    queryFn: () => capabilitiesApi.listTools(),
    enabled: tab === 'tools',
  })
  const skills = useQuery({
    queryKey: capabilitiesKeys.skills(activeTenant),
    queryFn: () => capabilitiesApi.listSkills(),
    enabled: tab === 'skills',
  })
  const wiring = useQuery({
    queryKey: capabilitiesKeys.wiring(activeTenant),
    queryFn: () => capabilitiesApi.wiring(),
    enabled: tab === 'wiring',
  })
  const configs = useQuery({
    queryKey: capabilitiesKeys.configs(activeTenant),
    queryFn: () => capabilitiesApi.listConfigs(),
    enabled: tab === 'configs' && canReadConfig,
  })

  function refresh() {
    void queryClient.invalidateQueries({
      queryKey: capabilitiesKeys.all(activeTenant),
    })
  }

  const serverColumns: TableColumn<ServerDTO, unknown>[] = [
    {
      accessorKey: 'name',
      header: t('servers.name'),
      cell: ({ row }) => (
        <span className="font-medium text-foreground">{row.original.name}</span>
      ),
    },
    {
      accessorKey: 'transport',
      header: t('servers.transport'),
      cell: ({ row }) => (
        <Badge variant="neutral">{row.original.transport}</Badge>
      ),
    },
    {
      accessorKey: 'version',
      header: t('servers.version'),
      cell: ({ row }) => (
        <span className="font-mono text-xs text-muted-foreground">
          {row.original.version || '—'}
        </span>
      ),
    },
    {
      accessorKey: 'connection',
      header: t('servers.connection'),
      cell: ({ row }) => {
        const c = row.original.connection
        const variant =
          c === 'connected'
            ? 'success'
            : c === 'degraded'
              ? 'warning'
              : c === 'down'
                ? 'danger'
                : 'neutral'
        return (
          <Badge variant={variant} title={t('connection.caption')}>
            {t(`connection.${c}`, { defaultValue: c })}
          </Badge>
        )
      },
    },
    {
      accessorKey: 'tool_count',
      header: t('servers.tools'),
      cell: ({ row }) => (
        <span className="font-mono tabular-nums">
          {row.original.tool_count}
        </span>
      ),
    },
    {
      id: 'managed',
      header: t('servers.managed'),
      cell: ({ row }) =>
        row.original.has_config ? (
          <Badge variant="success">
            {t('servers.managedRev', {
              rev: row.original.config_revision ?? 0,
            })}
          </Badge>
        ) : (
          <Badge variant="outline">{t('servers.discovered')}</Badge>
        ),
    },
  ]

  const toolColumns: TableColumn<ToolDTO, unknown>[] = [
    {
      accessorKey: 'name',
      header: t('tools.name'),
      cell: ({ row }) => (
        <span className="font-mono text-xs text-foreground">
          {row.original.name}
        </span>
      ),
    },
    { accessorKey: 'kind', header: t('tools.kind') },
    {
      id: 'annotations',
      header: t('tools.annotations'),
      cell: ({ row }) => <ToolAnnotations tool={row.original} />,
    },
    {
      accessorKey: 'schema_hash',
      header: t('tools.schemaHash'),
      cell: ({ row }) => (
        <span className="font-mono text-xs text-muted-foreground">
          {row.original.schema_hash
            ? row.original.schema_hash.slice(0, 12)
            : '—'}
        </span>
      ),
    },
  ]

  const skillColumns: TableColumn<SkillDTO, unknown>[] = [
    {
      accessorKey: 'name',
      header: t('skills.name'),
      cell: ({ row }) => (
        <span className="font-mono text-xs text-foreground">
          {row.original.name}
        </span>
      ),
    },
    { accessorKey: 'source', header: t('skills.source') },
    { accessorKey: 'version', header: t('skills.version') },
    {
      accessorKey: 'status',
      header: t('skills.status'),
      cell: ({ row }) => <StatusBadge status={row.original.status} />,
    },
    {
      accessorKey: 'description',
      header: t('skills.description'),
      cell: ({ row }) => (
        <span className="text-xs text-muted-foreground">
          {row.original.description || '—'}
        </span>
      ),
    },
  ]

  const configColumns: TableColumn<ConfigDTO, unknown>[] = [
    {
      accessorKey: 'server_ref',
      header: t('configs.serverRef'),
      cell: ({ row }) => (
        <span className="font-mono text-xs font-medium text-foreground">
          {row.original.server_ref}
        </span>
      ),
    },
    {
      accessorKey: 'transport',
      header: t('configs.transport'),
      cell: ({ row }) => (
        <Badge variant="neutral">{row.original.transport}</Badge>
      ),
    },
    {
      accessorKey: 'scope',
      header: t('configs.scope'),
      cell: ({ row }) => row.original.scope || '—',
    },
    {
      accessorKey: 'enabled',
      header: t('configs.enabled'),
      cell: ({ row }) => (
        <Badge variant={row.original.enabled ? 'success' : 'neutral'}>
          {t(
            row.original.enabled
              ? 'common:status.enabled'
              : 'common:status.disabled',
          )}
        </Badge>
      ),
    },
    {
      accessorKey: 'revision',
      header: t('configs.revision'),
      cell: ({ row }) => (
        <span className="font-mono tabular-nums">
          {row.original.revision ?? '—'}
        </span>
      ),
    },
    {
      id: 'secrets',
      header: t('configs.secrets'),
      cell: ({ row }) => (
        <div className="flex flex-wrap gap-1">
          {row.original.secret_refs.length === 0
            ? '—'
            : row.original.secret_refs
                .slice(0, 3)
                .map((s, i) => <SecretRef key={i} name={s.name} />)}
        </div>
      ),
    },
  ]

  return (
    <div className="flex flex-col gap-5 pb-10">
      <PageHeader
        title={t('title')}
        description={t('subtitle')}
        icon={Plug}
        actions={
          <Button variant="ghost" size="sm" onClick={refresh}>
            <RefreshCw />
            {t('common:actions.refresh')}
          </Button>
        }
      />

      <Tabs value={tab} onValueChange={(v) => setTab(v as TabKey)}>
        <TabsList>
          <TabsTrigger value="servers">{t('tabs.servers')}</TabsTrigger>
          <TabsTrigger value="tools">{t('tabs.tools')}</TabsTrigger>
          {canReadConfig && (
            <TabsTrigger value="tool-pins">{t('tabs.toolPins')}</TabsTrigger>
          )}
          <TabsTrigger value="skills">{t('tabs.skills')}</TabsTrigger>
          <TabsTrigger value="wiring">{t('tabs.wiring')}</TabsTrigger>
          {canReadConfig && (
            <TabsTrigger value="configs">{t('tabs.configs')}</TabsTrigger>
          )}
        </TabsList>

        <TabsContent value="servers">
          <ListTruncationBadge
            query={servers}
            // ⛔ CLAVE PROPIA, NO LA COMPARTIDA. La compartida dice «Loaded {{n}} rows» y esta
            //    pantalla enumera SERVIDORES: `servers.truncated` existe en los siete idiomas y
            //    estaba muerta. La perdi yo al integrar #1651 el 2026-08-29 —resolvi la vista al
            //    lado de main y el testigo al de la rama—, y el par dejo de casar: el test busca
            //    «servers; there are more» y la nota generica dice «rows». Sus hermanas `tools` y
            //    `skills` ya usan la suya; servers era la unica excepcion.
            label={t('servers.truncated', {
              n: servers.data?.items?.length ?? 0,
            })}
            hint={t('truncatedHint')}
            className="px-0 pt-0 pb-3"
          />
          <DataTable
            columns={serverColumns}
            data={servers.data?.items ?? []}
            isLoading={servers.isLoading}
            error={servers.error}
            onRetry={() => servers.refetch()}
            searchable
            searchPlaceholder={t('servers.search')}
            getRowId={(r) => r.id}
            onRowClick={(r) => {
              setSelectedServer(r.id)
              setDetailOpen(true)
            }}
            empty={
              <EmptyState
                title={t('empty.server.title')}
                description={t('empty.server.description')}
              />
            }
          />
        </TabsContent>

        <TabsContent value="tools">
          <ListTruncationBadge
            query={tools}
            label={t('tools.truncated', { n: tools.data?.items?.length ?? 0 })}
            hint={t('truncatedHint')}
          />
          <p className="mb-3 text-xs text-muted-foreground">
            {t('tools.untrustedNote')}
          </p>
          <ListTruncationBadge
            query={tools}
            label={t('intel:notices.listTruncated', {
              n: tools.data?.items?.length ?? 0,
            })}
            hint={t('intel:notices.listTruncatedHint')}
            className="px-0 pt-0 pb-3"
          />
          <DataTable
            columns={toolColumns}
            data={tools.data?.items ?? []}
            isLoading={tools.isLoading}
            error={tools.error}
            onRetry={() => tools.refetch()}
            searchable
            searchPlaceholder={t('tools.search')}
            getRowId={(r) => r.id}
            empty={
              <EmptyState
                title={t('empty.tool.title')}
                description={t('empty.tool.description')}
              />
            }
          />
        </TabsContent>

        {canReadConfig && (
          <TabsContent value="tool-pins">
            <ToolPinsTab canWrite={canWriteConfig} />
          </TabsContent>
        )}

        <TabsContent value="skills">
          <ListTruncationBadge
            query={skills}
            label={t('skills.truncated', {
              n: skills.data?.items?.length ?? 0,
            })}
            hint={t('truncatedHint')}
          />
          <p className="mb-3 text-xs text-muted-foreground">
            {t('skills.caption')}
          </p>
          <ListTruncationBadge
            query={skills}
            label={t('intel:notices.listTruncated', {
              n: skills.data?.items?.length ?? 0,
            })}
            hint={t('intel:notices.listTruncatedHint')}
            className="px-0 pt-0 pb-3"
          />
          <DataTable
            columns={skillColumns}
            data={skills.data?.items ?? []}
            isLoading={skills.isLoading}
            error={skills.error}
            onRetry={() => skills.refetch()}
            searchable
            searchPlaceholder={t('skills.search')}
            getRowId={(r) => r.id}
            empty={
              <EmptyState
                title={t('empty.skill.title')}
                description={t('empty.skill.description')}
              />
            }
          />
        </TabsContent>

        <TabsContent value="wiring">
          {wiring.isLoading ? (
            <Skeleton className="h-[560px] rounded-lg" />
          ) : wiring.error instanceof ApiError &&
            wiring.error.isStepUpRequired ? (
            /* ⛔ ASEGURAMIENTO ANTES QUE ROL: `isForbidden` es sólo el status
               (lib/api/errors.ts:59). Sin esta rama, 560 px de grafo se sustituían
               por un escudo tachado SIN reintento ni mención de la ceremonia, y el
               operador concluía que su rol no alcanza a algo que sí alcanza. */
            <StepUpRequiredState
              action="generic"
              onElevated={() => void wiring.refetch()}
            />
          ) : wiring.error instanceof ApiError && wiring.error.isForbidden ? (
            <ForbiddenState />
          ) : wiring.error ? (
            <ErrorState retry={() => wiring.refetch()} />
          ) : wiring.data ? (
            <WiringGraph graph={wiring.data} />
          ) : null}
        </TabsContent>

        {canReadConfig && (
          <TabsContent value="configs">
            <ListTruncationBadge
              query={configs}
              label={t('intel:notices.listTruncated', {
                n: configs.data?.items?.length ?? 0,
              })}
              hint={t('intel:notices.listTruncatedHint')}
              className="px-0 pt-0 pb-3"
            />
            <DataTable
              columns={configColumns}
              data={configs.data?.items ?? []}
              isLoading={configs.isLoading}
              error={configs.error}
              onRetry={() => configs.refetch()}
              searchable
              searchPlaceholder={t('configs.search')}
              getRowId={(r) => r.id ?? r.server_ref}
              onRowClick={(r) => {
                setEditingConfig(r)
                setEditorOpen(true)
              }}
              toolbar={
                canWriteConfig ? (
                  <Button
                    variant="primary"
                    size="sm"
                    onClick={() => {
                      setEditingConfig(null)
                      setEditorOpen(true)
                    }}
                  >
                    <Plus />
                    {t('configs.newConfig')}
                  </Button>
                ) : undefined
              }
              empty={
                <EmptyState
                  title={t('empty.config.title')}
                  description={t('empty.config.description')}
                />
              }
            />
          </TabsContent>
        )}
      </Tabs>

      <ServerDetailSheet
        serverId={selectedServer}
        open={detailOpen}
        onOpenChange={setDetailOpen}
      />

      <ConfigEditorDialog
        open={editorOpen}
        onOpenChange={setEditorOpen}
        config={editingConfig}
      />
    </div>
  )
}
