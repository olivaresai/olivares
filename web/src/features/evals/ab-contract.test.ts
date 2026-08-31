// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The CONSOLE half of the A/B contract, and the reason it is a separate file from
// evals.test.tsx: that suite does `vi.mock('./api')`, so it asserts against a
// double that accepts whatever the view hands it. The double was happy for as long
// as the real endpoint was answering 400 — `msw` is not used anywhere in web/src,
// so NOTHING measured this seam and that is why it was never seen.
//
// Here the real `evalsApi.ab` runs against the real `http.post`, and only `fetch`
// is stubbed, so the assertion is on the BYTES that would leave the browser. Those
// bytes are compared against modules/evals/testdata/ab_request_console.json — the
// same file the engine test posts to the real handler
// (modules/evals/ab_console_contract_test.go). One fixture, two sides: if either
// side drifts, one of the two tests fails.
import { existsSync, readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { configureApiClient } from '@/lib/api/client'
import { evalsApi } from './api'
import type { AbRequest } from './types'

// vitest runs with the `web/` project root as cwd. If the fixture ever moves, this
// must fail LOUDLY and name the path — a test that silently fell back to a literal
// would go on passing while the two sides drifted apart, which is the exact
// failure this file exists to prevent.
const FIXTURE = resolve(
  process.cwd(),
  '../modules/evals/testdata/ab_request_console.json',
)
if (!existsSync(FIXTURE)) {
  throw new Error(
    `A/B contract fixture not found at ${FIXTURE}. It is shared with ` +
      `modules/evals/ab_console_contract_test.go and must not be inlined here.`,
  )
}
const fixture = JSON.parse(readFileSync(FIXTURE, 'utf8')) as AbRequest

let sentBody: unknown
let sentUrl = ''

function stubFetch() {
  globalThis.fetch = vi.fn(async (url: string, init?: RequestInit) => {
    sentUrl = String(url)
    sentBody =
      init?.body === undefined ? undefined : JSON.parse(String(init.body))
    return new Response(
      JSON.stringify({ variants: [], winner: '', delta: 0, tie: true }),
      { status: 200, headers: { 'Content-Type': 'application/json' } },
    )
  }) as never
}

afterEach(() => {
  configureApiClient({
    getToken: () => null,
    getTenant: () => null,
    onUnauthorized: () => {},
  })
  sentBody = undefined
  sentUrl = ''
})

describe('POST /ab — the body the console actually puts on the wire', () => {
  it('sends the shape the engine decodes, byte-for-byte with the engine fixture', async () => {
    stubFetch()
    await evalsApi.ab(fixture)

    expect(sentUrl).toContain('/v1/m/evals/ab')
    // The whole point: what left the client is what the engine test posted.
    expect(sentBody).toEqual(fixture)
  })

  it('keeps suite_ref at the TOP level and the variants bare', async () => {
    stubFetch()
    await evalsApi.ab(fixture)
    const body = sentBody as Record<string, unknown>

    // `suite_ref` at the top level, NOT inside a/b.
    expect(body.suite_ref).toBe(fixture.suite_ref)
    // A variant carries a label and outputs and NOTHING else. This is the
    // assertion the old console failed: it sent a whole RunInput here, and the
    // engine decodes with DisallowUnknownFields, so the FIRST unknown key inside
    // `a` killed the decode and answered 400 "invalid JSON body".
    for (const key of ['a', 'b'] as const) {
      expect(Object.keys(body[key] as object).sort()).toEqual([
        'label',
        'outputs',
      ])
    }
    // The field the console never sent: without it the operator cannot ask for
    // half the endpoint's function (modules/evals/ab.go:43).
    expect(body.pairwise).toBe(true)
  })

  // Non-firing direction: the assertion above must fail for the shape that used
  // to be sent, or it is not measuring the contract at all.
  it('would reject the old {a: RunInput, b: RunInput} shape', async () => {
    stubFetch()
    const old = {
      a: {
        suite_ref: 's-1',
        subject_kind: 'agent',
        subject_ref: 'v6',
        outputs: {},
      },
      b: {
        suite_ref: 's-1',
        subject_kind: 'agent',
        subject_ref: 'v5',
        outputs: {},
      },
    }
    await evalsApi.ab(old as unknown as AbRequest)
    const body = sentBody as Record<string, unknown>

    expect(body.suite_ref).toBeUndefined()
    expect(Object.keys(body.a as object).sort()).not.toEqual([
      'label',
      'outputs',
    ])
  })
})
