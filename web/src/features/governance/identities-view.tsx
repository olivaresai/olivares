// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useQuery } from '@tanstack/react-query'
import { Link2, Link2Off, RefreshCw, Users } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { ConfirmDialog } from '@/components/ui/confirm-dialog'
import { ForbiddenState } from '@/components/ui/error-state'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { EmptyState } from '@/components/ui/empty-state'
import { DataTable, type TableColumn } from '@/components/data/data-table'
import { StatusBadge } from '@/components/data/badges'
import { useAuth } from '@/lib/auth/context'
import { usePrivilegedMutation } from '@/lib/hooks/use-privileged-mutation'
import { RegisterAgentDialog } from './register-agent-dialog'
import { EmergingStandardsPanel } from './emerging-standards'
import { governanceApi, governanceKeys } from './api'
import { BindDialog } from './bind-dialog'
import { GroupMembersSheet } from './group-members'
import './i18n'
import type { BindingDTO, GroupDTO, IdentityDTO, RosterReport } from './types'

type SubTab = 'identities' | 'groups' | 'bindings' | 'standards'

/**
 * IdentitiesView covers the reconciled roster (identities), the directory collections
 * (groups + members drill-in) and the agent↔NHI bindings. Reads gate on
 * governance:identity:read; the resync, bind and unbind privileged actions gate on
 * governance:identity:admin. Identity metadata is allow-listed by the backend — the
 * UI just renders what is returned.
 */
export function IdentitiesView() {
  const { t } = useTranslation(['governance', 'common'])
  const { activeTenant, can } = useAuth()
  const canRead = can('governance:identity:read')
  const canAdmin = can('governance:identity:admin')
  // ⛔ El registro de agentes NO va por `identity:admin`: el motor lo gatea con
  // `governance:nhi:admin` (`governance.go:71`, ruta `POST /agents`). Usar el de la vista
  // escondería el botón a quien SÍ puede registrar, o se lo enseñaría a quien recibirá un 403.
  const canNHIAdmin = can('governance:nhi:admin')

  const [sub, setSub] = useState<SubTab>('identities')
  const [membersRef, setMembersRef] = useState<string | null>(null)
  const [membersOpen, setMembersOpen] = useState(false)
  const [bindOpen, setBindOpen] = useState(false)
  const [bindAgent, setBindAgent] = useState<string | undefined>(undefined)
  const [confirmResync, setConfirmResync] = useState(false)
  const [confirmUnbind, setConfirmUnbind] = useState<BindingDTO | null>(null)

  const identities = useQuery({
    queryKey: governanceKeys.identities(activeTenant),
    queryFn: () => governanceApi.listIdentities(),
    enabled: canRead && sub === 'identities',
  })
  const groups = useQuery({
    queryKey: governanceKeys.groups(activeTenant),
    queryFn: () => governanceApi.listGroups(),
    enabled: canRead && sub === 'groups',
  })
  const bindings = useQuery({
    queryKey: governanceKeys.bindings(activeTenant),
    queryFn: () => governanceApi.listBindings(),
    enabled: canRead && sub === 'bindings',
  })

  const resync = usePrivilegedMutation<void, RosterReport>({
    mutationFn: () => governanceApi.syncRoster(),
    invalidateKeys: () => [governanceKeys.all(activeTenant)],
    // Render the RosterReport as a result summary (it is a single report, not a
    // list). With no providers wired, say so honestly instead of implying success.
    // A PARTIAL failure is a 200, so this is the ONLY place the operator learns a source
    // did not answer. Reporting it as plain success would turn a loud 502 into a green tick
    // — the exact "fail mute" this whole change exists to avoid. Found by the adversarial
    // contrast of PR #622 (P1-1): the field existed in types.ts and nothing rendered it.
    successMessage: (report) => {
      const failed = report.providers_failed ?? []
      if (report.providers_configured === 0) return t('resync.noProviders')
      const summary = `${t('resync.done')} — ${t('resync.result', {
        sources: report.sources,
        identities: report.identities,
        collections: report.collections,
        memberships: report.memberships,
      })}`
      if (failed.length === 0) return summary
      return `${summary} — ${t('resync.partial', {
        failed: failed.length,
        providers: failed.map((f) => f.provider).join(', '),
      })}`
    },
    onDone: () => setConfirmResync(false),
  })

  const unbind = usePrivilegedMutation({
    mutationFn: () =>
      governanceApi.unbindAgentIdentity(confirmUnbind!.agent_id),
    invalidateKeys: () => [governanceKeys.bindings(activeTenant)],
    successMessage: t('unbind.done'),
    onDone: () => setConfirmUnbind(null),
  })

  if (!canRead) return <ForbiddenState />

  const identityColumns: TableColumn<IdentityDTO, unknown>[] = [
    {
      accessorKey: 'name',
      header: t('identities.name'),
      cell: ({ row }) => (
        <span className="font-medium text-foreground">
          {row.original.name || '—'}
        </span>
      ),
    },
    {
      accessorKey: 'ref',
      header: t('identities.ref'),
      cell: ({ row }) => (
        <span className="font-mono text-xs text-muted-foreground">
          {row.original.ref}
        </span>
      ),
    },
    {
      accessorKey: 'kind',
      header: t('identities.kind'),
      cell: ({ row }) => row.original.kind || '—',
    },
    {
      accessorKey: 'source',
      header: t('identities.source'),
      cell: ({ row }) => row.original.source || '—',
    },
    {
      accessorKey: 'principal_type',
      header: t('identities.principalType'),
      cell: ({ row }) => {
        const p = row.original.principal_type
        if (!p) return '—'
        const label =
          p === 'human'
            ? t('identities.principalTypeHuman')
            : p === 'nhi'
              ? t('identities.principalTypeNhi')
              : t('identities.principalTypeUnknown')
        return <Badge variant="neutral">{label}</Badge>
      },
    },
    {
      accessorKey: 'disabled',
      header: t('identities.state'),
      cell: ({ row }) => (
        <StatusBadge status={row.original.disabled ? 'disabled' : 'enabled'} />
      ),
    },
  ]

  const groupColumns: TableColumn<GroupDTO, unknown>[] = [
    {
      accessorKey: 'display_name',
      header: t('groups.name'),
      cell: ({ row }) => (
        <span className="font-medium text-foreground">
          {row.original.display_name || row.original.ref}
        </span>
      ),
    },
    {
      accessorKey: 'ref',
      header: t('groups.ref'),
      cell: ({ row }) => (
        <span className="font-mono text-xs text-muted-foreground">
          {row.original.ref}
        </span>
      ),
    },
    {
      accessorKey: 'kind',
      header: t('groups.kind'),
      cell: ({ row }) =>
        row.original.kind ? (
          <Badge variant="neutral">{row.original.kind}</Badge>
        ) : (
          '—'
        ),
    },
    {
      accessorKey: 'source',
      header: t('groups.source'),
      cell: ({ row }) => row.original.source || '—',
    },
    {
      id: 'members',
      header: '',
      cell: ({ row }) => (
        <div className="flex items-center justify-end">
          <Button
            variant="ghost"
            size="sm"
            onClick={(e) => {
              e.stopPropagation()
              setMembersRef(row.original.ref)
              setMembersOpen(true)
            }}
          >
            <Users />
            {t('groups.members')}
          </Button>
        </div>
      ),
    },
  ]

  const bindingColumns: TableColumn<BindingDTO, unknown>[] = [
    {
      accessorKey: 'agent_name',
      header: t('bindings.agent'),
      cell: ({ row }) => (
        <div className="flex flex-col">
          <span className="font-medium text-foreground">
            {row.original.agent_name || row.original.agent_id}
          </span>
          <span className="font-mono text-xs text-muted-foreground">
            {row.original.agent_id}
          </span>
        </div>
      ),
    },
    {
      accessorKey: 'identity_ref',
      header: t('bindings.identity'),
      cell: ({ row }) => (
        <span className="font-mono text-xs text-muted-foreground">
          {row.original.identity_ref || row.original.identity_id}
        </span>
      ),
    },
    {
      id: 'shared',
      header: t('bindings.sharedBy'),
      cell: ({ row }) =>
        row.original.shared ? (
          <Badge variant="warning" title={t('bindings.sharedHint')}>
            {t('bindings.shared')} ·{' '}
            {t('bindings.agentCount', { count: row.original.agent_count })}
          </Badge>
        ) : (
          <span className="font-mono tabular-nums text-muted-foreground">
            {t('bindings.agentCount', { count: row.original.agent_count })}
          </span>
        ),
    },
    ...(canAdmin
      ? [
          {
            id: 'actions',
            header: '',
            cell: ({ row }: { row: { original: BindingDTO } }) => (
              <div className="flex items-center justify-end">
                <Button
                  variant="destructive"
                  size="sm"
                  onClick={(e) => {
                    e.stopPropagation()
                    setConfirmUnbind(row.original)
                  }}
                >
                  <Link2Off />
                  {t('bindings.unbind')}
                </Button>
              </div>
            ),
          } as TableColumn<BindingDTO, unknown>,
        ]
      : []),
  ]

  return (
    <div className="flex flex-col gap-3">
      <Tabs value={sub} onValueChange={(v) => setSub(v as SubTab)}>
        <TabsList>
          <TabsTrigger value="identities">{t('identities.title')}</TabsTrigger>
          <TabsTrigger value="groups">{t('groups.title')}</TabsTrigger>
          <TabsTrigger value="bindings">{t('bindings.title')}</TabsTrigger>
          <TabsTrigger value="standards">{t('emerging.tab')}</TabsTrigger>
        </TabsList>

        <TabsContent value="identities">
          <p className="mb-3 text-xs text-muted-foreground">
            {t('identities.caption')}
          </p>
          <DataTable
            columns={identityColumns}
            data={identities.data?.items ?? []}
            isLoading={identities.isLoading}
            error={identities.error}
            onRetry={() => identities.refetch()}
            searchable
            searchPlaceholder={t('identities.search')}
            getRowId={(r) => r.id}
            empty={
              <EmptyState
                title={t('empty.identity.title')}
                description={t('empty.identity.description')}
              />
            }
            toolbar={
              canAdmin ? (
                <Button
                  variant="secondary"
                  size="sm"
                  onClick={() => setConfirmResync(true)}
                >
                  <RefreshCw />
                  {t('identities.resync')}
                </Button>
              ) : undefined
            }
          />
        </TabsContent>

        <TabsContent value="groups">
          <p className="mb-3 text-xs text-muted-foreground">
            {t('groups.caption')}
          </p>
          <DataTable
            columns={groupColumns}
            data={groups.data?.items ?? []}
            isLoading={groups.isLoading}
            error={groups.error}
            onRetry={() => groups.refetch()}
            searchable
            searchPlaceholder={t('groups.search')}
            getRowId={(r) => r.ref}
            empty={
              <EmptyState
                title={t('empty.group.title')}
                description={t('empty.group.description')}
              />
            }
          />
        </TabsContent>

        <TabsContent value="bindings">
          <div className="mb-3 flex items-start justify-between gap-3">
            <p className="text-xs text-muted-foreground">
              {t('bindings.caption')}
            </p>
            <RegisterAgentDialog canAdmin={canNHIAdmin} />
          </div>
          <DataTable
            columns={bindingColumns}
            data={bindings.data?.items ?? []}
            isLoading={bindings.isLoading}
            error={bindings.error}
            onRetry={() => bindings.refetch()}
            searchable
            searchPlaceholder={t('bindings.search')}
            getRowId={(r) => `${r.agent_id}:${r.identity_id}`}
            empty={
              <EmptyState
                title={t('empty.binding.title')}
                description={t('empty.binding.description')}
              />
            }
            toolbar={
              canAdmin ? (
                <Button
                  variant="primary"
                  size="sm"
                  onClick={() => {
                    setBindAgent(undefined)
                    setBindOpen(true)
                  }}
                >
                  <Link2 />
                  {t('bind.confirm')}
                </Button>
              ) : undefined
            }
          />
        </TabsContent>

        <TabsContent value="standards">
          <EmergingStandardsPanel canRead={canRead} />
        </TabsContent>
      </Tabs>

      <GroupMembersSheet
        open={membersOpen}
        onOpenChange={setMembersOpen}
        groupRef={membersRef}
      />

      <BindDialog
        open={bindOpen}
        onOpenChange={setBindOpen}
        agentId={bindAgent}
      />

      {confirmResync && (
        <ConfirmDialog
          open={confirmResync}
          onOpenChange={setConfirmResync}
          title={t('resync.title')}
          description={t('resync.body')}
          confirmLabel={t('resync.confirm')}
          pending={resync.isPending}
          onConfirm={() => resync.mutate()}
        />
      )}

      {confirmUnbind && (
        <ConfirmDialog
          open={!!confirmUnbind}
          onOpenChange={(o) => !o && setConfirmUnbind(null)}
          title={t('unbind.title')}
          description={t('unbind.body')}
          tone="danger"
          confirmLabel={t('unbind.confirm')}
          pending={unbind.isPending}
          onConfirm={() => unbind.mutate(undefined)}
        />
      )}
    </div>
  )
}
