// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Models > Usage: what the runs the caller may read used, per model, account, provider or
// tool, over 7 or 30 days. It adds no engine route: the totals and the daily buckets are the
// sessions module's read (`GET /v1/m/sessions/usage`), which answers only for runs the
// caller may read. A sum no tool reported arrives as `null` and is shown as "not reported",
// never as 0.
import { useMemo, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import type { TFunction } from 'i18next'
import { BarChart3 } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { ChartLegend, TrendChart, useChartTheme } from '@/components/charts'
import { XScroll } from '@/components/data/scroll-edges'
import { AccessibleChart } from '@/components/data/accessible-chart'
import type { TableColumn } from '@/components/data/data-table'
import { StaticTable } from '@/components/data/static-table'
import { EmptyState } from '@/components/ui/empty-state'
import { Segmented } from '@/components/ui/segmented'
import { AsyncSection, SectionCard } from '@/features/_intel'
import { agentOpsApi, agentOpsKeys } from '@/features/agentops/api'
import { useAuthBoundary } from '@/features/agentops/auth-boundary'
import { profileLabel } from '@/features/agentops/types'
import { useUsagePath } from '@/features/sessions/session-meter'
import { useAuth } from '@/lib/auth/context'
import {
  formatDayKey,
  formatInt,
  formatMicroUsd,
  formatTokens,
} from '@/lib/format'
import { modelsApi, modelsKeys } from './api'
import type { UsageRow } from './types'
import {
  dailyRuns,
  USAGE_BY,
  USAGE_DAYS,
  usageWindow,
  type UsageBy,
  type UsageDays,
  type UsagePeriod,
  type UsageSeries,
} from './usage'
import './i18n'

type DayRow = Record<string, string | number>

type NameOf = (key: string) => string

/** The name a series carries in the legend, the chart and the table view. */
const seriesLabel = (t: TFunction, s: UsageSeries, nameOf: NameOf) =>
  s.other ? t('usage.other') : s.key === '' ? t('usage.noKey') : nameOf(s.key)

const asIs: NameOf = (key) => key

/** An account row is keyed by its provider profile reference. Name it as the rest of the
 *  console does; a reference the read does not know (or a read the caller may not make)
 *  stays as the reference, which is still the true key. */
function useAccountNames(enabled: boolean): NameOf {
  const { activeTenant, can } = useAuth()
  const boundary = useAuthBoundary()
  const profilesQ = useQuery({
    queryKey: agentOpsKeys.profiles(activeTenant, boundary.epoch, {
      limit: 200,
    }),
    queryFn: ({ signal }) =>
      agentOpsApi.listProfiles({ limit: 200 }, { signal }),
    enabled: enabled && can('sessions:profile:read'),
  })
  const profiles = profilesQ.data?.items
  return useMemo(() => {
    if (!enabled || !profiles) return asIs
    const names = new Map(profiles.map((p) => [p.profile_ref, profileLabel(p)]))
    return (key: string) => names.get(key) ?? key
  }, [enabled, profiles])
}

function UsageChart({
  items,
  days,
  by,
  period,
  nameOf,
}: {
  items: UsageRow[]
  days: string[]
  by: UsageBy
  period: UsagePeriod
  nameOf: NameOf
}) {
  const { t, i18n } = useTranslation('models')
  const lang = i18n.language
  const theme = useChartTheme()
  const { series, rows, total, busiest } = useMemo(
    () => dailyRuns(items, days),
    [items, days],
  )
  const labels = series.map((s) => seriesLabel(t, s, nameOf))
  const columns = useMemo<TableColumn<DayRow>[]>(
    () => [
      {
        accessorKey: 'day',
        header: t('usage.columns.day'),
        cell: ({ row }) => row.original.day,
      },
      ...series.map((s) => ({
        accessorKey: s.id,
        header: seriesLabel(t, s, nameOf),
        cell: ({ row }: { row: { original: DayRow } }) =>
          formatInt(row.original[s.id] as number, lang),
      })),
    ],
    [series, lang, t, nameOf],
  )
  if (total === 0) return null

  return (
    <SectionCard title={t('usage.chartTitle')}>
      <AccessibleChart<DayRow>
        title={t('usage.chartTitle')}
        hideTitle
        summary={t('usage.chartSummary', {
          by: t(`usage.by.${by}`),
          period: t(`usage.period.days${period}`),
          total: formatInt(total, lang),
          day: busiest.day,
          peak: formatInt(busiest.runs, lang),
        })}
        columns={columns}
        data={rows}
        getRowId={(r) => String(r.day)}
        empty={
          <EmptyState
            title={t('usage.empty.title')}
            description={t('usage.empty.description')}
          />
        }
      >
        <TrendChart
          data={rows}
          xKey="day"
          stacked
          series={series.map((s, i) => ({ key: s.id, label: labels[i] }))}
          valueFormatter={(v) => formatInt(v, lang)}
          xTickFormatter={(v) => formatDayKey(v, lang)}
          yAxis={{ allowDecimals: false }}
          emptyLabel={t('usage.empty.title')}
        />
        {/* Same palette order as the chart, so each swatch names its series. */}
        <ChartLegend
          className="mt-2 flex-row flex-wrap gap-x-4"
          valueFormatter={(v) => formatInt(v, lang)}
          items={series.map((s, i) => ({
            key: s.id,
            label: labels[i],
            value: rows.reduce((n, r) => n + (r[s.id] as number), 0),
            color: theme.series[i % theme.series.length],
          }))}
        />
      </AccessibleChart>
    </SectionCard>
  )
}

function UsageTable({
  items,
  by,
  nameOf,
}: {
  items: UsageRow[]
  by: UsageBy
  nameOf: NameOf
}) {
  const { t, i18n } = useTranslation('models')
  const lang = i18n.language
  // Cost only where Usage opens for the person; others see runs and tokens.
  const money = useUsagePath() !== null
  // An unknown figure says nothing: the cell stays empty, never "not reported" or a zero. A
  // sum over only some runs (the engine skips runs that reported none) is not the row's
  // total, so it stays empty too.
  const tokens = (r: UsageRow, n: number | null) =>
    n === null || r.runs_without_tokens > 0 ? null : formatTokens(n, lang)

  return (
    <SectionCard title={t('usage.totalsTitle')} noPadding>
      <XScroll
        contentKey={items.length}
        accessibleLabel={t('usage.totalsTitle')}
      >
        <StaticTable>
          <thead>
            <tr>
              <th>{t(`usage.by.${by}`)}</th>
              <th className="text-right">{t('usage.columns.runs')}</th>
              <th className="text-right">{t('usage.columns.input')}</th>
              <th className="text-right">{t('usage.columns.output')}</th>
              {money ? (
                <th className="text-right">{t('usage.columns.cost')}</th>
              ) : null}
            </tr>
          </thead>
          <tbody>
            {items.map((r) => (
              <tr key={r.key ?? ''}>
                <td className="font-mono text-caption">
                  {r.key === undefined ? t('usage.noKey') : nameOf(r.key)}
                </td>
                <td className="text-right tabular-nums">
                  {formatInt(r.runs, lang)}
                </td>
                <td className="text-right tabular-nums">
                  {tokens(r, r.input_tokens)}
                </td>
                <td className="text-right tabular-nums">
                  {tokens(r, r.output_tokens)}
                </td>
                {money ? (
                  <td className="text-right tabular-nums">
                    {r.cost_micro_usd === null || r.runs_without_cost > 0
                      ? null
                      : formatMicroUsd(r.cost_micro_usd, { locale: lang })}
                  </td>
                ) : null}
              </tr>
            ))}
          </tbody>
        </StaticTable>
      </XScroll>
    </SectionCard>
  )
}

/** `fixed` is a period a page above chooses for every figure it holds (Operations): the tab
 *  then asks for it and shows no period control of its own. */
export function UsageTab({ period: fixed }: { period?: UsagePeriod } = {}) {
  const { t } = useTranslation('models')
  const { activeTenant } = useAuth()
  const [by, setBy] = useState<UsageBy>('model')
  const [chosen, setPeriod] = useState<UsageDays>(7)
  const period = fixed ?? chosen
  const { since, days } = usageWindow(period)
  const nameOf = useAccountNames(by === 'account')

  const totalsQ = useQuery({
    queryKey: modelsKeys.usage(activeTenant, { by, since }),
    queryFn: () => modelsApi.usage({ group_by: by, since }),
  })
  const dailyQ = useQuery({
    queryKey: modelsKeys.usage(activeTenant, { by, since, bucket: 'day' }),
    queryFn: () => modelsApi.usage({ group_by: by, since, bucket: 'day' }),
  })

  return (
    <div className="flex flex-col gap-4">
      <div className="flex flex-wrap items-center gap-3">
        <Segmented
          size="sm"
          aria-label={t('usage.byLabel')}
          value={by}
          onValueChange={setBy}
          options={USAGE_BY.map((v) => ({
            value: v,
            label: t(`usage.by.${v}`),
          }))}
        />
        {fixed === undefined ? (
          <Segmented
            size="sm"
            aria-label={t('usage.periodLabel')}
            value={String(chosen)}
            onValueChange={(v) => setPeriod(Number(v) as UsageDays)}
            options={USAGE_DAYS.map((v) => ({
              value: String(v),
              label: t(`usage.period.days${v}`),
            }))}
          />
        ) : null}
      </div>

      <AsyncSection query={totalsQ}>
        {(totals) =>
          totals.items.length === 0 ? (
            <EmptyState
              icon={<BarChart3 />}
              title={t('usage.empty.title')}
              description={t('usage.empty.description')}
            />
          ) : (
            <>
              <AsyncSection query={dailyQ} skeletonHeight={240}>
                {(daily) => (
                  <UsageChart
                    items={daily.items}
                    days={days}
                    by={by}
                    period={period}
                    nameOf={nameOf}
                  />
                )}
              </AsyncSection>
              <UsageTable items={totals.items} by={by} nameOf={nameOf} />
            </>
          )
        }
      </AsyncSection>
    </div>
  )
}
