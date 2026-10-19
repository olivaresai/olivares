// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// ⌘K "Stop all agents": the verb opens the stop-all confirmation and engages nothing by
// itself.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, within } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({
    can: (p: string) => p === 'governance:killswitch:admin',
    activeTenant: 't1',
  }),
}))

import { queryKeys } from '@/lib/api/query'
import { liveCapabilityContext } from '@/lib/auth/capabilities'
import { useCommandStore } from '@/stores/command'
import { useSessionStore } from '@/stores/session'
import { useTenantStore } from '@/stores/tenant'
import { useWorkspaceStore } from '@/stores/workspace'
import { killswitchApi } from './api'
import { StopAllAction } from './engage-card'

function client() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  qc.setQueryData(queryKeys.whoami, {
    kind: 'user',
    user_id: 'u-1',
    actor: 'u-1',
    display_name: 'Ada',
    superadmin: false,
    grants: [],
  })
  return qc
}

beforeEach(() => {
  useTenantStore.setState({ activeTenant: 't1' })
  useSessionStore.setState({ credentialGeneration: 0 })
  useWorkspaceStore.setState({ activeWorkspace: null })
  useCommandStore.setState({ pendingAction: null, open: false, opener: null })
})
afterEach(() => vi.restoreAllMocks())

describe('the Stop all agents verb', () => {
  it('opens the stop-all confirmation and engages nothing', async () => {
    const engage = vi.spyOn(killswitchApi, 'engage')
    const qc = client()
    useCommandStore
      .getState()
      .setPendingAction('killswitch', 'engage', liveCapabilityContext(qc))
    render(
      <QueryClientProvider client={qc}>
        <StopAllAction estateStopped={false} />
      </QueryClientProvider>,
    )
    const dialog = await screen.findByRole('dialog')
    expect(within(dialog).getByText(/stop all agents\?/i)).toBeInTheDocument()
    expect(useCommandStore.getState().pendingAction).toBeNull()
    expect(engage).not.toHaveBeenCalled()
  })
})
