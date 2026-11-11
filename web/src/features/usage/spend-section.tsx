// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// USAGE › SPEND: what the runs of a chosen window used, by tool and by profile, narrowed
// to a tool or a profile and saved as CSV. It reads the sessions module's totals per day
// (`GET /v1/m/sessions/usage`, the read Models › Usage makes, on its keys), and only for a
// person who may read the runs. Money shows only where `money` says, the Usage place's own
// check; a sum no run reported stays unknown, and the runs that reported none are counted.
import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { ChartNoAxesColumn, Download } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { ChartLegend, TrendChart, useChartTheme } from '@/components/charts'
import { AccessibleChart } from '@/components/data/accessible-chart'
import type { TableColumn } from '@/components/data/data-table'
import { XScroll } from '@/components/data/scroll-edges'
import { StaticTable } from '@/components/data/static-table'
import { Button } from '@/components/ui/button'
import { EmptyState } from '@/components/ui/empty-state'
import { MetricStat, StatGrid } from '@/components/ui/figures'
import { Input } from '@/components/ui/input'
import { Segmented } from '@/components/ui/segmented'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { AsyncSection, SectionCard } from '@/features/_intel'
import { useProfileList } from '@/features/agent-tools/use-profiles'
import { toolName } from '@/features/agentops/engine-tools'
import { modelsApi, modelsKeys } from '@/features/models/api'
import type { UsageRow } from '@/features/models/types'
import { downloadBlob } from '@/lib/api/download'
import { useAuth } from '@/lib/auth/context'
import {
  formatDayKey,
  formatInt,
  formatMicroUsd,
  formatTokens,
} from '@/lib/format'
import {
  byKey,
  dailyOf,
  dayAfter,
  lastDays,
  MAX_RANGE_DAYS,
  RANGE_DAYS,
  spendCsv,
  spendOf,
  spendWindow,
  type Metric,
  type Spend,
  type SpendWindow,
} from './spend'

/** What Usage narrows to: a tool (its driver) and one profile. */
export interface SpendFilter {
  tool: string | null
  profile: string | null
}

type Range = `${(typeof RANGE_DAYS)[number]}` | 'custom'

type NameOf = (key?: string) => string

type Point = Record<string, string | number>

/** A Select item needs a value: this one stands for every tool or every profile. */
const ALL = '*'

/** Each tool's own colour, always beside its name in the legend and the table. */
const TOOL_COLOR: Record<string, string> = {
  claude: 'var(--tool-claude)',
  codex: 'var(--tool-codex)',
  opencode: 'var(--tool-opencode)',
  'gemini-cli': 'var(--tool-gemini)',
}

export function SpendSection({
  money,
  filter,
  onFilter,
}: {
  money: boolean
  filter: SpendFilter
  onFilter: (filter: SpendFilter) => void
}) {
  const { t } = useTranslation('usage')
  const { activeTenant, can } = useAuth()
  const permitted = can('sessions:run:read')
  const [range, setRange] = useState<Range>('14')
  const [custom, setCustom] = useState({ from: '', to: '' })
  const span =
    range === 'custom'
      ? spendWindow(custom.from, custom.to)
      : lastDays(Number(range))
  const read = (by: 'tool' | 'account') => ({
    queryKey: modelsKeys.usage(activeTenant, {
      by,
      since: span?.since,
      until: span?.until,
      bucket: 'day',
    }),
    queryFn: () =>
      modelsApi.usage({
        group_by: by,
        since: span!.since,
        until: span!.until,
        bucket: 'day',
      }),
    enabled: permitted && span !== null,
  })
  const toolsQ = useQuery(read('tool'))
  const profilesQ = useQuery(read('account'))
  const profiles = useProfileList().data
  if (!permitted) return null

  // A profile is named and tied to its tool by the account list; a removed one keeps
  // its runs and has no name left, and runs that recorded no profile have neither.
  const named = [...(profiles?.accounts ?? []), ...(profiles?.disabled ?? [])]
  const known = new Map(named.map((a) => [a.account_ref, a]))
  const driverOf = (key?: string) =>
    key === undefined ? undefined : known.get(key)?.driver
  const profileName: NameOf = (key) => {
    if (key === undefined) return t('spend.profiles.none')
    const account = known.get(key)
    return account
      ? account.display_name || account.name
      : t('spend.profiles.unnamed')
  }
  const tools = toolsQ.data?.items ?? []
  const accounts = profilesQ.data?.items ?? []
  const ofTool = (r: UsageRow) =>
    !filter.tool || driverOf(r.key) === filter.tool
  const profileRows = accounts.filter(
    (r) => ofTool(r) && (!filter.profile || r.key === filter.profile),
  )
  // With a profile chosen, the figures, the chart and the file say that profile's runs.
  const dimension = filter.profile ? 'profile' : 'tool'
  const shown = filter.profile
    ? profileRows
    : tools.filter((r) => !filter.tool || r.key === filter.tool)
  const total = spendOf(shown)
  const nameOf: NameOf = (key) =>
    dimension === 'tool' ? toolName(key ?? '') : profileName(key)
  const colorOf = (key?: string) =>
    TOOL_COLOR[(dimension === 'tool' ? key : driverOf(key)) ?? '']
  const keysOf = (rows: readonly UsageRow[], chosen: string | null) =>
    [...new Set([...rows.map((r) => r.key), chosen])].filter(
      (k): k is string => !!k,
    )
  const toolOptions = keysOf(tools, filter.tool).map((k) => ({
    value: k,
    label: toolName(k),
  }))
  const profileKeys = keysOf(accounts.filter(ofTool), filter.profile)
  const profileOptions = profileKeys.map((k) => ({
    value: k,
    label: profileName(k),
  }))

  const exportCsv = () => {
    if (!span) return
    const csv = spendCsv(shown, dimension, nameOf, money)
    downloadBlob(
      new Blob([csv], { type: 'text/csv' }),
      `usage-${span.from}-${span.to}.csv`,
    )
  }

  return (
    <section aria-labelledby="usage-spend" className="flex flex-col gap-4">
      <h2 id="usage-spend" className="sr-only">
        {t(money ? 'spend.heading' : 'spend.headingNoCost')}
      </h2>
      <div className="flex flex-wrap items-center gap-2">
        <Segmented
          size="sm"
          aria-label={t('spend.range.label')}
          value={range}
          onValueChange={(next) => {
            // A chosen range starts from the window on screen.
            if (next === 'custom' && span)
              setCustom({ from: span.from, to: span.to })
            setRange(next)
          }}
          options={[
            ...RANGE_DAYS.map((d) => ({
              value: `${d}` as Range,
              label: t(`spend.range.d${d}`),
            })),
            { value: 'custom' as Range, label: t('spend.range.custom') },
          ]}
        />
        {range === 'custom' ? (
          <>
            <Input
              type="date"
              aria-label={t('spend.range.from')}
              value={custom.from}
              min={dayAfter(custom.to, 1 - MAX_RANGE_DAYS)}
              max={custom.to || undefined}
              onChange={(e) => setCustom({ ...custom, from: e.target.value })}
              className="w-auto"
            />
            <Input
              type="date"
              aria-label={t('spend.range.to')}
              value={custom.to}
              min={custom.from || undefined}
              max={dayAfter(custom.from, MAX_RANGE_DAYS - 1)}
              onChange={(e) => setCustom({ ...custom, to: e.target.value })}
              className="w-auto"
            />
          </>
        ) : null}
        <Choice
          label={t('spend.tool')}
          all={t('spend.allTools')}
          value={filter.tool}
          options={toolOptions}
          onChange={(tool) => onFilter({ tool, profile: null })}
        />
        <Choice
          label={t('spend.profile')}
          all={t('spend.allProfiles')}
          value={filter.profile}
          options={profileOptions}
          onChange={(profile) => onFilter({ ...filter, profile })}
        />
        {shown.length > 0 ? (
          <Button
            variant="secondary"
            size="sm"
            className="ms-auto"
            onClick={exportCsv}
          >
            <Download aria-hidden />
            {t('spend.export')}
          </Button>
        ) : null}
      </div>
      {span === null ? (
        <p role="alert" className="text-caption text-bad">
          {t('spend.range.invalid', { days: MAX_RANGE_DAYS })}
        </p>
      ) : null}
      <AsyncSection query={toolsQ} skeletonHeight={360}>
        {() => (
          <AsyncSection query={profilesQ} skeletonHeight={360}>
            {() =>
              span && total.runs > 0 ? (
                <>
                  <Totals spend={total} money={money} />
                  <SpendChart
                    rows={shown}
                    span={span}
                    metric={money ? 'cost' : 'tokens'}
                    nameOf={nameOf}
                    colorOf={colorOf}
                    unpriced={total.runsWithoutCost}
                  />
                  <ProfileTable
                    rows={profileRows}
                    money={money}
                    nameOf={profileName}
                    driverOf={driverOf}
                  />
                </>
              ) : (
                <EmptyState
                  icon={<ChartNoAxesColumn />}
                  title={t('spend.empty.title')}
                  description={t('spend.empty.description')}
                />
              )
            }
          </AsyncSection>
        )}
      </AsyncSection>
    </section>
  )
}

/** A filter: every tool (or profile), or one of those the window holds. */
function Choice({
  label,
  all,
  value,
  options,
  onChange,
}: {
  label: string
  all: string
  value: string | null
  options: { value: string; label: string }[]
  onChange: (value: string | null) => void
}) {
  return (
    <Select
      value={value ?? ALL}
      onValueChange={(v) => onChange(v === ALL ? null : v)}
    >
      <SelectTrigger aria-label={label} className="w-auto">
        <SelectValue />
      </SelectTrigger>
      <SelectContent>
        <SelectItem value={ALL}>{all}</SelectItem>
        {options.map((o) => (
          <SelectItem key={o.value} value={o.value}>
            {o.label}
          </SelectItem>
        ))}
      </SelectContent>
    </Select>
  )
}

/** The window's figures. A sum over some runs counts the runs that reported none. */
function Totals({ spend, money }: { spend: Spend; money: boolean }) {
  const { t, i18n } = useTranslation('usage')
  const lang = i18n.language
  const left = (runs: number, key: string) =>
    runs > 0 ? t(key, { runs: formatInt(runs, lang) }) : undefined
  return (
    <div role="group" aria-label={t('spend.totals')}>
      <StatGrid>
        {money ? (
          <MetricStat
            label={t('spend.cost')}
            value={formatMicroUsd(spend.cost, { locale: lang })}
            caption={left(spend.runsWithoutCost, 'spend.costUnreported')}
          />
        ) : null}
        <MetricStat
          label={t('spend.runs')}
          value={formatInt(spend.runs, lang)}
        />
        <MetricStat
          label={t('spend.input')}
          value={formatTokens(spend.input, lang)}
          caption={left(spend.runsWithoutTokens, 'spend.tokensUnreported')}
        />
        <MetricStat
          label={t('spend.output')}
          value={formatTokens(spend.output, lang)}
        />
      </StatGrid>
    </div>
  )
}

/** Each day of the window, stacked by tool (or the one profile), with its table twin. */
function SpendChart({
  rows,
  span,
  metric,
  nameOf,
  colorOf,
  unpriced,
}: {
  rows: UsageRow[]
  span: SpendWindow
  metric: Metric
  nameOf: NameOf
  colorOf: (key?: string) => string | undefined
  unpriced: number
}) {
  const { t, i18n } = useTranslation('usage')
  const lang = i18n.language
  const theme = useChartTheme()
  const { series, points } = dailyOf(rows, span.days, metric)
  const format = (v: number) =>
    metric === 'cost'
      ? formatMicroUsd(v, { locale: lang, compact: true })
      : formatTokens(v, lang)
  const title = t(`spend.chart.${metric}`)
  const labels = series.map((s) => nameOf(s.key))
  const colors = series.map(
    (s, i) => colorOf(s.key) ?? theme.series[i % theme.series.length],
  )
  const totals = series.map((s) =>
    points.reduce((n, p) => n + (p[s.id] as number), 0),
  )
  const columns: TableColumn<Point>[] = [
    {
      accessorKey: 'day',
      header: t('spend.chart.day'),
      cell: ({ row }) => formatDayKey(row.original.day as string, lang),
    },
    ...series.map((s, i) => ({
      accessorKey: s.id,
      header: labels[i],
      cell: ({ row }: { row: { original: Point } }) =>
        format(row.original[s.id] as number),
    })),
  ]
  return (
    <SectionCard title={title}>
      <AccessibleChart<Point>
        title={title}
        hideTitle
        summary={t('spend.chart.summary', {
          title,
          from: formatDayKey(span.from, lang),
          to: formatDayKey(span.to, lang),
          total: format(totals.reduce((a, b) => a + b, 0)),
        })}
        columns={columns}
        data={points}
        getRowId={(p) => String(p.day)}
        empty={
          <EmptyState
            title={t('spend.empty.title')}
            description={t('spend.empty.description')}
          />
        }
        caption={
          metric === 'cost' && unpriced > 0
            ? t('spend.chart.unpriced')
            : undefined
        }
      >
        <TrendChart
          data={points}
          xKey="day"
          stacked
          series={series.map((s, i) => ({
            key: s.id,
            label: labels[i],
            color: colors[i],
          }))}
          valueFormatter={format}
          xTickFormatter={(v) => formatDayKey(v, lang)}
        />
        <ChartLegend
          className="mt-2 flex-row flex-wrap gap-x-4"
          valueFormatter={format}
          items={series.map((s, i) => ({
            key: s.id,
            label: labels[i],
            value: totals[i],
            color: colors[i],
          }))}
        />
      </AccessibleChart>
    </SectionCard>
  )
}

/** The window by profile. A sum over only some runs is not the row's total: its cell
 * stays empty, as on Models › Usage. */
function ProfileTable({
  rows,
  money,
  nameOf,
  driverOf,
}: {
  rows: UsageRow[]
  money: boolean
  nameOf: NameOf
  driverOf: (key?: string) => string | undefined
}) {
  const { t, i18n } = useTranslation('usage')
  const lang = i18n.language
  const heading = t('spend.profiles.heading')
  const tokens = (r: Spend, n: number | null) =>
    r.runsWithoutTokens > 0 ? null : formatTokens(n, lang)
  const totals = byKey(rows)
  return (
    <SectionCard title={heading} noPadding>
      <XScroll contentKey={totals.length} accessibleLabel={heading}>
        <StaticTable>
          <thead>
            <tr>
              <th>{t('spend.profiles.profile')}</th>
              <th>{t('spend.tool')}</th>
              <th className="text-right">{t('spend.runs')}</th>
              <th className="text-right">{t('spend.profiles.input')}</th>
              <th className="text-right">{t('spend.profiles.output')}</th>
              {money ? <th className="text-right">{t('spend.cost')}</th> : null}
            </tr>
          </thead>
          <tbody>
            {totals.map((r) => {
              const driver = driverOf(r.key)
              return (
                <tr key={r.key ?? ''}>
                  <td>{nameOf(r.key)}</td>
                  <td>{driver ? toolName(driver) : null}</td>
                  <td className="text-right tabular-nums">
                    {formatInt(r.runs, lang)}
                  </td>
                  <td className="text-right tabular-nums">
                    {tokens(r, r.input)}
                  </td>
                  <td className="text-right tabular-nums">
                    {tokens(r, r.output)}
                  </td>
                  {money ? (
                    <td className="text-right tabular-nums">
                      {r.runsWithoutCost > 0
                        ? null
                        : formatMicroUsd(r.cost, { locale: lang })}
                    </td>
                  ) : null}
                </tr>
              )
            })}
          </tbody>
        </StaticTable>
      </XScroll>
    </SectionCard>
  )
}
