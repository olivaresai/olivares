// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The seam the other two cells leave open, closed. ab-contract.test.ts calls
// `evalsApi.ab` DIRECTLY with the shared fixture, and evals.test.tsx asserts the
// view against a literal written next to it — so between them the VIEW could start
// building a differently-shaped body and both would stay green while the engine
// went back to answering 400.
//
// Here nothing between the operator and the wire is mocked: the real AbTab builds
// the body, the real evalsApi sends it through the real http.post, and only
// `fetch` is stubbed. The captured body's SHAPE is then compared against
// modules/evals/testdata/ab_request_console.json — the same file the engine test
// posts to the real handler. That is the whole chain, pinned end to end.
import { existsSync, readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { renderIntel, screen, userEvent, waitFor } from '@/test/intel'
import '@/features/_intel'
import { configureApiClient } from '@/lib/api/client'
import { suitesFixture } from './fixtures'
import './i18n'

vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({ activeTenant: 't1', can: () => true }),
}))

import { EvalsView } from './evals-view'

const FIXTURE = resolve(
  process.cwd(),
  '../modules/evals/testdata/ab_request_console.json',
)
if (!existsSync(FIXTURE)) {
  throw new Error(`A/B contract fixture not found at ${FIXTURE}`)
}
const fixture = JSON.parse(readFileSync(FIXTURE, 'utf8')) as Record<
  string,
  unknown
>

let abBody: Record<string, unknown> | undefined

beforeEach(() => {
  abBody = undefined
  globalThis.fetch = vi.fn(async (url: string, init?: RequestInit) => {
    const u = String(url)
    let payload: unknown = { items: [], has_more: false }
    if (u.includes('/evals/suites')) {
      payload = { items: suitesFixture, has_more: false }
    } else if (u.includes('/evals/ab')) {
      abBody = JSON.parse(String(init?.body)) as Record<string, unknown>
      payload = { variants: [], winner: '', delta: 0, tie: true }
    }
    return new Response(JSON.stringify(payload), {
      status: 200,
      headers: { 'Content-Type': 'application/json' },
    })
  }) as never
})

afterEach(() => {
  configureApiClient({
    getToken: () => null,
    getTenant: () => null,
    onUnauthorized: () => {},
  })
})

/** The key structure of a body, ignoring values: what the DECODER cares about. */
function shapeOf(body: Record<string, unknown>) {
  return {
    top: Object.keys(body).sort(),
    a: Object.keys((body.a ?? {}) as object).sort(),
    b: Object.keys((body.b ?? {}) as object).sort(),
  }
}

describe('A/B: what the VIEW puts on the wire', () => {
  it('builds the same shape the engine fixture pins', async () => {
    const user = userEvent.setup()
    renderIntel(<EvalsView />)
    await user.click(screen.getByRole('tab', { name: 'A/B' }))

    await user.click(await screen.findByRole('combobox', { name: /Suite/ }))
    await user.click(screen.getByRole('option', { name: 'code-review-rubric' }))
    await user.type(
      screen.getByRole('textbox', { name: /Variant A label/ }),
      'v6-concise',
    )
    await user.type(
      screen.getByRole('textbox', { name: /Variant B label/ }),
      'v5-verbose',
    )
    await user.type(
      screen.getByRole('textbox', { name: /Variant A outputs/ }),
      '{{"greeting": "hello", "farewell": "bye"}',
    )
    await user.type(
      screen.getByRole('textbox', { name: /Variant B outputs/ }),
      '{{"greeting": "hello there, friend", "farewell": "bye"}',
    )
    await user.click(screen.getByRole('checkbox', { name: /head-to-head/ }))
    await user.click(screen.getByRole('button', { name: 'Run comparison' }))

    await waitFor(() => expect(abBody).toBeDefined())
    const body = abBody as Record<string, unknown>

    // Same keys as the body the engine test posts and accepts — including
    // `suite_ref` at the top and NOTHING but label/outputs inside a variant.
    expect(shapeOf(body)).toEqual(shapeOf(fixture))
    // And the values the operator actually typed reached the wire intact.
    expect(body.suite_ref).toBe('suite-review-v2')
    expect(body.a).toEqual({
      label: 'v6-concise',
      outputs: { greeting: 'hello', farewell: 'bye' },
    })
    expect(body.pairwise).toBe(true)
  })
})
