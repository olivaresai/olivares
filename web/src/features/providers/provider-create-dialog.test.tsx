// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, expect, it, vi } from 'vitest'
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

// The setup wizard's "Add a local model" opens this form on ollama (#476): the address
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
