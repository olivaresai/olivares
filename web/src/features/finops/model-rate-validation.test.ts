// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The rate-catalog reader against the engine's own contract. The timestamp tables are the
// output of `time.Parse(time.RFC3339, …)` in go1.26.6 over these exact inputs — the parser
// modules/finops/ratecatalog.go validates writes with — with epoch milliseconds from
// `Time.UnixMilli`. Four inputs Go accepts are refused here because they are not RFC 3339.
import { describe, expect, it } from 'vitest'
import {
  parseRfc3339Instant,
  readModelRateCatalog,
} from './model-rate-validation'

const GO_ACCEPTED: Array<[string, number]> = [
  ['2026-01-01T00:00:00Z', 1767225600000],
  ['2026-01-01T00:00:00.000000000Z', 1767225600000],
  ['2026-01-01T00:00:00.5Z', 1767225600500],
  ['2026-09-11T14:30:00+02:00', 1789129800000],
  ['2026-09-11T14:30:00-05:30', 1789156800000],
  ['2026-01-01T00:00:00+00:00', 1767225600000],
  ['2026-01-01T00:00:00-00:00', 1767225600000],
  ['2028-02-29T00:00:00Z', 1835395200000],
  ['2000-02-29T00:00:00Z', 951782400000],
  ['0000-01-01T00:00:00Z', -62167219200000],
  ['9999-12-31T23:59:59.999999999Z', 253402300799999],
  ['2026-01-01T00:00:00.123456789123Z', 1767225600123],
]

const GO_REJECTED = [
  '',
  '2026-01-01',
  '2026-01-01T00:00:00',
  '2026-01-01 00:00:00Z',
  '2026-01-01t00:00:00z',
  '2026-01-01T00:00:00z',
  '2026-01-01T00:00:00+0200',
  '2026-02-30T00:00:00Z',
  '2027-02-29T00:00:00Z',
  '1900-02-29T00:00:00Z',
  '2026-13-01T00:00:00Z',
  '2026-00-10T00:00:00Z',
  '2026-01-00T00:00:00Z',
  '2026-01-01T24:00:00Z',
  '2026-01-01T23:60:00Z',
  '2026-01-01T23:59:60Z',
  'January 1, 2026',
  '2026-01-01T00:00:00.Z',
  '+2026-01-01T00:00:00Z',
  ' 2026-01-01T00:00:00Z',
  '2026-1-01T00:00:00Z',
  '2026-01-01T00:00:00Z\n',
  '２026-01-01T00:00:00Z',
]

/** Go's parser accepts these; the RFC 3339 grammar does not. */
const GO_LENIENT_NOT_RFC3339 = [
  '2026-01-01T00:00:00+24:00',
  '2026-01-01T00:00:00+23:60',
  '2026-01-01T00:00:00,5Z',
  '2026-01-01T0:00:00Z',
]

describe('parseRfc3339Instant', () => {
  it.each(GO_ACCEPTED)('accepts %j at the instant Go computes', (text, ms) => {
    expect(parseRfc3339Instant(text)).toBe(ms)
  })

  it.each(GO_REJECTED)('refuses %j, as the engine does', (text) => {
    expect(parseRfc3339Instant(text)).toBeNull()
  })

  it.each(GO_LENIENT_NOT_RFC3339)(
    'refuses %j: Go accepts it, RFC 3339 does not',
    (text) => {
      expect(parseRfc3339Instant(text)).toBeNull()
    },
  )
})

const row = (over: Record<string, unknown> = {}): Record<string, unknown> => ({
  id: 'mr-1',
  provider: 'anthropic',
  model: 'claude-opus-5',
  input_rate_micro_usd: 15_000_000,
  output_rate_micro_usd: 75_000_000,
  cache_read_rate_micro_usd: 1_500_000,
  cache_creation_rate_micro_usd: 18_750_000,
  effective_from: '2026-01-01T00:00:00.000000000Z',
  ...over,
})

const without = (key: string, over: Record<string, unknown> = {}) => {
  const r = row(over)
  delete r[key]
  return r
}

describe('readModelRateCatalog', () => {
  it('reads a server-shaped page in server order, with its truncation flag', () => {
    const read = readModelRateCatalog({
      items: [
        row({
          id: 'mr-2',
          model: 'claude-sonnet-5',
          effective_from: '2026-07-01T00:00:00.000000000Z',
        }),
        row({ effective_until: '2026-07-01T00:00:00.000000000Z' }),
      ],
      has_more: true,
    })
    expect(read).toEqual({
      status: 'valid',
      has_more: true,
      items: [
        {
          id: 'mr-2',
          provider: 'anthropic',
          model: 'claude-sonnet-5',
          inputRateMicroUsd: 15_000_000,
          outputRateMicroUsd: 75_000_000,
          effectiveFrom: {
            text: '2026-07-01T00:00:00.000000000Z',
            epochMs: Date.UTC(2026, 6, 1),
          },
        },
        {
          id: 'mr-1',
          provider: 'anthropic',
          model: 'claude-opus-5',
          inputRateMicroUsd: 15_000_000,
          outputRateMicroUsd: 75_000_000,
          effectiveFrom: {
            text: '2026-01-01T00:00:00.000000000Z',
            epochMs: Date.UTC(2026, 0, 1),
          },
          effectiveUntil: {
            text: '2026-07-01T00:00:00.000000000Z',
            epochMs: Date.UTC(2026, 6, 1),
          },
        },
      ],
    })
  })

  it('an actual empty items array is a valid empty page', () => {
    expect(readModelRateCatalog({ items: [], has_more: false })).toEqual({
      status: 'valid',
      items: [],
      has_more: false,
    })
  })

  it('claims no truncation when has_more is absent', () => {
    expect(readModelRateCatalog({ items: [row()] })).toMatchObject({
      status: 'valid',
      has_more: false,
    })
  })

  it('preserves an explicit zero rate and the largest exact integer', () => {
    const read = readModelRateCatalog({
      items: [
        row({
          input_rate_micro_usd: 0,
          output_rate_micro_usd: Number.MAX_SAFE_INTEGER,
        }),
      ],
      has_more: false,
    })
    expect(read).toMatchObject({
      status: 'valid',
      items: [
        {
          inputRateMicroUsd: 0,
          outputRateMicroUsd: Number.MAX_SAFE_INTEGER,
        },
      ],
    })
  })

  it('carries JSON -0 as the canonical zero', () => {
    const read = readModelRateCatalog(
      JSON.parse(
        '{"items":[{"id":"mr-1","provider":"p","model":"m","input_rate_micro_usd":-0,"output_rate_micro_usd":1,"effective_from":"2026-01-01T00:00:00Z"}],"has_more":false}',
      ),
    )
    expect(read.status).toBe('valid')
    expect(Object.is(read.items?.[0]?.inputRateMicroUsd, 0)).toBe(true)
  })

  it.each([
    ['a null payload', null, { reason: 'envelope' }],
    ['an array payload', [], { reason: 'envelope' }],
    ['a text payload', 'items', { reason: 'envelope' }],
    [
      'a page without items',
      { has_more: false },
      { reason: 'envelope', field: 'items' },
    ],
    ['items: null', { items: null }, { reason: 'envelope', field: 'items' }],
    [
      'items as an object',
      { items: {} },
      { reason: 'envelope', field: 'items' },
    ],
    [
      'has_more as text',
      { items: [], has_more: 'false' },
      { reason: 'envelope', field: 'has_more' },
    ],
    [
      'has_more true with no rows',
      { items: [], has_more: true },
      { reason: 'envelope', field: 'has_more' },
    ],
  ])('%s is an invalid page, not an empty one', (_what, payload, issue) => {
    expect(readModelRateCatalog(payload)).toEqual({ status: 'invalid', issue })
  })

  it.each([
    [
      'the legacy model_ref instead of model',
      without('model', { model_ref: 'claude-opus-5' }),
      'model',
      'missing',
    ],
    ['a null model', row({ model: null }), 'model', 'missing'],
    ['a blank model', row({ model: '   ' }), 'model', 'empty'],
    ['a numeric model', row({ model: 5 }), 'model', 'type'],
    ['no id', without('id'), 'id', 'missing'],
    ['an empty id', row({ id: '' }), 'id', 'empty'],
    ['an empty provider', row({ provider: '' }), 'provider', 'empty'],
    [
      'no input rate',
      without('input_rate_micro_usd'),
      'input_rate_micro_usd',
      'missing',
    ],
    [
      'no output rate',
      without('output_rate_micro_usd'),
      'output_rate_micro_usd',
      'missing',
    ],
    [
      'a rate as text',
      row({ input_rate_micro_usd: '15000000' }),
      'input_rate_micro_usd',
      'type',
    ],
    [
      'a fractional rate',
      row({ output_rate_micro_usd: 1.5 }),
      'output_rate_micro_usd',
      'notInteger',
    ],
    [
      'a negative rate',
      row({ input_rate_micro_usd: -1 }),
      'input_rate_micro_usd',
      'negative',
    ],
    [
      'a rate of 2^53',
      row({ input_rate_micro_usd: 2 ** 53 }),
      'input_rate_micro_usd',
      'unsafe',
    ],
    [
      'a rate of 1e21',
      row({ output_rate_micro_usd: 1e21 }),
      'output_rate_micro_usd',
      'unsafe',
    ],
    [
      'no effective_from',
      without('effective_from'),
      'effective_from',
      'missing',
    ],
    [
      'an empty effective_from',
      row({ effective_from: '' }),
      'effective_from',
      'empty',
    ],
    [
      'a prose effective_from',
      row({ effective_from: 'January 1, 2026' }),
      'effective_from',
      'timestamp',
    ],
    [
      'an impossible effective_from',
      row({ effective_from: '2026-02-30T00:00:00Z' }),
      'effective_from',
      'timestamp',
    ],
    [
      'a numeric effective_from',
      row({ effective_from: 1767225600 }),
      'effective_from',
      'type',
    ],
    [
      'a null effective_until',
      row({ effective_until: null }),
      'effective_until',
      'type',
    ],
    [
      'an empty effective_until',
      row({ effective_until: '' }),
      'effective_until',
      'empty',
    ],
    [
      'a prose effective_until',
      row({ effective_until: 'soon' }),
      'effective_until',
      'timestamp',
    ],
    [
      'a Go-lenient effective_until',
      row({ effective_until: '2026-01-01T0:00:00Z' }),
      'effective_until',
      'timestamp',
    ],
  ])('an entry with %s is invalid', (_what, entry, field, reason) => {
    expect(readModelRateCatalog({ items: [entry], has_more: false })).toEqual({
      status: 'invalid',
      issue: { reason, entry: 1, field },
    })
  })

  it('reports a non-object entry by its 1-based position', () => {
    expect(
      readModelRateCatalog({ items: [row(), 'mr-2'], has_more: false }),
    ).toEqual({
      status: 'invalid',
      issue: { reason: 'entry', entry: 2 },
    })
  })

  it('reports the first problem, in entry order and then field order', () => {
    expect(
      readModelRateCatalog({
        items: [
          row(),
          row({ id: '', model: '', input_rate_micro_usd: -1 }),
          row({ provider: '' }),
        ],
        has_more: false,
      }),
    ).toEqual({
      status: 'invalid',
      issue: { reason: 'empty', entry: 2, field: 'id' },
    })
  })
})
