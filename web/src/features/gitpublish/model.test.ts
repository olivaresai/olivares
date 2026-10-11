// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { readFileSync, readdirSync } from 'node:fs'
import { join, resolve } from 'node:path'
import { describe, expect, it } from 'vitest'
import { ApiError, NetworkError } from '@/lib/api/errors'
import { LANGUAGE_CODES } from '@/lib/i18n'
import {
  REFUSAL_CODES,
  actionAuthority,
  intentActions,
  isKnownRefusal,
  newOperationId,
  outcomeOf,
  refusalKey,
} from './model'
import type { PublicationIntent } from './types'

function intent(over: Partial<PublicationIntent> = {}): PublicationIntent {
  return {
    id: 'in-1',
    target_id: 'tg-1',
    target_version: 1,
    effect: 'push',
    operation_id: 'op-1',
    attempt: 1,
    state: 'applied',
    receipt: 'caused',
    requested: { ref: 'refs/heads/agents/fix', commit: 'a'.repeat(40) },
    observed: { present: true, merged: false },
    acknowledged: { acknowledged: true, status: 200 },
    ...over,
  }
}

function refusal(status: number, body: unknown): ApiError {
  return new ApiError(status, 'internal', 'refused', undefined, {}, body)
}

describe('outcomeOf', () => {
  it('reads a settled receipt as settled', () => {
    expect(outcomeOf({ data: intent() })).toEqual({
      kind: 'settled',
      intent: intent(),
    })
  })

  it('reads an uncertain receipt, or a replay still dispatching, as uncertain', () => {
    const uncertain = intent({ state: 'uncertain', receipt: 'none' })
    expect(outcomeOf({ data: uncertain }).kind).toBe('uncertain')
    // A replay that does not own the dispatch answers "dispatching" over a stored state.
    const replay = intent({ state: 'dispatching', answer: 'dispatching' })
    expect(outcomeOf({ data: replay }).kind).toBe('uncertain')
  })

  it('reads the 409 whose body is a rejected intent as a rejection, not a refusal', () => {
    const rejected = intent({ state: 'rejected', reason: 'stale_lease' })
    expect(outcomeOf({ error: refusal(409, rejected) })).toEqual({
      kind: 'rejected',
      intent: rejected,
    })
  })

  it('reads the flat refusal body the module writes, with the intent it names', () => {
    expect(
      outcomeOf({
        error: refusal(409, { error: 'unresolved_intent', intent_id: 'in-9' }),
      }),
    ).toEqual({
      kind: 'refused',
      code: 'unresolved_intent',
      status: 409,
      intentId: 'in-9',
    })
    expect(
      outcomeOf({ error: refusal(422, { error: 'ref_not_allowed' }) }),
    ).toEqual({ kind: 'refused', code: 'ref_not_allowed', status: 422 })
  })

  it('falls back to the envelope code when the body is the core envelope', () => {
    const stepUp = new ApiError(403, 'step_up_required', 'step up')
    expect(outcomeOf({ error: stepUp })).toEqual({
      kind: 'refused',
      code: 'step_up_required',
      status: 403,
    })
  })

  it('reads a lost answer as unknown: the request may have been recorded', () => {
    expect(outcomeOf({ error: new NetworkError('offline') })).toEqual({
      kind: 'unknown',
    })
  })
})

describe('intentActions', () => {
  const all = () => true

  it('offers reconcile and abandon for an uncertain intent, and never a retry', () => {
    const actions = intentActions(intent({ state: 'uncertain' }), all)
    expect(actions).toEqual({ reconcile: true, abandon: true })
    expect(Object.keys(actions)).not.toContain('retry')
  })

  it('offers only abandon for an intent that was not dispatched or is still dispatching', () => {
    expect(intentActions(intent({ state: 'not_dispatched' }), all)).toEqual({
      reconcile: false,
      abandon: true,
    })
    expect(intentActions(intent({ state: 'dispatching' }), all)).toEqual({
      reconcile: false,
      abandon: true,
    })
  })

  it('offers nothing on a settled intent', () => {
    for (const state of [
      'applied',
      'adopted',
      'rejected',
      'abandoned',
    ] as const) {
      expect(intentActions(intent({ state }), all)).toEqual({
        reconcile: false,
        abandon: false,
      })
    }
  })

  it('asks for the intent’s own effect permission to reconcile and target administration to abandon', () => {
    const asked: string[] = []
    const can = (p: string) => {
      asked.push(p)
      return p === 'gitpublish:merge:admin'
    }
    expect(
      intentActions(intent({ state: 'uncertain', effect: 'merge' }), can),
    ).toEqual({ reconcile: true, abandon: false })
    expect(asked).toContain('gitpublish:merge:admin')
    expect(asked).toContain('gitpublish:target:admin')
  })
})

describe('actionAuthority', () => {
  it('names the permission and the AAL3 floor the engine mounts each action with', () => {
    expect(actionAuthority('push')).toEqual({
      permission: 'gitpublish:push:write',
      aal3: false,
    })
    expect(actionAuthority('pull_request')).toEqual({
      permission: 'gitpublish:pull_request:write',
      aal3: false,
    })
    expect(actionAuthority('merge')).toEqual({
      permission: 'gitpublish:merge:admin',
      aal3: true,
    })
    expect(actionAuthority('target')).toEqual({
      permission: 'gitpublish:target:admin',
      aal3: true,
    })
    expect(actionAuthority('abandon')).toEqual({
      permission: 'gitpublish:target:admin',
      aal3: true,
    })
  })
})

describe('newOperationId', () => {
  it('matches the engine’s operation id pattern and is fresh on every call', () => {
    const a = newOperationId()
    const b = newOperationId()
    expect(a).toMatch(/^[A-Za-z0-9._:-]{1,128}$/)
    expect(a).not.toBe(b)
  })
})

describe('the refusal vocabulary', () => {
  it.each(LANGUAGE_CODES)(
    'has %s copy for every code the module can answer',
    (language) => {
      const { refusals } = JSON.parse(
        readFileSync(resolve(__dirname, `i18n/${language}.json`), 'utf8'),
      ) as { refusals: Record<string, string> }
      for (const code of REFUSAL_CODES) expect(refusals[code]).toBeTruthy()
      expect(refusals.unknown).toBeTruthy()
      expect(refusals.separation_of_duty).toBeTruthy()
    },
  )

  it('describes separation of duty while keeping unknown refusals generic', () => {
    expect(refusalKey('separation_of_duty')).toBe('refusals.separation_of_duty')
    expect(refusalKey('future_refusal')).toBe('refusals.unknown')
  })

  it('names every code the engine module refuses with', () => {
    // A code added to the module without console copy reads as "a code this console does
    // not describe" (the session_source_* codes of the session-run push did).
    const dir = resolve(__dirname, '../../../../modules/gitpublish')
    const engine = new Set(
      readdirSync(dir)
        .filter((n) => n.endsWith('.go') && !n.endsWith('_test.go'))
        .flatMap((n) => [
          ...readFileSync(join(dir, n), 'utf8').matchAll(
            /refuse\("([a-z_]+)"/g,
          ),
        ])
        .map((m) => m[1]!),
    )
    expect(engine.size).toBeGreaterThan(20)
    expect([...engine].filter((c) => !isKnownRefusal(c))).toEqual([])
  })
})
