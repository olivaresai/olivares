// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// C3 transport: reconstructed tenant/signal/limit, no spread of a wide options
// object, cursor omitted on the first page, local guards for a malformed 200.
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { configureApiClient, type RequestOptions } from '@/lib/api/client'
import { ApiError } from '@/lib/api/errors'
import { inventoryApi, OBSERVATION_PAGE_LIMIT } from './api'

export function typeProbes(): void {
  void (() => inventoryApi.detail('agent', 'a1', { tenant: 'acme' }))
  void (() => inventoryApi.observations('agent', 'a1', { tenant: 'acme' }))
  void (() =>
    inventoryApi.observations('agent', 'a1', {
      tenant: 'acme',
      cursor: '00000000-0000-4000-8000-000000000001',
    }))

  // @ts-expect-error detail requires a named tenant
  void (() => inventoryApi.detail('agent', 'a1'))
  // @ts-expect-error observations requires a named tenant
  void (() => inventoryApi.observations('agent', 'a1'))
  void (() =>
    inventoryApi.observations('agent', 'a1', {
      tenant: 'acme',
      // @ts-expect-error anonymous would drop the tenant header
      anonymous: true,
    }))
  void (() =>
    inventoryApi.detail('agent', 'a1', {
      tenant: 'acme',
      // @ts-expect-error headers are not on the allowlist
      headers: { Authorization: 'no' },
    }))
  void (() =>
    inventoryApi.observations('agent', 'a1', {
      tenant: 'acme',
      // @ts-expect-error a caller query is not forwarded
      query: { limit: 1 },
    }))
}

let requests: { url: string; init: RequestInit | undefined }[] = []

function captureFetch(
  body: unknown = { items: [], has_more: false },
  status = 200,
) {
  globalThis.fetch = vi.fn(async (url: string, init?: RequestInit) => {
    requests.push({ url: String(url), init })
    return new Response(JSON.stringify(body), {
      status,
      headers: new Headers({
        'Content-Type': 'application/json',
        'X-Request-ID': 'req-c3',
      }),
    })
  }) as never
}

function parsed(url: string): URL {
  return new URL(url, 'http://test.example')
}

function header(init: RequestInit | undefined, name: string): string | null {
  return new Headers(init?.headers).get(name)
}

const PAGE = {
  items: [],
  has_more: false,
}

beforeEach(() => {
  requests = []
  configureApiClient({
    getToken: () => 'tok',
    getTenant: () => 'getter-tenant',
    onUnauthorized: () => {},
  })
})

afterEach(() => {
  requests = []
  vi.restoreAllMocks()
  configureApiClient({
    getToken: () => null,
    getTenant: () => null,
    onUnauthorized: () => {},
  })
})

describe('Inventory C3 transport', () => {
  it('sends the captured tenant, signal, escaped kind/id, limit 25 and omits cursor on the first page', async () => {
    const signal = new AbortController().signal
    captureFetch(PAGE)
    await inventoryApi.observations('agent/kind', 'id space', {
      tenant: 't-captured',
      signal,
    })
    expect(requests).toHaveLength(1)
    const u = parsed(requests[0]!.url)
    expect(u.pathname).toBe(
      '/v1/m/inventory/entities/agent%2Fkind/id%20space/observations',
    )
    expect(u.searchParams.get('limit')).toBe(String(OBSERVATION_PAGE_LIMIT))
    expect(u.searchParams.has('cursor')).toBe(false)
    expect(u.searchParams.get('workspace_id')).toBeNull()
    expect(header(requests[0]!.init, 'X-Olivares-Tenant')).toBe('t-captured')
    expect(header(requests[0]!.init, 'Authorization')).toBe('Bearer tok')
    expect(requests[0]!.init?.method ?? 'GET').toBe('GET')
    expect(requests[0]!.init?.signal).toBe(signal)
  })

  it('sends the cursor only as the named continuation token', async () => {
    captureFetch(PAGE)
    await inventoryApi.observations('agent', 'a1', {
      tenant: 't1',
      cursor: '00000000-0000-4000-8000-000000000019',
    })
    const u = parsed(requests[0]!.url)
    expect(u.searchParams.get('cursor')).toBe(
      '00000000-0000-4000-8000-000000000019',
    )
    expect(u.searchParams.get('limit')).toBe('25')
  })

  it('omits an empty cursor rather than sending cursor=', async () => {
    captureFetch(PAGE)
    await inventoryApi.observations('agent', 'a1', {
      tenant: 't1',
      cursor: '',
    })
    expect(parsed(requests[0]!.url).searchParams.has('cursor')).toBe(false)
  })

  it('a wide variable with anonymous, headers and a foreign query does not propagate them', async () => {
    const wide = {
      tenant: 't1',
      anonymous: true,
      headers: { Authorization: 'evil', 'X-Olivares-Tenant': 'forged' },
      query: { limit: 1, foo: 'bar', workspace_id: 'w1' },
    } as RequestOptions & { tenant: string }
    captureFetch(PAGE)
    await inventoryApi.observations('agent', 'a1', wide)
    const u = parsed(requests[0]!.url)
    expect(header(requests[0]!.init, 'X-Olivares-Tenant')).toBe('t1')
    expect(header(requests[0]!.init, 'Authorization')).toBe('Bearer tok')
    expect(u.searchParams.get('limit')).toBe('25')
    expect(u.searchParams.get('foo')).toBeNull()
    expect(u.searchParams.get('workspace_id')).toBeNull()
  })

  it('detail reconstructs tenant and signal and does not spread a wide object', async () => {
    const signal = new AbortController().signal
    const wide = {
      tenant: 't-detail',
      anonymous: true,
      headers: { Authorization: 'evil' },
      query: { workspace_id: 'w1' },
      signal,
    } as RequestOptions & { tenant: string }
    captureFetch({ entry: { kind: 'agent' } })
    await inventoryApi.detail('agent', 'a1', wide)
    expect(header(requests[0]!.init, 'X-Olivares-Tenant')).toBe('t-detail')
    expect(header(requests[0]!.init, 'Authorization')).toBe('Bearer tok')
    expect(parsed(requests[0]!.url).searchParams.get('workspace_id')).toBeNull()
    expect(requests[0]!.init?.signal).toBe(signal)
    expect(requests[0]!.init?.method ?? 'GET').toBe('GET')
  })

  it('does not POST or query a roster', async () => {
    captureFetch(PAGE)
    await inventoryApi.observations('agent', 'a1', { tenant: 't1' })
    expect(requests[0]!.init?.method ?? 'GET').toBe('GET')
    expect(requests[0]!.url).not.toMatch(/roster|sources/)
  })

  it('aborts when the forwarded signal is aborted', async () => {
    const ctrl = new AbortController()
    globalThis.fetch = vi.fn(async (_url: string, init?: RequestInit) => {
      return await new Promise<Response>((_resolve, reject) => {
        init?.signal?.addEventListener('abort', () => {
          reject(new DOMException('Aborted', 'AbortError'))
        })
      })
    }) as never
    const pending = inventoryApi.observations('agent', 'a1', {
      tenant: 't1',
      signal: ctrl.signal,
    })
    ctrl.abort()
    await expect(pending).rejects.toMatchObject({ name: 'AbortError' })
  })

  it('treats missing items as an error, not an empty history', async () => {
    captureFetch({ has_more: false })
    await expect(
      inventoryApi.observations('agent', 'a1', { tenant: 't1' }),
    ).rejects.toBeInstanceOf(ApiError)
  })

  it('treats has_more without a usable cursor as an error, not the end', async () => {
    captureFetch({ items: [], has_more: true })
    await expect(
      inventoryApi.observations('agent', 'a1', { tenant: 't1' }),
    ).rejects.toBeInstanceOf(ApiError)
    requests = []
    captureFetch({ items: [], has_more: true, cursor: '' })
    await expect(
      inventoryApi.observations('agent', 'a1', { tenant: 't1' }),
    ).rejects.toBeInstanceOf(ApiError)
  })

  it('treats a continuation cursor that did not advance as an error', async () => {
    const cursor = '00000000-0000-4000-8000-000000000019'
    captureFetch({ items: [{ receipt_id: cursor }], has_more: true, cursor })
    await expect(
      inventoryApi.observations('agent', 'a1', { tenant: 't1', cursor }),
    ).rejects.toBeInstanceOf(ApiError)
  })

  it('records a 401 through the existing session hook, not as an empty page', async () => {
    const unauthorized = vi.fn()
    configureApiClient({
      getToken: () => 'tok',
      getTenant: () => 't1',
      onUnauthorized: unauthorized,
    })
    captureFetch({ error: { code: 'unauthenticated', message: 'no' } }, 401)
    await expect(
      inventoryApi.observations('agent', 'a1', { tenant: 't1' }),
    ).rejects.toMatchObject({ status: 401 })
    expect(unauthorized).toHaveBeenCalled()
  })
})
