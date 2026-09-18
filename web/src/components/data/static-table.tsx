// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import type { ComponentProps } from 'react'
import { cn } from '@/lib/utils'

/**
 * StaticTable — the small, whole, already-loaded table.
 *
 * ⛔ WHY THIS EXISTS AND WHY IT IS NOT `DataTable`. The console has two kinds of table
 *    and until this pass only one of them had a primitive. `DataTable` is a GRID over a
 *    paginated estate: a toolbar, a search box, sorting, selection, row navigation with
 *    arrow keys, virtualisation, a truncation notice, `has_more`. The other kind is a
 *    role's permission matrix, a licence's entitlements, a plan's line items — a handful
 *    of rows the engine returned whole, with nothing to sort, page or select. Rendering
 *    those through `DataTable` would hand an operator a search box over four rows and a
 *    grid role over something that is not a grid; that is the cargo cult, not the fix.
 *
 * ⛔ WHAT IT REPLACES, measured on 2026-09-18 over `web/src/features`: 57 hand-rolled
 *    `<table>` elements in 37 files, and under them the drift that makes one screen read as a
 *    different product from the next —
 *      · 100 `<th className="px-3 py-2 font-medium">` against 23 `py-2 pr-4`, 13 `p-2`,
 *        9 `px-2.5 py-2` and 3 `py-1.5 pr-3`: FIVE densities for one row of headings;
 *      · 27 `<thead>` on `bg-muted/40` in sentence case, beside `DataTable`'s own header
 *        on `bg-muted` in uppercase with `tracking-wide` — so the two tables on one
 *        screen announce themselves differently;
 *      · `w-full text-body`, `w-full border-collapse text-body`, `w-full text-left
 *        text-caption` — three table shells.
 *    This primitive keeps `DataTable`'s OWN decisions (the same header treatment, the
 *    same hairline, the same cell padding) so the two tables on a screen are the same
 *    table, one of them simply smaller.
 *
 * ⛔ AND WHY IT STYLES ITS CELLS BY DESCENDANT SELECTOR rather than exporting a `<Th>`
 *    and a `<Td>`. 100 `<th>` and 74 `<td>` call sites is a migration with 174 chances
 *    to change a colSpan, a key or a handler by hand. The shell owns the density, the
 *    call sites keep their own `<th>`/`<td>`, and the diff is the wrapper plus the
 *    classes the shell now owns. A cell that needs something else still says so with a
 *    class of its own — `text-right`, `font-mono`, `align-top` — because those are
 *    ALIGNMENT and FACE, not density.
 */
export function StaticTable({ className, ...props }: ComponentProps<'table'>) {
  return (
    <table
      className={cn(
        'w-full border-collapse text-body',
        // The header, identical to `DataTable`'s (data-table.tsx:822).
        '[&_thead_th]:bg-muted [&_thead_th]:px-3 [&_thead_th]:py-2',
        '[&_thead_th]:text-left [&_thead_th]:align-middle',
        '[&_thead_th]:text-caption [&_thead_th]:font-medium [&_thead_th]:tracking-wide',
        '[&_thead_th]:text-muted-foreground [&_thead_th]:uppercase',
        '[&_thead_tr]:border-b [&_thead_tr]:border-border-strong',
        // The body: one density, one hairline, and none under the last row.
        '[&_tbody_td]:px-3 [&_tbody_td]:py-2 [&_tbody_td]:align-middle',
        '[&_tbody_th]:px-3 [&_tbody_th]:py-2 [&_tbody_th]:text-left',
        '[&_tbody_tr]:border-b [&_tbody_tr]:border-border',
        '[&_tbody_tr:last-child]:border-0',
        className,
      )}
      {...props}
    />
  )
}
