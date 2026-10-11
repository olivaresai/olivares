// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { __resetRefreshState, configureApiClient } from '@/lib/api/client'
import { agentOpsApi } from './api'

beforeEach(() => {
  __resetRefreshState()
  configureApiClient({
    getToken: () => 'test-token',
    getTenant: () => 'active-tenant',
    onUnauthorized: () => {},
  })
  vi.stubGlobal(
    'fetch',
    vi.fn(
      async () =>
        new Response(JSON.stringify({ ok: true }), {
          status: 200,
          headers: { 'Content-Type': 'application/json' },
        }),
    ),
  )
})

afterEach(() => vi.unstubAllGlobals())

it.each(['stage', 'unstage', 'commit', 'branch'] as const)(
  'posts %s to its registered route with the action owner scope',
  async (action) => {
    const body = { paths: ['file.txt'], work_lease_fence: 7 }
    const signal = new AbortController().signal
    const dispatchGuard = vi.fn()
    await expect(
      agentOpsApi.gitAction('run/one', action, body, {
        tenant: 'owner-tenant',
        signal,
        dispatchGuard,
      }),
    ).resolves.toEqual({ ok: true })
    expect(dispatchGuard).toHaveBeenCalled()
    expect(fetch).toHaveBeenCalledOnce()
    const [input, init] = vi.mocked(fetch).mock.calls[0]!
    expect(new URL(String(input), 'http://console.test').pathname).toBe(
      `/v1/m/sessions/runs/run%2Fone/git/${action}`,
    )
    expect(init?.method).toBe('POST')
    expect(JSON.parse(String(init?.body))).toEqual(body)
    expect(new Headers(init?.headers).get('X-Olivares-Tenant')).toBe(
      'owner-tenant',
    )
    expect(init?.signal).toBe(signal)
  },
)
