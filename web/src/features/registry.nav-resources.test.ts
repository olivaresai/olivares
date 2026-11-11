// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { describe, expect, it } from 'vitest'
import i18n, { i18nReady, LANGUAGE_CODES } from '@/lib/i18n'
import { BUSINESS_ONLY_VIEW_IDS, FEATURE_VIEWS } from './registry'
import { PLACES } from './navigation/places'

await i18nReady

describe('edition navigation resources before lazy views load', () => {
  it.each([...LANGUAGE_CODES])('%s labels only mounted views', (language) => {
    // Inspect this language directly: English fallback must not hide a missing
    // eager Business registration, and absent paid views must not leave labels.
    const items = i18n.getResource(language, 'nav', 'items') as Record<
      string,
      unknown
    >
    // The pages of Business-only source keep their labels here, served or not.
    expect(Object.keys(items).sort()).toEqual(
      [
        ...new Set([
          ...FEATURE_VIEWS.map(({ id }) => id),
          ...BUSINESS_ONLY_VIEW_IDS,
          'settings',
          // A section a place opens on as a page of its own (Rules: Approvals) is
          // labelled like the Settings utility: it mounts as its view's tab.
          ...PLACES.flatMap((p) => (p.section ? [p.section.key] : [])),
        ]),
      ].sort(),
    )
  })
})
