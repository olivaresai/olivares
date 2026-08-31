// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Observability presentational pieces — PURE: they take the live read-model data as
// props and render it (the per-standard ingestion table, the per-source counters,
// the reusable maturity badge, the trace list, and the span waterfall). No fetching,
// no auth — so they are trivially testable with fixtures and reused by the container.
// Every figure is formatted via @/lib/format; waterfall colours come ONLY from
// useChartTheme.
import { Fragment, useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { DataTable, type TableColumn } from '@/components/data/data-table'
import { AccessibleChart } from '@/components/data/accessible-chart'
import { Badge } from '@/components/ui/badge'
import { EmptyState } from '@/components/ui/empty-state'
import { useChartTheme } from '@/components/charts'
import { CaveatNotice } from '@/features/_intel'
import {
  formatDateTime,
  formatDuration,
  formatInt,
  humanize,
} from '@/lib/format'
import { cn } from '@/lib/utils'
import type {
  IngestionSource,
  IngestionStandard,
  StandardMaturity,
  TraceDetail,
  TraceListItem,
  TraceSpan,
} from './types'

// --- standard maturity badge -------------------------------------------------

const MATURITY_VARIANT: Record<
  string,
  'success' | 'info' | 'warning' | 'neutral'
> = {
  stable: 'success',
  ga: 'success',
  pre_1_0: 'warning',
  development: 'warning',
}

/**
 * StandardMaturityBadge — the reusable version-pinned maturity chip. It carries the
 * upstream-declared maturity verbatim (Development / GA / Pre-1.0 / Stable) and pins
 * the version next to it, so a pre-stable convention can NEVER read as stable. Used by
 * the ingestion table and any future per-standard surface.
 */
export function StandardMaturityBadge({
  maturity,
  version,
  className,
}: {
  maturity: StandardMaturity
  /** Pinned upstream version (e.g. "1.41.1"); "—" when not version-pinned. */
  version?: string
  className?: string
}) {
  const { t } = useTranslation('observability')
  const key = String(maturity).toLowerCase()
  const variant = MATURITY_VARIANT[key] ?? 'neutral'
  const emphatic = key === 'development' || key === 'pre_1_0'
  const showVersion = version && version !== '—'
  return (
    <Badge variant={variant} className={cn('gap-1', className)}>
      <span className={cn(emphatic && 'font-semibold')}>
        {t(`maturity.${key}`, { defaultValue: humanize(maturity) })}
      </span>
      {showVersion ? (
        <span className="font-mono tabular-nums opacity-80">v{version}</span>
      ) : null}
    </Badge>
  )
}

// --- ingestion health table --------------------------------------------------

function StatusChip({ status }: { status: string }) {
  const { t } = useTranslation('observability')
  const key = String(status).toLowerCase()
  const variant =
    key === 'active'
      ? 'success'
      : key === 'available'
        ? 'info'
        : key === 'blocked'
          ? 'warning'
          : 'neutral'
  return (
    <Badge variant={variant}>
      {t(`ingestion.status.${key}`, { defaultValue: humanize(status) })}
    </Badge>
  )
}

/** Span / trace status. `error` reads loud (danger); `ok` calm success; `unset`
 *  neutral — the OTel status code mapped to a verdict, with our own labels (the
 *  shared OutcomeBadge has no `ok`/`unset` vocabulary and maps `error` to warning). */
export function SpanStatusBadge({ status }: { status: string }) {
  const { t } = useTranslation('observability')
  const key = String(status).toLowerCase()
  const variant =
    key === 'ok' ? 'success' : key === 'error' ? 'danger' : 'neutral'
  return (
    <Badge variant={variant}>
      {t(`traces.status.${key}`, { defaultValue: humanize(status) })}
    </Badge>
  )
}

export function IngestionHealthTable({
  standards,
}: {
  standards: IngestionStandard[]
}) {
  const { t, i18n } = useTranslation('observability')
  const columns = useMemo<TableColumn<IngestionStandard>[]>(
    () => [
      {
        accessorKey: 'label',
        header: t('ingestion.columns.standard'),
        cell: ({ row }) => (
          <div className="flex flex-col gap-0.5">
            <span className="font-medium text-foreground">
              {row.original.label}
            </span>
            {row.original.upstream_repo && row.original.upstream_ref ? (
              <span
                className="font-mono text-[11px] leading-tight text-muted-foreground"
                title={`${row.original.upstream_repo} ${row.original.upstream_ref}`}
              >
                {row.original.upstream_repo} {row.original.upstream_ref}
              </span>
            ) : null}
          </div>
        ),
      },
      {
        accessorKey: 'direction',
        header: t('ingestion.columns.direction'),
        cell: ({ row }) => (
          <Badge variant="outline">
            {t(`ingestion.direction.${row.original.direction}`, {
              defaultValue: humanize(row.original.direction),
            })}
          </Badge>
        ),
      },
      {
        id: 'maturity',
        header: t('ingestion.columns.maturity'),
        cell: ({ row }) => (
          <StandardMaturityBadge
            maturity={row.original.maturity}
            version={row.original.version}
          />
        ),
      },
      {
        accessorKey: 'status',
        header: t('ingestion.columns.status'),
        cell: ({ row }) => (
          <div className="flex flex-col gap-1">
            <StatusChip status={row.original.status} />
            {row.original.opt_in_gate ? (
              <span className="text-xs text-muted-foreground">
                {t('ingestion.gate')}:{' '}
                <span className="font-mono">{row.original.opt_in_gate}</span>{' '}
                <span className="font-medium">
                  {/* Tri-state, honestly: the backend only asserts `true` when
                      evidence flowed on the bus; absent = unknowable from inside
                      the engine (the gate lives in connector config) — never a
                      false-claimed "off". */}
                  {row.original.opt_in_active === true
                    ? t('ingestion.gateOn')
                    : row.original.opt_in_active === false
                      ? t('ingestion.gateOff')
                      : t('ingestion.gateNoEvidence')}
                </span>
              </span>
            ) : null}
          </div>
        ),
      },
      {
        id: 'records',
        header: t('ingestion.columns.records'),
        cell: ({ row }) => (
          <span className="font-mono tabular-nums text-muted-foreground">
            {row.original.records_total === undefined
              ? t('ingestion.recordsPending')
              : formatInt(row.original.records_total, i18n.language)}
          </span>
        ),
      },
      {
        accessorKey: 'last_seen',
        header: t('ingestion.columns.lastSeen'),
        cell: ({ row }) => (
          <span className="text-xs text-muted-foreground">
            {formatDateTime(row.original.last_seen, i18n.language)}
          </span>
        ),
      },
    ],
    [t, i18n.language],
  )
  return (
    <DataTable<IngestionStandard>
      columns={columns}
      data={standards}
      getRowId={(r) => r.id}
      label={t('ingestion.title')}
      empty={
        <EmptyState
          title={t('empty.ingestionHealth.title')}
          description={t('empty.ingestionHealth.description')}
        />
      }
    />
  )
}

// --- per-source live counters --------------------------------------------------

/**
 * IngestionSourcesTable — the per-source half of the live read-model: one row per
 * bus Event.Source (connector/module name) with its counters by observation kind
 * and, for edges, by collector signal. Counters are PROCESS-GLOBAL and accumulate
 * since engine start (`since`) — the caveat says so, and an empty list renders an
 * honest "nothing observed" state, never a fabricated row.
 */
export function IngestionSourcesTable({
  sources,
  since,
}: {
  sources: IngestionSource[]
  /** RFC3339 module-start instant the counters accumulate from. */
  since: string
}) {
  const { t, i18n } = useTranslation('observability')
  const columns = useMemo<TableColumn<IngestionSource>[]>(
    () => [
      {
        accessorKey: 'name',
        header: t('sources.columns.source'),
        cell: ({ row }) => (
          <span className="font-mono text-xs text-foreground">
            {row.original.name}
          </span>
        ),
      },
      {
        accessorKey: 'records_total',
        header: t('sources.columns.records'),
        cell: ({ row }) => (
          <span className="font-mono tabular-nums">
            {formatInt(row.original.records_total, i18n.language)}
          </span>
        ),
      },
      {
        id: 'kinds',
        header: t('sources.columns.kinds'),
        cell: ({ row }) => (
          <span className="flex flex-wrap gap-1">
            {Object.entries(row.original.kinds).map(([kind, n]) => (
              <Badge key={kind} variant="outline">
                {t(`sources.kind.${kind}`, { defaultValue: humanize(kind) })}{' '}
                <span className="font-mono tabular-nums">
                  {formatInt(n, i18n.language)}
                </span>
              </Badge>
            ))}
          </span>
        ),
      },
      {
        id: 'signals',
        header: t('sources.columns.signals'),
        cell: ({ row }) =>
          row.original.signals ? (
            <span className="flex flex-wrap gap-x-2 gap-y-0.5 font-mono text-xs text-muted-foreground">
              {Object.entries(row.original.signals).map(([sig, n]) => (
                <span key={sig}>
                  {sig}{' '}
                  <span className="tabular-nums">
                    {formatInt(n, i18n.language)}
                  </span>
                </span>
              ))}
            </span>
          ) : (
            <span className="text-muted-foreground">—</span>
          ),
      },
      {
        accessorKey: 'first_seen',
        header: t('sources.columns.firstSeen'),
        cell: ({ row }) => (
          <span className="text-xs text-muted-foreground">
            {formatDateTime(row.original.first_seen, i18n.language)}
          </span>
        ),
      },
      {
        accessorKey: 'last_seen',
        header: t('sources.columns.lastSeen'),
        cell: ({ row }) => (
          <span className="text-xs text-muted-foreground">
            {formatDateTime(row.original.last_seen, i18n.language)}
          </span>
        ),
      },
    ],
    [t, i18n.language],
  )

  if (sources.length === 0) {
    return (
      <EmptyState
        title={t('sources.empty')}
        description={t('sources.emptyHint')}
      />
    )
  }

  return (
    <div className="flex flex-col gap-2">
      <DataTable<IngestionSource>
        columns={columns}
        data={sources}
        getRowId={(r) => r.name}
        label={t('sources.title')}
        empty={
          <EmptyState
            title={t('empty.ingestionSources.title')}
            description={t('empty.ingestionSources.description')}
          />
        }
      />
      <CaveatNotice tone="neutral">
        {t('sources.sinceNote', {
          since: formatDateTime(since, i18n.language),
        })}
      </CaveatNotice>
    </div>
  )
}

// --- trace list --------------------------------------------------------------

export function TraceList({
  traces,
  onSelect,
}: {
  traces: TraceListItem[]
  onSelect?: (trace: TraceListItem) => void
}) {
  const { t, i18n } = useTranslation('observability')
  const columns = useMemo<TableColumn<TraceListItem>[]>(
    () => [
      {
        accessorKey: 'root_name',
        header: t('traces.columns.trace'),
        cell: ({ row }) => (
          <div className="flex flex-col gap-0.5">
            <span className="font-medium text-foreground">
              {row.original.root_name}
            </span>
            <span className="font-mono text-xs text-muted-foreground">
              {row.original.trace_id}
            </span>
          </div>
        ),
      },
      {
        accessorKey: 'started_at',
        header: t('traces.columns.started'),
        cell: ({ row }) => (
          <span className="text-xs text-muted-foreground">
            {formatDateTime(row.original.started_at, i18n.language)}
          </span>
        ),
      },
      {
        accessorKey: 'duration_ms',
        header: t('traces.columns.duration'),
        cell: ({ row }) => (
          <span className="font-mono tabular-nums">
            {formatDuration(row.original.duration_ms)}
          </span>
        ),
      },
      {
        accessorKey: 'span_count',
        header: t('traces.columns.spans'),
        cell: ({ row }) => (
          <span className="font-mono tabular-nums text-muted-foreground">
            {formatInt(row.original.span_count, i18n.language)}
          </span>
        ),
      },
      {
        accessorKey: 'agent_count',
        header: t('traces.columns.agents'),
        cell: ({ row }) => (
          <span className="font-mono tabular-nums text-muted-foreground">
            {formatInt(row.original.agent_count, i18n.language)}
          </span>
        ),
      },
      {
        accessorKey: 'status',
        header: t('traces.columns.status'),
        cell: ({ row }) => <SpanStatusBadge status={row.original.status} />,
      },
    ],
    [t, i18n.language],
  )
  return (
    <DataTable<TraceListItem>
      columns={columns}
      data={traces}
      getRowId={(r) => r.trace_id}
      onRowClick={onSelect ? (r) => onSelect(r) : undefined}
      label={t('traces.listTitle')}
      empty={
        <EmptyState
          title={t('empty.trace.title')}
          description={t('empty.trace.description')}
        />
      }
    />
  )
}

// --- trace waterfall ---------------------------------------------------------

/** Flatten the span tree into a depth-ordered list (parent before children) so the
 *  waterfall reads top-down. The view PRESENTS the spans; offsets (`start_ms`) and
 *  durations already come from the backend — nothing is recomputed here. */
interface FlatSpan {
  span: TraceSpan
  depth: number
}

function flattenSpans(spans: TraceSpan[]): FlatSpan[] {
  const byParent = new Map<string | undefined, TraceSpan[]>()
  for (const s of spans) {
    const list = byParent.get(s.parent_span_id) ?? []
    list.push(s)
    byParent.set(s.parent_span_id, list)
  }
  for (const list of byParent.values()) {
    list.sort((a, b) => a.start_ms - b.start_ms)
  }
  const known = new Set(spans.map((s) => s.span_id))
  const out: FlatSpan[] = []
  const visit = (parent: string | undefined, depth: number) => {
    for (const s of byParent.get(parent) ?? []) {
      out.push({ span: s, depth })
      visit(s.span_id, depth + 1)
    }
  }
  // Roots = spans with no parent, OR a parent not present in this slice (orphan-safe).
  visit(undefined, 0)
  for (const s of spans) {
    if (s.parent_span_id && !known.has(s.parent_span_id)) {
      out.push({ span: s, depth: 0 })
      visit(s.span_id, 1)
    }
  }
  return out
}

const INDENT_PX = 14

/** Assign a stable colour per distinct actor so multi-agent traces are
 *  visually distinguishable. The palette cycles through the chart theme's
 *  series colours; the mapping is deterministic for a given span set. */
function useActorColors(spans: TraceSpan[]): Map<string, string> {
  const theme = useChartTheme()
  return useMemo(() => {
    const actors = [...new Set(spans.map((s) => s.actor ?? '').filter(Boolean))]
    actors.sort()
    const m = new Map<string, string>()
    for (let i = 0; i < actors.length; i++) {
      m.set(actors[i], theme.series[i % theme.series.length])
    }
    return m
  }, [spans, theme.series])
}

/** Short display label for an actor: strips the kind prefix when it
 *  matches actor_kind (e.g. "user:alice" with kind "user" → "alice"). */
function actorLabel(actor?: string, kind?: string): string {
  if (!actor) return ''
  if (kind && actor.startsWith(kind + ':')) return actor.slice(kind.length + 1)
  return actor
}

export function TraceWaterfall({
  trace,
  onSelectSpan,
}: {
  trace: TraceDetail
  onSelectSpan?: (span: TraceSpan | null) => void
}) {
  const { t } = useTranslation('observability')
  const theme = useChartTheme()
  const flat = useMemo(() => flattenSpans(trace.spans), [trace.spans])
  const actorColors = useActorColors(trace.spans)
  const [selectedId, setSelectedId] = useState<string | null>(null)

  const axisMax = useMemo(() => {
    if (trace.duration_ms && trace.duration_ms > 0) return trace.duration_ms
    return Math.max(1, ...trace.spans.map((s) => s.start_ms + s.duration_ms))
  }, [trace.duration_ms, trace.spans])

  const colorForSpan = (span: TraceSpan): string => {
    if (String(span.status).toLowerCase() === 'error') return theme.danger
    if (span.actor && actorColors.has(span.actor)) {
      return actorColors.get(span.actor)!
    }
    return theme.series[5]
  }

  const handleClick = (span: TraceSpan) => {
    const next = selectedId === span.span_id ? null : span
    setSelectedId(next ? span.span_id : null)
    onSelectSpan?.(next)
  }

  const columns = useMemo<TableColumn<FlatSpan>[]>(
    () => [
      {
        id: 'span',
        header: t('traces.columns.span'),
        cell: ({ row }) => (
          <span
            className="truncate text-foreground"
            style={{ paddingLeft: row.original.depth * INDENT_PX }}
          >
            {row.original.span.name}
          </span>
        ),
      },
      {
        id: 'actor',
        header: t('traces.columns.actor'),
        cell: ({ row }) => (
          <span className="font-mono text-xs text-muted-foreground">
            {actorLabel(row.original.span.actor, row.original.span.actor_kind)}
          </span>
        ),
      },
      {
        id: 'kind',
        header: t('traces.columns.kind'),
        cell: ({ row }) => (
          <Badge variant="outline">{humanize(row.original.span.kind)}</Badge>
        ),
      },
      {
        id: 'timing',
        header: t('traces.columns.timing'),
        cell: ({ row }) => (
          <span className="font-mono text-xs tabular-nums text-muted-foreground">
            +{formatDuration(row.original.span.start_ms)} ·{' '}
            {formatDuration(row.original.span.duration_ms)}
          </span>
        ),
      },
      {
        id: 'status',
        header: t('traces.columns.status'),
        cell: ({ row }) => (
          <SpanStatusBadge status={row.original.span.status} />
        ),
      },
    ],
    [t],
  )

  const summary = t('traces.chartSummary', {
    count: trace.spans.length,
    duration: formatDuration(axisMax),
  })

  const multiAgent = actorColors.size > 1

  return (
    <AccessibleChart<FlatSpan>
      title={t('traces.waterfallTitle')}
      summary={summary}
      columns={columns}
      data={flat}
      getRowId={(r) => r.span.span_id}
      empty={
        <EmptyState
          title={t('empty.traceWaterfallChart.title')}
          description={t('empty.traceWaterfallChart.description')}
        />
      }
    >
      {multiAgent && (
        <div
          className="mb-2 flex flex-wrap gap-2"
          aria-label={t('traces.legend')}
        >
          {[...actorColors.entries()].map(([actor, color]) => (
            <span key={actor} className="flex items-center gap-1 text-xs">
              <span
                className="inline-block h-2.5 w-2.5 rounded-sm"
                style={{ backgroundColor: color }}
              />
              <span className="font-mono text-muted-foreground">{actor}</span>
            </span>
          ))}
        </div>
      )}
      <ul
        className="flex flex-col gap-1"
        aria-label={t('traces.waterfallTitle')}
      >
        {flat.map(({ span, depth }) => {
          const leftPct = (span.start_ms / axisMax) * 100
          const widthPct = Math.max((span.duration_ms / axisMax) * 100, 0.8)
          const selected = selectedId === span.span_id
          return (
            <li
              key={span.span_id}
              role="button"
              tabIndex={0}
              aria-pressed={selected}
              className={cn(
                'grid grid-cols-[minmax(0,18rem)_1fr] items-center gap-3 rounded px-1 py-0.5 text-xs transition-colors',
                // A selected row is an `accent` FILL, so its ink has to be the ink that
                // belongs on that fill. It used to keep the canvas inks, which are tuned
                // for the canvas and unreadable on the accent in BOTH themes: measured
                // 2.97:1 for the span name and 1.45:1 for the actor in light, 2.58:1 and
                // 1.17:1 in dark. `accent-foreground` is #1a1206 in both themes, so one
                // value serves both: 6.88:1, and 5.00:1 for the secondary at /80.
                selected
                  ? 'bg-accent text-accent-foreground'
                  : 'hover:bg-muted/60 cursor-pointer',
              )}
              onClick={() => handleClick(span)}
              onKeyDown={(e) => {
                if (e.key === 'Enter' || e.key === ' ') {
                  e.preventDefault()
                  handleClick(span)
                }
              }}
            >
              <div
                className="flex min-w-0 items-center gap-1.5"
                style={{ paddingLeft: depth * INDENT_PX }}
              >
                <span
                  className="inline-block h-2 w-2 shrink-0 rounded-sm"
                  style={{ backgroundColor: colorForSpan(span) }}
                />
                <span
                  className={cn(
                    'truncate font-medium',
                    selected ? 'text-inherit' : 'text-foreground',
                  )}
                >
                  {span.name}
                </span>
                {span.actor && (
                  <span
                    className={cn(
                      'shrink-0 font-mono text-[0.7rem]',
                      selected
                        ? 'text-accent-foreground/80'
                        : 'text-muted-foreground',
                    )}
                  >
                    {actorLabel(span.actor, span.actor_kind)}
                  </span>
                )}
              </div>
              <div className="relative h-5 w-full rounded-sm bg-muted">
                <div
                  className="absolute top-1/2 h-3 -translate-y-1/2 rounded-sm"
                  style={{
                    left: `${leftPct}%`,
                    width: `${widthPct}%`,
                    backgroundColor: colorForSpan(span),
                  }}
                  title={`${span.name} · +${formatDuration(span.start_ms)} · ${formatDuration(span.duration_ms)}`}
                />
                <span className="absolute top-1/2 right-1 -translate-y-1/2 font-mono text-[0.7rem] tabular-nums text-muted-foreground">
                  {formatDuration(span.duration_ms)}
                </span>
              </div>
            </li>
          )
        })}
      </ul>
    </AccessibleChart>
  )
}

// --- span detail panel ------------------------------------------------------

export function SpanDetailPanel({
  span,
  onClose,
}: {
  span: TraceSpan
  onClose: () => void
}) {
  const { t } = useTranslation('observability')
  return (
    <div
      className="rounded-md border bg-surface p-4 text-sm"
      role="region"
      aria-label={t('traces.detail.title')}
    >
      <div className="mb-3 flex items-center justify-between">
        <h4 className="font-medium text-foreground">
          {t('traces.detail.title')}
        </h4>
        <button
          onClick={onClose}
          className="text-xs text-muted-foreground hover:text-foreground"
          aria-label={t('common:actions.close')}
        >
          ✕
        </button>
      </div>
      <dl className="grid grid-cols-[auto_1fr] gap-x-4 gap-y-1.5">
        <dt className="text-muted-foreground">{t('traces.detail.spanId')}</dt>
        <dd className="font-mono text-xs">{span.span_id}</dd>
        <dt className="text-muted-foreground">{t('traces.detail.name')}</dt>
        <dd>{span.name}</dd>
        <dt className="text-muted-foreground">{t('traces.detail.timing')}</dt>
        <dd className="font-mono tabular-nums">
          +{formatDuration(span.start_ms)} · {formatDuration(span.duration_ms)}
        </dd>
        {span.actor && (
          <>
            <dt className="text-muted-foreground">
              {t('traces.detail.actor')}
            </dt>
            <dd className="font-mono text-xs">{span.actor}</dd>
          </>
        )}
        {span.actor_kind && (
          <>
            <dt className="text-muted-foreground">
              {t('traces.detail.actorKind')}
            </dt>
            <dd>
              <Badge variant="outline">{humanize(span.actor_kind)}</Badge>
            </dd>
          </>
        )}
        {span.entity_ref && (
          <>
            <dt className="text-muted-foreground">
              {t('traces.detail.entityRef')}
            </dt>
            <dd className="font-mono text-xs">{span.entity_ref}</dd>
          </>
        )}
        <dt className="text-muted-foreground">{t('traces.detail.kind')}</dt>
        <dd>
          <Badge variant="outline">{humanize(span.kind)}</Badge>
        </dd>
        <dt className="text-muted-foreground">{t('traces.detail.status')}</dt>
        <dd>
          <SpanStatusBadge status={span.status} />
        </dd>
      </dl>
      {span.attributes && Object.keys(span.attributes).length > 0 && (
        <div className="mt-3">
          <h5 className="mb-1 text-xs font-medium text-muted-foreground">
            {t('traces.detail.attributes')}
          </h5>
          <dl className="grid grid-cols-[auto_1fr] gap-x-3 gap-y-1">
            {Object.entries(span.attributes).map(([k, v]) => (
              <Fragment key={k}>
                <dt className="font-mono text-xs text-muted-foreground">{k}</dt>
                <dd className="break-all font-mono text-xs">{v}</dd>
              </Fragment>
            ))}
          </dl>
        </div>
      )}
    </div>
  )
}
