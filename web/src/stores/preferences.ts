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

interface PreferencesState {
  sidebarCollapsed: boolean
  density: Density
  /** Nav group ids the operator collapsed in the expanded sidebar.*/
  collapsedNavGroups: string[]
  setSidebarCollapsed: (collapsed: boolean) => void
  toggleSidebar: () => void
  setDensity: (density: Density) => void
  toggleNavGroup: (group: string) => void
}

export const usePreferencesStore = create<PreferencesState>()(
  persist(
    (set) => ({
      sidebarCollapsed: false,
      density: 'comfortable',
      collapsedNavGroups: [],
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
    }),
    { name: 'olivares.prefs' },
  ),
)
