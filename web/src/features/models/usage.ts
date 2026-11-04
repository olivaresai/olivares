// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Models > Usage: the window the tab asks for and the daily series its chart draws. The
// totals are the engine's (`GET /v1/m/sessions/usage`); nothing here sums a cost or a token.
import type { UsageRow } from './types'

export const USAGE_BY = ['model', 'account', 'provider', 'tool'] as const
export type UsageBy = (typeof USAGE_BY)[number]

export const USAGE_DAYS = [7, 30] as const
export type UsageDays = (typeof USAGE_DAYS)[number]

/** Series drawn by name; the runs of the rest fold into one "other" series. */
export const CHART_SERIES = 5

const DAY_MS = 86_400_000

/** The window ends today and opens at 00:00 UTC `count - 1` days earlier, so it holds
 *  `count` whole UTC days (the engine buckets by UTC day). `since` is the RFC 3339 bound the
 *  read takes; `days` are the YYYY-MM-DD labels the chart plots. */
export function usageWindow(
  count: UsageDays,
  now: Date = new Date(),
): { since: string; days: string[] } {
  const today = Date.UTC(
    now.getUTCFullYear(),
    now.getUTCMonth(),
    now.getUTCDate(),
  )
  const start = today - (count - 1) * DAY_MS
  return {
    since: new Date(start).toISOString().replace('.000Z', 'Z'),
    days: Array.from({ length: count }, (_, i) =>
      new Date(start + i * DAY_MS).toISOString().slice(0, 10),
    ),
  }
}

/** One plotted series. `key` is '' for runs that recorded none. */
export interface UsageSeries {
  id: string
  key: string
  other: boolean
}

export interface DailyRuns {
  series: UsageSeries[]
  /** One row per window day: `{ day, s0: runs, s1: runs, ... }`. A day without runs is 0:
   *  the engine counted it, so unlike a cost it is a fact. */
  rows: Record<string, string | number>[]
  total: number
  busiest: { day: string; runs: number }
}

/** Runs per day, a series per key (the busiest CHART_SERIES keys, the rest as "other"). */
export function dailyRuns(items: UsageRow[], days: string[]): DailyRuns {
  const byKey = new Map<string, number>()
  for (const r of items)
    byKey.set(r.key ?? '', (byKey.get(r.key ?? '') ?? 0) + r.runs)
  const ranked = [...byKey].sort((a, b) => b[1] - a[1]).map(([key]) => key)

  const series: UsageSeries[] = ranked
    .slice(0, CHART_SERIES)
    .map((key, i) => ({ id: `s${i}`, key, other: false }))
  if (ranked.length > CHART_SERIES)
    series.push({ id: 'other', key: '', other: true })
  const idOf = new Map(series.filter((s) => !s.other).map((s) => [s.key, s.id]))

  const rows: Record<string, string | number>[] = days.map((day) => ({
    day,
    ...Object.fromEntries(series.map((s) => [s.id, 0])),
  }))
  const rowOf = new Map(rows.map((r) => [r.day as string, r]))
  for (const r of items) {
    const row = rowOf.get(r.day ?? '')
    if (!row) continue
    const id = idOf.get(r.key ?? '') ?? 'other'
    row[id] = (row[id] as number) + r.runs
  }

  let total = 0
  let busiest = { day: days[0] ?? '', runs: 0 }
  for (const row of rows) {
    const runs = series.reduce((n, s) => n + (row[s.id] as number), 0)
    total += runs
    if (runs > busiest.runs) busiest = { day: row.day as string, runs }
  }
  return { series, rows, total, busiest }
}
