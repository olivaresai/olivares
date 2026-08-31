// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The bound parser is shared by every view that carries a time filter, so its
// edges are pinned once, here, rather than rediscovered per view. Each case
// below is a shape that got through some earlier version and produced a link
// whose displayed window differed from the one it queried.
import { describe, expect, it } from 'vitest'
import { isoDayBound, isoMinuteBound, parseIsoBound } from './iso-bound'

describe('parseIsoBound refuses what the parser would normalise', () => {
  it.each([
    // The parser answers an impossible day with a possible one.
    ['2026-02-30', 'the 30th of February is the 2nd of March to Date()'],
    ['2026-02-30T00:00:00Z', 'same, in the full timestamp form'],
    ['2026-02-30Z', 'same, in a form no anchored pattern recognised'],
    ['2026-02-29', 'not a leap year'],
    ['1900-02-29', 'a century that is not a leap year'],
    ['2026-13-01', 'month out of range'],
    ['2026-06-31', 'day out of range for the month'],
    // Shapes Date() accepts and no reader would call a bound.
    ['2026/02/30', 'slashes'],
    [' 2026-02-30', 'a leading space'],
    ['0', 'a bare number'],
    ['basura', 'not a date at all'],
    // Field ranges are the grammar's job, not the parser's.
    ['2026-06-01T25:00:00Z', 'hour out of range'],
    ['2026-06-01T10:60:00Z', 'minute out of range'],
    ['2026-06-01T10:30:60Z', 'second out of range'],
    // Years the control cannot render, INCLUDING the ones a four-digit input
    // crosses into through its offset or through 24:00.
    ['0000-01-01', 'year zero'],
    ['+010000-01-01T00:00:00Z', 'an extended year, stated'],
    ['9999-12-31T23:00:00-05:00', 'an extended year, crossed by offset'],
    ['9999-12-31T24:00:00Z', 'an extended year, crossed by end-of-day'],
    ['0001-01-01T00:00:00+05:00', 'year zero, crossed by offset'],
    // The DECLARED year is out of range even though the instant lands inside
    // it: checking only the result left this half invisible.
    ['0000-12-31T23:30:00-01:00', 'year zero declared, 0001 resulting'],
    ['0000-12-31T24:00:00Z', 'year zero declared, crossed by end-of-day'],
  ])('refuses %s (%s)', (raw) => {
    expect(parseIsoBound(raw)).toBeUndefined()
  })
})

describe('parseIsoBound accepts what a reader legitimately writes', () => {
  it.each([
    ['2026-06-01', '2026-06-01T00:00:00.000Z'],
    ['0001-01-01', '0001-01-01T00:00:00.000Z'],
    ['0099-01-01T00:00:00Z', '0099-01-01T00:00:00.000Z'],
    ['2028-02-29', '2028-02-29T00:00:00.000Z'],
    ['2000-02-29', '2000-02-29T00:00:00.000Z'],
    // An offset crossing UTC midnight is not a rollover — an earlier version
    // refused these as if the stated day did not exist.
    ['2026-06-01T00:30:00+02:00', '2026-05-31T22:30:00.000Z'],
    ['2026-06-01T23:30:00-02:00', '2026-06-02T01:30:00.000Z'],
    ['2026-06-01T10:30:45.123456789Z', '2026-06-01T10:30:45.123Z'],
    ['2026-06-01t10:30:00z', '2026-06-01T10:30:00.000Z'],
    ['2026-06-01 10:30', '2026-06-01T10:30:00.000Z'],
  ])('accepts %s as %s', (raw, expected) => {
    expect(parseIsoBound(raw)?.toISOString()).toBe(expected)
  })

  it.each([
    // A time with no offset is UTC, not the reader's local time. Two cases,
    // because one alone only discriminates on one side of Greenwich: read as
    // local, the first lands on the previous day east of UTC and the second on
    // the next day west of it.
    ['2026-06-01T00:30:00', '2026-06-01T00:30:00.000Z'],
    ['2026-06-01T23:30:00', '2026-06-01T23:30:00.000Z'],
  ])('reads %s as UTC', (raw, expected) => {
    expect(parseIsoBound(raw)?.toISOString()).toBe(expected)
  })

  // Said out loud rather than promised: the property "an offsetless time means
  // UTC" is UNOBSERVABLE on a process whose local time is already UTC, so on
  // such a box the two cases above cannot distinguish the implementation from
  // one that dropped the appended Z. A review found exactly that surviving
  // mutant. This states the condition instead of leaving the guarantee resting
  // on the machine that happened to run it.
  const processIsUTC =
    new Date('2026-06-01T00:30:00').getTime() === Date.UTC(2026, 5, 1, 0, 30)

  it.skipIf(processIsUTC)(
    'differs from the local-time reading, which is what the guarantee buys',
    () => {
      expect(parseIsoBound('2026-06-01T00:30:00')?.getTime()).not.toBe(
        new Date('2026-06-01T00:30:00').getTime(),
      )
    },
  )
})

describe('canonicalisation matches what each control can show', () => {
  it('collapses a day bound to UTC midnight', () => {
    // Keeping the time meant a link reading 2026-06-01 could query from 14:30
    // that day: identical on screen, different windows behind them.
    expect(isoDayBound('2026-06-01T14:30:45.123Z')).toBe(
      '2026-06-01T00:00:00.000Z',
    )
    expect(isoDayBound('2026-06-01')).toBe('2026-06-01T00:00:00.000Z')
  })

  it('collapses a minute bound to the minute', () => {
    expect(isoMinuteBound('2026-06-01T10:30:45.123Z')).toBe(
      '2026-06-01T10:30:00.000Z',
    )
  })

  it('propagates a refusal rather than inventing a bound', () => {
    expect(isoDayBound('2026-02-30')).toBeUndefined()
    expect(isoMinuteBound('2026-02-30')).toBeUndefined()
  })
})
