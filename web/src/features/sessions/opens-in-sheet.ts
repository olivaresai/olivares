// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Its own module so a list's links can ask it without loading the sheet they open: the
// sheet carries the whole conversation renderer and is loaded on the first open.
import type { MouseEvent } from 'react'

/**
 * Whether a click on a session link opens it in the Estate sheet (#2549): a plain primary
 * click. A click with a modifier, or another button, keeps the link's own meaning (a new
 * tab, a new window), which goes to the full page.
 */
export function opensInSheet(e: MouseEvent): boolean {
  return (
    e.button === 0 &&
    !e.defaultPrevented &&
    !e.metaKey &&
    !e.ctrlKey &&
    !e.shiftKey &&
    !e.altKey
  )
}
