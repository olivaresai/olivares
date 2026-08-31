// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { Badge } from '@/components/ui/badge'
import { ForbiddenState } from '@/components/ui/error-state'
import { EmptyState } from '@/components/ui/empty-state'
import { DataTable, type TableColumn } from '@/components/data/data-table'
import { AccessModeBadge, StatusBadge } from '@/components/data/badges'
import { SecretRef } from '@/components/data/secret-ref'
import { useAuth } from '@/lib/auth/context'
import { deployApi, deployKeys } from './api'
import './i18n'
import type { WiringDTO } from './types'
import { ListTruncationBadge } from '@/features/_intel'

/**
 * AttributionTag renders wiring provenance. `degraded` is an HONESTY signal — the
 * per-agent identity could not be firmly bound, so attribution is approximate. It is
 * rendered as a calm, muted neutral note, NEVER alarming and NEVER as a failure.
 */
function AttributionTag({ attribution }: { attribution: string }) {
  const { t } = useTranslation('deploy')
  if (attribution === 'degraded') {
    return (
      <span
        className="inline-flex items-center gap-1.5 rounded-sm border border-dashed border-border-strong px-1.5 py-0.5 text-xs font-medium text-muted-foreground"
        title={t('wirings.degradedHint')}
      >
        <span
          className="size-1.5 rounded-full border border-current"
          aria-hidden
        />
        {t('wirings.degraded')}
      </span>
    )
  }
  return (
    <span
      className="inline-flex items-center gap-1.5 rounded-sm border border-confidence-attributed/40 px-1.5 py-0.5 text-xs font-medium text-confidence-attributed"
      title={t('wirings.firmHint')}
    >
      <span className="size-1.5 rounded-full bg-current" aria-hidden />
      {t('wirings.firm')}
    </span>
  )
}

export function WiringsTable() {
  const { t } = useTranslation('deploy')
  const { activeTenant, can } = useAuth()
  const canRead = can('deploy:wiring:read')

  const query = useQuery({
    queryKey: deployKeys.wirings(activeTenant),
    queryFn: () => deployApi.listWirings(),
    enabled: canRead,
  })

  const columns: TableColumn<WiringDTO, unknown>[] = [
    {
      accessorKey: 'agent_ref',
      header: t('wirings.agent'),
      cell: ({ row }) => (
        <span className="font-mono text-xs font-medium text-foreground">
          {row.original.agent_ref}
        </span>
      ),
    },
    {
      id: 'resource',
      header: t('wirings.resource'),
      cell: ({ row }) => (
        <span className="flex items-center gap-1.5">
          <Badge variant="neutral">{row.original.resource_kind}</Badge>
          <span className="font-mono text-xs text-foreground">
            {row.original.resource_ref}
          </span>
        </span>
      ),
    },
    {
      accessorKey: 'mode',
      header: t('wirings.mode'),
      cell: ({ row }) => <AccessModeBadge mode={row.original.mode} />,
    },
    {
      id: 'secret',
      header: t('wirings.secret'),
      cell: ({ row }) =>
        row.original.secret_ref ? (
          <SecretRef name={row.original.secret_ref} />
        ) : (
          '—'
        ),
    },
    {
      accessorKey: 'status',
      header: t('wirings.status'),
      cell: ({ row }) => <StatusBadge status={row.original.status} />,
    },
    {
      accessorKey: 'attribution',
      header: t('wirings.attribution'),
      cell: ({ row }) => (
        <AttributionTag attribution={row.original.attribution} />
      ),
    },
    {
      accessorKey: 'version',
      header: t('wirings.version'),
      cell: ({ row }) => (
        <span className="font-mono tabular-nums">{row.original.version}</span>
      ),
    },
  ]

  // The Wirings face needs a SEPARATE permission (deploy:wiring:read). A user with
  // deployment:read but not wiring:read sees a calm forbidden state, never an error.
  if (!canRead) {
    return (
      <div className="rounded-lg border border-border bg-surface">
        <ForbiddenState
          title={t('wirings.forbidden')}
          description={t('wirings.forbiddenHint')}
        />
      </div>
    )
  }

  return (
    <div className="flex flex-col gap-3">
      <p className="text-xs text-muted-foreground">
        {t('wirings.description')}
      </p>
      <ListTruncationBadge
        query={query}
        label={t('wirings.truncated', { n: query.data?.items?.length ?? 0 })}
        hint={t('truncatedHint')}
      />
      <DataTable
        columns={columns}
        data={query.data?.items ?? []}
        isLoading={query.isLoading}
        error={query.error}
        onRetry={() => query.refetch()}
        searchable
        searchPlaceholder={t('wirings.search')}
        getRowId={(r) =>
          `${r.definition_id}:${r.agent_ref}:${r.resource_kind}:${r.resource_ref}`
        }
        empty={
          <EmptyState
            title={t('empty.wirings.title')}
            description={t('empty.wirings.description')}
          />
        }
      />
    </div>
  )
}
