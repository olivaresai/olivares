// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// HU-R15 (refresh 06): Grok Build installed from the console and then offered no
// sign-in. Its own device login (grok login --device-auth) is relayed like Codex's:
// the row offers it, then shows the link and the one-time code; an xAI API key from
// Providers stays the other way.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, expect, it, vi } from 'vitest'
import './i18n'

const { api, auth, signIn } = vi.hoisted(() => ({
  api: {
    inventory: vi.fn(),
    plan: vi.fn(),
    install: vi.fn(),
    job: vi.fn(),
    detect: vi.fn(),
  },
  auth: {
    isSuperadmin: true,
    activeTenant: null,
    principal: { user_id: 'root' },
  },
  signIn: { status: vi.fn(), start: vi.fn(), get: vi.fn(), cancel: vi.fn() },
}))
vi.mock('@/lib/auth/context', () => ({ useAuth: () => auth }))
vi.mock('./api', () => ({ agentToolsApi: api }))
vi.mock('@/components/ui/toaster', () => ({
  toast: { success: vi.fn(), error: vi.fn(), warning: vi.fn() },
}))
vi.mock('@/features/first-hour/api', async (orig) => ({
  ...((await orig()) as object),
  signInApi: signIn,
  listProviderKeys: () => Promise.resolve([]),
}))

import { useTenantStore } from '@/stores/tenant'
import { AgentToolsView } from './agent-tools-view'

beforeEach(() => {
  useTenantStore.setState({ activeTenant: 'tenant-a' })
  vi.clearAllMocks()
  api.inventory.mockResolvedValue({
    drivers: ['grok'],
    inventory: {
      installed: [{ driver: 'grok', version: '1.0.46', state: 'installed' }],
      leftovers: [],
    },
    read_only: false,
    jobs: [],
  })
  signIn.status.mockImplementation((driver: string) =>
    Promise.resolve({ driver, installed: true, signed_in: false }),
  )
  signIn.start.mockResolvedValue({
    id: 'si1',
    driver: 'grok',
    state: 'waiting',
    url: 'https://accounts.x.ai/device',
    user_code: 'K7M2QX9P',
  })
  signIn.get.mockResolvedValue({
    id: 'si1',
    driver: 'grok',
    state: 'waiting',
    url: 'https://accounts.x.ai/device',
    user_code: 'K7M2QX9P',
  })
})

it('offers Grok Build its own sign-in and relays the link and the code', async () => {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  render(
    <QueryClientProvider client={qc}>
      <AgentToolsView />
    </QueryClientProvider>,
  )
  const card = await screen.findByTestId('tool-grok')
  expect(await within(card).findByText('Not signed in')).toBeInTheDocument()
  expect(
    within(card).getByRole('link', { name: 'Use an API key instead' }),
  ).toHaveAttribute('href', '/providers')
  // Each tool card carries the API key link; the page adds no copy of it (Root's capture
  // review, 26.10.1).
  for (const link of screen.getAllByRole('link', {
    name: 'Use an API key instead',
  })) {
    expect(link.closest('[data-testid^="tool-"]')).not.toBeNull()
  }
  await userEvent.click(
    within(card).getByRole('button', { name: 'Sign in with your xAI account' }),
  )
  // The login is the organization's own (FH 036): the start names it.
  expect(signIn.start).toHaveBeenCalledWith('grok', 'tenant-a')
  expect(await within(card).findByTestId('device-code')).toHaveTextContent(
    'K7M2QX9P',
  )
  expect(
    within(card).getByRole('link', { name: /open the sign-in page/i }),
  ).toHaveAttribute('href', 'https://accounts.x.ai/device')
})
