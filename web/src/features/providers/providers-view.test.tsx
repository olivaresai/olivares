// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Providers screen acceptance. Three properties, each of them a measured complaint:
//  1. the empty state names the next action, because on a clean install it is the
//     first thing this screen ever says;
//  2. a registered provider is never shown as working — only a connection test is
//     evidence, and an untested one says so;
//  3. a provider that the PROVIDER refused is reported as a refusal, not as a failed
//     request, because the two send an operator to different places.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import type { ReactElement, ReactNode } from 'react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import './i18n'

const { providersApi, authState } = vi.hoisted(() => ({
  providersApi: {
    list: vi.fn(),
    test: vi.fn(),
    create: vi.fn(),
    patch: vi.fn(),
    revoke: vi.fn(),
  },
  authState: {
    can: (_p: string): boolean => true,
    activeTenant: 't1' as string | null,
    principal: null as unknown,
  },
}))

vi.mock('@/lib/auth/context', () => ({ useAuth: () => authState }))
vi.mock('@/features/identity/assurance', () => ({
  AAL: { PASSWORD: 1, MFA: 2, HARDWARE: 3 },
  RequireAssurance: ({ children }: { children: ReactNode }) => <>{children}</>,
}))
vi.mock('@tanstack/react-router', () => ({
  useRouterState: () => '',
  Link: ({ to, children }: { to: string; children: ReactNode }) => (
    <a href={to}>{children}</a>
  ),
}))
vi.mock('./api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./api')>()
  return { ...actual, providersApi }
})

import { ProvidersView } from './providers-view'

const KEY = 'sk-ant-api03-LEAKCANARY-0123456789-ABCD'

function wrap(ui: ReactElement) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(<QueryClientProvider client={qc}>{ui}</QueryClientProvider>)
}

const registered = {
  provider_ref: 'prv_1',
  kind: 'anthropic' as const,
  display_name: 'Anthropic (prod)',
  key_hint: '…ABCD',
  state: 'active' as const,
}

beforeEach(() => {
  vi.clearAllMocks()
  authState.can = () => true
  providersApi.list.mockResolvedValue({ items: [] })
})

describe('ProvidersView', () => {
  it('names the next action when nothing is registered', async () => {
    wrap(<ProvidersView />)
    expect(
      await screen.findByText(/no provider registered yet/i),
    ).toBeInTheDocument()
    expect(
      screen.getByRole('button', { name: /add your first provider/i }),
    ).toBeInTheDocument()
  })

  it('does NOT show a registered-but-untested provider as working', async () => {
    providersApi.list.mockResolvedValue({ items: [registered] })
    wrap(<ProvidersView />)
    expect(await screen.findByText('Anthropic (prod)')).toBeInTheDocument()
    expect(screen.getByText(/^Not tested$/i)).toBeInTheDocument()
    expect(screen.queryByText(/^Accepted$/i)).not.toBeInTheDocument()
    // And the next-step panel is NOT offered: nothing here is known to work yet.
    expect(screen.queryByText(/next: deploy an agent/i)).not.toBeInTheDocument()
  })

  it('reports a provider-side refusal as a refusal, not as a broken request', async () => {
    providersApi.list.mockResolvedValue({ items: [registered] })
    providersApi.test.mockResolvedValue({
      ...registered,
      probe_state: 'refused',
      probe_detail: 'the provider answered 401 for this credential',
    })
    const user = userEvent.setup()
    wrap(<ProvidersView />)
    await user.click(
      await screen.findByRole('button', { name: /test connection/i }),
    )
    expect(providersApi.test).toHaveBeenCalledWith('prv_1')
  })

  it('offers the next step once a provider answered a test', async () => {
    providersApi.list.mockResolvedValue({
      items: [{ ...registered, probe_state: 'ok', models: ['claude-opus-5'] }],
    })
    wrap(<ProvidersView />)
    expect(
      await screen.findByText(/next: deploy an agent/i),
    ).toBeInTheDocument()
  })

  it('never renders a credential: only the four-character hint', async () => {
    providersApi.list.mockResolvedValue({
      items: [{ ...registered, probe_state: 'ok' }],
    })
    const { container } = wrap(<ProvidersView />)
    await screen.findByText('Anthropic (prod)')
    expect(container.textContent ?? '').not.toContain(KEY)
    expect(screen.getByText('…ABCD')).toBeInTheDocument()
  })

  it('offers no write control to a read-only principal', async () => {
    authState.can = (p: string) => p === 'sessions:provider:read'
    providersApi.list.mockResolvedValue({ items: [registered] })
    wrap(<ProvidersView />)
    await screen.findByText('Anthropic (prod)')
    expect(
      screen.queryByRole('button', { name: /add provider/i }),
    ).not.toBeInTheDocument()
    expect(
      screen.queryByRole('button', { name: /test connection/i }),
    ).not.toBeInTheDocument()
    expect(
      screen.queryByRole('button', { name: /revoke/i }),
    ).not.toBeInTheDocument()
  })

  it('renders the calm permission boundary when the read tier is absent', () => {
    authState.can = () => false
    wrap(<ProvidersView />)
    expect(
      screen.getByText(/you cannot read the providers of this tenant/i),
    ).toBeInTheDocument()
  })
})
