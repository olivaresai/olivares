// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useCallback, useEffect, useRef } from 'react'

/**
 * useReturnFocus — give focus back to whatever opened a CONTROLLED overlay.
 *
 * Radix restores focus to its own `Trigger`, and these overlays have none: the
 * sheet and the cursor dialog are opened from application state (a row, a button,
 * a deep link), not from a `SheetTrigger`. MEASURED in the browser (K3 I2 run 9):
 * after Escape closed the administrative sheet, `document.activeElement` was
 * `<body>` — the operator's place in the page was gone, which is exactly what the
 * increment's contract forbids ("return, and focus to the origin").
 *
 * The origin is remembered by watching `focusin` while the overlay is CLOSED, and
 * ignoring anything inside an overlay. That is deliberate rather than reading
 * `document.activeElement` when `open` flips: child effects run before parent
 * effects, so by the time a parent could look, Radix has already moved focus into
 * the overlay. Watching the document cannot lose that race.
 *
 * Returns the `onCloseAutoFocus` handler for the content element. If the origin is
 * gone from the document — a row that a re-read removed — it does nothing and lets
 * Radix apply its own default rather than focus a detached node.
 */
export function useReturnFocus(open: boolean): (event: Event) => void {
  const origin = useRef<HTMLElement | null>(null)

  useEffect(() => {
    if (open) return
    const remember = (event: FocusEvent) => {
      const target = event.target
      if (!(target instanceof HTMLElement)) return
      // Never remember a place INSIDE an overlay: that is where focus is going,
      // not where it came from.
      if (target.closest('[role="dialog"], [role="alertdialog"]')) return
      origin.current = target
    }
    document.addEventListener('focusin', remember)
    return () => document.removeEventListener('focusin', remember)
  }, [open])

  return useCallback((event: Event) => {
    const element = origin.current
    if (!element || !element.isConnected) return
    event.preventDefault()
    element.focus()
  }, [])
}
