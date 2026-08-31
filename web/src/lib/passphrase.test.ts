// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { describe, expect, it } from 'vitest'
import {
  MIN_DR_PASSPHRASE_LENGTH,
  passphraseBelowFloor,
  passphraseLength,
  passphraseStrength,
} from './passphrase'

describe('passphrase floor (mirrors core/api/dr_handler.go)', () => {
  it('pins the backend floor value', () => {
    expect(MIN_DR_PASSPHRASE_LENGTH).toBe(12)
  })

  it('flags 1..11 characters and accepts 12', () => {
    expect(passphraseBelowFloor('x')).toBe(true)
    expect(passphraseBelowFloor('elevenchars')).toBe(true)
    expect(passphraseBelowFloor('twelve-chars')).toBe(false)
  })

  it('leaves empty to the required error, not the floor error', () => {
    expect(passphraseBelowFloor('')).toBe(false)
  })

  it('counts runes, not UTF-16 units (like Go utf8.RuneCountInString)', () => {
    const twelveEnye = 'ñ'.repeat(12)
    expect(passphraseLength(twelveEnye)).toBe(12)
    expect(passphraseBelowFloor(twelveEnye)).toBe(false)
    // 6 astral-plane chars = 12 UTF-16 units but only 6 runes → below floor.
    const sixAstral = '𝔞'.repeat(6)
    expect(sixAstral.length).toBe(12)
    expect(passphraseBelowFloor(sixAstral)).toBe(true)
  })
})

describe('passphraseStrength', () => {
  it('is weak under the floor, regardless of variety', () => {
    expect(passphraseStrength('aB3!aB3!aB3')).toBe('weak') // 11 chars
  })

  it('grows with length and character variety', () => {
    expect(passphraseStrength('twelve-chars')).toBe('fair')
    expect(passphraseStrength('sixteen--chars-x')).toBe('good')
    expect(passphraseStrength('Sixteen-Chars-9x')).toBe('strong')
    expect(passphraseStrength('twenty-characters-xx')).toBe('strong')
  })
})
