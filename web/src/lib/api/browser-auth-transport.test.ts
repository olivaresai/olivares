// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { totpApi } from '@/features/identity/api'
import { restoreBrowserSession } from '@/lib/auth/browser-session'
import { useSessionStore } from '@/stores/session'
import { configureApiClient, http } from './client'
import { authApi } from './endpoints'

beforeEach(() => {
  useSessionStore.getState().clear()
  configureApiClient({
    getToken: () => 'old-credential',
    getCSRFToken: () => 'old-csrf',
    getTenant: () => 'old-tenant',
    onUnauthorized: vi.fn(),
  })
})

afterEach(() => {
  vi.unstubAllGlobals()
  useSessionStore.getState().clear()
  localStorage.clear()
})

it('every console sign-in path requests cookie transport without an old bearer', async () => {
  const fetch = vi.fn<typeof globalThis.fetch>().mockImplementation(
    async () =>
      new Response(
        JSON.stringify({
          csrf_token: 'new-csrf',
          session_id: 's1',
          expires_at: '2099-01-01T00:00:00Z',
        }),
        {
          headers: { 'Content-Type': 'application/json' },
        },
      ),
  )
  vi.stubGlobal('fetch', fetch)
  await authApi.login({
    email: 'user@example.test',
    password: 'fixture-password',
  })
  await authApi.setup({
    token: 'setup-proof',
    email: 'user@example.test',
    password: 'fixture-password',
  })
  await authApi.acceptInvite({
    token: 'invite-proof',
    password: 'fixture-password',
  })
  await totpApi.enrol({ mfa_token: 'pending-proof' })
  await totpApi.activate({ mfa_token: 'pending-proof', code: '123456' })
  await totpApi.challenge({ mfa_token: 'pending-proof', code: '123456' })
  await totpApi.challenge({
    mfa_token: 'pending-proof',
    recovery_code: 'recovery-proof',
  })

  expect(fetch.mock.calls.map(([path]) => path)).toEqual([
    '/v1/auth/login',
    '/v1/setup',
    '/v1/invites/accept',
    '/v1/auth/totp/enrol',
    '/v1/auth/totp/activate',
    '/v1/auth/totp/challenge',
    '/v1/auth/totp/challenge',
  ])
  for (const [, init] of fetch.mock.calls) {
    const headers = new Headers(init?.headers)
    expect(init?.method).toBe('POST')
    expect(init?.credentials).toBe('same-origin')
    expect(headers.get('X-Olivares-Session')).toBe('cookie')
    expect(headers.has('Authorization')).toBe(false)
    expect(headers.has('X-Olivares-Tenant')).toBe(false)
  }
})

const cookieSignIns = [
  [
    'module sign-in',
    () =>
      http.post(
        '/v1/auth/module-login',
        {},
        {
          anonymous: true,
          headers: { 'X-Olivares-Session': 'cookie' },
        },
      ),
  ],
  [
    'password',
    () =>
      authApi.login({
        email: 'new@example.test',
        password: 'fixture-password',
      }),
  ],
  [
    'setup',
    () =>
      authApi.setup({
        token: 'setup-proof',
        email: 'new@example.test',
        password: 'fixture-password',
      }),
  ],
  [
    'invite',
    () =>
      authApi.acceptInvite({
        token: 'invite-proof',
        password: 'fixture-password',
      }),
  ],
  [
    'MFA activation',
    () => totpApi.activate({ mfa_token: 'pending-proof', code: '123456' }),
  ],
  [
    'MFA challenge',
    () => totpApi.challenge({ mfa_token: 'pending-proof', code: '123456' }),
  ],
] as const

it.each(cookieSignIns)(
  '%s waits for legacy migration headers before requesting a new cookie',
  async (_label, signIn) => {
    useSessionStore.getState().clear()
    useSessionStore.setState({ ready: false })
    localStorage.setItem(
      'olivares.session',
      JSON.stringify({ state: { token: 'olvs_old-fixture' } }),
    )
    const metadata = {
      csrf_token: 'old-csrf',
      session_id: 'old-session',
      expires_at: '2099-01-01T00:00:00Z',
    }
    let finish!: (response: Response) => void
    const calls: string[] = []
    vi.stubGlobal(
      'fetch',
      vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
        const call = (init?.method ?? 'GET') + ' ' + String(input)
        calls.push(call)
        if (call === 'GET /v1/auth/browser-session')
          return Promise.resolve(new Response(null, { status: 401 }))
        if (call === 'POST /v1/auth/browser-session')
          return new Promise<Response>((resolve) => {
            finish = resolve
          })
        return Promise.resolve(
          new Response(
            JSON.stringify({
              ...metadata,
              csrf_token: 'new-csrf',
              session_id: 'new-session',
            }),
          ),
        )
      }),
    )
    const recovering = restoreBrowserSession()
    await vi.waitFor(() => expect(finish).toBeDefined())
    const signingIn = signIn()
    try {
      await Promise.resolve()
      await Promise.resolve()
      expect(calls).toEqual([
        'GET /v1/auth/browser-session',
        'POST /v1/auth/browser-session',
      ])
    } finally {
      finish(new Response(JSON.stringify(metadata)))
      await Promise.all([recovering, signingIn])
    }
    expect(calls).toHaveLength(3)
  },
)

it('sign-in starts recovery when the provider has not started it yet', async () => {
  useSessionStore.setState({ ready: false })
  localStorage.setItem(
    'olivares.session',
    JSON.stringify({ state: { token: 'olvs_old-fixture' } }),
  )
  const fetch = vi
    .fn<typeof globalThis.fetch>()
    .mockResolvedValueOnce(new Response(null, { status: 401 }))
    .mockImplementation(
      async () =>
        new Response(
          JSON.stringify({
            csrf_token: 'csrf-fixture',
            session_id: 'session-fixture',
            expires_at: '2099-01-01T00:00:00Z',
          }),
        ),
    )
  vi.stubGlobal('fetch', fetch)
  await authApi.login({
    email: 'new@example.test',
    password: 'fixture-password',
  })
  expect(
    fetch.mock.calls.map(([path, init]) => `${init?.method} ${path}`),
  ).toEqual([
    'GET /v1/auth/browser-session',
    'POST /v1/auth/browser-session',
    'POST /v1/auth/login',
  ])
  const headers = new Headers(fetch.mock.calls[2][1]?.headers)
  expect(headers.has('Authorization')).toBe(false)
})

it('a failed migration releases sign-in without replaying its old bearer', async () => {
  useSessionStore.setState({ ready: false })
  localStorage.setItem(
    'olivares.session',
    JSON.stringify({ state: { token: 'olvs_old-fixture' } }),
  )
  let fail!: (error: Error) => void
  const calls: string[] = []
  vi.stubGlobal(
    'fetch',
    vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const call = (init?.method ?? 'GET') + ' ' + String(input)
      calls.push(call)
      if (call === 'GET /v1/auth/browser-session')
        return Promise.resolve(new Response(null, { status: 401 }))
      if (call === 'POST /v1/auth/browser-session')
        return new Promise<Response>((_resolve, reject) => {
          fail = reject
        })
      return Promise.resolve(
        new Response(
          JSON.stringify({
            csrf_token: 'new-csrf',
            session_id: 'new-session',
            expires_at: '2099-01-01T00:00:00Z',
          }),
        ),
      )
    }),
  )
  const recovering = restoreBrowserSession().catch(() => {})
  await vi.waitFor(() => expect(fail).toBeDefined())
  const signingIn = authApi.login({
    email: 'new@example.test',
    password: 'fixture-password',
  })
  try {
    await Promise.resolve()
    await Promise.resolve()
    expect(calls).toEqual([
      'GET /v1/auth/browser-session',
      'POST /v1/auth/browser-session',
    ])
  } finally {
    fail(new Error('fixture disconnected'))
    await Promise.all([recovering, signingIn])
  }
  expect(useSessionStore.getState().ready).toBe(true)
  // The provider can run after this early sign-in; it must not migrate again.
  await restoreBrowserSession()
  expect(calls).toEqual([
    'GET /v1/auth/browser-session',
    'POST /v1/auth/browser-session',
    'POST /v1/auth/login',
  ])
})
