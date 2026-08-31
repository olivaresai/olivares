// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { ListTruncationBadge } from '@/features/_intel'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { BookMarked, Plus, RefreshCw } from 'lucide-react'
import { useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { PageHeader } from '@/components/ui/page-header'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { EmptyState } from '@/components/ui/empty-state'
import { DataTable, type TableColumn } from '@/components/data/data-table'
import { StatusBadge } from '@/components/data/badges'
import { useAuth } from '@/lib/auth/context'
import { catalogApi, catalogKeys } from './api'
import { AdmissionPolicyTab } from './admission-policy'
import { EntryDetailSheet } from './entry-detail'
import { EntryEditorDialog } from './entry-editor'
import { InstanceDetailSheet } from './instance-detail'
import { SigningBadge } from './signing-badge'
import './i18n'
import {
  ENTRY_KINDS,
  ENTRY_STATUSES,
  INSTANCE_STATUSES,
  isInstanceNonTerminal,
} from './types'
import type { EntryDTO, InstanceDTO } from './types'

type TabKey = 'entries' | 'instances' | 'policy'

/** How often to poll instances while any visible request awaits a governance decision. */
const INSTANCE_POLL_MS = 12_000

export default function CatalogView() {
  const { t } = useTranslation(['catalog', 'common', 'intel'])
  const { activeTenant, can } = useAuth()
  const queryClient = useQueryClient()
  const canWriteEntry = can('catalog:entry:write')
  const canReadInstances = can('catalog:instance:read')
  // The two tenant admission policies are read at `entry:read` and written at
  // `entry:admin` (`modules/catalog/api.go:58-69`); the tab mirrors the read tier.
  const canReadPolicy = can('catalog:entry:read')

  const [tab, setTab] = useState<TabKey>('entries')
  const [kindFilter, setKindFilter] = useState<string>('all')
  const [statusFilter, setStatusFilter] = useState<string>('all')
  const [instanceStatusFilter, setInstanceStatusFilter] =
    useState<string>('all')

  const [selectedEntry, setSelectedEntry] = useState<string | null>(null)
  const [entryDetailOpen, setEntryDetailOpen] = useState(false)
  const [editorOpen, setEditorOpen] = useState(false)

  const [selectedInstance, setSelectedInstance] = useState<string | null>(null)
  const [instanceDetailOpen, setInstanceDetailOpen] = useState(false)

  const entryParams = useMemo(
    () => ({
      ...(kindFilter !== 'all' ? { kind: kindFilter } : {}),
      ...(statusFilter !== 'all' ? { status: statusFilter } : {}),
    }),
    [kindFilter, statusFilter],
  )
  const instanceParams = useMemo(
    () => ({
      ...(instanceStatusFilter !== 'all'
        ? { status: instanceStatusFilter }
        : {}),
    }),
    [instanceStatusFilter],
  )

  const entries = useQuery({
    queryKey: catalogKeys.entries(activeTenant, entryParams),
    queryFn: () => catalogApi.listEntries(entryParams),
    enabled: tab === 'entries',
  })

  const instances = useQuery({
    queryKey: catalogKeys.instances(activeTenant, instanceParams),
    queryFn: () => catalogApi.listInstances(instanceParams),
    enabled: tab === 'instances' && canReadInstances,
    // Poll only while a request is still moving through governance; back off when
    // every visible instance is terminal (rejected/active).
    refetchInterval: (q) => {
      const data = q.state.data
      const live = (data?.items ?? []).some((i) =>
        isInstanceNonTerminal(i.status),
      )
      return tab === 'instances' && live ? INSTANCE_POLL_MS : false
    },
  })

  const instancesLive = (instances.data?.items ?? []).some((i) =>
    isInstanceNonTerminal(i.status),
  )

  function refresh() {
    void queryClient.invalidateQueries({
      queryKey: catalogKeys.all(activeTenant),
    })
  }

  const entryColumns: TableColumn<EntryDTO, unknown>[] = [
    {
      accessorKey: 'name',
      header: t('entries.name'),
      cell: ({ row }) => (
        <span className="font-medium text-foreground">{row.original.name}</span>
      ),
    },
    {
      accessorKey: 'kind',
      header: t('entries.kind'),
      cell: ({ row }) => (
        <Badge variant="neutral">
          {t(`kind.${row.original.kind}`, { defaultValue: row.original.kind })}
        </Badge>
      ),
    },
    {
      accessorKey: 'slug',
      header: t('entries.slug'),
      cell: ({ row }) => (
        <span className="font-mono text-xs text-muted-foreground">
          {row.original.slug}
        </span>
      ),
    },
    {
      accessorKey: 'version',
      header: t('entries.version'),
      cell: ({ row }) => (
        <span className="font-mono text-xs text-muted-foreground">
          {row.original.version}
        </span>
      ),
    },
    {
      accessorKey: 'status',
      header: t('entries.status'),
      cell: ({ row }) =>
        row.original.status ? (
          <StatusBadge status={row.original.status} />
        ) : (
          '—'
        ),
    },
    {
      accessorKey: 'owner_ref',
      header: t('entries.owner'),
      cell: ({ row }) => (
        <span className="font-mono text-xs text-muted-foreground">
          {row.original.owner_ref || '—'}
        </span>
      ),
    },
    {
      id: 'signing',
      header: t('entries.signing'),
      cell: ({ row }) => <SigningBadge entry={row.original} />,
    },
  ]

  const instanceColumns: TableColumn<InstanceDTO, unknown>[] = [
    {
      accessorKey: 'name',
      header: t('instances.name'),
      cell: ({ row }) => (
        <span className="font-medium text-foreground">{row.original.name}</span>
      ),
    },
    {
      accessorKey: 'entry_slug',
      header: t('instances.entry'),
      cell: ({ row }) => (
        <span className="font-mono text-xs text-muted-foreground">
          {row.original.entry_slug || row.original.entry_id}
        </span>
      ),
    },
    {
      accessorKey: 'entry_kind',
      header: t('instances.kind'),
      cell: ({ row }) =>
        row.original.entry_kind ? (
          <Badge variant="neutral">
            {t(`kind.${row.original.entry_kind}`, {
              defaultValue: row.original.entry_kind,
            })}
          </Badge>
        ) : (
          '—'
        ),
    },
    {
      accessorKey: 'entry_version',
      header: t('instances.version'),
      cell: ({ row }) => (
        <span className="font-mono text-xs text-muted-foreground">
          {row.original.entry_version || '—'}
        </span>
      ),
    },
    {
      accessorKey: 'status',
      header: t('instances.status'),
      cell: ({ row }) =>
        row.original.status ? (
          <StatusBadge status={row.original.status} />
        ) : (
          '—'
        ),
    },
    {
      accessorKey: 'requested_by',
      header: t('instances.requestedBy'),
      cell: ({ row }) => (
        <span className="font-mono text-xs text-muted-foreground">
          {row.original.requested_by || '—'}
        </span>
      ),
    },
  ]

  return (
    <div className="flex flex-col gap-5 pb-10">
      <PageHeader
        title={t('title')}
        description={t('subtitle')}
        icon={BookMarked}
        actions={
          <Button variant="ghost" size="sm" onClick={refresh}>
            <RefreshCw />
            {t('common:actions.refresh')}
          </Button>
        }
      />

      <Tabs value={tab} onValueChange={(v) => setTab(v as TabKey)}>
        <TabsList>
          <TabsTrigger value="entries">{t('tabs.entries')}</TabsTrigger>
          {canReadInstances && (
            <TabsTrigger value="instances">{t('tabs.instances')}</TabsTrigger>
          )}
          {canReadPolicy && (
            <TabsTrigger value="policy">{t('tabs.policy')}</TabsTrigger>
          )}
        </TabsList>

        <TabsContent value="entries">
          <ListTruncationBadge
            query={entries}
            label={t('entries.truncated', {
              n: entries.data?.items?.length ?? 0,
            })}
            hint={t('entries.truncatedHint')}
            className="px-0 pt-0 pb-3"
          />
          <DataTable
            columns={entryColumns}
            data={entries.data?.items ?? []}
            isLoading={entries.isLoading}
            error={entries.error}
            onRetry={() => entries.refetch()}
            searchable
            searchPlaceholder={t('entries.search')}
            getRowId={(r) => r.id ?? `${r.kind}:${r.slug}:${r.version}`}
            onRowClick={(r) => {
              if (!r.id) return
              setSelectedEntry(r.id)
              setEntryDetailOpen(true)
            }}
            toolbar={
              <>
                <Select value={kindFilter} onValueChange={setKindFilter}>
                  <SelectTrigger
                    className="w-36"
                    aria-label={t('entries.filters.kind')}
                  >
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="all">
                      {t('entries.filters.all')}
                    </SelectItem>
                    {ENTRY_KINDS.map((k) => (
                      <SelectItem key={k} value={k}>
                        {t(`kind.${k}`, { defaultValue: k })}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
                <Select value={statusFilter} onValueChange={setStatusFilter}>
                  <SelectTrigger
                    className="w-36"
                    aria-label={t('entries.filters.status')}
                  >
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="all">
                      {t('entries.filters.all')}
                    </SelectItem>
                    {ENTRY_STATUSES.map((s) => (
                      <SelectItem key={s} value={s}>
                        {t(`status.${s}`, { defaultValue: s })}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
                {canWriteEntry && (
                  <Button
                    variant="primary"
                    size="sm"
                    onClick={() => setEditorOpen(true)}
                  >
                    <Plus />
                    {t('entries.newEntry')}
                  </Button>
                )}
              </>
            }
            empty={
              <EmptyState
                title={t('empty.entry.title')}
                description={t('empty.entry.description')}
              />
            }
          />
        </TabsContent>

        {canReadInstances && (
          <TabsContent value="instances">
            <ListTruncationBadge
              query={instances}
              label={t('instances.truncated', {
                n: instances.data?.items?.length ?? 0,
              })}
              hint={t('instances.truncatedHint')}
              className="px-0 pt-0 pb-3"
            />
            {instancesLive && (
              <p className="mb-3 text-xs text-muted-foreground">
                {t('instances.liveNote')}
              </p>
            )}
            <DataTable
              columns={instanceColumns}
              data={instances.data?.items ?? []}
              isLoading={instances.isLoading}
              error={instances.error}
              onRetry={() => instances.refetch()}
              searchable
              searchPlaceholder={t('instances.search')}
              getRowId={(r) => r.id ?? `${r.entry_id}:${r.name}`}
              onRowClick={(r) => {
                if (!r.id) return
                setSelectedInstance(r.id)
                setInstanceDetailOpen(true)
              }}
              toolbar={
                <Select
                  value={instanceStatusFilter}
                  onValueChange={setInstanceStatusFilter}
                >
                  <SelectTrigger
                    className="w-36"
                    aria-label={t('instances.filters.status')}
                  >
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="all">
                      {t('instances.filters.all')}
                    </SelectItem>
                    {INSTANCE_STATUSES.map((s) => (
                      <SelectItem key={s} value={s}>
                        {t(`status.${s}`, { defaultValue: s })}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              }
              empty={
                <EmptyState
                  title={t('empty.instance.title')}
                  description={t('empty.instance.description')}
                />
              }
            />
          </TabsContent>
        )}

        {canReadPolicy && (
          <TabsContent value="policy">
            <AdmissionPolicyTab />
          </TabsContent>
        )}
      </Tabs>

      {/* Create a new draft entry. */}
      <EntryEditorDialog
        open={editorOpen}
        onOpenChange={setEditorOpen}
        entry={null}
      />

      {/* Entry detail + lifecycle. */}
      <EntryDetailSheet
        entryId={selectedEntry}
        open={entryDetailOpen}
        onOpenChange={setEntryDetailOpen}
      />

      {/* Instance detail + governance transition. */}
      <InstanceDetailSheet
        instanceId={selectedInstance}
        open={instanceDetailOpen}
        onOpenChange={setInstanceDetailOpen}
      />
    </div>
  )
}
