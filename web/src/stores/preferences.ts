// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { create } from 'zustand'
import { persist } from 'zustand/middleware'

/**
 * UI preferences — local, non-sensitive operator settings persisted across
 * reloads. Table density is a foundation primitive: the data tables build
 * read it so the whole console switches compact/comfortable as one.
 */
export type Density = 'compact' | 'comfortable'

/**
 * The ONE place the density preference becomes a dimension. `DataTable` reads it for
 * header/body/skeleton row heights, for the virtualizer's row estimate and for the
 * sticky-header scroll padding, so the four cannot drift apart.
 *
 * Values are the console's spacing scale (Tailwind `h-8` = 2 rem = 32 px, `h-10` =
 * 2.5 rem = 40 px), the same scale that sizes the base controls (`Button` base `h-8`,
 * inputs, icon buttons `size-8`). Measured on the rendered console
 * (console-ui-current-baseline, 2026-09-06): the previous pair was `h-7`/`h-8`
 * (28/32 px) as a MINIMUM height on cells whose content was already taller, so the
 * Settings control changed nothing an operator could see. 32 px keeps every row a
 * WCAG 2.5.8 pointer target with room for the inset focus ring; 40 px is the
 * comfortable step, one control height, not a redesign of the type scale.
 *
 * A row is a MINIMUM height plus vertical cell padding: `comfortable` adds 4 px above
 * and below the content, `compact` none. A single-line row and a row holding a 32 px
 * control therefore sit exactly at the minimum in both densities (40 / 32), while a
 * two-line row — the common case in this console: a name over an id — is 49 px
 * comfortable and 41 px compact, so the preference is visible on every row kind
 * rather than only on the rare single-line one.
 *
 * `px` is the number the row virtualizer estimates with before it measures the real
 * element; `stores/preferences.test.ts` pins that it equals the `h-*` token.
 */
export const DENSITY_ROW: Record<Density, { className: string; px: number }> = {
  compact: { className: 'h-8 py-0', px: 32 },
  comfortable: { className: 'h-10 py-1', px: 40 },
}

/** The `h-*` token of a density class (the minimum row height). */
export function densityHeightToken(density: Density): string {
  return DENSITY_ROW[density].className
    .split(' ')
    .find((c) => /^h-\d+$/.test(c)) as string
}

/**
 * AREA EXPANSION (N1) — which of the nine navigation areas the operator holds open in the
 * expanded sidebar. Its OWN versioned field, separate from `collapsedNavGroups` (the five
 * retired hub ids, left untouched for compatibility) and from density, theme and
 * `sidebarCollapsed`, which it never resets.
 *
 * Two lists, because "open" has two sources and only one of them is a preference:
 *   - `open`   — areas the operator opened on purpose. Persisted; they stay open.
 *   - `closed` — areas the operator collapsed on purpose WHILE they were the active area,
 *                so the active area can be folded without changing route. Arriving at a
 *                route inside an area removes that area from `closed` (revealArea), which
 *                is how "the active route opens its area on arrival" and "I folded it, leave
 *                it folded" coexist.
 * The active area itself is derived from the url on every render, never stored.
 */
export const AREA_EXPANSION_VERSION = 1 as const

export interface AreaExpansion {
  v: typeof AREA_EXPANSION_VERSION
  open: string[]
  closed: string[]
}

export const DEFAULT_AREA_EXPANSION: AreaExpansion = {
  v: AREA_EXPANSION_VERSION,
  open: [],
  closed: [],
}

/** True when a persisted value is an AreaExpansion this build understands. Anything else —
 * a future version, a hand-edited blob, a truncated write — is discarded for the default
 * rather than trusted, so a bad entry can never leave the navigation without areas. */
export function isAreaExpansion(value: unknown): value is AreaExpansion {
  if (!value || typeof value !== 'object') return false
  const v = value as Record<string, unknown>
  const strings = (x: unknown) =>
    Array.isArray(x) && x.every((s) => typeof s === 'string')
  return v.v === AREA_EXPANSION_VERSION && strings(v.open) && strings(v.closed)
}

/** Whether an area renders open: opened on purpose, or active and not folded on purpose. */
export function isAreaOpen(
  expansion: AreaExpansion,
  areaId: string,
  activeAreaId: string | null,
): boolean {
  if (expansion.open.includes(areaId)) return true
  return activeAreaId === areaId && !expansion.closed.includes(areaId)
}

interface PreferencesState {
  sidebarCollapsed: boolean
  density: Density
  /** Nav group ids the operator collapsed in the expanded sidebar. Retired hub ids;
   * kept so an existing persisted preference keeps its shape. Not read by the N1 sidebar. */
  collapsedNavGroups: string[]
  /** N1 area expansion — see AreaExpansion. */
  navAreas: AreaExpansion
  setSidebarCollapsed: (collapsed: boolean) => void
  toggleSidebar: () => void
  setDensity: (density: Density) => void
  toggleNavGroup: (group: string) => void
  /** Open or fold one area on purpose. */
  setAreaOpen: (areaId: string, open: boolean) => void
  /** Open or fold every area named, on purpose (expand all / collapse all). */
  setAreasOpen: (areaIds: readonly string[], open: boolean) => void
  /** The route entered this area: it opens unless the operator folds it again. */
  revealArea: (areaId: string) => void
}

export const usePreferencesStore = create<PreferencesState>()(
  persist(
    (set) => ({
      sidebarCollapsed: false,
      density: 'comfortable',
      collapsedNavGroups: [],
      navAreas: DEFAULT_AREA_EXPANSION,
      setSidebarCollapsed: (sidebarCollapsed) => set({ sidebarCollapsed }),
      toggleSidebar: () =>
        set((s) => ({ sidebarCollapsed: !s.sidebarCollapsed })),
      setDensity: (density) => set({ density }),
      toggleNavGroup: (group) =>
        set((s) => ({
          collapsedNavGroups: s.collapsedNavGroups.includes(group)
            ? s.collapsedNavGroups.filter((g) => g !== group)
            : [...s.collapsedNavGroups, group],
        })),
      setAreaOpen: (areaId, open) =>
        set((s) => ({
          navAreas: {
            v: AREA_EXPANSION_VERSION,
            open: open
              ? [...new Set([...s.navAreas.open, areaId])]
              : s.navAreas.open.filter((a) => a !== areaId),
            closed: open
              ? s.navAreas.closed.filter((a) => a !== areaId)
              : [...new Set([...s.navAreas.closed, areaId])],
          },
        })),
      setAreasOpen: (areaIds, open) =>
        set((s) => ({
          navAreas: {
            v: AREA_EXPANSION_VERSION,
            open: open
              ? [...new Set([...s.navAreas.open, ...areaIds])]
              : s.navAreas.open.filter((a) => !areaIds.includes(a)),
            closed: open
              ? s.navAreas.closed.filter((a) => !areaIds.includes(a))
              : [...new Set([...s.navAreas.closed, ...areaIds])],
          },
        })),
      revealArea: (areaId) =>
        set((s) =>
          s.navAreas.closed.includes(areaId)
            ? {
                navAreas: {
                  ...s.navAreas,
                  closed: s.navAreas.closed.filter((a) => a !== areaId),
                },
              }
            : s,
        ),
    }),
    {
      name: 'olivares.prefs',
      // The persisted blob is untrusted input. Every field the older store already had
      // rehydrates exactly as before; `navAreas` rehydrates only when it is an
      // AreaExpansion of THIS version, otherwise the default — so a corrupt or future
      // value can never reset density, theme or the sidebar, and never hides the areas.
      merge: (persisted, current) => {
        const p = (persisted ?? {}) as Partial<PreferencesState>
        const { navAreas, ...rest } = p
        return {
          ...current,
          ...rest,
          navAreas: isAreaExpansion(navAreas)
            ? navAreas
            : DEFAULT_AREA_EXPANSION,
        }
      },
    },
  ),
)
