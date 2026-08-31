// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Structural guard: every FEATURE_VIEWS entry must carry a UNIQUE icon.
// The sidebar and the command palette are generated from the registry, so two
// views sharing an icon (Rocket on onboarding+deploy, ScrollText on three views
// at once at the time this guard landed) read as the same destination to a user
// scanning the nav — and to a screen-magnifier user the icon IS the landmark.
import { describe, expect, it } from 'vitest'
import { FEATURE_VIEWS } from './registry'

describe('FEATURE_VIEWS icon uniqueness', () => {
  it('never assigns the same icon to two registered views', () => {
    const byIcon = new Map<string, string[]>()
    for (const v of FEATURE_VIEWS) {
      // displayName survives minification in lucide-react; fall back to name.
      const icon =
        (v.icon as { displayName?: string }).displayName ?? v.icon.name
      byIcon.set(icon, [...(byIcon.get(icon) ?? []), v.id])
    }
    const duplicated = [...byIcon.entries()]
      .filter(([, ids]) => ids.length > 1)
      .map(([icon, ids]) => `${icon}: ${ids.join(', ')}`)

    expect(
      duplicated,
      `Icons assigned to more than one view (pick a distinct lucide icon per view):\n${duplicated.join('\n')}`,
    ).toEqual([])
  })
})
