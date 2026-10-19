// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useServerInfo } from '@/lib/hooks/use-server-info'
import { usePersonalNavigation } from '@/features/navigation/personal-navigation'
import { savedViewsApi } from '@/features/saved-views/api'
import { moduleOn, useModulesStore } from '@/stores/modules'
import { commercialEdition } from './commercial-edition'

/** Reuse the verified personal-navigation lifetime and the user's server interface row. */
export function useEditionBadge() {
  const client = useQueryClient()
  // The edition is a build fact: a loaded Community build is never re-read. Until
  // a read succeeds the build is unknown, so a failed first read keeps retrying.
  const info = useServerInfo({
    refetchInterval: (query) => {
      const loaded = query.state.data
      return loaded &&
        !['business', 'enterprise'].includes(loaded.edition ?? '')
        ? false
        : 30_000
    },
  })
  const scope = usePersonalNavigation()?.scope
  const off = useModulesStore((s) => s.off)
  const available =
    !!scope?.key && !!scope.tenant && scope.live() && !off.has('consoleviews')
  const key = [
    'consoleviews',
    scope?.tenant,
    'edition-badge',
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
  const preference = useQuery({
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
    mutationFn: async (shown: boolean) => {
      guard()
      const result = await savedViewsApi.saveUiState(
        { show_edition_badge: shown },
        options,
      )
      guard()
      return result
    },
    onSuccess: (result) => {
      client.setQueryData(key, result)
    },
  })
  return {
    // A failed refresh never supplies an active badge from cached licence facts.
    edition: info.isError ? null : commercialEdition(info.data),
    shown:
      available &&
      preference.isSuccess &&
      preference.data.show_edition_badge !== false,
    canSave: available && preference.isSuccess && !mutation.isPending,
    loadError: !available || preference.isError,
    saving: mutation.isPending,
    saveError: mutation.isError,
    setShown: mutation.mutateAsync,
  }
}
