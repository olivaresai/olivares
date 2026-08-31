// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useQuery } from '@tanstack/react-query'
import { authApi } from '@/lib/api/endpoints'
import { queryKeys } from '@/lib/api/query'

/** server-info is unauthenticated and drives the setup gate, the auth redirects,
 * and the "about" panel. Cached briefly; it changes rarely. */
export function useServerInfo() {
  return useQuery({
    queryKey: queryKeys.serverInfo,
    queryFn: () => authApi.serverInfo(),
    staleTime: 60_000,
    retry: 1,
  })
}
