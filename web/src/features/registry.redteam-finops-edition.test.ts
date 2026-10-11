// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { describe, expect, it } from 'vitest'
import { FEATURE_EXTENSIONS } from './extensions'
import { FEATURE_VIEWS, ROUTE_ALIASES } from './registry'

describe('Business red-team and FinOps console entries', () => {
  for (const [id, path] of [
    ['redteam', '/red-team'],
    ['finops', '/finops'],
    ['team-costs', '/team-costs'],
  ]) {
    it(`${id} is available only through the paid console extension`, () => {
      const extension = FEATURE_EXTENSIONS.find((view) => view.id === id)
      const entry = FEATURE_VIEWS.find((view) => view.id === id)
      expect(entry?.path).toBe(extension?.path)
      if (extension) {
        expect(entry?.path).toBe(path)
        expect(ROUTE_ALIASES.some((alias) => alias.from === path)).toBe(false)
      } else {
        expect(entry).toBeUndefined()
        expect(ROUTE_ALIASES.some((alias) => alias.from === path)).toBe(true)
      }
    })
  }
})
