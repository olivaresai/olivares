// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { describe, expect, it } from 'vitest'
import { isCommunityEdition } from './edition'

describe('isCommunityEdition', () => {
  it('is true only for a build that says it is Community', () => {
    expect(isCommunityEdition('community')).toBe(true)
  })

  it.each(['enterprise', 'business', '', undefined])(
    'is false for %j: an unnamed edition is unknown, never Community',
    (edition) => {
      expect(isCommunityEdition(edition)).toBe(false)
    },
  )
})
