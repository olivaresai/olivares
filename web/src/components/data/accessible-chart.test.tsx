// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// ADM-CORE-08 — AccessibleChart. Verifies the chart carries a screen-reader
// summary (1.1.1), and that the "Show as table" toggle swaps the visual for an
// equivalent, fully-labelled data grid (1.4.1 — data conveyed in text, not colour).
import type { TableColumn } from '@/components/data/data-table'
import { fireEvent, render, screen, within } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { EmptyState } from '@/components/ui/empty-state'
import { AccessibleChart, type AccessibleChartProps } from './accessible-chart'

interface Datum {
  model: string
  spend: number
}

const columns: TableColumn<Datum, string>[] = [
  { accessorKey: 'model', header: 'Model' },
  { accessorKey: 'spend', header: 'Spend' },
]

const data: Datum[] = [
  { model: 'opus', spend: 1200 },
  { model: 'sonnet', spend: 800 },
  { model: 'haiku', spend: 300 },
]

function Harness(props?: Partial<AccessibleChartProps<Datum>>) {
  return (
    <AccessibleChart
      title="Spend by model"
      summary="Opus leads at 1200, then sonnet 800, then haiku 300."
      columns={columns}
      data={data}
      getRowId={(r) => r.model}
      empty={<EmptyState title="No spend recorded yet" />}
      {...props}
    >
      <div data-testid="fake-chart">[chart svg]</div>
    </AccessibleChart>
  )
}

describe('AccessibleChart — ADM-CORE-08', () => {
  it('exposes the chart with a screen-reader summary (1.1.1)', () => {
    render(<Harness />)
    // The figure is named by its title.
    expect(
      screen.getByRole('figure', { name: 'Spend by model' }),
    ).toBeInTheDocument()
    // The chart is a role=img carrying the descriptive summary as its name.
    const img = screen.getByRole('img', {
      name: /opus leads at 1200/i,
    })
    expect(within(img).getByTestId('fake-chart')).toBeInTheDocument()
  })

  it('toggles to an equivalent data table and back (1.4.1 non-colour alternative)', () => {
    render(<Harness />)
    expect(screen.queryByRole('grid')).not.toBeInTheDocument()

    fireEvent.click(screen.getByRole('button', { name: /show as table/i }))

    // The data table replaces the chart and conveys every value in text.
    const grid = screen.getByRole('grid', { name: 'Spend by model' })
    expect(within(grid).getByText('opus')).toBeInTheDocument()
    expect(within(grid).getByText('1200')).toBeInTheDocument()
    expect(within(grid).getByText('haiku')).toBeInTheDocument()
    // The chart img is gone while the table is shown.
    expect(screen.queryByRole('img')).not.toBeInTheDocument()

    // Toggle back to the chart.
    fireEvent.click(screen.getByRole('button', { name: /show chart/i }))
    expect(screen.getByRole('img', { name: /opus leads/i })).toBeInTheDocument()
    expect(screen.queryByRole('grid')).not.toBeInTheDocument()
  })

  it('can start on the table view', () => {
    render(<Harness defaultView="table" />)
    expect(screen.getByRole('grid')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /show chart/i })).toHaveAttribute(
      'aria-pressed',
      'true',
    )
  })
})
