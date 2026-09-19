// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { cleanup, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it } from 'vitest'
import { FillingTable } from './filling-table'

afterEach(cleanup)

function rows(n: number) {
  return (
    <>
      <thead>
        <tr>
          <th>Name</th>
          <th>State</th>
        </tr>
      </thead>
      <tbody>
        {Array.from({ length: n }, (_, i) => (
          <tr key={i}>
            <td>row {i}</td>
            <td>active</td>
          </tr>
        ))}
      </tbody>
    </>
  )
}

describe('FillingTable — the region takes the height, never the rows', () => {
  // A `min-height` on the <table> is distributed over its body rows: three rows in a
  // 1440×900 frame measured 235 px each and a single row measured 704, against a 36 px
  // budget. The height belongs to the region; the row group takes the surplus.
  it('puts the height on the region and not on the table element', () => {
    render(
      <FillingTable fill colSpan={2} nextAction={<a href="#new">New thing</a>}>
        {rows(3)}
      </FillingTable>,
    )
    const table = screen.getByRole('table')
    expect(table.className).not.toMatch(/min-h-/)
    const region = table.closest('[data-slot="table-region"]')!
    expect(region.className).toMatch(/min-h-\[calc\(100svh-8rem\)\]/)
    expect(region.className).toMatch(/\bflex\b/)
    expect(table.className).toMatch(/flex-1/)
  })

  it('gives the surplus to the foot, so every body row keeps the table density', () => {
    render(
      <FillingTable fill colSpan={2} nextAction={<a href="#new">New thing</a>}>
        {rows(3)}
      </FillingTable>,
    )
    const table = screen.getByRole('table')
    // `height: 100%` on the tfoot ELEMENT is the one shape Chromium gives the surplus
    // to without stretching a body row (measured, four shapes, same fixture).
    expect(table.querySelector('tfoot')!.className).toMatch(/\bh-full\b/)
    for (const row of table.querySelectorAll('tbody tr')) {
      expect(row.className).not.toMatch(/h-\[|min-h-|flex-1/)
    }
    // The cells carry the density the primitive owns, and nothing overrides it.
    expect(table.className).toMatch(/\[&_tbody_td\]:h-9/)
  })

  it('carries the quiet next-action line at the bottom of the foot', () => {
    render(
      <FillingTable fill colSpan={2} nextAction={<a href="#new">New thing</a>}>
        {rows(1)}
      </FillingTable>,
    )
    const link = screen.getByRole('link', { name: 'New thing' })
    const cell = link.closest('td')!
    expect(cell.closest('tfoot')).toBeTruthy()
    expect(cell.getAttribute('colspan')).toBe('2')
    expect(cell.className).toMatch(/align-bottom/)
  })

  it('still fills when the principal may not act, with plain table surface', () => {
    render(
      <FillingTable fill colSpan={2}>
        {rows(1)}
      </FillingTable>,
    )
    const table = screen.getByRole('table')
    expect(table.querySelector('tfoot')).toBeTruthy()
    expect(table.querySelector('tfoot')!.className).toMatch(/\bh-full\b/)
    expect(screen.queryByRole('link')).toBeNull()
  })

  it('does not take the viewport when the table shares its region', () => {
    render(
      <FillingTable fill={false} colSpan={2}>
        {rows(2)}
      </FillingTable>,
    )
    const table = screen.getByRole('table')
    const region = table.closest('[data-slot="table-region"]')!
    expect(region.className).not.toMatch(/min-h-/)
    expect(table.className).not.toMatch(/flex-1/)
    // No verb and no height to absorb: there is nothing for a foot to do.
    expect(table.querySelector('tfoot')).toBeNull()
  })

  it('renders what the region carries under the table', () => {
    render(
      <FillingTable
        fill
        colSpan={2}
        after={<button type="button">Load more</button>}
      >
        {rows(2)}
      </FillingTable>,
    )
    const more = screen.getByRole('button', { name: 'Load more' })
    expect(more.closest('table')).toBeNull()
    expect(more.closest('[data-slot="table-region"]')).toBeTruthy()
  })
})
