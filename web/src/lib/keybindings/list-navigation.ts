// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// BARE `j`/`k` AS ROW MOVEMENT. The shell does not duplicate a list's
// keyboard logic: it focuses the page's first grid or row list and re-dispatches the
// arrow the letter stands for, so the grid's own handler — clamping, Enter-to-open,
// everything — moves the row exactly as an arrow key would.

/**
 * The page's first focusable grid, skipping ones that are hidden or inert to
 * assistive tech, or null when there is no list to move.
 */
export function firstFocusableGrid(): HTMLElement | null {
  for (const grid of document.querySelectorAll<HTMLElement>(
    '[role="grid"], [data-list-keys]',
  )) {
    if (grid.closest('[hidden], [aria-hidden="true"], [inert]')) continue
    return grid
  }
  return null
}

/** True while any real menu is open — a list behind a dropdown must not move. */
function menuIsOpen(): boolean {
  return !!document.querySelector('[role="menu"]:not([hidden])')
}

/**
 * Focus the first grid and re-dispatch the arrow key the list letter stands for.
 *
 * True only when the grid actually received the key — the page has a grid, no
 * menu is open, and the focus landed inside it (a grid that never took focus
 * would have swallowed the key for nothing). The caller lets the event continue
 * on false rather than swallowing a dead key.
 */
export function moveFirstList(key: string): boolean {
  if (menuIsOpen()) return false
  const grid = firstFocusableGrid()
  if (!grid) return false
  grid.focus()
  if (!grid.contains(document.activeElement)) return false
  grid.dispatchEvent(
    new KeyboardEvent('keydown', { key, bubbles: true, cancelable: true }),
  )
  return true
}
