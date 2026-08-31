// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useQuery } from '@tanstack/react-query'
import { ArrowDown, ArrowRight, ArrowUp, Plug, RefreshCw } from 'lucide-react'
import { useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { DataTable, type TableColumn } from '@/components/data/data-table'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card } from '@/components/ui/card'
import { EmptyState } from '@/components/ui/empty-state'
import { ErrorState, ForbiddenState } from '@/components/ui/error-state'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Skeleton } from '@/components/ui/skeleton'
import {
  normalizeSourceMode,
  RelTimeLabel,
  SourceModeBadge,
  type NormalizedSourceMode,
} from '@/features/shared'
import { StepUpRequiredState } from '@/components/layout/step-up-state'
import { ApiError } from '@/lib/api/errors'
import { cn } from '@/lib/utils'
import { healthApi, healthKeys } from './api'
import { HealthStateBadge } from './health-state-badge'
import type { ConnectorHealthDTO, ConnectorSummaryDTO } from './types'

const REFETCH_INTERVAL = 15_000

interface ConnectorHealthTabProps {
  tenant: string | null
}

export function ConnectorHealthTab({ tenant }: ConnectorHealthTabProps) {
  const { t } = useTranslation('health')

  const query = useQuery({
    queryKey: healthKeys.connectorHealth(tenant),
    queryFn: () => healthApi.connectorHealth(),
    refetchInterval: REFETCH_INTERVAL,
  })

  const items = query.data?.items ?? []
  const summary = query.data?.summary

  const error = query.error
  // ⛔ ASEGURAMIENTO ANTES QUE ROL, y aquí el defecto viajaba dentro de un BOOLEANO: un
  // `step_up_required` satisface `isForbidden` —que es sólo el status (lib/api/errors.ts:59)—
  // así que `forbidden` salía cierto y la pantalla entera se sustituía por una acusación
  // falsa. El aseguramiento se separa ANTES de derivar el booleano de rol.
  const stepUp = error instanceof ApiError && error.isStepUpRequired
  const forbidden = !stepUp && error instanceof ApiError && error.isForbidden

  if (stepUp) {
    return (
      <StepUpRequiredState
        action="generic"
        onElevated={() => void query.refetch()}
      />
    )
  }
  if (forbidden) {
    return (
      <ForbiddenState
        title={t('forbidden.title')}
        description={t('forbidden.description')}
      />
    )
  }

  return (
    <div className="flex flex-col gap-4">
      {/* Summary tiles */}
      {summary && <SummaryTiles summary={summary} loading={query.isLoading} />}

      {/* Connector table */}
      <div className="flex items-center justify-between">
        <p className="text-xs text-muted-foreground">
          {t('connectors.autoRefresh')}
        </p>
        <Button
          variant="ghost"
          size="sm"
          onClick={() => void query.refetch()}
          disabled={query.isFetching}
        >
          <RefreshCw
            className={cn('size-3.5', query.isFetching && 'animate-spin')}
          />
          {t('refresh')}
        </Button>
      </div>

      <ConnectorTable
        items={items}
        isLoading={query.isLoading}
        error={error}
        onRetry={() => void query.refetch()}
      />
    </div>
  )
}

function SummaryTiles({
  summary,
  loading,
}: {
  summary: ConnectorSummaryDTO
  loading: boolean
}) {
  const { t } = useTranslation('health')
  return (
    <div className="grid grid-cols-2 gap-3 sm:grid-cols-4">
      <SummaryTile
        label={t('connectors.summary.running')}
        value={summary.running}
        tone="success"
        loading={loading}
      />
      <SummaryTile
        label={t('connectors.summary.failed')}
        value={summary.failed}
        tone={summary.failed > 0 ? 'danger' : undefined}
        loading={loading}
      />
      <SummaryTile
        label={t('connectors.summary.stopped')}
        value={summary.stopped}
        tone={summary.stopped > 0 ? 'warning' : undefined}
        loading={loading}
      />
      <SummaryTile
        label={t('connectors.summary.disabled')}
        value={summary.disabled}
        loading={loading}
      />
    </div>
  )
}

function SummaryTile({
  label,
  value,
  tone,
  loading,
}: {
  label: string
  value: number
  tone?: 'success' | 'warning' | 'danger'
  loading?: boolean
}) {
  return (
    <Card className="p-3">
      <div className="text-xs text-muted-foreground">{label}</div>
      {loading ? (
        <Skeleton className="mt-1 h-7 w-12" />
      ) : (
        <div
          className={cn(
            'font-display text-2xl font-semibold tabular-nums',
            tone === 'success' && 'text-success',
            tone === 'warning' && 'text-warning',
            tone === 'danger' && 'text-danger',
            !tone && 'text-foreground',
          )}
        >
          {value}
        </div>
      )}
    </Card>
  )
}

function TrendIcon({ trend }: { trend: string }) {
  const { t } = useTranslation('health')
  switch (trend) {
    case 'up':
      return (
        <span
          className="inline-flex items-center gap-1 text-xs text-success"
          title={t('connectors.trend.up')}
        >
          <ArrowUp className="size-3" />
          {t('connectors.trend.up')}
        </span>
      )
    case 'down':
      return (
        <span
          className="inline-flex items-center gap-1 text-xs text-danger"
          title={t('connectors.trend.down')}
        >
          <ArrowDown className="size-3" />
          {t('connectors.trend.down')}
        </span>
      )
    default:
      return (
        <span
          className="inline-flex items-center gap-1 text-xs text-muted-foreground"
          title={t('connectors.trend.stable')}
        >
          <ArrowRight className="size-3" />
          {t('connectors.trend.stable')}
        </span>
      )
  }
}

function ConnectorTable({
  items,
  isLoading,
  error,
  onRetry,
}: {
  items: ConnectorHealthDTO[]
  isLoading: boolean
  error: unknown
  onRetry: () => void
}) {
  const { t } = useTranslation(['health', 'shared'])
  const [modeFilter, setModeFilter] = useState<NormalizedSourceMode | 'all'>(
    'all',
  )
  const filteredItems = useMemo(
    () =>
      items.filter(
        (item) =>
          modeFilter === 'all' ||
          normalizeSourceMode(item.source_mode) === modeFilter,
      ),
    [items, modeFilter],
  )

  const columns = useMemo<TableColumn<ConnectorHealthDTO>[]>(
    () => [
      {
        id: 'name',
        accessorKey: 'name',
        header: t('connectors.cols.name'),
        cell: ({ row }) => {
          const c = row.original
          return (
            <div className="min-w-0">
              <div className="truncate font-medium text-foreground">
                {c.title || c.name}
              </div>
              {c.title && c.title !== c.name && (
                <div className="truncate font-mono text-xs text-muted-foreground">
                  {c.name}
                </div>
              )}
            </div>
          )
        },
      },
      {
        accessorKey: 'kind',
        header: t('connectors.cols.kind'),
        cell: ({ getValue }) => (
          <Badge variant="outline">{getValue<string>()}</Badge>
        ),
      },
      {
        id: 'sourceMode',
        accessorKey: 'source_mode',
        header: t('connectors.cols.mode'),
        cell: ({ row }) => <SourceModeBadge value={row.original.source_mode} />,
      },
      {
        id: 'health',
        accessorKey: 'health_state',
        header: t('connectors.cols.health'),
        cell: ({ row }) => (
          <HealthStateBadge state={row.original.health_state} />
        ),
      },
      {
        accessorKey: 'status',
        header: t('connectors.cols.status'),
        cell: ({ getValue }) => {
          const s = getValue<string>()
          return (
            <Badge
              variant={
                s === 'running'
                  ? 'success'
                  : s === 'failed'
                    ? 'danger'
                    : 'neutral'
              }
            >
              {t(`connectors.status.${s}`, { defaultValue: s })}
            </Badge>
          )
        },
      },
      {
        id: 'trend',
        accessorKey: 'trend',
        header: t('connectors.cols.trend'),
        cell: ({ row }) => <TrendIcon trend={row.original.trend} />,
      },
      {
        id: 'lastPolled',
        accessorKey: 'last_polled_at',
        header: t('connectors.cols.lastPoll'),
        cell: ({ getValue }) => (
          <RelTimeLabel ts={getValue<string | undefined>()} />
        ),
      },
    ],
    [t],
  )

  // La negación tiene que excluir las DOS negativas, no sólo la de rol: si no, un
  // `step_up_required` caía aquí y se pintaba como avería roja además de lo de arriba.
  if (
    error &&
    !(
      error instanceof ApiError &&
      (error.isForbidden || error.isStepUpRequired)
    )
  ) {
    return <ErrorState retry={onRetry} />
  }

  const emptyTitle =
    items.length > 0
      ? t('connectors.emptyMode.title')
      : t('connectors.empty.title')
  const emptyDescription =
    items.length > 0
      ? t('connectors.emptyMode.description')
      : t('connectors.empty.description')

  return (
    <div className="flex flex-col gap-3">
      <div className="flex justify-end">
        <Select
          value={modeFilter}
          onValueChange={(v) =>
            setModeFilter(v as NormalizedSourceMode | 'all')
          }
        >
          <SelectTrigger
            className="h-8 w-36"
            aria-label={t('connectors.modeFilter')}
          >
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="all">{t('connectors.modeAll')}</SelectItem>
            <SelectItem value="export">
              {t('shared:sourceModes.export')}
            </SelectItem>
            <SelectItem value="live">{t('shared:sourceModes.live')}</SelectItem>
          </SelectContent>
        </Select>
      </div>
      <DataTable
        columns={columns}
        data={filteredItems}
        isLoading={isLoading}
        error={error}
        onRetry={onRetry}
        getRowId={(r) => r.name}
        searchable
        searchPlaceholder={t('connectors.searchPlaceholder')}
        stickyHeader
        empty={
          <EmptyState
            icon={<Plug />}
            title={emptyTitle}
            description={emptyDescription}
          />
        }
      />
    </div>
  )
}
