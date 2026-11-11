// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { queryOptions } from '@tanstack/react-query'
import { agentOpsApi, agentOpsKeys } from '@/features/agentops/api'

/** How many registered folders, newest first, the read asks for. The New session form
 * matches the last folder against them; one beyond them gives a new folder instead. */
const FOLDER_WINDOW = 200

/** Whether a session started in a folder of this mount mode writes there, as the
 * engine grants it: only a read-only mount keeps it to reading, and write_no_delete
 * writes and creates without erasing. */
export function sessionWrites(mountMode: string): boolean {
  return mountMode !== 'ro'
}

/** The registered folders a session can start in, for one sign-in and organization. One
 * read and one cache entry: the New session form's folder field and the Overview's list
 * of a person's folders ask the same question. */
export function registeredFoldersQuery(tenant: string | null, epoch: number) {
  const params = { state: 'active', limit: FOLDER_WINDOW }
  return queryOptions({
    queryKey: [
      ...agentOpsKeys.boundaryScope(tenant, epoch),
      'workspaces',
      params,
    ],
    queryFn: ({ signal }) =>
      agentOpsApi.listWorkspaces(params, { tenant, signal }),
    enabled: !!tenant,
    staleTime: 0,
  })
}
