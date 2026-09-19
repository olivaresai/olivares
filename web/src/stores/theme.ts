// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { create } from 'zustand'

/**
 * Theme store. The persisted value lives in localStorage under the RAW key
 * `olivares.theme` (a plain "light"|"dark"|"system" string, NOT JSON) so the
 * no-FOUC bootstrap in index.html — which runs before any JS bundle — reads the
 * exact same key and applies `.dark` before first paint. This store mirrors that
 * logic for in-app toggling and keeps it in sync with the OS while in "system".
 */
export type Theme = 'light' | 'dark' | 'system'
export type ResolvedTheme = 'light' | 'dark'

const STORAGE_KEY = 'olivares.theme'

/**
 * THE DEFAULT IS DARK, AND IT USED TO BE `system`.
 *
 * ⛔ THIS IS A PRODUCT DECISION, NOT A PREFERENCE. Named a dark, quiet surface as
 *    the standard the console is measured against; all 23 of those reference captures
 *    are dark, and none is light. `system` handed the product's own skin to whatever the
 *    operator's OS happened to be set to, so the first thing half the people who opened
 *    the console saw was NOT the product as designed.
 *
 * ⛔ AND NOTHING IS TAKEN AWAY. `light` and `system` remain explicit choices, the theme
 *    toggle offers all three, and every token pair stays AA+ in both themes: the light
 *    theme has parity and is not a courtesy. What changed is which one an operator gets
 *    before they have said anything.
 *
 * ⛔ THE BOOTSTRAP IN `index.html` MUST AGREE WITH THIS FUNCTION BYTE FOR BYTE. It runs
 *    before any bundle, and a disagreement is not a subtle bug — it is a visible flash
 *    of the wrong theme on every cold load. `resolveDark` below is the mirror; the
 *    bootstrap's condition is the same three clauses in the same order.
 */
function readStored(): Theme {
  try {
    const v = localStorage.getItem(STORAGE_KEY)
    if (v === 'light' || v === 'dark' || v === 'system') return v
  } catch {
    /* localStorage may be unavailable (private mode) */
  }
  return 'dark'
}

function systemPrefersDark(): boolean {
  return (
    typeof window !== 'undefined' &&
    !!window.matchMedia?.('(prefers-color-scheme: dark)').matches
  )
}

/**
 * The persisted choice, exposed for the one test that pins the DEFAULT.
 *
 * The store reads `readStored()` once at module load, so by the time a test can clear
 * `localStorage` the initial value is already fixed and unreadable. This is the same
 * function under a name that says it is a seam, not a second implementation.
 */
export function readStoredThemeForTest(): Theme {
  return readStored()
}

/** resolveDark mirrors the index.html bootstrap exactly. */
export function resolveDark(theme: Theme): boolean {
  return theme === 'dark' || (theme === 'system' && systemPrefersDark())
}

function applyTheme(theme: Theme): ResolvedTheme {
  const dark = resolveDark(theme)
  const root = document.documentElement
  root.classList.toggle('dark', dark)
  root.style.colorScheme = dark ? 'dark' : 'light'
  return dark ? 'dark' : 'light'
}

interface ThemeState {
  theme: Theme
  resolved: ResolvedTheme
  setTheme: (theme: Theme) => void
}

const initial = readStored()

export const useThemeStore = create<ThemeState>((set) => ({
  theme: initial,
  resolved: resolveDark(initial) ? 'dark' : 'light',
  setTheme: (theme) => {
    try {
      localStorage.setItem(STORAGE_KEY, theme)
    } catch {
      /* ignore */
    }
    set({ theme, resolved: applyTheme(theme) })
  },
}))

// Track the OS preference so a "system" choice updates live.
if (typeof window !== 'undefined' && window.matchMedia) {
  const mq = window.matchMedia('(prefers-color-scheme: dark)')
  mq.addEventListener?.('change', () => {
    const { theme } = useThemeStore.getState()
    if (theme === 'system') {
      useThemeStore.setState({ resolved: applyTheme('system') })
    }
  })
}
