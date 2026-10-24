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

// The page has one name: the areas, the palette and the directory read the item label. The
// top bar names the PLACE that opens it, Tools (#2544), and the page keeps its own name until
// the Tools place issue (#2552) renames it; then this pin fails and goes back to equality.
describe('the AI tools page name', () => {
  it.each(Object.entries({ de, en, es, fr, ja, ru, zh }))(
    'is the page, and the top bar names its place, until #2552 renames the page (%s)',
    (_, nav) => {
      expect(nav.items['agent-tools']).toBeTruthy()
      expect(nav.shell.journeys['agent-tools']).toBeTruthy()
      expect(nav.items['agent-tools']).not.toBe(
        nav.shell.journeys['agent-tools'],
      )
    },
  )
})
