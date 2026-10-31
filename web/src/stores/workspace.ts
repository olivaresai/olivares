// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { preferenceStorage } from '@/lib/preference-storage'
import { create } from 'zustand'
import { persist, createJSONStorage } from 'zustand/middleware'

/**
 * Active-workspace store. When the operator selects a workspace in the topbar
 * switcher, every list view that supports workspace scoping includes the chosen
 * id as `?workspace_id=` on its API calls (and in its TanStack Query key, so
 * switching triggers a refetch). `null` means "all workspaces" — no filter.
 *
 * Kept in the user's server preferences so the selection follows them. On a tenant
 * switch the workspace resets to `null` (the caller in providers.tsx subscribes
 * to the tenant store and clears us).
 */
interface WorkspaceState {
  activeWorkspace: string | null
  activeWorkspaceName: string | null
  setActiveWorkspace: (id: string | null, name?: string | null) => void
  clear: () => void
}

export const useWorkspaceStore = create<WorkspaceState>()(
  persist(
    (set) => ({
      activeWorkspace: null,
      activeWorkspaceName: null,
      setActiveWorkspace: (id, name) =>
        set({ activeWorkspace: id, activeWorkspaceName: name ?? null }),
      clear: () => set({ activeWorkspace: null, activeWorkspaceName: null }),
    }),
    {
      storage: createJSONStorage(() => preferenceStorage),
      name: 'olivares.workspace',
    },
  ),
)
