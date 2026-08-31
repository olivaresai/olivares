// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Presentation-only formatters for the intelligence views. The engine speaks
// in INTEGER micro-USD (millionths of a dollar) and raw token counts — money is
// NEVER a float on the wire (docs/SECURITY-HARDENING.md contract). These helpers turn those exact
// integers into operator-readable strings; they do NOT compute anything (the modules
// own the math, ARCHITECTURE.md). Everything degrades to an em-dash for null/undefined so
// a missing-but-honest value never reads as "$0".

import { currentLanguage } from './i18n'

const DASH = '—'

function isNil(v: unknown): v is null | undefined {
  return v === null || v === undefined
}

/** Micro-USD (integer millionths of a dollar) → a localized USD string.
 *  Precision auto-scales: ≥$1 shows cents; sub-dollar shows enough digits to be
 *  truthful (a $0.000015/token price is real); `compact` collapses big sums
 *  ($1.2M) for headline figures. Currency stays USD — this is a cost product. */
export function formatMicroUsd(
  micro: number | null | undefined,
  opts: { compact?: boolean; locale?: string; signDisplay?: boolean } = {},
): string {
  if (isNil(micro) || Number.isNaN(micro)) return DASH
  const usd = micro / 1_000_000
  const abs = Math.abs(usd)
  const locale = opts.locale ?? currentLanguage()
  const signDisplay = opts.signDisplay ? ('exceptZero' as const) : undefined

  if (opts.compact && abs >= 10_000) {
    return new Intl.NumberFormat(locale, {
      style: 'currency',
      currency: 'USD',
      notation: 'compact',
      maximumFractionDigits: 1,
      signDisplay,
    }).format(usd)
  }

  // Choose fraction digits so small per-token prices stay legible without noise.
  let maximumFractionDigits = 2
  if (abs > 0 && abs < 0.01) maximumFractionDigits = 6
  else if (abs < 1) maximumFractionDigits = 4

  return new Intl.NumberFormat(locale, {
    style: 'currency',
    currency: 'USD',
    minimumFractionDigits: 2,
    maximumFractionDigits,
    signDisplay,
  }).format(usd)
}

/** A raw token count → compact decimal (1.2M, 980K, 42). */
export function formatTokens(
  n: number | null | undefined,
  locale: string = currentLanguage(),
): string {
  if (isNil(n) || Number.isNaN(n)) return DASH
  return new Intl.NumberFormat(locale, {
    notation: Math.abs(n) >= 100_000 ? 'compact' : 'standard',
    maximumFractionDigits: 1,
  }).format(n)
}

/** A byte count → localized binary-unit string ("1.5 GiB", "420 B"). Used by
 *  the audit-spool budget card; binary units because budgets are declared in
 *  exact bytes, not marketing decimals. */
export function formatBytes(
  n: number | null | undefined,
  locale: string = currentLanguage(),
): string {
  if (isNil(n) || Number.isNaN(n) || n < 0) return DASH
  const units = ['B', 'KiB', 'MiB', 'GiB', 'TiB', 'PiB']
  let v = n
  let u = 0
  while (v >= 1024 && u < units.length - 1) {
    v /= 1024
    u++
  }
  const digits = u === 0 ? 0 : v < 10 ? 1 : 0
  return `${new Intl.NumberFormat(locale, {
    minimumFractionDigits: 0,
    maximumFractionDigits: digits,
  }).format(v)} ${units[u]}`
}

/** Integer with grouping (counts, samples, edges). */
export function formatInt(
  n: number | null | undefined,
  locale: string = currentLanguage(),
): string {
  if (isNil(n) || Number.isNaN(n)) return DASH
  return new Intl.NumberFormat(locale, { maximumFractionDigits: 0 }).format(n)
}

/** A value that is ALREADY a percentage (e.g. `consumed_pct: 60` → "60%"). */
export function formatPercent(
  pct: number | null | undefined,
  opts: { digits?: number; locale?: string } = {},
): string {
  if (isNil(pct) || Number.isNaN(pct)) return DASH
  const digits = opts.digits ?? (Math.abs(pct) < 10 ? 1 : 0)
  return `${new Intl.NumberFormat(opts.locale ?? currentLanguage(), {
    minimumFractionDigits: digits,
    maximumFractionDigits: digits,
  }).format(pct)}%`
}

/** A fraction in 0..1 (e.g. a threshold `0.8`) → "80%". */
export function formatFraction(
  frac: number | null | undefined,
  opts: { digits?: number; locale?: string } = {},
): string {
  if (isNil(frac) || Number.isNaN(frac)) return DASH
  return formatPercent(frac * 100, opts)
}

/** A 0..1 score → "0.92" (two decimals, the eval/redteam convention). */
export function formatScore(
  score: number | null | undefined,
  digits = 2,
): string {
  if (isNil(score) || Number.isNaN(score)) return DASH
  return score.toFixed(digits)
}

/** RFC3339 timestamp → absolute local date-time. Empty/invalid → em-dash. */
export function formatDateTime(
  iso: string | null | undefined,
  locale: string = currentLanguage(),
): string {
  if (!iso) return DASH
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return DASH
  return new Intl.DateTimeFormat(locale, {
    dateStyle: 'medium',
    timeStyle: 'short',
  }).format(d)
}

/** RFC3339 timestamp → just the date (axis ticks, evidence dates). */
export function formatDate(
  iso: string | null | undefined,
  locale: string = currentLanguage(),
): string {
  if (!iso) return DASH
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return DASH
  return new Intl.DateTimeFormat(locale, { dateStyle: 'medium' }).format(d)
}

/** A `YYYY-MM-DD` trend bucket key → a short axis label ("Jun 1"). */
export function formatDayKey(
  key: string,
  locale: string = currentLanguage(),
): string {
  if (!key) return DASH
  const d = new Date(`${key}T00:00:00Z`)
  if (Number.isNaN(d.getTime())) return key
  return new Intl.DateTimeFormat(locale, {
    month: 'short',
    day: 'numeric',
    timeZone: 'UTC',
  }).format(d)
}

/** A declared calendar date `YYYY-MM-DD` (a deadline / AsOf stamp, NOT a timestamp)
 * → a timezone-stable medium date WITH the year ("Jun 15, 2026"). Unlike formatDate,
 * this pins UTC, so a declared retirement deadline never shifts a day in a negative-
 * offset locale (a wrong deadline would be a real honesty bug). */
export function formatCalendarDate(
  date: string | null | undefined,
  locale: string = currentLanguage(),
): string {
  if (!date) return DASH
  const d = new Date(date.includes('T') ? date : `${date}T00:00:00Z`)
  if (Number.isNaN(d.getTime())) return date
  return new Intl.DateTimeFormat(locale, {
    dateStyle: 'medium',
    timeZone: 'UTC',
  }).format(d)
}

const RELATIVE_DIVISIONS: {
  amount: number
  unit: Intl.RelativeTimeFormatUnit
}[] = [
  { amount: 60, unit: 'second' },
  { amount: 60, unit: 'minute' },
  { amount: 24, unit: 'hour' },
  { amount: 7, unit: 'day' },
  { amount: 4.34524, unit: 'week' },
  { amount: 12, unit: 'month' },
  { amount: Number.POSITIVE_INFINITY, unit: 'year' },
]

/** RFC3339 timestamp → "2h ago" / "in 3d" relative to now. */
export function formatRelativeTime(
  iso: string | null | undefined,
  locale: string = currentLanguage(),
): string {
  if (!iso) return DASH
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return DASH
  const rtf = new Intl.RelativeTimeFormat(locale, { numeric: 'auto' })
  let duration = (d.getTime() - Date.now()) / 1000
  for (const division of RELATIVE_DIVISIONS) {
    if (Math.abs(duration) < division.amount) {
      return rtf.format(Math.round(duration), division.unit)
    }
    duration /= division.amount
  }
  return DASH
}

/** Milliseconds → a compact duration ("42.0s", "1m 02s", "110ms"). */
export function formatDuration(ms: number | null | undefined): string {
  if (isNil(ms) || Number.isNaN(ms)) return DASH
  if (ms < 1000) return `${Math.round(ms)}ms`
  const totalSeconds = ms / 1000
  if (totalSeconds < 60) return `${totalSeconds.toFixed(1)}s`
  const minutes = Math.floor(totalSeconds / 60)
  const seconds = Math.round(totalSeconds % 60)
  return `${minutes}m ${String(seconds).padStart(2, '0')}s`
}

/** Latency in ms as a plain operator figure ("110 ms"). */
export function formatLatency(ms: number | null | undefined): string {
  if (isNil(ms) || Number.isNaN(ms)) return DASH
  return `${formatInt(Math.round(ms))} ms`
}

/** A hex hash/fingerprint → head…tail, so a 64-char digest stays scannable.
 *  This is a FINGERPRINT, never a payload (docs/SECURITY-HARDENING.md) — the truncation is
 *  cosmetic; callers keep the full value for copy. */
export function truncateHash(
  hex: string | null | undefined,
  head = 8,
  tail = 6,
): string {
  if (!hex) return DASH
  if (hex.length <= head + tail + 1) return hex
  return `${hex.slice(0, head)}…${hex.slice(-tail)}`
}

/** Title-case a snake/kebab token for a fallback label ("prompt_injection" →
 *  "Prompt injection"). Prefer an i18n key; this is the honest default. */
export function humanize(s: string | null | undefined): string {
  if (!s) return DASH
  const spaced = s.replace(/[_-]+/g, ' ').trim()
  return spaced.charAt(0).toUpperCase() + spaced.slice(1)
}
