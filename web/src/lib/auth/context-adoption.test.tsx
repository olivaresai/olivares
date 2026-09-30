// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import {
  act,
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { useEffect } from 'react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { configureApiClient } from '@/lib/api/client'
import {
  AuthProvider,
  useAuth,
  type AuthContextValue,
} from '@/lib/auth/context'
import { queryKeys } from '@/lib/api/query'
import type { LoginResponse, Whoami } from '@/lib/api/types'
import { useSessionStore } from '@/stores/session'
import { useTenantStore } from '@/stores/tenant'

const expires = () => new Date(Date.now() + 3_600_000).toISOString()
const sessionA = (): LoginResponse => ({
  token: 'olvs_witness_A',
  session_id: 'witness-session-A',
  expires_at: expires(),
})
const sessionB = () => ({
  token: 'olvs_witness_B',
  sessionId: 'witness-session-B',
  expiresAt: expires(),
})
const identity = (name: 'A' | 'B'): Whoami => ({
  kind: 'user',
  user_id: `witness-user-${name}`,
  actor: `witness-user-${name}`,
  display_name: `Synthetic ${name}`,
  superadmin: false,
  grants: [],
})
let client: QueryClient
let auth: AuthContextValue
let adoption: Promise<void>
let releaseA: (value: Response) => void
const unauthorized = vi.fn(() => useSessionStore.getState().clear())
let order: string[]
let calls: { path: string; method: string; owner: 'A' | 'B' | 'none' }[]
const owner = () =>
  useSessionStore.getState().token === 'olvs_witness_A'
    ? 'A'
    : useSessionStore.getState().token === 'olvs_witness_B'
      ? 'B'
      : 'none'
function Probe() {
  const currentAuth = useAuth()
  useEffect(() => {
    auth = currentAuth
  }, [currentAuth])
  return (
    <>
      <output data-testid="principal">
        {currentAuth.principal?.user_id ?? 'anonymous'}
      </output>
      <button
        onClick={() => {
          order.push('adopt-A-start')
          adoption = currentAuth.adoptSession(sessionA())
        }}
      >
        Adopt A
      </button>
    </>
  )
}
beforeEach(() => {
  useSessionStore.getState().clear()
  useTenantStore.getState().clear()
  order = []
  calls = []
  unauthorized.mockClear()
  client = new QueryClient({
    defaultOptions: {
      queries: { retry: false, refetchOnWindowFocus: false },
      mutations: { retry: false },
    },
  })
  configureApiClient({
    getToken: () => useSessionStore.getState().token,
    getTenant: () => useTenantStore.getState().activeTenant,
    getExpiresAt: () => useSessionStore.getState().expiresAt,
    onUnauthorized: unauthorized,
    refreshSession: undefined,
  })
  vi.spyOn(globalThis, 'fetch').mockImplementation((url, options) => {
    const path = String(url)
    const bearer = new Headers(options?.headers).get('Authorization')
    const requestOwner =
      bearer === 'Bearer olvs_witness_A'
        ? 'A'
        : bearer === 'Bearer olvs_witness_B'
          ? 'B'
          : 'none'
    calls.push({ path, method: options?.method ?? 'GET', owner: requestOwner })
    if (path === '/v1/auth/whoami' && requestOwner === 'A') {
      order.push('A-whoami-dispatched-and-held')
      return new Promise((done) => {
        releaseA = done
      })
    }
    if (path === '/v1/auth/logout') {
      order.push('native-logout-dispatched')
      return Promise.resolve(new Response(null, { status: 204 }))
    }
    throw new Error(`Unexpected synthetic request: ${path}`)
  })
})
afterEach(() => {
  cleanup()
  client.clear()
  useSessionStore.getState().clear()
  useTenantStore.getState().clear()
  vi.restoreAllMocks()
})
async function startA() {
  render(
    <QueryClientProvider client={client}>
      <AuthProvider>
        <Probe />
      </AuthProvider>
    </QueryClientProvider>,
  )
  expect(screen.getByTestId('principal').textContent).toBe('anonymous')
  expect(calls).toEqual([])
  fireEvent.click(screen.getByRole('button', { name: 'Adopt A' }))
  await waitFor(() =>
    expect(calls).toEqual([
      { path: '/v1/auth/whoami', method: 'GET', owner: 'A' },
    ]),
  )
  expect(owner()).toBe('A')
  order.push('A-session-installed-before-response')
}
describe('session adoption credential ownership', () => {
  it('publishes its current principal with exactly one whoami request', async () => {
    await startA()
    await act(async () => {
      releaseA(new Response(JSON.stringify(identity('A'))))
      await adoption
    })
    expect(owner()).toBe('A')
    expect(client.getQueryData(queryKeys.whoami)).toEqual(identity('A'))
    expect(screen.getByTestId('principal')).toHaveTextContent('witness-user-A')
    expect(calls).toHaveLength(1)
  })

  it('preserves B principal when the earlier A adoption response settles', async () => {
    await startA()
    act(() => {
      useSessionStore.getState().setSession(sessionB())
      client.setQueryData(queryKeys.whoami, identity('B'))
    })
    await waitFor(() =>
      expect(screen.getByTestId('principal').textContent).toBe(
        'witness-user-B',
      ),
    )
    expect(owner()).toBe('B')
    expect(client.getQueryData<Whoami>(queryKeys.whoami)?.user_id).toBe(
      'witness-user-B',
    )
    order.push('B-session-and-principal-published')
    await act(async () => {
      order.push('A-response-released')
      releaseA(new Response(JSON.stringify(identity('A'))))
      await adoption
      order.push('A-adoption-settled')
    })
    expect(calls).toHaveLength(1)
    expect(owner()).toBe('B')
    expect(client.getQueryData<Whoami>(queryKeys.whoami)?.user_id).toBe(
      'witness-user-B',
    )
    expect(screen.getByTestId('principal').textContent).toBe('witness-user-B')
  })
  it('keeps the identity cache empty after native logout while A adoption is pending', async () => {
    await startA()
    await act(async () => {
      await auth.logout()
    })
    expect(owner()).toBe('none')
    expect(client.getQueryData(queryKeys.whoami)).toBeUndefined()
    order.push('native-logout-cleared-session-and-cache')
    await act(async () => {
      order.push('A-response-released')
      releaseA(new Response(JSON.stringify(identity('A'))))
      await adoption
      order.push('A-adoption-settled')
    })
    expect(calls).toEqual([
      { path: '/v1/auth/whoami', method: 'GET', owner: 'A' },
      { path: '/v1/auth/logout', method: 'POST', owner: 'A' },
    ])
    expect(owner()).toBe('none')
    expect(client.getQueryData(queryKeys.whoami)).toBeUndefined()
  })

  it('ignores an old response when the same credential is reinstalled after B', async () => {
    await startA()
    const fresh = { ...identity('A'), display_name: 'Fresh A' }
    act(() => {
      useSessionStore.getState().setSession(sessionB())
      const a = sessionA()
      useSessionStore.getState().setSession({
        token: a.token,
        sessionId: a.session_id,
        expiresAt: a.expires_at,
      })
      client.setQueryData(queryKeys.whoami, fresh)
    })
    await act(async () => {
      releaseA(new Response(JSON.stringify(identity('A'))))
      await adoption
    })
    expect(owner()).toBe('A')
    expect(client.getQueryData(queryKeys.whoami)).toEqual(fresh)
    expect(calls).toHaveLength(1)
  })

  it('does not clear B when the retired A request answers 401', async () => {
    await startA()
    act(() => {
      useSessionStore.getState().setSession(sessionB())
      client.setQueryData(queryKeys.whoami, identity('B'))
    })
    await act(async () => {
      releaseA(
        new Response(
          JSON.stringify({
            error: { code: 'unauthenticated', message: 'session ended' },
          }),
          { status: 401 },
        ),
      )
      await adoption.catch(() => undefined)
    })
    expect(owner()).toBe('B')
    expect(unauthorized).not.toHaveBeenCalled()
    expect(client.getQueryData(queryKeys.whoami)).toEqual(identity('B'))
    expect(calls).toHaveLength(1)
  })

  it('keeps the configured unauthorized hook for a current 401', async () => {
    await startA()
    await act(async () => {
      const rejected = expect(adoption).rejects.toMatchObject({ status: 401 })
      releaseA(
        new Response(
          JSON.stringify({
            error: { code: 'unauthenticated', message: 'session ended' },
          }),
          { status: 401 },
        ),
      )
      await rejected
    })
    expect(owner()).toBe('none')
    expect(unauthorized).toHaveBeenCalledTimes(1)
    expect(client.getQueryData(queryKeys.whoami)).toBeUndefined()
    expect(calls).toHaveLength(1)
  })

  it('does not release the newer adoption observer while B whoami is pending', async () => {
    await startA()
    let releaseB!: (response: Response) => void
    const bResponse = new Promise<Response>((resolve) => {
      releaseB = resolve
    })
    vi.mocked(globalThis.fetch).mockImplementationOnce((_url, opts) => {
      expect(new Headers(opts?.headers).get('Authorization')).toBe(
        'Bearer olvs_witness_B',
      )
      calls.push({ path: '/v1/auth/whoami', method: 'GET', owner: 'B' })
      return bResponse
    })
    let second!: Promise<void>
    act(() => {
      const b = sessionB()
      second = auth.adoptSession({
        token: b.token,
        session_id: b.sessionId,
        expires_at: b.expiresAt,
      })
    })
    try {
      await waitFor(() => expect(calls).toHaveLength(2))
      await act(async () => {
        releaseA(new Response(JSON.stringify(identity('A'))))
        await adoption
      })
      expect(calls).toHaveLength(2)
      expect(client.getQueryData(queryKeys.whoami)).toBeUndefined()
    } finally {
      await act(async () => {
        releaseB(new Response(JSON.stringify(identity('B'))))
        await second
      })
    }
    expect(owner()).toBe('B')
    expect(client.getQueryData(queryKeys.whoami)).toEqual(identity('B'))
    expect(calls).toHaveLength(2)
  })
})
