// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE LEDGER QUERY: what a person types in the top bar on the Ledger place
// (`person:marc since:7d approval`), read as the audit API's own filters.
//
// ⛔ ONE TOKEN, ONE FILTER. Every key names exactly one GET /v1/audit filter and a bare
//    word is its `q`, so a typed query and the same filters on the URL ask the engine the
//    same question. A key the API has no filter for (`tool:`, `folder:`) is refused and
//    said, never approximated with `q`: an approximation would answer a different
//    question under the name of this one.
//
// The URL carries the filters under the API's own keys (`?actor=…&since=7d`), the form
// existing links and saved views already use; a relative bound or a day stays as typed
// there and becomes a minute only when the request is made. A day is the whole day:
// `since:` its start, `until:` the start of the next (the engine's until is inclusive).
import { isoMinuteBound } from '@/features/shared'
import type { AuditFilters } from './types'

/** The filters the ledger list accepts, in the order the query paints them. */
export const FILTER_KEYS = [
  'actor',
  'action',
  'target_kind',
  'target_id',
  'since',
  'until',
  'q',
] as const
export type FilterKey = (typeof FILTER_KEYS)[number]
export type QueryParams = Partial<Record<FilterKey, string>>

/** The word a person types for each filter (`q` is the bare word). */
export const TOKEN_KEYS = {
  person: 'actor',
  action: 'action',
  kind: 'target_kind',
  target: 'target_id',
  since: 'since',
  until: 'until',
} as const satisfies Record<string, Exclude<FilterKey, 'q'>>
export type TokenKey = keyof typeof TOKEN_KEYS

export interface Token {
  /** The typed key, or null for the bare words (`q`). */
  key: TokenKey | null
  filter: FilterKey
  value: string
}

export interface QueryProblem {
  token: string
  /** `key`: no such filter; `time`: not a bound; `person`: nobody by that name;
   *  `several`: more than one person by that name; `names`: names cannot be looked up. */
  reason: 'key' | 'time' | 'person' | 'several' | 'names'
}

/** What is typed and not yet read, and what reading it said. The page holds it, so it
 *  survives the field moving between the bar and the page. */
export interface LedgerDraft {
  text: string
  problems: QueryProblem[]
}

export const EMPTY_DRAFT: LedgerDraft = { text: '', problems: [] }

const RELATIVE = /^(\d{1,4})([mhdw])$/
const DAY_ONLY = /^\d{4}-\d\d-\d\d$/
const UNIT_MS = { m: 60_000, h: 3_600_000, d: 86_400_000, w: 604_800_000 }

/** A bound as the URL may carry it: `7d`, `24h`, `90m`, `2w`, a day, or an ISO instant. */
export function validBound(value: string): string | undefined {
  if (RELATIVE.test(value)) return value
  if (DAY_ONLY.test(value)) return isoMinuteBound(value) && value
  return isoMinuteBound(value)
}

/** The bound as the API reads it, to the minute: a relative one counted back from `now`,
 *  a day from its start (`since`) or up to the start of the next one (`until`). */
function absoluteBound(
  key: 'since' | 'until',
  value: string,
  now: Date,
): string | undefined {
  const rel = RELATIVE.exec(value)
  if (rel) {
    const unit = UNIT_MS[rel[2] as keyof typeof UNIT_MS]
    return isoMinuteBound(
      new Date(now.getTime() - Number(rel[1]) * unit).toISOString(),
    )
  }
  const bound = isoMinuteBound(value)
  if (!bound || key === 'since' || !DAY_ONLY.test(value)) return bound
  // 9999-12-31 has no next day a bound can name: its own last minute.
  return (
    isoMinuteBound(
      new Date(new Date(bound).getTime() + UNIT_MS.d).toISOString(),
    ) ?? isoMinuteBound(`${value}T23:59`)
  )
}

/** The words of a query: whitespace splits, double quotes keep a value whole. */
function words(text: string): string[] {
  return [...text.matchAll(/(?:[^\s"]+|"[^"]*")+/g)].map((m) => m[0])
}

const unquote = (s: string) => s.replaceAll('"', '')

/** An actor written as the ledger writes one (`user:…`, `system`) is taken as it is. */
const isActorReference = (value: string) =>
  value.includes(':') || value === 'system'

/**
 * Reads typed text into filters. `resolvePerson` answers every actor reference a name
 * or email has in the roster, or null when the roster cannot answer. What cannot be
 * read is returned as a problem and filters nothing.
 */
export function parseQuery(
  text: string,
  resolvePerson: (name: string) => string[] | null,
): { params: QueryParams; problems: QueryProblem[] } {
  const params: QueryParams = {}
  const problems: QueryProblem[] = []
  const bare: string[] = []
  for (const word of words(text)) {
    const colon = word.indexOf(':')
    const key = colon > 0 ? word.slice(0, colon).toLowerCase() : null
    if (key === null || key.includes('"')) {
      bare.push(unquote(word))
      continue
    }
    const value = unquote(word.slice(colon + 1)).trim()
    if (!Object.hasOwn(TOKEN_KEYS, key) || value === '') {
      problems.push({ token: word, reason: 'key' })
      continue
    }
    const filter = TOKEN_KEYS[key as TokenKey]
    if (filter === 'since' || filter === 'until') {
      const bound = validBound(value)
      if (bound === undefined) problems.push({ token: word, reason: 'time' })
      else params[filter] = bound
    } else if (filter === 'actor') {
      const actors = isActorReference(value) ? [value] : resolvePerson(value)
      if (actors?.length === 1) params.actor = actors[0]
      else
        problems.push({
          token: word,
          reason: !actors ? 'names' : actors.length ? 'several' : 'person',
        })
    } else {
      params[filter] = value
    }
  }
  const q = bare.join(' ').trim()
  if (q) params.q = q
  return { params, problems }
}

const KEY_OF = Object.fromEntries(
  Object.entries(TOKEN_KEYS).map(([k, f]) => [f, k]),
) as Partial<Record<FilterKey, TokenKey>>

/** The filters on the URL as the tokens a person reads, in FILTER_KEYS order. */
export function tokensOf(params: QueryParams): Token[] {
  return FILTER_KEYS.flatMap((filter) => {
    const value = params[filter]
    return value ? [{ key: KEY_OF[filter] ?? null, filter, value }] : []
  })
}

/** The request filters: the URL's filters with each relative bound made absolute. */
export function apiFilters(params: QueryParams, now: Date): AuditFilters {
  const out: AuditFilters = {}
  for (const key of FILTER_KEYS) {
    const value = params[key]
    if (!value) continue
    if (key === 'since' || key === 'until') {
      const bound = absoluteBound(key, value, now)
      if (bound) out[key] = bound
    } else {
      out[key] = value
    }
  }
  return out
}
