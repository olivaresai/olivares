// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The keys of the terminal drawer, in one place: the drawer reads them from the page and the
// terminal reads them to leave them alone (a key the drawer owns is not typed into the
// shell). None is a key a browser or a shell uses: Mod+J (open or close the drawer) is the
// work surface's own binding.

export type TerminalShortcut = 'new' | 'previous' | 'next' | 'close' | 'search'

export function terminalShortcut(e: KeyboardEvent): TerminalShortcut | null {
  if (e.ctrlKey && e.shiftKey && !e.altKey && e.code === 'Backquote')
    return 'new'
  if (e.ctrlKey && e.shiftKey && !e.altKey && e.code === 'KeyF') return 'search'
  if (e.altKey && e.shiftKey && !e.ctrlKey && !e.metaKey) {
    if (e.code === 'ArrowLeft') return 'previous'
    if (e.code === 'ArrowRight') return 'next'
    if (e.code === 'KeyW') return 'close'
  }
  return null
}

/** Copy the selection without sending Ctrl+C to the shell. */
export function isCopyChord(e: KeyboardEvent): boolean {
  return e.ctrlKey && e.shiftKey && !e.altKey && e.code === 'KeyC'
}
