// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// What a provider profile is called on every screen that lists one: the label it was
// given, else the account name the engine gave it, else nothing (the caller then shows
// the reference or the tool). Whitespace is no name.
import { describe, expect, it } from 'vitest'
import { profileName } from './types'

describe('profileName', () => {
  it('prefers the display name over the account name', () => {
    expect(
      profileName({ display_name: 'Team login', account_name: 'claude-b' }),
    ).toBe('Team login')
  })

  it('names a profile that is an account by its account name', () => {
    expect(profileName({ account_name: 'claude-b' })).toBe('claude-b')
  })

  it('treats whitespace as no name, on either field', () => {
    expect(profileName({ display_name: '   ', account_name: 'claude-b' })).toBe(
      'claude-b',
    )
    expect(
      profileName({ display_name: '  ', account_name: ' ' }),
    ).toBeUndefined()
  })

  it('gives nothing for a profile nobody has named', () => {
    expect(profileName({})).toBeUndefined()
  })
})
