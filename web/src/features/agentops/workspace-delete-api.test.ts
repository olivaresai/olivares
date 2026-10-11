// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { __resetRefreshState, configureApiClient } from '@/lib/api/client'
import { ApiError } from '@/lib/api/errors'
import { agentOpsApi } from './api'

beforeEach(() => {
  __resetRefreshState()
  configureApiClient({
    getToken: () => null,
    getCSRFToken: () => 'test-csrf',
    getTenant: () => 'active-tenant',
    onUnauthorized: vi.fn(),
    refreshSession: undefined,
    getExpiresAt: undefined,
  })
  vi.stubGlobal(
    'fetch',
    vi.fn(async () => Response.json({ deleted: true })),
  )
})

afterEach(() => vi.unstubAllGlobals())

it.each([undefined, false, true])(
  'sends the selected path and recursive=%s in the DELETE URL without a body',
  async (recursive) => {
    const path = 'generated/a b & #?%/数据.txt'
    await expect(
      agentOpsApi.deleteFile('ws/one', path, recursive),
    ).resolves.toEqual({ deleted: true })

    expect(fetch).toHaveBeenCalledOnce()
    const [input, init] = vi.mocked(fetch).mock.calls[0]!
    const url = new URL(String(input), 'http://console.test')
    expect(url.pathname).toBe('/v1/m/sessions/workspaces/ws%2Fone/files')
    expect([...url.searchParams.entries()]).toEqual([
      ['path', path],
      ...(recursive ? [['recursive', 'true']] : []),
    ])
    expect(init?.method).toBe('DELETE')
    expect(init?.body).toBeUndefined()
    expect(init?.credentials).toBe('same-origin')
    const headers = new Headers(init?.headers)
    expect(headers.get('X-CSRF-Token')).toBe('test-csrf')
    expect(headers.get('X-Olivares-Tenant')).toBe('active-tenant')
  },
)

it('propagates a refused deletion instead of reporting success', async () => {
  vi.mocked(fetch).mockResolvedValue(
    Response.json(
      { error: 'refusing to delete workspace root' },
      { status: 400 },
    ),
  )
  await expect(agentOpsApi.deleteFile('ws_1', '', true)).rejects.toBeInstanceOf(
    ApiError,
  )
})
