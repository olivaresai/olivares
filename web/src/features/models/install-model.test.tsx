// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The Models page offers "Install a model" to a system administrator only,
// with Ollama installed or a way to install it.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, expect, it, vi } from 'vitest'

const { api, auth } = vi.hoisted(() => ({
  api: {
    ollama: vi.fn(),
    ollamaStart: vi.fn(),
    ollamaStop: vi.fn(),
    ollamaPull: vi.fn(),
    ollamaPullStatus: vi.fn(),
    ollamaPullCancel: vi.fn(),
    ollamaRemove: vi.fn(),
  },
  auth: { isSuperadmin: true, activeTenant: null, can: () => false },
}))
vi.mock('@/lib/auth/context', () => ({ useAuth: () => auth }))
vi.mock('@/features/agent-tools/api', async (orig) => ({
  ...((await orig()) as object),
  agentToolsApi: api,
}))
vi.mock('@tanstack/react-router', async () =>
  (await import('@/test/fake-router')).fakeRouterModule(),
)
vi.mock('@/components/ui/toaster', () => ({
  toast: { success: vi.fn(), error: vi.fn(), warning: vi.fn() },
}))
vi.mock('./available-tab', () => ({
  AvailableTab: () => <div>Available rows</div>,
}))
vi.mock('./api', async (orig) => {
  const actual = (await orig()) as typeof import('./api')
  const empty = () => Promise.resolve({ items: [], has_more: false })
  return {
    ...actual,
    modelsApi: {
      ...actual.modelsApi,
      catalog: () => Promise.resolve({ models: [], capabilities: [] }),
      models: empty,
      routingPolicies: empty,
      keys: empty,
      workspaceResidency: empty,
      modelAccess: empty,
      modelGroups: empty,
      gpaiPostures: empty,
    },
  }
})

import { InstallModel } from './install-model'
import { ModelsView } from './models-view'

function mount() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={qc}>
      <InstallModel />
    </QueryClientProvider>,
  )
}

function mountPage() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={qc}>
      <ModelsView />
    </QueryClientProvider>,
  )
}

beforeEach(() => {
  vi.clearAllMocks()
  auth.isSuperadmin = true
  api.ollama.mockResolvedValue({
    installed: true,
    state: 'running',
    endpoint: 'http://127.0.0.1:11434',
    models: [],
  })
})

it('offers the install control with Ollama installed', async () => {
  mount()
  expect(
    await screen.findByRole('heading', { name: 'Install a model' }),
  ).toBeInTheDocument()
  expect(await screen.findByLabelText('Model name')).toHaveValue('qwen2.5:0.5b')
  expect(screen.getByRole('button', { name: 'Download' })).toBeEnabled()
})

it('points to AI tools when Ollama is not installed', async () => {
  api.ollama.mockResolvedValue({
    installed: false,
    state: 'stopped',
    models: [],
  })
  mount()
  expect(
    await screen.findByText(/Ollama is not installed on this server\./),
  ).toBeInTheDocument()
  const installLink = screen.getByRole('link', {
    name: 'Install it in AI tools',
  })
  expect(installLink).toHaveAttribute('href', '/agent-tools')
  // One orange control per view: the inline link is neutral text with an underline.
  expect(installLink).toHaveClass('text-foreground', 'underline')
  expect(installLink).not.toHaveClass('text-accent-text')
  expect(screen.queryByLabelText('Model name')).not.toBeInTheDocument()
})

it('offers a retry when the Ollama status read fails', async () => {
  api.ollama.mockRejectedValueOnce(new Error('offline')).mockResolvedValueOnce({
    installed: true,
    state: 'running',
    endpoint: 'http://127.0.0.1:11434',
    models: [],
  })
  mount()
  await userEvent.click(await screen.findByRole('button', { name: 'Retry' }))
  expect(await screen.findByRole('button', { name: 'Download' })).toBeEnabled()
})

it('puts the installer in Available, not on every Models tab', async () => {
  mountPage()
  const available = await screen.findByRole('tab', { name: 'Available' })
  expect(available).toHaveAttribute('aria-selected', 'true')
  expect(
    await screen.findByRole('heading', { name: 'Install a model' }),
  ).toBeInTheDocument()
  await userEvent.click(screen.getByRole('tab', { name: 'Catalog' }))
  expect(
    screen.queryByRole('heading', { name: 'Install a model' }),
  ).not.toBeInTheDocument()
  await userEvent.click(screen.getByRole('tab', { name: 'Available' }))
  expect(
    await screen.findByRole('heading', { name: 'Install a model' }),
  ).toBeInTheDocument()
})

it('asks nothing and shows nothing to anyone but a system administrator', () => {
  auth.isSuperadmin = false
  const { container } = mount()
  expect(container).toBeEmptyDOMElement()
  expect(api.ollama).not.toHaveBeenCalled()
})
