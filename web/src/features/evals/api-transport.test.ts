// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { afterEach, describe, expect, it, vi } from 'vitest'
import { evalsApi } from './api'

let sent = ''
function capture() {
  globalThis.fetch = vi.fn(async (url: string) => {
    sent = String(url)
    return new Response(JSON.stringify({ items: [], has_more: false }), {
      status: 200,
      headers: { 'Content-Type': 'application/json' },
    })
  }) as never
}
const query = () => new URL(sent, 'http://test').searchParams
afterEach(() => (sent = ''))

describe('evals list ceilings travel on the wire', () => {
  it.each([
    ['suites', () => evalsApi.suites(), '1000'],
    ['runs', () => evalsApi.runs(), '1000'],
    [
      'calibration items',
      () => evalsApi.calibrationItems('gold', { tenant: 't-transporte' }),
      '25',
    ],
    ['calibration reports', () => evalsApi.calibrationReports(), '1000'],
    ['gates', () => evalsApi.gates(), '1000'],
  ])('%s sends its exact ceiling', async (_name, call, ceiling) => {
    capture()
    await call()
    expect(query().get('limit')).toBe(ceiling)
  })

  it.each([
    ['scorecards', () => evalsApi.scorecards('agent')],
    ['run results', () => evalsApi.runResults('run-1')],
    ['suite cases', () => evalsApi.suiteCases('suite-1')],
  ])(
    '%s remains a complete route without a decorative limit',
    async (_name, call) => {
      capture()
      await call()
      expect(query().get('limit')).toBeNull()
    },
  )

  it('keeps filters and lets an explicit runs ceiling win', async () => {
    capture()
    await evalsApi.runs({ suite_ref: 'suite-1', limit: 7 })
    expect(query().get('suite_ref')).toBe('suite-1')
    expect(query().get('limit')).toBe('7')
  })
})

// The engine's scorecard rows carry suite_ref, subject_ref and prompt_variant and no `key`
// (modules/evals/scorecards.go scorecardDTO), and it ignores `by`: it always groups by suite, subject and
// variant. The card is named from those fields, so the name does not change with the grouping asked for.
describe('scorecards are named from the fields the engine sends', () => {
  const row = {
    suite_ref: '01a1-suite-one',
    subject_ref: 'pr-reviewer',
    subject_kind: 'agent',
    runs: 2,
    pass_rate: 0.8,
    trend: [{ at: 'a', score: 1, pass_rate: 1 }],
  }
  function answer(items: unknown[], extra: object = {}) {
    globalThis.fetch = vi.fn(
      async () =>
        new Response(JSON.stringify({ items, has_more: false, ...extra }), {
          status: 200,
          headers: { 'Content-Type': 'application/json' },
        }),
    ) as never
  }

  it.each(['agent', 'model', 'suite', 'prompt_variant'] as const)(
    'names the card by its subject whatever grouping %s was asked for',
    async (by) => {
      answer([row])
      const page = await evalsApi.scorecards(by)
      expect(page.items[0].name).toBe('pr-reviewer')
    },
  )

  it('puts a prompt variant in the name', async () => {
    answer([{ ...row, prompt_variant: 'v2' }])
    expect((await evalsApi.scorecards('agent')).items[0].name).toBe(
      'pr-reviewer · v2',
    )
  })

  it('never leaves the name empty: a run with no subject is named by its suite', async () => {
    answer([{ ...row, subject_ref: '' }])
    expect((await evalsApi.scorecards('agent')).items[0].name).toBe(
      '01a1-suite-one',
    )
  })

  it('tells apart two cards of one subject in two suites', async () => {
    answer([row, { ...row, suite_ref: '01a1-suite-two' }])
    const keys = (await evalsApi.scorecards('agent')).items.map((c) => c.key)
    expect(new Set(keys).size).toBe(2)
    expect(keys[0]).toBe(JSON.stringify([row.suite_ref, row.subject_ref, '']))
    expect(keys[1]).toBe(
      JSON.stringify(['01a1-suite-two', row.subject_ref, '']),
    )
  })

  it('keeps the identity when a new sibling arrives or rows reorder', async () => {
    answer([row])
    const original = (await evalsApi.scorecards('agent')).items[0].key
    answer([{ ...row, suite_ref: '01a1-suite-two' }, row])
    const refreshed = (await evalsApi.scorecards('agent')).items
    expect(refreshed[1].key).toBe(original)
    expect(refreshed[0].key).not.toBe(original)
  })

  it('does not join tuple fields with an ambiguous display separator', async () => {
    answer([
      { ...row, subject_ref: 'reviewer', prompt_variant: 'baseline · tuned' },
      { ...row, subject_ref: 'reviewer · baseline', prompt_variant: 'tuned' },
      {
        ...row,
        suite_ref: 'other',
        subject_ref: 'reviewer',
        prompt_variant: 'baseline · tuned',
      },
    ])
    const cards = (await evalsApi.scorecards('agent')).items
    expect(new Set(cards.map((card) => card.key)).size).toBe(3)
  })

  it('keeps the identity when the latest run changes the subject kind', async () => {
    answer([row])
    const original = (await evalsApi.scorecards('agent')).items[0].key
    answer([{ ...row, subject_kind: 'model' }])
    expect((await evalsApi.scorecards('agent')).items[0].key).toBe(original)
  })

  it('keeps a legacy key as display text, and every other field of the page', async () => {
    answer([{ ...row, key: 'sent-by-engine' }], { next_cursor: 'c1' })
    const page = await evalsApi.scorecards('agent')
    expect(page.items[0]).toMatchObject({
      name: 'sent-by-engine',
      key: JSON.stringify([row.suite_ref, row.subject_ref, '']),
      runs: 2,
      pass_rate: 0.8,
    })
    expect(page.items[0].trend).toHaveLength(1)
    expect(page).toMatchObject({ has_more: false, next_cursor: 'c1' })
  })
})
