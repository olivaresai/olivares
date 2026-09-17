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
  act,
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { useState } from 'react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { EmptyState } from '@/components/ui/empty-state'
import { ApiError, NetworkError } from '@/lib/api/errors'
import { DENSITY_ROW, usePreferencesStore } from '@/stores/preferences'
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
const BENCH_EMPTY = (
  <EmptyState
    title="Nothing on this bench"
    description="Rows appear here once the read returns some."
  />
)

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
      expect.any(HTMLElement),
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

// ---------------------------------------------------------------------------
// Density (console-ui-layout-density, 2026-09-06). The Settings preference used to
// set `h-7`/`h-8` as a MINIMUM height on cells whose content was already taller, so
// it changed nothing visible. These pin that the preference now decides the header,
// body and skeleton row heights, the virtualizer's estimate for rows that are not
// in the DOM, and the sticky-header scroll padding — and that a change re-measures
// instead of remounting. Pixel truth is in the browser evidence.
describe('DataTable — density preference', () => {
  afterEach(() => {
    usePreferencesStore.setState({ density: 'comfortable' })
  })

  const wrapper = () =>
    document.querySelector('[data-slot="data-table"]') as HTMLElement
  const cellClasses = () =>
    screen.getAllByRole('gridcell').map((c) => c.className)
  const headerClasses = () =>
    screen.getAllByRole('columnheader').map((c) => c.className)

  it('applies the comfortable row height to header and body by default', () => {
    render(
      <DataTable
        columns={columns}
        data={make(3)}
        getRowId={(r) => r.id}
        empty={BENCH_EMPTY}
      />,
    )
    expect(wrapper()).toHaveAttribute('data-density', 'comfortable')
    const tokens = DENSITY_ROW.comfortable.className.split(' ')
    for (const c of cellClasses())
      expect(c.split(' ')).toEqual(expect.arrayContaining(tokens))
    for (const c of headerClasses())
      expect(c.split(' ')).toEqual(expect.arrayContaining(tokens))
    expect(headerClasses()[0].split(' ')).not.toContain('py-2')
  })

  it('switches every row to compact when the preference changes, without remounting', () => {
    render(
      <DataTable
        columns={columns}
        data={make(3)}
        getRowId={(r) => r.id}
        empty={BENCH_EMPTY}
      />,
    )
    const firstCell = screen.getAllByRole('gridcell')[0]
    act(() => usePreferencesStore.setState({ density: 'compact' }))
    expect(wrapper()).toHaveAttribute('data-density', 'compact')
    const compact = DENSITY_ROW.compact.className.split(' ')
    const comfortable = DENSITY_ROW.comfortable.className.split(' ')
    for (const c of [...cellClasses(), ...headerClasses()]) {
      expect(c.split(' ')).toEqual(expect.arrayContaining(compact))
      for (const token of comfortable) expect(c.split(' ')).not.toContain(token)
    }
    // Same element, re-rendered — not a new table.
    expect(screen.getAllByRole('gridcell')[0]).toBe(firstCell)
  })

  it('sizes the loading skeleton rows with the same height', () => {
    const { rerender } = render(
      <DataTable
        columns={columns}
        data={[]}
        isLoading
        getRowId={(r) => r.id}
        empty={BENCH_EMPTY}
      />,
    )
    const skeletonCells = () =>
      Array.from(screen.getByRole('grid').querySelectorAll('tbody td')).map(
        (td) => td.className.split(' '),
      )
    expect(skeletonCells().length).toBeGreaterThan(0)
    for (const c of skeletonCells())
      expect(c).toEqual(
        expect.arrayContaining(DENSITY_ROW.comfortable.className.split(' ')),
      )
    act(() => usePreferencesStore.setState({ density: 'compact' }))
    rerender(
      <DataTable
        columns={columns}
        data={[]}
        isLoading
        getRowId={(r) => r.id}
        empty={BENCH_EMPTY}
      />,
    )
    for (const c of skeletonCells())
      expect(c).toEqual(
        expect.arrayContaining(DENSITY_ROW.compact.className.split(' ')),
      )
  })

  it('keeps the empty state and reports the density around it', () => {
    render(
      <DataTable
        columns={columns}
        data={[]}
        getRowId={(r) => r.id}
        empty={BENCH_EMPTY}
      />,
    )
    expect(screen.getByText('Nothing on this bench')).toBeInTheDocument()
    act(() => usePreferencesStore.setState({ density: 'compact' }))
    expect(wrapper()).toHaveAttribute('data-density', 'compact')
    expect(screen.getByText('Nothing on this bench')).toBeInTheDocument()
  })

  it('estimates virtualized rows with the density height and re-estimates on change', () => {
    // jsdom reports every box as 0 × 0, which collapses the virtual window to
    // nothing (see the 100k test above). Simulate the two facts a browser supplies:
    // the scroll region is 640 px tall (the virtualizer reads offsetWidth/Height)
    // and each row measures exactly its density height (getBoundingClientRect).
    const originalRect = Element.prototype.getBoundingClientRect
    const originalOffsetHeight = Object.getOwnPropertyDescriptor(
      HTMLElement.prototype,
      'offsetHeight',
    )
    const originalOffsetWidth = Object.getOwnPropertyDescriptor(
      HTMLElement.prototype,
      'offsetWidth',
    )
    const isScroller = (el: Element) =>
      el instanceof HTMLElement && el.classList.contains('overflow-auto')
    const rect = (height: number) =>
      ({
        x: 0,
        y: 0,
        top: 0,
        left: 0,
        right: 800,
        bottom: height,
        width: 800,
        height,
        toJSON: () => ({}),
      }) as DOMRect
    Element.prototype.getBoundingClientRect = function (this: Element) {
      if (isScroller(this)) return rect(640)
      if (this.tagName === 'TR') {
        const density = this.closest('[data-density]')?.getAttribute(
          'data-density',
        ) as keyof typeof DENSITY_ROW | null
        return rect(density ? DENSITY_ROW[density].px : 0)
      }
      return originalRect.call(this)
    }
    Object.defineProperty(HTMLElement.prototype, 'offsetHeight', {
      configurable: true,
      get(this: HTMLElement) {
        return isScroller(this) ? 640 : 0
      },
    })
    Object.defineProperty(HTMLElement.prototype, 'offsetWidth', {
      configurable: true,
      get(this: HTMLElement) {
        return isScroller(this) ? 800 : 0
      },
    })
    try {
      exerciseVirtualDensity()
    } finally {
      Element.prototype.getBoundingClientRect = originalRect
      if (originalOffsetHeight)
        Object.defineProperty(
          HTMLElement.prototype,
          'offsetHeight',
          originalOffsetHeight,
        )
      if (originalOffsetWidth)
        Object.defineProperty(
          HTMLElement.prototype,
          'offsetWidth',
          originalOffsetWidth,
        )
    }
  })

  function exerciseVirtualDensity() {
    const data = make(1_000)
    render(
      <DataTable
        columns={columns}
        data={data}
        getRowId={(r) => r.id}
        virtualized
        empty={BENCH_EMPTY}
      />,
    )
    const tailSpacerHeight = () => {
      const spacers = Array.from(
        screen.getByRole('grid').querySelectorAll('tbody tr[aria-hidden]'),
      )
      const last = spacers[spacers.length - 1] as HTMLElement
      return parseFloat(last.style.height)
    }
    const lastRenderedIndex = () => {
      const rows = bodyRows()
      return Number(rows[rows.length - 1].getAttribute('aria-rowindex')) - 2
    }
    // Only a window is in the DOM; rows below it are the estimate, and the
    // estimate is the density height.
    const renderedComfortable = bodyRows().length
    expect(renderedComfortable).toBeGreaterThan(0)
    expect(renderedComfortable).toBeLessThan(100)
    let tail = 1_000 - lastRenderedIndex() - 1
    expect(tail).toBeGreaterThan(0)
    expect(tailSpacerHeight()).toBe(tail * DENSITY_ROW.comfortable.px)

    act(() => usePreferencesStore.setState({ density: 'compact' }))
    // Denser rows: the same 640 px window holds MORE rows, and the tail is
    // re-estimated with the compact height at once (measure()), not row by row.
    expect(bodyRows().length).toBeGreaterThan(renderedComfortable)
    tail = 1_000 - lastRenderedIndex() - 1
    expect(tailSpacerHeight()).toBe(tail * DENSITY_ROW.compact.px)
    for (const c of cellClasses())
      expect(c.split(' ')).toEqual(
        expect.arrayContaining(DENSITY_ROW.compact.className.split(' ')),
      )
  }

  it('moves only the table scroll region when the virtualizer leaves the active cell outside it', async () => {
    // Simulated layout (jsdom has none): a 640 px scroll region whose sticky header is
    // 40 px tall, rows measuring their density height, and — the defect — the cell the
    // keyboard lands on reported 20 px BELOW the region's bottom edge, then 10 px UNDER
    // the sticky header. Only `scrollTop` of the region may change; the document must
    // not be scrolled (no scrollIntoView).
    const originalRect = Element.prototype.getBoundingClientRect
    const originalOffsetHeight = Object.getOwnPropertyDescriptor(
      HTMLElement.prototype,
      'offsetHeight',
    )
    const originalOffsetWidth = Object.getOwnPropertyDescriptor(
      HTMLElement.prototype,
      'offsetWidth',
    )
    const isScroller = (el: Element) =>
      el instanceof HTMLElement && el.classList.contains('overflow-auto')
    let cellTop = 660
    const box = (top: number, height: number, width = 800) =>
      ({
        x: 0,
        y: top,
        top,
        left: 0,
        right: width,
        bottom: top + height,
        width,
        height,
        toJSON: () => ({}),
      }) as DOMRect
    Element.prototype.getBoundingClientRect = function (this: Element) {
      if (isScroller(this)) return box(0, 640)
      if (this.tagName === 'TH') return box(0, 40)
      if (this.tagName === 'TR') return box(0, DENSITY_ROW.comfortable.px)
      if (this.tagName === 'TD' && this === document.activeElement) {
        // The cell's viewport box follows the region's scroll position, as in a browser.
        const region = document.querySelector(
          '.overflow-auto',
        ) as HTMLElement | null
        const shift = (region?.scrollTop ?? 100) - 100
        return box(cellTop - shift, DENSITY_ROW.comfortable.px)
      }
      return originalRect.call(this)
    }
    Object.defineProperty(HTMLElement.prototype, 'offsetHeight', {
      configurable: true,
      get(this: HTMLElement) {
        return isScroller(this) ? 640 : 0
      },
    })
    Object.defineProperty(HTMLElement.prototype, 'offsetWidth', {
      configurable: true,
      get(this: HTMLElement) {
        return isScroller(this) ? 800 : 0
      },
    })
    const scrollIntoView = Element.prototype.scrollIntoView as ReturnType<
      typeof vi.fn
    >
    const scrollIntoViewCalls = scrollIntoView.mock.calls.length
    try {
      const user = userEvent.setup()
      render(
        <DataTable
          columns={columns}
          data={make(1_000)}
          getRowId={(r) => r.id}
          virtualized
          stickyHeader
          empty={BENCH_EMPTY}
        />,
      )
      const scroller = screen.getByRole('grid').parentElement as HTMLElement
      scroller.scrollTop = 100
      screen.getByRole('grid').focus()
      await user.keyboard('{ArrowDown}')
      await waitFor(() =>
        expect(document.activeElement?.getAttribute('role')).toBe('gridcell'),
      )
      // bottom 700 > region bottom 640 ⇒ the region scrolls down by the 60 px overflow.
      await waitFor(() => expect(scroller.scrollTop).toBe(160))
      // Now the next cell reports itself 10 px under the 40 px sticky header
      // (layout top 90 − current shift 60 = viewport top 30 < header bottom 40).
      cellTop = 90
      await user.keyboard('{ArrowDown}')
      await waitFor(() => expect(scroller.scrollTop).toBe(150))
      expect(scrollIntoView.mock.calls.length).toBe(scrollIntoViewCalls)
    } finally {
      Element.prototype.getBoundingClientRect = originalRect
      if (originalOffsetHeight)
        Object.defineProperty(
          HTMLElement.prototype,
          'offsetHeight',
          originalOffsetHeight,
        )
      if (originalOffsetWidth)
        Object.defineProperty(
          HTMLElement.prototype,
          'offsetWidth',
          originalOffsetWidth,
        )
    }
  })

  it('reserves the sticky header height for keyboard scroll positioning per density', () => {
    render(
      <DataTable
        columns={columns}
        data={make(3)}
        getRowId={(r) => r.id}
        stickyHeader
        empty={BENCH_EMPTY}
      />,
    )
    const scroller = screen.getByRole('grid').parentElement as HTMLElement
    expect(scroller).toHaveClass('scroll-pt-10')
    act(() => usePreferencesStore.setState({ density: 'compact' }))
    expect(scroller).toHaveClass('scroll-pt-8')
    expect(scroller).not.toHaveClass('scroll-pt-10')
  })
})

// UIQ1 D1 (2026-09-11): GET /v1/m/sessions/inbox/handoffs answers 503
// evidence_unavailable / NO_HE_PODIDO_MIRAR. The table's error branch is the
// real boundary the browser hit. Map THAT code as unknown; do not relabel every
// 503, and never paint empty / success / the raw verdict.
describe('DataTable — evidence_unavailable is unknown, not an unexpected 503', () => {
  const envelope = {
    code: 'evidence_unavailable',
    error: {
      code: 'evidence_unavailable',
      message: 'evidence_unavailable',
    },
    verdict: 'NO_HE_PODIDO_MIRAR',
  }
  const unavailable = new ApiError(
    503,
    'evidence_unavailable',
    'evidence_unavailable',
    'req-uiq1',
    {},
    envelope,
  )
  const retry = vi.fn()
  const mount = (error: unknown) =>
    render(
      <DataTable
        columns={columns}
        data={make(1)}
        empty={BENCH_EMPTY}
        error={error}
        onRetry={retry}
        getRowId={(row) => row.id}
      />,
    )

  it('keeps the unknown state, Retry, request id and alert; hides rows and the verdict', () => {
    mount(unavailable)
    const alert = screen.getByRole('alert')
    expect(alert).toHaveTextContent(/cannot currently be (verified|loaded)/i)
    expect(alert).not.toHaveTextContent(/unexpected error/i)
    expect(alert).not.toHaveTextContent('NO_HE_PODIDO_MIRAR')
    expect(screen.queryByText('Nothing on this bench')).not.toBeInTheDocument()
    expect(screen.queryByText('row-0')).not.toBeInTheDocument()
    expect(screen.getByText(/req-uiq1/)).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Retry' })).toBeInTheDocument()
  })

  it('does not relabel a generic 503 without that code', () => {
    mount(new ApiError(503, 'internal', 'boom', 'req-gen'))
    expect(screen.getByRole('alert')).toHaveTextContent(/unexpected error/i)
    expect(
      screen.queryByText(/cannot currently be (verified|loaded)/i),
    ).not.toBeInTheDocument()
  })

  it('keeps a transport failure as the network state', () => {
    mount(new NetworkError('socket closed'))
    expect(screen.getByRole('alert')).toHaveTextContent(
      /could not reach the control plane/i,
    )
    expect(screen.queryByText(/unexpected error/i)).not.toBeInTheDocument()
    expect(
      screen.queryByText(/cannot currently be (verified|loaded)/i),
    ).not.toBeInTheDocument()
  })

  it('keeps a 403 as a calm denial, never as unknown or unexpected', () => {
    mount(new ApiError(403, 'forbidden', 'no'))
    expect(screen.getByText(/not authorized/i)).toBeInTheDocument()
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()
    expect(screen.queryByText(/unexpected error/i)).not.toBeInTheDocument()
    expect(
      screen.queryByText(/cannot currently be (verified|loaded)/i),
    ).not.toBeInTheDocument()
  })
})

// ⛔ EL ESTADO NO ES UNA FILA, Y AHORA TAMPOCO OCUPA UNA. UI-T4 mueve el estado a un hermano
// en bloque DENTRO del mismo scrollport horizontal. Estas celdas fijan lo ESTRUCTURAL —dónde
// vive el nodo, cuántos hay y qué anuncia la rejilla— porque la geometría (ancho automático
// igual al scrollport, `sticky` a ambos extremos) sólo se puede medir en un navegador de
// verdad: jsdom no tiene motor de layout y daría verde a cualquier cosa.
describe('DataTable — the state is a sibling of the table, not a row in it', () => {
  const CALLER_EMPTY = (
    <EmptyState
      title="No agents enrolled yet"
      description="Rows appear here once the read returns some."
    />
  )
  const scrollport = () => screen.getByRole('grid').parentElement as HTMLElement
  const stateBlock = () =>
    document.querySelectorAll('[data-slot="data-table-state"]')

  const mount = (props: Partial<Parameters<typeof DataTable<Row>>[0]>) =>
    render(
      <DataTable
        columns={columns}
        data={make(2)}
        getRowId={(r) => r.id}
        empty={CALLER_EMPTY}
        {...props}
      />,
    )

  it('paints the error ONCE, after the table and inside the horizontal scrollport', () => {
    mount({ error: new NetworkError('socket closed') })
    const grid = screen.getByRole('grid')
    const blocks = stateBlock()
    expect(blocks).toHaveLength(1)
    const state = blocks[0]
    // Hermano de la tabla, no descendiente: si volviera a un `<td>` esto se rompe.
    expect(state.parentElement).toBe(grid.parentElement)
    // DESPUÉS de la tabla, que es el orden de lectura que pide la construcción.
    expect(
      grid.compareDocumentPosition(state) & Node.DOCUMENT_POSITION_FOLLOWING,
    ).toBeTruthy()
    // Y DENTRO del scrollport, no fuera: el mismo elemento que desplaza la cabecera.
    expect(scrollport().className).toContain('overflow-x-auto')
    expect(scrollport().contains(state)).toBe(true)
    expect(state).toContainElement(screen.getByRole('alert'))
  })

  it('no fabrica una fila de estado ni conserva las filas que el error oculta', () => {
    mount({ error: new NetworkError('socket closed') })
    const grid = screen.getByRole('grid')
    // El cuerpo queda VACÍO: ni la fila fabricada de antes ni las dos filas cargadas.
    expect(grid.querySelector('tbody')?.children).toHaveLength(0)
    expect(screen.queryByText('row-0')).not.toBeInTheDocument()
    expect(bodyRows()).toHaveLength(0)
    expect(grid).toHaveAttribute('aria-rowcount', '1')
  })

  it('el envoltorio NO es una segunda región viva; el estado conserva la suya', () => {
    mount({ error: new NetworkError('socket closed') })
    const state = stateBlock()[0]
    // El envoltorio no anuncia: duplicarlo haría que el lector oyera el estado dos veces.
    expect(state).not.toHaveAttribute('role')
    expect(state).not.toHaveAttribute('aria-live')
    // El componente de estado sí, exactamente como antes (ErrorState = alert).
    expect(within(state as HTMLElement).getByRole('alert')).toBeInTheDocument()
  })

  it('describe la rejilla con el estado vacío del llamante, y lo sitúa igual', () => {
    mount({ data: [] })
    const grid = screen.getByRole('grid')
    const state = stateBlock()[0]
    expect(state.parentElement).toBe(grid.parentElement)
    expect(grid.getAttribute('aria-describedby')).toBe(state.id)
    expect(state).toContainElement(screen.getByText('No agents enrolled yet'))
    expect(grid).toHaveAttribute('aria-rowcount', '1')
  })

  it('el vacío POR FILTRO sale por la misma costura, con la copy genérica', async () => {
    const user = userEvent.setup()
    mount({ data: make(3), searchable: true })
    await user.type(
      screen.getByRole('textbox', { name: /search/i }),
      'zzz-matches-nothing',
    )
    await waitFor(() => expect(stateBlock()).toHaveLength(1))
    const grid = screen.getByRole('grid')
    const state = stateBlock()[0]
    expect(state).toContainElement(screen.getByText('No results'))
    expect(state.parentElement).toBe(grid.parentElement)
    expect(grid.querySelector('tbody')?.children).toHaveLength(0)
    expect(grid).toHaveAttribute('aria-rowcount', '1')
  })

  it('CARGANDO gana al error: el esqueleto sigue en el cuerpo y no hay bloque de estado', () => {
    // La precedencia no cambió, y es lo que esta celda protege: pasar el estado a un
    // hermano habría sido la ocasión perfecta para pintarlo TAMBIÉN mientras carga.
    mount({ isLoading: true, error: new NetworkError('socket closed') })
    const grid = screen.getByRole('grid')
    expect(stateBlock()).toHaveLength(0)
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()
    expect(grid.querySelectorAll('tbody tr')).toHaveLength(6)
    expect(grid).not.toHaveAttribute('aria-describedby')
  })

  it('con filas no hay bloque de estado, y el recuento vuelve a describirlas', () => {
    mount({})
    expect(stateBlock()).toHaveLength(0)
    expect(screen.getByRole('grid')).toHaveAttribute('aria-rowcount', '3')
    expect(bodyRows()).toHaveLength(2)
  })
})
