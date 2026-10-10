// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { describe, expect, it } from 'vitest'
import i18n, { i18nReady, LANGUAGE_CODES } from '@/lib/i18n'
import { FEATURE_VIEWS } from './registry'

await i18nReady

describe('edition navigation resources before lazy views load', () => {
  it.each([...LANGUAGE_CODES])('%s labels only mounted views', (language) => {
    // Inspect this language directly: English fallback must not hide a missing
    // eager Business registration, and absent paid views must not leave labels.
    const items = i18n.getResource(language, 'nav', 'items') as Record<
      string,
      unknown
    >
    expect(Object.keys(items).sort()).toEqual(
      [...FEATURE_VIEWS.map(({ id }) => id), 'settings'].sort(),
    )
  })
})
