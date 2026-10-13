// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The effort a person keeps when they change the model: the same level when the new
// model takes it, none when it does not, so no level is shown that the model refuses.
import { describe, expect, it } from 'vitest'
import { effortAfterModel } from './model-choices'
import type { ProfileModelCatalogDTO } from './types'

const catalog: ProfileModelCatalogDTO = {
  profile_ref: 'ppf_a',
  driver: 'claude',
  state: 'fresh',
  default_model: 'big',
  free_text: true,
  checked_at: '2026-10-12T10:00:00Z',
  refresh_interval_seconds: 120,
  models: [
    { id: 'big', effort_levels: ['low', 'medium', 'high', 'max'] },
    { id: 'small', effort_levels: ['low', 'high'] },
  ],
}

describe('effortAfterModel', () => {
  it('keeps a level the new model takes', () => {
    expect(effortAfterModel('claude', catalog, 'small', '', 'high')).toBe(
      'high',
    )
  })

  it('forgets a level the new model does not take', () => {
    expect(effortAfterModel('claude', catalog, 'small', '', 'max')).toBe('')
  })

  it("judges the tool's default model by the catalog's own default", () => {
    expect(effortAfterModel('claude', catalog, '', '', 'max')).toBe('max')
  })

  it('keeps nothing while no model is known', () => {
    expect(effortAfterModel('claude', undefined, '', '', 'high')).toBe('')
  })

  it('uses the fallback model only when the catalog names none', () => {
    expect(effortAfterModel('codex', undefined, '', 'gpt-x', 'high')).toBe(
      'high',
    )
  })
})
