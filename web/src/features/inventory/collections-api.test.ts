// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Collection coverage transport: GET /v1/m/inventory/collections with exactly the
// three selectors of one opened registration, reconstructed tenant/signal, a
// tenant-scoped key, and local guards so a malformed 200 is an error, never "none".
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { configureApiClient, type RequestOptions } from '@/lib/api/client'
import { ApiError } from '@/lib/api/errors'
import { inventoryApi, inventoryKeys, type CollectionSelection } from './api'
import type { CollectionPage } from './types'

export function typeProbes(): void {
  const selection: CollectionSelection = {
    source_id: 'src-1',
    source_revision: 3,
    environment_ref: 'xenv_1',
  }
  void (() => inventoryApi.collections(selection, { tenant: 'acme' }))
  // @ts-expect-error collections requires a named tenant
  void (() => inventoryApi.collections(selection))
  void (() =>
    inventoryApi.collections(selection, {
      tenant: 'acme',
      // @ts-expect-error a caller query is not forwarded
      query: { limit: 1 },
    }))
  void (() =>
    inventoryApi.collections(
      // @ts-expect-error the revision is a number, not a string
      { source_id: 'src-1', source_revision: '3', environment_ref: 'xenv_1' },
      { tenant: 'acme' },
    ))
}

let requests: { url: string; init: RequestInit | undefined }[] = []

function captureFetch(body: unknown, status = 200) {
  globalThis.fetch = vi.fn(async (url: string, init?: RequestInit) => {
    requests.push({ url: String(url), init })
    return new Response(JSON.stringify(body), {
      status,
      headers: new Headers({
        'Content-Type': 'application/json',
        'X-Request-ID': 'req-cov',
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

const SELECTION: CollectionSelection = {
  source_id: 'src-aaaa',
  source_revision: 7,
  environment_ref: 'xenv_1',
}

const PAGE: CollectionPage = {
  items: [
    {
      current: {
        run_id: '',
        source_id: 'src-aaaa',
        source_revision: 7,
        environment_ref: 'xenv_1',
        run_order: 0,
        coverage: 'unknown',
        reason: 'missing_report',
        projection: 'pending',
        admitted_count: 0,
        committed_count: 0,
        expected_count: 0,
        host_started_at: '',
      },
    },
  ],
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

describe('Inventory collection coverage transport', () => {
  it('GETs /v1/m/inventory/collections with exactly the three selectors, the named tenant and the signal', async () => {
    const signal = new AbortController().signal
    captureFetch(PAGE)
    const page = await inventoryApi.collections(SELECTION, {
      tenant: 't-captured',
      signal,
    })
    expect(page).toEqual(PAGE)
    expect(requests).toHaveLength(1)
    const u = parsed(requests[0]!.url)
    expect(u.pathname).toBe('/v1/m/inventory/collections')
    expect([...u.searchParams.keys()].sort()).toEqual([
      'environment_ref',
      'source_id',
      'source_revision',
    ])
    expect(u.searchParams.get('source_id')).toBe('src-aaaa')
    expect(u.searchParams.get('source_revision')).toBe('7')
    expect(u.searchParams.get('environment_ref')).toBe('xenv_1')
    expect(requests[0]!.init?.method ?? 'GET').toBe('GET')
    expect(header(requests[0]!.init, 'X-Olivares-Tenant')).toBe('t-captured')
    expect(header(requests[0]!.init, 'Authorization')).toBe('Bearer tok')
    expect(requests[0]!.init?.signal).toBe(signal)
  })

  it('escapes selector values as query values, not as path segments', async () => {
    captureFetch(PAGE)
    await inventoryApi.collections(
      {
        source_id: 'src/with space&x=1',
        source_revision: 9_007_199_254_740_991,
        environment_ref: 'env?#',
      },
      { tenant: 't1' },
    )
    const u = parsed(requests[0]!.url)
    expect(u.pathname).toBe('/v1/m/inventory/collections')
    expect(u.searchParams.get('source_id')).toBe('src/with space&x=1')
    expect(u.searchParams.get('source_revision')).toBe('9007199254740991')
    expect(u.searchParams.get('environment_ref')).toBe('env?#')
    expect(u.searchParams.get('x')).toBeNull()
  })

  it('a wide selection or options object does not propagate extra fields', async () => {
    const wideSelection = {
      ...SELECTION,
      workspace_id: 'w1',
      limit: 1,
    } as CollectionSelection
    const wideOptions = {
      tenant: 't1',
      anonymous: true,
      headers: { Authorization: 'evil', 'X-Olivares-Tenant': 'forged' },
      query: { cursor: 'c', workspace_id: 'w2' },
    } as RequestOptions & { tenant: string }
    captureFetch(PAGE)
    await inventoryApi.collections(wideSelection, wideOptions)
    const u = parsed(requests[0]!.url)
    expect([...u.searchParams.keys()].sort()).toEqual([
      'environment_ref',
      'source_id',
      'source_revision',
    ])
    expect(header(requests[0]!.init, 'X-Olivares-Tenant')).toBe('t1')
    expect(header(requests[0]!.init, 'Authorization')).toBe('Bearer tok')
  })

  it('keys the read by tenant and the exact selection', () => {
    expect(inventoryKeys.collections('t1', SELECTION)).toEqual([
      'inventory',
      't1',
      'collections',
      {
        source_id: 'src-aaaa',
        source_revision: 7,
        environment_ref: 'xenv_1',
      },
    ])
    expect(inventoryKeys.collections('t2', SELECTION)[1]).toBe('t2')
    expect(inventoryKeys.collections('t1', SELECTION).slice(0, 2)).toEqual(
      inventoryKeys.all('t1'),
    )
  })

  it('treats a page without exactly one item as an error, not as no coverage', async () => {
    for (const body of [
      { has_more: false },
      { items: [], has_more: false },
      { items: [PAGE.items[0], PAGE.items[0]], has_more: false },
      { items: [{}], has_more: false },
      { items: PAGE.items },
      [],
    ]) {
      captureFetch(body)
      await expect(
        inventoryApi.collections(SELECTION, { tenant: 't1' }),
      ).rejects.toMatchObject({ status: 200, code: 'invalid_response' })
    }
  })

  it('surfaces a 400 selector refusal as an ApiError with its status', async () => {
    captureFetch(
      {
        error: {
          code: 'bad_request',
          message:
            'source_id, source_revision and environment_ref are required',
        },
      },
      400,
    )
    const err = await inventoryApi
      .collections(SELECTION, { tenant: 't1' })
      .catch((e: unknown) => e)
    expect(err).toBeInstanceOf(ApiError)
    expect((err as ApiError).status).toBe(400)
  })
})
