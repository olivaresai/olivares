// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// One implementation of "is this URL value a usable time bound", shared by every
// view that carries one.
//
// It is shared because the alternative was measured: the recordings bound was
// hardened four times across four review rounds while the observability bound
// sat with the original defect the whole way, and a review caught it only at the
// end. Two copies of a validator drift, and the direction they drift in is the
// one where a link shows one window and queries another.
//
// Three questions, asked SEPARATELY. Joining any pair produced a real defect in
// an earlier round:
//
//   1. Does the string match the accepted grammar? A whitelist, because
//      validating opportunistically and leaving the rest to `new Date` let
//      `2026-02-30Z`, `2026/02/30`, a leading space and even `0` through — the
//      parser normalises an impossible day into a possible one.
//   2. Does the calendar date it states EXIST? `new Date('2026-02-30')` is the
//      2nd of March, so an impossible day would be silently answered with a
//      different, possible one.
//   3. Which UTC instant is it? Comparing the stated date against the UTC day
//      refused legitimate input: 2026-06-01T00:30:00+02:00 is a real instant on
//      the 31st of May UTC.

/**
 * The accepted shape. Field ranges live in the pattern rather than being handed
 * to the parser to reject, because what is not stated is not checked. Lowercase
 * `t` and `z` are accepted, as RFC 3339 §5.6 permits.
 */
const ISO_BOUND =
  /^(\d{4})-(0[1-9]|1[0-2])-(0[1-9]|[12]\d|3[01])(?:[Tt ](?:[01]\d|2[0-4]):[0-5]\d(?::[0-5]\d(?:\.\d{1,9})?)?(Z|z|[+-](?:[01]\d|2[0-3]):[0-5]\d)?)?$/

/**
 * The year range a date control can render. `<input type="date">` and
 * `datetime-local` start at 0001, so a bound outside it would sit in the request
 * while the control showed nothing — the divergence this module exists to stop.
 * It also excludes the extended years a four-digit input can still CROSS into
 * through its offset or through 24:00 (9999-12-31T23:00:00-05:00 is the year
 * 10000 in UTC).
 */
const MIN_YEAR = 1
const MAX_YEAR = 9999

/**
 * The instant a bound denotes, or undefined if the string is not one.
 *
 * A time without an offset is read as UTC, not as the reader's local time. An
 * evidence link has to mean the same window for whoever sends it and whoever
 * opens it.
 */
export function parseIsoBound(raw: string): Date | undefined {
  const shape = ISO_BOUND.exec(raw)
  if (shape === null) return undefined

  const [, ys, ms, ds, offset] = shape
  const y = Number(ys)
  const m = Number(ms)
  const d = Number(ds)
  // The DECLARED year is bounded too, not only the resulting one. Checking the
  // result alone let 0000-12-31T23:30:00-01:00 and 0000-12-31T24:00:00Z through:
  // both state a year no control can render and both land in 0001, so the
  // out-of-range half was simply invisible to the check.
  if (y < MIN_YEAR || y > MAX_YEAR) return undefined

  // setUTCFullYear, not Date.UTC: the latter folds years 0-99 into 1900-1999,
  // so a legitimate 0099-01-01 came back as 1999 and was refused as impossible.
  const probe = new Date(0)
  probe.setUTCFullYear(y, m - 1, d)
  probe.setUTCHours(0, 0, 0, 0)
  if (
    probe.getUTCFullYear() !== y ||
    probe.getUTCMonth() !== m - 1 ||
    probe.getUTCDate() !== d
  ) {
    return undefined
  }

  const hasTime = /[Tt ]/.test(raw)
  const at = new Date(hasTime && offset === undefined ? `${raw}Z` : raw)
  if (Number.isNaN(at.getTime())) return undefined

  const uy = at.getUTCFullYear()
  if (uy < MIN_YEAR || uy > MAX_YEAR) return undefined
  return at
}

/** Zero-padded, built from components — never sliced off toISOString(), whose
 * year is not four characters outside the ordinary range. */
function pad(n: number, width = 2): string {
  return String(n).padStart(width, '0')
}

/**
 * The bound canonicalised to the UTC MIDNIGHT of its day, for a view whose
 * control is a day picker. Keeping the full instant let a link reading
 * 2026-06-01 query from 14:30 that day: two links identical on screen, two
 * different windows behind them.
 */
export function isoDayBound(raw: string): string | undefined {
  const at = parseIsoBound(raw)
  if (at === undefined) return undefined
  return `${pad(at.getUTCFullYear(), 4)}-${pad(at.getUTCMonth() + 1)}-${pad(
    at.getUTCDate(),
  )}T00:00:00.000Z`
}

/**
 * The bound canonicalised to the UTC MINUTE, for a view whose control is a
 * datetime-local picker. Same reasoning one unit down: a bound carrying seconds
 * renders identically to one without them and queries a different instant.
 */
export function isoMinuteBound(raw: string): string | undefined {
  const at = parseIsoBound(raw)
  if (at === undefined) return undefined
  const minute = new Date(at.getTime())
  minute.setUTCSeconds(0, 0)
  return `${pad(minute.getUTCFullYear(), 4)}-${pad(
    minute.getUTCMonth() + 1,
  )}-${pad(minute.getUTCDate())}T${pad(minute.getUTCHours())}:${pad(
    minute.getUTCMinutes(),
  )}:00.000Z`
}
