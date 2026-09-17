// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE TWO TRANSPORT GAPS a permission check at dispatch cannot see, measured with the
// REAL client (`lib/api/client.ts`): the preventive refresh it awaits BEFORE the first
// fetch, and the ONE replay it makes after a 401. In both, the authority can move while
// the client is waiting, and a mutation that then left would leave with the NEXT
// credential. The guard hands every dispatch an AbortSignal bound to the boundary and
// the permission; moving either aborts it, and `fetch()` with an aborted signal sends
// nothing. Positive controls prove the same request DOES go out when nothing moved.
import { act, renderHook } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { __resetRefreshState, configureApiClient } from '@/lib/api/client'
import { sendNotice } from './api'
import { buildSendIntent, useIntentGuard } from './intent'

/** Counts only the sends that actually LEFT: a fetch handed an aborted signal
 * rejects like the platform does and is not a network send. */
let networkSends = 0
let statuses: number[] = []
function stubFetch() {
  vi.stubGlobal(
    'fetch',
    vi.fn(async (_url: string, init?: RequestInit) => {
      if (init?.signal?.aborted) throw new DOMException('aborted', 'AbortError')
      networkSends++
      const status = statuses.shift() ?? 201
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
      return new Response(JSON.stringify({ replayed: false }), {
        status,
        headers: { 'Content-Type': 'application/json' },
      })
    }),
  )
}

function controlledRefresh() {
  let started!: () => void
  let release!: (ok: boolean) => void
  const startedP = new Promise<void>((r) => (started = r))
  const gate = new Promise<boolean>((r) => (release = r))
  let calls = 0
  return {
    refresh: () => {
      calls++
      started()
      return gate
    },
    started: startedP,
    release: (ok = true) => release(ok),
    calls: () => calls,
  }
}

const SCOPE = { tenant: 't1', workspace: 'ws1', boundary: 'b1' }
const intent = () =>
  buildSendIntent(SCOPE, 'c1', {
    channel_id: 'c1',
    recipient: { kind: 'user', ref: 'u2' },
    content: { subject: 's', blocks: [{ type: 'text', text: 'x' }] },
  })

const soon = () => new Date(Date.now() + 10_000).toISOString()
const far = () => new Date(Date.now() + 3_600_000).toISOString()

beforeEach(() => {
  networkSends = 0
  statuses = []
  __resetRefreshState()
  stubFetch()
})
afterEach(() => {
  vi.unstubAllGlobals()
})

describe('useIntentGuard closes the gaps of the shared transport', () => {
  it('a boundary that moves during the PREVENTIVE refresh: the send never leaves (0 network sends)', async () => {
    const r = controlledRefresh()
    configureApiClient({
      getToken: () => 'olvs_a',
      getTenant: () => 't1',
      onUnauthorized: () => {},
      refreshSession: r.refresh,
      getExpiresAt: soon,
    })
    const hook = renderHook(
      ({ boundary }) => useIntentGuard({ allowed: true, boundary }),
      { initialProps: { boundary: 'b1' } },
    )
    const signal = hook.result.current.begin()
    expect(signal).not.toBeNull()
    const outcome = sendNotice(intent(), { tenant: 't1' }, signal!)
    const settled = outcome.then(
      () => 'resolved' as const,
      (e: unknown) => (e instanceof DOMException ? e.name : 'other'),
    )
    await r.started
    expect(r.calls()).toBe(1)
    // The authority moves while the client is waiting on the refresh.
    act(() => {
      hook.rerender({ boundary: 'b2' })
    })
    expect(signal!.aborted).toBe(true)
    r.release(true)
    expect(await settled).toBe('AbortError')
    expect(networkSends).toBe(0)
    expect(hook.result.current.alive()).toBe(false)
  })

  it('POSITIVE CONTROL: the same refresh with nothing moving sends exactly once', async () => {
    const r = controlledRefresh()
    configureApiClient({
      getToken: () => 'olvs_a',
      getTenant: () => 't1',
      onUnauthorized: () => {},
      refreshSession: r.refresh,
      getExpiresAt: soon,
    })
    const hook = renderHook(() =>
      useIntentGuard({ allowed: true, boundary: 'b1' }),
    )
    const signal = hook.result.current.begin()!
    const outcome = sendNotice(intent(), { tenant: 't1' }, signal)
    await r.started
    r.release(true)
    await expect(outcome).resolves.toMatchObject({
      replayed: false,
      status: 201,
    })
    expect(networkSends).toBe(1)
  })

  it('a boundary that moves during the 401 REPLAY refresh: the replay never leaves (1 network send, not 2)', async () => {
    const r = controlledRefresh()
    statuses = [401, 201]
    configureApiClient({
      getToken: () => 'olvs_a',
      getTenant: () => 't1',
      onUnauthorized: () => {},
      refreshSession: r.refresh,
      getExpiresAt: far,
    })
    const hook = renderHook(
      ({ boundary }) => useIntentGuard({ allowed: true, boundary }),
      { initialProps: { boundary: 'b1' } },
    )
    const signal = hook.result.current.begin()!
    const outcome = sendNotice(intent(), { tenant: 't1' }, signal)
    const settled = outcome.then(
      () => 'resolved' as const,
      (e: unknown) => (e instanceof DOMException ? e.name : 'other'),
    )
    await r.started
    expect(networkSends).toBe(1)
    act(() => {
      hook.rerender({ boundary: 'b2' })
    })
    r.release(true)
    expect(await settled).toBe('AbortError')
    expect(networkSends).toBe(1)
  })

  it('POSITIVE CONTROL: the 401 replay with nothing moving sends twice and succeeds', async () => {
    const r = controlledRefresh()
    statuses = [401, 201]
    configureApiClient({
      getToken: () => 'olvs_a',
      getTenant: () => 't1',
      onUnauthorized: () => {},
      refreshSession: r.refresh,
      getExpiresAt: far,
    })
    const hook = renderHook(() =>
      useIntentGuard({ allowed: true, boundary: 'b1' }),
    )
    const signal = hook.result.current.begin()!
    const outcome = sendNotice(intent(), { tenant: 't1' }, signal)
    await r.started
    r.release(true)
    await expect(outcome).resolves.toMatchObject({ status: 201 })
    expect(networkSends).toBe(2)
  })

  it('a permission lost before dispatch refuses to begin; lost during the wait aborts the request', async () => {
    const r = controlledRefresh()
    configureApiClient({
      getToken: () => 'olvs_a',
      getTenant: () => 't1',
      onUnauthorized: () => {},
      refreshSession: r.refresh,
      getExpiresAt: soon,
    })
    const hook = renderHook(
      ({ allowed }) => useIntentGuard({ allowed, boundary: 'b1' }),
      { initialProps: { allowed: true } },
    )
    const signal = hook.result.current.begin()!
    const outcome = sendNotice(intent(), { tenant: 't1' }, signal)
    const settled = outcome.then(
      () => 'resolved' as const,
      (e: unknown) => (e instanceof DOMException ? e.name : 'other'),
    )
    await r.started
    act(() => {
      hook.rerender({ allowed: false })
    })
    // A queued click after the loss: begin() refuses, so nothing is even started.
    expect(hook.result.current.begin()).toBeNull()
    r.release(true)
    expect(await settled).toBe('AbortError')
    expect(networkSends).toBe(0)
  })

  it('unmounting the surface aborts what it had in flight', async () => {
    const r = controlledRefresh()
    configureApiClient({
      getToken: () => 'olvs_a',
      getTenant: () => 't1',
      onUnauthorized: () => {},
      refreshSession: r.refresh,
      getExpiresAt: soon,
    })
    const hook = renderHook(() =>
      useIntentGuard({ allowed: true, boundary: 'b1' }),
    )
    const signal = hook.result.current.begin()!
    const outcome = sendNotice(intent(), { tenant: 't1' }, signal).catch(
      (e: unknown) => (e instanceof DOMException ? e.name : 'other'),
    )
    await r.started
    hook.unmount()
    r.release(true)
    expect(await outcome).toBe('AbortError')
    expect(networkSends).toBe(0)
  })
})
