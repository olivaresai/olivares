// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
import { expect, it } from 'vitest'
import { FEATURE_VIEWS } from './registry'
import { FEATURE_EXTENSIONS } from './extensions'

it('organization management pages belong only to the private edition extensions', () => {
  for (const id of ['tenants', 'residency']) {
    expect(FEATURE_VIEWS.some((view) => view.id === id)).toBe(
      FEATURE_EXTENSIONS.some((view) => view.id === id),
    )
  }
})
