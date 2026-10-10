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
import { act, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import type { ReactElement, ReactNode } from 'react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { ApiError } from '@/lib/api/errors'
import { useStepUpStore } from '@/stores/step-up'
import './i18n'

const { providersApi, authState, toast, navigate, preparation } = vi.hoisted(
  () => ({
    preparation: { status: vi.fn(), install: vi.fn(), resolve: vi.fn() },
    toast: {
      success: vi.fn(),
      error: vi.fn(),
      warning: vi.fn(),
      info: vi.fn(),
    },
    navigate: vi.fn(),
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
  }),
)

vi.mock('@/lib/auth/context', () => ({ useAuth: () => authState }))
vi.mock('@/components/ui/toaster', () => ({ toast, Toaster: () => null }))
vi.mock('@/lib/hooks/use-is-phone', () => ({
  useIsPhone: () => authState.phone,
}))
vi.mock('@/features/identity/assurance', () => ({
  AAL: { PASSWORD: 1, MFA: 2, HARDWARE: 3 },
  RequireAssurance: ({ children }: { children: ReactNode }) => <>{children}</>,
}))
vi.mock('@tanstack/react-router', () => ({
  useRouterState: () => '',
  useNavigate: () => navigate,
  Link: ({ to, children }: { to: string; children: ReactNode }) => (
    <a href={to}>{children}</a>
  ),
}))
vi.mock('./api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./api')>()
  return { ...actual, providersApi }
})

vi.mock('@/features/first-hour/api', async (orig) => ({
  ...(await orig<typeof import('@/features/first-hour/api')>()),
  signInApi: { status: preparation.status },
  installLatest: preparation.install,
}))
vi.mock('@/features/agentops/api', async (orig) => {
  const actual = await orig<typeof import('@/features/agentops/api')>()
  return {
    ...actual,
    agentOpsApi: { ...actual.agentOpsApi, resolveProfile: preparation.resolve },
  }
})

import { firstHourKeys } from '@/features/first-hour/api'
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
  useStepUpStore.setState({ request: null })
  authState.can = () => true
  authState.phone = false
  providersApi.list.mockResolvedValue({ items: [] })
  preparation.status.mockResolvedValue({ installed: true, signed_in: false })
  preparation.install.mockResolvedValue({ state: 'succeeded' })
  preparation.resolve.mockResolvedValue({
    profile: { profile_ref: 'ppf_ready' },
  })
})

describe('ProvidersView', () => {
  it('reads, changes and clears the saved default through the provider patch', async () => {
    const user = userEvent.setup()
    let record = {
      ...registered,
      models: ['coding-a', 'coding-b'],
      default_model: 'coding-a' as string | null,
    }
    providersApi.list.mockImplementation(async () => ({ items: [record] }))
    providersApi.patch.mockImplementation(
      async (_ref: string, body: { default_model: string }) => {
        record = { ...record, default_model: body.default_model || null }
        return record
      },
    )
    wrap(<ProvidersView />)
    await user.click(
      within(
        await screen.findByRole('row', { name: /Anthropic \(prod\)/ }),
      ).getByRole('button', { name: 'Row actions for Anthropic (prod)' }),
    )
    await user.click(
      await screen.findByRole('menuitem', { name: 'Default model' }),
    )
    const dialog = screen.getByRole('dialog', { name: 'Default model' })
    expect(within(dialog).getByRole('combobox', { name: 'Model' })).toHaveValue(
      'coding-a',
    )
    await user.selectOptions(
      within(dialog).getByRole('combobox', { name: 'Model' }),
      'coding-b',
    )
    await user.click(within(dialog).getByRole('button', { name: 'Save' }))
    await waitFor(() =>
      expect(screen.queryByRole('dialog')).not.toBeInTheDocument(),
    )
    expect(providersApi.patch).toHaveBeenLastCalledWith(
      registered.provider_ref,
      { default_model: 'coding-b' },
    )
    await user.click(
      within(screen.getByRole('row', { name: /Anthropic \(prod\)/ })).getByRole(
        'button',
        { name: 'Row actions for Anthropic (prod)' },
      ),
    )
    await user.click(
      await screen.findByRole('menuitem', { name: 'Default model' }),
    )
    const reopened = screen.getByRole('dialog')
    expect(
      within(reopened).getByRole('combobox', { name: 'Model' }),
    ).toHaveValue('coding-b')
    await user.selectOptions(
      within(reopened).getByRole('combobox', { name: 'Model' }),
      '',
    )
    await user.click(within(reopened).getByRole('button', { name: 'Save' }))
    await waitFor(() =>
      expect(providersApi.patch).toHaveBeenLastCalledWith(
        registered.provider_ref,
        { default_model: '' },
      ),
    )
    expect(providersApi.test).not.toHaveBeenCalled()
    expect(providersApi.create).not.toHaveBeenCalled()
  })
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

  // Whether a tool can start on a key is the engine's readiness answer, which carries
  // the key's last verdict: a test makes the first-hour screens ask again at once.
  it('a connection test makes the first hour ask the readiness again', async () => {
    providersApi.list.mockResolvedValue({ items: [registered] })
    providersApi.test.mockResolvedValue({ ...registered, probe_state: 'refused' })
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
    const readiness = firstHourKeys.readiness('t1')
    qc.setQueryData(readiness, { tools: [] })
    const user = userEvent.setup()
    render(
      <QueryClientProvider client={qc}>
        <ProvidersView />
      </QueryClientProvider>,
    )
    await user.click(
      await screen.findByRole('button', { name: /test connection/i }),
    )
    await waitFor(() =>
      expect(qc.getQueryState(readiness)?.isInvalidated).toBe(true),
    )
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

  it('saves an explicit local model on create without a prior connection test', async () => {
    const user = userEvent.setup()
    providersApi.create.mockResolvedValue({
      provider_ref: 'prv_local',
      kind: 'ollama',
      display_name: 'Ollama',
      state: 'active',
      default_model: 'coding-local',
    })
    providersApi.test.mockResolvedValue({
      provider_ref: 'prv_local',
      kind: 'ollama',
      state: 'active',
    })
    wrap(<ProvidersView />)
    await user.click(
      await screen.findByRole('button', { name: /add your first provider/i }),
    )
    await user.click(screen.getByRole('combobox'))
    await user.click(await screen.findByRole('option', { name: 'Ollama' }))
    await user.type(
      screen.getByRole('textbox', { name: 'Default model' }),
      'coding-local',
    )
    expect(providersApi.test).not.toHaveBeenCalled()
    await user.click(screen.getByRole('button', { name: /^Add provider$/ }))
    expect(providersApi.create).toHaveBeenCalledExactlyOnceWith({
      kind: 'ollama',
      display_name: 'Ollama',
      base_url: 'http://127.0.0.1:11434',
      default_model: 'coding-local',
    })
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
    await user.click(
      screen.getByRole('button', { name: 'Row actions for Local' }),
    )
    await user.click(await screen.findByRole('menuitem', { name: 'Revoke' }))
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

  it('prepares the missing tool and managed profile after adding a working key', async () => {
    const user = userEvent.setup()
    preparation.status.mockResolvedValue({ installed: false, signed_in: false })
    providersApi.create.mockResolvedValue(registered)
    providersApi.test.mockResolvedValue({ ...registered, probe_state: 'ok' })
    window.history.pushState({}, '', '/providers?add=anthropic')
    wrap(<ProvidersView />)
    const dialog = await screen.findByRole('dialog')
    await user.type(within(dialog).getByPlaceholderText('Paste the key'), KEY)
    await user.click(
      within(dialog).getByRole('button', { name: /^Add provider$/ }),
    )
    await waitFor(() => expect(preparation.install).toHaveBeenCalled())
    expect(preparation.install.mock.calls[0][0]).toBe('claude')
    await waitFor(() => expect(preparation.resolve).toHaveBeenCalled())
    expect(preparation.resolve.mock.calls[0][0]).toBe('claude')
    expect(
      await screen.findByRole('button', { name: 'New session' }),
    ).toBeVisible()
    expect(
      screen.queryByLabelText(/settings folder|user home|configuration home/i),
    ).not.toBeInTheDocument()
    window.history.pushState({}, '', '/')
  })

  it.each([
    ['openai', 'codex'],
    ['ollama', 'opencode'],
  ] as const)('prepares the missing %s tool %s', async (kind, driver) => {
    const user = userEvent.setup()
    preparation.status.mockResolvedValue({ installed: false, signed_in: false })
    const record = { ...registered, kind }
    providersApi.create.mockResolvedValue(record)
    providersApi.test.mockResolvedValue({ ...record, probe_state: 'ok' })
    window.history.pushState({}, '', `/providers?add=${kind}`)
    wrap(<ProvidersView />)
    const dialog = await screen.findByRole('dialog')
    // The wizard's "Add a local model" lands here: its address is already filled in (#476).
    if (kind === 'ollama')
      expect(
        within(dialog).getByRole('textbox', { name: 'Endpoint' }),
      ).toHaveValue('http://127.0.0.1:11434')
    else
      await user.type(within(dialog).getByPlaceholderText('Paste the key'), KEY)
    await user.click(
      within(dialog).getByRole('button', { name: /^Add provider$/ }),
    )
    await waitFor(() => expect(preparation.resolve).toHaveBeenCalled())
    expect(preparation.resolve.mock.calls[0][0]).toBe(driver)
    expect(preparation.install.mock.calls[0][0]).toBe(driver)
    window.history.pushState({}, '', '/')
  })

  it('keeps the saved provider and retries setup after an installation fails', async () => {
    const user = userEvent.setup()
    preparation.status.mockResolvedValue({ installed: false, signed_in: false })
    preparation.install.mockResolvedValueOnce({
      state: 'failed',
      error: 'Download unavailable',
    })
    providersApi.create.mockResolvedValue(registered)
    providersApi.test.mockResolvedValue({ ...registered, probe_state: 'ok' })
    window.history.pushState({}, '', '/providers?add=anthropic')
    wrap(<ProvidersView />)
    const dialog = await screen.findByRole('dialog')
    await user.type(within(dialog).getByPlaceholderText('Paste the key'), KEY)
    await user.click(
      within(dialog).getByRole('button', { name: /^Add provider$/ }),
    )
    expect(await screen.findByRole('alert')).toHaveTextContent(
      'Provider saved. Session setup failed: Download unavailable',
    )
    expect(preparation.resolve).not.toHaveBeenCalled()
    expect(
      screen.queryByPlaceholderText('Paste the key'),
    ).not.toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Retry setup' }))
    expect(
      await screen.findByRole('button', { name: 'New session' }),
    ).toBeVisible()
    expect(providersApi.create).toHaveBeenCalledTimes(1)
    expect(providersApi.test).toHaveBeenCalledTimes(1)
    window.history.pushState({}, '', '/')
  })

  it.each(['system:admin', 'sessions:profile:write'])(
    'never turns provider write into %s authority',
    async (permission) => {
      const user = userEvent.setup()
      authState.can = (p) => p !== permission
      providersApi.create.mockResolvedValue(registered)
      providersApi.test.mockResolvedValue({ ...registered, probe_state: 'ok' })
      window.history.pushState({}, '', '/providers?add=anthropic')
      wrap(<ProvidersView />)
      const dialog = await screen.findByRole('dialog')
      await user.type(within(dialog).getByPlaceholderText('Paste the key'), KEY)
      await user.click(
        within(dialog).getByRole('button', { name: /^Add provider$/ }),
      )
      await waitFor(() => expect(toast.success).toHaveBeenCalledTimes(2))
      expect(preparation.status).not.toHaveBeenCalled()
      expect(preparation.install).not.toHaveBeenCalled()
      expect(preparation.resolve).not.toHaveBeenCalled()
      window.history.pushState({}, '', '/')
    },
  )

  it('waits for required install assurance before resolving the profile', async () => {
    const user = userEvent.setup()
    preparation.status.mockResolvedValue({ installed: false, signed_in: false })
    preparation.install.mockRejectedValueOnce(
      new ApiError(403, 'step_up_required', 'Step-up required'),
    )
    providersApi.create.mockResolvedValue(registered)
    providersApi.test.mockResolvedValue({ ...registered, probe_state: 'ok' })
    window.history.pushState({}, '', '/providers?add=anthropic')
    wrap(<ProvidersView />)
    const dialog = await screen.findByRole('dialog')
    await user.type(within(dialog).getByPlaceholderText('Paste the key'), KEY)
    await user.click(
      within(dialog).getByRole('button', { name: /^Add provider$/ }),
    )
    await waitFor(() =>
      expect(useStepUpStore.getState().request).not.toBeNull(),
    )
    expect(preparation.resolve).not.toHaveBeenCalled()
    expect(screen.getByRole('button', { name: 'Add provider' })).toBeDisabled()
    expect(
      screen.getByRole('button', { name: 'Add provider' }),
    ).toHaveAccessibleDescription('This action needs an elevated session')
    expect(screen.getByRole('button', { name: 'Retry setup' })).toBeDisabled()
    await act(async () => {
      useStepUpStore.getState().request?.retry?.()
    })
    await waitFor(() => expect(preparation.resolve).toHaveBeenCalledOnce())
    expect(providersApi.create).toHaveBeenCalledOnce()
    window.history.pushState({}, '', '/')
  })

  it('shows the download instead of a spinner while the preparation installs (#1086)', async () => {
    const user = userEvent.setup()
    preparation.status.mockResolvedValue({ installed: false, signed_in: false })
    preparation.install.mockImplementation(
      (
        _driver: string,
        _authority: unknown,
        onProgress?: (job: unknown) => void,
      ) => {
        onProgress?.({
          id: 'j1',
          state: 'running',
          // The page shows how far the download is, never the job's URLs or paths.
          progress:
            '  downloading from https://example.invalid/tool.tar.gz\n  downloaded 1.0 MiB of 4.0 MiB (25%)\n  downloaded 4.0 MiB of 4.0 MiB (100%)\n  probing the staged executable: /var/lib/olivares/tools/claude/claude --version\n',
        })
        return new Promise(() => {})
      },
    )
    providersApi.create.mockResolvedValue(registered)
    providersApi.test.mockResolvedValue({ ...registered, probe_state: 'ok' })
    window.history.pushState({}, '', '/providers?add=anthropic')
    wrap(<ProvidersView />)
    const dialog = await screen.findByRole('dialog')
    await user.type(within(dialog).getByPlaceholderText('Paste the key'), KEY)
    await user.click(
      within(dialog).getByRole('button', { name: /^Add provider$/ }),
    )
    const installing =
      'Installing Claude Code for your first session: downloaded 4.0 MiB of 4.0 MiB (100%)'
    await waitFor(() =>
      expect(
        screen.getByRole('button', { name: 'Add provider' }),
      ).toHaveAccessibleDescription(installing),
    )
    const status = screen
      .getAllByRole('status')
      .find((node) => node.textContent === installing)
    expect(status).toBeDefined()
    expect(status!.querySelector('.animate-spin')).toBeNull()
    window.history.pushState({}, '', '/')
  })

  it('forgets a failed preparation\'s download before Retry (#1086)', async () => {
    const user = userEvent.setup()
    preparation.status.mockResolvedValue({ installed: false, signed_in: false })
    preparation.install
      .mockImplementationOnce(
        async (
          _driver: string,
          _authority: unknown,
          onProgress?: (job: unknown) => void,
        ) => {
          onProgress?.({
            id: 'j1',
            state: 'running',
            progress: '  downloaded 1.0 MiB of 4.0 MiB (25%)\n',
          })
          throw new Error('connection reset')
        },
      )
      .mockImplementationOnce(() => new Promise(() => {}))
    providersApi.create.mockResolvedValue(registered)
    providersApi.test.mockResolvedValue({ ...registered, probe_state: 'ok' })
    window.history.pushState({}, '', '/providers?add=anthropic')
    wrap(<ProvidersView />)
    const dialog = await screen.findByRole('dialog')
    await user.type(within(dialog).getByPlaceholderText('Paste the key'), KEY)
    await user.click(
      within(dialog).getByRole('button', { name: /^Add provider$/ }),
    )
    await user.click(await screen.findByRole('button', { name: 'Retry setup' }))
    expect(
      screen.getByRole('button', { name: 'Add provider' }),
    ).toHaveAccessibleDescription('Preparing Claude Code for your first session…')
    expect(screen.queryByText(/^Installing Claude Code/)).toBeNull()
    window.history.pushState({}, '', '/')
  })

  it('does not install after the preparing page leaves during status', async () => {
    const user = userEvent.setup()
    let finishStatus!: (value: { installed: boolean }) => void
    preparation.status.mockReturnValue(
      new Promise((resolve) => {
        finishStatus = resolve
      }),
    )
    providersApi.create.mockResolvedValue(registered)
    providersApi.test.mockResolvedValue({ ...registered, probe_state: 'ok' })
    window.history.pushState({}, '', '/providers?add=anthropic')
    const view = wrap(<ProvidersView />)
    const dialog = await screen.findByRole('dialog')
    await user.type(within(dialog).getByPlaceholderText('Paste the key'), KEY)
    await user.click(
      within(dialog).getByRole('button', { name: /^Add provider$/ }),
    )
    await waitFor(() => expect(preparation.status).toHaveBeenCalled())
    expect(screen.getByRole('button', { name: 'Add provider' })).toBeDisabled()
    expect(
      screen.getByRole('button', { name: 'Add your first provider' }),
    ).toBeDisabled()
    expect(
      screen.getByRole('button', { name: 'Add provider' }),
    ).toHaveAccessibleDescription(
      'Preparing Claude Code for your first session…',
    )
    expect(
      screen.getByRole('button', { name: 'Add your first provider' }),
    ).toHaveAccessibleDescription(
      'Preparing Claude Code for your first session…',
    )
    view.unmount()
    const authority = preparation.status.mock.calls[0][4]
    expect(authority.signal.aborted).toBe(true)
    finishStatus({ installed: false })
    await Promise.resolve()
    expect(preparation.install).not.toHaveBeenCalled()
    expect(preparation.resolve).not.toHaveBeenCalled()
    window.history.pushState({}, '', '/')
  })

  it('says the provider was added, and leaves the verdict to the test it runs (SC, binary 12)', async () => {
    const user = userEvent.setup()
    providersApi.create.mockResolvedValue(registered)
    providersApi.test.mockResolvedValue({
      ...registered,
      probe_state: 'refused',
    })
    window.history.pushState({}, '', '/providers?add=anthropic')
    wrap(<ProvidersView />)
    const dialog = await screen.findByRole('dialog')
    await user.type(within(dialog).getByPlaceholderText('Paste the key'), KEY)
    await user.click(
      within(dialog).getByRole('button', { name: /^Add provider$/ }),
    )
    await waitFor(() => expect(providersApi.test).toHaveBeenCalled())
    // A refused key is a warning with its remedy, never a green check.
    await waitFor(() =>
      expect(toast.warning).toHaveBeenCalledWith(
        'The provider answered and rejected this credential. Rotate it.',
        undefined,
      ),
    )
    expect(toast.success.mock.calls.map(([m]) => m)).toEqual([
      'Provider added.',
    ])
    expect(toast.warning).toHaveBeenCalledTimes(1)
    expect(preparation.install).not.toHaveBeenCalled()
    expect(preparation.resolve).not.toHaveBeenCalled()
    window.history.pushState({}, '', '/')
  })

  it('goes back where the key was asked for once it passes its test', async () => {
    const user = userEvent.setup()
    providersApi.create.mockResolvedValue(registered)
    providersApi.test.mockResolvedValue({ ...registered, probe_state: 'ok' })
    window.history.pushState(
      {},
      '',
      '/providers?add=anthropic&returnTo=%2Fonboarding',
    )
    wrap(<ProvidersView />)
    const dialog = await screen.findByRole('dialog')
    await user.type(within(dialog).getByPlaceholderText('Paste the key'), KEY)
    await user.click(
      within(dialog).getByRole('button', { name: /^Add provider$/ }),
    )
    await waitFor(() =>
      expect(navigate).toHaveBeenCalledWith({
        to: '/onboarding',
        href: '/onboarding',
      }),
    )
    expect(preparation.install).not.toHaveBeenCalled()
    window.history.pushState({}, '', '/')
  })

  it('stays when the new key is refused, and never goes to another origin', async () => {
    const user = userEvent.setup()
    providersApi.create.mockResolvedValue(registered)
    providersApi.test.mockResolvedValue({
      ...registered,
      probe_state: 'refused',
    })
    window.history.pushState(
      {},
      '',
      '/providers?add=anthropic&returnTo=%2Fonboarding',
    )
    const first = wrap(<ProvidersView />)
    let dialog = await screen.findByRole('dialog')
    await user.type(within(dialog).getByPlaceholderText('Paste the key'), KEY)
    await user.click(
      within(dialog).getByRole('button', { name: /^Add provider$/ }),
    )
    await waitFor(() => expect(providersApi.test).toHaveBeenCalledTimes(1))
    first.unmount()
    providersApi.test.mockResolvedValue({ ...registered, probe_state: 'ok' })
    window.history.pushState(
      {},
      '',
      '/providers?add=anthropic&returnTo=%2F%2Fevil.example',
    )
    wrap(<ProvidersView />)
    dialog = await screen.findByRole('dialog')
    await user.type(within(dialog).getByPlaceholderText('Paste the key'), KEY)
    await user.click(
      within(dialog).getByRole('button', { name: /^Add provider$/ }),
    )
    await waitFor(() => expect(providersApi.test).toHaveBeenCalledTimes(2))
    expect(navigate).not.toHaveBeenCalled()
    window.history.pushState({}, '', '/')
  })

  it('keeps a table row to Test connection and its menu, where the other actions are', async () => {
    const user = userEvent.setup()
    providersApi.list.mockResolvedValue({ items: [registered] })
    wrap(<ProvidersView />)
    const row = await screen.findByRole('row', { name: /Anthropic \(prod\)/ })
    expect(
      within(row)
        .getAllByRole('button')
        .map((b) => b.getAttribute('aria-label') ?? b.textContent),
    ).toEqual(['Test connection', 'Row actions for Anthropic (prod)'])
    await user.click(
      within(row).getByRole('button', {
        name: 'Row actions for Anthropic (prod)',
      }),
    )
    expect(
      (await screen.findAllByRole('menuitem')).map((i) => i.textContent),
    ).toEqual(['Default model', 'Bind profile', 'Rotate key', 'Revoke'])
  })

  it.each([
    ['ok', 'success', 'The provider answered and accepted this credential.'],
    [
      'refused',
      'warning',
      'The provider answered and rejected this credential. Rotate it.',
    ],
    [
      'unreachable',
      'warning',
      'The endpoint did not answer. This says nothing about the credential.',
    ],
  ] as const)(
    'reports a %s connection test as a %s toast',
    async (probe_state, intent, message) => {
      const user = userEvent.setup()
      providersApi.list.mockResolvedValue({ items: [registered] })
      providersApi.test.mockResolvedValue({ ...registered, probe_state })
      wrap(<ProvidersView />)
      await user.click(
        await screen.findByRole('button', { name: 'Test connection' }),
      )
      await waitFor(() =>
        expect(toast[intent]).toHaveBeenCalledWith(message, undefined),
      )
      for (const other of ['success', 'warning', 'error', 'info'] as const)
        if (other !== intent) expect(toast[other]).not.toHaveBeenCalled()
    },
  )

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

it('explains Add provider while another provider connection is being tested', async () => {
  window.history.pushState({}, '', '/providers')
  providersApi.list.mockResolvedValue({ items: [registered] })
  providersApi.test.mockImplementationOnce(() => new Promise(() => {}))
  wrap(<ProvidersView />)
  await userEvent.click(
    await screen.findByRole('button', { name: 'Test connection' }),
  )
  const add = screen.getByRole('button', { name: 'Add provider' })
  await waitFor(() => expect(add).toBeDisabled())
  expect(add).toHaveAccessibleDescription('Testing…')
})
