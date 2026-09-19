// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import type { ReactNode } from 'react'
import { cn } from '@/lib/utils'
import { StaticTable } from './static-table'

/**
 * The REGION — the bordered rectangle a management table lives in, and the only place
 * its height is decided.
 *
 * ⛔ IT IS EXPORTED BECAUSE ZERO ROWS NEVER REACHED IT. At every other row count the
 *    region is `FillingTable`'s; an EMPTY page renders an `EmptyState` instead and went
 *    round it, so the one count where a dead half is CERTAIN was the one count the
 *    decision did not cover — measured in Chromium at 1440×900, a centred panel 220 px
 *    tall with no region at all, where the same screen's rows get 772 and reach the fold;
 *    with the region it measures 770 inside 772. The two screens now pass
 *    their empty page through this, so the height, the border and the radius are stated
 *    once. A second `min-h-[calc(...)]` written beside an empty state would drift from
 *    this one the first time either was corrected, which is the same reason the table's
 *    three decisions live in one piece.
 */
export function TableRegion({
  fill,
  className,
  children,
}: {
  /** Take the viewport. `false` for a table embedded in a tab beside other content. */
  fill: boolean
  className?: string
  children: ReactNode
}) {
  return (
    <div
      data-slot="table-region"
      className={cn(
        'overflow-hidden rounded-lg border border-border',
        fill && 'flex min-h-[calc(100svh-8rem)] flex-col',
        className,
      )}
    >
      {children}
    </div>
  )
}

/**
 * FillingTable — a management table whose REGION reaches the fold, and whose rows stay
 * 36 px while it does.
 *
 * ⛔ WHY IT EXISTS, MEASURED IN THE BROWSER ON THE SEEDED ESTATE. Two screens had put
 *    `min-h-[calc(100svh-8rem)]` on the `<table>` element itself so a short list would
 *    not leave half a 1440×900 viewport empty. A table does not keep surplus height: it
 *    DISTRIBUTES it over its body rows. So `/console?tab=agents` painted its three rows
 *    at **235 px each** and `/provider-profiles` painted its single row at **704** —
 *    against a 36 px budget. The region was full and every row in it was wrong.
 *
 * ⇒ The height moves off the table and onto the region, and a ROW GROUP takes the
 *   surplus instead of the rows. Measured in Chromium, four shapes, same fixture:
 *
 *   | shape                                   | table | body rows |
 *   |-----------------------------------------|-------|-----------|
 *   | `min-height` on the table               |   700 | 332 · 332 |
 *   | a `height:100%` spacer row in `tbody`    |   700 | 36 · 36 · 556 |
 *   | a `height:100%` row inside `tfoot`       |   700 | 324 · 324 |
 *   | **`height:100%` on the `tfoot` ELEMENT** |   700 | **36 · 36** |
 *
 *   Only the last one is both: the table's own surface covers the region — so the
 *   region is not an empty rectangle — and every body row measures exactly what the
 *   density says. The spacer row in `tbody` gets the arithmetic right and is still
 *   wrong: it is a `tbody tr`, so every measurement of "this screen's row height",
 *   ours and anyone else's, reads it as a 556 px row.
 *
 * ⛔ AND THE QUIET NEXT-ACTION LINE RIDES IN THAT SAME FOOT, at its bottom
 *    (`align-bottom`), because it is the one thing that belongs under a short list: what
 *    to do next. With rows enough to overflow, the foot collapses to its own height and
 *    the line sits under the last row, where it always was.
 *
 * ⛔ AND IT IS ONE PIECE FOR BOTH SCREENS. The height, the row group that absorbs it and
 *    the column span that keeps the foot aligned are three decisions that only work
 *    together; written twice they drift the first time one of them is corrected.
 */
export function FillingTable({
  fill,
  oneLine,
  colSpan,
  nextAction,
  after,
  className,
  children,
}: {
  /**
   * Take the viewport. `false` for a table embedded in a tab beside other content,
   * which divides a region it does not own: there the table is as tall as its rows.
   */
  fill: boolean
  /** `StaticTable`'s own prop: one line per cell, the table widens instead. */
  oneLine?: boolean
  /** The table's column count, so the foot spans the whole width. */
  colSpan: number
  /** One quiet line under the last row — the verb this screen exists for. Omitted
   *  when the principal may not act, and the foot is then plain table surface. */
  nextAction?: ReactNode
  /** Anything the region carries under the table: a "load more" bar, a note. */
  after?: ReactNode
  className?: string
  /** The table's own `<thead>` and `<tbody>`, written at the call site. */
  children: ReactNode
}) {
  return (
    <TableRegion fill={fill} className={className}>
      <StaticTable oneLine={oneLine} className={cn(fill && 'flex-1')}>
        {children}
        {fill || nextAction ? (
          <tfoot className={cn(fill && 'h-full')}>
            <tr>
              <td
                colSpan={colSpan}
                className="border-0 px-3 py-2 align-bottom text-caption text-muted-foreground"
              >
                {nextAction}
              </td>
            </tr>
          </tfoot>
        ) : null}
      </StaticTable>
      {after}
    </TableRegion>
  )
}
