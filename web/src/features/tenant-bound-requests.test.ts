// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { afterEach, describe, expect, it, vi } from 'vitest'
import { knowledgeApi } from '@/features/knowledge/api'
import { __resetRefreshState, configureApiClient } from '@/lib/api/client'

let globalTenant: string | null = 'tenant-A'
const sentTenants: Array<string | null> = []

/**
 * Model a production mutation created by a render in one tenant. TanStack keeps
 * that mutation function as the operation's executable; the global store can move
 * independently before the transport starts or while authentication is replayed.
 *
 * The closure is the production mutation's creation boundary: it retains A even if
 * the active tenant store changes before transport or while auth is replayed.
 */
function mutationCreatedIn(tenant: string | null): () => Promise<unknown> {
  return () => knowledgeApi.deleteDlpRule('rule-1', { tenant })
}

function controlledRefresh(): {
  refresh: () => Promise<boolean>
  started: Promise<void>
  release: () => void
  calls: () => number
} {
  let signalStarted!: () => void
  let release!: () => void
  let calls = 0
  const started = new Promise<void>((resolve) => {
    signalStarted = resolve
  })
  const gate = new Promise<boolean>((resolve) => {
    release = () => resolve(true)
  })
  return {
    refresh: () => {
      calls++
      signalStarted()
      return gate
    },
    started,
    release,
    calls: () => calls,
  }
}

function respondWith(statuses: number[]): void {
  let index = 0
  vi.stubGlobal(
    'fetch',
    vi.fn(async (_url: string, init?: RequestInit) => {
      sentTenants.push(new Headers(init?.headers).get('X-Olivares-Tenant'))
      const status = statuses[Math.min(index, statuses.length - 1)]
      index++
      if (status === 401) {
        return new Response(
          JSON.stringify({
            error: { code: 'unauthenticated', message: 'expired' },
          }),
          {
            status,
            headers: { 'Content-Type': 'application/json' },
          },
        )
      }
      return new Response(null, { status })
    }),
  )
}

afterEach(() => {
  vi.unstubAllGlobals()
  configureApiClient({
    getToken: () => null,
    getTenant: () => null,
    onUnauthorized: () => {},
    refreshSession: undefined,
    getExpiresAt: undefined,
  })
  __resetRefreshState()
  globalTenant = 'tenant-A'
  sentTenants.length = 0
})

describe('production operations bind their tenant when they are created', () => {
  it('keeps tenant A through preventive refresh after the global tenant moves to B', async () => {
    const refresh = controlledRefresh()
    configureApiClient({
      getToken: () => 'olvs_test',
      getTenant: () => globalTenant,
      getExpiresAt: () => new Date(Date.now() + 30_000).toISOString(),
      refreshSession: refresh.refresh,
    })
    respondWith([204])

    const mutation = mutationCreatedIn('tenant-A')
    globalTenant = 'tenant-B'
    const pending = mutation()
    await refresh.started

    // Positive controls: this request really is suspended in the preventive
    // refresh window and has not reached the network early.
    expect(refresh.calls()).toBe(1)
    expect(sentTenants).toEqual([])

    refresh.release()
    await pending

    expect(sentTenants).toEqual(['tenant-A'])
  })

  it('replays a 401 in tenant A even while the global tenant keeps changing', async () => {
    const refresh = controlledRefresh()
    configureApiClient({
      getToken: () => 'olvs_test',
      getTenant: () => globalTenant,
      refreshSession: refresh.refresh,
    })
    respondWith([401, 204])

    const mutation = mutationCreatedIn('tenant-A')
    globalTenant = 'tenant-B'
    const pending = mutation()
    await refresh.started

    // The 401 leg happened and the replay has not happened yet.
    expect(refresh.calls()).toBe(1)
    expect(sentTenants).toEqual(['tenant-A'])

    globalTenant = 'tenant-C'
    refresh.release()
    await pending

    expect(sentTenants).toEqual(['tenant-A', 'tenant-A'])
  })
})
