// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { Badge, type BadgeVariant } from '@/components/ui/badge'
import { EmptyState } from '@/components/ui/empty-state'
import { DataTable, type TableColumn } from '@/components/data/data-table'
import { useAuth } from '@/lib/auth/context'
import { RelTimeLabel } from '@/features/shared'
import { deployApi, deployKeys } from './api'
import { GateBadge } from './diff'
import './i18n'
import type { OperationDTO } from './types'
import { ListTruncationBadge } from '@/features/_intel'

const OP_STATUS_VARIANT: Record<string, BadgeVariant> = {
  planned: 'info',
  requested: 'info',
  blocked: 'danger',
  applied: 'success',
  verified: 'success',
  retired: 'neutral',
  failed: 'danger',
  noop: 'neutral',
}

export function OperationsTable() {
  const { t } = useTranslation('deploy')
  const { activeTenant } = useAuth()

  // Governance/approval liveness — poll the ledger so a pending gate decision and
  // its eventual blocked/applied outcome surface without a manual refresh (~12s).
  const query = useQuery({
    queryKey: deployKeys.operations(activeTenant),
    queryFn: () => deployApi.listOperations(),
    refetchInterval: 12_000,
  })

  const columns: TableColumn<OperationDTO, unknown>[] = [
    {
      accessorKey: 'op',
      header: t('operations.op'),
      cell: ({ row }) => (
        <Badge variant="neutral">
          {t(`operations.opKind.${row.original.op}`, {
            defaultValue: row.original.op,
          })}
        </Badge>
      ),
    },
    {
      id: 'version',
      header: t('operations.version'),
      cell: ({ row }) => (
        <span className="font-mono text-xs tabular-nums text-muted-foreground">
          {t('operations.fromTo', {
            from: row.original.from_version,
            to: row.original.to_version,
          })}
        </span>
      ),
    },
    {
      accessorKey: 'status',
      header: t('operations.status'),
      cell: ({ row }) => (
        <Badge variant={OP_STATUS_VARIANT[row.original.status] ?? 'neutral'}>
          {t(`operations.opStatus.${row.original.status}`, {
            defaultValue: row.original.status,
          })}
        </Badge>
      ),
    },
    {
      id: 'gate',
      header: t('operations.gate'),
      cell: ({ row }) =>
        row.original.gate_status ? (
          <GateBadge gate={row.original.gate_status} />
        ) : (
          '—'
        ),
    },
    {
      id: 'approval',
      header: t('operations.approval'),
      cell: ({ row }) => (
        <span className="font-mono text-xs text-muted-foreground">
          {row.original.approval_ref || '—'}
        </span>
      ),
    },
    {
      accessorKey: 'actor',
      header: t('operations.actor'),
      cell: ({ row }) => (
        <span className="font-mono text-xs text-muted-foreground">
          {row.original.actor || '—'}
        </span>
      ),
    },
    {
      id: 'plan_hash',
      header: t('operations.planHash'),
      cell: ({ row }) => (
        <span className="font-mono text-xs text-muted-foreground">
          {row.original.plan_hash ? row.original.plan_hash.slice(0, 12) : '—'}
        </span>
      ),
    },
    {
      id: 'when',
      header: t('operations.when'),
      cell: ({ row }) =>
        row.original.occurred_at ? (
          <RelTimeLabel ts={row.original.occurred_at} />
        ) : (
          '—'
        ),
    },
  ]

  return (
    <div className="flex flex-col gap-3">
      <p className="text-xs text-muted-foreground">
        {t('operations.description')}
      </p>
      <ListTruncationBadge
        query={query}
        label={t('operations.truncated', { n: query.data?.items?.length ?? 0 })}
        hint={t('truncatedHint')}
      />
      <DataTable
        columns={columns}
        data={query.data?.items ?? []}
        isLoading={query.isLoading}
        error={query.error}
        onRetry={() => query.refetch()}
        searchable
        searchPlaceholder={t('operations.search')}
        getRowId={(r) =>
          `${r.definition_id}:${r.op}:${r.occurred_at ?? ''}:${r.plan_hash ?? ''}`
        }
        empty={
          <EmptyState
            title={t('empty.operations.title')}
            description={t('empty.operations.description')}
          />
        }
      />
    </div>
  )
}
