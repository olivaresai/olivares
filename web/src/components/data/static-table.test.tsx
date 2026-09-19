// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { describe, expect, it } from 'vitest'
import { render, screen } from '@testing-library/react'
import { DENSITY_ROW, densityHeightToken } from '@/stores/preferences'
import { StaticTable } from './static-table'

/**
 * The console has TWO table primitives, and a reader cannot tell them apart on
 * screen: `/models` and `/platforms` paint static tables beside data tables. So they
 * owe the reader one row height. A browser probe measured the static one at 39 px
 * against the data one's 36 px — the 3 px came from `py-2` with no height token at
 * all, and it applied to all 39 files that render a static table.
 *
 * These tests pin the AGREEMENT rather than the number: the static table's height
 * class is read back out of the rendered markup and compared against the token
 * `DENSITY_ROW.comfortable` declares. Changing either primitive alone fails here.
 */
function body() {
  render(
    <StaticTable>
      <tbody>
        <tr>
          <th scope="row">Family</th>
          <td>claude-opus</td>
        </tr>
      </tbody>
    </StaticTable>,
  )
  return screen.getByRole('table')
}

describe('StaticTable density', () => {
  it('gives its body cells the height the data table calls comfortable', () => {
    const classes = body().className.split(/\s+/)
    const token = densityHeightToken('comfortable')
    expect(classes).toContain(`[&_tbody_td]:${token}`)
    expect(classes).toContain(`[&_tbody_th]:${token}`)
  })

  it('gives its header the strip height the data table paints', () => {
    // The two headers are one header, and until this was pinned they differed by a
    // pixel: `py-2` plus an 18 px caption line is 34, plus the rule 35, while the data
    // table's header carried the ROW token and measured 36. One pixel on `/audit`'s
    // first ledger row is the whole of a 136 px budget met or missed.
    const classes = body().className.split(/\s+/)
    expect(classes).toContain(
      `[&_thead_th]:${DENSITY_ROW.comfortable.headClassName}`,
    )
  })

  it('pads to that height instead of past it', () => {
    // `py-2` is what made the row 39: 16 px of padding on top of a 22 px line box
    // overshoots a 36 px row, and a height token cannot pull a cell back DOWN.
    const classes = body().className.split(/\s+/)
    expect(classes).toContain('[&_tbody_td]:py-1')
    expect(classes).not.toContain('[&_tbody_td]:py-2')
  })
})

/**
 * A ROW THAT HOLDS A CONTROL IS STILL A 36 PX ROW.
 *
 * Measured in a real browser against the seeded estate: `/residency` 36.5 px,
 * `/console` 36.5, `/reporting` 37, `/security` 37, `/backups` 40.5 — and every one of
 * them came back to exactly 36 when the cell holding the button was emptied, while
 * emptying any text cell beside it changed nothing. The cell is 36 px with `py-1`,
 * which leaves 28 px of content box, and a 28 px button plus the half-leading of its
 * line does not fit in 28. The padding is the part with no work to do.
 *
 * jsdom has no layout engine, so what is pinned here is the RULE, not the pixel: the
 * pixel is measured by the browser probe in the assessment directory.
 */
describe('StaticTable rows that hold a control', () => {
  it('takes the vertical padding off a cell that holds one', () => {
    const classes = body().className.split(/\s+/)
    expect(classes).toContain('[&_tbody_td:has(button)]:py-0')
    expect(classes).toContain('[&_tbody_td:has(a)]:py-0')
  })

  it('covers the other two controls a row can hold', () => {
    const classes = body().className.split(/\s+/)
    expect(classes).toContain('[&_tbody_td:has(input)]:py-0')
    expect(classes).toContain('[&_tbody_td:has(select)]:py-0')
  })

  it('offers the same escape to a cell that holds a drawn box', () => {
    // `/team-costs` paints a 32 px sparkline in a cell and measured 40.5 px a row. It
    // is an attribute rather than a class because this primitive styles its cells by
    // descendant selector: `className="py-0"` on the cell has lower specificity than
    // `[&_tbody_td]:py-1` and loses without saying so.
    const classes = body().className.split(/\s+/)
    expect(classes).toContain('[&_tbody_td[data-cell="box"]]:py-0')
  })

  it('leaves the padding on a cell that holds only text', () => {
    // The rule is conditional on the control, and that is the half of it that keeps
    // ordinary rows looking like rows: `[&_tbody_td]:py-1` is still there.
    const classes = body().className.split(/\s+/)
    expect(classes).toContain('[&_tbody_td]:py-1')
  })
})
