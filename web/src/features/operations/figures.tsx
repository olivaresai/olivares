// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The two figures Operations draws, each as a titled card holding the chart, its sentence and
// the same data as a table (`AccessibleChart`): bars for a ranking, a trend over time. They
// only lay out what the sections hand them; no figure here is computed.
import { CategoryBarChart, TrendChart } from '@/components/charts'
import type { SeriesSpec } from '@/components/charts'
import {
  AccessibleChart,
  type AccessibleChartProps,
} from '@/components/data/accessible-chart'
import { SectionCard } from '@/features/_intel'

/** Bars drawn for a ranking; the table beside them holds every row. */
const BAR_ROWS = 10
const BAR_ROW_PX = 32
const TREND_HEIGHT = 200
/** The category axis is 120 px wide: a longer mount or folder name keeps its end, which is
 *  the part that tells two of them apart. The table view carries the whole name. */
const MAX_LABEL = 18
const shorten = (label: string) =>
  label.length > MAX_LABEL ? `…${label.slice(1 - MAX_LABEL)}` : label

/** A figure is never given an empty `rows`: each section says in words that it has nothing
 *  to show (no samples, no filesystem, no live session) and draws no figure at all. */
const NEVER_EMPTY = <></>

interface FigureProps<Row extends Record<string, unknown>> {
  title: string
  /** The figure in one sentence: what a screen reader hears and what the chart says. */
  summary: string
  /** At least one row. */
  rows: Row[]
  columns: AccessibleChartProps<Row>['columns']
  getRowId: (row: Row) => string
  valueFormatter: (value: number) => string
}

export function BarFigure<Row extends Record<string, unknown>>({
  title,
  summary,
  rows,
  columns,
  getRowId,
  valueFormatter,
  categoryKey,
  valueKey,
}: FigureProps<Row> & { categoryKey: string; valueKey: string }) {
  const shown = rows.slice(0, BAR_ROWS)
  return (
    <SectionCard title={title}>
      <AccessibleChart<Row>
        title={title}
        hideTitle
        summary={summary}
        columns={columns}
        data={rows}
        getRowId={getRowId}
        empty={NEVER_EMPTY}
      >
        <CategoryBarChart
          data={shown}
          categoryKey={categoryKey}
          valueKey={valueKey}
          height={48 + shown.length * BAR_ROW_PX}
          valueFormatter={valueFormatter}
          categoryFormatter={shorten}
          colorByCategory={false}
        />
      </AccessibleChart>
    </SectionCard>
  )
}

export function TrendFigure<Row extends Record<string, unknown>>({
  title,
  summary,
  rows,
  columns,
  getRowId,
  valueFormatter,
  xKey,
  series,
  xTickFormatter,
  reference,
}: FigureProps<Row> & {
  xKey: string
  series: SeriesSpec[]
  xTickFormatter: (value: string) => string
  /** A line at a limit the series cannot pass (memory: the total). */
  reference?: { y: number; label: string }
}) {
  return (
    <SectionCard title={title}>
      <AccessibleChart<Row>
        title={title}
        hideTitle
        summary={summary}
        columns={columns}
        data={rows}
        getRowId={getRowId}
        empty={NEVER_EMPTY}
      >
        <TrendChart
          data={rows}
          xKey={xKey}
          series={series}
          height={TREND_HEIGHT}
          valueFormatter={valueFormatter}
          xTickFormatter={xTickFormatter}
          reference={reference}
          yAxis={{ width: 64 }}
        />
      </AccessibleChart>
    </SectionCard>
  )
}
