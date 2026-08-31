// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { Panel } from '@xyflow/react'
import { ListTruncationBadge } from '@/features/_intel'
import { useQuery } from '@tanstack/react-query'
import { Network } from 'lucide-react'
import { useMemo } from 'react'
import { useTranslation } from 'react-i18next'
import { EmptyState } from '@/components/ui/empty-state'
import { ErrorState, ForbiddenState } from '@/components/ui/error-state'
import { Spinner } from '@/components/ui/spinner'
import { GraphCanvas } from '@/features/shared'
import { StepUpRequiredState } from '@/components/layout/step-up-state'
import { ApiError } from '@/lib/api/errors'
import { cn } from '@/lib/utils'
import { healthApi, healthKeys } from './api'
import { buildDependencyGraph } from './dependency-model'
import { depEdgeTypes, depNodeTypes } from './dependency-nodes'

const DEP_LIMIT = 500

/** The health-state legend swatches (same palette as every other health view). */
const LEGEND: { key: string; dot: string }[] = [
  { key: 'healthy', dot: 'bg-success' },
  { key: 'degraded', dot: 'bg-warning' },
  { key: 'down', dot: 'bg-danger' },
  { key: 'observed', dot: 'bg-info' },
  { key: 'unknown', dot: 'bg-graphite-400' },
]

export function DependencyMap({ tenant }: { tenant: string | null }) {
  const { t } = useTranslation('health')

  const query = useQuery({
    queryKey: healthKeys.dependencies(tenant),
    queryFn: () => healthApi.dependencies({ limit: DEP_LIMIT }),
  })

  const built = useMemo(
    () =>
      query.data
        ? buildDependencyGraph(query.data)
        : {
            nodes: [],
            edges: [],
            stats: {
              nodes: 0,
              edges: 0,
              healthy: 0,
              degraded: 0,
              down: 0,
              observed: 0,
              unknown: 0,
            },
          },
    [query.data],
  )

  if (query.isLoading) {
    return (
      <div className="flex h-[60vh] min-h-[400px] items-center justify-center rounded-lg border border-border bg-surface">
        <Spinner />
      </div>
    )
  }
  if (query.error) {
    // ⛔ ASEGURAMIENTO ANTES QUE ROL: `isForbidden` es SÓLO el status (lib/api/errors.ts:59)
    // y un `step_up_required` lo satisface también, así que leerlo primero acusaba al
    // operador de no tener un permiso que SÍ tiene, y sin salida.
    if (query.error instanceof ApiError && query.error.isStepUpRequired)
      return (
        <StepUpRequiredState
          action="generic"
          onElevated={() => void query.refetch()}
        />
      )
    return query.error instanceof ApiError && query.error.isForbidden ? (
      <ForbiddenState
        title={t('forbidden.title')}
        description={t('forbidden.description')}
      />
    ) : (
      <ErrorState retry={() => void query.refetch()} />
    )
  }
  if (built.nodes.length === 0) {
    return (
      <EmptyState
        icon={<Network />}
        title={t('deps.empty.title')}
        description={t('deps.empty.description')}
      />
    )
  }

  return (
    <div className="flex flex-col gap-3">
      <p className="max-w-3xl text-sm text-muted-foreground">
        {t('deps.subtitle')}
      </p>

      <ListTruncationBadge
        query={query}
        label={t('deps.truncated', { n: DEP_LIMIT })}
        hint={t('deps.truncatedHint')}
      />

      {/* Graph (desktop) */}
      <div className="hidden h-[64vh] min-h-[440px] md:block">
        <GraphCanvas
          nodes={built.nodes}
          edges={built.edges}
          nodeTypes={depNodeTypes}
          edgeTypes={depEdgeTypes}
          minimapColor={(n) =>
            String(
              (n.data as { color?: string }).color ??
                'var(--color-graphite-400)',
            )
          }
          fitKey={`${built.nodes.length}|${built.edges.length}`}
        >
          {/* React Flow puts its pointer controls in the bottom-left corner.
              Keep the legend elsewhere so its Panel wrapper cannot cover Zoom
              in/out or Fit view even though the legend body is non-interactive. */}
          <Panel position="top-right">
            <DepLegend />
          </Panel>
        </GraphCanvas>
      </div>

      {/* Mobile fallback: the graph needs width; show a dignified summary. */}
      <div className="rounded-lg border border-border bg-surface p-6 text-center md:hidden">
        <Network className="mx-auto mb-2 size-6 text-muted-foreground" />
        <p className="text-sm font-medium text-foreground">
          {t('deps.mobile.title')}
        </p>
        <p className="mt-1 text-sm text-muted-foreground">
          {t('deps.mobile.summary', {
            nodes: built.stats.nodes,
            edges: built.stats.edges,
          })}
        </p>
        <p className="mt-1 text-xs text-muted-foreground">
          {t('deps.mobile.hint')}
        </p>
        <div className="mt-3 flex flex-wrap items-center justify-center gap-2">
          {LEGEND.filter(
            (l) => built.stats[l.key as keyof typeof built.stats] > 0,
          ).map((l) => (
            <span
              key={l.key}
              className="inline-flex items-center gap-1.5 text-xs text-muted-foreground"
            >
              <span className={cn('size-2 rounded-full', l.dot)} aria-hidden />
              {t(`state.${l.key}`)}
              <span className="font-mono tabular-nums text-foreground">
                {built.stats[l.key as keyof typeof built.stats]}
              </span>
            </span>
          ))}
        </div>
      </div>
    </div>
  )
}

function DepLegend() {
  const { t } = useTranslation('health')
  return (
    <div className="pointer-events-none flex flex-col gap-1.5 rounded-md border border-border bg-surface/90 px-2.5 py-2 shadow-sm backdrop-blur">
      <span className="text-[10px] font-semibold uppercase tracking-wide text-muted-foreground">
        {t('deps.legendTitle')}
      </span>
      <div className="flex flex-wrap items-center gap-x-3 gap-y-1">
        {LEGEND.map((l) => (
          <span key={l.key} className="inline-flex items-center gap-1.5">
            <span className={cn('size-2 rounded-full', l.dot)} aria-hidden />
            <span className="text-[11px] text-muted-foreground">
              {t(`state.${l.key}`)}
            </span>
          </span>
        ))}
      </div>
      <div className="flex flex-wrap items-center gap-x-3 gap-y-1 border-t border-border pt-1.5">
        <RelSwatch
          color="var(--color-info)"
          label={t('deps.relation.uses_mcp')}
        />
        <RelSwatch
          color="var(--color-muted-foreground)"
          label={t('deps.relation.uses_tool')}
        />
        <RelSwatch
          color="var(--color-accent-text)"
          label={t('deps.relation.delegates_to')}
        />
      </div>
    </div>
  )
}

function RelSwatch({ color, label }: { color: string; label: string }) {
  return (
    <span className="inline-flex items-center gap-1.5">
      <svg
        width="22"
        height="8"
        viewBox="0 0 22 8"
        aria-hidden
        className="shrink-0"
      >
        <line
          x1="1"
          y1="4"
          x2="21"
          y2="4"
          stroke={color}
          strokeWidth={2}
          strokeLinecap="round"
        />
      </svg>
      <span className="text-[11px] text-muted-foreground">{label}</span>
    </span>
  )
}
