// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The capture gate allows one filled orange control per first screen. The setup checklist
// shows one card per tool, so each card's sign-in is a quiet button there. The same
// SignIn is the only action of the profile sign-in dialog, where it keeps the filled
// primary: demoting it in the shared component took that verb away (objective 2).
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen } from '@testing-library/react'
import type { ReactNode } from 'react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import '@/features/agent-tools/i18n'

vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({
    activeTenant: 'tnt-a',
    can: () => true,
    isSuperadmin: true,
    principal: { user_id: 'u1', aal: 1 },
  }),
}))

import { agentOpsApi } from '@/features/agentops/api'
import type { ProviderAccountDTO } from '@/features/agentops/types'
import { ProfileSignInDialog } from '@/features/agent-tools/profile-sign-in'
import { ApiError } from '@/lib/api/errors'
import { useTenantStore } from '@/stores/tenant'
import { signInApi } from './api'
import { readinessOf } from './readiness.fixture'
import { ToolCard } from './first-hour'

const FILLED = 'text-on-accent'

function wrap(ui: ReactNode) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(<QueryClientProvider client={qc}>{ui}</QueryClientProvider>)
}

beforeEach(() => {
  vi.restoreAllMocks()
  useTenantStore.setState({ activeTenant: 'tnt-a' })
  vi.spyOn(signInApi, 'status').mockResolvedValue({
    driver: 'codex',
    installed: true,
    signed_in: false,
  })
  vi.spyOn(agentOpsApi, 'toolsReadiness').mockResolvedValue(readinessOf({}))
})

describe('the sign-in button, by where it stands', () => {
  it('is the filled primary in the profile sign-in dialog, its only action', async () => {
    // The dialog starts the login by itself; a start that fails leaves the button.
    vi.spyOn(signInApi, 'start').mockRejectedValue(
      new ApiError(500, 'internal', 'The login did not start.'),
    )
    wrap(
      <ProfileSignInDialog
        driver="codex"
        name="Codex"
        tenant="tnt-a"
        onOpenChange={() => undefined}
      />,
    )
    const button = await screen.findByRole('button', {
      name: 'Sign in with ChatGPT',
    })
    expect(button).toHaveClass(FILLED)
  })

  it('is the filled primary in the dialog of a named account too', async () => {
    const account: ProviderAccountDTO = {
      account_ref: 'ppf_x',
      name: 'codex-b',
      driver: 'codex',
      environment_ref: 'xenv_1',
      state: 'active',
      home_mode: 'managed',
      home_generation: 0,
      home_relative: '',
      isolation_level: 'shared',
      auth_source: 'provider_account_home',
      identity: '',
      identity_source: 'none',
      created_at: '2026-10-01T10:00:00Z',
      updated_at: '2026-10-01T10:00:00Z',
    }
    vi.spyOn(signInApi, 'start').mockRejectedValue(
      new ApiError(500, 'internal', 'The login did not start.'),
    )
    wrap(
      <ProfileSignInDialog
        driver="codex"
        name="codex-b"
        account={account}
        tenant="tnt-a"
        onOpenChange={() => undefined}
      />,
    )
    const button = await screen.findByRole('button', {
      name: 'Sign in with ChatGPT',
    })
    expect(button).toHaveClass(FILLED)
  })

  it('is a quiet button on a setup checklist card, one of several', async () => {
    wrap(<ToolCard driver="codex" part="signIn" />)
    const button = await screen.findByRole('button', {
      name: 'Sign in with ChatGPT',
    })
    expect(button).not.toHaveClass(FILLED)
  })
})
