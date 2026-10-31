// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { describe, expect, it } from 'vitest'
import de from '@/lib/i18n/locales/de/nav.json'
import en from '@/lib/i18n/locales/en/nav.json'
import es from '@/lib/i18n/locales/es/nav.json'
import fr from '@/lib/i18n/locales/fr/nav.json'
import it_ from '@/lib/i18n/locales/it/nav.json'
import ja from '@/lib/i18n/locales/ja/nav.json'
import ko from '@/lib/i18n/locales/ko/nav.json'
import ru from '@/lib/i18n/locales/ru/nav.json'
import zh from '@/lib/i18n/locales/zh/nav.json'

// One name per thing: a place of the left navigation whose first page already has a name is
// called by that name (AI tools, Sessions, Now), so the sidebar, the page title, the palette
// and the row above a page read the same word in every language.
const SAME_NAME: Readonly<Record<string, string>> = {
  now: 'home',
  sessions: 'sessions',
  'ai-tools': 'agent-tools',
  models: 'models',
  'mcp-skills': 'capabilities',
  security: 'security',
  settings: 'settings',
  access: 'accessMap',
}

describe('a place and its first page share one name', () => {
  it.each(Object.entries({ de, en, es, fr, it: it_, ja, ko, ru, zh }))(
    'in %s',
    (_, nav) => {
      for (const [place, page] of Object.entries(SAME_NAME)) {
        const items = nav.items as Record<string, string>
        const places = nav.places as Record<string, string>
        expect(items[page], page).toBeTruthy()
        expect(places[place], place).toBe(items[page])
      }
    },
  )
})
