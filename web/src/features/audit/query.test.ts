// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { describe, expect, it } from 'vitest'
import { apiFilters, parseQuery, tokensOf } from './query'

const roster: Record<string, string[]> = {
  marc: ['user:01a0-marc'],
  'marc vidal': ['user:01a0-marc'],
  ana: ['user:02b0-ana', 'user:03c0-ana'],
}
const resolvePerson = (name: string) => roster[name.toLowerCase()] ?? []
const NOW = new Date('2026-10-20T16:42:31Z')

describe('the ledger query', () => {
  it('maps each token to the audit API filter of the same meaning', () => {
    const { params, problems } = parseQuery(
      'person:marc action:sessions. kind:sessions.run target:run-7 since:7d until:2026-10-20 approval',
      resolvePerson,
    )
    expect(problems).toEqual([])
    expect(params).toEqual({
      actor: 'user:01a0-marc',
      action: 'sessions.',
      target_kind: 'sessions.run',
      target_id: 'run-7',
      since: '7d',
      // A day stays the day typed, as a relative bound stays relative.
      until: '2026-10-20',
      q: 'approval',
    })
  })

  it('reads a quoted value whole', () => {
    expect(
      parseQuery('person:"Marc Vidal" "db migrate"', resolvePerson).params,
    ).toEqual({ actor: 'user:01a0-marc', q: 'db migrate' })
  })

  it('takes an actor reference as written, without the roster', () => {
    expect(parseQuery('person:system', () => []).params).toEqual({
      actor: 'system',
    })
    expect(parseQuery('person:user:9f', () => []).params).toEqual({
      actor: 'user:9f',
    })
  })

  it('refuses what the API cannot filter instead of guessing', () => {
    const { params, problems } = parseQuery(
      'person:nobody tool:codex since:soon',
      resolvePerson,
    )
    expect(params).toEqual({})
    expect(problems).toEqual([
      { token: 'person:nobody', reason: 'person' },
      { token: 'tool:codex', reason: 'key' },
      { token: 'since:soon', reason: 'time' },
    ])
  })

  it('says a person key fits several people rather than nobody', () => {
    expect(parseQuery('person:ana', resolvePerson)).toEqual({
      params: {},
      problems: [{ token: 'person:ana', reason: 'several' }],
    })
  })

  it('says names cannot be looked up rather than that nobody matches', () => {
    expect(parseQuery('person:marc', () => null)).toEqual({
      params: {},
      problems: [{ token: 'person:marc', reason: 'names' }],
    })
    // A reference needs no lookup.
    expect(parseQuery('person:user:9f', () => null).params).toEqual({
      actor: 'user:9f',
    })
  })

  it('takes no key from Object.prototype', () => {
    expect(
      parseQuery('constructor:x __proto__:y toString:z', resolvePerson),
    ).toEqual({
      params: {},
      problems: [
        { token: 'constructor:x', reason: 'key' },
        { token: '__proto__:y', reason: 'key' },
        { token: 'toString:z', reason: 'key' },
      ],
    })
  })

  it('sends a relative bound as the minute it names', () => {
    expect(apiFilters({ since: '7d', actor: 'user:x' }, NOW)).toEqual({
      since: '2026-10-13T16:42:00.000Z',
      actor: 'user:x',
    })
    expect(
      apiFilters({ since: '90m', until: '2026-10-20T09:30' }, NOW),
    ).toEqual({
      since: '2026-10-20T15:12:00.000Z',
      until: '2026-10-20T09:30:00.000Z',
    })
    // Only a bare day moves to the next one: a midnight with its time stays put.
    expect(apiFilters({ until: '2026-10-20T00:00:00.000Z' }, NOW)).toEqual({
      until: '2026-10-20T00:00:00.000Z',
    })
  })

  it('reads a day as the whole day: since its start, until the next one', () => {
    const typed = parseQuery('since:2026-10-19 until:2026-10-20', () => [])
    expect(typed).toEqual({
      params: { since: '2026-10-19', until: '2026-10-20' },
      problems: [],
    })
    // The engine's until is inclusive: the whole 20th is in, and of the 21st only its
    // first instant, the same one instant a bar's bound shares with the next bar.
    expect(apiFilters(typed.params, NOW)).toEqual({
      since: '2026-10-19T00:00:00.000Z',
      until: '2026-10-21T00:00:00.000Z',
    })
    expect(apiFilters({ until: '2026-12-31' }, NOW)).toEqual({
      until: '2027-01-01T00:00:00.000Z',
    })
    // The last day a bound can name keeps a bound: its own last minute.
    expect(apiFilters({ until: '9999-12-31' }, NOW)).toEqual({
      until: '9999-12-31T23:59:00.000Z',
    })
  })

  it('paints the URL filters back as the tokens a person typed', () => {
    expect(
      tokensOf({ actor: 'user:01a0-marc', since: '7d', q: 'db migrate' }),
    ).toEqual([
      { key: 'person', filter: 'actor', value: 'user:01a0-marc' },
      { key: 'since', filter: 'since', value: '7d' },
      { key: null, filter: 'q', value: 'db migrate' },
    ])
  })
})
