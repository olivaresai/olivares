// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The console's reading of `GET /v1/m/finops/model-rates`. The engine serves
// `listResponse[modelRateDTO]` (modules/finops/ratecatalog.go): `items` is always an array
// (empty when there are no rows), the model is named by `model`, rates are integer micro-USD
// per 1M tokens and `effective_from` / `effective_until` are RFC 3339 text. A payload without
// that shape is an unreadable catalog: never an empty one and never a zero rate. The result
// keeps only a safe description of the first problem (position, field, reason), no values.

/** A validated RFC 3339 instant. */
export interface RateInstant {
  /** The server's text, unchanged. */
  text: string
  /** Milliseconds since the Unix epoch, truncated like Go's `Time.UnixMilli`. */
  epochMs: number
}

export interface ModelRateEntry {
  id: string
  provider: string
  model: string
  inputRateMicroUsd: number
  outputRateMicroUsd: number
  effectiveFrom: RateInstant
  /** Absent when the entry declares no end. That absence does not show that it applies now. */
  effectiveUntil?: RateInstant
}

export type RateCatalogField =
  | 'items'
  | 'has_more'
  | 'id'
  | 'provider'
  | 'model'
  | 'input_rate_micro_usd'
  | 'output_rate_micro_usd'
  | 'effective_from'
  | 'effective_until'

export type RateCatalogIssueReason =
  | 'envelope'
  | 'entry'
  | 'missing'
  | 'empty'
  | 'type'
  | 'negative'
  | 'notInteger'
  | 'unsafe'
  | 'timestamp'

export interface RateCatalogIssue {
  reason: RateCatalogIssueReason
  /** 1-based position in `items`; absent for a problem with the envelope itself. */
  entry?: number
  field?: RateCatalogField
}

export type RateCatalogRead =
  | {
      status: 'valid'
      // Envelope names on purpose: the shared truncation badge and the finops list-ceiling
      // ratchet read a valid page exactly as they read every other finops list.
      items: ModelRateEntry[]
      has_more: boolean
    }
  | {
      status: 'invalid'
      issue: RateCatalogIssue
      /** An unreadable page has no rows and claims no truncation. */
      items?: undefined
      has_more?: undefined
    }

// RFC 3339 `date-time` with the upper-case `T`/`Z` Go's `time.RFC3339` parser requires. Forms
// Go also accepts but RFC 3339 does not (one-digit hour, comma fraction, `+24:00`) are refused:
// the accepted set is the intersection, measured against Go in the delivery's oracle.
const RFC3339 =
  /^([0-9]{4})-([0-9]{2})-([0-9]{2})T([0-9]{2}):([0-9]{2}):([0-9]{2})(?:\.([0-9]+))?(?:Z|([+-])([0-9]{2}):([0-9]{2}))$/

function daysInMonth(year: number, month: number): number {
  if (month === 2) {
    const leap = (year % 4 === 0 && year % 100 !== 0) || year % 400 === 0
    return leap ? 29 : 28
  }
  return month === 4 || month === 6 || month === 9 || month === 11 ? 30 : 31
}

/** Epoch milliseconds of an RFC 3339 `date-time`, or null when the text is not one. */
export function parseRfc3339Instant(text: string): number | null {
  const m = RFC3339.exec(text)
  if (!m) return null
  const year = Number(m[1])
  const month = Number(m[2])
  const day = Number(m[3])
  const hour = Number(m[4])
  const minute = Number(m[5])
  const second = Number(m[6])
  const fraction = m[7]
  const sign = m[8]
  const offsetHour = sign === undefined ? 0 : Number(m[9])
  const offsetMinute = sign === undefined ? 0 : Number(m[10])
  if (month < 1 || month > 12) return null
  if (day < 1 || day > daysInMonth(year, month)) return null
  if (hour > 23 || minute > 59 || second > 59) return null
  if (offsetHour > 23 || offsetMinute > 59) return null
  const millis =
    fraction === undefined ? 0 : Number(fraction.slice(0, 3).padEnd(3, '0'))
  // setUTCFullYear, not Date.UTC: Date.UTC maps years 0-99 to 1900-1999.
  const at = new Date(0)
  at.setUTCFullYear(year, month - 1, day)
  at.setUTCHours(hour, minute, second, millis)
  const offsetMs = (offsetHour * 60 + offsetMinute) * 60_000
  return at.getTime() - (sign === '-' ? -offsetMs : offsetMs)
}

type Checked<T> =
  { ok: true; value: T } | { ok: false; reason: RateCatalogIssueReason }

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}

function readText(value: unknown): Checked<string> {
  if (value === undefined || value === null)
    return { ok: false, reason: 'missing' }
  if (typeof value !== 'string') return { ok: false, reason: 'type' }
  if (value.trim() === '') return { ok: false, reason: 'empty' }
  return { ok: true, value }
}

function readRate(value: unknown): Checked<number> {
  if (value === undefined || value === null)
    return { ok: false, reason: 'missing' }
  if (typeof value !== 'number') return { ok: false, reason: 'type' }
  if (!Number.isInteger(value)) return { ok: false, reason: 'notInteger' }
  if (value < 0) return { ok: false, reason: 'negative' }
  // Beyond 2^53 - 1 the parsed number is no longer the integer the engine sent.
  if (!Number.isSafeInteger(value)) return { ok: false, reason: 'unsafe' }
  // JSON `-0` is zero; carry the canonical 0 so it cannot render as "-0".
  return { ok: true, value: value === 0 ? 0 : value }
}

function readInstant(value: unknown): Checked<RateInstant> {
  const text = readText(value)
  if (!text.ok) return text
  const epochMs = parseRfc3339Instant(text.value)
  if (epochMs === null) return { ok: false, reason: 'timestamp' }
  return { ok: true, value: { text: text.value, epochMs } }
}

function readEntry(
  raw: unknown,
  entry: number,
):
  { ok: true; value: ModelRateEntry } | { ok: false; issue: RateCatalogIssue } {
  if (!isRecord(raw)) return { ok: false, issue: { reason: 'entry', entry } }
  const fail = (field: RateCatalogField, reason: RateCatalogIssueReason) =>
    ({ ok: false, issue: { reason, entry, field } }) as const

  const id = readText(raw.id)
  if (!id.ok) return fail('id', id.reason)
  const provider = readText(raw.provider)
  if (!provider.ok) return fail('provider', provider.reason)
  const model = readText(raw.model)
  if (!model.ok) return fail('model', model.reason)
  const input = readRate(raw.input_rate_micro_usd)
  if (!input.ok) return fail('input_rate_micro_usd', input.reason)
  const output = readRate(raw.output_rate_micro_usd)
  if (!output.ok) return fail('output_rate_micro_usd', output.reason)
  const from = readInstant(raw.effective_from)
  if (!from.ok) return fail('effective_from', from.reason)

  const value: ModelRateEntry = {
    id: id.value,
    provider: provider.value,
    model: model.value,
    inputRateMicroUsd: input.value,
    outputRateMicroUsd: output.value,
    effectiveFrom: from.value,
  }
  // The engine omits an empty end (`omitempty`); a present `null` is not its contract.
  if (raw.effective_until !== undefined) {
    if (raw.effective_until === null) return fail('effective_until', 'type')
    const until = readInstant(raw.effective_until)
    if (!until.ok) return fail('effective_until', until.reason)
    value.effectiveUntil = until.value
  }
  return { ok: true, value }
}

/**
 * Classify a model-rate list response. Only `has_more === true` claims truncation, the rule the
 * shared list badge applies. A present non-boolean `has_more` is refused so a malformed flag
 * cannot hide a truncated list, and so is `has_more: true` with no rows: the store sets the flag
 * only when it trims a full page, and presenting that page as an empty catalog would contradict
 * its own flag.
 */
export function readModelRateCatalog(payload: unknown): RateCatalogRead {
  if (!isRecord(payload))
    return { status: 'invalid', issue: { reason: 'envelope' } }
  const items = payload.items
  if (!Array.isArray(items))
    return { status: 'invalid', issue: { reason: 'envelope', field: 'items' } }
  const hasMore = payload.has_more
  if (
    (hasMore !== undefined && typeof hasMore !== 'boolean') ||
    (hasMore === true && items.length === 0)
  )
    return {
      status: 'invalid',
      issue: { reason: 'envelope', field: 'has_more' },
    }
  const entries: ModelRateEntry[] = []
  for (let index = 0; index < items.length; index++) {
    const read = readEntry(items[index], index + 1)
    if (!read.ok) return { status: 'invalid', issue: read.issue }
    entries.push(read.value)
  }
  return { status: 'valid', items: entries, has_more: hasMore === true }
}
