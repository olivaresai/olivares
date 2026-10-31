// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { waitFor } from '@testing-library/react'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { __resetRefreshState, configureApiClient } from '@/lib/api/client'
import { consoleApi } from './api'

let tenant = 'tenant-a'
const fetchMock = vi.fn()
const userID = '018f2c2e-0000-7000-8000-000000000002'

beforeEach(() => {
  tenant = 'tenant-a'
  __resetRefreshState()
  fetchMock.mockReset().mockResolvedValue(new Response(null, { status: 204 }))
  vi.stubGlobal('fetch', fetchMock)
  configureApiClient({
    getToken: () => 'fixture',
    getCSRFToken: undefined,
    getTenant: () => tenant,
    getExpiresAt: undefined,
    refreshSession: undefined,
    onUnauthorized: vi.fn(),
  })
})

afterEach(() => {
  __resetRefreshState()
  configureApiClient({
    getToken: () => null,
    getTenant: () => null,
    getExpiresAt: undefined,
    refreshSession: undefined,
    onUnauthorized: () => undefined,
  })
  vi.unstubAllGlobals()
})

it('ends the selected member sessions with the current action signal', async () => {
  const controller = new AbortController()
  const dispatchGuard = vi.fn()
  await consoleApi.endMemberSessions(userID, {
    signal: controller.signal,
    dispatchGuard,
  })
  expect(dispatchGuard).toHaveBeenCalled()
  expect(fetchMock).toHaveBeenCalledOnce()
  const [url, request] = fetchMock.mock.calls[0]
  expect(String(url)).toContain(`/v1/users/${userID}/sessions`)
  expect(request.method).toBe('DELETE')
  expect(new Headers(request.headers).get('X-Olivares-Tenant')).toBe('tenant-a')
  expect(request.signal).toBe(controller.signal)
})

it('refuses an end request after an organization switch during session refresh', async () => {
  let release!: (value: boolean) => void
  const refreshSession = vi.fn(
    () => new Promise<boolean>((resolve) => (release = resolve)),
  )
  configureApiClient({
    getExpiresAt: () => new Date(Date.now() + 1000).toISOString(),
    refreshSession,
  })
  const stale = new Error('The action belongs to the previous organization.')
  const outcome = consoleApi
    .endMemberSessions(userID, {
      dispatchGuard: () => {
        if (tenant !== 'tenant-a') throw stale
      },
    })
    .then(
      () => undefined,
      (error: unknown) => error,
    )
  await waitFor(() => expect(refreshSession).toHaveBeenCalledOnce())
  tenant = 'tenant-b'
  release(true)
  expect(await outcome).toBe(stale)
  expect(fetchMock).not.toHaveBeenCalled()
})
