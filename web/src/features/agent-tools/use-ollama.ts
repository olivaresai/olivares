// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE OLLAMA THIS ENGINE RUNS, as one read for every screen that shows it (the AI tools
// panel and the Models page): its status, Start and Stop, and the refresh that follows.
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useCallback, useEffect, useRef } from 'react'
import { useTranslation } from 'react-i18next'
import { firstHourKeys } from '@/features/first-hour/api'
import { providerKeys } from '@/features/providers/api'
import { usePrivilegedMutation } from '@/lib/hooks/use-privileged-mutation'
import { useTenantStore } from '@/stores/tenant'
import { agentToolsApi, OLLAMA_AVAILABLE_KEY, type OllamaStatus } from './api'
import './i18n'

export const OLLAMA_STATUS_KEY = ['agent-tools', 'ollama'] as const
/** `enabled` false reads nothing: the routes answer a system administrator only. */
export function useOllama(enabled = true) {
  const { t } = useTranslation('agentTools')
  const qc = useQueryClient()
  // The organization the person works in gets the endpoint in its Providers, and only it.
  const tenant = useTenantStore((s) => s.activeTenant)
  const status = useQuery({
    queryKey: OLLAMA_STATUS_KEY,
    queryFn: ({ signal }) => agentToolsApi.ollama(signal),
    enabled,
    refetchInterval: (query) =>
      query.state.data?.state === 'starting' ? 1000 : false,
  })
  const readinessChanged = useCallback(
    () =>
      Promise.all([
        qc.invalidateQueries({ queryKey: firstHourKeys.all(tenant) }),
        qc.invalidateQueries({ queryKey: providerKeys.all(tenant) }),
      ]),
    [qc, tenant],
  )
  // Progress lives below the status key, so that read is invalidated exactly.
  // Available has its own prefix; both follow a finished download or service change.
  const refresh = () =>
    Promise.all([
      qc.invalidateQueries({ queryKey: OLLAMA_STATUS_KEY, exact: true }),
      qc.invalidateQueries({
        predicate: (q) => q.queryKey.includes(OLLAMA_AVAILABLE_KEY),
      }),
      readinessChanged(),
    ])
  // The engine registers this server in Providers once it is up, after the start request
  // returned: reaching running is when the readiness reads must ask again.
  const serverState = status.data?.state
  const lastState = useRef(serverState)
  useEffect(() => {
    if (
      (serverState === 'running' || serverState === 'failed') &&
      lastState.current !== serverState
    )
      void qc.invalidateQueries({
        predicate: (q) => q.queryKey.includes(OLLAMA_AVAILABLE_KEY),
      })
    if (serverState === 'running' && lastState.current !== serverState)
      void readinessChanged()
    lastState.current = serverState
  }, [serverState, readinessChanged, qc])
  const start = usePrivilegedMutation<void, OllamaStatus>({
    mutationFn: (_, authority) => agentToolsApi.ollamaStart(tenant, authority),
    stepUpAction: 'console',
    successMessage: t('ollama.startRequested'),
    onDone: () => void refresh(),
  })
  const stop = useMutation({
    mutationFn: () => agentToolsApi.ollamaStop(),
    onSettled: () => void refresh(),
  })
  return { status, start, stop, refresh }
}

export type Ollama = ReturnType<typeof useOllama>
