// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { afterEach, describe, expect, it, vi } from 'vitest'
import { __resetRefreshState, configureApiClient } from '@/lib/api/client'
import { ApiError } from '@/lib/api/errors'
import { fetchStatementExport } from '@/features/finops/api'
import { securityApi } from '@/features/security/api'

/** Downloads are ordinary requests: the same bearer, the same 401 handling and the same
 * error envelope as every call made through the shared client. */
let sent: Headers | undefined

function answer(status: number, body: string, type = 'text/csv') {
  globalThis.fetch = vi.fn(async (_url: string, init?: RequestInit) => {
    sent = new Headers(init?.headers)
    return new Response(body, { status, headers: { 'Content-Type': type } })
  }) as never
}

afterEach(() => {
  configureApiClient({
    getToken: () => null,
    getTenant: () => null,
    onUnauthorized: () => {},
    refreshSession: undefined,
    getExpiresAt: undefined,
  })
  __resetRefreshState()
  sent = undefined
})

describe('downloads use the shared client', () => {
  it('a statement export signs out on a 401 the session cannot recover', async () => {
    const onUnauthorized = vi.fn()
    configureApiClient({ getToken: () => 'olvs_x', onUnauthorized })
    answer(
      401,
      JSON.stringify({ error: { code: 'unauthenticated', message: 'gone' } }),
      'application/json',
    )
    await expect(fetchStatementExport('st-1')).rejects.toBeInstanceOf(ApiError)
    expect(onUnauthorized).toHaveBeenCalledTimes(1)
  })

  it('a findings export carries the session bearer and tenant', async () => {
    configureApiClient({
      getToken: () => 'olvs_abc',
      getTenant: () => 'tenant-1',
    })
    answer(200, '{"runs":[]}', 'application/json')
    const result = await securityApi.exportFindings()
    expect(sent?.get('Authorization')).toBe('Bearer olvs_abc')
    expect(sent?.get('X-Olivares-Tenant')).toBe('tenant-1')
    expect(result.text).toBe('{"runs":[]}')
  })
})
