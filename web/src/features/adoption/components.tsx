// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Adoption presentational pieces — PURE: they take the module's data as props and render
// it (headline stats, model-mix, acceptance, trend, team/developer tables). No fetching,
// no auth — trivially testable with fixtures and reused by the container. The two lenses
// (analytics / telemetry) are rendered separately and never summed.
import { useMemo } from 'react'
import {
  Clock,
  GitCommitHorizontal,
  GitPullRequest,
  Hash,
  ThumbsUp,
  Users,
} from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { DataTable, type TableColumn } from '@/components/data/data-table'
import { Badge } from '@/components/ui/badge'
import { EmptyState } from '@/components/ui/empty-state'
import {
  ChartLegend,
  DonutChart,
  TrendChart,
  useChartTheme,
} from '@/components/charts'
import { CaveatNotice, MetricStat, StatGrid } from '@/features/_intel'
import {
  formatDayKey,
  formatDuration,
  formatFraction,
  formatInt,
  formatPercent,
  formatTokens,
} from '@/lib/format'
import type {
  AdoptionTotals,
  DeveloperRow,
  DiscrepancyDirection,
  DiscrepancyResponse,
  Lens,
  ModelMix as ModelMixT,
  TeamRow,
  ToolBreakdown,
  TrendResponse,
} from './types'

// acceptancePercent maps a 0..1 acceptance fraction (or null) to the percentage NUMBER
// formatPercent expects, or null so it renders the honest dash for "no decisions".
function acceptancePercent(rate: number | null): number | null {
  return rate == null ? null : rate * 100
}

// --- boundary banner ---------------------------------------------------------

// BoundaryBanner is the non-optional Claude-API-only honesty note: the adoption read-model
// covers only the Claude API plane, never a 3P-provider (Bedrock/Vertex/Foundry) estate.
export function BoundaryBanner({ excludes }: { excludes: string[] }) {
  const { t } = useTranslation('adoption')
  const names = excludes.map((e) =>
    t(`boundary.providers.${e}`, { defaultValue: e }),
  )
  return (
    <CaveatNotice tone="neutral">
      {t('boundary.note', { providers: names.join(', ') })}
    </CaveatNotice>
  )
}

// --- headline stats ----------------------------------------------------------

// LensStats renders one lens's productivity headline. activeTime is shown only when the
// lens reports it (the admin Analytics feed carries no active time).
export function LensStats({ totals }: { totals: AdoptionTotals }) {
  const { t } = useTranslation('adoption')
  return (
    <StatGrid>
      <MetricStat
        icon={<Users />}
        label={t('stats.sessions')}
        value={formatInt(totals.sessions)}
      />
      <MetricStat
        icon={<Hash />}
        label={t('stats.linesNet')}
        value={formatInt(totals.lines_net)}
        caption={t('stats.addedRemoved', {
          added: formatInt(totals.lines_added),
          removed: formatInt(totals.lines_removed),
        })}
      />
      <MetricStat
        icon={<GitCommitHorizontal />}
        label={t('stats.commits')}
        value={formatInt(totals.commits)}
      />
      <MetricStat
        icon={<GitPullRequest />}
        label={t('stats.pullRequests')}
        value={formatInt(totals.pull_requests)}
      />
      <MetricStat
        icon={<ThumbsUp />}
        label={t('stats.acceptance')}
        value={formatPercent(acceptancePercent(totals.acceptance_rate))}
        caption={t('stats.acceptedOfTotal', {
          accepted: formatInt(totals.tools_accepted),
          total: formatInt(totals.tools_accepted + totals.tools_rejected),
        })}
      />
      <MetricStat
        icon={<Hash />}
        label={t('stats.tokens')}
        value={formatTokens(totals.tokens)}
      />
      {totals.active_time_ms > 0 ? (
        <MetricStat
          icon={<Clock />}
          label={t('stats.activeTime')}
          value={formatDuration(totals.active_time_ms)}
        />
      ) : null}
    </StatGrid>
  )
}

// --- model mix ---------------------------------------------------------------

export function ModelMix({ byModel }: { byModel: ModelMixT[] }) {
  const { t } = useTranslation('adoption')
  // Resolve a stable series color per slice so the donut and its legend line up
  // (the legend requires an explicit color; the donut falls back to the same series).
  const theme = useChartTheme()
  const data = byModel.map((m, i) => ({
    key: m.model,
    label: m.model,
    value: m.tokens,
    color: theme.series[i % theme.series.length],
  }))
  const total = byModel.reduce((s, m) => s + m.tokens, 0)
  if (data.length === 0) {
    return <p className="text-sm text-muted-foreground">{t('models.empty')}</p>
  }
  return (
    <div className="flex flex-col gap-3 sm:flex-row sm:items-center">
      <DonutChart
        data={data}
        valueFormatter={(v) => formatTokens(Number(v))}
        centerLabel={t('models.total')}
        centerValue={formatTokens(total)}
        className="sm:w-1/2"
      />
      <ChartLegend
        items={data}
        valueFormatter={(v) => formatTokens(Number(v))}
        className="sm:w-1/2"
      />
    </div>
  )
}

// --- acceptance breakdown ----------------------------------------------------

export function AcceptanceBreakdown({ byTool }: { byTool: ToolBreakdown[] }) {
  const { t } = useTranslation('adoption')
  const columns = useMemo<TableColumn<ToolBreakdown>[]>(
    () => [
      { accessorKey: 'tool', header: t('acceptance.columns.tool') },
      {
        accessorKey: 'accepted',
        header: t('acceptance.columns.accepted'),
        cell: ({ row }) => (
          <span className="font-mono tabular-nums">
            {formatInt(row.original.accepted)}
          </span>
        ),
      },
      {
        accessorKey: 'rejected',
        header: t('acceptance.columns.rejected'),
        cell: ({ row }) => (
          <span className="font-mono tabular-nums">
            {formatInt(row.original.rejected)}
          </span>
        ),
      },
      {
        id: 'rate',
        header: t('acceptance.columns.rate'),
        cell: ({ row }) => (
          <span className="font-mono tabular-nums">
            {formatPercent(acceptancePercent(row.original.acceptance_rate))}
          </span>
        ),
      },
    ],
    [t],
  )
  if (byTool.length === 0) {
    return (
      <p className="text-sm text-muted-foreground">{t('acceptance.empty')}</p>
    )
  }
  return (
    <DataTable<ToolBreakdown>
      columns={columns}
      data={byTool}
      empty={
        <EmptyState
          title={t('empty.acceptance.title')}
          description={t('empty.acceptance.description')}
        />
      }
    />
  )
}

// --- trend -------------------------------------------------------------------

export function AdoptionTrend({ trend }: { trend: TrendResponse }) {
  const { t, i18n } = useTranslation('adoption')
  const data = trend.days.map((d) => ({
    day: d.day,
    commits: d.totals.commits,
    lines_net: d.totals.lines_net,
    sessions: d.totals.sessions,
  }))
  return (
    <TrendChart
      data={data}
      xKey="day"
      height={280}
      series={[
        { key: 'lines_net', label: t('stats.linesNet'), kind: 'area' },
        { key: 'commits', label: t('stats.commits'), kind: 'line' },
        { key: 'sessions', label: t('stats.sessions'), kind: 'line' },
      ]}
      valueFormatter={(v) => formatInt(Number(v))}
      xTickFormatter={(v) => formatDayKey(v, i18n.language)}
      emptyLabel={t('trend.empty')}
    />
  )
}

// --- team / developer tables -------------------------------------------------

// productivityColumns builds the shared productivity columns a team/developer row carries.
function productivityColumns<T extends { totals: AdoptionTotals }>(
  t: (k: string) => string,
): TableColumn<T>[] {
  return [
    {
      accessorKey: 'sessions',
      header: t('columns.sessions'),
      cell: ({ row }) => num(row.original.totals.sessions),
    },
    {
      accessorKey: 'commits',
      header: t('columns.commits'),
      cell: ({ row }) => num(row.original.totals.commits),
    },
    {
      accessorKey: 'pull_requests',
      header: t('columns.pullRequests'),
      cell: ({ row }) => num(row.original.totals.pull_requests),
    },
    {
      accessorKey: 'lines_net',
      header: t('columns.linesNet'),
      cell: ({ row }) => num(row.original.totals.lines_net),
    },
    {
      id: 'acceptance',
      header: t('columns.acceptance'),
      cell: ({ row }) => (
        <span className="font-mono tabular-nums">
          {formatPercent(
            acceptancePercent(row.original.totals.acceptance_rate),
          )}
        </span>
      ),
    },
  ]
}

function num(v: number) {
  return <span className="font-mono tabular-nums">{formatInt(v)}</span>
}

// --- official-vs-observed comparison ----------------------------------------

const COMPARISON_METRICS = [
  'claude_code.session.count',
  'claude_code.token.usage',
  'claude_code.lines_of_code.count',
  'claude_code.commit.count',
  'claude_code.pull_request.count',
] as const

type ComparisonMetricName = (typeof COMPARISON_METRICS)[number]

interface ComparisonRow {
  name: ComparisonMetricName
  analytics: number
  telemetry: number
  material: boolean
  direction: DiscrepancyDirection
  ratio: number
}

function comparisonRows(discrepancy: DiscrepancyResponse): ComparisonRow[] {
  const rows = new Map<ComparisonMetricName, ComparisonRow>()
  for (const name of COMPARISON_METRICS) {
    rows.set(name, {
      name,
      analytics: 0,
      telemetry: 0,
      material: false,
      direction: 'aligned',
      ratio: 0,
    })
  }
  for (const day of discrepancy.days) {
    for (const metric of day.metrics) {
      if (!COMPARISON_METRICS.includes(metric.name as ComparisonMetricName))
        continue
      const row = rows.get(metric.name as ComparisonMetricName)
      if (!row) continue
      row.analytics += metric.analytics
      row.telemetry += metric.telemetry
      if (metric.material && (!row.material || metric.ratio > row.ratio)) {
        row.material = true
        row.direction = metric.direction
        row.ratio = metric.ratio
      }
    }
  }
  return COMPARISON_METRICS.map((name) => rows.get(name)!)
}

function metricValue(name: string, value: number, locale: string): string {
  return name === 'claude_code.token.usage'
    ? formatTokens(value, locale)
    : formatInt(value, locale)
}

function metricLabelKey(name: string): string {
  switch (name) {
    case 'claude_code.session.count':
      return 'comparison.metrics.sessions'
    case 'claude_code.token.usage':
      return 'comparison.metrics.tokens'
    case 'claude_code.lines_of_code.count':
      return 'comparison.metrics.linesAdded'
    case 'claude_code.commit.count':
      return 'comparison.metrics.commits'
    case 'claude_code.pull_request.count':
      return 'comparison.metrics.pullRequests'
    default:
      return name
  }
}

export function OfficialObservedComparison({
  discrepancy,
}: {
  discrepancy: DiscrepancyResponse
}) {
  const { t, i18n } = useTranslation('adoption')
  const rows = useMemo(() => comparisonRows(discrepancy), [discrepancy])
  const providerNames = discrepancy.boundary.excludes.map((e) =>
    t(`boundary.providers.${e}`, { defaultValue: e }),
  )
  if (discrepancy.days.length === 0) {
    return (
      <p className="text-sm text-muted-foreground">{t('comparison.empty')}</p>
    )
  }
  return (
    <div className="flex flex-col gap-3">
      <div className="grid gap-2">
        {rows.map((row) => (
          <div
            key={row.name}
            className="grid gap-3 rounded-sm border border-border bg-muted/20 p-3 md:grid-cols-[minmax(10rem,1.2fr)_minmax(8rem,0.7fr)_minmax(8rem,0.7fr)_minmax(13rem,1fr)] md:items-center"
          >
            <div className="min-w-0">
              <p className="text-sm font-medium text-foreground">
                {t(metricLabelKey(row.name))}
              </p>
              <p className="font-mono text-xs text-muted-foreground">
                {row.name}
              </p>
            </div>
            <div>
              <p className="text-xs text-muted-foreground">
                {t('comparison.official')}
              </p>
              <p className="font-mono text-sm tabular-nums">
                {metricValue(row.name, row.analytics, i18n.language)}
              </p>
            </div>
            <div>
              <p className="text-xs text-muted-foreground">
                {t('comparison.observed')}
              </p>
              <p className="font-mono text-sm tabular-nums">
                {metricValue(row.name, row.telemetry, i18n.language)}
              </p>
            </div>
            <div className="flex flex-wrap items-center gap-2">
              {row.material ? (
                <Badge variant="warning">
                  {t(`comparison.directions.${row.direction}`, {
                    ratio: formatFraction(row.ratio, {
                      digits: 0,
                      locale: i18n.language,
                    }),
                  })}
                </Badge>
              ) : (
                <Badge variant="neutral">
                  {t('comparison.withinThreshold')}
                </Badge>
              )}
            </div>
          </div>
        ))}
      </div>
      <CaveatNotice tone="neutral">
        {t('comparison.boundaryNote', { providers: providerNames.join(', ') })}
      </CaveatNotice>
    </div>
  )
}

export function TeamTable({ teams }: { teams: TeamRow[] }) {
  const { t } = useTranslation('adoption')
  const columns = useMemo<TableColumn<TeamRow>[]>(
    () => [
      {
        accessorKey: 'team',
        header: t('teams.columns.team'),
        cell: ({ row }) =>
          row.original.team === '' ? (
            <span className="italic text-muted-foreground">
              {t('teams.unassigned')}
            </span>
          ) : (
            <span className="font-medium">{row.original.team}</span>
          ),
      },
      ...productivityColumns<TeamRow>(t),
    ],
    [t],
  )
  return (
    <DataTable<TeamRow>
      columns={columns}
      data={teams}
      empty={
        <EmptyState
          title={t('empty.team.title')}
          description={t('empty.team.description')}
        />
      }
    />
  )
}

export function DeveloperTable({ developers }: { developers: DeveloperRow[] }) {
  const { t } = useTranslation('adoption')
  const columns = useMemo<TableColumn<DeveloperRow>[]>(
    () => [
      {
        accessorKey: 'developer',
        header: t('developers.columns.developer'),
        cell: ({ row }) => (
          <span className="font-mono text-xs">{row.original.developer}</span>
        ),
      },
      ...productivityColumns<DeveloperRow>(t),
    ],
    [t],
  )
  return (
    <DataTable<DeveloperRow>
      columns={columns}
      data={developers}
      empty={
        <EmptyState
          title={t('empty.developer.title')}
          description={t('empty.developer.description')}
        />
      }
    />
  )
}

// --- one-lens section --------------------------------------------------------

export function LensSection({ lens }: { lens: Lens }) {
  return (
    <div className="flex flex-col gap-4">
      <LensStats totals={lens.totals} />
      <ModelMix byModel={lens.by_model} />
      <AcceptanceBreakdown byTool={lens.by_tool} />
    </div>
  )
}
