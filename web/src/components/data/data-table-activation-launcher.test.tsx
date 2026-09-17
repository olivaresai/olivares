// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Focal seam: onRowClick's optional launcher is the activating node, not a later guess.
import {
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it, vi } from 'vitest'
import { EmptyState } from '@/components/ui/empty-state'
import { DataTable, type TableColumn } from './data-table'

interface Row {
  id: string
  name: string
}

const columns: TableColumn<Row, string>[] = [
  { accessorKey: 'name', header: 'Name' },
]

const EMPTY = <EmptyState title="Nothing on this bench" />

const rows: Row[] = [
  { id: 'r1', name: 'alpha' },
  { id: 'r2', name: 'beta' },
]

describe('DataTable row-activation launcher', () => {
  it('click delivers the focusable cell of the row where the event was born', async () => {
    const onRowClick = vi.fn()
    const user = userEvent.setup()
    render(
      <DataTable
        columns={columns}
        data={rows}
        getRowId={(r) => r.id}
        onRowClick={onRowClick}
        empty={EMPTY}
      />,
    )
    const name = await screen.findByText('beta')
    const cell = name.closest('td[role="gridcell"]') as HTMLElement
    expect(cell).toBeInstanceOf(HTMLElement)
    await user.click(name)
    expect(onRowClick).toHaveBeenCalledTimes(1)
    expect(onRowClick).toHaveBeenCalledWith(rows[1], cell)
  })

  it('click on the row itself falls back to that same row', () => {
    const onRowClick = vi.fn()
    render(
      <DataTable
        columns={columns}
        data={rows}
        getRowId={(r) => r.id}
        onRowClick={onRowClick}
        empty={EMPTY}
      />,
    )
    const row = within(screen.getByRole('grid').querySelector('tbody')!)
      .getAllByRole('row')
      .find((r) => r.getAttribute('aria-rowindex') === '2') as HTMLElement
    fireEvent.click(row)
    expect(onRowClick).toHaveBeenCalledTimes(1)
    expect(onRowClick.mock.calls[0][0]).toEqual(rows[0])
    expect(onRowClick.mock.calls[0][1]).toBe(row)
  })

  it('keyboard activation delivers the originating cell', async () => {
    const onRowClick = vi.fn()
    render(
      <DataTable
        columns={columns}
        data={rows}
        getRowId={(r) => r.id}
        onRowClick={onRowClick}
        empty={EMPTY}
      />,
    )
    const grid = screen.getByRole('grid')
    grid.focus()
    await userEvent.keyboard('{ArrowDown}')
    await waitFor(() => expect(document.activeElement?.tagName).toBe('TD'))
    const origin = document.activeElement as HTMLElement
    await userEvent.keyboard('{Enter}')
    expect(onRowClick).toHaveBeenCalledTimes(1)
    expect(onRowClick).toHaveBeenCalledWith(rows[0], origin)
  })
})
