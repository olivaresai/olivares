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
/**
 * `oneLine` — every cell is one line and the table widens instead of growing rows.
 *
 * ⛔ WHY A PROP AND NOT THE DEFAULT, which is the opposite of what `DataTable` does with
 *    the same rule. A `DataTable` is a management list: its cells are values. A
 *    `StaticTable` is whatever a screen needed a small table for, and 57 call sites came
 *    from hand-rolled `<table>`s — some of them hold a sentence per row (a permission's
 *    description, a policy's effect). Making those nowrap would widen the table past its
 *    container, and the containers are not all scrollers, so a prose table would push the
 *    PAGE sideways. Measured where it matters instead: `/models` paints its pricing row
 *    at 89 px because the cells wrap, and it opts in.
 */
export function StaticTable({
  className,
  oneLine,
  ...props
}: ComponentProps<'table'> & { oneLine?: boolean }) {
  return (
    <table
      // The same handle `DataTable` carries, so one probe can measure BOTH kinds of
      // table on a screen. Before this, a measurement of "the console's row heights"
      // silently skipped `/models`, whose two tables are static ones.
      data-slot="static-table"
      className={cn(
        'w-full border-collapse text-body',
        // The header, identical to `DataTable`'s (data-table.tsx:822) — including its
        // HEIGHT, which used to be left to the padding and the caption line and came out
        // at 34 + the 1 px rule, one pixel short of the strip `DataTable` paints. The
        // literal is `DENSITY_ROW.comfortable.headClassName` and `static-table.test.tsx`
        // pins it there, for the same reason `h-9` below is pinned: Tailwind only emits a
        // class whose whole name it reads in the source.
        '[&_thead_th]:bg-muted [&_thead_th]:h-[35px] [&_thead_th]:px-3 [&_thead_th]:py-2',
        '[&_thead_th]:text-left [&_thead_th]:align-middle',
        '[&_thead_th]:text-caption [&_thead_th]:font-medium [&_thead_th]:tracking-wide',
        '[&_thead_th]:text-muted-foreground [&_thead_th]:uppercase',
        '[&_thead_tr]:border-b [&_thead_tr]:border-border-strong',
        // The body: one density, one hairline, and none under the last row.
        //
        // The height is `DataTable`'s comfortable density, not a second opinion about
        // it. It was `py-2` with no height at all, which measures 39 px against the
        // 36 px the design budgets for a management table row — a 3 px overrun on every
        // static table in the console, `/models` and `/platforms` among the routes the
        // browser probe caught it on.
        //
        // The literal `h-9` is deliberate, and `static-table.test.tsx` pins it equal to
        // `DENSITY_ROW.comfortable` so the two primitives cannot drift: Tailwind only
        // generates a class whose whole name it reads in the source, so a token built by
        // interpolation would compile to no rule and the row would silently keep its
        // old height.
        '[&_tbody_td]:h-9 [&_tbody_td]:px-3 [&_tbody_td]:py-1 [&_tbody_td]:align-middle',
        '[&_tbody_th]:h-9 [&_tbody_th]:px-3 [&_tbody_th]:py-1 [&_tbody_th]:text-left',
        // A CELL THAT HOLDS A CONTROL SPENDS NO PADDING ON IT, because the 36 px box
        // already reserves the room. Measured in the browser on the seeded estate:
        // `/residency` 36.5 px, `/console` 36.5, `/reporting` 37, `/security` 37 and
        // `/backups` 40.5 — and in every one of them the row came back to exactly 36 the
        // moment the cell holding the button was emptied, with the text cells beside it
        // making no difference at all.
        //
        // The arithmetic is the whole finding: the cell is 36 px with `py-1`, which
        // leaves 28 px of content box, and a `size="sm"` button is 28 px of BOX plus the
        // half-leading of the line it sits on — half a pixel more than fits, so the row
        // grows. An icon button (`size-8`, 32 px) overshoots by four. Neither is the
        // control's fault and neither is fixed by shrinking it: this padding is what has
        // no work to do. Verified by injecting exactly this rule into the live page —
        // the five rows above all measured 36, and `/inventory` and `/audit`, already at
        // 36, did not move.
        '[&_tbody_td:has(button)]:py-0 [&_tbody_td:has(a)]:py-0',
        '[&_tbody_td:has(input)]:py-0 [&_tbody_td:has(select)]:py-0',
        // AND THE SAME ESCAPE FOR A CELL THAT HOLDS A DRAWN BOX rather than a control —
        // a trend, a meter, a swatch. `/team-costs` paints a 32 px sparkline and measured
        // 40.5 px a row; with this attribute it measures 36.
        //
        // It is an ATTRIBUTE and not a class at the call site, and that is not a
        // preference: this primitive styles its cells by descendant selector, so
        // `className="py-0"` on a `<td>` has LOWER specificity than the shell's own
        // `[&_tbody_td]:py-1` and silently loses. A cell that needs to opt out has to say
        // so in a way that outranks the rule it is opting out of.
        '[&_tbody_td[data-cell="box"]]:py-0',
        '[&_tbody_tr]:border-b [&_tbody_tr]:border-border [&_tbody_tr]:transition-colors',
        '[&_tbody_tr]:hover:bg-surface',
        '[&_tbody_tr:last-child]:border-0',
        oneLine &&
          '[&_tbody_td]:whitespace-nowrap [&_tbody_th]:whitespace-nowrap',
        oneLine && '[&_thead_th]:whitespace-nowrap',
        className,
      )}
      {...props}
    />
  )
}
