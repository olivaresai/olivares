// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Install a local model from the console: pick one or type its name,
// Ollama's own progress, cancel, Ollama's last line when it fails, start Ollama from the
// same control when it is not running, and see what is installed with its size.
import {
  QueryClient,
  QueryClientProvider,
  useQuery,
} from '@tanstack/react-query'
import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import axe from 'axe-core'
import { beforeEach, expect, it, vi } from 'vitest'
import './i18n'

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
import { InstalledModels, ModelInstaller } from './model-installer'
import { useOllama } from './use-ollama'
import { OLLAMA_AVAILABLE_KEY } from './api'

const running = {
  installed: true,
  state: 'running',
  endpoint: 'http://127.0.0.1:11434',
  models: [] as string[],
}
const pull = (over: object) => ({
  id: 'p1',
  model: 'qwen2.5:0.5b',
  state: 'running',
  completed: 0,
  total: 0,
  ...over,
})

beforeEach(() => {
  vi.clearAllMocks()
  useTenantStore.setState({ activeTenant: 'tenant-a' })
  api.ollama.mockResolvedValue(running)
  api.ollamaStart.mockResolvedValue({ ...running, state: 'starting' })
  api.ollamaPull.mockResolvedValue(pull({}))
})

function Both() {
  const ollama = useOllama()
  if (!ollama.status.data) return null
  return (
    <>
      <ModelInstaller ollama={ollama} />
      <InstalledModels ollama={ollama} />
    </>
  )
}

function mount(
  qc = new QueryClient({ defaultOptions: { queries: { retry: false } } }),
) {
  return render(
    <QueryClientProvider client={qc}>
      <Both />
    </QueryClientProvider>,
  )
}

it('lists short suggestions that fill the field, and downloads the one picked', async () => {
  mount()
  const group = await screen.findByRole('group', { name: 'Suggested models' })
  await userEvent.click(
    within(group).getByRole('button', { name: 'llama3.2:1b' }),
  )
  expect(screen.getByLabelText('Model name')).toHaveValue('llama3.2:1b')
  await userEvent.click(screen.getByRole('button', { name: 'Download' }))
  expect(api.ollamaPull).toHaveBeenCalledWith('llama3.2:1b', expect.anything())
})

it('shows the download as a progress bar and cancels it', async () => {
  api.ollamaPullStatus.mockResolvedValue(
    pull({ status: 'pulling 7c2f', completed: 40, total: 100 }),
  )
  api.ollamaPullCancel.mockResolvedValue(
    pull({ state: 'failed', error: 'Download cancelled.' }),
  )
  mount()
  await userEvent.click(await screen.findByRole('button', { name: 'Download' }))
  const bar = await screen.findByRole('progressbar', {
    name: 'Download of qwen2.5:0.5b',
  })
  expect(bar).toHaveAttribute('aria-valuenow', '40')
  expect(screen.getByText('pulling 7c2f')).toBeInTheDocument()
  // One download at a time: the button waits for this one.
  expect(screen.getByRole('button', { name: 'Download' })).toBeDisabled()

  await userEvent.click(
    screen.getByRole('button', { name: 'Cancel the download of qwen2.5:0.5b' }),
  )
  expect(api.ollamaPullCancel).toHaveBeenCalledWith('p1')
  expect(
    await screen.findByText(
      /qwen2\.5:0\.5b was not downloaded\. Download cancelled\./,
    ),
  ).toBeInTheDocument()
  expect(screen.queryByRole('progressbar')).not.toBeInTheDocument()
  expect(screen.getByRole('button', { name: 'Download' })).toBeEnabled()
})

it('lists the model, with its size, once its download is done', async () => {
  mount()
  expect(await screen.findByText('No model yet.')).toBeInTheDocument()
  api.ollamaPullStatus.mockResolvedValue(
    pull({ state: 'succeeded', completed: 100, total: 100 }),
  )
  api.ollama.mockResolvedValue({
    ...running,
    models: ['qwen2.5:0.5b'],
    model_sizes: { 'qwen2.5:0.5b': 397821319 },
  })
  await userEvent.click(screen.getByRole('button', { name: 'Download' }))
  expect(
    await screen.findByText('Downloaded qwen2.5:0.5b.'),
  ).toBeInTheDocument()
  const list = await screen.findByRole('list', { name: 'Installed models' })
  expect(list).toHaveTextContent('qwen2.5:0.5b')
  expect(list).toHaveTextContent('379 MiB')
  expect(screen.queryByText('No model yet.')).not.toBeInTheDocument()
})

it('refreshes the Available Ollama read when a download finishes', async () => {
  const available = vi.fn(async () => running)
  function AvailableRead() {
    const q = useQuery({
      queryKey: ['models', OLLAMA_AVAILABLE_KEY, 'epoch'],
      queryFn: available,
      staleTime: Infinity,
    })
    return (
      <span data-testid="available-read">{q.data?.models.length ?? 0}</span>
    )
  }
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  render(
    <QueryClientProvider client={qc}>
      <Both />
      <AvailableRead />
    </QueryClientProvider>,
  )
  expect(await screen.findByText('No model yet.')).toBeInTheDocument()
  await waitFor(() => expect(available).toHaveBeenCalled())
  const reads = available.mock.calls.length
  api.ollamaPullStatus.mockResolvedValue(
    pull({ state: 'succeeded', completed: 100, total: 100 }),
  )
  await userEvent.click(screen.getByRole('button', { name: 'Download' }))
  await waitFor(() =>
    expect(available.mock.calls.length).toBeGreaterThan(reads),
  )
})

it('refreshes Available again when Ollama reaches running', async () => {
  api.ollama.mockResolvedValue({
    installed: true,
    state: 'stopped',
    models: [],
  })
  const available = vi.fn(async () => ({
    ...running,
    models: ['qwen2.5:0.5b'],
  }))
  function AvailableRead() {
    const q = useQuery({
      queryKey: ['models', OLLAMA_AVAILABLE_KEY, 'epoch'],
      queryFn: available,
      staleTime: Infinity,
    })
    return (
      <span data-testid="available-read">{q.data?.models.length ?? 0}</span>
    )
  }
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  render(
    <QueryClientProvider client={qc}>
      <Both />
      <AvailableRead />
    </QueryClientProvider>,
  )
  expect(
    await screen.findByRole('button', { name: 'Start Ollama' }),
  ).toBeEnabled()
  await waitFor(() => expect(available).toHaveBeenCalledTimes(1))
  api.ollama.mockResolvedValue({
    ...running,
    models: ['qwen2.5:0.5b'],
  })
  await userEvent.click(screen.getByRole('button', { name: 'Start Ollama' }))
  expect(await screen.findByRole('button', { name: 'Download' })).toBeEnabled()
  await waitFor(() =>
    expect(available.mock.calls.length).toBeGreaterThanOrEqual(3),
  )
  expect(screen.getByTestId('available-read')).toHaveTextContent('1')
})

it('keeps the download locked and retries when its progress cannot be read', async () => {
  api.ollamaPullStatus
    .mockRejectedValueOnce(new Error('offline'))
    .mockResolvedValue(
      pull({ status: 'pulling 7c2f', completed: 40, total: 100 }),
    )
  mount()
  await userEvent.click(await screen.findByRole('button', { name: 'Download' }))
  expect(
    await screen.findByText(
      "The download's progress could not be read. offline",
    ),
  ).toBeInTheDocument()
  expect(screen.getByRole('button', { name: 'Download' })).toBeDisabled()
  await userEvent.click(screen.getByRole('button', { name: 'Retry' }))
  expect(await screen.findByRole('progressbar')).toHaveAttribute(
    'aria-valuenow',
    '40',
  )
})

it('restores progress and Cancel after leaving and returning to the installer', async () => {
  api.ollamaPullStatus.mockResolvedValue(
    pull({ status: 'pulling 7c2f', completed: 40, total: 100 }),
  )
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  const first = mount(qc)
  await userEvent.click(await screen.findByRole('button', { name: 'Download' }))
  expect(await screen.findByRole('progressbar')).toBeInTheDocument()
  first.unmount()
  mount(qc)
  expect(await screen.findByRole('progressbar')).toHaveAttribute(
    'aria-valuenow',
    '40',
  )
  expect(
    screen.getByRole('button', {
      name: 'Cancel the download of qwen2.5:0.5b',
    }),
  ).toBeEnabled()
})

it('shows a model-list read failure instead of saying there is no model', async () => {
  api.ollama.mockResolvedValue({
    ...running,
    models_error: 'Ollama: tags unavailable',
  })
  mount()
  expect(await screen.findByRole('alert')).toHaveTextContent('tags unavailable')
  expect(screen.queryByText('No model yet.')).not.toBeInTheDocument()
})

it('says when Ollama installed the model but provider synchronization failed', async () => {
  api.ollamaPullStatus.mockResolvedValue(
    pull({
      state: 'succeeded',
      completed: 100,
      total: 100,
      error: 'its provider record could not be updated: probe unavailable',
    }),
  )
  mount()
  await userEvent.click(await screen.findByRole('button', { name: 'Download' }))
  expect(
    await screen.findByText(
      'qwen2.5:0.5b is installed, but its provider record could not be updated: probe unavailable',
    ),
  ).toBeInTheDocument()
  expect(screen.queryByText('Downloaded qwen2.5:0.5b.')).not.toBeInTheDocument()
})

it("shows Ollama's own last line when a download fails", async () => {
  api.ollamaPullStatus.mockResolvedValue(
    pull({
      state: 'failed',
      error: 'Ollama: pull model manifest: file does not exist',
    }),
  )
  mount()
  await userEvent.click(await screen.findByRole('button', { name: 'Download' }))
  expect(
    await screen.findByText(
      'qwen2.5:0.5b was not downloaded. Ollama: pull model manifest: file does not exist',
    ),
  ).toBeInTheDocument()
})

it('starts Ollama from the same control when it is not running, and says so', async () => {
  api.ollama.mockResolvedValue({
    installed: true,
    state: 'stopped',
    models: [],
  })
  mount()
  expect(
    await screen.findByText(
      'Ollama is not running. Start it on this server to download a model.',
    ),
  ).toBeInTheDocument()
  // The name typed stays while Ollama starts; nothing can be downloaded before it answers.
  await userEvent.clear(screen.getByLabelText('Model name'))
  await userEvent.type(screen.getByLabelText('Model name'), 'gemma3:1b')
  expect(
    screen.queryByRole('button', { name: 'Download' }),
  ).not.toBeInTheDocument()
  api.ollama.mockResolvedValue(running)
  await userEvent.click(screen.getByRole('button', { name: 'Start Ollama' }))
  expect(api.ollamaStart).toHaveBeenCalledWith('tenant-a', expect.anything())
  expect(
    await screen.findByRole('button', { name: 'Download' }),
  ).toBeInTheDocument()
  expect(screen.getByLabelText('Model name')).toHaveValue('gemma3:1b')
  expect(api.ollamaPull).not.toHaveBeenCalled()
})

it('lists the installed models with their size, and removes one after a confirmation', async () => {
  api.ollama.mockResolvedValue({
    ...running,
    models: ['qwen2.5:0.5b', 'llama3.2:1b'],
    model_sizes: { 'qwen2.5:0.5b': 397821319, 'llama3.2:1b': 1321098329 },
  })
  api.ollamaRemove.mockResolvedValue({
    ...running,
    models: ['llama3.2:1b'],
    model_sizes: { 'llama3.2:1b': 1321098329 },
  })
  mount()
  const list = await screen.findByRole('list', { name: 'Installed models' })
  const rows = within(list).getAllByRole('listitem')
  expect(rows).toHaveLength(2)
  expect(rows[0]).toHaveTextContent('qwen2.5:0.5b')
  expect(rows[0]).toHaveTextContent('379 MiB')
  expect(rows[1]).toHaveTextContent('1.2 GiB')

  api.ollama.mockResolvedValue({
    ...running,
    models: ['llama3.2:1b'],
    model_sizes: { 'llama3.2:1b': 1321098329 },
  })
  await userEvent.click(
    within(rows[0]).getByRole('button', { name: 'Remove qwen2.5:0.5b' }),
  )
  // Nothing is deleted before the person confirms.
  expect(api.ollamaRemove).not.toHaveBeenCalled()
  await userEvent.click(
    within(await screen.findByRole('dialog')).getByRole('button', {
      name: 'Remove model',
    }),
  )
  await waitFor(() =>
    expect(api.ollamaRemove).toHaveBeenCalledWith(
      'qwen2.5:0.5b',
      expect.anything(),
    ),
  )
  await waitFor(() =>
    expect(within(list).getAllByRole('listitem')).toHaveLength(1),
  )
})

it('has no accessibility violations with a download running and a model installed', async () => {
  api.ollama.mockResolvedValue({
    ...running,
    models: ['llama3.2:1b'],
    model_sizes: { 'llama3.2:1b': 1321098329 },
  })
  api.ollamaPullStatus.mockResolvedValue(
    pull({ status: 'pulling 7c2f', completed: 40, total: 100 }),
  )
  const { container } = mount()
  await userEvent.click(await screen.findByRole('button', { name: 'Download' }))
  await screen.findByRole('progressbar')
  // jsdom has no layout: contrast is judged on the real page (the UI proof).
  const result = await axe.run(container, {
    rules: { 'color-contrast': { enabled: false } },
  })
  expect(result.violations.map((v) => `${v.id}: ${v.nodes.length}`)).toEqual([])
})

it('says there is no model yet', async () => {
  mount()
  expect(await screen.findByText('No model yet.')).toBeInTheDocument()
  expect(
    screen.queryByRole('list', { name: 'Installed models' }),
  ).not.toBeInTheDocument()
})
