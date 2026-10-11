// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// HU-R17 (refresh 06): the console installed Ollama, and the person had to start
// `ollama serve` and pull a model by hand. The Ollama row now starts it on this server,
// says where it answers, and downloads a model by name with its progress.
import {
  QueryClient,
  QueryClientProvider,
  useQuery,
} from '@tanstack/react-query'
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
import { firstHourKeys } from '@/features/first-hour/api'
import { providerKeys } from '@/features/providers/api'
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

it('readiness reads ask again once Ollama is up and after a download, without a remount', async () => {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  // The engine registers the server in Providers only once it is running, after the start
  // request has returned; until then the consumer's cached answers are the old ones.
  let registered = false
  const providersList = vi.fn(async () =>
    registered ? [{ provider_ref: 'prv_ollama' }] : [],
  )
  const preview = vi.fn(async () => ({
    reason: registered ? 'api_key' : 'none',
  }))
  function Consumer() {
    const providers = useQuery({
      queryKey: providerKeys.list('tenant-a', 0, { state: 'active' }),
      queryFn: providersList,
      staleTime: Infinity,
    })
    const runsOn = useQuery({
      queryKey: firstHourKeys.readiness('tenant-a'),
      queryFn: preview,
      staleTime: Infinity,
    })
    return (
      <p data-testid="consumer">
        {providers.data?.length ?? '-'} {runsOn.data?.reason ?? '-'}
      </p>
    )
  }
  render(
    <QueryClientProvider client={qc}>
      <OllamaService />
      <Consumer />
    </QueryClientProvider>,
  )
  expect(await screen.findByText('Not running')).toBeInTheDocument()
  await waitFor(() =>
    expect(screen.getByTestId('consumer')).toHaveTextContent('0 none'),
  )
  api.ollama
    .mockResolvedValueOnce({ installed: true, state: 'starting', models: [] })
    .mockImplementation(async () => {
      registered = true
      return running
    })
  await userEvent.click(screen.getByRole('button', { name: 'Start' }))
  await waitFor(
    () => expect(screen.getByTestId('consumer')).toHaveTextContent('1 api_key'),
    { timeout: 4000 },
  )
  const reads = providersList.mock.calls.length
  api.ollamaPullStatus.mockResolvedValue({
    id: 'p1',
    model: 'qwen2.5:0.5b',
    state: 'succeeded',
    status: 'success',
    completed: 100,
    total: 100,
  })
  await userEvent.click(screen.getByRole('button', { name: 'Download' }))
  await waitFor(
    () => expect(providersList.mock.calls.length).toBeGreaterThan(reads),
    { timeout: 4000 },
  )
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
  // The field starts on the recommended model (HU2-07); a name typed instead replaces it.
  await userEvent.clear(screen.getByLabelText('Pull a model'))
  await userEvent.type(screen.getByLabelText('Pull a model'), 'qwen2.5:0.5b')
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

it('pulls a public Hugging Face GGUF reference without changing its quantization', async () => {
  const model = 'hf.co/bartowski/SmolLM2-135M-Instruct-GGUF:Q4_K_M'
  api.ollama.mockResolvedValue(running)
  api.ollamaPull.mockResolvedValue({
    id: 'hf-pull',
    model,
    state: 'running',
    completed: 0,
    total: 0,
  })
  api.ollamaPullStatus.mockResolvedValue({
    id: 'hf-pull',
    model,
    state: 'succeeded',
    completed: 100,
    total: 100,
  })
  mount()
  await screen.findByText(/Running at/)
  expect(screen.getByText(/public Hugging Face GGUF/)).toBeInTheDocument()
  await userEvent.clear(screen.getByLabelText('Pull a model'))
  await userEvent.type(screen.getByLabelText('Pull a model'), model)
  await userEvent.click(screen.getByRole('button', { name: 'Download' }))
  expect(api.ollamaPull).toHaveBeenCalledWith(model, expect.anything())
  expect(await screen.findByText(`Downloaded ${model}.`)).toBeInTheDocument()
})

// HU2-07: the model field was empty and Download disabled until the person typed a name they
// had to know already.
it('offers a small recommended model, ready to download', async () => {
  api.ollama.mockResolvedValue(running)
  mount()
  await screen.findByText(/Running at/)
  expect(screen.getByLabelText('Pull a model')).toHaveValue('qwen2.5:0.5b')
  const download = screen.getByRole('button', { name: 'Download' })
  expect(download).toBeEnabled()
  await userEvent.click(download)
  expect(api.ollamaPull).toHaveBeenCalledWith('qwen2.5:0.5b', expect.anything())
})
