// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useQueryClient } from '@tanstack/react-query'
import { useEffect, useMemo, useRef } from 'react'
import { useAuthBoundary } from '@/features/agentops/auth-boundary'
import { useWorkspaceStore } from '@/stores/workspace'
import { communicationsKeys } from './api'
import type { IntentScope } from './intent'

/**
 * THE SCOPE of everything this feature reads or does: the accepted authority
 * boundary (principal | tenant | credential generation, from `useAuthBoundary`)
 * plus the EXPLICIT workspace selection. `useAuthBoundary` clears only the
 * provider-plane's own keys when the boundary moves, so this hook does the same
 * for the communications partition: the previous partition is cancelled (the abort
 * reaches the network through the reads' signals) and removed, so a late answer has
 * nowhere to land and no row of the previous scope waits for a return to it. A
 * workspace change ends only that workspace's partition — the boundary is the same
 * operator, and the tenant-wide directory read may stay.
 */
export interface CommunicationsScope {
  tenant: string | null
  epoch: number
  principal: string | null
  /** The explicit workspace, or null when the switcher says "all". */
  workspace: string | null
  workspaceName: string | null
  /** Opaque key of (boundary, workspace): the remount key and the intent boundary. */
  key: string
  /** The boundary alone, for reads that are not workspace-bound. */
  boundaryKey: string
}

export function useCommunicationsScope(): CommunicationsScope {
  const boundary = useAuthBoundary()
  const workspace = useWorkspaceStore((s) => s.activeWorkspace)
  const workspaceName = useWorkspaceStore((s) => s.activeWorkspaceName)
  const queryClient = useQueryClient()
  const scope = useMemo<CommunicationsScope>(
    () => ({
      tenant: boundary.tenant,
      epoch: boundary.epoch,
      principal: boundary.principal,
      workspace,
      workspaceName,
      key: `${boundary.key}|w:${workspace ?? '-'}`,
      boundaryKey: boundary.key,
    }),
    [boundary, workspace, workspaceName],
  )
  const previous = useRef<{
    tenant: string | null
    epoch: number
    workspace: string | null
  } | null>(null)
  useEffect(() => {
    const prev = previous.current
    if (prev && prev.epoch !== scope.epoch) {
      const key = communicationsKeys.boundaryScope(prev.tenant, prev.epoch)
      void queryClient.cancelQueries({ queryKey: key })
      queryClient.removeQueries({ queryKey: key })
    } else if (prev && prev.workspace !== scope.workspace && prev.workspace) {
      const key = communicationsKeys.workspaceScope(
        prev.tenant,
        prev.epoch,
        prev.workspace,
      )
      void queryClient.cancelQueries({ queryKey: key })
      queryClient.removeQueries({ queryKey: key })
    }
    previous.current = {
      tenant: scope.tenant,
      epoch: scope.epoch,
      workspace: scope.workspace,
    }
  }, [scope, queryClient])
  return scope
}

/** The intent scope of an action confirmed NOW, or null without a workspace. */
export function intentScopeOf(scope: CommunicationsScope): IntentScope | null {
  if (!scope.workspace) return null
  return {
    tenant: scope.tenant,
    workspace: scope.workspace,
    boundary: scope.key,
  }
}
