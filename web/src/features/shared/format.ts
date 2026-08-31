// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Formatters that the shared lib (`@/lib/format`) does not cover.
// Use `@/lib/format` for cost (formatMicroUsd), tokens, ints, percent, absolute
// and relative time, and ms-durations; these helpers add the two units needs:
// a SECONDS-based duration that scales to hours/days (session duration, SLA
// windows) and a PPM→percent for SLA targets. Pure and deterministic.

/** Parse an engine timestamp (RFC3339) to epoch ms, or null if unparseable. */
export function parseTs(ts: string | undefined | null): number | null {
  if (!ts) return null
  const ms = Date.parse(ts)
  return Number.isNaN(ms) ? null : ms
}

/**
 * humanDurationSeconds renders a whole-seconds span as a compact `1h 23m` / `2d 4h`
 * / `45s` (at most two adjacent units). `@/lib/format`'s formatDuration is
 * millisecond-based and stops at minutes; session/SLA spans need hours and days.
 */
export function humanDurationSeconds(totalSeconds: number): string {
  const s = Math.max(0, Math.floor(totalSeconds))
  if (s < 60) return `${s}s`
  const m = Math.floor(s / 60)
  if (m < 60) {
    const rem = s % 60
    return rem ? `${m}m ${rem}s` : `${m}m`
  }
  const h = Math.floor(m / 60)
  if (h < 24) {
    const rem = m % 60
    return rem ? `${h}h ${rem}m` : `${h}h`
  }
  const d = Math.floor(h / 24)
  const rem = h % 24
  return rem ? `${d}d ${rem}h` : `${d}d`
}

/** PPM (parts-per-million) → percent string, trimming trailing zeros
 * (999000 → "99.9%", 999500 → "99.95%"). 0/negative → em-dash. */
export function ppmToPercent(ppm: number): string {
  if (!Number.isFinite(ppm) || ppm <= 0) return '—'
  const pct = (ppm / 1_000_000) * 100
  return `${parseFloat(pct.toFixed(4))}%`
}
