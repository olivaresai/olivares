// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, expect, it, vi } from 'vitest'
import { useModulesStore } from '@/stores/modules'
import { useSessionStore } from '@/stores/session'

const state = vi.hoisted(() => {
  const info: { isSuccess: boolean; data: { modules_not_enabled: string[] } } =
    { isSuccess: true, data: { modules_not_enabled: [] } }
  return {
    availability: vi.fn(),
    auth: {
      activeTenant: 'tenant-a',
      principal: { user_id: 'alice' },
      can: (_permission: string): boolean => true,
    },
    info,
  }
})
vi.mock('@/lib/auth/context', () => ({ useAuth: () => state.auth }))
vi.mock('@/lib/hooks/use-server-info', () => ({
  useServerInfo: () => state.info,
}))
vi.mock('./api', () => ({ modelsApi: { availability: state.availability } }))
import { ModelAvailabilityPicker } from './availability-picker'

const profile = {
  profile_ref: 'ppf_a',
  driver: 'codex',
  auth_source: 'provider_account_home',
}
const fresh = {
  source_ref: 'ppf_a',
  account_ref: 'ppf_a',
  driver: 'codex',
  provider_kind: 'openai',
  state: 'fresh',
  models: [{ id: 'available-a', seen_at: '2026-10-02T14:00:00Z' }],
  seen_at: '2026-10-02T14:00:00Z',
  checked_at: '2026-10-02T14:01:00Z',
}
const onChange = vi.fn()
function show(props = {}) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  const view = (extra = {}) => (
    <QueryClientProvider client={client}>
      <ModelAvailabilityPicker
        profile={profile}
        value=""
        onChange={onChange}
        enabled
        {...props}
        {...extra}
      />
    </QueryClientProvider>
  )
  const mounted = render(view())
  return {
    ...mounted,
    client,
    update: (extra = {}) => mounted.rerender(view(extra)),
  }
}
beforeEach(() => {
  vi.resetAllMocks()
  state.auth.activeTenant = 'tenant-a'
  state.auth.can = () => true
  state.info = { isSuccess: true, data: { modules_not_enabled: [] } }
  useModulesStore.getState().setOff([])
  state.availability.mockResolvedValue({
    items: [fresh],
    refresh_interval_seconds: 3600,
  })
})
it('offers only this account’s fresh model IDs, observation time and tool default; custom model input still works', async () => {
  state.availability.mockResolvedValue({
    items: [
      fresh,
      { ...fresh, account_ref: 'ppf_other', models: [{ id: 'foreign-model' }] },
    ],
    refresh_interval_seconds: 3600,
  })
  show({ value: 'custom-model' })
  await screen.findByText(/2026-10-02T14:00:00Z/)
  expect(document.querySelector('option[value="available-a"]')).not.toBeNull()
  expect(document.querySelector('option[value="foreign-model"]')).toBeNull()
  expect(screen.getByRole('combobox', { name: 'Model' })).toHaveValue(
    'custom-model',
  )
  await userEvent.type(screen.getByRole('combobox', { name: 'Model' }), 'x')
  expect(onChange).toHaveBeenCalledWith('custom-modelx')
  await userEvent.click(
    screen.getByRole('button', { name: 'Use the tool default' }),
  )
  expect(onChange).toHaveBeenCalledWith('')
  expect(state.availability).toHaveBeenCalledWith({
    tenant: 'tenant-a',
    signal: expect.any(AbortSignal),
    query: { account_ref: 'ppf_a' },
  })
})
it.each([
  ['stale', 'Model list is out of date'],
  [
    'failed',
    'Models could not be refreshed. Check the provider connection or sign in again.',
  ],
  [
    'unsupported',
    'Automatic model discovery is unavailable for this tool. Use its default model.',
  ],
])(
  'keeps %s last-observed IDs out of current choices',
  async (status, message) => {
    state.availability.mockResolvedValue({
      items: [{ ...fresh, state: status, message }],
      refresh_interval_seconds: 3600,
    })
    show()
    expect(await screen.findByText(message)).toBeVisible()
    expect(document.querySelector('option[value="available-a"]')).toBeNull()
    expect(
      screen.getByRole('button', { name: 'Use the tool default' }),
    ).toBeVisible()
  },
)
it('explains an empty fresh list and a source not yet observed', async () => {
  state.availability.mockResolvedValue({
    items: [{ ...fresh, models: [] }],
    refresh_interval_seconds: 3600,
  })
  const view = show()
  expect(
    await screen.findByText('This provider listed no models.'),
  ).toBeVisible()
  state.availability.mockResolvedValue({
    items: [],
    refresh_interval_seconds: 3600,
  })
  view.update({ profile: { ...profile, profile_ref: 'ppf_new' } })
  expect(await screen.findByText('Models are being discovered.')).toBeVisible()
})
it.each([
  'unanswered',
  'module-off',
  'store-off',
  'permission-denied',
  'closed',
  'unresolved-profile',
])('does not read availability when %s', async (gate) => {
  if (gate === 'unanswered') state.info.isSuccess = false
  if (gate === 'module-off') state.info.data.modules_not_enabled = [' MODELS ']
  if (gate === 'store-off') useModulesStore.getState().setOff(['models'])
  if (gate === 'permission-denied') state.auth.can = () => false
  show({
    enabled: gate !== 'closed',
    ...(gate === 'unresolved-profile' ? { profile: undefined } : {}),
  })
  await new Promise((resolve) => setTimeout(resolve, 30))
  expect(state.availability).not.toHaveBeenCalled()
})
it('uses the selected managed provider and aborts a pending old-account read on selection change', async () => {
  let oldSignal: AbortSignal | undefined
  state.availability.mockImplementation((request) => {
    if (request.query.account_ref) {
      oldSignal = request.signal
      return new Promise(() => {})
    }
    return Promise.resolve({
      items: [
        {
          ...fresh,
          source_ref: 'provider-b',
          provider_ref: 'provider-b',
          models: [{ id: 'available-b' }],
        },
      ],
      refresh_interval_seconds: 3600,
    })
  })
  const view = show()
  await waitFor(() => expect(oldSignal).toBeDefined())
  view.update({
    profile: {
      ...profile,
      auth_source: 'managed_injection',
      provider_record_ref: 'provider-b',
    },
  })
  await waitFor(() =>
    expect(
      document.querySelector('option[value="available-b"]'),
    ).not.toBeNull(),
  )
  expect(oldSignal?.aborted).toBe(true)
  expect(state.availability).toHaveBeenLastCalledWith({
    tenant: 'tenant-a',
    signal: expect.any(AbortSignal),
    query: { provider_ref: 'provider-b' },
  })
})
it('never paints the previous tenant’s available IDs while the new tenant is loading', async () => {
  const view = show()
  await waitFor(() =>
    expect(
      document.querySelector('option[value="available-a"]'),
    ).not.toBeNull(),
  )
  state.auth.activeTenant = 'tenant-b'
  state.availability.mockReturnValue(new Promise(() => {}))
  view.update()
  expect(document.querySelector('option[value="available-a"]')).toBeNull()
  await waitFor(() =>
    expect(state.availability).toHaveBeenLastCalledWith({
      tenant: 'tenant-b',
      signal: expect.any(AbortSignal),
      query: { account_ref: 'ppf_a' },
    }),
  )
})
it.each(['permission', 'module', 'closed'])(
  'hides cached choices immediately when %s leaves',
  async (gate) => {
    const view = show()
    await waitFor(() =>
      expect(
        document.querySelector('option[value="available-a"]'),
      ).not.toBeNull(),
    )
    if (gate === 'permission') state.auth.can = () => false
    if (gate === 'module') state.info.data.modules_not_enabled = ['models']
    view.update({ enabled: gate !== 'closed' })
    expect(document.querySelector('option[value="available-a"]')).toBeNull()
    expect(screen.queryByText(/Last observed/)).toBeNull()
  },
)
it('retires the old credential’s catalog even when the principal and tenant stay the same', async () => {
  const view = show()
  await waitFor(() =>
    expect(
      document.querySelector('option[value="available-a"]'),
    ).not.toBeNull(),
  )
  state.availability.mockReturnValue(new Promise(() => {}))
  useSessionStore.setState((current) => ({
    credentialGeneration: current.credentialGeneration + 1,
  }))
  view.update()
  expect(document.querySelector('option[value="available-a"]')).toBeNull()
  await waitFor(() => expect(state.availability).toHaveBeenCalledTimes(2))
})

it('stops offering the cached fresh IDs if the next availability read fails', async () => {
  const view = show()
  await waitFor(() =>
    expect(
      document.querySelector('option[value="available-a"]'),
    ).not.toBeNull(),
  )
  state.availability.mockRejectedValue(new Error('availability read failed'))
  await view.client.invalidateQueries()
  expect(
    await screen.findByText(
      'Models could not be refreshed. Check the provider connection or sign in again.',
    ),
  ).toBeVisible()
  expect(document.querySelector('option[value="available-a"]')).toBeNull()
  expect(
    screen.getByRole('button', { name: 'Use the tool default' }),
  ).toBeVisible()
})
