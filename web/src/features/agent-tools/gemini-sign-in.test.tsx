// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Gemini relays the native Google OAuth URL and manual authorization code.
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
    providers: vi.fn(() => Promise.resolve({ providers: [] })),
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
  signIn: {
    status: vi.fn(),
    start: vi.fn(),
    get: vi.fn(),
    code: vi.fn(),
    cancel: vi.fn(),
  },
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
  id: 'si-gemini',
  driver: 'gemini-cli',
  state: 'needs_code',
  url: 'https://accounts.google.com/o/oauth2/v2/auth?state=fixture',
}

beforeEach(() => {
  useTenantStore.setState({ activeTenant: 'tenant-a' })
  vi.clearAllMocks()
  auth.can.mockReturnValue(false)
  vi.spyOn(agentOpsApi, 'toolsReadiness').mockResolvedValue(readinessOf({}))
  api.inventory.mockResolvedValue({
    drivers: ['gemini-cli'],
    inventory: {
      installed: [
        { driver: 'gemini-cli', version: '0.62.0', state: 'installed' },
      ],
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
  signIn.code.mockResolvedValue({ ...flow, state: 'signed_in' })
})

it('relays the native Google sign-in URL and pasted code from the Gemini row', async () => {
  const user = userEvent.setup()
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  render(
    <QueryClientProvider client={qc}>
      <AgentToolsView />
    </QueryClientProvider>,
  )
  const box = await screen.findByTestId('tool-gemini-cli')
  expect(
    await within(box).findByText('Account · not signed in'),
  ).toBeInTheDocument()
  await user.click(within(box).getByRole('button', { name: /^Sign in/ }))
  const dialog = await screen.findByRole('dialog', {
    name: /Sign in Gemini CLI/,
  })
  await waitFor(() =>
    expect(signIn.start).toHaveBeenCalledWith('gemini-cli', 'tenant-a'),
  )
  expect(
    await within(dialog).findByRole('link', { name: 'Open the sign-in page' }),
  ).toHaveAttribute('href', flow.url)
  expect(
    within(dialog).getByRole('link', {
      name: 'Use an API key instead',
      hidden: true,
    }),
  ).toHaveAttribute(
    'href',
    expect.stringMatching(/^\/providers\?add=gemini&returnTo=/),
  )
  await user.type(
    within(dialog).getByLabelText('Code from the sign-in page'),
    'fixture-code',
  )
  await user.click(within(dialog).getByRole('button', { name: 'Continue' }))
  await waitFor(() =>
    expect(signIn.code).toHaveBeenCalledWith('si-gemini', 'fixture-code'),
  )
})

it('starts the first Gemini session through its existing driver', async () => {
  auth.can.mockReturnValue(true)
  signIn.status.mockImplementation(async (driver: string) => ({
    driver,
    installed: driver === 'gemini-cli',
    signed_in: driver === 'gemini-cli',
  }))
  vi.mocked(agentOpsApi.toolsReadiness).mockResolvedValue(
    readinessOf({ 'gemini-cli': 'own_login' }),
  )
  launch.mockResolvedValue({ run_ref: 'gemini-run' })
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  render(
    <QueryClientProvider client={qc}>
      <StartSessionForm />
    </QueryClientProvider>,
  )
  const start = await screen.findByRole('button', { name: 'Start' })
  expect(start).toBeEnabled()
  await userEvent.click(start)
  expect(launch).toHaveBeenCalledWith(
    expect.objectContaining({
      quick: expect.objectContaining({ driver: 'gemini-cli' }),
    }),
    expect.objectContaining({
      signal: expect.any(AbortSignal),
      dispatchGuard: expect.any(Function),
    }),
  )
  await waitFor(() =>
    expect(navigate).toHaveBeenCalledWith({
      to: '/sessions',
      search: { session: 'run:gemini-run', pane: 'narrative' },
    }),
  )
})

it('reviews and installs the exact approved Gemini bundle from the Install menu', async () => {
  api.inventory.mockResolvedValue({
    drivers: ['gemini-cli'],
    inventory: { installed: [], leftovers: [] },
    read_only: false,
    jobs: [],
  })
  signIn.status.mockImplementation(async (driver: string) => ({
    driver,
    installed: false,
    signed_in: false,
  }))
  api.plan.mockResolvedValue({
    driver: 'gemini-cli',
    version: '0.62.0',
    digest: 'gemini-approved-digest',
    verification: 'sha256',
    executable: '/tools/gemini-cli/bin/gemini',
  })
  api.install.mockResolvedValue({
    id: 'gemini-install',
    driver: 'gemini-cli',
    state: 'running',
    progress: 'downloading',
  })
  api.job.mockResolvedValue({
    id: 'gemini-install',
    driver: 'gemini-cli',
    state: 'succeeded',
    progress: 'placed',
  })
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  render(
    <QueryClientProvider client={qc}>
      <AgentToolsView />
    </QueryClientProvider>,
  )
  const user = userEvent.setup()
  await user.click(await screen.findByRole('button', { name: /^Install/ }))
  await user.click(
    await screen.findByRole('menuitem', { name: 'Install Gemini CLI' }),
  )
  expect(await screen.findByText('0.62.0')).toBeInTheDocument()
  expect(api.install).not.toHaveBeenCalled()
  await userEvent.click(
    screen.getByRole('button', { name: /Install approved version/i }),
  )
  await waitFor(() =>
    expect(api.install).toHaveBeenCalledWith(
      expect.objectContaining({
        plan_digest: 'gemini-approved-digest',
        request_id: expect.any(String),
      }),
      expect.any(Object),
    ),
  )
  expect(await screen.findByText(/Installation complete/i)).toBeInTheDocument()
})
