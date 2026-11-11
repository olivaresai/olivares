// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Usage › spend: the window a person chooses, what its runs used, and the file Export
// saves. A figure no run reported stays unknown, never 0.
import { describe, expect, it } from 'vitest'
import type { UsageRow } from '@/features/models/types'
import {
  byKey,
  dailyOf,
  lastDays,
  spendCsv,
  spendOf,
  spendWindow,
} from './spend'

const row = (over: Partial<UsageRow>): UsageRow => ({
  runs: 0,
  input_tokens: null,
  output_tokens: null,
  cost_micro_usd: null,
  runs_without_tokens: 0,
  runs_without_cost: 0,
  ...over,
})

describe('the window', () => {
  it('holds whole UTC days, today included, and ends at the next midnight', () => {
    const w = lastDays(7, new Date('2026-11-11T15:30:00Z'))
    expect(w.days).toEqual([
      '2026-11-05',
      '2026-11-06',
      '2026-11-07',
      '2026-11-08',
      '2026-11-09',
      '2026-11-10',
      '2026-11-11',
    ])
    expect(w.since).toBe('2026-11-05T00:00:00Z')
    expect(w.until).toBe('2026-11-12T00:00:00Z')
  })

  it('takes a chosen first and last day, and no window that ends before it starts', () => {
    expect(spendWindow('2026-10-30', '2026-11-02')?.days).toEqual([
      '2026-10-30',
      '2026-10-31',
      '2026-11-01',
      '2026-11-02',
    ])
    expect(spendWindow('2026-11-02', '2026-11-02')?.until).toBe(
      '2026-11-03T00:00:00Z',
    )
    expect(spendWindow('2026-11-03', '2026-11-02')).toBeNull()
    expect(spendWindow('', '2026-11-02')).toBeNull()
  })

  it('takes a chosen window of 366 days at most', () => {
    expect(spendWindow('2025-11-11', '2026-11-11')?.days).toHaveLength(366)
    expect(spendWindow('2025-11-10', '2026-11-11')).toBeNull()
    expect(spendWindow('1970-01-01', '2026-11-11')).toBeNull()
  })
})

describe('what the runs used', () => {
  it('adds what was reported and keeps a figure no run reported unknown', () => {
    expect(
      spendOf([
        row({
          runs: 2,
          input_tokens: 100,
          output_tokens: 10,
          cost_micro_usd: 1_500_000,
        }),
        row({ runs: 3, runs_without_tokens: 3, runs_without_cost: 3 }),
      ]),
    ).toEqual({
      runs: 5,
      input: 100,
      output: 10,
      cost: 1_500_000,
      runsWithoutTokens: 3,
      runsWithoutCost: 3,
    })
    expect(spendOf([row({ runs: 1, runs_without_cost: 1 })]).cost).toBeNull()
  })

  it('totals each key over the days, the busiest first', () => {
    const totals = byKey([
      row({ key: 'a', day: '2026-11-01', runs: 1, cost_micro_usd: 10 }),
      row({ key: 'b', day: '2026-11-01', runs: 3, cost_micro_usd: 5 }),
      row({ key: 'a', day: '2026-11-02', runs: 1, cost_micro_usd: 20 }),
      row({ day: '2026-11-02', runs: 2 }),
    ])
    expect(totals.map((t) => [t.key, t.runs, t.cost])).toEqual([
      ['b', 3, 5],
      ['a', 2, 30],
      [undefined, 2, null],
    ])
  })

  it('plots every day of the window by key, a day without runs at 0', () => {
    const { series, points } = dailyOf(
      [
        row({ key: 'claude', day: '2026-11-01', cost_micro_usd: 30 }),
        row({ key: 'codex', day: '2026-11-02', cost_micro_usd: 50 }),
        row({ key: 'claude', day: '2026-11-02', input_tokens: 7 }),
      ],
      ['2026-11-01', '2026-11-02', '2026-11-03'],
      'cost',
    )
    expect(series).toEqual([
      { id: 's0', key: 'codex' },
      { id: 's1', key: 'claude' },
    ])
    expect(points).toEqual([
      { day: '2026-11-01', s0: 0, s1: 30 },
      { day: '2026-11-02', s0: 50, s1: 0 },
      { day: '2026-11-03', s0: 0, s1: 0 },
    ])
    expect(
      dailyOf(
        [
          row({
            key: 'claude',
            day: '2026-11-01',
            input_tokens: 7,
            output_tokens: 3,
          }),
        ],
        ['2026-11-01'],
        'tokens',
      ).points,
    ).toEqual([{ day: '2026-11-01', s0: 10 }])
  })
})

describe('the exported file', () => {
  const rows = [
    row({
      key: 'p1',
      day: '2026-11-02',
      runs: 2,
      input_tokens: 100,
      output_tokens: 10,
      cost_micro_usd: 1_500_000,
    }),
    row({ key: 'p2', day: '2026-11-02', runs: 1, runs_without_cost: 1 }),
  ]
  const names: Record<string, string> = { p1: 'a, "b"', p2: '=HYPERLINK("x")' }
  const nameOf = (key?: string) => names[key ?? ''] ?? ''

  it('says what is shown, quotes what a spreadsheet would split and runs no cell as a formula', () => {
    expect(spendCsv(rows, 'profile', nameOf, true)).toBe(
      'day,profile,runs,input_tokens,output_tokens,runs_without_tokens,cost_usd,runs_without_cost\r\n' +
        '2026-11-02,"a, ""b""",2,100,10,0,1.5,0\r\n' +
        '2026-11-02,"\'=HYPERLINK(""x"")",1,,,0,,1\r\n',
    )
  })

  it('carries no money for a person Usage does not open for', () => {
    const csv = spendCsv(rows, 'profile', nameOf, false)
    expect(csv.split('\r\n')[0]).toBe(
      'day,profile,runs,input_tokens,output_tokens,runs_without_tokens',
    )
    expect(csv).not.toMatch(/cost|1\.5/)
  })
})
