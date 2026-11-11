// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Usage › spend: the window a person chooses and what the runs in it used. The sums are
// the engine's (`GET /v1/m/sessions/usage`, per UTC day); this file adds a window's days
// together and never turns a figure no run reported into 0.
import type { UsageRow } from '@/features/models/types'

/** The windows offered by name; a person may also choose a first and last day. */
export const RANGE_DAYS = [7, 14, 30] as const

/** The most days a chosen window holds: a year, a leap day included. */
export const MAX_RANGE_DAYS = 366

const DAY_MS = 86_400_000
const dayOf = (ms: number) => new Date(ms).toISOString().slice(0, 10)
const midnight = (day: string) => Date.parse(`${day}T00:00:00Z`)

/** Whole UTC days, `from` to `to` included: `since` and `until` as the read takes them,
 * and the days the chart plots. */
export interface SpendWindow {
  from: string
  to: string
  since: string
  until: string
  days: string[]
}

/** The window from a first to a last day (YYYY-MM-DD), or null for one that is not a
 * day, ends before it starts or holds more than `MAX_RANGE_DAYS`. */
export function spendWindow(from: string, to: string): SpendWindow | null {
  const start = midnight(from)
  const end = midnight(to) + DAY_MS
  if (Number.isNaN(start) || Number.isNaN(end) || start >= end) return null
  if (end - start > MAX_RANGE_DAYS * DAY_MS) return null
  const days: string[] = []
  for (let ms = start; ms < end; ms += DAY_MS) days.push(dayOf(ms))
  return {
    from,
    to,
    since: `${from}T00:00:00Z`,
    until: `${dayOf(end)}T00:00:00Z`,
    days,
  }
}

/** The day `n` days after `day`, or undefined when `day` is not a day. */
export function dayAfter(day: string, n: number): string | undefined {
  const ms = midnight(day)
  return Number.isNaN(ms) ? undefined : dayOf(ms + n * DAY_MS)
}

/** The last `count` whole UTC days, today included. */
export function lastDays(count: number, now: Date = new Date()): SpendWindow {
  const today = Date.UTC(
    now.getUTCFullYear(),
    now.getUTCMonth(),
    now.getUTCDate(),
  )
  return spendWindow(dayOf(today - (count - 1) * DAY_MS), dayOf(today))!
}

/** What a set of rows used. A sum is null when no run in the rows reported it; the runs
 * that reported none are counted, so a sum over some runs says so. */
export interface Spend {
  runs: number
  input: number | null
  output: number | null
  cost: number | null
  runsWithoutTokens: number
  runsWithoutCost: number
}

const add = (sum: number | null, n: number | null) =>
  n === null ? sum : (sum ?? 0) + n

export function spendOf(rows: readonly UsageRow[]): Spend {
  return rows.reduce<Spend>(
    (s, r) => ({
      runs: s.runs + r.runs,
      input: add(s.input, r.input_tokens),
      output: add(s.output, r.output_tokens),
      cost: add(s.cost, r.cost_micro_usd),
      runsWithoutTokens: s.runsWithoutTokens + r.runs_without_tokens,
      runsWithoutCost: s.runsWithoutCost + r.runs_without_cost,
    }),
    {
      runs: 0,
      input: null,
      output: null,
      cost: null,
      runsWithoutTokens: 0,
      runsWithoutCost: 0,
    },
  )
}

/** Each key's total over the days, the most runs first; on a tie a key before the runs
 * that recorded none, then by key, so two reads of the same numbers keep one order. */
export function byKey(rows: readonly UsageRow[]): (Spend & { key?: string })[] {
  const keys = new Map<string | undefined, UsageRow[]>()
  for (const r of rows) keys.set(r.key, [...(keys.get(r.key) ?? []), r])
  return [...keys]
    .map(([key, of]) => ({ key, ...spendOf(of) }))
    .sort(
      (a, b) =>
        b.runs - a.runs ||
        Number(a.key === undefined) - Number(b.key === undefined) ||
        (a.key ?? '').localeCompare(b.key ?? '', 'en'),
    )
}

/** What the chart plots: a run's cost, or its input and output tokens. */
export type Metric = 'cost' | 'tokens'

const valueOf = (r: UsageRow, metric: Metric) =>
  metric === 'cost'
    ? (r.cost_micro_usd ?? 0)
    : (r.input_tokens ?? 0) + (r.output_tokens ?? 0)

/** One point per day of the window, `{ day, s0, s1, … }`, a series per key, the largest
 * first. A day or a run with nothing reported plots 0: the runs left out are counted
 * beside the chart, never drawn as a value. */
export function dailyOf(
  rows: readonly UsageRow[],
  days: readonly string[],
  metric: Metric,
): {
  series: { id: string; key?: string }[]
  points: Record<string, string | number>[]
} {
  const totals = new Map<string | undefined, number>()
  for (const r of rows)
    totals.set(r.key, (totals.get(r.key) ?? 0) + valueOf(r, metric))
  const series = [...totals]
    .sort((a, b) => b[1] - a[1])
    .map(([key], i) => ({ id: `s${i}`, key }))
  const idOf = new Map(series.map((s) => [s.key, s.id]))
  const points: Record<string, string | number>[] = days.map((day) => ({
    day,
    ...Object.fromEntries(series.map((s) => [s.id, 0])),
  }))
  const pointOf = new Map(points.map((p) => [p.day as string, p]))
  for (const r of rows) {
    const point = pointOf.get(r.day ?? '')
    const id = idOf.get(r.key)!
    if (point) point[id] = (point[id] as number) + valueOf(r, metric)
  }
  return { series, points }
}

/** A text a spreadsheet would run as a formula starts with one of these (OWASP CSV
 * injection): such a cell is saved with a leading apostrophe. */
const FORMULA = /^[=+\-@\t\r]/
const QUOTE = /[",\r\n]/

function cell(value: string | number | null): string {
  if (value === null) return ''
  if (typeof value === 'number') return String(value)
  const text = FORMULA.test(value) ? `'${value}` : value
  return QUOTE.test(text) ? `"${text.replaceAll('"', '""')}"` : text
}

/** The rows shown as CSV (RFC 4180), one line per day and key, the engine's own numbers:
 * an unknown sum is an empty cell. Money only when `money`. */
export function spendCsv(
  rows: readonly UsageRow[],
  dimension: 'tool' | 'profile',
  nameOf: (key?: string) => string,
  money: boolean,
): string {
  const head = [
    'day',
    dimension,
    'runs',
    'input_tokens',
    'output_tokens',
    'runs_without_tokens',
    ...(money ? ['cost_usd', 'runs_without_cost'] : []),
  ]
  const lines = rows.map((r) => [
    r.day ?? '',
    nameOf(r.key),
    r.runs,
    r.input_tokens,
    r.output_tokens,
    r.runs_without_tokens,
    ...(money
      ? [
          r.cost_micro_usd === null ? null : r.cost_micro_usd / 1_000_000,
          r.runs_without_cost,
        ]
      : []),
  ])
  return [head, ...lines].map((l) => l.map(cell).join(',') + '\r\n').join('')
}
