// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// A person's model list for one profile: which of the tool's models the pickers offer, in
// what order, and the models they added that the tool does not list.
import { describe, expect, it } from 'vitest'
import {
  applyPreferences,
  decodePreferences,
  moveModel,
  NO_PREFERENCES,
} from './model-preferences'
import type { ProfileModelDTO } from './types'

const m = (id: string): ProfileModelDTO => ({ id })
const models = [m('opus'), m('sonnet'), m('haiku')]
const ids = (list: readonly ProfileModelDTO[]) => list.map((x) => x.id)

describe('model preferences', () => {
  it("offers the tool's models in the tool's order when nothing is set", () => {
    expect(ids(applyPreferences(models, NO_PREFERENCES))).toEqual([
      'opus',
      'sonnet',
      'haiku',
    ])
  })

  it('drops hidden models, puts ordered ones first and adds custom models last', () => {
    const prefs = {
      hidden: ['sonnet'],
      order: ['haiku', 'opus'],
      custom: ['my-model', 'opus'],
    }
    expect(ids(applyPreferences(models, prefs))).toEqual([
      'haiku',
      'opus',
      'my-model',
    ])
    // The management list keeps the hidden ones, so they can be switched back on.
    expect(ids(applyPreferences(models, prefs, true))).toEqual([
      'haiku',
      'opus',
      'sonnet',
      'my-model',
    ])
  })

  it('moves a model one place and leaves the first one where it is', () => {
    const shown = ['opus', 'sonnet', 'haiku']
    expect(moveModel(shown, 'haiku', -1)).toEqual(['opus', 'haiku', 'sonnet'])
    expect(moveModel(shown, 'opus', -1)).toEqual(shown)
  })

  it('reads a stored value of this profile only, and nothing from a malformed one', () => {
    const raw = JSON.stringify({
      version: 1,
      profiles: { ppf_b: { hidden: ['sonnet'], order: [], custom: ['x'] } },
    })
    expect(decodePreferences(raw, 'ppf_b').hidden).toEqual(['sonnet'])
    expect(decodePreferences(raw, 'ppf_c')).toEqual(NO_PREFERENCES)
    expect(decodePreferences('{not json', 'ppf_b')).toEqual(NO_PREFERENCES)
  })
})
