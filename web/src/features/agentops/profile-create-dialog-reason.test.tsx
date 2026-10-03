// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// HU-R16 (refresh 06): with an API key from Providers, Register stayed disabled with no
// reason until two home folders were typed. The server already makes a key-backed
// profile's homes when both are empty (provider_profile.go managedProfileHomes), so
// nothing has to be typed; when something IS missing, the form says what.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, expect, it, vi } from 'vitest'

const auth = vi.hoisted(() => ({
  permissions: new Set(['sessions:profile:write', 'sessions:provider:read']),
}))
vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({
    principal: { user_id: 'u1', aal: 1 },
    activeTenant: 't1',
    can: (p: string) => auth.permissions.has(p),
  }),
}))
vi.mock('@/components/ui/toaster', () => ({
  toast: { success: vi.fn(), warning: vi.fn(), error: vi.fn() },
}))
const api = vi.hoisted(() => ({
  createProfile: vi.fn(),
  patchProfile: vi.fn(),
}))
vi.mock('./api', async (orig) => ({
  ...(await orig<typeof import('./api')>()),
  agentOpsApi: api,
}))
const providers = vi.hoisted(() => ({ list: vi.fn() }))
vi.mock('@/features/providers/api', async (orig) => ({
  ...(await orig<typeof import('@/features/providers/api')>()),
  providersApi: providers,
}))

import { ProfileCreateDialog } from './profile-create-dialog'

function mount() {
  const qc = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  })
  return render(
    <QueryClientProvider client={qc}>
      <ProfileCreateDialog open onOpenChange={vi.fn()} />
    </QueryClientProvider>,
  )
}

beforeEach(() => {
  vi.clearAllMocks()
  providers.list.mockResolvedValue({
    items: [
      {
        provider_ref: 'prv_ollama',
        kind: 'ollama',
        state: 'active',
        display_name: 'Local Ollama',
      },
    ],
    has_more: false,
  })
  api.createProfile.mockResolvedValue({ profile_ref: 'ppf_new' })
})

it('registers a key-backed profile with no folder typed: the server makes its homes', async () => {
  mount()
  const user = userEvent.setup()
  await user.selectOptions(screen.getByLabelText('Driver'), 'codex')
  await user.selectOptions(
    screen.getByLabelText('Authentication source'),
    'managed_injection',
  )
  await screen.findByRole('option', { name: /Local Ollama/ })
  await user.selectOptions(screen.getByLabelText('Provider'), 'prv_ollama')
  await user.click(screen.getByRole('button', { name: 'Register' }))
  await waitFor(() =>
    expect(api.createProfile).toHaveBeenCalledWith({
      driver: 'codex',
      config_home: '',
      user_home: '',
      auth_source: 'managed_injection',
      provider_record_ref: 'prv_ollama',
    }),
  )
})

it('says what is missing next to Register while it cannot be pressed', async () => {
  mount()
  const user = userEvent.setup()
  await user.selectOptions(screen.getByLabelText('Driver'), 'codex')
  await user.selectOptions(
    screen.getByLabelText('Authentication source'),
    'managed_injection',
  )
  expect(screen.getByRole('button', { name: 'Register' })).toBeDisabled()
  expect(
    screen.getByText('Choose the API key this profile uses.'),
  ).toBeInTheDocument()

  await screen.findByRole('option', { name: /Local Ollama/ })
  await user.selectOptions(screen.getByLabelText('Provider'), 'prv_ollama')
  await user.type(screen.getByLabelText('Settings folder'), '/fixture/config')
  expect(screen.getByRole('button', { name: 'Register' })).toBeDisabled()
  expect(
    screen.getByText('Name both folders, or leave both empty.'),
  ).toBeInTheDocument()

  await user.selectOptions(screen.getByLabelText('Authentication source'), '')
  await user.clear(screen.getByLabelText('Settings folder'))
  expect(screen.getByRole('button', { name: 'Register' })).toBeDisabled()
  expect(
    screen.getByText(
      'Name both folders. They must already exist on this server.',
    ),
  ).toBeInTheDocument()
})
