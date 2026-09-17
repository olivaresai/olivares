// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// R2 — AN OLD QUEUED INTENTION MUST NEVER LEAVE WITH A NEW CREDENTIAL OR SCOPE.
//
// The independent review of d8c5ad3e9e measured, in a real Chromium with the real
// client, hook, session store and React Query: `mutate(intent)` followed in the SAME
// turn by a credential rotation (same session id) sent ONE POST with the NEW credential
// before React's cleanup aborted the signal. The bytes had left; the AbortError came
// after. This file measures the closure of that gap on the real transport
// (`lib/api/client.ts`), with the real stores, and keeps the causal controls beside
// every claim: the same request DOES leave when nothing moved, and the same request
// DOES leave with the rotated credential when the dispatch guard is removed.
import {
  QueryClient,
  QueryClientProvider,
  useMutation,
} from '@tanstack/react-query'
import { render, renderHook } from '@testing-library/react'
import { readFileSync } from 'node:fs'
import path from 'node:path'
import { useLayoutEffect } from 'react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { AuthorityLostError } from '@/features/agentops/auth-boundary'
import { __resetRefreshState, configureApiClient, http } from '@/lib/api/client'
import { ApiError, NetworkError } from '@/lib/api/errors'
import { queryKeys } from '@/lib/api/query'
import { useSessionStore } from '@/stores/session'
import { useTenantStore } from '@/stores/tenant'
import { useWorkspaceStore } from '@/stores/workspace'
import {
  ackDelivery,
  advanceCursor,
  createChannel,
  getCursorToken,
  grantChannel,
  offerHandoff,
  respondToHandoff,
  revokeChannelGrant,
  sendNotice,
  updateChannel,
} from './api'
import {
  buildAckIntent,
  buildCreateIntent,
  buildCursorIntent,
  buildGrantIntent,
  buildHandoffOfferIntent,
  buildHandoffResponseIntent,
  buildRevokeIntent,
  buildSendIntent,
  buildUpdateChannelIntent,
  dispatchGuardFor,
  snapshotAuthority,
  StaleIntentError,
  useIntentGuard,
  type IntentGuard,
  type SendIntent,
} from './intent'
import { USER_A, USER_B, WS, WS2 } from './test-harness'

// ─── the wire, counted the way a server counts it ────────────────────────────────
interface Sent {
  authorization: string | null
  tenant: string | null
  path: string
}
let sent: Sent[] = []
let statuses: number[] = []
let events: string[] = []
function stubFetch() {
  vi.stubGlobal(
    'fetch',
    vi.fn(async (url: string, init?: RequestInit) => {
      if (init?.signal?.aborted) throw new DOMException('aborted', 'AbortError')
      const h = new Headers(init?.headers)
      sent.push({
        authorization: h.get('Authorization'),
        tenant: h.get('X-Olivares-Tenant'),
        path: url,
      })
      events.push('fetch')
      const status = statuses.shift() ?? 201
      const body =
        status === 401
          ? { error: { code: 'unauthenticated', message: 'expired' } }
          : { replayed: false }
      return new Response(JSON.stringify(body), {
        status,
        headers: { 'Content-Type': 'application/json' },
      })
    }),
  )
}

const TOKEN_A = 'synthetic-credential-A'
const TOKEN_B = 'synthetic-credential-B'
const far = () => new Date(Date.now() + 3_600_000).toISOString()
const soon = () => new Date(Date.now() + 10_000).toISOString()
function install(token: string) {
  useSessionStore
    .getState()
    .setSession({ token, sessionId: 'same-session', expiresAt: far() })
}
/** The production wiring of providers.tsx: getters over the live stores. */
function wireClient(over: {
  refreshSession?: () => Promise<boolean>
  getExpiresAt?: () => string | null
}) {
  configureApiClient({
    getToken: () => useSessionStore.getState().token,
    getTenant: () => useTenantStore.getState().activeTenant,
    onUnauthorized: () => {},
    refreshSession: over.refreshSession ?? (async () => false),
    getExpiresAt: over.getExpiresAt ?? far,
  })
}
function controlledRefresh(rotateTo?: string) {
  let started!: () => void
  let release!: () => void
  const startedP = new Promise<void>((r) => (started = r))
  const gate = new Promise<void>((r) => (release = r))
  return {
    refresh: async () => {
      started()
      await gate
      if (rotateTo) install(rotateTo)
      return true
    },
    started: startedP,
    release,
  }
}
const outcomeOf = (p: Promise<unknown>) =>
  p.then(
    () => 'resolved' as const,
    (e: unknown) => (e instanceof Error ? e.name : 'other'),
  )
const noticeBody = (subject: string) => ({
  channel_id: 'c1',
  recipient: { kind: 'user' as const, ref: USER_B },
  content: {
    subject,
    blocks: [{ type: 'text' as const, format: 'plain' as const, text: 'x' }],
  },
})
const scopeNow = () => ({
  tenant: useTenantStore.getState().activeTenant,
  workspace: useWorkspaceStore.getState().activeWorkspace ?? WS,
  boundary: `${USER_A}|t1|c${useSessionStore.getState().credentialGeneration}|w:${WS}`,
})

beforeEach(() => {
  sent = []
  statuses = []
  events = []
  __resetRefreshState()
  stubFetch()
  useSessionStore.getState().clear()
  install(TOKEN_A)
  useTenantStore.getState().setActiveTenant('t1')
  useWorkspaceStore.getState().setActiveWorkspace(WS, 'Billing')
  wireClient({})
})
afterEach(() => {
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
  useSessionStore.getState().clear()
  useTenantStore.getState().clear()
  useWorkspaceStore.getState().clear()
})

// ─── 1. the transport seam itself ──────────────────────────────────────────────────
describe('RequestOptions.dispatchGuard on the real client', () => {
  it('runs synchronously before EACH fetch — the first send and the 401 replay — after the refresh', async () => {
    statuses = [401, 201]
    const r = controlledRefresh()
    wireClient({ refreshSession: r.refresh })
    const guard = vi.fn(() => {
      events.push('guard')
    })
    const out = http.postWithMeta('/v1/x', { a: 1 }, { dispatchGuard: guard })
    await r.started
    events.push('refresh-released')
    r.release()
    await expect(out).resolves.toMatchObject({ status: 201 })
    expect(events).toEqual([
      'guard',
      'fetch',
      'refresh-released',
      'guard',
      'fetch',
    ])
    expect(guard).toHaveBeenCalledTimes(2)
  })

  it('runs AFTER the preventive refresh is awaited, and its throw propagates unwrapped with zero fetches', async () => {
    const r = controlledRefresh()
    wireClient({ refreshSession: r.refresh, getExpiresAt: soon })
    class Refusal extends Error {}
    let calls = 0
    const out = http.postWithMeta(
      '/v1/x',
      { a: 1 },
      {
        dispatchGuard: () => {
          calls++
          events.push('guard')
          throw new Refusal('no')
        },
      },
    )
    const settled = out.then(
      () => 'resolved',
      (e: unknown) => e,
    )
    await r.started
    expect(calls).toBe(0) // not yet: the client is still waiting on the refresh
    events.push('refresh-released')
    r.release()
    const err = await settled
    expect(err).toBeInstanceOf(Refusal)
    expect(err).not.toBeInstanceOf(NetworkError)
    expect(err).not.toBeInstanceOf(ApiError)
    expect(events).toEqual(['refresh-released', 'guard'])
    expect(sent).toHaveLength(0)
  })

  it('a refusal on the REPLAY leaves exactly the first send on the wire', async () => {
    statuses = [401, 201]
    let calls = 0
    const settled = outcomeOf(
      http.postWithMeta(
        '/v1/x',
        { a: 1 },
        {
          dispatchGuard: () => {
            if (++calls === 2) throw new StaleIntentError('credential')
          },
        },
      ),
    )
    wireClient({ refreshSession: async () => true })
    expect(await settled).toBe('StaleIntentError')
    expect(sent).toHaveLength(1)
  })

  it('POSITIVE CONTROL: a request without a guard is untouched (one send, resolved)', async () => {
    await expect(http.post('/v1/x', { a: 1 })).resolves.toEqual({
      replayed: false,
    })
    expect(sent).toHaveLength(1)
    expect(sent[0].authorization).toBe(`Bearer ${TOKEN_A}`)
  })

  it('SOURCE ORDER: the guard call sits after the awaited refresh and the header composition, immediately before the fetch, once', () => {
    const src = readFileSync(
      path.resolve(process.cwd(), 'src/lib/api/client.ts'),
      'utf8',
    )
    const fn = src.slice(src.indexOf('export async function apiFetchWithMeta'))
    const refresh = fn.indexOf('await refreshOnce()')
    const bearer = fn.indexOf("headers.set('Authorization'")
    const guard = fn.indexOf('opts.dispatchGuard?.()')
    const fetchAt = fn.indexOf('res = await fetch(')
    const replay = fn.indexOf('return apiFetchWithMeta<T>(path, opts, true)')
    expect(refresh).toBeGreaterThan(-1)
    expect(guard).toBeGreaterThan(refresh)
    expect(guard).toBeGreaterThan(bearer)
    expect(fetchAt).toBeGreaterThan(guard)
    // Nothing awaited between the guard and the fetch.
    expect(fn.slice(guard, fetchAt)).not.toMatch(/await /)
    // The replay re-enters with the SAME opts, so the same guard runs again.
    expect(replay).toBeGreaterThan(fetchAt)
    expect(fn.split('opts.dispatchGuard?.()').length - 1).toBe(1)
  })
})

// ─── 2. the intent's frozen authority against the live stores ──────────────────────
describe('an I1 intent carries the authority it was confirmed under, and the transport checks it', () => {
  const moves: Array<[string, () => void]> = [
    ['same-session credential rotation', () => install(TOKEN_B)],
    ['tenant', () => useTenantStore.getState().setActiveTenant('t2')],
    ['workspace', () => useWorkspaceStore.getState().setActiveWorkspace(WS2)],
  ]

  it.each(moves)(
    'a %s during the PREVENTIVE refresh, with NO signal and NO surface guard: zero fetches, StaleIntentError',
    async (_what, move) => {
      const intent = buildSendIntent(scopeNow(), 'c1', noticeBody('s'))
      const r = controlledRefresh()
      wireClient({ refreshSession: r.refresh, getExpiresAt: soon })
      const settled = outcomeOf(sendNotice(intent, { tenant: 't1' }))
      await r.started
      move()
      r.release()
      expect(await settled).toBe('StaleIntentError')
      expect(sent).toHaveLength(0)
    },
  )

  it.each(moves)(
    'a %s during the 401-REPLAY refresh: the replay never leaves (1 send, never with the new credential)',
    async (_what, move) => {
      statuses = [401, 201]
      const intent = buildSendIntent(scopeNow(), 'c1', noticeBody('s'))
      const r = controlledRefresh()
      wireClient({ refreshSession: r.refresh })
      const settled = outcomeOf(sendNotice(intent, { tenant: 't1' }))
      await r.started
      expect(sent).toHaveLength(1)
      move()
      r.release()
      expect(await settled).toBe('StaleIntentError')
      expect(sent).toHaveLength(1)
      expect(sent[0].authorization).toBe(`Bearer ${TOKEN_A}`)
    },
  )

  it('POSITIVE CONTROLS: nothing moving — preventive refresh sends once, 401 replay sends twice', async () => {
    const r1 = controlledRefresh()
    wireClient({ refreshSession: r1.refresh, getExpiresAt: soon })
    const one = sendNotice(buildSendIntent(scopeNow(), 'c1', noticeBody('s')), {
      tenant: 't1',
    })
    await r1.started
    r1.release()
    await expect(one).resolves.toMatchObject({ status: 201 })
    expect(sent).toHaveLength(1)

    sent = []
    statuses = [401, 201]
    const r2 = controlledRefresh()
    wireClient({ refreshSession: r2.refresh })
    const two = sendNotice(buildSendIntent(scopeNow(), 'c1', noticeBody('s')), {
      tenant: 't1',
    })
    await r2.started
    r2.release()
    await expect(two).resolves.toMatchObject({ status: 201 })
    expect(sent).toHaveLength(2)
  })

  it('the SAME guard reaches create and Ack: a rotation during the refresh refuses both with zero fetches', async () => {
    const r = controlledRefresh()
    wireClient({ refreshSession: r.refresh, getExpiresAt: soon })
    const create = outcomeOf(
      createChannel(
        buildCreateIntent(scopeNow(), {
          workspace_id: WS,
          slug: 'ops',
          name: 'Ops',
          kind: 'coordination',
          initial_grants: [],
        }),
        { tenant: 't1' },
      ),
    )
    await r.started
    install(TOKEN_B)
    r.release()
    expect(await create).toBe('StaleIntentError')
    __resetRefreshState()
    const r2 = controlledRefresh()
    wireClient({ refreshSession: r2.refresh, getExpiresAt: soon })
    const ack = outcomeOf(
      ackDelivery(buildAckIntent(scopeNow(), 'd1', 1), { tenant: 't1' }),
    )
    await r2.started
    install(TOKEN_A)
    r2.release()
    expect(await ack).toBe('StaleIntentError')
    expect(sent).toHaveLength(0)
  })

  // ─── I2: the four new intents ride the same guard ───────────────────────────────
  const i2Dispatches: Array<
    [string, () => Promise<unknown>, (typeof http)[keyof typeof http] | null]
  > = [
    [
      'PATCH /channels (updateChannel)',
      () =>
        updateChannel(
          buildUpdateChannelIntent(scopeNow(), 'c1', '"v1"', {
            channel_id: 'c1',
            name: 'Renamed',
          }),
          { tenant: 't1' },
        ),
      null,
    ],
    [
      'POST /channels/{id}/grants (grantChannel)',
      () =>
        grantChannel(
          buildGrantIntent(scopeNow(), 'c1', '"v1"', {
            subject: { kind: 'user', ref: USER_B },
            can_read: true,
            can_write: false,
            can_admin: false,
          }),
          { tenant: 't1' },
        ),
      null,
    ],
    [
      'POST /channels/{id}/grants/{gid}/revoke (revokeChannelGrant)',
      () =>
        revokeChannelGrant(buildRevokeIntent(scopeNow(), 'c1', 'g1', '"v1"'), {
          tenant: 't1',
        }),
      null,
    ],
    [
      'PUT /inbox/cursors/personal/{recipient} (advanceCursor)',
      () =>
        advanceCursor(
          buildCursorIntent(
            scopeNow(),
            USER_A,
            { cursor: 'c2v2.t', version: 0, etag: '"v0"' },
            'd1',
          ),
          { tenant: 't1' },
        ),
      null,
    ],
  ]
  it.each(i2Dispatches)(
    'I2 — %s: a same-session credential rotation during the PREVENTIVE refresh sends ZERO bytes (StaleIntentError)',
    async (_what, dispatch) => {
      const r = controlledRefresh()
      wireClient({ refreshSession: r.refresh, getExpiresAt: soon })
      const settled = outcomeOf(dispatch())
      await r.started
      install(TOKEN_B)
      r.release()
      expect(await settled).toBe('StaleIntentError')
      expect(sent).toHaveLength(0)
    },
  )
  it.each(i2Dispatches)(
    'I2 — %s: a workspace move between 401 and its replay leaves exactly ONE send, never with the new scope',
    async (_what, dispatch) => {
      statuses = [401, 200]
      const r = controlledRefresh()
      wireClient({ refreshSession: r.refresh })
      const settled = outcomeOf(dispatch())
      await r.started
      expect(sent).toHaveLength(1)
      useWorkspaceStore.getState().setActiveWorkspace(WS2)
      r.release()
      expect(await settled).toBe('StaleIntentError')
      expect(sent).toHaveLength(1)
      expect(sent[0].authorization).toBe(`Bearer ${TOKEN_A}`)
    },
  )
  it.each(i2Dispatches)(
    'I2 POSITIVE CONTROL — %s: with nothing moving the request leaves once with the original credential',
    async (_what, dispatch) => {
      statuses = [200]
      const r = controlledRefresh()
      wireClient({ refreshSession: r.refresh, getExpiresAt: soon })
      const settled = outcomeOf(dispatch())
      await r.started
      r.release()
      expect(await settled).toBe('resolved')
      expect(sent).toHaveLength(1)
      expect(sent[0].authorization).toBe(`Bearer ${TOKEN_A}`)
    },
  )

  // ─── IR-I2-1: the cursor PREPARATION is a guarded dispatch too ─────────────────
  //
  // The independent review measured this GET leaving with a ROTATED credential: the
  // caller passed only `{tenant}` and a signal, so `getWithMeta` set no
  // `dispatchGuard`, and the client's preventive refresh gave the rotation a window
  // the abort effect cannot close. These cases hold both directions at that exact
  // caller, with the real client and the real stores.
  const prepareCursor = (guard?: () => void) =>
    getCursorToken(
      USER_A,
      { workspace_id: WS, target: 'c2n1.target' },
      // The cast is deliberate and is itself part of the evidence: `guard` is
      // REQUIRED on `PreparationOptions`, so the unguarded shape the review found
      // no longer type-checks and can only be reached by forcing it here.
      { tenant: 't1', guard } as { tenant: string; guard: () => void },
    )
  const cursorMoves: Array<[string, () => void]> = [
    ['same-session credential rotation', () => install(TOKEN_B)],
    ['tenant', () => useTenantStore.getState().setActiveTenant('t2')],
    ['workspace', () => useWorkspaceStore.getState().setActiveWorkspace(WS2)],
  ]
  it.each(cursorMoves)(
    'IR-I2-1 — cursor preparation: a %s during the PREVENTIVE refresh sends ZERO bytes',
    async (_what, move) => {
      const authority = snapshotAuthority()
      const r = controlledRefresh()
      wireClient({ refreshSession: r.refresh, getExpiresAt: soon })
      const settled = outcomeOf(prepareCursor(dispatchGuardFor({ authority })))
      await r.started
      move()
      r.release()
      expect(await settled).toBe('StaleIntentError')
      expect(sent).toHaveLength(0)
    },
  )

  it('IR-I2-1 — cursor preparation: a rotation between the 401 and its replay leaves exactly ONE request, never with the new credential', async () => {
    statuses = [401, 200]
    const authority = snapshotAuthority()
    const r = controlledRefresh()
    wireClient({ refreshSession: r.refresh })
    const settled = outcomeOf(prepareCursor(dispatchGuardFor({ authority })))
    await r.started
    expect(sent).toHaveLength(1)
    install(TOKEN_B)
    r.release()
    expect(await settled).toBe('StaleIntentError')
    expect(sent).toHaveLength(1)
    expect(sent[0].authorization).toBe(`Bearer ${TOKEN_A}`)
  })

  it('IR-I2-1 POSITIVE CONTROL — cursor preparation: with nothing moving the normal GET leaves once with the original credential', async () => {
    statuses = [200]
    const authority = snapshotAuthority()
    const r = controlledRefresh()
    wireClient({ refreshSession: r.refresh, getExpiresAt: soon })
    const settled = outcomeOf(prepareCursor(dispatchGuardFor({ authority })))
    await r.started
    r.release()
    expect(await settled).toBe('resolved')
    expect(sent).toHaveLength(1)
    expect(sent[0].authorization).toBe(`Bearer ${TOKEN_A}`)
    expect(sent[0].path).toContain('/v1/m/sessions/inbox/cursors/personal/')
  })

  it("IR-I2-1 MISSING-GUARD WITNESS (the reviewer's case): a preparation forced through WITHOUT a guard sends the GET with the ROTATED credential", async () => {
    // The reviewer's witness, reproduced through the caller's own signature. It is
    // what `inbox-table.tsx` used to do; production can no longer express it,
    // because `PreparationOptions.guard` is required — only the cast above reaches
    // this outcome, and it is kept so the defect stays legible.
    statuses = [200]
    const r = controlledRefresh()
    wireClient({ refreshSession: r.refresh, getExpiresAt: soon })
    const settled = outcomeOf(prepareCursor(undefined))
    await r.started
    install(TOKEN_B)
    r.release()
    expect(await settled).toBe('resolved')
    expect(sent).toHaveLength(1)
    expect(sent[0].authorization).toBe(`Bearer ${TOKEN_B}`)
  })

  it('IR-I2-1 MISSING-GUARD MUTANT at the seam (causal control): with `dispatchGuard` stripped from getWithMeta, the SAME guarded preparation sends the GET with the ROTATED credential', async () => {
    // The mutant is the delivered defect, expressed at the transport seam: the
    // caller still composes and passes its guard, and the client simply never runs
    // it. If this stayed green with the guard restored, the guard would be
    // decoration; it goes red, so the guard is what stops the request.
    statuses = [200]
    const real = http.getWithMeta
    vi.spyOn(http, 'getWithMeta').mockImplementation((path, opts) => {
      const { dispatchGuard: _dropped, ...rest } = opts ?? {}
      void _dropped
      return real(path, rest)
    })
    const authority = snapshotAuthority()
    const r = controlledRefresh()
    wireClient({ refreshSession: r.refresh, getExpiresAt: soon })
    const settled = outcomeOf(prepareCursor(dispatchGuardFor({ authority })))
    await r.started
    install(TOKEN_B)
    r.release()
    expect(await settled).toBe('resolved')
    expect(sent).toHaveLength(1)
    expect(sent[0].authorization).toBe(`Bearer ${TOKEN_B}`)
  })

  it('IR-I2-1 — the preparation guard re-evaluates BOTH permissions live: losing delivery:read or delivery:write refuses it', () => {
    const qc = new QueryClient()
    const seed = (permissions: string[]) =>
      qc.setQueryData(queryKeys.whoami, {
        kind: 'user',
        user_id: USER_A,
        actor: `user:${USER_A}`,
        display_name: 'A',
        superadmin: false,
        grants: [{ tenant: 't1', role: 'editor', permissions }],
      })
    seed(['sessions:delivery:read', 'sessions:delivery:write'])
    const hook = renderHook(
      () =>
        useIntentGuard({
          allowed: true,
          boundary: `${USER_A}|t1|c0|w:${WS}`,
          permission: 'sessions:delivery:write',
          alsoRequires: 'sessions:delivery:read',
        }),
      {
        wrapper: ({ children }) => (
          <QueryClientProvider client={qc}>{children}</QueryClientProvider>
        ),
      },
    )
    // Both held: the guard lets a dispatch begin.
    expect(hook.result.current.begin()).not.toBeNull()
    // The WRITE goes: refused.
    seed(['sessions:delivery:read'])
    expect(() => hook.result.current.check()).toThrow(StaleIntentError)
    // The READ goes: refused too — this is the half the delivered guard never saw.
    seed(['sessions:delivery:write'])
    expect(() => hook.result.current.check()).toThrow(StaleIntentError)
    seed(['sessions:delivery:read', 'sessions:delivery:write'])
    expect(hook.result.current.begin()).not.toBeNull()
    hook.unmount()
  })

  it('MISSING-GUARD MUTANT (causal control): with the dispatch guard stripped at the seam, the same rotation sends ONE POST with the ROTATED credential', async () => {
    // The mutant: the seam forgets `dispatchGuard`. Everything else is identical.
    const real = http.postWithMeta
    vi.spyOn(http, 'postWithMeta').mockImplementation((path, body, opts) => {
      const { dispatchGuard: _dropped, ...rest } = opts ?? {}
      void _dropped
      return real(path, body, rest)
    })
    const intent = buildSendIntent(scopeNow(), 'c1', noticeBody('s'))
    const r = controlledRefresh()
    wireClient({ refreshSession: r.refresh, getExpiresAt: soon })
    const settled = outcomeOf(sendNotice(intent, { tenant: 't1' }))
    await r.started
    install(TOKEN_B)
    r.release()
    expect(await settled).toBe('resolved')
    expect(sent).toHaveLength(1)
    expect(sent[0].authorization).toBe(`Bearer ${TOKEN_B}`)
  })

  it('a stale intent is a typed LOCAL cancellation: an AuthorityLostError naming only the fact that moved, no status, no request id', async () => {
    const intent = buildSendIntent(scopeNow(), 'c1', noticeBody('s'))
    install(TOKEN_B)
    const err = await sendNotice(intent, { tenant: 't1' }).catch((e) => e)
    expect(err).toBeInstanceOf(StaleIntentError)
    expect(err).toBeInstanceOf(AuthorityLostError)
    expect((err as StaleIntentError).moved).toBe('credential')
    expect(String((err as Error).message)).not.toContain(TOKEN_A)
    expect(String((err as Error).message)).not.toContain('same-session')
    expect(Object.keys(err as object)).not.toContain('status')
    expect(sent).toHaveLength(0)
  })
})

// ─── 3. the queued mutation — the reviewer's witness, on React Query ─────────────────
type Guarded = { guard: IntentGuard; mutate: (i: SendIntent) => void }
let current: Guarded | null = null
let settle: ((v: string) => void) | null = null
function QueuedSurface({
  generation,
  viaBegin,
}: {
  generation: number
  viaBegin: boolean
}) {
  const guard = useIntentGuard({
    allowed: true,
    boundary: `${USER_A}|t1|c${generation}|w:${WS}`,
    permission: 'sessions:message-send:write',
  })
  const m = useMutation({
    mutationFn: (i: SendIntent) => {
      if (viaBegin) {
        const signal = guard.begin()
        if (!signal) throw new AuthorityLostError()
        return sendNotice(
          i,
          { tenant: i.scope.tenant, guard: guard.check },
          signal,
        )
      }
      // No begin(), no signal, no surface guard: the transport is the only defence.
      return sendNotice(i, { tenant: i.scope.tenant })
    },
    onSuccess: () => settle?.('resolved'),
    onError: (e) => settle?.(e.name),
  })
  useLayoutEffect(() => {
    current = { guard, mutate: m.mutate }
  })
  return null
}
function KeyedByGeneration({ viaBegin }: { viaBegin: boolean }) {
  const generation = useSessionStore((s) => s.credentialGeneration)
  return (
    <QueuedSurface
      key={generation}
      generation={generation}
      viaBegin={viaBegin}
    />
  )
}
function mountQueued(viaBegin: boolean) {
  const qc = new QueryClient({
    defaultOptions: { mutations: { retry: false }, queries: { retry: false } },
  })
  qc.setQueryData(queryKeys.whoami, {
    kind: 'user',
    user_id: USER_A,
    actor: `user:${USER_A}`,
    display_name: 'A',
    superadmin: false,
    grants: [
      {
        tenant: 't1',
        role: 'editor',
        permissions: ['sessions:message-send:write'],
      },
    ],
  })
  const r = render(
    <QueryClientProvider client={qc}>
      <KeyedByGeneration viaBegin={viaBegin} />
    </QueryClientProvider>,
  )
  return { qc, unmount: r.unmount }
}

describe('queued mutate(intent) then a SAME-TURN rotation (the review witness)', () => {
  afterEach(() => {
    current = null
    settle = null
  })

  const rotations: Array<[string, () => void]> = [
    ['same-session credential', () => install(TOKEN_B)],
    ['tenant', () => useTenantStore.getState().setActiveTenant('t2')],
    ['workspace', () => useWorkspaceStore.getState().setActiveWorkspace(WS2)],
  ]

  for (const viaBegin of [true, false]) {
    it.each(rotations)(
      `mutate() then a %s move in the same turn (mutationFn ${viaBegin ? 'with begin()+surface guard' : 'straight to the transport'}): zero POST, typed refusal`,
      async (_what, move) => {
        const { unmount } = mountQueued(viaBegin)
        const intent = buildSendIntent(scopeNow(), 'c1', noticeBody('queued'))
        const settled = new Promise<string>((res) => (settle = res))
        current!.mutate(intent)
        // The store moves before React Query resumes the mutationFn and before any
        // React commit: no act, no flushSync, no forced rerender.
        move()
        const outcome = await settled
        expect(['StaleIntentError', 'AuthorityLostError']).toContain(outcome)
        expect(sent).toHaveLength(0)
        unmount()
      },
    )

    it(`POSITIVE CONTROL (${viaBegin ? 'with begin()' : 'straight to the transport'}): the same queued mutation with nothing moving sends once with the original credential`, async () => {
      const { unmount } = mountQueued(viaBegin)
      const intent = buildSendIntent(scopeNow(), 'c1', noticeBody('queued'))
      const settled = new Promise<string>((res) => (settle = res))
      current!.mutate(intent)
      expect(await settled).toBe('resolved')
      expect(sent).toHaveLength(1)
      expect(sent[0].authorization).toBe(`Bearer ${TOKEN_A}`)
      expect(sent[0].tenant).toBe('t1')
      unmount()
    })
  }

  it('a PRINCIPAL change in the same turn is refused by the surface guard (the whoami entry the auth context reads)', async () => {
    const { qc, unmount } = mountQueued(true)
    const intent = buildSendIntent(scopeNow(), 'c1', noticeBody('queued'))
    const settled = new Promise<string>((res) => (settle = res))
    current!.mutate(intent)
    qc.setQueryData(queryKeys.whoami, {
      kind: 'user',
      user_id: USER_B,
      actor: `user:${USER_B}`,
      display_name: 'B',
      superadmin: false,
      grants: [
        {
          tenant: 't1',
          role: 'editor',
          permissions: ['sessions:message-send:write'],
        },
      ],
    })
    expect(['StaleIntentError', 'AuthorityLostError']).toContain(await settled)
    expect(sent).toHaveLength(0)
    unmount()
  })

  it('a core PERMISSION that leaves the principal in the same turn is refused live (deny-closed on the RBAC rule, not on a prop)', async () => {
    const { qc, unmount } = mountQueued(true)
    const intent = buildSendIntent(scopeNow(), 'c1', noticeBody('queued'))
    const settled = new Promise<string>((res) => (settle = res))
    current!.mutate(intent)
    qc.setQueryData(queryKeys.whoami, {
      kind: 'user',
      user_id: USER_A,
      actor: `user:${USER_A}`,
      display_name: 'A',
      superadmin: false,
      grants: [{ tenant: 't1', role: 'viewer', permissions: [] }],
    })
    expect(['StaleIntentError', 'AuthorityLostError']).toContain(await settled)
    expect(sent).toHaveLength(0)
    unmount()
  })

  it('MISSING-GUARD MUTANT on the witness (causal control): with the seam forgetting the guard, the queued mutation sends ONE POST with the NEW credential', async () => {
    const real = http.postWithMeta
    vi.spyOn(http, 'postWithMeta').mockImplementation((path, body, opts) => {
      const { dispatchGuard: _dropped, ...rest } = opts ?? {}
      void _dropped
      return real(path, body, rest)
    })
    const { unmount } = mountQueued(false)
    const intent = buildSendIntent(scopeNow(), 'c1', noticeBody('queued'))
    const settled = new Promise<string>((res) => (settle = res))
    current!.mutate(intent)
    install(TOKEN_B)
    expect(await settled).toBe('resolved')
    expect(sent).toHaveLength(1)
    expect(sent[0].authorization).toBe(`Bearer ${TOKEN_B}`)
    unmount()
  })
})

// ─── 4. an unmounted surface, a retained callback ───────────────────────────────────
describe('a retained guard of an unmounted surface cannot resurrect a dispatch', () => {
  it('after unmount begin() is null, check() throws, and a retained send with the surface guard makes zero fetches', async () => {
    const qc = new QueryClient()
    const hook = renderHook(
      () =>
        useIntentGuard({
          allowed: true,
          boundary: `${USER_A}|t1|c0|w:${WS}`,
        }),
      {
        wrapper: ({ children }) => (
          <QueryClientProvider client={qc}>{children}</QueryClientProvider>
        ),
      },
    )
    const retained = hook.result.current
    const intent = buildSendIntent(scopeNow(), 'c1', noticeBody('late'))
    expect(retained.begin()).not.toBeNull()
    hook.unmount()
    expect(retained.begin()).toBeNull()
    expect(() => retained.check()).toThrow(StaleIntentError)
    expect(retained.alive()).toBe(false)
    const late = await outcomeOf(
      sendNotice(intent, { tenant: 't1', guard: retained.check }),
    )
    expect(late).toBe('StaleIntentError')
    expect(sent).toHaveLength(0)
  })

  it('a boundary that moved between confirmation and dispatch: begin() refuses without minting a controller, before any effect ran', () => {
    const hook = renderHook(() =>
      useIntentGuard({
        allowed: true,
        boundary: `${USER_A}|t1|c${useSessionStore.getState().credentialGeneration}|w:${WS}`,
      }),
    )
    // The store moves; the hook has NOT re-rendered and no effect has run.
    install(TOKEN_B)
    expect(hook.result.current.begin()).toBeNull()
    expect(hook.result.current.alive()).toBe(false)
    expect(() => hook.result.current.check()).toThrow(StaleIntentError)
    hook.unmount()
  })
})

/* ── I3: the two handoff mutations on the same transport seam ─────────────────── */

const offerBody = () => ({
  channel_id: 'c1',
  work_item_id: 'w-1',
  recipient: { kind: 'user' as const, ref: USER_B },
  handoff: { summary: 's', next_action: 'n' },
  ack_deadline: '2026-09-30T12:00:00Z',
  expected_owner_epoch: 1,
})
const offerNow = () =>
  buildHandoffOfferIntent(scopeNow(), 'w-1', '"w-v3"', offerBody())
const respondNow = () =>
  buildHandoffResponseIntent(
    scopeNow(),
    {
      handoffId: 'h-1',
      etag: '"h-v1"',
      workItemId: 'w-1',
      recipient: { kind: 'user' as const, ref: USER_B },
      deliveryId: 'd-h',
    },
    { transition: 'accept' },
  )

describe('I3 — ZERO before the first send, ONE before a refused 401 replay', () => {
  // THE DISTINCTION THIS BLOCK EXISTS FOR, and the reason the offer surface must
  //    NOT say "nothing was sent". Both cases end in the SAME StaleIntentError and
  //    the SAME screen; only the wire tells them apart, and the wire is not exposed
  //    to the surface. So the surface says the submission stopped and the result is
  //    not confirmed — which is true of both — and this file proves the two really
  //    are different byte counts, so the honest copy is not merely cautious.
  const mutations: Array<[string, () => Promise<unknown>, string]> = [
    [
      'offer',
      () => offerHandoff(offerNow(), { tenant: 't1' }),
      '/v1/m/sessions/handoffs',
    ],
    [
      'response',
      () => respondToHandoff(respondNow(), { tenant: 't1' }),
      '/v1/m/sessions/handoffs/h-1/responses',
    ],
  ]

  it.each(mutations)(
    'a %s refused during the PREVENTIVE refresh leaves ZERO requests',
    async (_what, run) => {
      const r = controlledRefresh()
      wireClient({ refreshSession: r.refresh, getExpiresAt: soon })
      const settled = outcomeOf(run())
      await r.started
      install(TOKEN_B)
      r.release()
      expect(await settled).toBe('StaleIntentError')
      expect(sent).toHaveLength(0)
    },
  )

  it.each(mutations)(
    'a %s refused on the 401 REPLAY leaves exactly ONE transmitted first leg, never with the new credential',
    async (_what, run, path) => {
      statuses = [401, 200]
      const r = controlledRefresh()
      wireClient({ refreshSession: r.refresh })
      const settled = outcomeOf(run())
      await r.started
      // The first leg is ALREADY on the wire when the rotation happens.
      expect(sent).toHaveLength(1)
      install(TOKEN_B)
      r.release()
      expect(await settled).toBe('StaleIntentError')
      // …and the replay never leaves: one request, under the ORIGINAL credential.
      expect(sent).toHaveLength(1)
      expect(sent[0].path).toBe(path)
      expect(sent[0].authorization).toBe(`Bearer ${TOKEN_A}`)
    },
  )

  it.each(mutations)(
    'POSITIVE CONTROLS for %s: nothing moving — the preventive refresh sends once and the 401 replay sends twice',
    async (_what, run) => {
      const r1 = controlledRefresh()
      wireClient({ refreshSession: r1.refresh, getExpiresAt: soon })
      const one = outcomeOf(run())
      await r1.started
      r1.release()
      expect(await one).toBe('resolved')
      expect(sent).toHaveLength(1)

      sent = []
      statuses = [401, 200]
      __resetRefreshState()
      const r2 = controlledRefresh()
      wireClient({ refreshSession: r2.refresh })
      const two = outcomeOf(run())
      await r2.started
      r2.release()
      expect(await two).toBe('resolved')
      expect(sent).toHaveLength(2)
    },
  )

  it('MISSING-GUARD MUTANT (causal control): with dispatchGuard stripped at the seam, the same rotation sends the offer with the ROTATED credential', async () => {
    const real = http.postWithMeta
    vi.spyOn(http, 'postWithMeta').mockImplementation((path, body, opts) => {
      const { dispatchGuard: _dropped, ...rest } = opts ?? {}
      void _dropped
      return real(path, body, rest)
    })
    const r = controlledRefresh()
    wireClient({ refreshSession: r.refresh, getExpiresAt: soon })
    const settled = outcomeOf(offerHandoff(offerNow(), { tenant: 't1' }))
    await r.started
    install(TOKEN_B)
    r.release()
    expect(await settled).toBe('resolved')
    expect(sent).toHaveLength(1)
    expect(sent[0].authorization).toBe(`Bearer ${TOKEN_B}`)
  })

  it('a same-key EXPLICIT retry re-sends the identical object: same key, same If-Match, same bytes, same target', async () => {
    const intent = offerNow()
    statuses = [200, 200]
    await offerHandoff(intent, { tenant: 't1' })
    await offerHandoff(intent, { tenant: 't1' })
    expect(sent).toHaveLength(2)
    expect(sent[0].path).toBe(sent[1].path)
    // The recorded wire in THIS file keeps headers only for the credential and the
    // tenant, so the identity of the retry is asserted on the intent that produced
    // both requests — and api.test.ts measures the header bytes themselves.
    expect(Object.isFrozen(intent)).toBe(true)
    expect(Object.isFrozen(intent.body)).toBe(true)
  })

  it('a moved workspace refuses the RESPONSE too: the room is workspace-scoped and so is its intent', async () => {
    const intent = respondNow()
    useWorkspaceStore.getState().setActiveWorkspace(WS2)
    const err = await respondToHandoff(intent, { tenant: 't1' }).catch((e) => e)
    expect(err).toBeInstanceOf(StaleIntentError)
    expect((err as StaleIntentError).moved).toBe('workspace')
    expect(sent).toHaveLength(0)
  })
})
