// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { describe, expect, it } from 'vitest'
import de from '@/lib/i18n/locales/de/nav.json'
import en from '@/lib/i18n/locales/en/nav.json'
import es from '@/lib/i18n/locales/es/nav.json'
import fr from '@/lib/i18n/locales/fr/nav.json'
import ja from '@/lib/i18n/locales/ja/nav.json'
import ru from '@/lib/i18n/locales/ru/nav.json'
import zh from '@/lib/i18n/locales/zh/nav.json'

// The page has one name: the pinned destination and the areas, the palette and the
// directory (which read the item label) all call it the same.
describe('the AI tools page name', () => {
  it.each(Object.entries({ de, en, es, fr, ja, ru, zh }))(
    'is the same in the item list and the pinned destinations (%s)',
    (_, nav) => {
      expect(nav.items['agent-tools']).toBe(nav.shell.journeys['agent-tools'])
    },
  )
})
