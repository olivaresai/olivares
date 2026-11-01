// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { preferenceStorage } from '@/lib/preference-storage'
import { create } from 'zustand'
import { persist, createJSONStorage } from 'zustand/middleware'

/**
 * UI preferences — non-sensitive operator settings kept in the user's server row. Table density is a foundation primitive: the data tables build
 * read it so the whole console switches compact/comfortable as one.
 */
export type Density = 'compact' | 'comfortable'

/** How much of a session's first message its title keeps; the engine's own set. */
export const TITLE_STYLES = ['short', 'sentence', 'long'] as const
export type TitleStyle = (typeof TITLE_STYLES)[number]

/**
 * The ONE place the density preference becomes a dimension. `DataTable` reads it for
 * header/body/skeleton row heights, for the virtualizer's row estimate and for the
 * sticky-header scroll padding, so the four cannot drift apart.
 *
 * Values are the console's spacing scale (Tailwind `h-8` = 2 rem = 32 px, `h-9` =
 * 2.25 rem = 36 px), the same scale that sizes the base controls (`Button` base `h-8`,
 * inputs, icon buttons `size-8`). Measured on the rendered console
 * (console-ui-current-baseline, 2026-09-06): the previous pair was `h-7`/`h-8`
 * (28/32 px) as a MINIMUM height on cells whose content was already taller, so the
 * Settings control changed nothing an operator could see. 32 px keeps every row a
 * WCAG 2.5.8 pointer target with room for the inset focus ring; 40 px is the
 * comfortable step, one control height, not a redesign of the type scale.
 *
 * A row is a MINIMUM height plus vertical cell padding: `comfortable` adds 4 px above
 * and below the content, `compact` none. A single-line row and a row holding a 32 px
 * control therefore sit exactly at the minimum in both densities (36 / 32).
 *
 * ⛔ A TWO-LINE ROW IS NO LONGER "the common case in this console", and that sentence
 *    used to be here. It was true and it was the defect: the measurement put
 *    the sessions table at 110 px a row because its widest column stacked a badge over
 *    a three-line profile reference, and four other tables the same way. The
 *    work-first pass made
 *    those cells ONE line each — nothing dropped, the identifiers on `title` and in the
 *    detail pane, which is where an identifier is the thing being operated on. The
 *    preference is still visible on a row holding a control or two lines of content; it
 *    is simply no longer the case that most rows are two lines.
 *
 * `px` is the number the row virtualizer estimates with before it measures the real
 * element; `stores/preferences.test.ts` pins that it equals the `h-*` token.
 *
 * ⛔ `headPx` IS `px` MINUS ONE, AND THE ONE IS THE RULE. The header strip carries the
 *    hairline that separates it from the first row, and `border-collapse` draws that
 *    hairline INSIDE the row box it belongs to. A header cell given the row's own `h-*`
 *    therefore measures one pixel MORE than a row — which is exactly the pixel that put
 *    `/audit`'s first ledger row at y=137 against a budget of 136, while the two screens
 *    built on `StaticTable` (whose header is a caption line plus `py-2`, 34 + 1) landed
 *    at 136. One row tall means one row tall INCLUDING the rule, at either density, and
 *    `preferences.test.ts` pins the subtraction so the pair cannot drift.
 */
export const DENSITY_ROW: Record<
  Density,
  { className: string; px: number; headClassName: string; headPx: number }
> = {
  compact: {
    className: 'h-8 py-0',
    px: 32,
    headClassName: 'h-[31px]',
    headPx: 31,
  },
  // ⛔ 36 px, AND IT WAS 40. `--console-row-height: 2.25rem` is the
  //    budget the design declares for a management table row, and `h-9` is that value
  //    on this scale. The step down is one row of type, not a redesign: a single-line
  //    row is still governed by this minimum (20 px of text plus `py-1`'s 8 px is 28),
  //    it is still well above the 24 px WCAG 2.5.8 pointer target, and it still leaves
  //    room for the inset focus ring. Over the 60-row page `/audit` loads it is 240 px
  //    of viewport returned — four more rows on a 900 px screen.
  comfortable: {
    className: 'h-9 py-1',
    px: 36,
    headClassName: 'h-[35px]',
    headPx: 35,
  },
}

/** The `h-*` token of a density class (the minimum row height). */
export function densityHeightToken(density: Density): string {
  return DENSITY_ROW[density].className
    .split(' ')
    .find((c) => /^h-\d+$/.test(c)) as string
}

/**
 * The two appearance switches the design tokens honor, written onto <html>:
 * `data-density` keys the spacing tokens (comfortable by default, compact on request) and
 * `data-motion="reduce"` stops the motion tokens and animations, exactly as the operating
 * system's reduced-motion preference does (styles/tokens.css, index.css). The attribute is
 * absent unless the operator asks for reduced motion, so the system preference still
 * applies on its own.
 */
export function applyAppearance(p: {
  density: Density
  reduceMotion: boolean
}): void {
  if (typeof document === 'undefined') return
  const root = document.documentElement
  root.dataset.density = p.density
  if (p.reduceMotion) root.dataset.motion = 'reduce'
  else root.removeAttribute('data-motion')
}

interface PreferencesState {
  density: Density
  /** The console's own Reduce motion setting; the system preference applies regardless. */
  reduceMotion: boolean
  /** Nav group ids the operator collapsed in the expanded sidebar. Retired hub ids;
   * kept so an existing persisted preference keeps its shape. Not read by the N1 sidebar. */
  collapsedNavGroups: string[]
  /** The left navigation folded to its icons (0.5). */
  navCollapsed: boolean
  /** The style the console asks the engine for when it titles a session. */
  titleStyle: TitleStyle
  setTitleStyle: (titleStyle: TitleStyle) => void
  setNavCollapsed: (navCollapsed: boolean) => void
  setDensity: (density: Density) => void
  setReduceMotion: (reduceMotion: boolean) => void
  toggleNavGroup: (group: string) => void
}

export const usePreferencesStore = create<PreferencesState>()(
  persist(
    (set) => ({
      density: 'comfortable',
      reduceMotion: false,
      collapsedNavGroups: [],
      navCollapsed: false,
      titleStyle: 'sentence',
      setTitleStyle: (titleStyle) => set({ titleStyle }),
      setNavCollapsed: (navCollapsed) => set({ navCollapsed }),
      setDensity: (density) => set({ density }),
      setReduceMotion: (reduceMotion) => set({ reduceMotion }),
      toggleNavGroup: (group) =>
        set((s) => ({
          collapsedNavGroups: s.collapsedNavGroups.includes(group)
            ? s.collapsedNavGroups.filter((g) => g !== group)
            : [...s.collapsedNavGroups, group],
        })),
    }),
    {
      storage: createJSONStorage(() => preferenceStorage),
      name: 'olivares.prefs',
      // The persisted blob is untrusted input. Every field the older store already had
      // rehydrates exactly as before; the navigation fields of earlier shells a blob
      // may still carry (the pre-Estate sidebar fold, All areas' `navAreas`) are read
      // and dropped here, so a corrupt or retired value can never reset density or
      // theme, and never folds the navigation.
      merge: (persisted, current) => {
        const p = (persisted ?? {}) as Partial<PreferencesState> & {
          sidebarCollapsed?: unknown
          sidebarChoices?: unknown
          sidebarUnsent?: unknown
          navAreas?: unknown
        }
        const {
          navAreas: _areas,
          navCollapsed,
          reduceMotion,
          titleStyle,
          sidebarCollapsed: _collapsed,
          sidebarChoices: _choices,
          sidebarUnsent: _unsent,
          ...rest
        } = p
        return {
          ...current,
          ...rest,
          // A blob saved before the setting existed (or holding anything but `true`)
          // keeps full motion; the system preference still applies on its own.
          reduceMotion: reduceMotion === true,
          // Anything but `true` keeps the navigation open.
          navCollapsed: navCollapsed === true,
          // Anything but one of the engine's styles takes the default.
          titleStyle: TITLE_STYLES.find((s) => s === titleStyle) ?? 'sentence',
        }
      },
    },
  ),
)

// The switches reach <html> once the stored preference is read, and on every change after.
applyAppearance(usePreferencesStore.getState())
usePreferencesStore.subscribe((s, prev) => {
  if (s.density !== prev.density || s.reduceMotion !== prev.reduceMotion)
    applyAppearance(s)
})
