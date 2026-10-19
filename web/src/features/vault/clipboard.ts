// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Copy a secret and take it back off the clipboard. A password left on the clipboard is
// read by whatever asks next, so the copy is undone after a short while.

/** How long a copied secret stays on the clipboard. */
export const CLIPBOARD_CLEAR_MS = 30_000

// What we put on the clipboard and have not yet taken back. It stays set until a clear
// succeeds or the clipboard is seen to hold something else, so a refused clear is still
// retried when the window returns and when the page is left.
let held: string | null = null
let timer: ReturnType<typeof setTimeout> | null = null
let retryOnFocus: (() => void) | null = null

/**
 * Put text on the clipboard and clear it after CLIPBOARD_CLEAR_MS. The clear leaves the
 * clipboard alone when it can see the person has copied something else since; when the
 * browser does not let a page read the clipboard it clears anyway, because a secret
 * left behind costs more than a lost copy. Only the latest copy is tracked. A clear the
 * browser refuses (the tab is in the background) is retried once when the window is
 * back, and once more when the page is left; those retries clear only what they can
 * read and see unchanged. Resolves false when the browser refused the copy.
 */
export async function copySecret(
  text: string,
  clearAfterMs: number = CLIPBOARD_CLEAR_MS,
): Promise<boolean> {
  const clipboard = navigator.clipboard
  if (!clipboard?.writeText) return false
  try {
    await clipboard.writeText(text)
  } catch {
    return false
  }
  if (timer) clearTimeout(timer)
  dropRetry()
  held = text
  timer = setTimeout(() => {
    timer = null
    void clearHeld(text, false)
  }, clearAfterMs)
  clearOnPageHide()
  return true
}

function dropRetry() {
  if (retryOnFocus) window.removeEventListener('focus', retryOnFocus)
  retryOnFocus = null
}

/**
 * Take `text` back off the clipboard if it is still the secret we put there. `careful`
 * is for the late attempts: they clear only what they can read and see unchanged.
 */
async function clearHeld(text: string, careful: boolean): Promise<void> {
  if (held !== text) return // a newer copy owns the clipboard now, or it is already clear
  try {
    const clipboard = navigator.clipboard
    if (clipboard.readText) {
      const current = await clipboard.readText().catch(() => null)
      if (held !== text) return // a newer copy took the clipboard while we were reading
      if (current !== null && current !== text) {
        held = null // the person copied something else: not ours any more
        return
      }
      if (current === null && careful) return // cannot verify: leave it alone
    } else if (careful) {
      return
    }
    await clipboard.writeText('')
    if (held === text) held = null
  } catch {
    if (careful) return
    // The tab is not focused, or permission is gone: try once more when the person is back.
    dropRetry()
    retryOnFocus = () => {
      retryOnFocus = null
      void clearHeld(text, true)
    }
    window.addEventListener('focus', retryOnFocus, { once: true })
  }
}

let pageHideHooked = false

/** Leaving the page is the last chance to take a secret back off the clipboard. */
function clearOnPageHide() {
  if (pageHideHooked) return
  pageHideHooked = true
  window.addEventListener('pagehide', () => {
    if (held !== null) void clearHeld(held, true)
  })
}
