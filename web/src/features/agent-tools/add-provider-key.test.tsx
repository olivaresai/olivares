// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// AI tools offers its next step in the first viewport (#3388): a person who may
// add a provider key finds "Add provider key" in the page's header, above the tools and the
// accounts, and it opens the same form as the key list's own button.
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
import { providersApi } from '@/features/providers/api'
import { AgentToolsView } from './agent-tools-view'

function mount() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={qc}>
      <AgentToolsView />
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
})

describe('Add provider key on AI tools', () => {
  it('is the page header action and opens the add form', async () => {
    auth.perms = new Set(['sessions:provider:read', 'sessions:provider:write'])
    const user = userEvent.setup()
    mount()
    const add = await screen.findByRole('button', { name: 'Add provider key' })
    const accounts = await screen.findByRole('heading', {
      name: 'Accounts and API keys',
    })
    // The first viewport: the action precedes the accounts and keys in reading order.
    expect(
      add.compareDocumentPosition(accounts) & Node.DOCUMENT_POSITION_FOLLOWING,
    ).toBeTruthy()
    await user.click(add)
    expect(
      await screen.findByRole('dialog', { name: 'Add a provider' }),
    ).toBeInTheDocument()
  })

  it('is not offered to a reader who cannot add a key', async () => {
    auth.perms = new Set(['sessions:provider:read'])
    mount()
    await screen.findByRole('heading', { name: 'Accounts and API keys' })
    expect(
      screen.queryByRole('button', { name: 'Add provider key' }),
    ).not.toBeInTheDocument()
  })
})
