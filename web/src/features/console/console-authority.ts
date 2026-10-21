// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import type { RequestOptions } from '@/lib/api/client'
import { useAuth } from '@/lib/auth/context'
import { PANEL_EXTENSIONS } from '@/features/extensions'

export const AGENT_CREATION = 'POST /v1/agents'
export const GROUP_CREATION = 'POST /v1/agent-groups'
export const GRANT_CREATION = 'POST /v1/m/governance/rbac/grants'

function useReflectedOperation(_operation: string, permission: string) {
  const { can } = useAuth()
  return { current: can(permission), workspace: null, access: null }
}

export function useConsoleOperation(operation: string, permission: string) {
  const useOperation =
    PANEL_EXTENSIONS.consoleAccess?.useOperation ?? useReflectedOperation
  return useOperation(operation, permission)
}

function useReflectedPreflight(_operation: string, _workspace?: string | null) {
  return async (authority: Pick<RequestOptions, 'signal' | 'dispatchGuard'>) =>
    authority
}

export function useConsolePreflight(
  operation: string,
  workspace?: string | null,
) {
  const usePreflight =
    PANEL_EXTENSIONS.consoleAccess?.usePreflight ?? useReflectedPreflight
  return usePreflight(operation, workspace)
}
