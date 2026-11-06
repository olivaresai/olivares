// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The badge an account wears wherever it is shown: its accent dot and its short label (the
// display name, else the account name). One read of the accounts feeds every badge.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, waitFor } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'

const { auth } = vi.hoisted(() => ({
  auth: {
    isSuperadmin: true,
    can: (_permission: string): boolean => true,
    activeTenant: 't1' as string | null,
    principal: { user_id: 'root', aal: 1 },
  },
}))
vi.mock('@/lib/auth/context', () => ({ useAuth: () => auth }))

import { agentOpsApi } from '@/features/agentops/api'
import type { ProviderAccountDTO } from '@/features/agentops/types'
import { AccountBadge, AccountMarksProvider } from './account-marks'

const account = (
  ref: string,
  name: string,
  over: Partial<ProviderAccountDTO> = {},
): ProviderAccountDTO => ({
  account_ref: ref,
  name,
  driver: 'claude',
  environment_ref: 'xenv_1',
  state: 'active',
  home_mode: 'managed',
  home_generation: 1,
  home_relative: '',
  isolation_level: 'shared',
  auth_source: 'provider_account_home',
  identity: '',
  identity_source: 'none',
  created_at: '2026-10-01T10:00:00Z',
  updated_at: '2026-10-01T10:00:00Z',
  ...over,
})

const page = <T,>(items: T[]) => ({ items, has_more: false })

function mount(profileRef: string | undefined, client = new QueryClient()) {
  const utils = render(
    <QueryClientProvider client={client}>
      <AccountMarksProvider>
        <AccountBadge profileRef={profileRef} />
      </AccountMarksProvider>
    </QueryClientProvider>,
  )
  return { ...utils, client }
}

beforeEach(() => {
  vi.restoreAllMocks()
  auth.can = () => true
  vi.spyOn(agentOpsApi, 'listProfiles').mockResolvedValue(page([]))
})

describe('AccountBadge', () => {
  it('wears the display name and the accent of the account behind a profile', async () => {
    vi.spyOn(agentOpsApi, 'listAccounts').mockResolvedValue(
      page([
        account('ppf_b', 'claude-b', {
          display_name: 'Research',
          accent: 'teal',
        }),
      ]),
    )
    const { container } = mount('ppf_b')
    expect(await screen.findByText('Research')).toBeInTheDocument()
    expect(
      container.querySelector('[data-provider-accent="teal"]'),
    ).not.toBeNull()
  })

  it('falls back to the account name, with no dot when there is no accent', async () => {
    vi.spyOn(agentOpsApi, 'listAccounts').mockResolvedValue(
      page([account('ppf_b', 'claude-b')]),
    )
    const { container } = mount('ppf_b')
    expect(await screen.findByText('claude-b')).toBeInTheDocument()
    expect(container.querySelector('[data-provider-accent]')).toBeNull()
  })

  it('does not paint a stored color it does not know', async () => {
    vi.spyOn(agentOpsApi, 'listAccounts').mockResolvedValue(
      page([account('ppf_b', 'claude-b', { accent: 'var(--danger)' })]),
    )
    const { container } = mount('ppf_b')
    expect(await screen.findByText('claude-b')).toBeInTheDocument()
    expect(container.querySelector('[data-provider-accent]')).toBeNull()
  })

  it.each([undefined, 'ppf_unknown'])(
    'says nothing for the profile %s it cannot place',
    async (ref) => {
      const list = vi
        .spyOn(agentOpsApi, 'listAccounts')
        .mockResolvedValue(page([account('ppf_b', 'claude-b')]))
      const { container } = mount(ref)
      await waitFor(() => expect(list).toHaveBeenCalled())
      await waitFor(() => expect(container).toBeEmptyDOMElement())
    },
  )

  it('asks for nothing and shows nothing to a reader who may not list accounts', async () => {
    auth.can = (permission) => permission !== 'sessions:account:read'
    const list = vi
      .spyOn(agentOpsApi, 'listAccounts')
      .mockResolvedValue(page([account('ppf_b', 'claude-b')]))
    const { container } = mount('ppf_b')
    await new Promise((resolve) => setTimeout(resolve, 20))
    expect(list).not.toHaveBeenCalled()
    expect(container).toBeEmptyDOMElement()
  })

  it('asks for nothing while no tenant is active', async () => {
    auth.activeTenant = null
    const list = vi
      .spyOn(agentOpsApi, 'listAccounts')
      .mockResolvedValue(page([account('ppf_b', 'claude-b')]))
    try {
      const { container } = mount('ppf_b')
      await new Promise((resolve) => setTimeout(resolve, 20))
      expect(list).not.toHaveBeenCalled()
      expect(container).toBeEmptyDOMElement()
    } finally {
      auth.activeTenant = 't1'
    }
  })

  it('follows a changed accent when the accounts are read again, with no reload', async () => {
    const list = vi
      .spyOn(agentOpsApi, 'listAccounts')
      .mockResolvedValue(
        page([account('ppf_b', 'claude-b', { accent: 'blue' })]),
      )
    const { container, client } = mount('ppf_b')
    await waitFor(() =>
      expect(
        container.querySelector('[data-provider-accent="blue"]'),
      ).not.toBeNull(),
    )
    list.mockResolvedValue(
      page([account('ppf_b', 'claude-b', { accent: 'purple' })]),
    )
    await client.invalidateQueries()
    await waitFor(() =>
      expect(
        container.querySelector('[data-provider-accent="purple"]'),
      ).not.toBeNull(),
    )
    expect(container.querySelector('[data-provider-accent="blue"]')).toBeNull()
  })

  it('renders nothing, and needs no server state, outside a provider', () => {
    const { container } = render(<AccountBadge profileRef="ppf_b" />)
    expect(container).toBeEmptyDOMElement()
  })
})
