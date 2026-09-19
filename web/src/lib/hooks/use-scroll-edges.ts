// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useEffect, useState } from 'react'

/**
 * A TABLE THAT DOES NOT FIT SAYS SO, and the fix is not a scroller.
 *
 * ⛔ WHAT THE REVIEW ACTUALLY PHOTOGRAPHED. It filed *"a table column is cut at the right
 *    viewport edge with no scroll affordance"* on `/models` (`EXTENDED THINK`), `/audit`
 *    (`TARGET`), `/killswitch` (`ENGAGED`) and `/platforms` (`ZDR`). Measured in Chromium
 *    against the seeded engine before this file existed: those tables DID scroll —
 *    `/platforms` had 584 px of reachable horizontal overflow, `/audit` 115 — and nothing
 *    on the screen said so. The defect is not a missing scroller. It is an operator who
 *    cannot know a column was cut, which is worse than one who has to scroll: they read a
 *    truncated table as the whole table.
 *
 * ⛔ WHY A MEASURED HINT AND NOT A CSS SCROLL SHADOW. `background-attachment: local`
 *    paints a shadow without JavaScript, and it composes with the element's own
 *    background — every one of these scrollers sits on `bg-surface` inside a bordered
 *    card, so the trick would either fight the card's background or the card's radius.
 *    A measured hint also knows which END it is at, which is the half that tells an
 *    operator whether they have seen the last column.
 *
 * It is `aria-hidden` and `pointer-events-none` on purpose: the grid's own arrow keys
 * reach every cell and a screen reader reads whole rows, so this is information for a
 * pointer user and must never become a control a keyboard has to walk past.
 */
export interface ScrollEdges {
  left: boolean
  right: boolean
}

/**
 * Track which side of a horizontal scroller has more to show.
 *
 * `deps` exists because the content, not the element, is what changes: a table that
 * loads 60 rows or switches density keeps the same node and needs a new measurement.
 */
export function useScrollEdges(
  ref: { current: HTMLElement | null },
  deps: readonly unknown[] = [],
): ScrollEdges {
  const [edges, setEdges] = useState<ScrollEdges>({ left: false, right: false })
  useEffect(() => {
    const el = ref.current
    if (!el) return
    const measure = () => {
      const max = el.scrollWidth - el.clientWidth
      const next =
        max <= 1
          ? { left: false, right: false }
          : { left: el.scrollLeft > 1, right: el.scrollLeft < max - 1 }
      // Only on a real change: this runs on every scroll frame, and a setState per frame
      // would re-render the table under the operator's finger.
      setEdges((prev) =>
        prev.left === next.left && prev.right === next.right ? prev : next,
      )
    }
    measure()
    el.addEventListener('scroll', measure, { passive: true })
    const ro = new ResizeObserver(measure)
    ro.observe(el)
    return () => {
      el.removeEventListener('scroll', measure)
      ro.disconnect()
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [ref, ...deps])
  return edges
}
