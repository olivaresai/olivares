// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useQuery } from '@tanstack/react-query'
import { useAuth } from '@/lib/auth/context'
import { useTenantStore } from '@/stores/tenant'
import { agentToolsApi, agentToolsKeys } from './api'

/** The usage windows the console names, by the label the engine gives them. */
export const USAGE_WINDOWS = { '5-hour': 'fiveHour', Weekly: 'week' } as const

/** The tone of a usage window, for the status bar and Usage & cost alike: a warning from 80 %, a stop at 100 %. */
export function windowTone(
  percent: number,
): 'text-bad' | 'text-warn' | undefined {
  return percent >= 100 ? 'text-bad' : percent >= 80 ? 'text-warn' : undefined
}

/** While the engine is still asking a tool, how soon to ask the engine again. */
const REFRESHING_POLL_MS = 10_000

/** Otherwise, how often to read the engine's cached snapshot while the page is visible. */
const USAGE_POLL_MS = 60_000

/**
 * The last answer the engine has about each tool's logins and their usage windows, as the
 * status bar and Usage & cost show it. Both share one query.
 *
 * Background reads return the cached snapshot without writing the audit ledger. Poll
 * once a minute while visible, or sooner while a native probe is still answering.
 */
export function useBackgroundProviders() {
  const { isSuperadmin } = useAuth()
  const tenant = useTenantStore((s) => s.activeTenant)
  return useQuery({
    queryKey: [...agentToolsKeys.all, 'providers', tenant, 'background'],
    // The last answer the engine has: the reader never waits on a tool.
    queryFn: ({ signal }) => agentToolsApi.providers(tenant, signal, true),
    // A system read, and tenant-scoped: none before an organization is chosen (the shell
    // sits outside TenantGate, and the engine answers "tenant required").
    enabled: !!isSuperadmin && !!tenant,
    staleTime: Infinity,
    refetchOnWindowFocus: false,
    refetchInterval: (query) =>
      query.state.data?.refreshing ? REFRESHING_POLL_MS : USAGE_POLL_MS,
  })
}
