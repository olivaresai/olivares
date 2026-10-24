// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The accounts and API keys on Tools: each part under its own read tier. A reader of one
// sees that part and asks the engine for nothing of the other. A profile or binding
// reader with no account to open them through gets their own list, as their doors did.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'

const auth = vi.hoisted(() => ({ perms: new Set<string>() }))
vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({
    activeTenant: 't1',
    can: (p: string) => auth.perms.has(p),
    isSuperadmin: false,
    principal: { user_id: 'u1', aal: 1 },
  }),
}))

import { agentOpsApi } from '@/features/agentops/api'
import type { ProviderAccountDTO } from '@/features/agentops/types'
import { providersApi } from '@/features/providers/api'
import { AccountsAndKeys } from './accounts-and-keys'

const workClaude: ProviderAccountDTO = {
  account_ref: 'ppf_a',
  name: 'work-claude',
  driver: 'claude',
  environment_ref: 'xenv_1',
  state: 'active',
  home_mode: 'adopted',
  home_generation: 0,
  home_relative: '',
  isolation_level: 'shared',
  auth_source: 'provider_account_home',
  identity: '',
  identity_source: 'none',
  created_at: '2026-09-01T10:00:00Z',
  updated_at: '2026-09-01T10:00:00Z',
}

function mount(folded = false) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={qc}>
      <AccountsAndKeys folded={folded} />
    </QueryClientProvider>,
  )
}

beforeEach(() => {
  vi.restoreAllMocks()
  vi.spyOn(agentOpsApi, 'listAccounts').mockResolvedValue({
    items: [],
    has_more: false,
  })
  vi.spyOn(providersApi, 'list').mockResolvedValue({
    items: [],
    has_more: false,
  })
  vi.spyOn(agentOpsApi, 'listProfiles').mockResolvedValue({
    items: [],
    has_more: false,
  })
  vi.spyOn(agentOpsApi, 'listBindings').mockResolvedValue({
    items: [],
    has_more: false,
  })
})

describe('accounts and API keys on Tools', () => {
  it('an account-only reader gets the accounts and no key request', async () => {
    auth.perms = new Set(['sessions:account:read'])
    mount()
    expect(
      await screen.findByRole('heading', { name: 'Accounts' }),
    ).toBeInTheDocument()
    expect(screen.queryByRole('heading', { name: 'API keys' })).toBeNull()
    expect(agentOpsApi.listAccounts).toHaveBeenCalled()
    expect(providersApi.list).not.toHaveBeenCalled()
  })

  it('a key-only reader gets the API keys and no account request', async () => {
    auth.perms = new Set(['sessions:provider:read'])
    mount()
    expect(
      await screen.findByRole('heading', { name: 'API keys' }),
    ).toBeInTheDocument()
    expect(screen.queryByRole('heading', { name: 'Accounts' })).toBeNull()
    expect(providersApi.list).toHaveBeenCalled()
    expect(agentOpsApi.listAccounts).not.toHaveBeenCalled()
  })

  it('folded, the accounts are read only once opened', async () => {
    // A deployment administrator already has every account as a row of its tool.
    auth.perms = new Set(['sessions:account:read'])
    mount(true)
    const summary = screen.getByText(
      'All accounts: details, labels and bindings',
    )
    expect(screen.queryByRole('heading', { name: 'Accounts' })).toBeNull()
    expect(agentOpsApi.listAccounts).not.toHaveBeenCalled()
    await userEvent.setup().click(summary)
    expect(
      await screen.findByText('No provider accounts', { exact: false }),
    ).toBeInTheDocument()
    expect(agentOpsApi.listAccounts).toHaveBeenCalled()
    // Closing it again keeps the list (and any open detail or draft) mounted.
    await userEvent.setup().click(summary)
    expect(
      screen.getByText('No provider accounts', { exact: false }),
    ).toBeInTheDocument()
  })

  // The list used to be a page of its own and reached the fold. On Tools it shares the
  // screen with the API keys: one account row must not push them a screen down.
  it.each([
    ['empty', []],
    ['with one account', [workClaude]],
  ])(
    'the account list (%s) takes its own height beside the API keys, not the screen',
    async (_, items) => {
      auth.perms = new Set(['sessions:account:read', 'sessions:provider:read'])
      vi.mocked(agentOpsApi.listAccounts).mockResolvedValue({
        items,
        has_more: false,
      })
      const { container } = mount()
      await screen.findByRole('heading', { name: 'API keys' })
      await (items.length
        ? screen.findByText('work-claude')
        : screen.findByText('No provider accounts', { exact: false }))
      const regions = container.querySelectorAll('[data-slot="table-region"]')
      expect(regions.length).toBeGreaterThan(0)
      for (const region of regions)
        expect(region.className).not.toMatch(/100svh/)
    },
  )

  it('a profile-only reader gets the profiles, and nothing of accounts, keys or bindings', async () => {
    auth.perms = new Set(['sessions:profile:read'])
    mount()
    expect(
      await screen.findByRole('heading', { name: 'Provider profiles' }),
    ).toBeInTheDocument()
    expect(
      await screen.findByText('No provider profiles', { exact: false }),
    ).toBeInTheDocument()
    expect(
      screen.queryByRole('heading', { name: 'Source bindings' }),
    ).toBeNull()
    expect(agentOpsApi.listAccounts).not.toHaveBeenCalled()
    expect(providersApi.list).not.toHaveBeenCalled()
    expect(agentOpsApi.listBindings).not.toHaveBeenCalled()
  })

  it('a binding-only reader gets the source bindings, and nothing of profiles, accounts or keys', async () => {
    auth.perms = new Set(['sessions:profile-binding:read'])
    mount()
    expect(
      await screen.findByRole('heading', { name: 'Source bindings' }),
    ).toBeInTheDocument()
    expect(
      await screen.findByText('No source bindings', { exact: false }),
    ).toBeInTheDocument()
    expect(agentOpsApi.listBindings).toHaveBeenCalled()
    expect(agentOpsApi.listProfiles).not.toHaveBeenCalled()
    expect(agentOpsApi.listAccounts).not.toHaveBeenCalled()
    expect(providersApi.list).not.toHaveBeenCalled()
  })

  it('an account reader without profile read keeps the source bindings, which no account detail can show them', async () => {
    auth.perms = new Set([
      'sessions:account:read',
      'sessions:profile-binding:read',
    ])
    mount()
    expect(
      await screen.findByRole('heading', { name: 'Accounts' }),
    ).toBeInTheDocument()
    expect(
      screen.getByRole('heading', { name: 'Source bindings' }),
    ).toBeInTheDocument()
    expect(agentOpsApi.listBindings).toHaveBeenCalled()
  })

  it('a reader of accounts and profiles finds profiles and bindings in each account, not again on the page', async () => {
    auth.perms = new Set([
      'sessions:account:read',
      'sessions:profile:read',
      'sessions:profile-binding:read',
    ])
    mount()
    expect(
      await screen.findByRole('heading', { name: 'Accounts' }),
    ).toBeInTheDocument()
    expect(
      screen.queryByRole('heading', { name: 'Provider profiles' }),
    ).toBeNull()
    expect(
      screen.queryByRole('heading', { name: 'Source bindings' }),
    ).toBeNull()
    expect(agentOpsApi.listProfiles).not.toHaveBeenCalled()
    expect(agentOpsApi.listBindings).not.toHaveBeenCalled()
  })

  it('a reader of none of them is told so and sends nothing', () => {
    auth.perms = new Set(['sessions:run:read'])
    mount()
    expect(screen.getByRole('status')).toHaveTextContent(
      'Your role cannot read the accounts or API keys of this organization.',
    )
    expect(screen.queryByRole('heading')).toBeNull()
    expect(agentOpsApi.listAccounts).not.toHaveBeenCalled()
    expect(providersApi.list).not.toHaveBeenCalled()
    expect(agentOpsApi.listProfiles).not.toHaveBeenCalled()
    expect(agentOpsApi.listBindings).not.toHaveBeenCalled()
  })
})
