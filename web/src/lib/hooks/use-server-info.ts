// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import {
  queryOptions,
  useQuery,
  type UseQueryOptions,
} from '@tanstack/react-query'
import { authApi } from '@/lib/api/endpoints'
import { queryKeys } from '@/lib/api/query'
import { isCommunityEdition } from '@/lib/edition'

/** server-info is unauthenticated and drives the setup gate, the auth redirects,
 * and the "about" panel. Cached briefly; it changes rarely. */
export const serverInfoQuery = queryOptions({
  queryKey: queryKeys.serverInfo,
  queryFn: () => authApi.serverInfo(),
  staleTime: 60_000,
  retry: 1,
})

type ServerInfo = Awaited<ReturnType<typeof authApi.serverInfo>>

export function useServerInfo(
  options: Pick<
    UseQueryOptions<ServerInfo, Error, ServerInfo, typeof queryKeys.serverInfo>,
    'refetchInterval'
  > = {},
) {
  return useQuery({ ...serverInfoQuery, ...options })
}

/**
 * Whether this engine is a Community build (lib/edition.ts): `undefined` until server-info has
 * answered, so a page can wait instead of reading a Business route it would be refused.
 */
export function useCommunityBuild(): boolean | undefined {
  const info = useServerInfo()
  return info.isSuccess ? isCommunityEdition(info.data.edition) : undefined
}
