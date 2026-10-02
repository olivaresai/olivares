// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, expect, it, vi } from 'vitest'
const auth = vi.hoisted(() => ({
  principal: 'u1',
  tenant: 't1',
  permissions: new Set<string>(),
}))
vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({
    principal: { user_id: auth.principal, aal: 1 },
    activeTenant: auth.tenant,
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
import { ProfileAuthentication } from './profile-authentication'
import { ProfileCreateDialog } from './profile-create-dialog'
import { useSessionStore } from '@/stores/session'
const records = [
  {
    provider_ref: 'prv_a',
    kind: 'anthropic',
    state: 'active',
    display_name: 'Claude fixture',
  },
  {
    provider_ref: 'prv_o',
    kind: 'openai',
    state: 'active',
    display_name: 'Codex fixture',
  },
  {
    provider_ref: 'prv_c',
    kind: 'openai_compatible',
    state: 'active',
    display_name: 'Compatible fixture',
  },
  {
    provider_ref: 'prv_r',
    kind: 'anthropic',
    state: 'revoked',
    display_name: 'Revoked fixture',
  },
]
function mount() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  const element = () => (
    <QueryClientProvider client={qc}>
      <ProfileCreateDialog open onOpenChange={vi.fn()} />
    </QueryClientProvider>
  )
  const ui = render(element())
  return { ...ui, refresh: () => ui.rerender(element()) }
}
async function homes() {
  const user = userEvent.setup()
  await user.selectOptions(screen.getByLabelText('Driver'), 'claude')
  await user.type(screen.getByLabelText('Settings folder'), '/fixture/config')
  await user.type(screen.getByLabelText('User home'), '/fixture/user')
  return user
}
beforeEach(() => {
  vi.clearAllMocks()
  auth.principal = 'u1'
  auth.tenant = 't1'
  auth.permissions = new Set([
    'sessions:profile:write',
    'sessions:provider:read',
  ])
  useSessionStore.setState({ credentialGeneration: 1 })
  providers.list.mockResolvedValue({ items: records, has_more: false })
  api.createProfile.mockResolvedValue({ profile_ref: 'ppf_new' })
})
it('requires explicit managed binding and offers only active compatible records', async () => {
  mount()
  const user = await homes()
  await user.selectOptions(
    screen.getByLabelText('Authentication source'),
    'managed_injection',
  )
  expect(screen.getByRole('button', { name: 'Register' })).toBeDisabled()
  expect(
    await screen.findByRole('option', { name: /Claude fixture/ }),
  ).toBeInTheDocument()
  expect(
    screen.queryByRole('option', { name: /Codex fixture|Revoked fixture/ }),
  ).toBeNull()
  await user.selectOptions(screen.getByLabelText('Provider'), 'prv_a')
  await user.click(screen.getByRole('button', { name: 'Register' }))
  await waitFor(() =>
    expect(api.createProfile).toHaveBeenCalledWith({
      driver: 'claude',
      config_home: '/fixture/config',
      user_home: '/fixture/user',
      auth_source: 'managed_injection',
      provider_record_ref: 'prv_a',
    }),
  )
})
it('clears incompatible selection on driver change and never substitutes another record', async () => {
  mount()
  const user = await homes()
  await user.selectOptions(
    screen.getByLabelText('Authentication source'),
    'managed_injection',
  )
  await screen.findByRole('option', { name: /Claude fixture/ })
  await user.selectOptions(screen.getByLabelText('Provider'), 'prv_a')
  await user.selectOptions(screen.getByLabelText('Driver'), 'codex')
  expect(screen.getByRole('button', { name: 'Register' })).toBeDisabled()
  expect(api.createProfile).not.toHaveBeenCalled()
})
it('account-home selection withdraws the provider binding without copying a credential', async () => {
  mount()
  const user = await homes()
  await user.selectOptions(
    screen.getByLabelText('Authentication source'),
    'managed_injection',
  )
  await screen.findByRole('option', { name: /Claude fixture/ })
  await user.selectOptions(screen.getByLabelText('Provider'), 'prv_a')
  await user.selectOptions(
    screen.getByLabelText('Authentication source'),
    'provider_account_home',
  )
  await user.click(screen.getByRole('button', { name: 'Register' }))
  await waitFor(() =>
    expect(api.createProfile).toHaveBeenCalledWith({
      driver: 'claude',
      config_home: '/fixture/config',
      user_home: '/fixture/user',
      auth_source: 'provider_account_home',
    }),
  )
})
it('registers Claude Code with its own login and nothing typed', async () => {
  mount()
  const user = userEvent.setup()
  await user.click(screen.getByRole('button', { name: 'Register' }))
  await waitFor(() =>
    expect(api.createProfile).toHaveBeenCalledWith({
      driver: 'claude',
      config_home: '',
      user_home: '',
      auth_source: 'provider_account_home',
    }),
  )
})
it('asks for both homes once one is named', async () => {
  mount()
  const user = userEvent.setup()
  await user.type(screen.getByLabelText('Settings folder'), '/fixture/config')
  expect(screen.getByRole('button', { name: 'Register' })).toBeDisabled()
  await user.type(screen.getByLabelText('User home'), '/fixture/user')
  expect(screen.getByRole('button', { name: 'Register' })).toBeEnabled()
})
it('does not read providers or allow a managed write without provider-read permission', async () => {
  auth.permissions.delete('sessions:provider:read')
  mount()
  const user = await homes()
  await user.selectOptions(
    screen.getByLabelText('Authentication source'),
    'managed_injection',
  )
  expect(providers.list).not.toHaveBeenCalled()
  expect(screen.getByRole('button', { name: 'Register' })).toBeDisabled()
})
it.each(['principal', 'tenant', 'credential'] as const)(
  'discards the draft when %s changes',
  async (change) => {
    const ui = mount()
    const user = await homes()
    await user.selectOptions(
      screen.getByLabelText('Authentication source'),
      'managed_injection',
    )
    await screen.findByRole('option', { name: /Claude fixture/ })
    await user.selectOptions(screen.getByLabelText('Provider'), 'prv_a')
    if (change === 'principal') auth.principal = 'u2'
    if (change === 'tenant') auth.tenant = 't2'
    if (change === 'credential')
      useSessionStore.setState({ credentialGeneration: 2 })
    ui.refresh()
    // The draft is gone: back to the defaults, with no provider binding left over.
    expect(screen.getByLabelText('Driver')).toHaveValue('claude')
    expect(screen.getByLabelText('Settings folder')).toHaveValue('')
    expect(screen.getByLabelText('Authentication source')).toHaveValue(
      'provider_account_home',
    )
    expect(screen.queryByLabelText('Provider')).toBeNull()
    expect(api.createProfile).not.toHaveBeenCalled()
  },
)
it('withholds a create form when profile write permission is withdrawn', async () => {
  const ui = mount()
  await homes()
  auth.permissions.delete('sessions:profile:write')
  ui.refresh()
  expect(screen.queryByRole('button', { name: 'Register' })).toBeNull()
  expect(api.createProfile).not.toHaveBeenCalled()
})

const profile = {
  profile_ref: 'ppf_edit',
  driver: 'claude',
  environment_ref: 'env',
  state: 'active',
  local_environment: true,
  operable: true,
  auth_source: 'managed_injection',
  provider_record_ref: 'prv_a',
}
it('holds the authentication editor until an authorized point read is complete', async () => {
  const qc = new QueryClient()
  render(
    <QueryClientProvider client={qc}>
      <ProfileAuthentication profile={profile} readReady={false} />
    </QueryClientProvider>,
  )
  expect(
    screen.queryByRole('button', { name: 'Edit provider authentication' }),
  ).toBeNull()
  expect(api.patchProfile).not.toHaveBeenCalled()
})
it('withdraws a binding explicitly when editing to saved account login', async () => {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  api.patchProfile.mockResolvedValue(profile)
  render(
    <QueryClientProvider client={qc}>
      <ProfileAuthentication profile={profile} readReady />
    </QueryClientProvider>,
  )
  const user = userEvent.setup()
  await user.click(
    screen.getByRole('button', { name: 'Edit provider authentication' }),
  )
  await user.selectOptions(
    screen.getByLabelText('Authentication source'),
    'provider_account_home',
  )
  await user.click(screen.getByRole('button', { name: 'Save authentication' }))
  await waitFor(() =>
    expect(api.patchProfile).toHaveBeenCalledWith('ppf_edit', {
      auth_source: 'provider_account_home',
      provider_record_ref: '',
    }),
  )
})
it('returns focus to the authentication editor trigger on Escape', async () => {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  render(
    <QueryClientProvider client={qc}>
      <ProfileAuthentication profile={profile} readReady />
    </QueryClientProvider>,
  )
  const trigger = screen.getByRole('button', {
    name: 'Edit provider authentication',
  })
  trigger.focus()
  const user = userEvent.setup()
  await user.keyboard('{Enter}')
  await user.keyboard('{Escape}')
  await waitFor(() => expect(trigger).toHaveFocus())
})
