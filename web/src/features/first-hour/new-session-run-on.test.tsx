// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Where a build mounts a Run on control, the node it picks goes with the Advanced handover
// (new-session-model.test.tsx and new-session-profile.test.tsx pin that an unset node is left
// out of it). The control is a build-time seam, so this file mounts a stand-in for it.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import type { ReactNode } from 'react'
import { beforeEach, describe, expect, it, vi } from 'vitest'

vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({
    activeTenant: 'tnt-a',
    can: () => true,
    isSuperadmin: true,
  }),
}))
vi.mock('@tanstack/react-router', () => ({
  useNavigate: () => vi.fn(),
  useRouterState: () => '',
}))
vi.mock('./api', async (orig) => ({
  ...((await orig()) as Record<string, unknown>),
  signInApi: {
    status: (driver: string) =>
      Promise.resolve({ driver, installed: true, signed_in: true }),
  },
}))
vi.mock('@/features/extensions', async (orig) => ({
  ...((await orig()) as Record<string, unknown>),
  RUN_ON_EXTENSION: ({ onChange }: { onChange: (node: string) => void }) => (
    <button type="button" onClick={() => onChange('node-remote')}>
      Pick node-remote
    </button>
  ),
}))

import { agentOpsApi } from '@/features/agentops/api'
import { useTenantStore } from '@/stores/tenant'
import { StartSessionForm } from './first-hour'
import { readinessOf } from './readiness.fixture'

function wrap(ui: ReactNode) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(<QueryClientProvider client={qc}>{ui}</QueryClientProvider>)
}

beforeEach(() => {
  vi.restoreAllMocks()
  useTenantStore.setState({ activeTenant: 'tnt-a' })
})

describe('the Run on node in the Advanced handover', () => {
  it('goes with the tool for a tool on a key', async () => {
    const user = userEvent.setup()
    vi.spyOn(agentOpsApi, 'toolsReadiness').mockResolvedValue(
      readinessOf({
        claude: 'own_login',
        grok: 'own_login',
        opencode: 'own_login',
        codex: {
          provider: {
            provider_ref: 'prv-1',
            kind: 'openai',
            display_name: 'Team key',
            default_model: 'gpt-b',
            models: ['gpt-a', 'gpt-b'],
          },
        },
      }),
    )
    const onAdvanced = vi.fn()
    wrap(<StartSessionForm onAdvanced={onAdvanced} />)
    await user.click(await screen.findByRole('radio', { name: 'Codex' }))
    await user.click(screen.getByRole('button', { name: 'Pick node-remote' }))
    await user.click(screen.getByRole('button', { name: 'More options' }))
    await user.click(
      await screen.findByRole('button', { name: 'Advanced launch options' }),
    )
    expect(onAdvanced).toHaveBeenCalledExactlyOnceWith({
      driver: 'codex',
      runOn: 'node-remote',
    })
  })

  it('goes with the sign-in the form shows', async () => {
    const user = userEvent.setup()
    vi.spyOn(agentOpsApi, 'toolsReadiness').mockResolvedValue(
      readinessOf({
        claude: 'own_login',
        grok: 'own_login',
        opencode: 'own_login',
        codex: 'own_login',
      }),
    )
    vi.spyOn(agentOpsApi, 'previewProfile').mockResolvedValue({
      reason: 'own_login',
      profile: {
        profile_ref: 'ppf_claude',
        driver: 'claude',
        environment_ref: 'xenv_1',
        state: 'active',
        local_environment: true,
        operable: true,
      },
    })
    const onAdvanced = vi.fn()
    wrap(<StartSessionForm onAdvanced={onAdvanced} />)
    await user.click(await screen.findByRole('radio', { name: 'Claude Code' }))
    await user.click(screen.getByRole('button', { name: 'Pick node-remote' }))
    await user.click(screen.getByRole('button', { name: 'More options' }))
    await user.click(
      await screen.findByRole('button', { name: 'Advanced launch options' }),
    )
    expect(onAdvanced).toHaveBeenCalledExactlyOnceWith(
      expect.objectContaining({
        driver: 'claude',
        profileRef: 'ppf_claude',
        runOn: 'node-remote',
      }),
    )
  })
})
