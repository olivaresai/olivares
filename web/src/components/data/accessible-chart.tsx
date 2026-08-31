// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import type { TableColumn } from '@/components/data/data-table'
import { BarChart3, TableIcon } from 'lucide-react'
import { useId, useState, type ReactElement, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { cn } from '@/lib/utils'
import { Button } from '@/components/ui/button'
import { DataTable } from './data-table'

/**
 * AccessibleChart — THE wrapper the dashboard views (FinOps / observability)
 * put around any themed chart (TrendChart / CategoryBarChart / DonutChart from
 * `@/components/charts`) so it is accessible by default (ADM-CORE-08):
 *
 *  - WCAG 1.1.1 Non-text Content: the chart is a `role="img"` with a descriptive
 *    `summary` as its accessible name, plus a visually-hidden text summary — a
 *    screen reader hears WHAT the chart shows, not "graphic".
 *  - WCAG 1.4.1 Use of Color: a one-click "Show as table" toggle swaps the visual
 *    for an equivalent, fully-labelled data table (reusing `DataTable`) — every
 *    series/category is conveyed in TEXT, never by colour alone.
 *  - WCAG 1.4.11 Non-text Contrast: the chart inherits the brandv4 tokens
 *    (AA-contrasted axis/series), so no raw low-contrast hue is introduced here.
 *
 * The wrapper owns the chart↔table toggle, the figure semantics and the labels;
 * the caller supplies the rendered chart, the SR `summary`, and the equivalent
 * tabular `columns`/`data`. i18n: toggle labels come from the `common` namespace.
 */
export interface AccessibleChartProps<TRow> {
  /** Visible + accessible name for the whole figure (e.g. "Spend by model"). */
  title: string
  /** A sentence describing what the chart conveys, for assistive tech (1.1.1).
   * Be specific: trend direction, peak, total — not "a bar chart". */
  summary: string
  /** The rendered themed chart (e.g. `<CategoryBarChart .../>`). */
  children: ReactNode
  /** The equivalent data as a table (the non-colour alternative, 1.4.1). */
  // eslint-disable-next-line @typescript-eslint/no-explicit-any -- heterogeneous columns
  columns: TableColumn<TRow, any>[]
  data: TRow[]
  getRowId?: (row: TRow, index: number) => string
  /** REQUIRED, and forwarded to the table view: what an operator sees when the
   * figure has nothing to plot. It is asked of the CALLER because only the caller
   * knows what the series measures and what makes a first point appear; a default
   * here would be the same absent decision this wrapper exists to avoid, moved one
   * layer up. `ReactElement` and not `ReactNode` on purpose — `ReactNode` admits
   * `undefined`, so "no decision" would still typecheck. */
  empty: ReactElement
  /** Start on the chart (default) or the data table. */
  defaultView?: 'chart' | 'table'
  /** Optional visible caption rendered under the figure. */
  caption?: ReactNode
  /** Hide the visible title row (the chart still gets the accessible name). */
  hideTitle?: boolean
  className?: string
}

export function AccessibleChart<TRow>({
  title,
  summary,
  children,
  columns,
  data,
  getRowId,
  empty,
  defaultView = 'chart',
  caption,
  hideTitle = false,
  className,
}: AccessibleChartProps<TRow>) {
  const { t } = useTranslation('common')
  const [view, setView] = useState<'chart' | 'table'>(defaultView)
  const summaryId = useId()
  const isChart = view === 'chart'

  return (
    <figure aria-label={title} className={cn('flex flex-col gap-2', className)}>
      <div className="flex items-center justify-between gap-2">
        {!hideTitle ? (
          <figcaption className="text-sm font-medium text-foreground">
            {title}
          </figcaption>
        ) : (
          <span />
        )}
        <Button
          variant="ghost"
          size="sm"
          aria-pressed={!isChart}
          onClick={() => setView(isChart ? 'table' : 'chart')}
        >
          {isChart ? (
            <>
              <TableIcon aria-hidden />
              {t('chart.showTable')}
            </>
          ) : (
            <>
              <BarChart3 aria-hidden />
              {t('chart.showChart')}
            </>
          )}
        </Button>
      </div>

      {isChart ? (
        <div role="img" aria-label={summary} aria-describedby={summaryId}>
          {children}
          {/* The same summary as readable text — belt-and-braces for AT that does
              not expose the aria-label of a role=img region, and for 1.1.1. */}
          <p id={summaryId} className="sr-only">
            {summary}
          </p>
        </div>
      ) : (
        <DataTable
          columns={columns}
          data={data}
          getRowId={getRowId}
          label={title}
          empty={empty}
        />
      )}

      {caption ? (
        <p className="text-xs text-muted-foreground">{caption}</p>
      ) : null}
    </figure>
  )
}
