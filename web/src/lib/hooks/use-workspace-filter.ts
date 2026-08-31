// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useWorkspaceStore } from '@/stores/workspace'

/**
 * Returns the active workspace id for API query params and TanStack Query keys.
 * When null (all workspaces), `queryParam` is undefined so it's omitted from the
 * URL. `queryKey` is always a stable string for cache keying ("__all__" or the id).
 */
export function useWorkspaceFilter() {
  const activeWorkspace = useWorkspaceStore((s) => s.activeWorkspace)
  return {
    workspaceId: activeWorkspace ?? undefined,
    queryKey: activeWorkspace ?? '__all__',
  }
}
