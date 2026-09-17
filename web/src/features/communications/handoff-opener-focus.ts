// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useCallback, useEffect, useRef } from 'react'

/**
 * Focus return for a dialog whose opener lives inside another overlay.
 *
 * `useReturnFocus` learns the origin by watching `focusin` while the overlay is
 * closed and deliberately ignores anything inside a dialog, so it cannot see the
 * button that opens the response dialog from the handoff sheet or the offer dialog
 * from the work item sheet. Those openers are captured explicitly, in the click
 * handler, and handed here.
 *
 * Restoration runs both on the close event and after the close has settled: the
 * overlay is still unmounting when `onCloseAutoFocus` fires, and other focus
 * management can run afterwards.
 */
export interface OpenerFocus {
  /** For `onCloseAutoFocus`; prevents the default only when it can focus. */
  onCloseAutoFocus: (event: Event) => void
}

function usable(
  element: HTMLElement | null | undefined,
): element is HTMLElement {
  if (!element || !element.isConnected) return false
  if (element.hasAttribute('disabled')) return false
  if (element.getAttribute('aria-hidden') === 'true') return false
  // `checkVisibility` is present in jsdom and browsers; treat its absence as
  // visible rather than refusing to restore focus at all.
  const check = (element as { checkVisibility?: () => boolean }).checkVisibility
  return typeof check === 'function' ? check.call(element) : true
}

export function useOpenerFocus({
  open,
  getOpener,
  getFallback,
}: {
  open: boolean
  /** The control that opened this dialog, captured at the click. */
  getOpener: () => HTMLElement | null
  /** A visible control in the owning view, used when the opener is gone. */
  getFallback?: () => HTMLElement | null
}): OpenerFocus {
  const wasOpen = useRef(open)
  // The getters are usually inline arrows, so they are held in refs: a `restore`
  // that changed identity every render would re-run the effect below and its
  // cleanup would cancel the pending restoration before it could fire.
  const openerRef = useRef(getOpener)
  const fallbackRef = useRef(getFallback)
  useEffect(() => {
    openerRef.current = getOpener
    fallbackRef.current = getFallback
  })

  const restore = useCallback((): HTMLElement | null => {
    const opener = openerRef.current()
    if (usable(opener)) {
      opener.focus()
      return opener
    }
    const fallback = fallbackRef.current?.() ?? null
    if (usable(fallback)) {
      fallback.focus()
      return fallback
    }
    return null
  }, [])

  useEffect(() => {
    const closing = wasOpen.current && !open
    wasOpen.current = open
    if (!closing) return
    // The overlay is still unmounting on this tick; re-assert once it has settled
    // and only if nothing else has taken focus meanwhile.
    const id = setTimeout(() => {
      const active = document.activeElement
      if (active && active !== document.body && active instanceof HTMLElement) {
        if (active.closest('[role="dialog"], [role="alertdialog"]') === null)
          return
      }
      restore()
    }, 0)
    return () => clearTimeout(id)
  }, [open, restore])

  return {
    onCloseAutoFocus: useCallback(
      (event: Event) => {
        if (restore()) event.preventDefault()
      },
      [restore],
    ),
  }
}
