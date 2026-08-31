// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// ADM-CORE-07 (virtualization) + ADM-CORE-02 (APG grid). jsdom has no layout
// engine, so the scale tests assert the DETERMINISTIC invariants: the public API is
// unchanged for small tables; a 100k-row page advertises the TRUE aria-rowcount +
// per-row aria-rowindex WITHOUT materialising 100k DOM rows; and it renders fast.
// The keyboard tests exercise the APG grid roving-tabindex navigation (focus does
// not need layout). The real-browser virtual WINDOW is asserted in the Playwright
// e2e (e2e/foundation.spec.ts), the honest split for a layout-driven feature.
import type { TableColumn } from '@/components/data/data-table'
import {
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { useState } from 'react'
import { describe, expect, it, vi } from 'vitest'
import { EmptyState } from '@/components/ui/empty-state'
import { DataTable } from './data-table'

interface Row {
  id: string
  name: string
  kind: string
}

const columns: TableColumn<Row, string>[] = [
  { accessorKey: 'name', header: 'Name' },
  { accessorKey: 'kind', header: 'Kind' },
]

const make = (n: number): Row[] =>
  Array.from({ length: n }, (_, i) => ({
    id: String(i),
    name: `row-${i}`,
    kind: i % 2 ? 'agent' : 'resource',
  }))

// The bench's own tables are not product surfaces; `empty` is required, so they
// declare one once and share it. A test that omitted it would not compile — which
// is the point of the prop being required at all.
const BENCH_EMPTY = <EmptyState title="Nothing on this bench" />

const bodyRows = () =>
  within(screen.getByRole('grid').querySelector('tbody')!)
    .queryAllByRole('row')
    .filter((r) => r.getAttribute('aria-rowindex'))

function SelectionHarness({
  searchable = false,
  onRowClick,
}: {
  searchable?: boolean
  onRowClick?: (row: Row) => void
}) {
  const [selectedIds, setSelectedIds] = useState<Set<string>>(new Set())
  return (
    <>
      <DataTable
        columns={columns}
        data={make(3)}
        getRowId={(row) => row.id}
        selectable
        selectedIds={selectedIds}
        onSelectedIdsChange={setSelectedIds}
        onRowClick={onRowClick}
        searchable={searchable}
        empty={BENCH_EMPTY}
      />
      <output data-testid="selected-ids">
        {[...selectedIds].sort().join(',')}
      </output>
    </>
  )
}

describe('DataTable — small (non-virtualized) path is unchanged', () => {
  it('renders every row in flow below the threshold', () => {
    render(
      <DataTable
        columns={columns}
        data={make(5)}
        getRowId={(r) => r.id}
        empty={BENCH_EMPTY}
      />,
    )
    expect(screen.getByText('row-0')).toBeInTheDocument()
    expect(screen.getByText('row-4')).toBeInTheDocument()
    expect(bodyRows().length).toBe(5)
    expect(screen.getByRole('grid')).toHaveAttribute('aria-rowcount', '6')
  })

  it('gives the first body row aria-rowindex 2 (header occupies grid row 1)', () => {
    render(
      <DataTable
        columns={columns}
        data={make(5)}
        getRowId={(r) => r.id}
        empty={BENCH_EMPTY}
      />,
    )
    expect(bodyRows()[0]).toHaveAttribute('aria-rowindex', '2')
    expect(bodyRows()[4]).toHaveAttribute('aria-rowindex', '6')
  })

  it('exposes sortable headers with aria-sort', () => {
    render(
      <DataTable
        columns={columns}
        data={make(3)}
        getRowId={(r) => r.id}
        empty={BENCH_EMPTY}
      />,
    )
    const headers = screen.getAllByRole('columnheader')
    expect(headers.length).toBe(2)
    expect(headers[0]).toHaveAttribute('aria-sort', 'none')
  })
})

describe('DataTable — ADM-CORE-02 APG grid', () => {
  it('is a labelled grid with a single tab stop and column count', () => {
    render(
      <DataTable
        columns={columns}
        data={make(3)}
        getRowId={(r) => r.id}
        label="Inventory"
        empty={BENCH_EMPTY}
      />,
    )
    const grid = screen.getByRole('grid')
    expect(grid).toHaveAttribute('aria-label', 'Inventory')
    expect(grid).toHaveAttribute('aria-colcount', '2')
    expect(grid).toHaveAttribute('tabindex', '0')
    // Cells expose 1-based aria-colindex.
    const cells = within(bodyRows()[0]).getAllByRole('gridcell')
    expect(cells[0]).toHaveAttribute('aria-colindex', '1')
    expect(cells[1]).toHaveAttribute('aria-colindex', '2')
  })

  it('navigates cells with arrow keys (roving tabindex) and activates a row', async () => {
    const onRowClick = vi.fn()
    render(
      <DataTable
        columns={columns}
        data={make(4)}
        getRowId={(r) => r.id}
        onRowClick={onRowClick}
        empty={BENCH_EMPTY}
      />,
    )
    const grid = screen.getByRole('grid')
    // First navigation key enters the grid at the top-left cell.
    fireEvent.keyDown(grid, { key: 'ArrowDown' })
    await waitFor(() =>
      expect(document.activeElement).toHaveAttribute('aria-colindex', '1'),
    )
    expect(document.activeElement).toHaveAttribute('role', 'gridcell')
    // Right moves a column; Down moves a row.
    fireEvent.keyDown(grid, { key: 'ArrowRight' })
    await waitFor(() =>
      expect(document.activeElement).toHaveAttribute('aria-colindex', '2'),
    )
    fireEvent.keyDown(grid, { key: 'ArrowDown' })
    await waitFor(() =>
      expect(
        document.activeElement?.closest('tr')?.getAttribute('aria-rowindex'),
      ).toBe('3'),
    )
    // Enter on the active cell activates the row.
    fireEvent.keyDown(grid, { key: 'Enter' })
    expect(onRowClick).toHaveBeenCalledTimes(1)
    expect(onRowClick).toHaveBeenCalledWith(
      expect.objectContaining({ id: '1' }),
    )
  })

  it('Ctrl+Home / Ctrl+End jump to the grid corners', async () => {
    render(
      <DataTable
        columns={columns}
        data={make(6)}
        getRowId={(r) => r.id}
        empty={BENCH_EMPTY}
      />,
    )
    const grid = screen.getByRole('grid')
    fireEvent.keyDown(grid, { key: 'ArrowDown' }) // enter at {0,0}
    fireEvent.keyDown(grid, { key: 'End', ctrlKey: true })
    await waitFor(() => {
      expect(document.activeElement).toHaveAttribute('aria-colindex', '2')
      expect(
        document.activeElement?.closest('tr')?.getAttribute('aria-rowindex'),
      ).toBe('7') // last of 6 body rows (header = 1)
    })
    fireEvent.keyDown(grid, { key: 'Home', ctrlKey: true })
    await waitFor(() => {
      expect(document.activeElement).toHaveAttribute('aria-colindex', '1')
      expect(
        document.activeElement?.closest('tr')?.getAttribute('aria-rowindex'),
      ).toBe('2')
    })
  })
})

describe('DataTable — controlled row selection', () => {
  it('selects rows and all currently visible rows without activating them', async () => {
    const onRowClick = vi.fn()
    const user = userEvent.setup()
    render(<SelectionHarness onRowClick={onRowClick} />)

    const grid = screen.getByRole('grid')
    expect(grid).toHaveAttribute('aria-colcount', '3')

    await user.click(screen.getByRole('checkbox', { name: 'Select row 1' }))
    expect(screen.getByTestId('selected-ids')).toHaveTextContent('1')
    expect(bodyRows()[1]).toHaveAttribute('aria-selected', 'true')
    expect(onRowClick).not.toHaveBeenCalled()

    const selectAll = screen.getByRole('checkbox', {
      name: 'Select all visible rows',
    })
    expect(selectAll).toHaveAttribute('data-state', 'indeterminate')
    await user.click(selectAll)
    expect(screen.getByTestId('selected-ids')).toHaveTextContent('0,1,2')
  })

  it('limits select-all to filtered rows', async () => {
    const user = userEvent.setup()
    render(<SelectionHarness searchable />)

    await user.type(screen.getByRole('textbox', { name: /search/i }), 'row-1')
    await user.click(
      screen.getByRole('checkbox', { name: 'Select all visible rows' }),
    )

    expect(screen.getByTestId('selected-ids')).toHaveTextContent(/^1$/)
  })

  it('toggles the selection checkbox with Space in grid navigation', async () => {
    render(<SelectionHarness />)
    const grid = screen.getByRole('grid')

    fireEvent.keyDown(grid, { key: 'ArrowDown' })
    await waitFor(() =>
      expect(document.activeElement).toHaveAttribute('aria-colindex', '1'),
    )
    fireEvent.keyDown(grid, { key: ' ' })

    expect(screen.getByTestId('selected-ids')).toHaveTextContent('0')
    expect(bodyRows()[0]).toHaveAttribute('aria-selected', 'true')
  })
})

//. A table with zero rows has TWO causes and they are NOT the same screen.
// Before this, both painted the generic "No results": on a clean install — where
// every list is empty — that string WAS the customer's first screen on 69 surfaces.
// Making `empty` required fixes the first cause; if the caller's copy then painted
// on the SECOND cause too, "No agents enrolled yet" would appear over three records
// that exist — a poor string swapped for a false one. Both directions are asserted
// here, because a test that cannot tell the two apart is worth nothing.
describe('DataTable — zero rows has two causes', () => {
  const CALLER_EMPTY = (
    <EmptyState
      title="No agents enrolled yet"
      description="Install the CLI on a workstation to enrol its first agent."
    />
  )
  const GENERIC = 'No results'
  const search = () => screen.getByRole('textbox', { name: /search/i })

  it('paints the CALLER copy when nothing is loaded and no filter is typed', () => {
    render(
      <DataTable columns={columns} data={[]} empty={CALLER_EMPTY} searchable />,
    )
    expect(screen.getByText('No agents enrolled yet')).toBeInTheDocument()
    expect(screen.queryByText(GENERIC)).not.toBeInTheDocument()
  })

  it('paints the CALLER copy when nothing is loaded even with a filter typed', async () => {
    const user = userEvent.setup()
    render(
      <DataTable columns={columns} data={[]} empty={CALLER_EMPTY} searchable />,
    )
    await user.type(search(), 'anything')
    // The filter is not WHY it is empty — there was never anything to filter.
    expect(screen.getByText('No agents enrolled yet')).toBeInTheDocument()
    expect(screen.queryByText(GENERIC)).not.toBeInTheDocument()
  })

  it('paints the GENERIC copy when rows exist but the filter matches none', async () => {
    const user = userEvent.setup()
    render(
      <DataTable
        columns={columns}
        data={make(3)}
        getRowId={(r) => r.id}
        empty={CALLER_EMPTY}
        searchable
      />,
    )
    expect(bodyRows().length).toBe(3)

    await user.type(search(), 'zzz-matches-nothing')
    await waitFor(() => expect(bodyRows().length).toBe(0))

    expect(screen.getByText(GENERIC)).toBeInTheDocument()
    // THE lie this branch exists to prevent: three rows are loaded, so claiming
    // the estate has no agents would be false.
    expect(screen.queryByText('No agents enrolled yet')).not.toBeInTheDocument()
  })

  it('returns to the rows when the filter is cleared — neither state lingers', async () => {
    const user = userEvent.setup()
    render(
      <DataTable
        columns={columns}
        data={make(3)}
        getRowId={(r) => r.id}
        empty={CALLER_EMPTY}
        searchable
      />,
    )
    await user.type(search(), 'zzz-matches-nothing')
    await waitFor(() => expect(screen.getByText(GENERIC)).toBeInTheDocument())

    await user.clear(search())
    await waitFor(() => expect(bodyRows().length).toBe(3))
    expect(screen.queryByText(GENERIC)).not.toBeInTheDocument()
    expect(screen.queryByText('No agents enrolled yet')).not.toBeInTheDocument()
  })
})

describe('DataTable — ADM-CORE-07 virtualization at scale', () => {
  it('handles a 100k-row page WITHOUT materialising 100k DOM rows, fast', () => {
    const data = make(100_000)
    const t0 = performance.now()
    render(
      <DataTable
        columns={columns}
        data={data}
        getRowId={(r) => r.id}
        virtualized
        maxBodyHeight={600}
        empty={BENCH_EMPTY}
      />,
    )
    const elapsed = performance.now() - t0

    const grid = screen.getByRole('grid')
    expect(grid).toHaveAttribute('aria-rowcount', '100001')

    // The DOM does NOT contain 100k rows (under jsdom's null layout the window
    // collapses to spacers; in a real browser it is the visible slice). THIS row
    // ceiling is the virtualization guard — a non-virtualized render of 100k
    // rows fails it structurally, on any machine.
    expect(grid.querySelectorAll('tr').length).toBeLessThan(100)
    // The wall-clock bound is a secondary catastrophe detector, not a perf
    // budget. 60s, not 2s: the suite now runs in the pre-push gate on a
    // shared container where THIS correct render was measured at 25.7s under
    // parallel-session load (12x degradation) — any tight bound is machine
    // noise there. A genuinely broken virtualization materialises 100k jsdom
    // rows and takes minutes, so the loose bound still catches it.
    expect(elapsed).toBeLessThan(60_000)
  })

  it('auto-virtualizes above the threshold and not below', () => {
    const { rerender } = render(
      <DataTable
        columns={columns}
        data={make(20)}
        getRowId={(r) => r.id}
        virtualizeThreshold={50}
        empty={BENCH_EMPTY}
      />,
    )
    expect(bodyRows().length).toBe(20)

    rerender(
      <DataTable
        columns={columns}
        data={make(5_000)}
        getRowId={(r) => r.id}
        virtualizeThreshold={50}
        empty={BENCH_EMPTY}
      />,
    )
    expect(
      screen.getByRole('grid').querySelectorAll('tbody tr').length,
    ).toBeLessThan(100)
    expect(screen.getByRole('grid')).toHaveAttribute('aria-rowcount', '5001')
  })
})
