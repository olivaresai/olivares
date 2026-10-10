// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { afterEach, describe, expect, it, vi } from 'vitest'
import {
  __resetRefreshState,
  apiFetch,
  configureApiClient,
  ensureFreshSession,
} from './client'
import { ApiError } from './errors'

afterEach(() => {
  configureApiClient({
    getToken: () => null,
    getCSRFToken: undefined,
    getTenant: () => null,
    onUnauthorized: () => {},
    refreshSession: undefined,
    getExpiresAt: undefined,
  })
  __resetRefreshState()
  vi.unstubAllGlobals()
})

describe('ensureFreshSession shared consumer', () => {
  it.each([null, 'invalid', new Date(Date.now() + 3_600_000).toISOString()])(
    'does not renew when expiry is %s',
    async (expiresAt) => {
      const refreshSession = vi.fn(async () => true)
      configureApiClient({ getExpiresAt: () => expiresAt, refreshSession })

      await ensureFreshSession()

      expect(refreshSession).not.toHaveBeenCalled()
    },
  )

  it('shares one real refresh with concurrent consumers and ordinary requests', async () => {
    let release!: () => void
    const gate = new Promise<void>((resolve) => {
      release = resolve
    })
    let csrfToken = 'csrf_old'
    let expiresAt = new Date(Date.now() + 30_000).toISOString()
    const fetchMock = vi.fn(async (url: RequestInfo | URL) => {
      if (url === '/v1/auth/refresh') {
        await gate
        return Response.json({
          csrf_token: 'csrf_renewed',
          expires_at: new Date(Date.now() + 3_600_000).toISOString(),
        })
      }
      return Response.json({ ok: true })
    })
    vi.stubGlobal('fetch', fetchMock)
    const refreshSession = vi.fn(async () => {
      const session = await apiFetch<{
        csrf_token: string
        expires_at: string
      }>('/v1/auth/refresh', { method: 'POST' })
      csrfToken = session.csrf_token
      expiresAt = session.expires_at
      return true
    })
    configureApiClient({
      getToken: () => null,
      getCSRFToken: () => csrfToken,
      getExpiresAt: () => expiresAt,
      refreshSession,
    })

    const pending = Promise.all([
      ensureFreshSession(),
      ensureFreshSession(),
      ensureFreshSession(),
      apiFetch('/v1/agents'),
    ])
    expect(refreshSession).toHaveBeenCalledTimes(1)
    expect(fetchMock).toHaveBeenCalledTimes(1)
    release()
    await pending

    const headers = new Headers(vi.mocked(fetch).mock.calls[1][1]?.headers)
    expect(headers.get('X-CSRF-Token')).toBe('csrf_renewed')
    expect(headers.has('Authorization')).toBe(false)
    await ensureFreshSession()
    expect(refreshSession).toHaveBeenCalledTimes(1)

    expiresAt = new Date(Date.now() + 30_000).toISOString()
    await ensureFreshSession()
    expect(refreshSession).toHaveBeenCalledTimes(2)
  })

  it.each([false, 'reject'] as const)(
    'preserves best-effort renewal for a callback outcome of %s',
    async (outcome) => {
      const onUnauthorized = vi.fn()
      configureApiClient({
        getExpiresAt: () => new Date(Date.now() + 30_000).toISOString(),
        refreshSession: async () => {
          if (outcome === 'reject') throw new Error('unreachable')
          return false
        },
        onUnauthorized,
      })

      await expect(ensureFreshSession()).resolves.toBeUndefined()
      expect(onUnauthorized).not.toHaveBeenCalled()
    },
  )

  it.each([false, true])(
    'keeps a retired isolated mutation 401 away from the successor (abort=%s)',
    async (abort) => {
      let release!: (response: Response) => void
      const response = new Promise<Response>((resolve) => {
        release = resolve
      })
      let token = 'olvs_old'
      let expiresAt = new Date(Date.now() + 30_000).toISOString()
      const onUnauthorized = vi.fn()
      const refreshSession = vi.fn(async () => {
        token = 'olvs_renewed'
        expiresAt = new Date(Date.now() + 3_600_000).toISOString()
        return true
      })
      configureApiClient({
        getToken: () => token,
        getExpiresAt: () => expiresAt,
        refreshSession,
        onUnauthorized,
      })
      const fetchMock = vi.fn(() => response.then((value) => value.clone()))
      vi.stubGlobal('fetch', fetchMock)
      const controller = new AbortController()

      await ensureFreshSession()
      const pending = apiFetch('/v1/scoped-mutation', {
        method: 'POST',
        body: { value: 'once' },
        sessionEffects: 'none',
        signal: controller.signal,
      })
      const headers = new Headers(vi.mocked(fetch).mock.calls[0][1]?.headers)
      expect(headers.get('Authorization')).toBe('Bearer olvs_renewed')
      token = 'olvs_successor'
      if (abort) controller.abort()
      release(
        Response.json(
          { error: { code: 'unauthenticated', message: 'retired' } },
          { status: 401 },
        ),
      )

      if (abort)
        await expect(pending).rejects.toMatchObject({ name: 'AbortError' })
      else await expect(pending).rejects.toBeInstanceOf(ApiError)
      expect(fetchMock).toHaveBeenCalledTimes(1)
      expect(refreshSession).toHaveBeenCalledTimes(1)
      expect(onUnauthorized).not.toHaveBeenCalled()
      expect(token).toBe('olvs_successor')
    },
  )
})
