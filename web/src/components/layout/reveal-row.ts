// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

/**
 * Brings a row into view by moving the scroll container that holds it the least amount
 * that shows it, and nothing when it is already inside — the same move `DataTable` and the
 * tab strip make.
 *
 * ⛔ NEVER `scrollIntoView` HERE. Chromium makes the element it scrolls the starting point
 *    of sequential focus navigation, and this runs on every page load: the first Tab then
 *    began inside the sidebar, on the footer, and skipped the skip link that WCAG 2.4.1
 *    makes the first stop. Measured in a browser on `/` and `/inventory` (#1181): the first
 *    Tab reached "Toggle theme" with it, and "Skip to content" without it.
 *
 * Returns `false` when the row has no box yet (a closed panel, a drawer not laid out), so
 * the caller can try again on a later render instead of treating the row as revealed.
 */
export function revealRow(row: HTMLElement): boolean {
  const at = row.getBoundingClientRect()
  if (at.height === 0) return false
  // The nearest ancestor that really scrolls: an `overflow-x` clip makes `overflow-y`
  // compute to `auto` too, and that box holds everything it has, so it is not the one.
  let box = row.parentElement
  while (
    box &&
    !(
      /auto|scroll/.test(getComputedStyle(box).overflowY) &&
      box.scrollHeight > box.clientHeight
    )
  )
    box = box.parentElement
  if (!box) return true
  const inner = box.getBoundingClientRect()
  if (at.bottom > inner.bottom) box.scrollTop += at.bottom - inner.bottom
  else if (at.top < inner.top) box.scrollTop -= inner.top - at.top
  return true
}
