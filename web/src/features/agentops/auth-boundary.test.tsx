// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The authority boundary of the provider plane follows the session lifecycle's CREDENTIAL
// GENERATION, not the session id. `POST /v1/auth/refresh` rotates the bearer in place and
// keeps the session id, so a boundary watching the id slept through every real renewal
// (independent review of d5cf66ed, C3). What is measured here: which transitions move the
// boundary, what happens to the previous scope when it moves, and that nothing secret —
// no bearer, no session id — ever reaches a key or the boundary's own string.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { act, renderHook, waitFor } from '@testing-library/react'
import type { ReactNode } from 'react'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import { useSessionStore } from '@/stores/session'

const auth = vi.hoisted(() => ({
  tenant: 't1' as string | null,
  principal: 'u1',
}))
vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({
    activeTenant: auth.tenant,
    can: () => true,
    isSuperadmin: false,
    principal: { user_id: auth.principal, aal: 1 },
  }),
}))

import { agentOpsKeys } from './api'
import { useAuthBoundary } from './auth-boundary'

const EXP = '2030-01-01T00:00:00Z'
const SID = 'sid-fixed'
const setSession = (token: string, sessionId = SID, expiresAt = EXP) =>
  useSessionStore.getState().setSession({ token, sessionId, expiresAt })

function mount() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  const hook = renderHook(() => useAuthBoundary(), {
    wrapper: ({ children }: { children: ReactNode }) => (
      <QueryClientProvider client={qc}>{children}</QueryClientProvider>
    ),
  })
  return { qc, hook }
}

/** A read left pending under the given scope, with the signal TanStack handed it. */
async function pendingRead(
  qc: QueryClient,
  tenant: string | null,
  epoch: number,
) {
  let signal: AbortSignal | undefined
  const queryKey = agentOpsKeys.profiles(tenant, epoch, { state: 'active' })
  qc.fetchQuery({
    queryKey,
    queryFn: ({ signal: s }) => {
      signal = s
      return new Promise<never>(() => {})
    },
  }).catch(() => {})
  await waitFor(() => expect(signal).toBeDefined())
  return { queryKey, aborted: () => signal?.aborted === true }
}

beforeEach(() => {
  auth.tenant = 't1'
  auth.principal = 'u1'
  useSessionStore.setState({
    token: 'olvs_first',
    sessionId: SID,
    expiresAt: EXP,
  })
})

describe('useAuthBoundary — the credential generation moves the boundary', () => {
  it('a renewal that rotates the bearer under the SAME session id is a new boundary: old scope cancelled and removed, keys and key opaque', async () => {
    const { qc, hook } = mount()
    const first = hook.result.current
    const read = await pendingRead(qc, 't1', first.epoch)
    act(() => {
      setSession('olvs_ROTATED')
    })
    hook.rerender()
    const next = hook.result.current
    expect(useSessionStore.getState().sessionId).toBe(SID)
    expect(next.epoch).not.toBe(first.epoch)
    expect(next.key).not.toBe(first.key)
    expect(next.credentialGeneration).toBe(first.credentialGeneration + 1)
    expect(next.principal).toBe('u1')
    expect(next.tenant).toBe('t1')
    // The previous boundary's read was aborted and its entry removed.
    await waitFor(() => expect(read.aborted()).toBe(true))
    expect(qc.getQueryCache().find({ queryKey: read.queryKey })).toBeUndefined()
    // Nothing secret anywhere: not in the boundary's own string, not in a key.
    for (const s of [first.key, next.key]) {
      expect(s).toMatch(/^u1\|t1\|c\d+$/)
      expect(s).not.toContain('olvs')
      expect(s).not.toContain(SID)
    }
    const keys = JSON.stringify(
      qc
        .getQueryCache()
        .findAll()
        .map((q) => q.queryKey),
    )
    expect(keys).not.toContain('olvs')
    expect(keys).not.toContain(SID)
    expect(typeof next.epoch).toBe('number')
  })

  it.each([
    ['a new session id', () => setSession('olvs_other', 'sid-other')],
    ['a logout', () => useSessionStore.getState().clear()],
    ['another principal', () => (auth.principal = 'u2')],
    ['another tenant', () => (auth.tenant = 't2')],
  ])('%s moves it too, and ends the previous scope', async (_what, move) => {
    const { qc, hook } = mount()
    const first = hook.result.current
    const read = await pendingRead(qc, 't1', first.epoch)
    act(() => {
      move()
    })
    hook.rerender()
    expect(hook.result.current.epoch).not.toBe(first.epoch)
    await waitFor(() => expect(read.aborted()).toBe(true))
    expect(qc.getQueryCache().find({ queryKey: read.queryKey })).toBeUndefined()
  })

  it('a renewal answer repeating the same bearer and session id (only a later expiry) does not move it', async () => {
    const { qc, hook } = mount()
    const first = hook.result.current
    const read = await pendingRead(qc, 't1', first.epoch)
    act(() => {
      setSession('olvs_first', SID, '2030-06-01T00:00:00Z')
    })
    hook.rerender()
    expect(hook.result.current.epoch).toBe(first.epoch)
    expect(hook.result.current.key).toBe(first.key)
    expect(read.aborted()).toBe(false)
    expect(qc.getQueryCache().find({ queryKey: read.queryKey })).toBeDefined()
  })

  it('ends only the previous boundary’s scope of THIS plane, never another tenant’s entries', async () => {
    const { qc, hook } = mount()
    const first = hook.result.current
    const mine = await pendingRead(qc, 't1', first.epoch)
    const other = await pendingRead(qc, 't-other', 999)
    qc.setQueryData(['elsewhere', 't1'], { untouched: true })
    act(() => {
      setSession('olvs_ROTATED_2')
    })
    hook.rerender()
    await waitFor(() => expect(mine.aborted()).toBe(true))
    expect(other.aborted()).toBe(false)
    expect(qc.getQueryCache().find({ queryKey: other.queryKey })).toBeDefined()
    expect(qc.getQueryData(['elsewhere', 't1'])).toEqual({ untouched: true })
  })
})
