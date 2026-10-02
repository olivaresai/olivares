// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { authApi } from '@/lib/api/endpoints'
import { configureApiClient } from '@/lib/api/client'
import { useSessionStore } from '@/stores/session'
import { restoreBrowserSession } from './browser-session'

beforeEach(() => {
  vi.useFakeTimers()
  localStorage.clear()
  useSessionStore.getState().clear()
  useSessionStore.setState({ ready: false })
  configureApiClient({
    getToken: () => null,
    getTenant: () => null,
    getExpiresAt: () => null,
    refreshSession: undefined,
    onUnauthorized: () => {},
  })
})
afterEach(() => {
  vi.useRealTimers()
  vi.unstubAllGlobals()
  useSessionStore.getState().clear()
  localStorage.clear()
})

const metadata = {
  csrf_token: 'old-csrf',
  session_id: 'old-session',
  expires_at: '2099-01-01T00:00:00Z',
}

it.each([
  'cookie headers',
  'cookie body',
  'migration headers',
  'migration body',
])('aborts stalled %s before allowing fresh sign-in', async (where) => {
  localStorage.setItem(
    'olivares.session',
    JSON.stringify({ state: { token: 'olvs_synthetic-old' } }),
  )
  let release!: () => void
  let aborted = false
  const signIns: { recoveryAborted: boolean; bearer: boolean }[] = []
  const fetcher = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
    if (String(input) !== '/v1/auth/browser-session') {
      signIns.push({
        recoveryAborted: aborted,
        bearer: new Headers(init?.headers).has('Authorization'),
      })
      return Promise.resolve(
        new Response(
          JSON.stringify({ ...metadata, session_id: 'fresh-session' }),
        ),
      )
    }
    if (init?.method === 'GET' && where.startsWith('migration'))
      return Promise.resolve(new Response(null, { status: 401 }))
    if (where.endsWith('headers'))
      return new Promise<Response>((resolve, reject) => {
        release = () => resolve(new Response(JSON.stringify(metadata)))
        init?.signal?.addEventListener(
          'abort',
          () => {
            aborted = true
            reject(new DOMException('Aborted', 'AbortError'))
          },
          { once: true },
        )
      })
    const stream = new ReadableStream<Uint8Array>({
      start(controller) {
        release = () => {
          try {
            controller.enqueue(
              new TextEncoder().encode(JSON.stringify(metadata)),
            )
            controller.close()
          } catch {
            /* An aborted response is already closed. */
          }
        }
        init?.signal?.addEventListener(
          'abort',
          () => {
            aborted = true
            controller.error(new DOMException('Aborted', 'AbortError'))
          },
          { once: true },
        )
      },
    })
    return Promise.resolve(new Response(stream))
  })
  vi.stubGlobal('fetch', fetcher)
  const recovery = restoreBrowserSession().catch(() => {})
  for (let i = 0; i < 8; i++) await Promise.resolve()
  expect(release).toBeDefined()
  const signIn = authApi.login({
    email: 'fresh@example.test',
    password: 'synthetic-password',
  })
  try {
    await vi.advanceTimersByTimeAsync(7_999)
    expect(signIns).toEqual([])
    await vi.advanceTimersByTimeAsync(1)
    expect(signIns).toEqual([{ recoveryAborted: true, bearer: false }])
    expect(useSessionStore.getState().ready).toBe(true)
    expect(localStorage.getItem('olivares.session')).not.toBeNull()
    const requests = fetcher.mock.calls.length
    await restoreBrowserSession()
    expect(fetcher).toHaveBeenCalledTimes(requests)
  } finally {
    release()
    await Promise.all([recovery, signIn])
  }
})

it('successful recovery clears its abort deadline', async () => {
  let signal: AbortSignal | null | undefined
  const fetcher = vi.fn((_input: RequestInfo | URL, init?: RequestInit) => {
    signal = init?.signal
    return Promise.resolve(new Response(JSON.stringify(metadata)))
  })
  vi.stubGlobal('fetch', fetcher)
  await restoreBrowserSession()
  await vi.advanceTimersByTimeAsync(8_000)
  expect(signal).toBeDefined()
  expect(signal?.aborted).toBe(false)
  expect(useSessionStore.getState().sessionId).toBe(metadata.session_id)
})

it.each([
  'network failure',
  'invalid JSON',
  'invalid metadata',
  'server failure',
])(
  "settles a separate caller's %s before shared sign-in recovery",
  async (where) => {
    const stored = JSON.stringify({ state: { token: 'olvs_synthetic-old' } })
    localStorage.setItem('olivares.session', stored)
    const fetcher = vi.fn()
    if (where === 'network failure')
      fetcher.mockRejectedValueOnce(new Error('fixture offline'))
    else if (where === 'invalid JSON')
      fetcher.mockResolvedValueOnce(new Response('{'))
    else if (where === 'invalid metadata')
      fetcher.mockResolvedValueOnce(new Response('{}'))
    else fetcher.mockResolvedValueOnce(new Response('{}', { status: 503 }))
    fetcher.mockResolvedValueOnce(
      new Response(
        JSON.stringify({ ...metadata, session_id: 'fresh-session' }),
      ),
    )
    vi.stubGlobal('fetch', fetcher)
    // A module's old explicit await catches the failure before shared dispatch.
    await expect(restoreBrowserSession()).rejects.toThrow()
    expect(useSessionStore.getState().ready).toBe(true)
    const response = await authApi.login({
      email: 'fresh@example.test',
      password: 'synthetic-password',
    })
    expect(response).toMatchObject({ session_id: 'fresh-session' })
    expect(fetcher.mock.calls.map(([path]) => path)).toEqual([
      '/v1/auth/browser-session',
      '/v1/auth/login',
    ])
    expect(useSessionStore.getState().ready).toBe(true)
    expect(localStorage.getItem('olivares.session')).toBe(stored)
    await restoreBrowserSession()
    expect(fetcher).toHaveBeenCalledTimes(2)
  },
)
