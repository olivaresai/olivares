// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useServerInfo } from '@/lib/hooks/use-server-info'
import { commercialEdition } from './commercial-edition'
import { useUiState } from './use-ui-state'

/** Reuse the verified personal-navigation lifetime and the user's server interface row. */
export function useEditionBadge() {
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
  const { available, query: preference, save, mutation } = useUiState()
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
    setShown: (shown: boolean) => save({ show_edition_badge: shown }),
  }
}
