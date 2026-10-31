// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// the credentials picked in New session's More options reach the launch's
// quick form — the one link no component or launch test pins on its own.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import type { ReactNode } from 'react'
import { beforeEach, describe, expect, it, vi } from 'vitest'

vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({
    can: () => true,
    isSuperadmin: true,
    activeTenant: 'tnt-a',
    principal: { user_id: 'u1', aal: 1 },
  }),
}))
const navigate = vi.hoisted(() => vi.fn())
vi.mock('@tanstack/react-router', () => ({
  useNavigate: () => navigate,
  useRouterState: () => '',
}))
const launch = vi.hoisted(() => ({ launchSession: vi.fn() }))
vi.mock('@/features/agentops/session-launch', async (orig) => ({
  ...((await orig()) as Record<string, unknown>),
  launchSession: launch.launchSession,
}))
const consoleApi = vi.hoisted(() => ({ listSecrets: vi.fn() }))
vi.mock('@/features/console/api', async (orig) => ({
  ...((await orig()) as Record<string, unknown>),
  consoleApi,
}))

import { agentOpsApi } from '@/features/agentops/api'
import { useTenantStore } from '@/stores/tenant'
import { StartSessionForm } from './first-hour'
import { readinessOf } from './readiness.fixture'

function wrap(ui: ReactNode) {
  return render(
    <QueryClientProvider
      client={
        new QueryClient({ defaultOptions: { queries: { retry: false } } })
      }
    >
      {ui}
    </QueryClientProvider>,
  )
}

beforeEach(() => {
  vi.restoreAllMocks()
  launch.launchSession.mockReset()
  launch.launchSession.mockResolvedValue({ run_ref: 'run_1' })
  useTenantStore.setState({ activeTenant: 'tnt-a' })
  vi.spyOn(agentOpsApi, 'toolsReadiness').mockResolvedValue(
    readinessOf({ claude: 'own_login' }),
  )
  vi.spyOn(agentOpsApi, 'listAccounts').mockResolvedValue({
    items: [],
    has_more: false,
  })
  vi.spyOn(agentOpsApi, 'listRuns').mockResolvedValue({
    items: [],
    has_more: false,
  })
  vi.spyOn(agentOpsApi, 'listWorkspaces').mockResolvedValue({
    items: [],
    has_more: false,
  })
  consoleApi.listSecrets.mockReset()
  consoleApi.listSecrets.mockResolvedValue({
    sealer_available: true,
    secrets: [{ name: 'credential/prod-db', hint: 'ab12' }],
  })
})

describe('New session: the managed credentials chosen reach the launch', () => {
  it('sends the picked credential names on the quick launch', async () => {
    const user = userEvent.setup()
    wrap(<StartSessionForm />)
    await screen.findByRole('button', { name: 'Start' })
    await user.click(screen.getByRole('button', { name: /more options/i }))
    await user.click(await screen.findByRole('checkbox', { name: /prod-db/ }))
    await user.click(screen.getByRole('button', { name: 'Start' }))
    await waitFor(() => expect(launch.launchSession).toHaveBeenCalledTimes(1))
    expect(launch.launchSession.mock.calls[0][0].quick.credentials).toEqual([
      'prod-db',
    ])
  })

  it('starts without the field when none was picked', async () => {
    const user = userEvent.setup()
    wrap(<StartSessionForm />)
    await screen.findByRole('button', { name: 'Start' })
    await user.click(screen.getByRole('button', { name: 'Start' }))
    await waitFor(() => expect(launch.launchSession).toHaveBeenCalledTimes(1))
    expect(launch.launchSession.mock.calls[0][0].quick).not.toHaveProperty(
      'credentials',
    )
  })
})
