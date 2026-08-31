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
