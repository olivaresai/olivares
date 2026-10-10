// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { describe, expect, it } from 'vitest'
import { isProfileName, nextFreeName, profileStem } from './profile-names'

describe('nextFreeName', () => {
  it('offers the tool key first, then -b, -c in the engine order', () => {
    expect(nextFreeName('claude', new Set())).toBe('claude')
    expect(nextFreeName('claude', new Set(['claude']))).toBe('claude-b')
    expect(nextFreeName('claude', new Set(['claude', 'claude-b']))).toBe(
      'claude-c',
    )
  })

  it('skips the names that are taken, not only the first ones', () => {
    expect(nextFreeName('codex', new Set(['codex-b']))).toBe('codex')
    expect(nextFreeName('codex', new Set(['codex', 'codex-c']))).toBe('codex-b')
  })

  it('counts a name taken by another tool: a name is unique across tools', () => {
    expect(nextFreeName('claude', new Set(['claude']))).toBe('claude-b')
    expect(nextFreeName('codex', new Set(['claude', 'codex-b']))).toBe('codex')
  })

  it('goes on after z with aa, ab (bijective base 26, never -a)', () => {
    const taken = new Set(['claude'])
    for (const c of 'bcdefghijklmnopqrstuvwxyz') taken.add(`claude-${c}`)
    expect(nextFreeName('claude', taken)).toBe('claude-aa')
    taken.add('claude-aa')
    expect(nextFreeName('claude', taken)).toBe('claude-ab')
    expect(nextFreeName('claude', new Set(['claude-a']))).toBe('claude')
  })

  it('gives no name when the tool key leaves no room for a suffix', () => {
    const stem = 'a'.repeat(32)
    expect(nextFreeName(stem, new Set([stem]))).toBe('')
  })

  it('gives no name for a tool key the engine would refuse', () => {
    expect(nextFreeName('Claude Code', new Set())).toBe('')
  })
})

describe('isProfileName', () => {
  it('accepts the engine shape: lowercase letters, digits and dashes, a letter first, at most 32', () => {
    expect(isProfileName('claude-b')).toBe(true)
    expect(isProfileName('work2')).toBe(true)
    expect(isProfileName('a'.repeat(32))).toBe(true)
  })

  it('refuses anything else, exactly as typed', () => {
    // prettier-ignore
    const nonASCIIName = 'é' // language-data: invalid profile name
    for (const bad of [
      '',
      'Claude',
      '2fast',
      '-b',
      'a b',
      'a_b',
      nonASCIIName,
      ' a',
    ]) {
      expect(isProfileName(bad)).toBe(false)
    }
    expect(isProfileName('a'.repeat(33))).toBe(false)
  })
})

describe('profileStem', () => {
  it("is the first word of the tool's product name, as the profile names are: gemini, gemini-b", () => {
    expect(profileStem('claude')).toBe('claude')
    expect(profileStem('codex')).toBe('codex')
    expect(profileStem('grok')).toBe('grok')
    expect(profileStem('opencode')).toBe('opencode')
    expect(profileStem('gemini-cli')).toBe('gemini')
  })

  it('is the tool key for a tool the console has no name for', () => {
    expect(profileStem('new-tool')).toBe('new-tool')
  })
})
