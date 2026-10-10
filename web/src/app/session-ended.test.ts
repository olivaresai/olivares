// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// HU-R37 through the real wiring: one request's 401, the failed credential refresh, and the
// request's own 401 again leave the reason the sign-in page shows.
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import './providers'
import { __resetRefreshState, apiFetch } from '@/lib/api/client'
import { useSessionStore } from '@/stores/session'

beforeEach(() => {
  __resetRefreshState()
  useSessionStore.setState({
    csrfToken: 'csrf',
    sessionId: 'sid',
    expiresAt: '2999-01-01T00:00:00Z',
    endReason: null,
  })
})
afterEach(() => vi.unstubAllGlobals())

it('says the session ended after a refused request and a refused refresh', async () => {
  const fetchMock = vi.fn(
    async (_input: RequestInfo | URL) =>
      new Response(
        JSON.stringify({ error: { code: 'unauthenticated', message: 'no' } }),
        { status: 401, headers: { 'Content-Type': 'application/json' } },
      ),
  )
  vi.stubGlobal('fetch', fetchMock)
  await expect(apiFetch('/v1/whoami')).rejects.toMatchObject({ status: 401 })
  expect(
    fetchMock.mock.calls.some(([input]) =>
      String(input).includes('/v1/auth/refresh'),
    ),
  ).toBe(true)
  expect(useSessionStore.getState().sessionId).toBeNull()
  expect(useSessionStore.getState().endReason).toBe('ended')
})
