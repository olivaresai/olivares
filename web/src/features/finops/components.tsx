// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// FinOps presentational pieces — PURE: they take the module's data as props and
// render it (charts, breakdown, budget cards, alerts, recommendations). No fetching,
// no auth — so they are trivially testable with fixtures and reused by the container.
import { useMemo, useState, type ReactNode } from 'react'
import {
  Coins,
  Hash,
  Receipt,
  Scale,
  TrendingUp,
  Users,
  Zap,
} from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { AccessibleChart } from '@/components/data/accessible-chart'
import { DataTable, type TableColumn } from '@/components/data/data-table'
import { Badge } from '@/components/ui/badge'
import { EmptyState } from '@/components/ui/empty-state'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import {
  CategoryBarChart,
  ChartLegend,
  DonutChart,
  TrendChart,
  useChartTheme,
} from '@/components/charts'
import {
  CaveatNotice,
  ConsumptionBar,
  DisclaimerNote,
  MetricStat,
  SeamBadge,
  SectionCard,
  SeverityBadge,
  StatGrid,
  TruncatedNotice,
} from '@/features/_intel'
import {
  formatDate,
  formatDateTime,
  formatDayKey,
  formatInt,
  formatMicroUsd,
  formatPercent,
  formatScore,
  formatTokens,
  humanize,
} from '@/lib/format'
import {
  FUTURE_BREAKDOWNS,
  FUTURE_DIMENSIONS,
  FUTURE_DIMS_AS_OF,
  type FutureBreakdown,
  type FutureDimension,
} from './schema'
import type {
  Alert,
  AllocationAgent,
  AllocationResponse,
  BudgetStatus,
  CacheSummary,
  ForecastAnomaly,
  ForecastResponse,
  Recommendation,
  ReconciliationDay,
  ReconciliationResponse,
  SpendBucket,
  SpendDimension,
  SpendResponse,
  SummaryResponse,
  TrendResponse,
} from './types'

// --- headline stats ----------------------------------------------------------

export function SpendStats({
  summary,
  forecast,
}: {
  summary: SummaryResponse
  forecast?: ForecastResponse
}) {
  const { t } = useTranslation(['finops', 'intel'])
  // Headline uses the trailing-window run-rate (trend_projected) so it matches the
  // detailed ForecastCard — never two different projections for the same period.
  const projectedOver =
    forecast && forecast.trend_projected_micro_usd > forecast.spend_micro_usd
  return (
    <StatGrid>
      <MetricStat
        icon={<Coins />}
        label={t('stats.totalSpend')}
        value={formatMicroUsd(summary.total_micro_usd, { compact: true })}
        caption={t('stats.inThisRange')}
      />
      <MetricStat
        icon={<Hash />}
        label={t('stats.tokens')}
        value={formatTokens(summary.input_tokens + summary.output_tokens)}
        caption={t('stats.inOut', {
          in: formatTokens(summary.input_tokens),
          out: formatTokens(summary.output_tokens),
        })}
      />
      <MetricStat
        icon={<Receipt />}
        label={t('stats.samples')}
        value={formatInt(summary.samples)}
      />
      {forecast ? (
        <MetricStat
          icon={<TrendingUp />}
          label={t('stats.forecast')}
          value={formatMicroUsd(forecast.trend_projected_micro_usd, {
            compact: true,
          })}
          caption={t('stats.atRunRate')}
          tone={projectedOver ? 'warning' : 'default'}
        />
      ) : null}
    </StatGrid>
  )
}

// --- cost trend --------------------------------------------------------------

export function CostTrend({ trend }: { trend: TrendResponse }) {
  const { t, i18n } = useTranslation('finops')
  const data = trend.days.map((d) => ({
    key: d.key,
    cost: d.cost_micro_usd,
  }))
  return (
    <SectionCard title={t('trend.title')} description={t('trend.description')}>
      {trend.truncated ? <TruncatedNotice className="mb-3" /> : null}
      <TrendChart
        data={data}
        xKey="key"
        series={[{ key: 'cost', label: t('trend.cost') }]}
        valueFormatter={(v) => formatMicroUsd(v, { compact: true })}
        xTickFormatter={(k) => formatDayKey(k, i18n.language)}
        height={260}
      />
    </SectionCard>
  )
}

// --- breakdown ---------------------------------------------------------------

type BreakdownDim = 'model' | 'provider' | 'agent'

function bucketColumns(
  t: ReturnType<typeof useTranslation>['t'],
  lang: string,
): TableColumn<SpendBucket>[] {
  return [
    {
      accessorKey: 'key',
      header: t('breakdown.columns.key'),
      cell: ({ row }) => (
        <span className="font-mono text-xs">{row.original.key || '—'}</span>
      ),
    },
    {
      accessorKey: 'cost_micro_usd',
      header: t('breakdown.columns.cost'),
      cell: ({ row }) => (
        <span className="font-mono tabular-nums">
          {formatMicroUsd(row.original.cost_micro_usd)}
        </span>
      ),
    },
    {
      accessorKey: 'input_tokens',
      header: t('breakdown.columns.inputTokens'),
      cell: ({ row }) => (
        <span className="font-mono tabular-nums text-muted-foreground">
          {formatTokens(row.original.input_tokens, lang)}
        </span>
      ),
    },
    {
      accessorKey: 'output_tokens',
      header: t('breakdown.columns.outputTokens'),
      cell: ({ row }) => (
        <span className="font-mono tabular-nums text-muted-foreground">
          {formatTokens(row.original.output_tokens, lang)}
        </span>
      ),
    },
    {
      accessorKey: 'samples',
      header: t('breakdown.columns.samples'),
      cell: ({ row }) => (
        <span className="font-mono tabular-nums text-muted-foreground">
          {formatInt(row.original.samples, lang)}
        </span>
      ),
    },
  ]
}

export function SpendBreakdown({ summary }: { summary: SummaryResponse }) {
  const { t, i18n } = useTranslation('finops')
  const theme = useChartTheme()
  const [dim, setDim] = useState<BreakdownDim>('model')

  const buckets =
    dim === 'model'
      ? summary.by_model
      : dim === 'provider'
        ? summary.by_provider
        : summary.by_agent

  const top = buckets.slice(0, 8)
  const colored = top.map((b, i) => ({
    ...b,
    color: theme.series[i % theme.series.length],
  }))
  const columns = useMemo(
    () => bucketColumns(t, i18n.language),
    [t, i18n.language],
  )

  const dimLabel =
    dim === 'model'
      ? t('breakdown.byModel')
      : dim === 'provider'
        ? t('breakdown.byProvider')
        : t('breakdown.byAgent')

  return (
    <SectionCard
      title={t('breakdown.title')}
      description={t('breakdown.description')}
      actions={
        <Select value={dim} onValueChange={(v) => setDim(v as BreakdownDim)}>
          <SelectTrigger className="w-36" aria-label={dimLabel}>
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="model">{t('breakdown.byModel')}</SelectItem>
            <SelectItem value="provider">
              {t('breakdown.byProvider')}
            </SelectItem>
            <SelectItem value="agent">{t('breakdown.byAgent')}</SelectItem>
          </SelectContent>
        </Select>
      }
    >
      {dim === 'agent' && summary.by_agent.length === 0 ? (
        <p className="mb-3 text-xs text-muted-foreground">
          {t('breakdown.noAgentAttribution')}
        </p>
      ) : null}
      <div className="grid gap-4 lg:grid-cols-[1.4fr_1fr]">
        <CategoryBarChart
          data={colored}
          categoryKey="key"
          valueKey="cost_micro_usd"
          valueFormatter={(v) => formatMicroUsd(v, { compact: true })}
          height={Math.max(160, top.length * 30 + 24)}
        />
        <div className="flex flex-col items-center gap-3">
          <DonutChart
            data={colored.map((b) => ({
              key: b.key,
              label: b.key || '—',
              value: b.cost_micro_usd,
              color: b.color,
            }))}
            valueFormatter={(v) => formatMicroUsd(v, { compact: true })}
            centerValue={formatPercent(100)}
            centerLabel={t('breakdown.share')}
            height={180}
          />
          <ChartLegend
            className="w-full"
            items={colored.map((b) => ({
              key: b.key,
              label: b.key || '—',
              value: b.cost_micro_usd,
              color: b.color,
            }))}
            valueFormatter={(v) => formatMicroUsd(v, { compact: true })}
          />
        </div>
      </div>
      <div className="mt-4">
        <DataTable<SpendBucket>
          columns={columns}
          data={buckets}
          getRowId={(r) => r.key}
          empty={
            <EmptyState
              title={t('empty.spend.title')}
              description={t('empty.spend.description')}
            />
          }
        />
      </div>
    </SectionCard>
  )
}

// --- forecast ----------------------------------------------------------------

export function ForecastCard({ forecast }: { forecast: ForecastResponse }) {
  // `periods` is NOT a namespace — the labels live at `finops:periods.*`, which the
  // first entry already resolves. Listing it here named a bundle nobody registers.
  const { t } = useTranslation('finops')
  // The trend projection is the trailing-window run-rate; show its confidence band as
  // a range (low … high). It is a projection AT THE CURRENT RUN-RATE, not a model.
  const hasBand =
    forecast.confidence_high_micro_usd > forecast.confidence_low_micro_usd
  return (
    <SectionCard
      title={t('forecast.title')}
      description={t('forecast.subtitle')}
    >
      {forecast.truncated ? <TruncatedNotice className="mb-3" /> : null}
      <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-4">
        <Stat label={t('forecast.spentSoFar')}>
          {formatMicroUsd(forecast.spend_micro_usd)}
        </Stat>
        <Stat label={t('forecast.trendProjected')}>
          {formatMicroUsd(forecast.trend_projected_micro_usd)}
        </Stat>
        <Stat label={t('forecast.dailyRunRate')}>
          {formatMicroUsd(forecast.daily_run_rate_micro_usd)}
        </Stat>
        <Stat label={t('forecast.period')}>
          <span className="capitalize">
            {t(`periods.${forecast.period}`, { defaultValue: forecast.period })}
          </span>
        </Stat>
      </div>
      {hasBand ? (
        <p className="mt-3 text-xs text-muted-foreground">
          {t('forecast.confidenceBand', {
            low: formatMicroUsd(forecast.confidence_low_micro_usd),
            high: formatMicroUsd(forecast.confidence_high_micro_usd),
            window: forecast.window_days,
          })}
        </p>
      ) : null}
      <DisclaimerNote className="mt-3" text={t('forecast.runRateNote')} />
      {forecast.anomalies && forecast.anomalies.length > 0 ? (
        <div className="mt-4">
          <p className="mb-2 text-xs font-medium tracking-wide text-muted-foreground uppercase">
            {t('forecast.anomaliesTitle')}
          </p>
          <AnomaliesTable anomalies={forecast.anomalies} />
        </div>
      ) : null}
    </SectionCard>
  )
}

// --- spend anomalies (measured outliers, not predictions) --------------------

export function AnomaliesTable({
  anomalies,
}: {
  anomalies: ForecastAnomaly[]
}) {
  const { t, i18n } = useTranslation('finops')
  const columns = useMemo<TableColumn<ForecastAnomaly>[]>(
    () => [
      {
        accessorKey: 'day',
        header: t('forecast.anomalyColumns.day'),
        cell: ({ row }) => (
          <span className="text-xs text-muted-foreground">
            {formatDate(`${row.original.day}T00:00:00Z`, i18n.language)}
          </span>
        ),
      },
      {
        accessorKey: 'spend_micro_usd',
        header: t('forecast.anomalyColumns.spend'),
        cell: ({ row }) => (
          <span className="font-mono tabular-nums">
            {formatMicroUsd(row.original.spend_micro_usd)}
          </span>
        ),
      },
      {
        accessorKey: 'baseline_micro_usd',
        header: t('forecast.anomalyColumns.baseline'),
        cell: ({ row }) => (
          <span className="font-mono tabular-nums text-muted-foreground">
            {formatMicroUsd(row.original.baseline_micro_usd)}
          </span>
        ),
      },
      {
        accessorKey: 'deviation_sigma',
        header: t('forecast.anomalyColumns.deviation'),
        cell: ({ row }) => (
          <Badge variant="warning">
            {t('forecast.sigma', {
              value: formatScore(row.original.deviation_sigma),
            })}
          </Badge>
        ),
      },
    ],
    [t, i18n.language],
  )
  return (
    <DataTable<ForecastAnomaly>
      columns={columns}
      data={anomalies}
      getRowId={(r) => r.day}
      empty={
        <EmptyState
          title={t('empty.anomalies.title')}
          description={t('empty.anomalies.description')}
        />
      }
    />
  )
}

function Stat({
  label,
  children,
}: {
  label: string
  children: React.ReactNode
}) {
  return (
    <div className="flex flex-col gap-0.5">
      <span className="text-xs font-medium tracking-wide text-muted-foreground uppercase">
        {label}
      </span>
      <span className="font-display text-lg font-semibold tabular-nums text-foreground">
        {children}
      </span>
    </div>
  )
}

// --- budget card -------------------------------------------------------------

export function BudgetCard({
  status,
  actions,
}: {
  status: BudgetStatus
  /** Optional right-aligned action slot (edit/delete) rendered by the container
   * when the principal can write budgets. */
  actions?: ReactNode
}) {
  const { t } = useTranslation('finops')
  const atRisk = status.over || status.projected_pct >= 100
  return (
    <SectionCard className={atRisk ? 'border-warning-line' : undefined}>
      <div className="flex flex-col gap-3">
        <div className="flex items-start justify-between gap-2">
          <div className="min-w-0">
            <div className="flex items-center gap-2">
              <span className="truncate text-sm font-medium text-foreground">
                {status.name}
              </span>
              {!status.enabled ? (
                <Badge variant="neutral">{t('budgets.disabled')}</Badge>
              ) : status.over ? (
                <Badge variant="danger">{t('budgets.over')}</Badge>
              ) : status.projected_pct >= 100 ? (
                <Badge variant="warning">{t('budgets.onTrackToExceed')}</Badge>
              ) : null}
            </div>
            <p className="flex flex-wrap items-center gap-x-1.5 text-xs text-muted-foreground">
              <span className="font-mono">
                {status.dimension === 'global'
                  ? t('budgets.global')
                  : `${status.dimension}: ${status.key}`}
              </span>
              {' · '}
              {t(`periods.${status.period}`, { defaultValue: status.period })}
              {status.action && status.action !== 'alert' ? (
                <Badge variant="outline">
                  {t(`budgets.actions.${status.action}`, {
                    defaultValue: status.action,
                  })}
                </Badge>
              ) : null}
            </p>
          </div>
          <div className="flex items-start gap-2">
            <div className="text-right">
              <div className="font-mono text-sm tabular-nums text-foreground">
                {formatMicroUsd(status.spend_micro_usd)}
              </div>
              <div className="text-xs text-muted-foreground">
                / {formatMicroUsd(status.limit_micro_usd)}
              </div>
            </div>
            {actions ? (
              <div className="flex shrink-0 items-center gap-1">{actions}</div>
            ) : null}
          </div>
        </div>
        <ConsumptionBar
          consumedPct={status.consumed_pct}
          projectedPct={status.projected_pct}
          over={status.over}
        />
        {status.reserved_micro_usd && status.reserved_micro_usd > 0 ? (
          <p className="text-xs text-muted-foreground">
            {t('budgets.reservedHint', {
              amount: formatMicroUsd(status.reserved_micro_usd),
            })}
          </p>
        ) : null}
        <div className="flex items-center justify-between text-xs text-muted-foreground">
          <span>
            {t('budgets.remaining')}:{' '}
            <span className="font-mono text-foreground">
              {formatMicroUsd(status.remaining_micro_usd)}
            </span>
          </span>
          {status.truncated ? (
            <Badge variant="warning">{t('intel:notices.truncated')}</Badge>
          ) : null}
        </div>
      </div>
    </SectionCard>
  )
}

// --- alerts ------------------------------------------------------------------

export function AlertsTable({ alerts }: { alerts: Alert[] }) {
  const { t, i18n } = useTranslation('finops')
  const columns = useMemo<TableColumn<Alert>[]>(
    () => [
      {
        accessorKey: 'triggered_at',
        header: t('alerts.columns.triggeredAt'),
        cell: ({ row }) => (
          <span className="text-xs text-muted-foreground">
            {formatDateTime(row.original.triggered_at, i18n.language)}
          </span>
        ),
      },
      {
        accessorKey: 'key',
        header: t('alerts.columns.budget'),
        cell: ({ row }) => (
          <span className="font-mono text-xs">
            {row.original.dimension === 'global'
              ? t('budgets.global')
              : `${row.original.dimension}: ${row.original.key}`}
          </span>
        ),
      },
      {
        accessorKey: 'threshold_pct',
        header: t('alerts.columns.threshold'),
        cell: ({ row }) => (
          <span className="font-mono tabular-nums">
            {formatPercent(row.original.threshold_pct)}
          </span>
        ),
      },
      {
        id: 'spend',
        header: t('alerts.columns.spend'),
        cell: ({ row }) => (
          <span className="font-mono text-xs tabular-nums text-muted-foreground">
            {formatMicroUsd(row.original.spend_micro_usd)} /{' '}
            {formatMicroUsd(row.original.limit_micro_usd)}
          </span>
        ),
      },
      {
        accessorKey: 'severity',
        header: t('alerts.columns.severity'),
        cell: ({ row }) => <SeverityBadge severity={row.original.severity} />,
      },
    ],
    [t, i18n.language],
  )
  return (
    <DataTable<Alert>
      columns={columns}
      data={alerts}
      empty={
        <EmptyState
          title={t('empty.alerts.title')}
          description={t('empty.alerts.description')}
        />
      }
    />
  )
}

// --- recommendations ---------------------------------------------------------

export function RecommendationCard({ rec }: { rec: Recommendation }) {
  const { t } = useTranslation('finops')
  return (
    <div className="flex flex-col gap-2 rounded-lg border border-border bg-surface p-4 shadow-sm">
      <div className="flex items-start justify-between gap-3">
        <div className="flex flex-col gap-1">
          <div className="flex items-center gap-2">
            <Badge variant="outline">
              {t(`optimization.kind.${rec.kind}`, { defaultValue: rec.kind })}
            </Badge>
            <SeverityBadge severity={rec.severity} />
          </div>
          <p className="text-sm font-medium text-foreground">{rec.title}</p>
        </div>
        {rec.estimated_savings_micro_usd !== undefined ? (
          <div className="shrink-0 text-right">
            <div className="text-xs text-muted-foreground">
              {t('optimization.estimatedSavings')}
            </div>
            <div className="font-display text-base font-semibold tabular-nums text-success">
              {formatMicroUsd(rec.estimated_savings_micro_usd, {
                compact: true,
              })}
            </div>
          </div>
        ) : null}
      </div>
      <p className="text-xs leading-relaxed text-muted-foreground">
        {rec.detail}
      </p>
      {rec.subject ? (
        <p className="text-xs text-muted-foreground">
          {t('optimization.subject')}:{' '}
          <span className="font-mono text-foreground">{rec.subject}</span>
        </p>
      ) : null}
    </div>
  )
}

// --- prompt-cache efficiency -------------------------------------------

/** Cache-read vs creation token split + realized saving. The saving is the share of
 *  the base input price saved by cache reads (priced per-model); 0 is honest when
 *  nothing is cached — no figure is invented (analytics.go:cacheReadSavings). */
export function CacheEfficiencyPanel({ cache }: { cache: CacheSummary }) {
  const { t } = useTranslation('finops')
  const theme = useChartTheme()
  const segments = [
    {
      key: 'uncached',
      label: t('cache.uncached'),
      value: cache.uncached_input_tokens,
      color: theme.series[0]!,
    },
    {
      key: 'read',
      label: t('cache.read'),
      value: cache.cache_read_tokens,
      color: theme.series[1]!,
    },
    {
      key: 'create1h',
      label: t('cache.create1h'),
      value: cache.cache_creation_1h_tokens,
      color: theme.series[2]!,
    },
    {
      key: 'create5m',
      label: t('cache.create5m'),
      value: cache.cache_creation_5m_tokens,
      color: theme.series[3]!,
    },
  ]
  const hasTokens = segments.some((s) => s.value > 0)
  const columns = useMemo<TableColumn<(typeof segments)[number]>[]>(
    () => [
      { accessorKey: 'label', header: t('cache.columns.tier') },
      {
        accessorKey: 'value',
        header: t('cache.columns.tokens'),
        cell: ({ row }) => (
          <span className="font-mono tabular-nums">
            {formatTokens(row.original.value)}
          </span>
        ),
      },
    ],
    [t],
  )
  return (
    <SectionCard title={t('cache.title')} description={t('cache.description')}>
      <div className="grid gap-4 lg:grid-cols-[1fr_1.2fr]">
        <StatGrid>
          <MetricStat
            label={t('cache.hitRate')}
            value={formatPercent(cache.hit_rate_pct)}
            caption={t('cache.hitRateCaption')}
          />
          <MetricStat
            label={t('cache.savings')}
            value={formatMicroUsd(cache.savings_micro_usd, { compact: true })}
            caption={t('cache.savingsCaption')}
            tone={cache.savings_micro_usd > 0 ? 'success' : 'default'}
          />
        </StatGrid>
        <AccessibleChart
          title={t('cache.chartTitle')}
          summary={t('cache.chartSummary', {
            read: formatTokens(cache.cache_read_tokens),
            uncached: formatTokens(cache.uncached_input_tokens),
            rate: formatPercent(cache.hit_rate_pct),
          })}
          columns={columns}
          data={segments}
          getRowId={(r) => r.key}
          empty={
            <EmptyState
              title={t('empty.cacheEfficiencyChart.title')}
              description={t('empty.cacheEfficiencyChart.description')}
            />
          }
        >
          <div className="flex flex-col items-center gap-3">
            <DonutChart
              data={segments.map((s) => ({
                key: s.key,
                label: s.label,
                value: s.value,
                color: s.color,
              }))}
              valueFormatter={(v) => formatTokens(v)}
              centerValue={formatPercent(cache.hit_rate_pct)}
              centerLabel={t('cache.hitRate')}
              height={180}
              emptyLabel={t('cache.empty')}
            />
            {hasTokens ? (
              <ChartLegend
                className="w-full"
                items={segments.map((s) => ({
                  key: s.key,
                  label: s.label,
                  value: s.value,
                  color: s.color,
                }))}
                valueFormatter={(v) => formatTokens(v)}
              />
            ) : null}
          </div>
        </AccessibleChart>
      </div>
      <DisclaimerNote className="mt-3" text={t('cache.note')} />
    </SectionCard>
  )
}

// --- chargeback: free-form dimension breakdown -------------------------------

/** Columns for a free-form spend breakdown — identical shape to bucketColumns but
 *  reused by the dimension chargeback table. */
function spendBucketColumns(
  t: ReturnType<typeof useTranslation>['t'],
  lang: string,
): TableColumn<SpendBucket>[] {
  return [
    {
      accessorKey: 'key',
      header: t('breakdown.columns.key'),
      cell: ({ row }) => (
        <span className="font-mono text-xs">{row.original.key || '—'}</span>
      ),
    },
    {
      accessorKey: 'cost_micro_usd',
      header: t('breakdown.columns.cost'),
      cell: ({ row }) => (
        <span className="font-mono tabular-nums">
          {formatMicroUsd(row.original.cost_micro_usd)}
        </span>
      ),
    },
    {
      accessorKey: 'input_tokens',
      header: t('breakdown.columns.inputTokens'),
      cell: ({ row }) => (
        <span className="font-mono tabular-nums text-muted-foreground">
          {formatTokens(row.original.input_tokens, lang)}
        </span>
      ),
    },
    {
      accessorKey: 'output_tokens',
      header: t('breakdown.columns.outputTokens'),
      cell: ({ row }) => (
        <span className="font-mono tabular-nums text-muted-foreground">
          {formatTokens(row.original.output_tokens, lang)}
        </span>
      ),
    },
    {
      accessorKey: 'samples',
      header: t('breakdown.columns.samples'),
      cell: ({ row }) => (
        <span className="font-mono tabular-nums text-muted-foreground">
          {formatInt(row.original.samples, lang)}
        </span>
      ),
    },
  ]
}

/** Chargeback breakdown for ANY of the 15 queryable dimensions. The dimension picker
 *  is owned by the caller (it drives the live query); this renders the returned
 *  buckets as a ranked chart + table. service_tier keys render verbatim (free-form);
 *  cost_type carries the billed-only caveat (it is ~empty on the estimated stream). */
export function DimensionBreakdown({
  dimension,
  spend,
  picker,
}: {
  dimension: SpendDimension
  spend: SpendResponse
  /** The dimension <Select>, rendered in the card actions slot. */
  picker: React.ReactNode
}) {
  const { t, i18n } = useTranslation('finops')
  const theme = useChartTheme()
  const top = spend.buckets.slice(0, 10)
  const colored = top.map((b, i) => ({
    ...b,
    color: theme.series[i % theme.series.length]!,
  }))
  const columns = useMemo(
    () => spendBucketColumns(t, i18n.language),
    [t, i18n.language],
  )
  const dimLabel = t(`dimensions.${dimension}`, {
    defaultValue: humanize(dimension),
  })
  const isCostType = dimension === 'cost_type'
  const empty = spend.buckets.length === 0
  return (
    <SectionCard
      title={t('chargeback.title')}
      description={t('chargeback.description')}
      actions={picker}
    >
      {spend.truncated ? <TruncatedNotice className="mb-3" /> : null}
      {isCostType ? (
        <CaveatNotice tone="info" className="mb-3">
          {t('chargeback.costTypeBilledOnly')}
        </CaveatNotice>
      ) : null}
      {dimension === 'service_tier' ? (
        <p className="mb-3 text-xs text-muted-foreground">
          {t('chargeback.serviceTierFreeform')}
        </p>
      ) : null}
      {empty ? (
        <p className="text-xs text-muted-foreground">
          {isCostType
            ? t('chargeback.costTypeEmpty')
            : t('chargeback.empty', { dimension: dimLabel })}
        </p>
      ) : (
        <AccessibleChart
          title={t('chargeback.chartTitle', { dimension: dimLabel })}
          summary={t('chargeback.chartSummary', {
            dimension: dimLabel,
            top: colored[0]?.key || '—',
            total: formatMicroUsd(spend.total_micro_usd, { compact: true }),
          })}
          columns={columns}
          data={spend.buckets}
          getRowId={(r) => r.key || '∅'}
          empty={
            <EmptyState
              title={t('empty.dimensionChart.title')}
              description={t('empty.dimensionChart.description')}
            />
          }
        >
          <CategoryBarChart
            data={colored}
            categoryKey="key"
            valueKey="cost_micro_usd"
            valueFormatter={(v) => formatMicroUsd(v, { compact: true })}
            height={Math.max(160, top.length * 30 + 24)}
          />
        </AccessibleChart>
      )}
    </SectionCard>
  )
}

// --- billed-vs-estimated reconciliation --------------------------------------

/** Reconciles the authoritative billed cost against the derived estimate. The two
 *  streams are NEVER summed: the note + estimated_only_tiers are shown PROMINENTLY
 *  (analytics.go:reconcile — Priority is never billed via cost_report). */
export function ReconciliationView({
  reconciliation,
}: {
  reconciliation: ReconciliationResponse
}) {
  const { t, i18n } = useTranslation('finops')
  const r = reconciliation
  const driftPositive = r.drift_micro_usd > 0
  const data = r.days.map((d) => ({
    key: d.day,
    billed: d.billed_micro_usd,
    estimated: d.estimated_micro_usd,
  }))
  const columns = useMemo<TableColumn<ReconciliationDay>[]>(
    () => [
      {
        accessorKey: 'day',
        header: t('reconciliation.columns.day'),
        cell: ({ row }) => (
          <span className="text-xs text-muted-foreground">
            {formatDayKey(row.original.day, i18n.language)}
          </span>
        ),
      },
      {
        accessorKey: 'billed_micro_usd',
        header: t('reconciliation.columns.billed'),
        cell: ({ row }) => (
          <span className="font-mono tabular-nums">
            {formatMicroUsd(row.original.billed_micro_usd)}
          </span>
        ),
      },
      {
        accessorKey: 'estimated_micro_usd',
        header: t('reconciliation.columns.estimated'),
        cell: ({ row }) => (
          <span className="font-mono tabular-nums">
            {formatMicroUsd(row.original.estimated_micro_usd)}
          </span>
        ),
      },
      {
        accessorKey: 'drift_micro_usd',
        header: t('reconciliation.columns.drift'),
        cell: ({ row }) => (
          <span className="font-mono tabular-nums text-muted-foreground">
            {formatMicroUsd(row.original.drift_micro_usd, {
              signDisplay: true,
            })}
          </span>
        ),
      },
    ],
    [t, i18n.language],
  )
  return (
    <SectionCard
      title={t('reconciliation.title')}
      description={t('reconciliation.description')}
    >
      {r.truncated ? <TruncatedNotice className="mb-3" /> : null}
      {/* The note + estimated-only tiers are the headline honesty signals. */}
      {r.note ? (
        <CaveatNotice tone="info" className="mb-3">
          {r.note}
        </CaveatNotice>
      ) : null}
      {r.estimated_only_tiers && r.estimated_only_tiers.length > 0 ? (
        <div className="mb-3 flex flex-wrap items-center gap-2 text-xs text-muted-foreground">
          <span>{t('reconciliation.estimatedOnlyTiers')}:</span>
          {r.estimated_only_tiers.map((tier) => (
            <Badge key={tier} variant="warning">
              {tier}
            </Badge>
          ))}
        </div>
      ) : null}
      <StatGrid>
        <MetricStat
          icon={<Receipt />}
          label={t('reconciliation.billedTotal')}
          value={
            r.has_billed
              ? formatMicroUsd(r.billed_total_micro_usd, { compact: true })
              : t('reconciliation.noBilled')
          }
          caption={t('reconciliation.billedCaption')}
        />
        <MetricStat
          icon={<Coins />}
          label={t('reconciliation.estimatedTotal')}
          value={formatMicroUsd(r.estimated_total_micro_usd, { compact: true })}
          caption={t('reconciliation.estimatedCaption')}
        />
        <MetricStat
          icon={<Scale />}
          label={t('reconciliation.drift')}
          value={
            r.has_billed
              ? formatMicroUsd(r.drift_micro_usd, {
                  compact: true,
                  signDisplay: true,
                })
              : '—'
          }
          caption={
            r.has_billed
              ? driftPositive
                ? t('reconciliation.driftUnder')
                : t('reconciliation.driftOver')
              : t('reconciliation.driftNoBaseline')
          }
          tone={r.has_billed ? 'warning' : 'default'}
        />
      </StatGrid>
      {r.has_billed && data.length > 0 ? (
        <div className="mt-4">
          <AccessibleChart
            title={t('reconciliation.chartTitle')}
            summary={t('reconciliation.chartSummary', {
              billed: formatMicroUsd(r.billed_total_micro_usd, {
                compact: true,
              }),
              estimated: formatMicroUsd(r.estimated_total_micro_usd, {
                compact: true,
              }),
            })}
            columns={columns}
            data={r.days}
            getRowId={(d) => d.day}
            empty={
              <EmptyState
                title={t('empty.reconciliationChart.title')}
                description={t('empty.reconciliationChart.description')}
              />
            }
          >
            <TrendChart
              data={data}
              xKey="key"
              series={[
                {
                  key: 'billed',
                  label: t('reconciliation.columns.billed'),
                  kind: 'line',
                },
                {
                  key: 'estimated',
                  label: t('reconciliation.columns.estimated'),
                  kind: 'line',
                },
              ]}
              valueFormatter={(v) => formatMicroUsd(v, { compact: true })}
              xTickFormatter={(k) => formatDayKey(k, i18n.language)}
              height={240}
            />
          </AccessibleChart>
        </div>
      ) : null}
    </SectionCard>
  )
}

// --- multi-agent allocation (open FinOps problem — heuristic) ----------------

/** Multi-agent cost allocation over the access graph. MANDATORY: the heuristic
 *  disclaimer (allocated_method_details / note) is rendered via DisclaimerNote — the
 *  split is NOT a settled cost (allocation.go). */
export function AllocationTable({
  allocation,
}: {
  allocation: AllocationResponse
}) {
  const { t } = useTranslation('finops')
  const a = allocation
  const columns = useMemo<TableColumn<AllocationAgent>[]>(
    () => [
      {
        accessorKey: 'agent_ref',
        header: t('allocation.columns.agent'),
        cell: ({ row }) => (
          <span className="font-mono text-xs">{row.original.agent_ref}</span>
        ),
      },
      {
        accessorKey: 'cost_micro_usd',
        header: t('allocation.columns.cost'),
        cell: ({ row }) => (
          <span className="font-mono tabular-nums">
            {formatMicroUsd(row.original.cost_micro_usd)}
          </span>
        ),
      },
      {
        id: 'resolved',
        header: t('allocation.columns.resolved'),
        cell: ({ row }) =>
          row.original.resolved ? (
            <Badge variant="neutral">{t('allocation.resolved')}</Badge>
          ) : (
            <Badge variant="warning">{t('allocation.unresolved')}</Badge>
          ),
      },
      {
        accessorKey: 'confidence',
        header: t('allocation.columns.confidence'),
        cell: ({ row }) => (
          <Badge
            variant={
              row.original.confidence === 'attributed' ? 'neutral' : 'warning'
            }
          >
            {t(`allocation.confidence.${row.original.confidence}`, {
              defaultValue: row.original.confidence,
            })}
          </Badge>
        ),
      },
      {
        id: 'resources',
        header: t('allocation.columns.resources'),
        cell: ({ row }) => {
          const shared = row.original.resources.filter((r) => r.shared).length
          return (
            <span className="text-xs text-muted-foreground">
              {t('allocation.resourceCount', {
                count: row.original.resources.length,
              })}
              {shared > 0 ? (
                <Badge variant="outline" className="ml-2">
                  {t('allocation.sharedCount', { count: shared })}
                </Badge>
              ) : null}
            </span>
          )
        },
      },
    ],
    [t],
  )
  return (
    <SectionCard
      title={t('allocation.title')}
      description={t('allocation.description')}
    >
      {a.truncated ? <TruncatedNotice className="mb-3" /> : null}
      {/* MANDATORY heuristic disclaimer — multi-agent allocation is an open problem. */}
      <DisclaimerNote
        className="mb-3"
        text={a.allocated_method_details || a.note}
      />
      {a.agents.length === 0 ? (
        <p className="text-xs text-muted-foreground">
          {a.note || t('allocation.empty')}
        </p>
      ) : (
        <>
          <p className="mb-3 text-xs text-muted-foreground">
            {t('allocation.method')}:{' '}
            <span className="font-mono text-foreground">
              {a.allocated_method_id}
            </span>
          </p>
          <DataTable<AllocationAgent>
            columns={columns}
            data={a.agents}
            getRowId={(r) => r.agent_ref}
            empty={
              <EmptyState
                title={t('empty.allocation.title')}
                description={t('empty.allocation.description')}
              />
            }
          />
        </>
      )}
    </SectionCard>
  )
}

// --- future dimensions (declared reference — backend pending) ----------------

/** Catalog of FUTURE Anthropic dimensions/breakdowns the backend does not yet model
 *. PURELY a declared static reference (no HTTP): rendered behind a
 *  SeamBadge + an "Declared reference — AsOf" caveat. NEVER a queryable slice. */
export function FutureDimensionsPanel() {
  const { t, i18n } = useTranslation('finops')
  const dimColumns = useMemo<TableColumn<FutureDimension>[]>(
    () => [
      {
        accessorKey: 'id',
        header: t('future.columns.dimension'),
        cell: ({ row }) => (
          <div className="flex flex-col">
            <span className="text-sm text-foreground">
              {t(`future.dims.${row.original.id}.label`, {
                defaultValue: humanize(row.original.id),
              })}
            </span>
            <span className="text-xs text-muted-foreground">
              {t(`future.dims.${row.original.id}.detail`)}
            </span>
          </div>
        ),
      },
      {
        accessorKey: 'groupBy',
        header: t('future.columns.field'),
        cell: ({ row }) => (
          <span className="font-mono text-xs text-muted-foreground">
            {row.original.groupBy}
          </span>
        ),
      },
      {
        accessorKey: 'maturity',
        header: t('future.columns.maturity'),
        cell: ({ row }) => (
          <Badge variant="outline">
            {t(`future.maturity.${row.original.maturity}`, {
              defaultValue: row.original.maturity,
            })}
          </Badge>
        ),
      },
      {
        accessorKey: 'ref',
        header: t('future.columns.ref'),
        cell: ({ row }) => (
          <span className="font-mono text-xs text-muted-foreground">
            {row.original.ref}
          </span>
        ),
      },
    ],
    [t],
  )
  const bdColumns = useMemo<TableColumn<FutureBreakdown>[]>(
    () => [
      {
        accessorKey: 'id',
        header: t('future.columns.breakdown'),
        cell: ({ row }) => (
          <div className="flex flex-col">
            <span className="text-sm text-foreground">
              {t(`future.breakdowns.${row.original.id}.label`, {
                defaultValue: humanize(row.original.id),
              })}
            </span>
            <span className="text-xs text-muted-foreground">
              {t(`future.breakdowns.${row.original.id}.detail`)}
            </span>
          </div>
        ),
      },
      {
        accessorKey: 'usageField',
        header: t('future.columns.field'),
        cell: ({ row }) => (
          <span className="font-mono text-xs text-muted-foreground">
            {row.original.usageField || '—'}
          </span>
        ),
      },
      {
        accessorKey: 'maturity',
        header: t('future.columns.maturity'),
        cell: ({ row }) => (
          <Badge variant="outline">
            {t(`future.maturity.${row.original.maturity}`, {
              defaultValue: row.original.maturity,
            })}
          </Badge>
        ),
      },
      {
        accessorKey: 'ref',
        header: t('future.columns.ref'),
        cell: ({ row }) => (
          <span className="font-mono text-xs text-muted-foreground">
            {row.original.ref}
          </span>
        ),
      },
    ],
    [t],
  )
  return (
    <SectionCard
      title={t('future.title')}
      description={t('future.description')}
      actions={<SeamBadge label={t('future.seam')} />}
    >
      <CaveatNotice tone="info" className="mb-3">
        {t('future.caveat', {
          date: formatDate(`${FUTURE_DIMS_AS_OF}T00:00:00Z`, i18n.language),
        })}
      </CaveatNotice>
      <div className="flex flex-col gap-5">
        <div>
          <p className="mb-2 flex items-center gap-1.5 text-xs font-medium tracking-wide text-muted-foreground uppercase">
            <Users className="size-3.5" aria-hidden />
            {t('future.dimensionsTitle')}
          </p>
          <DataTable<FutureDimension>
            columns={dimColumns}
            data={[...FUTURE_DIMENSIONS]}
            getRowId={(r) => r.id}
            label={t('future.dimensionsTitle')}
            empty={
              <EmptyState
                title={t('empty.futureDimensions.title')}
                description={t('empty.futureDimensions.description')}
              />
            }
          />
        </div>
        <div>
          <p className="mb-2 flex items-center gap-1.5 text-xs font-medium tracking-wide text-muted-foreground uppercase">
            <Zap className="size-3.5" aria-hidden />
            {t('future.breakdownsTitle')}
          </p>
          <DataTable<FutureBreakdown>
            columns={bdColumns}
            data={[...FUTURE_BREAKDOWNS]}
            getRowId={(r) => r.id}
            label={t('future.breakdownsTitle')}
            empty={
              <EmptyState
                title={t('empty.futureBreakdowns.title')}
                description={t('empty.futureBreakdowns.description')}
              />
            }
          />
        </div>
      </div>
    </SectionCard>
  )
}
