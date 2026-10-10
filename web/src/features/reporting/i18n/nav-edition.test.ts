// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { describe, expect, it } from 'vitest'
import { FEATURE_EXTENSIONS } from '@/features/extensions'
import { FEATURE_VIEWS } from '@/features/registry'
import { LANGUAGE_CODES } from '@/lib/i18n'
import { en, es, zh, ja, de, ru, fr } from './index'

const bundles = { en, es, zh, ja, de, ru, fr }
const labels = {
  en: 'Reports',
  es: 'Informes',
  zh: '报告',
  ja: 'レポート',
  de: 'Berichte',
  ru: 'Отчёты',
  fr: 'Rapports',
}

describe('reporting navigation edition boundary', () => {
  it('takes the reporting view only from the extension composition', () => {
    expect(FEATURE_VIEWS.some(({ id }) => id === 'reporting')).toBe(
      FEATURE_EXTENSIONS.some(({ id }) => id === 'reporting'),
    )
  })

  it.each(LANGUAGE_CODES)(
    '%s keeps the Business label without claiming a Community nav item',
    (language) => {
      const nav = JSON.parse(
        readFileSync(
          resolve(__dirname, '../../../lib/i18n/locales', language, 'nav.json'),
          'utf8',
        ),
      ) as { items: Record<string, string> }
      expect(bundles[language].title).toBe(labels[language])
      expect(nav.items).not.toHaveProperty('reporting')
    },
  )
})
