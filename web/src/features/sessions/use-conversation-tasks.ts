// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useQuery } from '@tanstack/react-query'
import { agentOpsApi, agentOpsKeys } from '@/features/agentops/api'
import { readsNativeRecord } from '@/features/agentops/provider-contract'
import type { RunDTO } from '@/features/agentops/types'

/** Use the engine's list, shared with Activity: Claude tasks and Codex plans survive
 * a reload and live-frame eviction without a second transcript fold in the browser. */
export function useConversationTasks(run: RunDTO, tenant: string | null) {
  const key = agentOpsKeys.runActivity(tenant, run.run_ref)
  return useQuery({
    queryKey: [...key, run.state],
    queryFn: ({ signal }) => agentOpsApi.activity(run.run_ref, { signal }),
    placeholderData: (previous, previousQuery) =>
      key.every((part, i) => previousQuery?.queryKey[i] === part)
        ? previous
        : undefined,
    enabled: readsNativeRecord(run) && run.transport !== 'remote-control',
  })
}
