// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE CAPABILITY OBSERVATION, MEASURED WHERE IT CAN BE WRONG.
//
// These cases run the REAL admission rule against REAL parsed JSON, the real transport
// through the shared client, and a real monotonic clock. They are deliberately not a
// re-run of the component suites: a surface's behaviour GIVEN an observation is measured
// once, in that surface's own file. What is measured here is the thing no component can
// see — whether a body, a clock and a context may become an observation at all.
//
// Each block names the defect it exists to catch, and several carry an explicit control:
// the same case with the guard removed, so a green result cannot come from the test being
// unable to fail.
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { configureApiClient, __resetRefreshState } from '@/lib/api/client'
import {
  CAPABILITY_REQUEST_MAX_MS,
  CAPABILITY_RETRY_MS,
  admitCapabilityResult,
  capabilityKey,
  capabilityQuestion,
  capabilityRequestBody,
  capabilityScopeKey,
  createCapabilityPermit,
  observeCapability,
  requestCapabilityPermit,
  sameContext,
  sameQuestion,
  wireQuestion,
  CapabilityLostError,
  type CapabilityContext,
  type NormalizedCapabilityQuestion,
} from './capabilities'

const WS = '0192f2c0-aaaa-7000-8000-000000000001'
const CH = '0192f2c0-bbbb-7000-8000-000000000001'
const GR = '0192f2c0-cccc-7000-8000-000000000001'
const TOKEN = 'olvs_secret_bearer_value'
const SESSION = 'session-id-77'

const CONTEXT: CapabilityContext = {
  principalKind: 'user',
  actor: 'user:u-a',
  tenant: 't1',
  credentialGeneration: 3,
  workspace: WS,
  lifetime: 7,
}

const SURFACE = capabilityQuestion({
  kind: 'surface',
  operation: 'GET /v1/m/sessions/channels/administration',
  workspaceId: WS,
})
const SHEET = capabilityQuestion({
  kind: 'operation',
  operation: 'GET /v1/m/sessions/channels/{id}/grants',
  workspaceId: WS,
  path: { id: CH },
})
const PATCH = capabilityQuestion({
  kind: 'operation',
  operation: 'PATCH /v1/m/sessions/channels',
  workspaceId: WS,
  body: { channel_id: CH },
})
const REVOKE = capabilityQuestion({
  kind: 'operation',
  operation: 'POST /v1/m/sessions/channels/{id}/grants/{grant_id}/revoke',
  workspaceId: WS,
  path: { id: CH, grant_id: GR },
})

/** Ask `positiveBody` for a result with NO `refresh_after_ms` key at all — which is a
 *  different body from one carrying `undefined`, and the one the contract calls an
 *  instruction to disbelieve the positive. A default parameter cannot express it. */
const OMIT = Symbol('omit refresh_after_ms')

/** One well-formed positive body for `question`, as the engine actually sends it. */
function positiveBody(
  question: NormalizedCapabilityQuestion,
  budget: unknown = 30_000,
): unknown {
  const result: Record<string, unknown> = {
    id: 'q',
    kind: question.kind,
    state: question.kind === 'surface' ? 'reachable' : 'allowed',
    code: question.kind === 'surface' ? 'admitted' : 'authorized',
    observed_at: '2026-09-07T10:00:00Z',
  }
  if (budget !== OMIT) result.refresh_after_ms = budget
  return { schema_version: 2, results: [result] }
}

describe('the structural cache identity', () => {
  // The defect: two DIFFERENT questions, or the same question under a different
  // authority, sharing one cache entry. Each row below is a distinction the key must make.
  it('separates every field of the context and of the question', () => {
    const base = capabilityKey(CONTEXT, SHEET)
    const differs = (
      over: Partial<CapabilityContext>,
      q: NormalizedCapabilityQuestion = SHEET,
    ) => capabilityKey({ ...CONTEXT, ...over }, q)
    const rows: [string, readonly unknown[]][] = [
      ['principal kind', differs({ principalKind: 'token' })],
      ['actor', differs({ actor: 'user:u-b' })],
      ['tenant', differs({ tenant: 't2' })],
      ['credential generation', differs({ credentialGeneration: 4 })],
      ['workspace', differs({ workspace: 'other' })],
      ['question kind', capabilityKey(CONTEXT, SURFACE)],
      ['operation', capabilityKey(CONTEXT, PATCH)],
      ['path selector', capabilityKey(CONTEXT, REVOKE)],
      [
        'body selector',
        capabilityKey(
          CONTEXT,
          capabilityQuestion({
            kind: 'operation',
            operation: 'PATCH /v1/m/sessions/channels',
            workspaceId: WS,
            body: { channel_id: GR },
          }),
        ),
      ],
    ]
    for (const [what, key] of rows)
      expect(JSON.stringify(key), what).not.toBe(JSON.stringify(base))
  })

  it('is stable under selector declaration order and cannot be collided by a delimiter', () => {
    const a = capabilityQuestion({
      kind: 'operation',
      operation: 'POST /v1/m/sessions/channels/{id}/grants/{grant_id}/revoke',
      workspaceId: WS,
      path: { grant_id: GR, id: CH },
    })
    const b = capabilityQuestion({
      kind: 'operation',
      operation: 'POST /v1/m/sessions/channels/{id}/grants/{grant_id}/revoke',
      workspaceId: WS,
      path: { id: CH, grant_id: GR },
    })
    expect(JSON.stringify(capabilityKey(CONTEXT, a))).toBe(
      JSON.stringify(capabilityKey(CONTEXT, b)),
    )
    expect(sameQuestion(a, b)).toBe(true)

    // ⛔ THE COLLISION A JOINED STRING WOULD PRODUCE. `id="x|y", grant_id="z"` and
    //    `id="x", grant_id="y|z"` join to the same `x|y|z`. As sorted PAIRS they cannot.
    const left = capabilityQuestion({
      kind: 'operation',
      operation: 'POST /v1/m/sessions/channels/{id}/grants/{grant_id}/revoke',
      workspaceId: WS,
      path: { id: 'x|y', grant_id: 'z' },
    })
    const right = capabilityQuestion({
      kind: 'operation',
      operation: 'POST /v1/m/sessions/channels/{id}/grants/{grant_id}/revoke',
      workspaceId: WS,
      path: { id: 'x', grant_id: 'y|z' },
    })
    expect(JSON.stringify(capabilityKey(CONTEXT, left))).not.toBe(
      JSON.stringify(capabilityKey(CONTEXT, right)),
    )
    expect(sameQuestion(left, right)).toBe(false)
  })

  it('carries no credential: neither the key nor its scope prefix contains a token or a session id', () => {
    const serialized = JSON.stringify([
      capabilityKey(CONTEXT, SHEET),
      capabilityScopeKey(CONTEXT),
    ])
    expect(serialized).not.toContain(TOKEN)
    expect(serialized).not.toContain(SESSION)
    expect(serialized).not.toContain('Bearer')
    expect(serialized).not.toContain('observed_at')
    // Positive control: the fields it SHOULD carry are there, so "contains nothing" is
    // not passing because the key is empty.
    expect(serialized).toContain(CONTEXT.actor)
    expect(serialized).toContain(WS)
  })

  it('the scope prefix is a prefix of the exact key, so invalidating one reaches the other', () => {
    const scope = capabilityScopeKey(CONTEXT)
    const exact = capabilityKey(CONTEXT, SHEET)
    expect(exact.slice(0, scope.length)).toEqual(scope)
  })
})

describe('the normalized question', () => {
  it('freezes each selector PAIR, not only the list that holds them', () => {
    const q = capabilityQuestion({
      kind: 'operation',
      operation: 'PATCH /v1/m/sessions/channels',
      workspaceId: WS,
      path: { id: CH },
      body: { channel_id: CH },
    })
    // ⛔ AT CONSTRUCTION, NOT AT THE PERMIT. A question is a cache key and the assertion
    //    a response is checked against long before any permit exists, so freezing it only
    //    when a permit is granted would leave every rendered observation writable. The
    //    outer freeze reported true while `body[0]` was a plain mutable array.
    expect(Object.isFrozen(q)).toBe(true)
    expect(Object.isFrozen(q.path)).toBe(true)
    expect(Object.isFrozen(q.body)).toBe(true)
    expect(Object.isFrozen(q.path[0])).toBe(true)
    expect(Object.isFrozen(q.body[0])).toBe(true)
    expect(() => {
      ;(q.body[0] as unknown as string[])[1] = 'another-channel'
    }).toThrow(TypeError)
    expect(q.body[0][1]).toBe(CH)
  })
})

describe('the wire question', () => {
  it('sends the mounted pattern, the workspace and only the declared selector families', () => {
    expect(wireQuestion(SURFACE)).toEqual({
      id: 'q',
      kind: 'surface',
      operation: 'GET /v1/m/sessions/channels/administration',
      workspace_id: WS,
    })
    // A collection carries NO selectors key at all: an empty object is still an object,
    // and the engine answers `inputs_required` to a collection that received a locator.
    expect('selectors' in wireQuestion(SURFACE)).toBe(false)
    expect(wireQuestion(PATCH).selectors).toEqual({ body: { channel_id: CH } })
    expect('path' in (wireQuestion(PATCH).selectors ?? {})).toBe(false)
    expect(wireQuestion(REVOKE).selectors).toEqual({
      path: { id: CH, grant_id: GR },
    })
    expect(capabilityRequestBody(SHEET)).toEqual({
      schema_version: 2,
      questions: [wireQuestion(SHEET)],
    })
  })
})

describe('admitting an actual response body', () => {
  const T0 = 1_000

  it('admits the exact positive pair of each kind, and only with a usable budget', () => {
    expect(
      admitCapabilityResult(positiveBody(SHEET), SHEET, T0, T0 + 5),
    ).toEqual({ access: 'allowed', deadline: T0 + 30_000 })
    expect(
      admitCapabilityResult(positiveBody(SURFACE), SURFACE, T0, T0 + 5),
    ).toEqual({ access: 'reachable', deadline: T0 + 30_000 })
  })

  it('refuses every malformed envelope, cardinality, correlator and kind', () => {
    const bad: [string, unknown][] = [
      ['not an object', 'allowed'],
      ['null', null],
      ['schema 1', { schema_version: 1, results: [] }],
      ['no version', { results: [] }],
      ['results absent', { schema_version: 2 }],
      ['results not an array', { schema_version: 2, results: {} }],
      ['zero results', { schema_version: 2, results: [] }],
      [
        'two results for one question',
        {
          schema_version: 2,
          results: [
            (positiveBody(SHEET) as { results: unknown[] }).results[0],
            (positiveBody(SHEET) as { results: unknown[] }).results[0],
          ],
        },
      ],
      [
        'another question id',
        {
          schema_version: 2,
          results: [
            {
              ...(positiveBody(SHEET) as { results: { id: string }[] })
                .results[0],
              id: 'other',
            },
          ],
        },
      ],
      [
        'the other kind answered',
        {
          schema_version: 2,
          results: [
            {
              ...(positiveBody(SHEET) as { results: object[] }).results[0],
              kind: 'surface',
            },
          ],
        },
      ],
    ]
    for (const [what, body] of bad)
      expect(admitCapabilityResult(body, SHEET, T0, T0 + 5), what).toEqual({
        access: 'unknown',
        deadline: null,
      })
  })

  it('refuses a MISMATCHED state/code pair rather than believing half of it', () => {
    // ⛔ THE PAIR IS THE VERDICT. `allowed` with the surface's code, or `authorized` under
    //    a non-positive state, is a body this console does not understand — and the
    //    dangerous reading of it is the permissive one.
    const pairs: [string, string, string][] = [
      ['positive state, foreign code', 'allowed', 'admitted'],
      ['positive state, refusal code', 'allowed', 'not_permitted'],
      ['refusal state, positive code', 'denied', 'authorized'],
      ['concealment state, positive code', 'undisclosed', 'authorized'],
      ['unknown vocabulary', 'maybe', 'perhaps'],
      ['surface vocabulary on an operation', 'reachable', 'admitted'],
    ]
    for (const [what, state, code] of pairs)
      expect(
        admitCapabilityResult(
          {
            schema_version: 2,
            results: [
              {
                id: 'q',
                kind: 'operation',
                state,
                code,
                observed_at: 'x',
                refresh_after_ms: 30_000,
              },
            ],
          },
          SHEET,
          T0,
          T0 + 5,
        ),
        what,
      ).toEqual({ access: 'unknown', deadline: null })
  })

  it('recognises each established non-positive in its own vocabulary, and never as a refusal it is not', () => {
    const answer = (
      kind: 'surface' | 'operation',
      state: string,
      code: string,
    ) =>
      admitCapabilityResult(
        {
          schema_version: 2,
          results: [{ id: 'q', kind, state, code, observed_at: 'x' }],
        },
        kind === 'surface' ? SURFACE : SHEET,
        T0,
        T0 + 5,
      ).access
    expect(answer('operation', 'denied', 'not_permitted')).toBe('denied')
    expect(answer('surface', 'not_reachable', 'not_permitted')).toBe(
      'not_reachable',
    )
    expect(answer('operation', 'undisclosed', 'not_disclosed')).toBe(
      'undisclosed',
    )
    expect(answer('operation', 'unknown', 'step_up_required')).toBe(
      'step_up_required',
    )
    // Everything the engine may answer that this console has no rule for stays UNKNOWN.
    // It is never promoted to a denial: "I do not understand" is not "you may not".
    expect(answer('operation', 'unknown', 'engine_unready')).toBe('unknown')
    expect(answer('operation', 'unknown', 'evidence_unavailable')).toBe(
      'unknown',
    )
    expect(answer('operation', 'unknown', 'not_supported')).toBe('unknown')
    expect(answer('operation', 'unknown', 'inputs_required')).toBe('unknown')
  })

  it('refuses every unusable budget, including the absent one', () => {
    const budgets: [string, unknown][] = [
      // `undefined` is not a row here on purpose: JSON parsed off the wire never yields
      // it, and a default parameter cannot express it anyway. `OMIT` and `null` are the
      // two shapes an actual body can take.
      ['absent (no key at all)', OMIT],
      ['null', null],
      ['zero', 0],
      ['negative', -1],
      ['fractional', 1500.5],
      ['over the 30s ceiling', CAPABILITY_REQUEST_MAX_MS + 1],
      ['not a number', '30000'],
      ['NaN', Number.NaN],
      ['infinite', Number.POSITIVE_INFINITY],
    ]
    for (const [what, budget] of budgets)
      expect(
        admitCapabilityResult(positiveBody(SHEET, budget), SHEET, T0, T0 + 5),
        what,
      ).toEqual({ access: 'unknown', deadline: null })
    // The boundary itself is usable: 30000 is the contract's own maximum.
    expect(
      admitCapabilityResult(
        positiveBody(SHEET, CAPABILITY_REQUEST_MAX_MS),
        SHEET,
        T0,
        T0 + 5,
      ).access,
    ).toBe('allowed')
    // And the smallest legal one is too, so "refuses everything" is not why this passes.
    expect(
      admitCapabilityResult(positiveBody(SHEET, 1), SHEET, T0, T0 + 0.5).access,
    ).toBe('allowed')
  })

  it('spends the budget from BEFORE the request, so a slow answer cannot buy a fresh window', () => {
    // 2000 ms of budget, answered after 2500 ms: exhausted ON ARRIVAL. A client that
    // measured from receipt would call this a live permit for two more seconds — which is
    // exactly the state a proactive refresh plus a 401 replay produces.
    expect(
      admitCapabilityResult(positiveBody(SHEET, 2_000), SHEET, T0, T0 + 2_500),
    ).toEqual({ access: 'unknown', deadline: null })
    // CONTROL: the same body, the same budget, answered in time. Without this the case
    // above would also pass on a rule that rejected every positive.
    expect(
      admitCapabilityResult(positiveBody(SHEET, 2_000), SHEET, T0, T0 + 1_999)
        .access,
    ).toBe('allowed')
    // The deadline is start-based in both readings.
    expect(
      admitCapabilityResult(positiveBody(SHEET, 2_000), SHEET, T0, T0 + 1_999)
        .deadline,
    ).toBe(T0 + 2_000)
  })

  it('refuses a clock that is not usable as a clock', () => {
    for (const [what, started, now] of [
      ['regressed', T0, T0 - 1],
      ['non-finite now', T0, Number.NaN],
      ['non-finite start', Number.POSITIVE_INFINITY, T0],
    ] as [string, number, number][])
      expect(
        admitCapabilityResult(positiveBody(SHEET), SHEET, started, now),
        what,
      ).toEqual({ access: 'unknown', deadline: null })
  })
})

describe('the permit', () => {
  it('is immutable, expires at its deadline and dies when the context moves', () => {
    const live = vi.fn<() => CapabilityContext | null>(() => CONTEXT)
    const permit = createCapabilityPermit(
      CONTEXT,
      SHEET,
      performance.now() + 50_000,
      live,
    )
    expect(permit.isCurrent()).toBe(true)
    expect(() => permit.assertCurrent()).not.toThrow()
    expect(Object.isFrozen(permit)).toBe(true)

    // A → B: the same permit refuses immediately, with no re-request.
    live.mockReturnValue({ ...CONTEXT, workspace: 'other' })
    expect(permit.isCurrent()).toBe(false)
    expect(() => permit.assertCurrent()).toThrow(CapabilityLostError)

    // ⛔ THIS ASSERTION USED TO EXPECT THE OPPOSITE, and it was the defect written down
    //    as a promise. It said A → B → A left the permit current again "because the
    //    values match", and deferred the round trip to the cache layer — where `gcTime: 0`
    //    and the removal effect both need a React commit that a batched round trip never
    //    produces. The independent review reproduced it three ways, one of which spent
    //    this permit on a real PATCH. A permit that has once been found outside its
    //    context is dead: nothing it is asked afterwards can bring it back.
    live.mockReturnValue(CONTEXT)
    expect(permit.isCurrent()).toBe(false)
    expect(() => permit.assertCurrent()).toThrow(CapabilityLostError)
    // And the latched REASON is the one that actually happened, not the last check.
    expect(() => permit.assertCurrent()).toThrow(/context/)

    // A renewal under the same session advances the credential generation, so the value
    // comparison refuses a fresh permit on its own — the latch is the second defence,
    // not a replacement for the first.
    const renewed = createCapabilityPermit(
      CONTEXT,
      SHEET,
      performance.now() + 50_000,
      () => ({ ...CONTEXT, credentialGeneration: 4 }),
    )
    expect(renewed.isCurrent()).toBe(false)

    // An unreadable principal is not "the same principal": deny-closed.
    const unreadable = createCapabilityPermit(
      CONTEXT,
      SHEET,
      performance.now() + 50_000,
      () => null,
    )
    expect(unreadable.isCurrent()).toBe(false)
  })

  it('is dead for good once its window has passed, even if the clock is re-read', () => {
    let clock = 1_000
    vi.spyOn(performance, 'now').mockImplementation(() => clock)
    const permit = createCapabilityPermit(CONTEXT, SHEET, 1_100, () => CONTEXT)
    expect(permit.isCurrent()).toBe(true)
    clock = 1_200
    expect(permit.isCurrent()).toBe(false)
    // A monotonic clock cannot go back, but a MOCKED or corrected one can, and a permit
    // whose freshness has already failed must not be re-fresh because a reading changed.
    clock = 1_050
    expect(permit.isCurrent()).toBe(false)
    expect(() => permit.assertCurrent()).toThrow(/expired/)
    vi.restoreAllMocks()
  })

  it("owns a frozen copy of its context and question, not the caller's objects", () => {
    const mutableContext = { ...CONTEXT }
    const question = capabilityQuestion({
      kind: 'operation',
      operation: 'PATCH /v1/m/sessions/channels',
      workspaceId: WS,
      body: { channel_id: CH },
    })
    const permit = createCapabilityPermit(
      mutableContext,
      question,
      performance.now() + 50_000,
      () => CONTEXT,
    )
    // ⛔ THE CONTAINER WAS FROZEN AND THE CONTENTS WERE NOT. `Object.isFrozen(permit)`
    //    answered true while `permit.context.workspace` and `question.body[0][1]` were
    //    both writable, so a caller could change what the permit claims to be about
    //    after it was granted.
    expect(Object.isFrozen(permit.context)).toBe(true)
    expect(Object.isFrozen(permit.question.body)).toBe(true)
    expect(Object.isFrozen(permit.question.body[0])).toBe(true)
    expect(() => {
      ;(permit.context as { workspace: string | null }).workspace = 'other'
    }).toThrow(TypeError)
    expect(() => {
      ;(permit.question.body[0] as unknown as string[])[1] = 'other-channel'
    }).toThrow(TypeError)
    expect(permit.context.workspace).toBe(WS)
    expect(permit.question.body[0][1]).toBe(CH)

    // The caller's own object is a different object: writing it cannot reach the permit.
    mutableContext.workspace = 'other'
    expect(permit.context.workspace).toBe(WS)
    expect(permit.isCurrent()).toBe(true)
  })

  it('expires on the monotonic clock, not on wall time', () => {
    const permit = createCapabilityPermit(
      CONTEXT,
      SHEET,
      performance.now() - 1,
      () => CONTEXT,
    )
    expect(permit.isCurrent()).toBe(false)
    expect(() => permit.assertCurrent()).toThrow(/expired/)
  })

  it('sameContext refuses a null on either side', () => {
    expect(sameContext(CONTEXT, CONTEXT)).toBe(true)
    expect(sameContext(null, CONTEXT)).toBe(false)
    expect(sameContext(CONTEXT, null)).toBe(false)
  })
})

/* ── the real transport ───────────────────────────────────────────────────────── */

let calls: { url: string; init: RequestInit | undefined }[] = []

function serve(...responses: (() => Response | Promise<Response>)[]) {
  let i = 0
  globalThis.fetch = vi.fn(async (url: string, init?: RequestInit) => {
    calls.push({ url: String(url), init })
    const make = responses[Math.min(i, responses.length - 1)]
    i++
    return make()
  }) as never
}

const json = (status: number, body: unknown) => () =>
  new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  })

beforeEach(() => {
  calls = []
  configureApiClient({
    getToken: () => TOKEN,
    getTenant: () => 'ambient-tenant',
    onUnauthorized: () => {},
  })
})

afterEach(() => {
  configureApiClient({
    getToken: () => null,
    getTenant: () => null,
    onUnauthorized: () => {},
    refreshSession: undefined,
    getExpiresAt: undefined,
  })
  __resetRefreshState()
  vi.restoreAllMocks()
})

describe('observing over the real shared client', () => {
  it('POSTs the exact schema-2 body to the capability route with the CAPTURED tenant', async () => {
    serve(json(200, positiveBody(SHEET)))
    const result = await observeCapability({
      question: SHEET,
      context: CONTEXT,
      live: () => CONTEXT,
    })
    expect(result.access).toBe('allowed')
    expect(calls).toHaveLength(1)
    expect(calls[0].url).toContain('/v1/auth/capabilities')
    expect(calls[0].init?.method).toBe('POST')
    expect(JSON.parse(String(calls[0].init?.body))).toEqual({
      schema_version: 2,
      questions: [wireQuestion(SHEET)],
    })
    // ⛔ THE CAPTURED TENANT, NOT THE AMBIENT ONE. The client would have supplied
    //    `ambient-tenant`; the question was asked about `t1` and must travel as `t1`.
    expect(new Headers(calls[0].init?.headers).get('X-Olivares-Tenant')).toBe(
      't1',
    )
  })

  it('discards an answer whose context moved WHILE it was in flight', async () => {
    serve(json(200, positiveBody(SHEET)))
    let live: CapabilityContext | null = CONTEXT
    const promise = observeCapability({
      question: SHEET,
      context: CONTEXT,
      live: () => live,
    })
    live = { ...CONTEXT, tenant: 't2' }
    const result = await promise
    expect(result.access).toBe('unknown')
    // The request DID leave — this is about the answer, not about preventing the ask.
    expect(calls).toHaveLength(1)
    // CONTROL: the identical body with a context that did not move is a permit.
    calls = []
    serve(json(200, positiveBody(SHEET)))
    expect(
      (
        await observeCapability({
          question: SHEET,
          context: CONTEXT,
          live: () => CONTEXT,
        })
      ).access,
    ).toBe('allowed')
  })

  it('counts the 401 refresh AND the replay inside the one budget, and guards BOTH sends', async () => {
    const guard = vi.fn()
    configureApiClient({
      getToken: () => TOKEN,
      getTenant: () => 'ambient-tenant',
      onUnauthorized: () => {},
      refreshSession: async () => {
        // A refresh that costs real time: the budget below is spent by it.
        await new Promise((r) => setTimeout(r, 60))
        return true
      },
    })
    serve(
      json(401, { error: { code: 'token_expired', message: 'expired' } }),
      json(200, positiveBody(SHEET, 50)),
    )
    const result = await observeCapability({
      question: SHEET,
      context: CONTEXT,
      live: () => CONTEXT,
      dispatchGuard: guard,
    })
    // Two sends of the capability request itself, and the guard ran before EACH.
    const asks = calls.filter((c) => c.url.includes('/v1/auth/capabilities'))
    expect(asks).toHaveLength(2)
    expect(guard).toHaveBeenCalledTimes(2)
    // 50 ms of budget, more than 50 ms of refresh: exhausted on arrival, so no permit.
    expect(result.access).toBe('unknown')
  })

  it('a guard that throws stops the request before any byte leaves', async () => {
    serve(json(200, positiveBody(SHEET)))
    const boom = new Error('surface moved')
    await expect(
      observeCapability({
        question: SHEET,
        context: CONTEXT,
        live: () => CONTEXT,
        dispatchGuard: () => {
          throw boom
        },
      }),
    ).rejects.toBe(boom)
    expect(calls).toHaveLength(0)
  })

  it('a transport failure is a local unknown, never a denial and never an error state', async () => {
    globalThis.fetch = vi.fn(async () => {
      throw new TypeError('network down')
    }) as never
    expect(
      (
        await observeCapability({
          question: SHEET,
          context: CONTEXT,
          live: () => CONTEXT,
        })
      ).access,
    ).toBe('unknown')
    // A 403 from the endpoint itself is equally an unknown: this console did not obtain a
    // decision, and inventing one from a status code is the whole defect.
    serve(json(403, { error: { code: 'forbidden', message: 'no' } }))
    expect(
      (
        await observeCapability({
          question: SHEET,
          context: CONTEXT,
          live: () => CONTEXT,
        })
      ).access,
    ).toBe('unknown')
  })

  it('a caller abort propagates as a cancellation rather than as an observation', async () => {
    const controller = new AbortController()
    globalThis.fetch = vi.fn(
      (_url: string, init?: RequestInit) =>
        new Promise((_resolve, reject) => {
          init?.signal?.addEventListener('abort', () =>
            reject(
              new DOMException('The operation was aborted.', 'AbortError'),
            ),
          )
        }),
    ) as never
    const promise = observeCapability({
      question: SHEET,
      context: CONTEXT,
      live: () => CONTEXT,
      caller: controller.signal,
    })
    controller.abort()
    await expect(promise).rejects.toThrow(/abort/i)
  })

  it('the preflight returns a permit only for a current positive', async () => {
    serve(json(200, positiveBody(PATCH)))
    const permit = await requestCapabilityPermit({
      question: PATCH,
      context: CONTEXT,
      live: () => CONTEXT,
    })
    expect(permit).not.toBeNull()
    expect(permit?.question).toBe(PATCH)
    expect(permit?.isCurrent()).toBe(true)

    // A concealed non-verdict is not a permit, and it is not an error either.
    serve(
      json(200, {
        schema_version: 2,
        results: [
          {
            id: 'q',
            kind: 'operation',
            state: 'undisclosed',
            code: 'not_disclosed',
            observed_at: 'x',
          },
        ],
      }),
    )
    expect(
      await requestCapabilityPermit({
        question: PATCH,
        context: CONTEXT,
        live: () => CONTEXT,
      }),
    ).toBeNull()
  })

  it('the two ratified constants are the ones the module uses', () => {
    expect(CAPABILITY_REQUEST_MAX_MS).toBe(30_000)
    expect(CAPABILITY_RETRY_MS).toBe(5_000)
  })
})

/* ── the bound, the cancellation and what may arrive too late ─────────────────── */

describe('ending an attempt, and what a late answer may do', () => {
  const positive = () =>
    new Response(JSON.stringify(positiveBody(SHEET)), {
      status: 200,
      headers: { 'Content-Type': 'application/json' },
    })

  it('an ALREADY-aborted caller sends zero bytes and publishes no authority', async () => {
    // ⛔ THE LISTENER ONLY FIRES ON A FUTURE ABORT. A signal that was already aborted
    //    produced a composed controller that was never aborted, so the request went out
    //    under a withdrawn intention and came back with a positive the caller could
    //    spend. Reproduced against the frozen source: one capability POST, `allowed`.
    const fetchSpy = vi.fn(async () => positive())
    globalThis.fetch = fetchSpy as never
    const controller = new AbortController()
    controller.abort()

    await expect(
      observeCapability({
        question: SHEET,
        context: CONTEXT,
        live: () => CONTEXT,
        caller: controller.signal,
      }),
    ).rejects.toThrow()
    expect(fetchSpy).not.toHaveBeenCalled()

    // The preflight is the path a mutation actually takes: it must not return a permit.
    await expect(
      requestCapabilityPermit({
        question: PATCH,
        context: CONTEXT,
        live: () => CONTEXT,
        signal: controller.signal,
      }),
    ).rejects.toThrow()
    expect(fetchSpy).not.toHaveBeenCalled()
  })

  it('cancellation is retained THROUGH an answer that was already on its way back', async () => {
    const controller = new AbortController()
    globalThis.fetch = vi.fn(async () => {
      // The bytes are already in flight when the caller gives up: aborting cannot unsend
      // them, so the rule is that they may not become authority.
      controller.abort()
      return positive()
    }) as never
    await expect(
      observeCapability({
        question: SHEET,
        context: CONTEXT,
        live: () => CONTEXT,
        caller: controller.signal,
      }),
    ).rejects.toThrow()
  })

  it('settles at the bound even when the shared refresh never answers, and the late answer publishes nothing', async () => {
    vi.useFakeTimers()
    let finishRefresh!: (renewed: boolean) => void
    const refresh = vi.fn(
      () =>
        new Promise<boolean>((resolve) => {
          finishRefresh = resolve
        }),
    )
    const fetchSpy = vi.fn(async () => positive())
    globalThis.fetch = fetchSpy as never
    configureApiClient({
      getToken: () => TOKEN,
      // Close enough to expiry that the client awaits its PROACTIVE refresh before it
      // builds a request at all — the await that observes no signal.
      getExpiresAt: () => new Date(Date.now() + 1_000).toISOString(),
      refreshSession: refresh,
    })

    let settled = false
    const attempt = observeCapability({
      question: SHEET,
      context: CONTEXT,
      live: () => CONTEXT,
    }).finally(() => {
      settled = true
    })

    await vi.advanceTimersByTimeAsync(CAPABILITY_REQUEST_MAX_MS - 1)
    // ⛔ THE MEASURED DEFECT: aborting a controller does not end an await that never
    //    looked at it. On the frozen source this was still pending at 30.001 ms with
    //    zero capability fetches made.
    expect(settled).toBe(false)
    expect(fetchSpy).not.toHaveBeenCalled()

    await vi.advanceTimersByTimeAsync(2)
    expect(settled).toBe(true)
    const observation = await attempt
    // A local unknown on the ordinary visible cadence — never a denial, never a positive.
    expect(observation.access).toBe('unknown')
    expect(observation.deadline).toBeNull()

    // ⛔ AND THE SHARED REFRESH WAS NOT CANCELLED. It belongs to every other consumer of
    //    this client; ending our own wait is not a licence to break theirs. It is still
    //    the same single flight, it still completes, and the next observation uses it.
    expect(refresh).toHaveBeenCalledTimes(1)
    finishRefresh(true)
    await vi.advanceTimersByTimeAsync(1)
    // The renewal landed, so the credential is no longer near expiry: the next request
    // needs no refresh of its own, and the fact that it goes out at all is the evidence
    // that the shared single flight completed instead of being torn down.
    configureApiClient({
      getExpiresAt: () => new Date(Date.now() + 3_600_000).toISOString(),
    })
    const after = await observeCapability({
      question: SHEET,
      context: CONTEXT,
      live: () => CONTEXT,
    })
    expect(after.access).toBe('allowed')
    expect(refresh).toHaveBeenCalledTimes(1)
    vi.useRealTimers()
  })

  it('ignores a late fulfillment and HANDLES a late rejection after the bound', async () => {
    vi.useFakeTimers()
    let land!: (body: Response) => void
    let fail!: (cause: unknown) => void
    let call = 0
    globalThis.fetch = vi.fn(
      () =>
        new Promise<Response>((resolve, reject) => {
          call += 1
          if (call === 1) land = resolve
          else fail = reject
        }),
    ) as never

    const first = observeCapability({
      question: SHEET,
      context: CONTEXT,
      live: () => CONTEXT,
    })
    const second = observeCapability({
      question: SHEET,
      context: CONTEXT,
      live: () => CONTEXT,
    })
    await vi.advanceTimersByTimeAsync(CAPABILITY_REQUEST_MAX_MS + 1)
    expect((await first).access).toBe('unknown')
    expect((await second).access).toBe('unknown')

    // A positive that arrives after its own window was abandoned changes nothing: the
    // race is over and nobody reads the value.
    land(positive())
    // And the rejection has a handler attached from the start, so a transport that gives
    // up minutes later is not an unhandled rejection crashing an unrelated screen.
    fail(new Error('late transport failure'))
    await vi.advanceTimersByTimeAsync(1)
    expect((await first).access).toBe('unknown')
    expect((await second).access).toBe('unknown')
    vi.useRealTimers()
  })
})
