// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useState } from 'react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, expect, it, vi } from 'vitest'
import { toast } from '@/components/ui/toaster'
import { ApiError } from '@/lib/api/errors'
import { providersApi } from './api'
import { ProviderCreateDialog } from './provider-create-dialog'
import { PROVIDER_KINDS } from './kinds'
import { en } from './i18n'
import type { ProviderKind, ProviderRecordDTO } from './types'

vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({ can: () => true, activeTenant: 'tenant', principal: null }),
}))

afterEach(() => {
  vi.restoreAllMocks()
})

it.each(PROVIDER_KINDS)(
  'guides creation of %s without repeated help',
  async (kind) => {
    const user = userEvent.setup()
    render(
      <QueryClientProvider client={new QueryClient()}>
        <ProviderCreateDialog open initialKind={kind} onOpenChange={() => {}} />
      </QueryClientProvider>,
    )
    expect(screen.getByRole('textbox', { name: 'Name' })).toHaveAttribute(
      'placeholder',
      en.kinds[kind],
    )
    expect(screen.getAllByText(en.kindHints[kind])).toHaveLength(1)
    expect(screen.getByRole('combobox', { name: 'Provider' })).toBeRequired()
    expect(screen.getByRole('textbox', { name: 'Name' })).not.toBeRequired()
    const submit = screen.getByRole('button', { name: 'Add provider' })
    const endpointRequired = kind === 'ollama' || kind === 'openai_compatible'
    if (endpointRequired) {
      const endpoint = screen.getByRole('textbox', { name: 'Endpoint' })
      expect(endpoint).toBeRequired()
      await user.clear(endpoint)
      expect(submit).toBeDisabled()
      expect(submit).toHaveAccessibleDescription(/Endpoint/)
      expect(screen.getByRole('status')).toHaveTextContent('Endpoint')
      await user.type(
        endpoint,
        kind === 'ollama'
          ? 'http://localhost:11434'
          : 'https://llm.example.com',
      )
    }
    if (kind !== 'ollama') {
      const key = screen.getByLabelText(/^API key/)
      expect(key).toBeRequired()
      expect(submit).toBeDisabled()
      expect(submit).toHaveAccessibleDescription(/API key/)
      expect(screen.getByRole('status')).toHaveTextContent('API key')
      await user.type(key, 'synthetic-test-key')
    } else {
      expect(screen.queryByLabelText('API key')).not.toBeInTheDocument()
    }
    expect(submit).toBeEnabled()
    expect(screen.queryByRole('status')).not.toBeInTheDocument()
  },
)

// The endpoint hint says what the engine accepts for this kind (validProviderBaseURL in
// modules/sessions/provider_record.go): a local model server is plain http, so
// anthropic, xai and openai_compatible also take http at a loopback or private
// address; openai is https only. "Must be https." on a local anthropic or xai
// endpoint was wrong: the engine accepts http://127.0.0.1.
const LOCAL_OPTIONAL =
  /official endpoint\. Otherwise https, or plain http at a loopback or private-network address\.$/
const ENDPOINT_HINT: Record<ProviderKind, RegExp | null> = {
  anthropic: LOCAL_OPTIONAL,
  openai: /Must be https\.$/,
  xai: LOCAL_OPTIONAL,
  gemini: null,
  // The endpoint is required here, so the hint asks for it.
  openai_compatible:
    /^Enter the service endpoint: https, or plain http at a loopback or private-network address\.$/,
  // The local model's address is filled in for the operator; no hint repeats it.
  ollama: null,
}

it.each(PROVIDER_KINDS)('states which endpoints %s accepts', async (kind) => {
  const user = userEvent.setup()
  render(
    <QueryClientProvider client={new QueryClient()}>
      <ProviderCreateDialog open initialKind={kind} onOpenChange={() => {}} />
    </QueryClientProvider>,
  )
  const advanced = screen.queryByRole('button', {
    name: 'Advanced: custom endpoint',
  })
  if (advanced) await user.click(advanced)
  if (kind === 'gemini') {
    expect(
      screen.queryByRole('textbox', { name: 'Endpoint' }),
    ).not.toBeInTheDocument()
    return
  }
  const endpoint = screen.getByRole('textbox', { name: 'Endpoint' })
  const hint = ENDPOINT_HINT[kind]
  if (hint) expect(endpoint).toHaveAccessibleDescription(hint)
  else expect(endpoint).not.toHaveAccessibleDescription()
})

// The setup wizard's "Add a local model" opens this form on ollama: the address
// the form knows is filled in at once, and again after a create resets the form.
it('opens a local model on its default address', async () => {
  const user = userEvent.setup()
  const create = vi.spyOn(providersApi, 'create').mockResolvedValue({
    provider_ref: 'prv_local',
    kind: 'ollama',
    display_name: 'Ollama',
    state: 'active',
  } as ProviderRecordDTO)
  render(
    <QueryClientProvider client={new QueryClient()}>
      <ProviderCreateDialog open initialKind="ollama" onOpenChange={() => {}} />
    </QueryClientProvider>,
  )
  const endpoint = screen.getByRole('textbox', { name: 'Endpoint' })
  expect(endpoint).toHaveValue('http://127.0.0.1:11434')
  const submit = screen.getByRole('button', { name: 'Add provider' })
  expect(submit).toBeEnabled()
  expect(screen.queryByRole('status')).not.toBeInTheDocument()
  await user.clear(endpoint)
  await user.type(endpoint, 'http://192.168.1.5:11434')
  await user.click(submit)
  expect(create).toHaveBeenCalledWith({
    kind: 'ollama',
    display_name: 'Ollama',
    base_url: 'http://192.168.1.5:11434',
  })
  await waitFor(() => expect(endpoint).toHaveValue('http://127.0.0.1:11434'))
})

it('clears another provider endpoint when switching to Gemini', async () => {
  const user = userEvent.setup()
  const create = vi.spyOn(providersApi, 'create').mockResolvedValue({
    provider_ref: 'prv_gemini',
    kind: 'gemini',
    display_name: 'Gemini',
    state: 'active',
  } as ProviderRecordDTO)
  render(
    <QueryClientProvider client={new QueryClient()}>
      <ProviderCreateDialog
        open
        initialKind="openai_compatible"
        onOpenChange={() => {}}
      />
    </QueryClientProvider>,
  )
  await user.type(
    screen.getByRole('textbox', { name: 'Endpoint' }),
    'https://gateway.example/v1',
  )
  await user.click(screen.getByRole('combobox', { name: 'Provider' }))
  await user.click(screen.getByRole('option', { name: 'Gemini' }))
  expect(
    screen.queryByRole('textbox', { name: 'Endpoint' }),
  ).not.toBeInTheDocument()
  await user.type(screen.getByLabelText(/^API key/), 'synthetic-gemini-key')
  await user.click(screen.getByRole('button', { name: 'Add provider' }))
  await waitFor(() =>
    expect(create).toHaveBeenCalledWith({
      kind: 'gemini',
      display_name: 'Gemini',
      api_key: 'synthetic-gemini-key',
    }),
  )
})

// The providers view keeps the dialog mounted and reopens it, so a key typed before
// Cancel must not survive the next opening.
it('Cancel clears the typed key before the dialog closes', async () => {
  const user = userEvent.setup()
  function Openable() {
    const [open, setOpen] = useState(true)
    return (
      <>
        <button type="button" onClick={() => setOpen(true)}>
          open
        </button>
        <ProviderCreateDialog
          open={open}
          onOpenChange={setOpen}
          initialKind="anthropic"
        />
      </>
    )
  }
  render(
    <QueryClientProvider client={new QueryClient()}>
      <Openable />
    </QueryClientProvider>,
  )
  await user.type(screen.getByLabelText(/^API key/), 'synthetic-typed-key')
  await user.click(screen.getByRole('button', { name: 'Cancel' }))
  await user.click(screen.getByRole('button', { name: 'open' }))
  expect(screen.getByLabelText(/^API key/)).toHaveValue('')
})

// Escape is the dialog's own close, not a button: it takes the same reset.
it('Escape clears the typed key before the dialog closes', async () => {
  const user = userEvent.setup()
  function Openable() {
    const [open, setOpen] = useState(true)
    return (
      <>
        <button type="button" onClick={() => setOpen(true)}>
          open
        </button>
        <ProviderCreateDialog
          open={open}
          onOpenChange={setOpen}
          initialKind="anthropic"
        />
      </>
    )
  }
  render(
    <QueryClientProvider client={new QueryClient()}>
      <Openable />
    </QueryClientProvider>,
  )
  await user.type(screen.getByLabelText(/^API key/), 'synthetic-typed-key')
  await user.keyboard('{Escape}')
  await waitFor(() =>
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument(),
  )
  await user.click(screen.getByRole('button', { name: 'open' }))
  expect(screen.getByLabelText(/^API key/)).toHaveValue('')
})

// A modal dialog hides the rest of the page from assistive technology, toasts
// included: a refusal is announced inside the dialog, next to the form.
it('announces a refused create inside the dialog', async () => {
  const user = userEvent.setup()
  vi.spyOn(providersApi, 'create').mockRejectedValue(
    new Error('the engine refused the key'),
  )
  render(
    <QueryClientProvider client={new QueryClient()}>
      <ProviderCreateDialog open initialKind="openai" onOpenChange={() => {}} />
    </QueryClientProvider>,
  )
  await user.type(screen.getByLabelText(/^API key/), 'synthetic-test-key')
  await user.click(screen.getByRole('button', { name: 'Add provider' }))
  expect(
    await within(screen.getByRole('dialog')).findByRole('alert'),
  ).toHaveTextContent('the engine refused the key')
})

it('says a refusal without a message and clears it on a new attempt', async () => {
  const user = userEvent.setup()
  vi.spyOn(providersApi, 'create')
    .mockRejectedValueOnce('no message')
    .mockReturnValueOnce(new Promise(() => {}))
  render(
    <QueryClientProvider client={new QueryClient()}>
      <ProviderCreateDialog open initialKind="openai" onOpenChange={() => {}} />
    </QueryClientProvider>,
  )
  await user.type(screen.getByLabelText(/^API key/), 'synthetic-test-key')
  const submit = screen.getByRole('button', { name: 'Add provider' })
  await user.click(submit)
  expect(await screen.findByRole('alert')).toHaveTextContent(
    'Something went wrong.',
  )
  await user.click(submit)
  expect(screen.queryByRole('alert')).not.toBeInTheDocument()
})

// The providers page keeps this dialog mounted and only toggles it.
it('does not show an old refusal when the dialog opens again', async () => {
  const user = userEvent.setup()
  vi.spyOn(providersApi, 'create').mockRejectedValue(
    new Error('the engine refused the key'),
  )
  function Page() {
    const [open, setOpen] = useState(true)
    return (
      <>
        <button onClick={() => setOpen(true)}>Open</button>
        <ProviderCreateDialog
          open={open}
          initialKind="openai"
          onOpenChange={setOpen}
        />
      </>
    )
  }
  render(
    <QueryClientProvider client={new QueryClient()}>
      <Page />
    </QueryClientProvider>,
  )
  await user.type(screen.getByLabelText(/^API key/), 'synthetic-test-key')
  await user.click(screen.getByRole('button', { name: 'Add provider' }))
  expect(await screen.findByRole('alert')).toHaveTextContent('refused')
  await user.click(screen.getByRole('button', { name: 'Cancel' }))
  await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
  await user.click(screen.getByRole('button', { name: 'Open' }))
  expect(await screen.findByRole('dialog')).toBeInTheDocument()
  expect(screen.queryByRole('alert')).not.toBeInTheDocument()
})

// A role refusal stays with the shared reporter; the dialog does not claim it.
it('leaves a forbidden refusal to the shared reporter', async () => {
  const user = userEvent.setup()
  const create = vi
    .spyOn(providersApi, 'create')
    .mockRejectedValue(new ApiError(403, 'forbidden', 'not allowed'))
  const warned = vi.spyOn(toast, 'warning').mockReturnValue('test-toast')
  render(
    <QueryClientProvider client={new QueryClient()}>
      <ProviderCreateDialog open initialKind="openai" onOpenChange={() => {}} />
    </QueryClientProvider>,
  )
  await user.type(screen.getByLabelText(/^API key/), 'synthetic-test-key')
  await user.click(screen.getByRole('button', { name: 'Add provider' }))
  await waitFor(() => expect(create).toHaveBeenCalled())
  await waitFor(() =>
    expect(screen.getByRole('button', { name: 'Add provider' })).toBeEnabled(),
  )
  expect(screen.queryByRole('alert')).not.toBeInTheDocument()
  expect(warned).toHaveBeenCalled()
})

// The vendor presets come from the engine (GET /v1/m/sessions/provider-presets), never
// from a list in the console. A preset fixes the kind and the endpoint, so the form
// asks for the key only and posts the preset's ref as `service`.
it('lists the engine presets and creates DeepSeek for Claude Code from a key', async () => {
  const user = userEvent.setup()
  vi.spyOn(providersApi, 'presets').mockResolvedValue({
    items: [
      {
        ref: 'deepseek',
        title: 'DeepSeek',
        monogram: 'DS',
        color: '#6780F6',
        kind: 'openai_compatible',
        base_url: 'https://api.deepseek.com',
        auth: 'bearer',
        models: {},
        docs_url: 'https://api-docs.deepseek.com/',
        key_url: 'https://platform.deepseek.com/api_keys',
        governed: true,
      },
      {
        ref: 'deepseek-anthropic',
        title: 'DeepSeek (Claude Code)',
        monogram: 'DS',
        color: '#6780F6',
        kind: 'anthropic',
        base_url: 'https://api.deepseek.com/anthropic',
        auth: 'bearer',
        models: { default: 'deepseek-flash' },
        docs_url: 'https://api-docs.deepseek.com/',
        key_url: 'https://platform.deepseek.com/api_keys',
      },
      {
        ref: 'openrouter',
        title: 'OpenRouter',
        monogram: 'OR',
        color: '#768BA3',
        kind: 'openai_compatible',
        base_url: 'https://openrouter.ai/api/v1',
        auth: 'bearer',
        models: {},
        docs_url: 'https://openrouter.ai/docs/quickstart',
      },
    ],
    has_more: false,
  })
  const create = vi.spyOn(providersApi, 'create').mockResolvedValue({
    provider_ref: 'prv_ds',
    kind: 'anthropic',
    display_name: 'DeepSeek (Claude Code)',
    state: 'active',
  } as ProviderRecordDTO)
  render(
    <QueryClientProvider client={new QueryClient()}>
      <ProviderCreateDialog open onOpenChange={() => {}} />
    </QueryClientProvider>,
  )
  await user.click(screen.getByRole('combobox', { name: 'Provider' }))
  // Each row says which tools it serves, by the engine's binding rule; the
  // governed row serves no tool and is not offered; the six kinds stay below.
  const deepseek = await screen.findByRole('option', {
    name: /^DeepSeek \(Claude Code\)/,
  })
  expect(deepseek).toHaveTextContent('Works with Claude Code')
  expect(screen.getByRole('option', { name: /^OpenRouter/ })).toHaveTextContent(
    'Works with Codex',
  )
  expect(
    screen.queryByRole('option', { name: /^DeepSeek$/ }),
  ).not.toBeInTheDocument()
  expect(screen.getByText(en.create.otherKinds)).toBeInTheDocument()
  for (const k of PROVIDER_KINDS)
    expect(
      screen.getByRole('option', { name: en.kinds[k] }),
    ).toBeInTheDocument()
  await user.click(deepseek)
  expect(
    screen.queryByRole('textbox', { name: 'Endpoint' }),
  ).not.toBeInTheDocument()
  expect(
    screen.queryByRole('button', { name: 'Advanced: custom endpoint' }),
  ).not.toBeInTheDocument()
  expect(
    screen.getByRole('link', { name: /DeepSeek \(Claude Code\)/ }),
  ).toHaveAttribute('href', 'https://platform.deepseek.com/api_keys')
  await user.type(screen.getByLabelText(/^API key/), 'synthetic-deepseek-key')
  await user.click(screen.getByRole('button', { name: 'Add provider' }))
  await waitFor(() =>
    expect(create).toHaveBeenCalledWith({
      kind: 'anthropic',
      service: 'deepseek-anthropic',
      display_name: 'DeepSeek (Claude Code)',
      api_key: 'synthetic-deepseek-key',
    }),
  )
})

// The engine refuses Codex for a vendor without the Responses API, whatever the kind says
// (GET /tools refused_services): the row must not promise a tool the bind then refuses.
it('does not promise Codex on a preset the engine refuses for it', async () => {
  const user = userEvent.setup()
  vi.spyOn(providersApi, 'presets').mockResolvedValue({
    items: [
      {
        ref: 'zai',
        title: 'Z.ai',
        monogram: 'Z',
        color: '#329797',
        kind: 'openai_compatible',
        base_url: 'https://api.z.ai/api/paas/v4',
        auth: 'bearer',
        models: {},
        docs_url: 'https://docs.z.ai/',
      },
    ],
    has_more: false,
  })
  render(
    <QueryClientProvider client={new QueryClient()}>
      <ProviderCreateDialog open onOpenChange={() => {}} />
    </QueryClientProvider>,
  )
  await user.click(screen.getByRole('combobox', { name: 'Provider' }))
  const zai = await screen.findByRole('option', { name: /^Z\.ai/ })
  expect(zai).not.toHaveTextContent('Codex')
})
