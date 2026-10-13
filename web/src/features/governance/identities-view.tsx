// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useInfiniteQuery, useQuery } from '@tanstack/react-query'
import { Link2, Link2Off, Users } from 'lucide-react'
import { useMemo, useState } from 'react'
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
import { governanceApi, governanceKeys } from './api'
import { BindDialog } from './bind-dialog'
import { GroupMembersSheet } from './group-members'
import './i18n'
import type { BindingDTO, GroupDTO, IdentityDTO } from './types'
import { PagePrimaryAction } from '@/components/ui/page-actions'

type SubTab = 'identities' | 'groups' | 'bindings'

/** An account state the source never reported is unknown, not enabled. */
function accountState(disabled: boolean | null) {
  if (disabled === null) return 'unknown'
  return disabled ? 'disabled' : 'enabled'
}

/**
 * IdentitiesView covers the reconciled roster (identities), the directory collections
 * (groups + members drill-in) and the agent↔NHI bindings. Reads gate on
 * governance:identity:read; the bind and unbind privileged actions gate on
 * governance:identity:admin. Filling the roster from a directory is Business. Identity metadata is allow-listed by the backend — the
 * UI just renders what is returned.
 */
export function IdentitiesView() {
  const { t } = useTranslation(['governance', 'common'])
  const { activeTenant, can } = useAuth()
  const canRead = can('governance:identity:read')
  const canAdmin = can('governance:identity:admin')
  const [sub, setSub] = useState<SubTab>('identities')
  const [membersRef, setMembersRef] = useState<string | null>(null)
  const [membersOpen, setMembersOpen] = useState(false)
  const [bindOpen, setBindOpen] = useState(false)
  const [bindAgent, setBindAgent] = useState<string | undefined>(undefined)
  const [confirmUnbind, setConfirmUnbind] = useState<BindingDTO | null>(null)

  // The roster and the group list are cursor-paged: walk the pages so a row past the
  // first one is reachable, and say there is more instead of presenting a page as the whole.
  const identities = useInfiniteQuery({
    queryKey: governanceKeys.identities(activeTenant),
    queryFn: ({ pageParam }) =>
      governanceApi.listIdentities({ cursor: pageParam }),
    initialPageParam: undefined as string | undefined,
    getNextPageParam: (last) => (last.has_more ? last.cursor : undefined),
    enabled: canRead && sub === 'identities',
  })
  const groups = useInfiniteQuery({
    queryKey: governanceKeys.groups(activeTenant),
    queryFn: ({ pageParam }) => governanceApi.listGroups({ cursor: pageParam }),
    initialPageParam: undefined as string | undefined,
    getNextPageParam: (last) => (last.has_more ? last.cursor : undefined),
    enabled: canRead && sub === 'groups',
  })
  const identityRows = useMemo(
    () => identities.data?.pages.flatMap((p) => p.items) ?? [],
    [identities.data],
  )
  const groupRows = useMemo(
    () => groups.data?.pages.flatMap((p) => p.items) ?? [],
    [groups.data],
  )
  const bindings = useQuery({
    queryKey: governanceKeys.bindings(activeTenant),
    queryFn: () => governanceApi.listBindings(),
    enabled: canRead && sub === 'bindings',
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
        <span className="font-mono text-caption text-muted-foreground">
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
        <StatusBadge status={accountState(row.original.disabled)} />
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
        <span className="font-mono text-caption text-muted-foreground">
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
          <span className="font-mono text-caption text-muted-foreground">
            {row.original.agent_id}
          </span>
        </div>
      ),
    },
    {
      accessorKey: 'identity_ref',
      header: t('bindings.identity'),
      cell: ({ row }) => (
        <span className="font-mono text-caption text-muted-foreground">
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
        </TabsList>

        <TabsContent value="identities">
          <p className="mb-3 text-caption text-muted-foreground">
            {t('identities.caption')}
          </p>
          {identities.hasNextPage && (
            <p className="mb-3 text-caption text-muted-foreground">
              {t('list.loadedOnly')}
            </p>
          )}
          <DataTable
            columns={identityColumns}
            data={identityRows}
            isLoading={identities.isLoading}
            error={identities.error}
            onRetry={() => identities.refetch()}
            hasMore={identities.hasNextPage}
            onLoadMore={() => void identities.fetchNextPage()}
            isFetchingMore={identities.isFetchingNextPage}
            searchable
            searchPlaceholder={t('identities.search')}
            getRowId={(r) => r.id}
            empty={
              <EmptyState
                title={t('empty.identity.title')}
                description={t('empty.identity.description')}
              />
            }
          />
        </TabsContent>

        <TabsContent value="groups">
          <p className="mb-3 text-caption text-muted-foreground">
            {t('groups.caption')}
          </p>
          {groups.hasNextPage && (
            <p className="mb-3 text-caption text-muted-foreground">
              {t('list.loadedOnly')}
            </p>
          )}
          <DataTable
            columns={groupColumns}
            data={groupRows}
            isLoading={groups.isLoading}
            error={groups.error}
            onRetry={() => groups.refetch()}
            hasMore={groups.hasNextPage}
            onLoadMore={() => void groups.fetchNextPage()}
            isFetchingMore={groups.isFetchingNextPage}
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
          {canAdmin && (
            <PagePrimaryAction>
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
            </PagePrimaryAction>
          )}
          <p className="mb-3 text-caption text-muted-foreground">
            {t('bindings.caption')}
          </p>
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
          />
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
