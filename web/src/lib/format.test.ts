// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { afterAll, describe, expect, it } from 'vitest'
import i18n from './i18n'
import {
  formatDate,
  formatDayKey,
  formatDuration,
  formatFraction,
  formatInt,
  formatLatency,
  formatMicroUsd,
  formatPercent,
  formatScore,
  formatTokens,
  truncateHash,
} from './format'

describe('formatMicroUsd', () => {
  it('renders dollars from integer micro-USD', () => {
    // 9_000_000 µUSD = $9.00
    expect(formatMicroUsd(9_000_000)).toBe('$9.00')
    // 1_000_000 µUSD = $1.00 (a budget limit)
    expect(formatMicroUsd(1_000_000)).toBe('$1.00')
  })

  it('keeps sub-cent per-token prices truthful, not rounded to zero', () => {
    // 15 µUSD = $0.000015 — must not collapse to $0.00
    const out = formatMicroUsd(15)
    expect(out).not.toBe('$0.00')
    expect(out).toContain('0.0000')
  })

  it('compacts large sums on request', () => {
    expect(formatMicroUsd(12_000_000_000, { compact: true })).toBe('$12K')
  })

  it('returns an em-dash for nil', () => {
    expect(formatMicroUsd(null)).toBe('—')
    expect(formatMicroUsd(undefined)).toBe('—')
  })
})

describe('percentages', () => {
  it('formats an already-percent value', () => {
    expect(formatPercent(60)).toBe('60%')
    expect(formatPercent(180)).toBe('180%')
  })
  it('formats a 0..1 fraction as a percent', () => {
    expect(formatFraction(0.8)).toBe('80%')
  })
})

describe('tokens / score / latency / duration', () => {
  it('compacts large token counts', () => {
    expect(formatTokens(1_200_000)).toBe('1.2M')
    expect(formatTokens(42)).toBe('42')
  })
  it('formats a 0..1 score to two decimals', () => {
    expect(formatScore(0.9234)).toBe('0.92')
  })
  it('formats latency and duration', () => {
    expect(formatLatency(110)).toBe('110 ms')
    expect(formatDuration(42_000)).toBe('42.0s')
    expect(formatDuration(62_000)).toBe('1m 02s')
    expect(formatDuration(300)).toBe('300ms')
  })
})

describe('truncateHash', () => {
  it('keeps a head…tail fingerprint and leaves short strings intact', () => {
    const long = 'a'.repeat(8) + 'b'.repeat(50) + 'c'.repeat(6)
    expect(truncateHash(long)).toBe('aaaaaaaa…cccccc')
    expect(truncateHash('deadbeef')).toBe('deadbeef')
    expect(truncateHash('')).toBe('—')
  })
})

describe('formatDayKey', () => {
  it('renders a YYYY-MM-DD bucket as a short axis label', () => {
    // Locale-formatted; assert it mentions the month and day, not the raw key.
    const out = formatDayKey('2026-06-01')
    expect(out).toMatch(/Jun/)
    expect(out).toMatch(/1/)
  })
})

//the formatters must follow the ACTIVE UI language, not a hardcoded en-US —
// otherwise a de/fr/ja operator sees US number/date formats. The default locale is
// currentLanguage(); changing the language changes the output with no explicit arg.
describe('locale awareness (active UI language)', () => {
  afterAll(async () => {
    await i18n.changeLanguage('en')
  })

  it('groups thousands per the active language when no locale is passed', async () => {
    await i18n.changeLanguage('de')
    // German groups with '.' and would read "1.234.567"; en reads "1,234,567".
    expect(formatInt(1234567)).toBe('1.234.567')
    await i18n.changeLanguage('en')
    expect(formatInt(1234567)).toBe('1,234,567')
  })

  it('localizes currency and dates to the active language', async () => {
    await i18n.changeLanguage('de')
    const deDate = formatDate('2026-06-15T00:00:00Z')
    // The USD amount uses de grouping ("1.234,00 $"-style), not en's "1,234.00".
    const deUsd = formatMicroUsd(1_234_000_000)
    await i18n.changeLanguage('en')
    const enDate = formatDate('2026-06-15T00:00:00Z')

    expect(deDate).not.toBe(enDate) // localized, not a fixed en-US render
    expect(deDate).toMatch(/^15/) // German medium date is day-first (15.06.2026)
    expect(enDate).toMatch(/Jun/) // English medium uses the month abbreviation
    expect(deUsd).toContain('1.234') // de groups thousands with a dot
  })

  it('renders trend-axis day keys in the active language too', async () => {
    // The trap: formatDayKey defaulted to a hardcoded en-US while every
    // sibling formatter follows currentLanguage() — a de operator's chart axes
    // read "Dec 1" instead of "1. Dez.". December because its de abbreviation
    // ("Dez.") cannot be mistaken for the English one ("Dec").
    await i18n.changeLanguage('de')
    expect(formatDayKey('2026-12-01')).toContain('Dez')
    await i18n.changeLanguage('en')
    expect(formatDayKey('2026-12-01')).toContain('Dec')
  })
})
