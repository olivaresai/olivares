// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { beforeEach, afterEach, expect, it, vi } from 'vitest'
import { configureApiClient } from '@/lib/api/client'
import { agentOpsApi } from './api'

const fetcher = vi.fn()
beforeEach(() => {
  vi.stubGlobal('fetch', fetcher)
  fetcher
    .mockReset()
    .mockImplementation(
      async () =>
        new Response('{}', { headers: { 'Content-Type': 'application/json' } }),
    )
  configureApiClient({
    getToken: () => null,
    getCSRFToken: () => null,
    getTenant: () => 'tenant-current',
    getExpiresAt: undefined,
    refreshSession: undefined,
    onUnauthorized: () => {},
  })
})
afterEach(() => {
  vi.unstubAllGlobals()
})

it('pins wiki file reads, pagination and readiness to their query tenant and cancellation signal', async () => {
  const controller = new AbortController()
  const scope = { tenant: 'tenant-original', signal: controller.signal }
  await agentOpsApi.readFile('ws /1', 'wiki/a b.md', undefined, scope)
  await agentOpsApi.listFiles(
    'ws /1',
    { path: 'raw/nested', cursor: 'next', limit: 100 },
    scope,
  )
  await agentOpsApi.toolsReadiness(scope)
  for (const [, options] of fetcher.mock.calls) {
    expect(new Headers(options.headers).get('X-Olivares-Tenant')).toBe(
      'tenant-original',
    )
    expect(options.signal).toBe(controller.signal)
  }
  const read = new URL(fetcher.mock.calls[0]![0], 'http://engine.test')
  expect(read.pathname).toBe('/v1/m/sessions/workspaces/ws%20%2F1/files/raw')
  expect(read.searchParams.get('path')).toBe('wiki/a b.md')
  const list = new URL(fetcher.mock.calls[1]![0], 'http://engine.test')
  expect(list.searchParams.get('path')).toBe('raw/nested')
  expect(list.searchParams.get('cursor')).toBe('next')
})

it('posts init without a body and uploads binary bytes verbatim through the files API', async () => {
  const authority = {
    tenant: 'tenant-original',
    signal: new AbortController().signal,
    dispatchGuard: vi.fn(),
  }
  await agentOpsApi.initWiki('ws_1', authority)
  const bytes = new Blob([new Uint8Array([0, 255, 10, 13, 34])])
  await agentOpsApi.writeFile('ws_1', 'raw/source.bin', bytes, authority)
  const [initUrl, init] = fetcher.mock.calls[0]!
  expect(initUrl).toBe('/v1/m/sessions/workspaces/ws_1/wiki')
  expect(init.method).toBe('POST')
  expect(init.body).toBeUndefined()
  const [writeUrl, write] = fetcher.mock.calls[1]!
  expect(new URL(writeUrl, 'http://engine.test').searchParams.get('path')).toBe(
    'raw/source.bin',
  )
  expect(write.method).toBe('PUT')
  expect(write.body).toBe(bytes)
  expect(new Headers(write.headers).get('Content-Type')).toBe(
    'application/octet-stream',
  )
  expect(new Headers(write.headers).get('X-Olivares-Tenant')).toBe(
    'tenant-original',
  )
  expect(authority.dispatchGuard).toHaveBeenCalled()
})

it('refuses a queued upload before fetch when its authority retired', async () => {
  await expect(
    agentOpsApi.writeFile('ws_1', 'raw/source.md', 'bytes', {
      tenant: 't1',
      dispatchGuard: () => {
        throw new DOMException('retired', 'AbortError')
      },
    }),
  ).rejects.toThrow('retired')
  expect(fetcher).not.toHaveBeenCalled()
})
