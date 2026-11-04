// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
import { useInfiniteQuery, useQuery } from '@tanstack/react-query'
import { agentOpsApi, agentOpsKeys } from '@/features/agentops/api'
import { useAuthBoundary } from '@/features/agentops/auth-boundary'
import { profileLabel } from '@/features/agentops/types'
import { consoleApi, consoleKeys } from '@/features/console/api'
import { useModuleOn } from '@/stores/modules'
import { useAuth } from '@/lib/auth/context'
import type { SkillTargetKind } from './types'
const DIRECTORY_PAGE = { limit: 1000 }
const FOLDER_PAGE = { state: 'active', limit: 200 } as const
const PROFILE_PAGE = { limit: 200 }
interface DirectoryList {
  items: Array<{ id: string; name: string }>
  isLoading: boolean
  error: unknown
  hasMore?: boolean
  isFetchingMore?: boolean
  loadMore?: () => void
}
export type Directory = Partial<Record<SkillTargetKind, DirectoryList>>

export function useDirectory(tenant: string | null): Directory {
  const { can } = useAuth()
  const boundary = useAuthBoundary()
  // Session folders belong to the sessions module: without it there are none.
  const sessionsOn = useModuleOn('sessions')
  const workspaces = useQuery({
    queryKey: consoleKeys.workspaces(tenant, DIRECTORY_PAGE),
    queryFn: () => consoleApi.listWorkspaces(DIRECTORY_PAGE),
    enabled: can('tenant:read'),
  })
  const groups = useQuery({
    queryKey: consoleKeys.agentGroups(tenant, DIRECTORY_PAGE),
    queryFn: () => consoleApi.listAgentGroups(DIRECTORY_PAGE),
    enabled: can('agent:read'),
  })
  const agents = useQuery({
    queryKey: consoleKeys.agents(tenant, DIRECTORY_PAGE),
    queryFn: () => consoleApi.listAgents(DIRECTORY_PAGE),
    enabled: can('agent:read'),
  })
  const folders = useQuery({
    queryKey: agentOpsKeys.workspaces(tenant, FOLDER_PAGE),
    queryFn: ({ signal }) =>
      agentOpsApi.listWorkspaces(FOLDER_PAGE, { tenant, signal }),
    enabled: sessionsOn && can('sessions:workspace:read'),
  })
  const profiles = useInfiniteQuery({
    queryKey: [
      ...agentOpsKeys.profiles(tenant, boundary.epoch, PROFILE_PAGE),
      'skill-targets',
    ],
    queryFn: ({ pageParam, signal }) =>
      agentOpsApi.listProfiles(
        { ...PROFILE_PAGE, cursor: pageParam },
        { signal },
      ),
    initialPageParam: undefined as string | undefined,
    getNextPageParam: (page) => (page.has_more ? page.cursor : undefined),
    enabled:
      sessionsOn &&
      can('sessions:profile:read') &&
      can('sessions:profile:write'),
  })
  const entry = (q: typeof workspaces | typeof groups | typeof agents) => ({
    items: q.data?.items ?? [],
    isLoading: q.isLoading,
    error: q.error,
  })
  return {
    workspace: entry(workspaces),
    profile: {
      items: (profiles.data?.pages.flatMap((page) => page.items) ?? []).map(
        (p) => ({ id: p.profile_ref, name: profileLabel(p) }),
      ),
      isLoading: profiles.isLoading,
      error: profiles.error,
      hasMore: profiles.hasNextPage,
      isFetchingMore: profiles.isFetchingNextPage,
      loadMore: () => {
        void profiles.fetchNextPage()
      },
    },
    // A folder is named by its workspace_ref; its label is the name it was
    // registered under, else the last segment of its path.
    folder: {
      items: (folders.data?.items ?? []).map((w) => ({
        id: w.workspace_ref,
        name:
          w.name || w.root_path.split('/').filter(Boolean).pop() || w.root_path,
      })),
      isLoading: folders.isLoading,
      error: folders.error,
    },
    agent_group: entry(groups),
    agent: entry(agents),
  }
}
