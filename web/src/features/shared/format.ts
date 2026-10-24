// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Formatters that the shared lib (`@/lib/format`) does not cover.
// Use `@/lib/format` for cost (formatMicroUsd), tokens, ints, percent, absolute
// and relative time, and durations; these helpers add the two inputs has:
// a duration in whole SECONDS (session duration, SLA windows), said by the same
// formatDuration, and a PPM→percent for SLA targets. Pure and deterministic.
import { formatDuration } from '@/lib/format'

/** Parse an engine timestamp (RFC3339) to epoch ms, or null if unparseable. */
export function parseTs(ts: string | undefined | null): number | null {
  if (!ts) return null
  const ms = Date.parse(ts)
  return Number.isNaN(ms) ? null : ms
}

/**
 * humanDurationSeconds says a whole-seconds span (`45s`, `1h 23m`, `2d 4h`) in the
 * console's one duration format, `@/lib/format`'s formatDuration: a session ticking
 * here and its Activity panel never tell the same time two ways.
 */
export function humanDurationSeconds(totalSeconds: number): string {
  const s = Math.max(0, Math.floor(totalSeconds))
  return s === 0 ? '0s' : formatDuration(s * 1000)
}

/** PPM (parts-per-million) → percent string, trimming trailing zeros
 * (999000 → "99.9%", 999500 → "99.95%"). 0/negative → em-dash. */
export function ppmToPercent(ppm: number): string {
  if (!Number.isFinite(ppm) || ppm <= 0) return '—'
  const pct = (ppm / 1_000_000) * 100
  return `${parseFloat(pct.toFixed(4))}%`
}
