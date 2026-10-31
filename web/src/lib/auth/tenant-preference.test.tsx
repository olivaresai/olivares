// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { act, cleanup, renderHook, waitFor } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { afterEach, expect, it, vi } from 'vitest'
import { queryKeys } from '@/lib/api/query'
import { AuthProvider, useAuth } from './context'
import { authApi, systemApi } from '@/lib/api/endpoints'
import { savedViewsApi } from '@/features/saved-views/api'
import { useSessionStore } from '@/stores/session'
import { useTenantStore } from '@/stores/tenant'
import type { Whoami } from '@/lib/api/types'

const principal = {
  kind: 'user',
  user_id: 'person',
  superadmin: false,
  grants: [
    { tenant: 'a', role: 'viewer' },
    { tenant: 'b', role: 'viewer' },
  ],
} as Whoami
const clients: QueryClient[] = []
function mount() {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  clients.push(client)
  return renderHook(() => useAuth(), {
    wrapper: ({ children }) => (
      <QueryClientProvider client={client}>
        <AuthProvider>{children}</AuthProvider>
      </QueryClientProvider>
    ),
  })
}
afterEach(() => {
  cleanup()
  clients.splice(0).forEach((client) => client.clear())
  useSessionStore.getState().clear()
  useTenantStore.getState().clear()
  vi.restoreAllMocks()
})
function session() {
  useSessionStore.getState().setSession({
    csrfToken: 'synthetic',
    sessionId: 'synthetic',
    expiresAt: '',
  })
}
it('keeps the second organization on the engine across a fresh console mount', async () => {
  session()
  vi.spyOn(authApi, 'whoami').mockResolvedValue(principal)
  let saved: string | undefined
  vi.spyOn(savedViewsApi, 'uiState').mockImplementation(async () => ({
    stored: !!saved,
    preferences: Object.fromEntries(saved ? [['olivares.tenant', saved]] : []),
  }))
  const save = vi
    .spyOn(savedViewsApi, 'saveUiState')
    .mockImplementation(async (state) => {
      saved = state.preferences?.['olivares.tenant'] ?? undefined
      return { stored: true }
    })
  const first = mount()
  await waitFor(() => expect(first.result.current.activeTenant).toBe('a'))
  act(() => first.result.current.setActiveTenant('b'))
  await waitFor(() =>
    expect(save).toHaveBeenCalledWith(
      expect.objectContaining({
        preferences: { 'olivares.tenant': expect.any(String) },
      }),
      expect.objectContaining({ tenant: 'a', sessionEffects: 'none' }),
    ),
  )
  first.unmount()
  useTenantStore.getState().clear()
  const second = mount()
  await waitFor(() => expect(second.result.current.activeTenant).toBe('b'))
  second.unmount()
})
it.each(['outside-grants', '{broken'])(
  'rejects a stale or malformed saved organization: %s',
  async (choice) => {
    session()
    vi.spyOn(authApi, 'whoami').mockResolvedValue(principal)
    vi.spyOn(savedViewsApi, 'uiState').mockResolvedValue({
      stored: true,
      preferences: {
        'olivares.tenant':
          choice === '{broken'
            ? choice
            : JSON.stringify({ state: { activeTenant: choice } }),
      },
    })
    const view = mount()
    await waitFor(() => expect(view.result.current.activeTenant).toBe('a'))
    view.unmount()
  },
)
it('restores a grant-less superadmin choice from a stable organization row', async () => {
  session()
  vi.spyOn(authApi, 'whoami').mockResolvedValue({
    ...principal,
    grants: [],
    superadmin: true,
  })
  vi.spyOn(systemApi, 'listOrgs').mockResolvedValue({
    items: [
      { tenant_id: '019a0000-0000-7000-8000-000000000002' },
      { tenant_id: '019a0000-0000-7000-8000-000000000001' },
    ],
  } as Awaited<ReturnType<typeof systemApi.listOrgs>>)
  const read = vi.spyOn(savedViewsApi, 'uiState').mockResolvedValue({
    stored: true,
    preferences: {
      'olivares.tenant': JSON.stringify({
        state: { activeTenant: '019a0000-0000-7000-8000-000000000002' },
        version: 0,
      }),
    },
  })
  const view = mount()
  await waitFor(() =>
    expect(view.result.current.activeTenant).toBe(
      '019a0000-0000-7000-8000-000000000002',
    ),
  )
  expect(read).toHaveBeenCalledWith(
    expect.objectContaining({ tenant: '019a0000-0000-7000-8000-000000000001' }),
  )
  view.unmount()
})

it('restores the organization before session adoption returns', async () => {
  vi.spyOn(authApi, 'whoami').mockResolvedValue(principal)
  vi.spyOn(savedViewsApi, 'uiState').mockResolvedValue({
    stored: true,
    preferences: {
      'olivares.tenant': JSON.stringify({ state: { activeTenant: 'b' } }),
    },
  })
  const view = mount()
  await act(async () => {
    await view.result.current.adoptSession({
      csrf_token: 'synthetic',
      session_id: 'synthetic',
      expires_at: '',
    })
  })
  expect(view.result.current.activeTenant).toBe('b')
  view.unmount()
})

it('does not apply a delayed preference to a replacement session', async () => {
  vi.spyOn(authApi, 'whoami').mockResolvedValue(principal)
  let release!: (
    state: Awaited<ReturnType<typeof savedViewsApi.uiState>>,
  ) => void
  vi.spyOn(savedViewsApi, 'uiState').mockImplementation(
    () =>
      new Promise((resolve) => {
        release = resolve
      }),
  )
  const view = mount()
  let adoption!: Promise<void>
  act(() => {
    adoption = view.result.current.adoptSession({
      csrf_token: 'old',
      session_id: 'old',
      expires_at: '',
    })
  })
  await waitFor(() => expect(release).toBeTypeOf('function'))
  await act(async () => {
    useSessionStore.getState().setSession({
      csrfToken: 'replacement',
      sessionId: 'replacement',
      expiresAt: '',
    })
    release({
      stored: true,
      preferences: {
        'olivares.tenant': JSON.stringify({ state: { activeTenant: 'b' } }),
      },
    })
    await adoption
  })
  expect(useTenantStore.getState().activeTenant).toBeNull()
  view.unmount()
})

it('loads sign-in settings from a restored superadmin organization outside its grants', async () => {
  vi.spyOn(authApi, 'whoami').mockResolvedValue({
    ...principal,
    superadmin: true,
    grants: [principal.grants[0]!],
  })
  const read = vi.spyOn(savedViewsApi, 'uiState').mockResolvedValue({
    stored: true,
    preferences: {
      'olivares.tenant': JSON.stringify({
        state: { activeTenant: '019a0000-0000-7000-8000-000000000002' },
      }),
    },
  })
  const view = mount()
  await act(async () => {
    await view.result.current.adoptSession({
      csrf_token: 'synthetic',
      session_id: 'synthetic',
      expires_at: '',
    })
  })
  expect(view.result.current.activeTenant).toBe(
    '019a0000-0000-7000-8000-000000000002',
  )
  expect(read).toHaveBeenLastCalledWith(
    expect.objectContaining({ tenant: '019a0000-0000-7000-8000-000000000002' }),
  )
  view.unmount()
})

it('keeps organization selection writable after session refresh', async () => {
  vi.useFakeTimers({ shouldAdvanceTime: true })
  try {
    useSessionStore.getState().setSession({
      csrfToken: 'original',
      sessionId: 'original',
      expiresAt: new Date(Date.now() + 90000).toISOString(),
    })
    vi.spyOn(authApi, 'whoami').mockResolvedValue(principal)
    vi.spyOn(authApi, 'refresh').mockResolvedValue({
      csrf_token: 'renewed',
      session_id: 'renewed',
      expires_at: new Date(Date.now() + 600000).toISOString(),
    })
    const read = vi
      .spyOn(savedViewsApi, 'uiState')
      .mockResolvedValue({ stored: false })
    const save = vi
      .spyOn(savedViewsApi, 'saveUiState')
      .mockResolvedValue({ stored: true })
    const view = mount()
    await waitFor(() => expect(view.result.current.activeTenant).toBe('a'))
    act(() => view.result.current.setActiveTenant('b'))
    await waitFor(() => expect(save).toHaveBeenCalled())
    await act(async () => {
      await vi.advanceTimersByTimeAsync(35000)
      await clients[0]!.refetchQueries({ queryKey: queryKeys.whoami })
    })
    expect(view.result.current.activeTenant).toBe('b')
    expect(read).toHaveBeenCalledTimes(1)
    expect(useSessionStore.getState().csrfToken).toBe('renewed')
    act(() => view.result.current.setActiveTenant('b'))
    await waitFor(() => expect(save).toHaveBeenCalled())
    expect(view.result.current.activeTenant).toBe('b')
    view.unmount()
  } finally {
    vi.useRealTimers()
  }
})

it.each(['failed read', 'another tab', 'pending save'])(
  'keeps the live organization on whoami refetch: %s',
  async (scenario) => {
    session()
    vi.spyOn(authApi, 'whoami').mockResolvedValue(principal)
    const read = vi
      .spyOn(savedViewsApi, 'uiState')
      .mockResolvedValue({ stored: false })
    let finishSave!: () => void
    const save = vi
      .spyOn(savedViewsApi, 'saveUiState')
      .mockImplementation(async () => {
        if (scenario === 'pending save')
          await new Promise<void>((resolve) => {
            finishSave = resolve
          })
        return { stored: true }
      })
    const view = mount()
    await waitFor(() => expect(view.result.current.activeTenant).toBe('a'))
    act(() => view.result.current.setActiveTenant('b'))
    await waitFor(() => expect(save).toHaveBeenCalled())
    if (scenario === 'failed read') read.mockRejectedValue(new Error('offline'))
    else
      read.mockResolvedValue({
        stored: true,
        preferences: {
          'olivares.tenant': JSON.stringify({ state: { activeTenant: 'a' } }),
        },
      })
    try {
      await act(async () => {
        await clients[0]!.refetchQueries({ queryKey: queryKeys.whoami })
      })
      expect(view.result.current.activeTenant).toBe('b')
      expect(read).toHaveBeenCalledTimes(1)
    } finally {
      finishSave?.()
      view.unmount()
    }
  },
)

it('keeps a permitted live choice if the initial preference read fails', async () => {
  session()
  useTenantStore.getState().setActiveTenant('b')
  vi.spyOn(authApi, 'whoami').mockResolvedValue(principal)
  vi.spyOn(savedViewsApi, 'uiState').mockRejectedValue(new Error('offline'))
  const view = mount()
  await waitFor(() => expect(view.result.current.status).toBe('authenticated'))
  expect(view.result.current.activeTenant).toBe('b')
})

it('drops a live organization when whoami revokes its grant', async () => {
  session()
  const whoami = vi.spyOn(authApi, 'whoami').mockResolvedValue(principal)
  vi.spyOn(savedViewsApi, 'uiState').mockResolvedValue({ stored: false })
  vi.spyOn(savedViewsApi, 'saveUiState').mockResolvedValue({ stored: true })
  const view = mount()
  await waitFor(() => expect(view.result.current.activeTenant).toBe('a'))
  act(() => view.result.current.setActiveTenant('b'))
  whoami.mockResolvedValue({ ...principal, grants: [principal.grants[0]!] })
  await act(async () => {
    await clients[0]!.refetchQueries({ queryKey: queryKeys.whoami })
  })
  await waitFor(() => expect(view.result.current.activeTenant).toBe('a'))
})

it('rejects a superadmin preference that cannot be a tenant header', async () => {
  session()
  vi.spyOn(authApi, 'whoami').mockResolvedValue({
    ...principal,
    superadmin: true,
  })
  vi.spyOn(savedViewsApi, 'uiState').mockResolvedValue({
    stored: true,
    preferences: {
      'olivares.tenant': JSON.stringify({
        state: { activeTenant: 'bad\r\nheader' },
      }),
    },
  })
  const view = mount()
  await waitFor(() => expect(view.result.current.status).toBe('authenticated'))
  expect(view.result.current.activeTenant).toBe('a')
})

it('keeps the live organization across a client renewal and whoami refetch', async () => {
  session()
  vi.spyOn(authApi, 'whoami').mockResolvedValue(principal)
  const read = vi
    .spyOn(savedViewsApi, 'uiState')
    .mockResolvedValue({ stored: false })
  const save = vi
    .spyOn(savedViewsApi, 'saveUiState')
    .mockResolvedValue({ stored: true })
  const view = mount()
  await waitFor(() => expect(view.result.current.activeTenant).toBe('a'))
  act(() => view.result.current.setActiveTenant('b'))
  await waitFor(() => expect(save).toHaveBeenCalled())
  read.mockResolvedValue({
    stored: true,
    preferences: {
      'olivares.tenant': JSON.stringify({ state: { activeTenant: 'a' } }),
    },
  })
  await act(async () => {
    // The API client's renewal (providers.tsx refreshSession) installs the new credential.
    useSessionStore.getState().setSession({
      csrfToken: 'renewed-by-client',
      sessionId: 'renewed-by-client',
      expiresAt: '',
    })
    await clients[0]!.refetchQueries({ queryKey: queryKeys.whoami })
  })
  expect(view.result.current.activeTenant).toBe('b')
  expect(read).toHaveBeenCalledTimes(1)
  view.unmount()
})

it('restores the saved organization when another principal signs in', async () => {
  session()
  const whoami = vi.spyOn(authApi, 'whoami').mockResolvedValue(principal)
  const read = vi
    .spyOn(savedViewsApi, 'uiState')
    .mockResolvedValue({ stored: false })
  vi.spyOn(savedViewsApi, 'saveUiState').mockResolvedValue({ stored: true })
  const view = mount()
  await waitFor(() => expect(view.result.current.activeTenant).toBe('a'))
  act(() => view.result.current.setActiveTenant('b'))
  whoami.mockResolvedValue({ ...principal, user_id: 'another' })
  read.mockResolvedValue({
    stored: true,
    preferences: {
      'olivares.tenant': JSON.stringify({ state: { activeTenant: 'a' } }),
    },
  })
  await act(async () => {
    useSessionStore.getState().setSession({
      csrfToken: 'another',
      sessionId: 'another',
      expiresAt: '',
    })
    await clients[0]!.refetchQueries({ queryKey: queryKeys.whoami })
  })
  expect(view.result.current.activeTenant).toBe('a')
  expect(read).toHaveBeenCalledTimes(2)
  view.unmount()
})

it('keeps a choice made while the restore read is in flight', async () => {
  session()
  const whoami = vi.spyOn(authApi, 'whoami').mockResolvedValue(principal)
  const read = vi
    .spyOn(savedViewsApi, 'uiState')
    .mockResolvedValue({ stored: false })
  vi.spyOn(savedViewsApi, 'saveUiState').mockResolvedValue({ stored: true })
  const view = mount()
  await waitFor(() => expect(view.result.current.activeTenant).toBe('a'))
  whoami.mockResolvedValue({ ...principal, user_id: 'another' })
  let release!: (
    state: Awaited<ReturnType<typeof savedViewsApi.uiState>>,
  ) => void
  read.mockImplementation(
    () =>
      new Promise((resolve) => {
        release = resolve
      }),
  )
  let refetch!: Promise<unknown>
  act(() => {
    useSessionStore.getState().setSession({
      csrfToken: 'another',
      sessionId: 'another',
      expiresAt: '',
    })
    refetch = clients[0]!.refetchQueries({ queryKey: queryKeys.whoami })
  })
  await waitFor(() => expect(read).toHaveBeenCalledTimes(2))
  act(() => view.result.current.setActiveTenant('b'))
  await act(async () => {
    release({
      stored: true,
      preferences: {
        'olivares.tenant': JSON.stringify({ state: { activeTenant: 'a' } }),
      },
    })
    await refetch
  })
  expect(view.result.current.activeTenant).toBe('b')
  view.unmount()
})
