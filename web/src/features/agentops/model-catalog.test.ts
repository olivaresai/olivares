// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// What a person may pick for a session, derived from the tool's own live catalog: the
// groups of the list, the model a session starts on and the effort levels that model
// offers. Nothing here names a model: a new vendor model is only a new catalog entry.
import { describe, expect, it } from 'vitest'
import { effortChoices, groupModels, startModel } from './model-catalog'
import type { ProfileModelCatalogDTO, ProfileModelDTO } from './types'

const model = (
  id: string,
  over: Partial<ProfileModelDTO> = {},
): ProfileModelDTO => ({
  id,
  ...over,
})

const catalog = (
  models: ProfileModelDTO[],
  over: Partial<ProfileModelCatalogDTO> = {},
): ProfileModelCatalogDTO => ({
  profile_ref: 'ppf_a',
  driver: 'claude',
  state: 'fresh',
  models,
  default_model: '',
  free_text: true,
  checked_at: '2026-10-12T10:00:00Z',
  refresh_interval_seconds: 120,
  ...over,
})

describe('groupModels', () => {
  it('puts favorites first, retired models last, and keeps the tool order inside a group', () => {
    const groups = groupModels(
      [
        model('opus'),
        model('old-a', { is_legacy: true }),
        model('sonnet'),
        model('old-b', { is_legacy: true }),
        model('haiku'),
      ],
      new Set(['sonnet', 'old-b']),
    )
    expect(groups.favorites.map((m) => m.id)).toEqual(['sonnet', 'old-b'])
    expect(groups.models.map((m) => m.id)).toEqual(['opus', 'haiku'])
    expect(groups.legacy.map((m) => m.id)).toEqual(['old-a'])
  })

  it('ignores a favorite the tool no longer lists', () => {
    const groups = groupModels([model('opus')], new Set(['gone']))
    expect(groups.favorites).toEqual([])
    expect(groups.models.map((m) => m.id)).toEqual(['opus'])
  })
})

describe('startModel', () => {
  it('is the model chosen, whatever the catalog marks', () => {
    const c = catalog([model('a', { is_default: true }), model('b')])
    expect(startModel(c, 'b')?.id).toBe('b')
  })

  it("is the catalog's default_model when none is chosen", () => {
    const c = catalog([model('a'), model('b')], { default_model: 'b' })
    expect(startModel(c, '')?.id).toBe('b')
  })

  it('is the model the tool marks as its default when default_model is empty', () => {
    const c = catalog([model('a'), model('b', { is_default: true })])
    expect(startModel(c, '')?.id).toBe('b')
  })

  it('is nothing when no model is chosen and the tool names none', () => {
    expect(startModel(catalog([model('a'), model('b')]), '')).toBeUndefined()
    expect(startModel(undefined, '')).toBeUndefined()
  })

  it('keeps a typed model the catalog does not list, as that id alone', () => {
    expect(startModel(catalog([model('a')]), 'typed')).toEqual({ id: 'typed' })
    expect(startModel(undefined, 'typed')).toEqual({ id: 'typed' })
  })
})

describe('effortChoices', () => {
  it('refuses to offer an effort while no model is known', () => {
    expect(effortChoices('claude', undefined)).toBeNull()
  })

  it("lists the levels the model reports, in the tool's order", () => {
    expect(
      effortChoices(
        'codex',
        model('m', { effort_levels: ['minimal', 'low', 'high'] }),
      ),
    ).toEqual(['minimal', 'low', 'high'])
  })

  it('offers a Claude launch only the levels it accepts, so no choice ends in a 400', () => {
    expect(
      effortChoices(
        'claude',
        model('m', {
          effort_levels: ['low', 'medium', 'high', 'xhigh', 'max', 'ultra'],
        }),
      ),
    ).toEqual(['low', 'medium', 'high', 'xhigh', 'max'])
  })

  it('falls back to the launch levels when the tool does not report them', () => {
    expect(effortChoices('claude', model('m'))).toEqual([
      'low',
      'medium',
      'high',
      'xhigh',
      'max',
    ])
    expect(effortChoices('codex', model('m'))).toEqual([
      'low',
      'medium',
      'high',
      'xhigh',
      'max',
    ])
  })
})
