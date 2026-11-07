// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The engine runs llama.cpp and vLLM as services (GET/POST /v1/m/agenttools/model-servers) and
// only the CLI could start them. AI tools shows a row for each beside Ollama: installed or not,
// its state, where it answers and what it serves; Start names the model and the organization.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, expect, it, vi } from 'vitest'
import './i18n'

const { api, auth } = vi.hoisted(() => ({
  api: {
    modelServers: vi.fn(),
    modelServerStart: vi.fn(),
    modelServerStop: vi.fn(),
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
import { ModelServers } from './model-servers'

const stopped = (kind: string, name: string, installed: boolean) => ({
  kind,
  name,
  installed,
  state: 'stopped',
  models: [] as string[],
})

beforeEach(() => {
  vi.clearAllMocks()
  useTenantStore.setState({ activeTenant: 'tenant-a' })
  api.modelServers.mockResolvedValue({
    servers: [
      stopped('llama-cpp', 'llama.cpp', true),
      stopped('vllm', 'vLLM', false),
    ],
  })
})

function mount() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={qc}>
      <ModelServers />
    </QueryClientProvider>,
  )
}

it('lists each server, says where a missing one is looked for, and offers Start only for an installed one', async () => {
  mount()
  const llama = await screen.findByTestId('model-server-llama-cpp')
  const vllm = screen.getByTestId('model-server-vllm')
  expect(within(llama).getByText('Not running')).toBeInTheDocument()
  expect(
    within(llama).getByRole('button', { name: 'Start' }),
  ).toBeInTheDocument()
  expect(within(vllm).getByText(/not installed/)).toHaveTextContent(
    'Olivares looks for vllm on this server',
  )
  expect(within(vllm).queryByRole('button', { name: 'Start' })).toBeNull()
})

it('starts the named model for the signed-in organization, reads "starting" while it loads and "running" once it answers', async () => {
  api.modelServerStart.mockResolvedValue({
    ...stopped('llama-cpp', 'llama.cpp', true),
    state: 'starting',
    model: 'ggml-org/tiny-GGUF',
  })
  mount()
  const row = await screen.findByTestId('model-server-llama-cpp')
  const start = within(row).getByRole('button', { name: 'Start' })
  expect(start).toBeDisabled()
  await userEvent.type(
    within(row).getByLabelText('Model'),
    ' ggml-org/tiny-GGUF:Q4_K_M ',
  )
  api.modelServers.mockResolvedValue({
    servers: [
      {
        ...stopped('llama-cpp', 'llama.cpp', true),
        state: 'starting',
        model: 'ggml-org/tiny-GGUF:Q4_K_M',
      },
      stopped('vllm', 'vLLM', false),
    ],
  })
  await userEvent.click(start)
  expect(api.modelServerStart).toHaveBeenCalledWith(
    'llama-cpp',
    'ggml-org/tiny-GGUF:Q4_K_M',
    'tenant-a',
    expect.anything(),
  )
  expect(await within(row).findByText(/Starting/)).toBeInTheDocument()
  expect(within(row).getByRole('button', { name: 'Stop' })).toBeInTheDocument()

  api.modelServers.mockResolvedValue({
    servers: [
      {
        ...stopped('llama-cpp', 'llama.cpp', true),
        state: 'running',
        endpoint: 'http://127.0.0.1:41000/v1',
        models: ['tiny'],
      },
      stopped('vllm', 'vLLM', false),
    ],
  })
  expect(
    await within(row).findByText(
      /Running at http:\/\/127\.0\.0\.1:41000\/v1/,
      {},
      { timeout: 4000 },
    ),
  ).toBeInTheDocument()
  expect(within(row).getByText('tiny')).toBeInTheDocument()

  api.modelServerStop.mockResolvedValue(stopped('llama-cpp', 'llama.cpp', true))
  api.modelServers.mockResolvedValue({
    servers: [
      stopped('llama-cpp', 'llama.cpp', true),
      stopped('vllm', 'vLLM', false),
    ],
  })
  await userEvent.click(within(row).getByRole('button', { name: 'Stop' }))
  expect(api.modelServerStop).toHaveBeenCalledWith('llama-cpp')
  await waitFor(() =>
    expect(within(row).getByText('Not running')).toBeInTheDocument(),
  )
})

it("shows the server's own last line when a start fails", async () => {
  api.modelServers.mockResolvedValue({
    servers: [
      {
        ...stopped('llama-cpp', 'llama.cpp', true),
        state: 'failed',
        message: 'error: failed to load model: file not found',
      },
      stopped('vllm', 'vLLM', true),
    ],
  })
  mount()
  const row = await screen.findByTestId('model-server-llama-cpp')
  expect(
    await within(row).findByText('error: failed to load model: file not found'),
  ).toBeInTheDocument()
  expect(within(row).getByRole('button', { name: 'Start' })).toBeInTheDocument()
})

it('shows the message of a start the engine refuses', async () => {
  api.modelServerStart.mockRejectedValue(
    new Error('llama.cpp already runs another model. Stop it first.'),
  )
  mount()
  const row = await screen.findByTestId('model-server-llama-cpp')
  await userEvent.type(within(row).getByLabelText('Model'), '/models/a.gguf')
  await userEvent.click(within(row).getByRole('button', { name: 'Start' }))
  expect(
    await within(row).findByText(
      'llama.cpp already runs another model. Stop it first.',
    ),
  ).toBeInTheDocument()
})
