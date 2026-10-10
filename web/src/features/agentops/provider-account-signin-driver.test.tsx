// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Which accounts the console offers an official sign-in is the engine's answer, not a
// list of tool names in the console: an account whose tool the engine can sign in gets the
// flow, and one whose tool it cannot is refused by the sign-in route itself (400).
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'

vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({
    activeTenant: 't1',
    can: () => true,
    isSuperadmin: true,
    principal: { user_id: 'u1', aal: 1 },
  }),
}))
const signIn = vi.hoisted(() => ({
  status: vi.fn(),
  start: vi.fn(),
  code: vi.fn(),
  get: vi.fn(),
  cancel: vi.fn(),
}))
vi.mock('@/features/first-hour/api', async (orig) => ({
  ...((await orig()) as object),
  signInApi: signIn,
}))

import { ApiError } from '@/lib/api/errors'
import { ProviderAccountSignIn } from './provider-account-signin'
import type { ProviderAccountDTO } from './types'

const account = (driver: string): ProviderAccountDTO => ({
  account_ref: 'ppf_x',
  name: `${driver}-b`,
  driver,
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
})

function mount(driver: string) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={qc}>
      <ProviderAccountSignIn account={account(driver)} />
    </QueryClientProvider>,
  )
}

beforeEach(() => vi.clearAllMocks())

describe('ProviderAccountSignIn — the engine decides which tools sign in', () => {
  it('offers the official sign-in to an OpenCode account, which the old list left out', async () => {
    signIn.status.mockResolvedValue({
      driver: 'opencode',
      installed: true,
      signed_in: false,
    })
    mount('opencode')
    expect(
      await screen.findByRole('button', { name: 'Sign in with ChatGPT' }),
    ).toBeInTheDocument()
    expect(signIn.status).toHaveBeenCalledWith(
      'opencode',
      't1',
      expect.anything(),
      'ppf_x',
      expect.anything(),
    )
  })

  it('asks the sign-in route about a tool it has never heard of, and offers its sign-in when the route accepts it', async () => {
    signIn.status.mockResolvedValue({
      driver: 'acme-agent',
      installed: true,
      signed_in: false,
    })
    mount('acme-agent')
    expect(
      await screen.findByRole('button', { name: 'Sign in' }),
    ).toBeInTheDocument()
  })

  it('shows nothing for a tool the sign-in route refuses', async () => {
    signIn.status.mockRejectedValue(
      new ApiError(
        400,
        'bad_request',
        'Choose claude, codex, grok or opencode.',
      ),
    )
    const { container } = mount('some-tool')
    await vi.waitFor(() => expect(signIn.status).toHaveBeenCalled())
    await vi.waitFor(() => expect(container).toBeEmptyDOMElement())
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()
  })
})
