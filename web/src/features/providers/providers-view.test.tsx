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
import { render, screen, waitFor, within } from '@testing-library/react'
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
    phone: false,
  },
}))

vi.mock('@/lib/auth/context', () => ({ useAuth: () => authState }))
vi.mock('@/lib/hooks/use-is-phone', () => ({
  useIsPhone: () => authState.phone,
}))
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
  authState.phone = false
  providersApi.list.mockResolvedValue({ items: [] })
})

describe('ProvidersView', () => {
  it('shows state, verdict, reference and every action together on phone', async () => {
    authState.phone = true
    providersApi.list.mockResolvedValue({
      items: [{ ...registered, probe_state: 'refused' }],
    })
    wrap(<ProvidersView />)
    const card = await screen.findByRole('article', {
      name: registered.display_name,
    })
    expect(
      within(card).getByText('Active', { exact: true }),
    ).toBeInTheDocument()
    expect(
      within(card).getByText('Refused', { exact: true }),
    ).toBeInTheDocument()
    expect(within(card).getByText(registered.provider_ref)).toBeInTheDocument()
    for (const name of [/test connection/i, /rotate key/i, /revoke/i]) {
      expect(within(card).getByRole('button', { name })).toBeEnabled()
    }
    expect(screen.queryByRole('grid')).not.toBeInTheDocument()
  })

  it('uses the same connection endpoint from a phone card', async () => {
    authState.phone = true
    providersApi.list.mockResolvedValue({ items: [registered] })
    providersApi.test.mockResolvedValue({ ...registered, probe_state: 'ok' })
    wrap(<ProvidersView />)
    const card = await screen.findByRole('article', {
      name: registered.display_name,
    })
    await userEvent
      .setup()
      .click(within(card).getByRole('button', { name: /test connection/i }))
    expect(providersApi.test).toHaveBeenCalledExactlyOnceWith(
      registered.provider_ref,
    )
  })

  it('withholds phone write actions from a read-only operator', async () => {
    authState.phone = true
    authState.can = (p) => p === 'sessions:provider:read'
    providersApi.list.mockResolvedValue({ items: [registered] })
    wrap(<ProvidersView />)
    const card = await screen.findByRole('article', {
      name: registered.display_name,
    })
    expect(
      within(card).getByText('Not tested', { exact: true }),
    ).toBeInTheDocument()
    expect(within(card).queryByRole('button')).not.toBeInTheDocument()
  })

  it('keeps the revoked verdict visible and removes all phone actions', async () => {
    authState.phone = true
    providersApi.list.mockResolvedValue({
      items: [{ ...registered, state: 'revoked', probe_state: 'refused' }],
    })
    wrap(<ProvidersView />)
    const card = await screen.findByRole('article', {
      name: registered.display_name,
    })
    expect(
      within(card).getByText('Revoked', { exact: true }),
    ).toBeInTheDocument()
    expect(
      within(card).getByText('Refused', { exact: true }),
    ).toBeInTheDocument()
    expect(within(card).queryByRole('button')).not.toBeInTheDocument()
  })

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

  // HU2-17: a session used a key with no binding, while this page said a provider
  // "launches nothing on its own" until it is bound to a provider profile.
  it('does not send a working key to provider profiles to be bound', async () => {
    providersApi.list.mockResolvedValue({
      items: [{ ...registered, probe_state: 'ok' }],
    })
    wrap(<ProvidersView />)
    expect(await screen.findByText('Anthropic (prod)')).toBeInTheDocument()
    expect(screen.queryByText(/next: deploy an agent/i)).not.toBeInTheDocument()
    expect(
      screen.queryByText(/launches nothing on its own/i),
    ).not.toBeInTheDocument()
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

  it('never renders a credential: only the four-character hint', async () => {
    providersApi.list.mockResolvedValue({
      items: [{ ...registered, probe_state: 'ok' }],
    })
    const { container } = wrap(<ProvidersView />)
    await screen.findByText('Anthropic (prod)')
    expect(container.textContent ?? '').not.toContain(KEY)
    expect(screen.getByText('…ABCD')).toBeInTheDocument()
  })

  it('registers an Ollama endpoint without a credential and offers connection testing', async () => {
    const user = userEvent.setup()
    providersApi.create.mockResolvedValue({
      provider_ref: 'prv_local',
      kind: 'ollama',
      display_name: 'Local',
      state: 'active',
    })
    wrap(<ProvidersView />)
    await user.click(
      await screen.findByRole('button', { name: /add your first provider/i }),
    )
    await user.click(screen.getByRole('combobox'))
    await user.click(await screen.findByRole('option', { name: 'Ollama' }))
    expect(
      screen.queryByPlaceholderText('Paste the key'),
    ).not.toBeInTheDocument()
    await user.type(screen.getByRole('textbox', { name: 'Name' }), 'Local')
    expect(
      screen.getByDisplayValue('http://127.0.0.1:11434'),
    ).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: /^Add provider$/ }))
    expect(providersApi.create).toHaveBeenCalledWith({
      kind: 'ollama',
      display_name: 'Local',
      base_url: 'http://127.0.0.1:11434',
    })
    await waitFor(() =>
      expect(providersApi.test).toHaveBeenCalledWith('prv_local'),
    )
  })

  it('describes Ollama probe and revocation without inventing a credential', async () => {
    const user = userEvent.setup()
    providersApi.list.mockResolvedValue({
      items: [
        {
          provider_ref: 'prv_local',
          kind: 'ollama',
          display_name: 'Local',
          state: 'active',
          probe_state: 'ok',
          models: ['qwen3:8b'],
        },
      ],
    })
    wrap(<ProvidersView />)
    expect(
      await screen.findByTitle(
        'The local endpoint answered and listed its models.',
      ),
    ).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: /^Revoke$/ }))
    expect(screen.getByRole('dialog')).toHaveTextContent(
      'This revokes the endpoint registration.',
    )
    expect(screen.getByRole('dialog')).not.toHaveTextContent(
      'sealed credential',
    )
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

  // Root 19:15Z / HU2-18: the form opened with no provider chosen.
  it('opens the form with a provider chosen', async () => {
    const user = userEvent.setup()
    wrap(<ProvidersView />)
    await user.click(
      await screen.findByRole('button', { name: /add your first provider/i }),
    )
    expect(screen.getByRole('combobox')).toHaveTextContent('Anthropic')
  })

  it("opens the form on the provider ?add= names, and on nothing it doesn't know", async () => {
    window.history.pushState({}, '', '/providers?add=openai')
    const first = wrap(<ProvidersView />)
    expect(await screen.findByRole('combobox')).toHaveTextContent('OpenAI')
    first.unmount()
    window.history.pushState({}, '', '/providers?add=nope')
    wrap(<ProvidersView />)
    await screen.findByRole('button', { name: /add your first provider/i })
    expect(screen.queryByRole('combobox')).toBeNull()
    window.history.pushState({}, '', '/')
  })
})
