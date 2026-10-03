// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// HU-R17 (refresh 06): the console installed Ollama, and the person had to start
// `ollama serve` and pull a model by hand. The Ollama row now starts it on this server,
// says where it answers, and downloads a model by name with its progress.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, expect, it, vi } from 'vitest'
import './i18n'

const { api, auth } = vi.hoisted(() => ({
  api: {
    ollama: vi.fn(),
    ollamaStart: vi.fn(),
    ollamaStop: vi.fn(),
    ollamaPull: vi.fn(),
    ollamaPullStatus: vi.fn(),
  },
  auth: {
    isSuperadmin: true,
    activeTenant: null,
    principal: { user_id: 'root' },
  },
}))
vi.mock('@/lib/auth/context', () => ({ useAuth: () => auth }))
vi.mock('./api', async (orig) => ({
  ...((await orig()) as object),
  agentToolsApi: api,
}))
vi.mock('@/components/ui/toaster', () => ({
  toast: { success: vi.fn(), error: vi.fn(), warning: vi.fn() },
}))

import { useTenantStore } from '@/stores/tenant'
import { OllamaService } from './ollama-service'

const running = {
  installed: true,
  state: 'running',
  endpoint: 'http://127.0.0.1:11434',
  models: [] as string[],
}

beforeEach(() => {
  vi.clearAllMocks()
  useTenantStore.setState({ activeTenant: 'tenant-a' })
  api.ollama.mockResolvedValue({
    installed: true,
    state: 'stopped',
    models: [],
  })
  api.ollamaStart.mockResolvedValue({
    installed: true,
    state: 'starting',
    models: [],
  })
  api.ollamaPull.mockResolvedValue({
    id: 'p1',
    model: 'qwen2.5:0.5b',
    state: 'running',
    completed: 0,
    total: 0,
  })
})

function mount() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={qc}>
      <OllamaService />
    </QueryClientProvider>,
  )
}

it('starts Ollama on this server and says where it answers', async () => {
  mount()
  expect(await screen.findByText('Not running')).toBeInTheDocument()
  api.ollama.mockResolvedValue(running)
  await userEvent.click(screen.getByRole('button', { name: 'Start' }))
  // Only the organization the person works in gets the endpoint (Root on FH 033).
  expect(api.ollamaStart).toHaveBeenCalledWith('tenant-a', expect.anything())
  expect(
    await screen.findByText(/Running at http:\/\/127\.0\.0\.1:11434/),
  ).toBeInTheDocument()
  expect(screen.getByText('No model yet.')).toBeInTheDocument()
  expect(screen.getByRole('button', { name: 'Stop' })).toBeInTheDocument()
})

it('downloads a model by name and shows its progress until it is there', async () => {
  api.ollama.mockResolvedValue(running)
  api.ollamaPullStatus
    .mockResolvedValueOnce({
      id: 'p1',
      model: 'qwen2.5:0.5b',
      state: 'running',
      status: 'pulling 7c2f',
      completed: 40,
      total: 100,
    })
    .mockResolvedValue({
      id: 'p1',
      model: 'qwen2.5:0.5b',
      state: 'succeeded',
      status: 'success',
      completed: 100,
      total: 100,
    })
  mount()
  await screen.findByText(/Running at/)
  await userEvent.type(screen.getByLabelText('Model'), 'qwen2.5:0.5b')
  api.ollama.mockResolvedValue({ ...running, models: ['qwen2.5:0.5b'] })
  await userEvent.click(screen.getByRole('button', { name: 'Download' }))
  expect(api.ollamaPull).toHaveBeenCalledWith('qwen2.5:0.5b', expect.anything())
  expect(await screen.findByText(/40%/)).toBeInTheDocument()
  expect(
    await screen.findByText('Downloaded qwen2.5:0.5b.', {}, { timeout: 4000 }),
  ).toBeInTheDocument()
  await waitFor(() =>
    expect(screen.getByText('qwen2.5:0.5b')).toBeInTheDocument(),
  )
  // A finished download stops asking: its own refresh of the status must not
  // refetch the progress again (it did, without end, until the heap ran out).
  const asked = api.ollamaPullStatus.mock.calls.length
  await new Promise((r) => setTimeout(r, 1500))
  expect(api.ollamaPullStatus.mock.calls.length).toBe(asked)
})
