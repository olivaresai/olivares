// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

/**
 * THE RESPONSE DEADLINE, and why it is neither defaulted nor extended here.
 *
 * `ack_deadline` is an absolute instant the SERVER measures against database time. A
 * console that seeded it — "two hours from now", "end of day" — would be choosing a
 * commitment on the operator's behalf and hiding the choice inside a default, so the
 * field starts EMPTY and confirmation is refused until an instant is entered.
 *
 * Two coordinates of the same instant are shown, because a `datetime-local` control
 * is read in the browser's zone and the engine stores UTC: the local value the
 * operator typed, and the exact UTC string that will travel. If they disagree in the
 * operator's head, the review is where that has to be visible.
 *
 * The client clock check is a COURTESY, not authority: it stops an instant already
 * in the past from being sent at all, and the server's clock still decides and can
 * still refuse. Nothing here silently moves a deadline forward to make it pass.
 */
export type DeadlineProblem = 'empty' | 'unreadable' | 'past'

export interface DeadlineReading {
  /** RFC 3339 in UTC with seconds, e.g. `2026-09-10T18:30:00Z`. */
  utc: string
  at: Date
}

/** RFC 3339 UTC with SECONDS. `toISOString` always carries milliseconds; the wire
 * asks for a date-time and seconds precision is what the surface promises to send,
 * so the fractional part is dropped rather than shipped as noise. */
export function toRfc3339Utc(at: Date): string {
  return at.toISOString().replace(/\.\d{3}Z$/, 'Z')
}

export function readDeadline(
  value: string,
  now: number,
):
  | { ok: true; reading: DeadlineReading }
  | { ok: false; problem: DeadlineProblem } {
  if (value.trim() === '') return { ok: false, problem: 'empty' }
  const at = new Date(value)
  if (Number.isNaN(at.getTime())) return { ok: false, problem: 'unreadable' }
  if (at.getTime() <= now) return { ok: false, problem: 'past' }
  return { ok: true, reading: { utc: toRfc3339Utc(at), at } }
}

/** The operator's own zone, named so the local half of the review is unambiguous.
 * Falls back to the fixed offset when the platform cannot name one. */
export function localZoneLabel(at: Date): string {
  const named = Intl.DateTimeFormat().resolvedOptions().timeZone
  if (named) return named
  const minutes = -at.getTimezoneOffset()
  const sign = minutes < 0 ? '-' : '+'
  const abs = Math.abs(minutes)
  const hh = String(Math.floor(abs / 60)).padStart(2, '0')
  const mm = String(abs % 60).padStart(2, '0')
  return `UTC${sign}${hh}:${mm}`
}
