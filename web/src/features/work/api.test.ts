// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { ApiError } from '@/lib/api/errors'
import { __resetRefreshState, configureApiClient } from '@/lib/api/client'
import {
  acceptanceEvaluateBody,
  applyWork,
  attributesCurrentState,
  buildIntent as buildWorkIntent,
  classifyApplyFailure,
  foldFields,
  isUnknownVerdict,
  listDecisions,
  listLeases,
  listWorkItems,
  planWork,
  requiredFieldsFor,
} from './api'
import type { WorkDecision } from './types'

/**
 * — the work client's contract with the kernel.
 *
 * Each block below names the CONTROL it measures, the mutation that makes it fire, and
 * the legitimate neighbouring operation that must NOT make it fire. A control with only
 * the firing direction passes just as happily when it rejects everything.
 */

/** Capture what the client actually put on the wire. */
interface Captured {
  url: string
  method: string
  headers: Record<string, string>
  body: string | undefined
}
let captured: Captured[] = []
const tenantOptions = { tenant: 't1' } as const

function buildIntent(
  args: Omit<Parameters<typeof buildWorkIntent>[0], 'tenant'> & {
    tenant?: Parameters<typeof buildWorkIntent>[0]['tenant']
  },
) {
  return buildWorkIntent({
    ...args,
    tenant: args.tenant === undefined ? tenantOptions.tenant : args.tenant,
  })
}

function stubFetch(
  responder: (c: Captured) => {
    status?: number
    body?: unknown
    headers?: Record<string, string>
  },
) {
  vi.stubGlobal(
    'fetch',
    vi.fn(async (url: string, init: RequestInit = {}) => {
      const headers: Record<string, string> = {}
      new Headers(init.headers).forEach((v, k) => {
        headers[k.toLowerCase()] = v
      })
      const c: Captured = {
        url: String(url),
        method: init.method ?? 'GET',
        headers,
        body: init.body === undefined ? undefined : String(init.body),
      }
      captured.push(c)
      const r = responder(c)
      return new Response(JSON.stringify(r.body ?? {}), {
        status: r.status ?? 200,
        headers: { 'Content-Type': 'application/json', ...(r.headers ?? {}) },
      })
    }),
  )
}

beforeEach(() => {
  captured = []
  __resetRefreshState()
  configureApiClient({
    getToken: () => 'olvs_test',
    getTenant: () => 't1',
    onUnauthorized: () => {},
    refreshSession: undefined,
    getExpiresAt: undefined,
  })
})

afterEach(() => {
  vi.unstubAllGlobals()
})

// ---------------------------------------------------------------------------

describe('NO_HE_PODIDO_MIRAR arrives by two doors', () => {
  // CONTROL: the third outcome is never read as success.
  // FIRES IF: isUnknownVerdict stops inspecting the 200 body (door 2) or the ApiError
  //   body (door 1). Deleting either arm leaves the other test green, which is why both
  //   are asserted separately rather than in one case.
  // DOES NOT FIRE FOR: a genuine LIMPIO — the legitimate neighbouring outcome, which
  //   must stay distinguishable or the helper is just "return true".
  it('door 1: a 503 error body carrying the verdict', () => {
    const err = new ApiError(
      503,
      'evidence_unavailable',
      'evidence_unavailable',
      undefined,
      {},
      {
        verdict: 'NO_HE_PODIDO_MIRAR',
        code: 'evidence_unavailable',
        error: {
          code: 'evidence_unavailable',
          message: 'evidence_unavailable',
        },
      },
    )
    expect(isUnknownVerdict(err)).toBe(true)
  })

  it('door 2: a 200 OK Assessment — the one a status check would call success', async () => {
    // This is exactly what modules/sessions/work_api.go:199-205 writes: the request
    // succeeded, the observation did not.
    stubFetch(() => ({
      status: 200,
      body: {
        verdict: 'NO_HE_PODIDO_MIRAR',
        code: 'evidence_unavailable',
        observed_at: '2026-08-10T00:00:00Z',
        checks: [
          { name: 'evidence_unavailable', verdict: 'NO_HE_PODIDO_MIRAR' },
        ],
        plan_hash: '',
        command: 'item.update',
        row_effects: [],
        event_type: '',
        audit_action: '',
        permission: '',
        external_calls: [],
      },
    }))
    const plan = await planWork(
      buildIntent({ command: 'item.update', itemId: 'i1', etag: '"v1"' }),
    )
    expect(isUnknownVerdict(plan)).toBe(true)
  })

  it('NON-FIRING: a clean plan is not reported as unknown', async () => {
    stubFetch(() => ({
      status: 200,
      body: {
        verdict: 'LIMPIO',
        code: 'ok',
        observed_at: '2026-08-10T00:00:00Z',
        checks: [],
        plan_hash: 'abc',
        command: 'item.update',
        row_effects: ['work_item'],
        event_type: 'work.item.updated',
        audit_action: 'work.item.update',
        permission: 'sessions:work:write',
        external_calls: [],
      },
    }))
    const plan = await planWork(
      buildIntent({ command: 'item.update', itemId: 'i1', etag: '"v1"' }),
    )
    expect(isUnknownVerdict(plan)).toBe(false)
    expect(plan.plan_hash).toBe('abc')
  })
})

describe('apply failure classification matches the engine codes', () => {
  // CONTROL: the operator is told which of the incompatible recoveries to take.
  // FIRES IF: the classifier goes back to testing only for status 409, which is how the
  //   first draft of this function was written. The engine's own suite
  //   (modules/sessions/work_api_test.go:292-319) says a stale If-Match is 412
  //   version_mismatch and a missing one is 428 version_required, so under that draft
  //   the single most important case fell through to 'other' with no re-read offered.
  const err = (status: number, code: string) =>
    new ApiError(
      status,
      code,
      code,
      undefined,
      {},
      {
        verdict: 'ROTO',
        code,
        error: { code, message: code },
      },
    )

  it('412 version_mismatch is a version conflict, not a generic failure', () => {
    expect(classifyApplyFailure(err(412, 'version_mismatch'))).toBe(
      'conflict-version',
    )
  })

  it('428 version_required is our own missing precondition', () => {
    expect(classifyApplyFailure(err(428, 'version_required'))).toBe(
      'version-required',
    )
  })

  it('409 idempotency_key_reused is a divergent reuse', () => {
    expect(classifyApplyFailure(err(409, 'idempotency_key_reused'))).toBe(
      'conflict-idempotency',
    )
  })

  it('NON-FIRING: a 409 from the kernel rules is a DOMAIN conflict, not a version one', () => {
    // illegal_transition must not be routed to "someone else changed this", which would
    // offer a re-read that cannot possibly help.
    expect(classifyApplyFailure(err(409, 'illegal_transition'))).toBe(
      'conflict-domain',
    )
  })

  it('an unknown verdict outranks the status: it is undetermined, not failed', () => {
    const unknown = new ApiError(
      503,
      'evidence_unavailable',
      'x',
      undefined,
      {},
      {
        verdict: 'NO_HE_PODIDO_MIRAR',
        code: 'evidence_unavailable',
        error: { code: 'evidence_unavailable', message: 'x' },
      },
    )
    expect(classifyApplyFailure(unknown)).toBe('unknown')
  })
})

describe('the idempotency key is per INTENTION, not per request', () => {
  // CONTROL: a retry must not become a second application.
  // FIRES IF: applyWork mints a key internally instead of using the intent's.
  // DOES NOT FIRE FOR: a genuinely new intention, which must get a NEW key — otherwise
  //   the "fix" would be a constant key and every distinct change would collide.
  it('two applies of the SAME intent send the SAME key', async () => {
    stubFetch(() => ({
      status: 200,
      body: { verdict: 'LIMPIO', code: 'ok' },
      headers: { ETag: '"v2"' },
    }))
    const intent = buildIntent({
      command: 'item.update',
      itemId: 'i1',
      etag: '"v1"',
    })
    await applyWork(intent)
    await applyWork(intent) // the retry
    const keys = captured.map((c) => c.headers['idempotency-key'])
    expect(keys).toHaveLength(2)
    expect(keys[0]).toBe(keys[1])
    expect(keys[0]).toMatch(/^[0-9a-f-]{36}$/)
  })

  it('NON-FIRING: a second INTENTION gets its own key', () => {
    const a = buildIntent({
      command: 'item.update',
      itemId: 'i1',
      etag: '"v1"',
    })
    const b = buildIntent({
      command: 'item.update',
      itemId: 'i1',
      etag: '"v1"',
    })
    expect(a.key).not.toBe(b.key)
  })
})

describe('one work intention keeps one tenant through plan and apply', () => {
  /**
   * CONTROL: a WorkIntent is created once, then the SAME intent is planned and
   * applied. A 401 between those phases can rotate credentials while the operator
   * changes the global tenant; neither the replay nor the later write may follow it.
   *
   * MUTANT: remove `tenant: intent.tenant` from `send`. This still compiles, the
   * replay remains protected by the client-level pin, and the apply moves to B — so
   * only the third header changes and this test goes red for the intended reason.
   */
  it('applies in A after a 401 replay changes the global tenant to B', async () => {
    let liveTenant: string | null = 'tenant-A'
    let refreshes = 0
    configureApiClient({
      getToken: () => 'olvs_test',
      getTenant: () => liveTenant,
      refreshSession: async () => {
        refreshes++
        liveTenant = 'tenant-B'
        return true
      },
    })

    let request = 0
    stubFetch(() => {
      request++
      if (request === 1) {
        return {
          status: 401,
          body: {
            error: { code: 'unauthenticated', message: 'expired' },
          },
        }
      }
      if (request === 2) {
        return {
          body: {
            verdict: 'LIMPIO',
            code: 'ok',
            plan_hash: 'plan-A',
            command: 'item.complete',
            row_effects: [],
            external_calls: [],
          },
        }
      }
      return {
        body: { verdict: 'LIMPIO', code: 'ok' },
        headers: { ETag: '"v2"' },
      }
    })

    const intent = buildIntent({
      command: 'item.complete',
      itemId: 'i1',
      etag: '"v1"',
      tenant: 'tenant-A',
    })

    const plan = await planWork(intent)
    await applyWork(intent, plan.plan_hash)

    // Positive controls: the first two requests are the 401 leg and its replay;
    // the third is a distinct apply of the SAME production intention.
    expect(refreshes).toBe(1)
    expect(captured).toHaveLength(3)
    expect(captured.map((c) => c.url)).toEqual([
      '/v1/m/sessions/work-items/i1/transitions?mode=plan',
      '/v1/m/sessions/work-items/i1/transitions?mode=plan',
      '/v1/m/sessions/work-items/i1/transitions?mode=apply',
    ])
    expect(captured.map((c) => c.headers['x-olivares-tenant'])).toEqual([
      'tenant-A',
      'tenant-A',
      'tenant-A',
    ])
  })
})

describe('If-Match rides every apply but create', () => {
  // CONTROL: optimistic concurrency is actually asserted.
  // FIRES IF: the If-Match header stops being sent — the change would then silently
  //   overwrite a concurrent writer instead of being refused with 412.
  it('sends the ETag verbatim on a non-create apply', async () => {
    stubFetch(() => ({ status: 200, body: {}, headers: { ETag: '"v3"' } }))
    await applyWork(
      buildIntent({ command: 'item.complete', itemId: 'i1', etag: '"v2"' }),
    )
    expect(captured[0].headers['if-match']).toBe('"v2"')
  })

  it('NON-FIRING: create sends NO If-Match, because there is no prior version', async () => {
    stubFetch(() => ({ status: 200, body: {}, headers: { ETag: '"v1"' } }))
    await applyWork(
      buildIntent({ command: 'item.create', body: { title: 'x' } }),
    )
    expect(captured[0].headers['if-match']).toBeUndefined()
    expect(captured[0].headers['idempotency-key']).toBeDefined()
  })
})

describe('replay is reported as replay', () => {
  // CONTROL: "already applied" never renders as a fresh success.
  // FIRES IF: the Idempotency-Replayed header stops being read. The body is
  //   byte-identical to the original apply by design (CommandResult.Replayed is
  //   json:"-"), so there is nothing else to detect it by.
  it('reads Idempotency-Replayed from the response headers', async () => {
    stubFetch(() => ({
      status: 200,
      body: { verdict: 'LIMPIO', code: 'ok', command_id: 'c1' },
      headers: { ETag: '"v2"', 'Idempotency-Replayed': 'true' },
    }))
    const out = await applyWork(
      buildIntent({ command: 'item.complete', itemId: 'i1', etag: '"v1"' }),
    )
    expect(out.replayed).toBe(true)
    expect(out.etag).toBe('"v2"')
  })

  it('NON-FIRING: a first apply is not reported as a replay', async () => {
    stubFetch(() => ({ status: 200, body: {}, headers: { ETag: '"v2"' } }))
    const out = await applyWork(
      buildIntent({ command: 'item.complete', itemId: 'i1', etag: '"v1"' }),
    )
    expect(out.replayed).toBe(false)
  })
})

describe('mode is mandatory on every mutation', () => {
  // CONTROL: validate/plan/apply are distinct phases the engine requires
  //   (work_api.go:140-144 answers mode_required to anything else).
  it('each phase sends its own mode', async () => {
    stubFetch(() => ({
      status: 200,
      body: { verdict: 'LIMPIO', code: 'ok', row_effects: [] },
    }))
    const intent = buildIntent({
      command: 'item.complete',
      itemId: 'i1',
      etag: '"v1"',
    })
    await planWork(intent)
    await applyWork(intent)
    expect(captured[0].url).toContain('mode=plan')
    expect(captured[1].url).toContain('mode=apply')
  })
})

describe('archived is a TRI-STATE, and absent is not false', () => {
  // CONTROL: the operator sees the list they think they are reading.
  // FIRES IF: the param defaults to false — the ordinary list-UI habit — which would
  //   hide archived work behind a filter nobody chose (work_api.go: archived=false is
  //   `archived_at IS NULL`, absent is neither).
  it('omits the key entirely when the filter is not set', async () => {
    stubFetch(() => ({
      status: 200,
      body: { items: [], next_cursor: '', has_more: false },
    }))
    await listWorkItems({}, tenantOptions)
    expect(captured[0].url).not.toContain('archived')
  })

  it('NON-FIRING: false and true are BOTH sent when chosen, and differ', async () => {
    stubFetch(() => ({
      status: 200,
      body: { items: [], next_cursor: '', has_more: false },
    }))
    await listWorkItems({ archived: false }, tenantOptions)
    await listWorkItems({ archived: true }, tenantOptions)
    expect(captured[0].url).toContain('archived=false')
    expect(captured[1].url).toContain('archived=true')
  })
})

describe('the decision list never asks a contradictory question', () => {
  // CONTROL: effective=true&revoked=true is invalid_command by contract, and a history
  //   row must not be labelled with current state.
  it('history sends NEITHER boolean', async () => {
    stubFetch(() => ({
      status: 200,
      body: { items: [], next_cursor: '', has_more: false },
    }))
    await listDecisions({ view: 'history' }, tenantOptions)
    expect(captured[0].url).not.toContain('effective')
    expect(captured[0].url).not.toContain('revoked')
  })

  it('each projection sends exactly ONE boolean', async () => {
    stubFetch(() => ({
      status: 200,
      body: { items: [], next_cursor: '', has_more: false },
    }))
    await listDecisions({ view: 'effective' }, tenantOptions)
    await listDecisions({ view: 'revoked' }, tenantOptions)
    expect(captured[0].url).toContain('effective=true')
    expect(captured[0].url).not.toContain('revoked')
    expect(captured[1].url).toContain('revoked=true')
    expect(captured[1].url).not.toContain('effective')
  })

  // ⚠ These two cells were WRONG and still green, which is the lesson worth keeping.
  // They used `head_state`, a field the engine never sends (the projected column is
  // `state`, work_schema.go:116) — and the `as WorkDecision` cast made the test agree
  // with the mistaken TYPE instead of with the engine. A cast turns a contract check
  // into a restatement of the author's belief. The engine-anchored guard is
  // api.contract.test.ts, which reads the Go source; these cells only pin the predicate.
  it('a HISTORY row does not attribute current state — the engine omits the projection', () => {
    const historyRow = { id: 'd1', decision_key: 'k' } as WorkDecision
    expect(attributesCurrentState(historyRow)).toBe(false)
  })

  it('NON-FIRING: a projected row DOES attribute state, or the badge could never show', () => {
    const projected = {
      id: 'd1',
      decision_key: 'k',
      state: 'effective',
    } as WorkDecision
    expect(attributesCurrentState(projected)).toBe(true)
  })
})

describe('the THIRD door: an unknown verdict on a READ', () => {
  // CONTROL: a read that could not be completed is not collapsed into the generic
  // "server error, retry" card the rest of the console uses.
  //
  // WHY IT NEEDS ITS OWN CELL: the shared AsyncSection maps a query to loading /
  // forbidden / error / data (features/_intel/async.tsx). That is four states for
  // THREE outcomes plus loading, and ROTO and NO_HE_PODIDO_MIRAR both land on
  // "error". Nothing was reading the verdict on the read path until WorkSection.
  //
  // FIRES IF: WorkSection stops checking the verdict before delegating.
  // DOES NOT FIRE FOR: an ordinary ROTO read, which must KEEP the retryable card —
  //   routing every failure to "could not look" would be the mirror defect.
  it('a 503 list read carrying the verdict is recognised as unknown', async () => {
    stubFetch(() => ({
      status: 503,
      body: {
        verdict: 'NO_HE_PODIDO_MIRAR',
        code: 'observation_unavailable',
        error: {
          code: 'observation_unavailable',
          message: 'observation_unavailable',
        },
      },
    }))
    await expect(listWorkItems({}, tenantOptions)).rejects.toSatisfy(
      (e: unknown) => isUnknownVerdict(e),
    )
  })

  it('NON-FIRING: a ROTO read stays an ordinary failure, not an unknown one', async () => {
    stubFetch(() => ({
      status: 400,
      body: {
        verdict: 'ROTO',
        code: 'invalid_command',
        error: { code: 'invalid_command', message: 'invalid_command' },
      },
    }))
    await expect(listWorkItems({}, tenantOptions)).rejects.toSatisfy(
      (e: unknown) => !isUnknownVerdict(e),
    )
  })
})

describe('the fields the engine will reject the command without', () => {
  // CONTROL: an action offered in the UI can actually succeed.
  //
  // WHY: the first pass shipped SIX inoperable buttons. block/fail/cancel were sent with
  // an empty body against work_state.go:292-294, which requires boundedToken(Code,64) and
  // boundedText(Reason,1,2048) — both reject empty, so every press was a certain 400.
  // The three acceptance verdicts were sent as a bare {state} against :318-321, which
  // requires the acceptance ARRAY, plus evidence (:196-201). A button that always fails
  // is worse than an absent one; it tells the operator the product is broken.
  it('block, fail and cancel declare code and reason as required', () => {
    for (const cmd of ['item.block', 'item.fail', 'item.cancel'] as const) {
      const f = requiredFieldsFor(cmd)
      expect(f.map((x) => x.name).sort()).toEqual(['code', 'reason'])
      expect(f.every((x) => x.required && x.path === 'root')).toBe(true)
    }
  })

  it('acceptance evidence follows the STATE, exactly as the engine grades it', () => {
    // passed: ref + hash. failed: ref only. waived: neither (an optional waiver id).
    expect(
      requiredFieldsFor('acceptance.evaluate', 'passed').map((f) => f.name),
    ).toEqual(['evidence_ref', 'evidence_hash'])
    expect(
      requiredFieldsFor('acceptance.evaluate', 'failed').map((f) => f.name),
    ).toEqual(['evidence_ref'])
    expect(
      requiredFieldsFor('acceptance.evaluate', 'waived').filter(
        (f) => f.required,
      ),
    ).toHaveLength(0)
  })

  it('NON-FIRING: an ordinary transition demands nothing extra', () => {
    // If this ever returned fields, every plain transition would sprout a form.
    expect(requiredFieldsFor('item.complete')).toEqual([])
    expect(requiredFieldsFor('item.ready')).toEqual([])
  })

  it('acceptance values fold INSIDE the array, not at the root', () => {
    // The level is part of the contract: a field at the wrong level is rejected exactly
    // like a missing one, and silently.
    const body = acceptanceEvaluateBody('passed', '', '', '')
    const folded = foldFields(
      body,
      requiredFieldsFor('acceptance.evaluate', 'passed'),
      {
        evidence_ref: 'run://ci/1',
        evidence_hash: 'sha256:abc',
      },
    )
    expect(folded).toEqual({
      acceptance: [
        {
          state: 'passed',
          evidence_ref: 'run://ci/1',
          evidence_hash: 'sha256:abc',
        },
      ],
    })
    expect('evidence_ref' in folded).toBe(false)
  })

  it('root values fold at the root', () => {
    const folded = foldFields(
      { command: 'item.block' },
      requiredFieldsFor('item.block'),
      {
        code: 'blocked_on_review',
        reason: 'waiting for the reviewer',
      },
    )
    expect(folded).toEqual({
      command: 'item.block',
      code: 'blocked_on_review',
      reason: 'waiting for the reviewer',
    })
  })
})

describe('the plan the operator approved is the plan that gets applied', () => {
  // CONTROL: apply is bound to the plan shown. The first pass DISPLAYED the canonical
  // hash and never sent it, so approval and application were related only by luck.
  // FIRES IF: If-Plan-Hash stops being sent.
  it('sends the plan hash as If-Plan-Hash', async () => {
    stubFetch(() => ({ status: 200, body: {}, headers: { ETag: '"v2"' } }))
    await applyWork(
      buildIntent({ command: 'item.complete', itemId: 'i1', etag: '"v1"' }),
      'a1b2c3',
    )
    expect(captured[0].headers['if-plan-hash']).toBe('a1b2c3')
  })

  it('NON-FIRING: no hash means no header, not an empty one', async () => {
    // An empty If-Plan-Hash is not "no expectation" — work_api.go:155-165 only reads the
    // header when non-empty, and sending "" would be a header the engine must parse.
    stubFetch(() => ({ status: 200, body: {}, headers: { ETag: '"v2"' } }))
    await applyWork(
      buildIntent({ command: 'item.complete', itemId: 'i1', etag: '"v1"' }),
    )
    expect(captured[0].headers['if-plan-hash']).toBeUndefined()
  })

  it('plan_changed is its own recovery, not a version conflict', () => {
    // Both are 412. They need OPPOSITE actions: re-plan vs re-read.
    const e = new ApiError(
      412,
      'plan_changed',
      'plan_changed',
      undefined,
      {},
      {
        verdict: 'ROTO',
        code: 'plan_changed',
        error: { code: 'plan_changed', message: 'plan_changed' },
      },
    )
    expect(classifyApplyFailure(e)).toBe('plan-changed')
  })
})

// ---------------------------------------------------------------------------
// K2 leases (C07-01)
// ---------------------------------------------------------------------------

describe('lease client', () => {
  /**
   * THE CONTROL: the list query is built from the CLOSED filter set. The engine's allowlist is
   * strict — an unknown key, or a repeated one, is 400 invalid_command and fails the whole
   * request (`work_api.go:649-668`), so a stray param is a broken screen, not an ignored hint.
   *
   * THE MUTATION: forwarding the caller's object wholesale. The extra key would appear.
   * THE NON-FIRING DIRECTION: the six legitimate keys must still be sent — asserted below, so
   * a client that emitted nothing at all would fail this cell too.
   */
  it('emits only the filters the engine allows', async () => {
    stubFetch(() => ({ body: { items: [], next_cursor: '', has_more: false } }))
    await listLeases(
      {
        state: 'held',
        holder_sid: 's1',
        limit: 25,
        // @ts-expect-error — a caller reaching past the closed type is exactly the case
        q: 'injected',
      },
      tenantOptions,
    )
    const q = new URL(captured[0]!.url, 'http://x').searchParams
    expect(q.get('state')).toBe('held')
    expect(q.get('holder_sid')).toBe('s1')
    expect(q.get('limit')).toBe('25')
    expect(q.has('q')).toBe(false)
  })
})

// ---------------------------------------------------------------------------
// What the engine requires of a lease command, and how the value reaches it
// ---------------------------------------------------------------------------

describe('lease command fields', () => {
  /**
   * THE CONTROL THE CONTRAST FOUND MISSING, and it was right: a mutant that emptied
   * `requiredFieldsFor` for the lease commands left all 95 cells green, because nothing read
   * the table. A dialog that asks for nothing sends an empty body and the engine answers
   * invalid_command — six buttons that plan and always fail.
   *
   * The six do NOT ask for the same things (work_state.go:371-404), so this pins each one:
   * asserting "some fields" would pass on a table that returned the same list for all of them.
   */
  it('asks for exactly what each lease command requires', () => {
    const names = (c: Parameters<typeof requiredFieldsFor>[0]) =>
      requiredFieldsFor(c)
        .map((f) => f.name)
        .sort()
    expect(names('lease.acquire')).toEqual(['holder_sid'])
    expect(names('lease.renew')).toEqual(['fence', 'holder_sid'])
    expect(names('lease.release')).toEqual(['fence', 'holder_sid'])
    expect(names('lease.takeover')).toEqual(['fence', 'holder_sid'])
    expect(names('lease.revoke')).toEqual(['fence', 'reason'])
    expect(names('lease.clock_rebase')).toEqual(['decision_id', 'evidence_ref'])
  })

  /**
   * THE NON-FIRING DIRECTION: `ttl_seconds` is absent ON PURPOSE. validWorkLeaseTTL accepts 0
   * (work_state.go:424-426), so demanding it would invent a requirement the engine does not
   * have and refuse a command it would take. A table that asked for everything would satisfy
   * the case above and fail this one.
   */
  it('does not demand the optional ttl', () => {
    for (const c of [
      'lease.acquire',
      'lease.renew',
      'lease.takeover',
    ] as const) {
      expect(requiredFieldsFor(c).map((f) => f.name)).not.toContain(
        'ttl_seconds',
      )
    }
  })

  /**
   * THE CONTROL: a `number` field reaches the engine as a NUMBER. foldFields wrote every value
   * as a string, and the engine's int64 rejects that as invalid_command — the operator fills
   * the field correctly and the command is still refused, which is the worst kind of refusal
   * because the screen looks right.
   *
   * THE MUTATION: drop the coercion. `fence` arrives as "7" and this fires.
   * THE NON-FIRING DIRECTION: text fields must stay strings — asserted in the same case, so a
   * fold that coerced everything would not pass either.
   */
  it('folds a number field as a number and a text field as text', () => {
    const out = foldFields({}, requiredFieldsFor('lease.revoke'), {
      fence: '7',
      reason: 'because',
    })
    expect(out.fence).toBe(7)
    expect(out.reason).toBe('because')
  })
})
