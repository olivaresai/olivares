// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// OpenCode signs in with its own ChatGPT login (opencode auth login, headless method),
// relayed like Codex's with a link and one-time code. A ready provider makes that
// login unnecessary, including a keyless local model.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, expect, it, vi } from 'vitest'
import './i18n'

const { api, auth, signIn, launch, navigate } = vi.hoisted(() => ({
  launch: vi.fn(),
  navigate: vi.fn(),
  api: {
    inventory: vi.fn(),
    // The providers section reads its own snapshot; these cases are about the rows below.
    providers: vi.fn(() =>
      Promise.resolve({
        providers: [
          {
            instance: 'opencode',
            driver: 'opencode',
            default: true,
            config_dir: '/home/op/.config/opencode',
            state: 'not_signed_in',
            installed: true,
            limits: [],
            models: [],
            checked_at: '2026-10-09T10:00:00Z',
            source: 'opencode',
          },
        ],
      }),
    ),
    plan: vi.fn(),
    install: vi.fn(),
    job: vi.fn(),
    detect: vi.fn(),
  },
  auth: {
    isSuperadmin: true,
    // Provider-read access is enabled by the provider readiness cases below.
    can: vi.fn(() => false),
    activeTenant: null,
    principal: { user_id: 'root' },
  },
  signIn: { status: vi.fn(), start: vi.fn(), get: vi.fn(), cancel: vi.fn() },
}))
vi.mock('@tanstack/react-router', () => ({ useNavigate: () => navigate }))
vi.mock('@/lib/auth/context', () => ({ useAuth: () => auth }))
vi.mock('./api', async (orig) => ({
  ...(await orig<typeof import('./api')>()),
  agentToolsApi: api,
}))
vi.mock('@/components/ui/toaster', () => ({
  toast: { success: vi.fn(), error: vi.fn(), warning: vi.fn() },
}))
vi.mock('@/features/first-hour/api', async (orig) => ({
  ...((await orig()) as object),
  signInApi: signIn,
  listProviderKeys: () => Promise.resolve([]),
}))
vi.mock('@/features/agentops/session-launch', async (orig) => ({
  ...((await orig()) as Record<string, unknown>),
  launchSession: launch,
}))

import { agentOpsApi } from '@/features/agentops/api'
import { readinessOf } from '@/features/first-hour/readiness.fixture'
import { useTenantStore } from '@/stores/tenant'
import { StartSessionForm } from '@/features/first-hour/first-hour'
import { AgentToolsView } from './agent-tools-view'

const flow = {
  id: 'si1',
  driver: 'opencode',
  state: 'waiting',
  url: 'https://auth.openai.example/device',
  user_code: 'QW12-ER34',
}

beforeEach(() => {
  useTenantStore.setState({ activeTenant: 'tenant-a' })
  vi.clearAllMocks()
  auth.can.mockReturnValue(false)
  vi.spyOn(agentOpsApi, 'toolsReadiness').mockResolvedValue(readinessOf({}))
  api.inventory.mockResolvedValue({
    drivers: ['opencode'],
    inventory: {
      installed: [{ driver: 'opencode', version: '1.2.0', state: 'installed' }],
      leftovers: [],
    },
    read_only: false,
    jobs: [],
  })
  signIn.status.mockImplementation((driver: string) =>
    Promise.resolve({ driver, installed: true, signed_in: false }),
  )
  signIn.start.mockResolvedValue(flow)
  signIn.get.mockResolvedValue(flow)
})

it('offers OpenCode its own ChatGPT sign-in and relays the link and the code', async () => {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  render(
    <QueryClientProvider client={qc}>
      <AgentToolsView />
    </QueryClientProvider>,
  )
  const card = await screen.findByTestId('tool-opencode')
  expect(
    await within(card).findByText('Account · not signed in'),
  ).toBeInTheDocument()
  await userEvent.click(within(card).getByRole('button', { name: /^Sign in/ }))
  const dialog = await screen.findByRole('dialog', { name: /Sign in OpenCode/ })
  await waitFor(() =>
    expect(signIn.start).toHaveBeenCalledWith('opencode', 'tenant-a'),
  )
  expect(await within(dialog).findByTestId('device-code')).toHaveTextContent(
    'QW12-ER34',
  )
  expect(
    within(dialog).getByRole('link', { name: /open the sign-in page/i }),
  ).toHaveAttribute('href', 'https://auth.openai.example/device')
  expect(
    within(dialog).getByRole('link', {
      name: 'Use an API key instead',
      hidden: true,
    }),
  ).toHaveAttribute(
    'href',
    expect.stringMatching(/^\/providers\?add=openai&returnTo=/),
  )
})

const localProvider = {
  provider_ref: 'prv_local',
  kind: 'ollama' as const,
  display_name: 'Local model server',
  state: 'active' as const,
  probe_state: 'ok' as const,
}

it.each(['ollama', 'openai'] as const)(
  'does not ask OpenCode to sign in when its %s provider is ready',
  async (kind) => {
    auth.can.mockReturnValue(true)
    const provider = { ...localProvider, kind }
    vi.mocked(agentOpsApi.toolsReadiness).mockResolvedValue(
      readinessOf({ opencode: { provider } }),
    )
    const qc = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    })
    render(
      <QueryClientProvider client={qc}>
        <AgentToolsView />
      </QueryClientProvider>,
    )
    const card = await screen.findByTestId('tool-opencode')
    expect(
      await within(card).findByText('API key · Local model server'),
    ).toBeInTheDocument()
    expect(within(card).getByText('Ready')).toBeInTheDocument()
    expect(
      within(card).queryByRole('button', { name: /^Sign in/ }),
    ).not.toBeInTheDocument()
    expect(signIn.start).not.toHaveBeenCalled()
  },
)

// The engine says the key it would use was refused at its last test: the tool cannot
// start on it, so its own sign-in stays offered.
it('keeps sign-in available when the selected provider is refused', async () => {
  auth.can.mockReturnValue(true)
  vi.mocked(agentOpsApi.toolsReadiness).mockResolvedValue(
    readinessOf({ opencode: { provider: localProvider, refused: true } }),
  )
  const qc = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  render(
    <QueryClientProvider client={qc}>
      <AgentToolsView />
    </QueryClientProvider>,
  )
  const card = await screen.findByTestId('tool-opencode')
  expect(
    await within(card).findByRole('button', { name: /^Sign in/ }),
  ).toBeEnabled()
  expect(within(card).getByText('Account · not signed in')).toBeInTheDocument()
})

it('starts the first OpenCode session on the local provider without signing in', async () => {
  auth.can.mockReturnValue(true)
  signIn.status.mockImplementation(async (driver: string) => ({
    driver,
    installed: driver === 'opencode',
    signed_in: false,
  }))
  vi.mocked(agentOpsApi.toolsReadiness).mockResolvedValue(
    readinessOf({ opencode: { provider: localProvider } }),
  )
  launch.mockResolvedValue({ run_ref: 'local-run' })
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  render(
    <QueryClientProvider client={qc}>
      <StartSessionForm />
    </QueryClientProvider>,
  )
  const start = await screen.findByRole('button', {
    name: 'Start',
  })
  expect(start).toBeEnabled()
  await userEvent.click(start)
  // Launched under the authority Start was pressed with.
  expect(launch).toHaveBeenCalledWith(
    expect.objectContaining({
      quick: expect.objectContaining({ driver: 'opencode' }),
    }),
    expect.objectContaining({
      signal: expect.any(AbortSignal),
      dispatchGuard: expect.any(Function),
    }),
  )
  expect(signIn.start).not.toHaveBeenCalled()
  await waitFor(() =>
    expect(navigate).toHaveBeenCalledWith({
      to: '/sessions',
      search: { session: 'run:local-run', pane: 'narrative' },
    }),
  )
})
