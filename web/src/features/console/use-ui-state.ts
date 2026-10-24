// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { usePersonalNavigation } from '@/features/navigation/personal-navigation'
import { savedViewsApi } from '@/features/saved-views/api'
import type { UiState } from '@/features/saved-views/types'
import { moduleOn, useModulesStore } from '@/stores/modules'

/**
 * The person's own interface state on the engine (`/v1/m/consoleviews/ui-state`): one read
 * and one save for every setting kept there (the edition badge, the panel widths), bound to
 * the verified personal-navigation lifetime. A save names only the fields it changes; the
 * engine keeps the rest.
 */
export function useUiState() {
  const client = useQueryClient()
  const scope = usePersonalNavigation()?.scope
  const off = useModulesStore((s) => s.off)
  const available =
    !!scope?.key && !!scope.tenant && scope.live() && !off.has('consoleviews')
  const key = [
    'consoleviews',
    scope?.tenant,
    'ui-state',
    scope?.key,
    scope?.generation,
  ] as const
  const guard = () => {
    if (!scope?.live() || !scope.tenant || !moduleOn('consoleviews'))
      throw new Error('Interface setting lifetime ended')
  }
  const options = {
    tenant: scope?.tenant,
    dispatchGuard: guard,
    sessionEffects: 'none' as const,
  }
  const query = useQuery({
    queryKey: key,
    enabled: available,
    queryFn: async ({ signal }) => {
      guard()
      const result = await savedViewsApi.uiState({ ...options, signal })
      guard()
      return result
    },
  })
  const mutation = useMutation({
    mutationKey: key,
    // One save at a time: a slow save must not land after the newer one.
    scope: { id: 'ui-state' },
    mutationFn: async (state: UiState) => {
      guard()
      const result = await savedViewsApi.saveUiState(state, options)
      guard()
      return result
    },
    onSuccess: (result) => {
      client.setQueryData(key, result)
    },
  })
  return { available, query, save: mutation.mutateAsync, mutation, scope }
}
