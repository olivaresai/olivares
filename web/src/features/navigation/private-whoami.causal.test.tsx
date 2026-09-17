// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
// Real HTTP client/endpoints, AuthProvider, QueryClient, stores and router. Only
// fetch is controlled locally. It deliberately permits late delivery after abort
// to exercise both cancellation and the client's response/body suspension windows.
import { useLayoutEffect } from 'react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import {
  createMemoryHistory,
  createRootRoute,
  createRouter,
  RouterProvider,
} from '@tanstack/react-router'
import { act, cleanup, render, screen, waitFor } from '@testing-library/react'
import {
  afterEach,
  beforeEach,
  expect,
  it,
  vi,
  type MockInstance,
} from 'vitest'
import { authApi } from '@/lib/api/endpoints'
import { __resetRefreshState, configureApiClient } from '@/lib/api/client'
import { ApiError, NetworkError } from '@/lib/api/errors'
import { queryKeys } from '@/lib/api/query'
import type { Whoami } from '@/lib/api/types'
import { AuthProvider, useAuth } from '@/lib/auth/context'
import { useSessionStore } from '@/stores/session'
import { useTenantStore } from '@/stores/tenant'
import { useWorkspaceStore } from '@/stores/workspace'
import {
  PersonalNavigationProvider,
  usePersonalNavigation,
} from './personal-navigation'
import { favoriteStorageKey, personalLink } from './personal-navigation-store'

const A: Whoami = {
  kind: 'user',
  user_id: 'n3-f1-a',
  display_name: 'Local identity fixture',
  actor: 'user:n3-f1-a',
  superadmin: false,
  grants: [
    { tenant: 'tenant-a', role: 'viewer', permissions: [] },
    { tenant: 'tenant-b', role: 'viewer', permissions: [] },
  ],
}
const B: Whoami = { ...A, user_id: 'n3-f1-b', actor: 'user:n3-f1-b' }
const home = personalLink('home')!
interface Flight {
  path: string
  headers: Headers
  signal: AbortSignal | null | undefined
  response: (response: Response) => void
  fail: (error: Error) => void
}
let flights: Flight[]
let refreshSucceeds: boolean
let client: QueryClient
let latest: NonNullable<ReturnType<typeof usePersonalNavigation>>
let whoami: MockInstance<typeof authApi.whoami>
const unauthorized = vi.fn(() => {
  useSessionStore.getState().clear()
  useWorkspaceStore.getState().clear()
})
const refresh = vi.fn(async () => {
  try {
    const result = await authApi.refresh()
    useSessionStore.getState().setSession({
      token: result.token,
      sessionId: result.session_id,
      expiresAt: result.expires_at,
    })
    return true
  } catch {
    return false
  }
})
function json(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json', 'X-Request-ID': 'local-f1' },
  })
}
function Probe() {
  const personal = usePersonalNavigation()!
  const auth = useAuth()
  useLayoutEffect(() => {
    latest = personal
  }, [personal])
  return (
    <output
      data-testid="personal"
      data-ready={personal.available}
      data-owner={auth.principal?.user_id}
    >
      {personal.favorites.map((link) => link.id).join(',')}
    </output>
  )
}
function Shell() {
  const auth = useAuth()
  if (auth.status !== 'authenticated') return <p>Fixture signed out</p>
  return (
    <PersonalNavigationProvider>
      <Probe />
    </PersonalNavigationProvider>
  )
}
async function mount() {
  // A fresh shared Whoami cache isolates the PRIVATE read without replacing Auth's query.
  client.setQueryData(queryKeys.whoami, A)
  const root = createRootRoute({
    component: () => (
      <AuthProvider>
        <Shell />
      </AuthProvider>
    ),
  })
  const router = createRouter({
    routeTree: root,
    history: createMemoryHistory({ initialEntries: ['/'] }),
  })
  const result = render(
    <QueryClientProvider client={client}>
      <RouterProvider router={router} />
    </QueryClientProvider>,
  )
  await waitFor(() =>
    expect(flights.filter((f) => f.path === '/v1/auth/whoami')).toHaveLength(1),
  )
  return result
}
function promiseAt(index: number) {
  // A pass-through spy captures the real endpoint promise, never supplies an answer.
  return whoami.mock.results[index].value as Promise<Whoami>
}
async function settle(index: number, response: Response | Error) {
  const promise = promiseAt(index)
  let outcome: unknown
  await act(async () => {
    if (response instanceof Error) flights[index].fail(response)
    else flights[index].response(response)
    outcome = await promise.catch((error: unknown) => error)
  })
  return outcome
}
function noGlobalEffects() {
  expect(refresh).not.toHaveBeenCalled()
  expect(unauthorized).not.toHaveBeenCalled()
  expect(flights.every((flight) => flight.path === '/v1/auth/whoami')).toBe(
    true,
  )
}
beforeEach(() => {
  flights = []
  refreshSucceeds = false
  unauthorized.mockClear()
  refresh.mockClear()
  __resetRefreshState()
  localStorage.clear()
  useSessionStore.getState().setSession({
    token: 'n3-f1-defensive-a',
    sessionId: 'f1-a',
    expiresAt: '',
  })
  useTenantStore.setState({ activeTenant: 'tenant-a' })
  useWorkspaceStore.setState({ activeWorkspace: null })
  client = new QueryClient({
    defaultOptions: { queries: { retry: false, gcTime: 0 } },
  })
  configureApiClient({
    getToken: () => useSessionStore.getState().token,
    getTenant: () => useTenantStore.getState().activeTenant,
    getExpiresAt: () => useSessionStore.getState().expiresAt,
    onUnauthorized: unauthorized,
    refreshSession: refresh,
  })
  whoami = vi.spyOn(authApi, 'whoami')
  vi.stubGlobal(
    'fetch',
    vi.fn(
      (url: string, init?: RequestInit) =>
        new Promise<Response>((resolve, reject) => {
          const flight = {
            path: String(url),
            headers: new Headers(init?.headers),
            signal: init?.signal,
            response: resolve,
            fail: reject,
          }
          flights.push(flight)
          if (flight.path === '/v1/auth/refresh')
            resolve(
              refreshSucceeds
                ? json({
                    token: 'n3-f1-defensive-refreshed',
                    session_id: 'f1-renewed',
                    expires_at: '',
                  })
                : json(
                    {
                      error: {
                        code: 'unavailable',
                        message: 'Local refresh failure',
                      },
                    },
                    503,
                  ),
            )
          else if (
            flight.headers.get('Authorization') ===
            'Bearer n3-f1-defensive-refreshed'
          )
            resolve(json(A))
        }),
    ),
  )
})
afterEach(() => {
  cleanup()
  client.clear()
  configureApiClient({
    getToken: () => null,
    getTenant: () => null,
    getExpiresAt: undefined,
    refreshSession: undefined,
    onUnauthorized: () => {},
  })
  __resetRefreshState()
  vi.restoreAllMocks()
  vi.unstubAllGlobals()
})

it('late 401 from private A cannot refresh, replay or log out established B, or access the retired partition', async () => {
  const keyA = favoriteStorageKey(window.location.origin, A, 'tenant-a')!
  localStorage.setItem(keyA, JSON.stringify({ version: 1, favorites: [home] }))
  const reads = vi.spyOn(Storage.prototype, 'getItem')
  const writes = vi.spyOn(Storage.prototype, 'setItem')
  await mount()
  const old = latest
  const first = flights[0]
  expect(first.headers.get('Authorization')).toBe('Bearer n3-f1-defensive-a')
  expect(first.headers.get('X-Olivares-Tenant')).toBe('tenant-a')
  let abortedBeforeCommit = false
  act(() => {
    useSessionStore.getState().setSession({
      token: 'n3-f1-defensive-b',
      sessionId: 'f1-b',
      expiresAt: '',
    })
    useTenantStore.getState().setActiveTenant('tenant-b')
    client.setQueryData(queryKeys.whoami, B)
    abortedBeforeCommit = first.signal?.aborted === true
    expect(screen.getByTestId('personal')).toHaveAttribute(
      'data-owner',
      A.user_id!,
    )
  })
  await waitFor(() => expect(flights).toHaveLength(2))
  expect(flights[1].headers.get('Authorization')).toBe(
    'Bearer n3-f1-defensive-b',
  )
  expect(flights[1].headers.get('X-Olivares-Tenant')).toBe('tenant-b')
  await settle(1, json(B))
  await waitFor(() => expect(latest.available).toBe(true))
  const result = await settle(
    0,
    json({ error: { code: 'unauthenticated', message: 'Late A' } }, 401),
  )
  noGlobalEffects()
  expect(abortedBeforeCommit).toBe(true)
  expect(result).toMatchObject({ name: 'AbortError' })
  expect(flights).toHaveLength(2)
  expect(useSessionStore.getState().token).toBe('n3-f1-defensive-b')
  expect(client.getQueryData(queryKeys.whoami)).toEqual(B)
  act(() => {
    old.setFavorite(home, true)
    old.clearFavorites()
  })
  expect(reads.mock.calls.some(([key]) => key === keyA)).toBe(false)
  expect(writes.mock.calls.some(([key]) => key === keyA)).toBe(false)
  expect(latest.favorites).toEqual([])
})

it('a current private 200 verifies preferences with real auth/tenant headers and no preventive renewal near expiry', async () => {
  useSessionStore.getState().setSession({
    token: 'n3-f1-defensive-a',
    sessionId: 'f1-a',
    expiresAt: new Date(Date.now() + 90_000).toISOString(),
  })
  const key = favoriteStorageKey(window.location.origin, A, 'tenant-a')!
  localStorage.setItem(key, JSON.stringify({ version: 1, favorites: [home] }))
  await mount()
  expect(latest.available).toBe(false)
  noGlobalEffects()
  expect(flights[0].headers.get('Authorization')).toBe(
    'Bearer n3-f1-defensive-a',
  )
  expect(flights[0].headers.get('X-Olivares-Tenant')).toBe('tenant-a')
  expect(flights[0].signal?.aborted).toBe(false)
  expect(await settle(0, json(A))).toEqual(A)
  await waitFor(() => expect(latest.favorites).toEqual([home]))
  expect(latest.available).toBe(true)
  expect(client.getQueryData(queryKeys.whoami)).toEqual(A)
  noGlobalEffects()
  expect(flights).toHaveLength(1)
})

it.each(['401', '503', 'network'] as const)(
  'current private %s only quarantines preferences and preserves the ordinary error',
  async (failure) => {
    const key = favoriteStorageKey(window.location.origin, A, 'tenant-a')!
    const reads = vi.spyOn(Storage.prototype, 'getItem')
    await mount()
    const result = await settle(
      0,
      failure === 'network'
        ? new TypeError('Local transport failure')
        : json(
            { error: { code: 'fixture_failure', message: 'Local rejection' } },
            Number(failure),
          ),
    )
    if (failure === 'network') expect(result).toBeInstanceOf(NetworkError)
    else {
      expect(result).toBeInstanceOf(ApiError)
      expect(result).toMatchObject({
        status: Number(failure),
        code: 'fixture_failure',
        requestId: 'local-f1',
      })
    }
    expect(latest.available).toBe(false)
    expect(reads.mock.calls.some(([stored]) => stored === key)).toBe(false)
    expect(screen.getByTestId('personal')).toHaveAttribute(
      'data-owner',
      A.user_id!,
    )
    expect(useSessionStore.getState().token).toBe('n3-f1-defensive-a')
    expect(client.getQueryState(queryKeys.whoami)?.status).toBe('success')
    expect(client.getQueryData(queryKeys.whoami)).toEqual(A)
    expect(flights).toHaveLength(1)
    noGlobalEffects()
  },
)

it.each([200, 401])(
  'retirement during the HTTP %s body await aborts before React cleanup and cannot publish or handle auth',
  async (status) => {
    const key = favoriteStorageKey(window.location.origin, A, 'tenant-a')!
    const reads = vi.spyOn(Storage.prototype, 'getItem')
    await mount()
    let body!: ReadableStreamDefaultController<Uint8Array>
    const response = new Response(
      new ReadableStream<Uint8Array>({
        start(controller) {
          body = controller
        },
      }),
      { status },
    )
    const text = vi.spyOn(response, 'text')
    await act(async () => flights[0].response(response))
    await waitFor(() => expect(text).toHaveBeenCalledOnce())
    act(() => {
      useTenantStore.getState().setActiveTenant('tenant-b')
      // Same identity/credential: tenant movement alone retires transport immediately.
      expect(flights[0].signal?.aborted).toBe(true)
      expect(screen.getByTestId('personal')).toHaveAttribute(
        'data-owner',
        A.user_id!,
      )
    })
    await waitFor(() => expect(flights).toHaveLength(2))
    await settle(1, json(A))
    let retired: unknown
    await act(async () => {
      body.enqueue(
        new TextEncoder().encode(
          JSON.stringify(
            status === 200
              ? A
              : { error: { code: 'unauthenticated', message: 'Retired body' } },
          ),
        ),
      )
      body.close()
      retired = await promiseAt(0).catch((error: unknown) => error)
    })
    expect(retired).toMatchObject({ name: 'AbortError' })
    expect(reads.mock.calls.some(([stored]) => stored === key)).toBe(false)
    expect(latest.available).toBe(true)
    expect(flights).toHaveLength(2)
    noGlobalEffects()
  },
)

it.each(['principal', 'unmount'] as const)(
  'retires private transport on %s without waiting for a late response',
  async (movement) => {
    const mounted = await mount()
    const first = flights[0]
    act(() => {
      if (movement === 'unmount') mounted.unmount()
      else {
        client.setQueryData(queryKeys.whoami, B)
        expect(first.signal?.aborted).toBe(true)
        client.setQueryData(queryKeys.whoami, A)
      }
      expect(first.signal?.aborted).toBe(true)
    })
    expect(await settle(0, json(A))).toMatchObject({ name: 'AbortError' })
    if (movement === 'principal') {
      await waitFor(() => expect(flights).toHaveLength(2))
      expect(latest.available).toBe(false)
      await settle(1, json(A))
      await waitFor(() => expect(latest.available).toBe(true))
    }
    noGlobalEffects()
  },
)

it('a successful verification queued before a batched tenant round trip cannot open the retired partition', async () => {
  const key = favoriteStorageKey(window.location.origin, A, 'tenant-a')!
  localStorage.setItem(key, JSON.stringify({ version: 1, favorites: [home] }))
  const reads = vi.spyOn(Storage.prototype, 'getItem')
  await mount()
  // The hook registered its await first: it queues setVerified before this callback.
  // React has not committed it when the synchronous source subscribers retire the call.
  const queued = promiseAt(0).then(() => {
    useTenantStore.getState().setActiveTenant('tenant-b')
    useTenantStore.getState().setActiveTenant('tenant-a')
    expect(flights[0].signal?.aborted).toBe(true)
  })
  await act(async () => {
    flights[0].response(json(A))
    await queued
  })
  await waitFor(() => expect(flights).toHaveLength(2))
  expect(latest.available).toBe(false)
  expect(reads.mock.calls.some(([stored]) => stored === key)).toBe(false)
  await settle(1, json(A))
  await waitFor(() => expect(latest.favorites).toEqual([home]))
  noGlobalEffects()
})

it('ordinary Whoami retains preventive renewal and the default 401 refresh/replay path', async () => {
  refreshSucceeds = true
  useSessionStore.getState().setSession({
    token: 'n3-f1-defensive-a',
    sessionId: 'f1-a',
    expiresAt: new Date(Date.now() + 90_000).toISOString(),
  })
  expect(await authApi.whoami()).toEqual(A)
  expect(flights.map((f) => f.path)).toEqual([
    '/v1/auth/refresh',
    '/v1/auth/whoami',
  ])
  expect(refresh).toHaveBeenCalledOnce()
  expect(unauthorized).not.toHaveBeenCalled()
  flights = []
  refresh.mockClear()
  useSessionStore.getState().setSession({
    token: 'n3-f1-defensive-a',
    sessionId: 'f1-a',
    expiresAt: '',
  })
  const ordinary = authApi.whoami()
  flights[0].response(
    json({ error: { code: 'unauthenticated', message: 'Ordinary 401' } }, 401),
  )
  expect(await ordinary).toEqual(A)
  expect(flights.map((f) => f.path)).toEqual([
    '/v1/auth/whoami',
    '/v1/auth/refresh',
    '/v1/auth/whoami',
  ])
  expect(refresh).toHaveBeenCalledOnce()
  expect(unauthorized).not.toHaveBeenCalled()
})

it('ordinary Whoami still logs out when its 401 cannot renew', async () => {
  const ordinary = authApi.whoami()
  flights[0].response(
    json({ error: { code: 'unauthenticated', message: 'Ordinary 401' } }, 401),
  )
  await expect(ordinary).rejects.toMatchObject({ status: 401 })
  expect(refresh).toHaveBeenCalledOnce()
  expect(unauthorized).toHaveBeenCalledOnce()
  expect(useSessionStore.getState().token).toBeNull()
  expect(flights.map((f) => f.path)).toEqual([
    '/v1/auth/whoami',
    '/v1/auth/refresh',
  ])
})
