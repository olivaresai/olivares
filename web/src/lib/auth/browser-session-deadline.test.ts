// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { authApi } from '@/lib/api/endpoints'
import { configureApiClient } from '@/lib/api/client'
import { useSessionStore } from '@/stores/session'
import { restoreBrowserSession, retryWhileUnreachable } from './browser-session'

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

// A slow or restarting engine at page load used to look like "logged out": the 8 s recovery
// deadline expired, the console went to /login and said nothing.
it('a 9 s answer, then a 503, keeps the unreachable state and retries until the engine returns', async () => {
  let call = 0
  const fetcher = vi.fn((_input: RequestInfo | URL, init?: RequestInit) => {
    call++
    if (call === 1)
      return new Promise<Response>((resolve, reject) => {
        const answer = setTimeout(
          () => resolve(new Response(JSON.stringify(metadata))),
          9_000,
        )
        init?.signal?.addEventListener(
          'abort',
          () => {
            clearTimeout(answer)
            reject(new DOMException('Aborted', 'AbortError'))
          },
          { once: true },
        )
      })
    if (call === 2)
      return Promise.resolve(
        new Response('<html>Bad Gateway</html>', { status: 503 }),
      )
    return Promise.resolve(new Response(JSON.stringify(metadata)))
  })
  vi.stubGlobal('fetch', fetcher)
  const recovery = restoreBrowserSession().catch(() => {})
  // Not the 9 s answer: the deadline came first. No session, and the engine is the reason.
  await vi.advanceTimersByTimeAsync(8_000)
  await recovery
  expect(useSessionStore.getState()).toMatchObject({
    ready: true,
    sessionId: null,
    engineUnreachable: true,
  })
  const stop = new AbortController()
  const retrying = retryWhileUnreachable(stop.signal)
  await vi.advanceTimersByTimeAsync(1_000)
  expect(fetcher).toHaveBeenCalledTimes(2)
  expect(useSessionStore.getState().engineUnreachable).toBe(true)
  await vi.advanceTimersByTimeAsync(2_000)
  await retrying
  expect(fetcher).toHaveBeenCalledTimes(3)
  expect(useSessionStore.getState()).toMatchObject({
    sessionId: metadata.session_id,
    engineUnreachable: false,
  })
})

// An engine that always takes 9 s (a loaded host) is slow, not down: the first attempt gives
// up at 8 s, but a retry waits longer and the person gets in.
it('an engine that always answers in 9 s ends signed in, not stuck on the unreachable state', async () => {
  const fetcher = vi.fn((_input: RequestInfo | URL, init?: RequestInit) => {
    return new Promise<Response>((resolve, reject) => {
      const answer = setTimeout(
        () => resolve(new Response(JSON.stringify(metadata))),
        9_000,
      )
      init?.signal?.addEventListener(
        'abort',
        () => {
          clearTimeout(answer)
          reject(new DOMException('Aborted', 'AbortError'))
        },
        { once: true },
      )
    })
  })
  vi.stubGlobal('fetch', fetcher)
  const recovery = restoreBrowserSession().catch(() => {})
  await vi.advanceTimersByTimeAsync(8_000)
  await recovery
  expect(useSessionStore.getState().engineUnreachable).toBe(true)
  const stop = new AbortController()
  const retrying = retryWhileUnreachable(stop.signal)
  await vi.advanceTimersByTimeAsync(1_000 + 9_000)
  await retrying
  expect(useSessionStore.getState()).toMatchObject({
    sessionId: metadata.session_id,
    engineUnreachable: false,
  })
})

// A bearer from the old storage is sent for migration once per page, not on every retry.
it('a failing migration is not repeated on every retry', async () => {
  localStorage.setItem(
    'olivares.session',
    JSON.stringify({ state: { token: 'olvs_synthetic-old' } }),
  )
  const fetcher = vi.fn((_input: RequestInfo | URL, init?: RequestInit) =>
    Promise.resolve(
      new Response(null, { status: init?.method === 'POST' ? 503 : 401 }),
    ),
  )
  vi.stubGlobal('fetch', fetcher)
  await restoreBrowserSession().catch(() => {})
  expect(useSessionStore.getState().engineUnreachable).toBe(true)
  const stop = new AbortController()
  const retrying = retryWhileUnreachable(stop.signal)
  await vi.advanceTimersByTimeAsync(1_000 + 2_000 + 4_000)
  stop.abort()
  await retrying
  const posts = fetcher.mock.calls.filter(([, init]) => init?.method === 'POST')
  expect(posts).toHaveLength(1)
})

// 429 is the engine asking to slow down: not an answer about the session.
it('a 429 keeps the unreachable state and retries', async () => {
  vi.stubGlobal(
    'fetch',
    vi.fn(() => Promise.resolve(new Response(null, { status: 429 }))),
  )
  await restoreBrowserSession().catch(() => {})
  expect(useSessionStore.getState()).toMatchObject({
    ready: true,
    engineUnreachable: true,
  })
})
