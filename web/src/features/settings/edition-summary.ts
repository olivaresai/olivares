// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useTranslation } from 'react-i18next'

export type SummaryLine = { text: string; lang?: 'en' }

/**
 * What Business and Enterprise include, in the words of docs/editions.md: vite.config.ts quotes
 * the lines between its console summary markers at build time. A line shows in the reader's
 * language when `edition.summary` holds it under its exact English text; a line the contract
 * changed has no translation yet and shows as written, marked English.
 */
export function useEditionSummary(): {
  business: SummaryLine[]
  enterprise: SummaryLine[]
} {
  const { t } = useTranslation('settings')
  const translated = t('edition.summary', { returnObjects: true }) as Record<
    string,
    string | undefined
  >
  const lines = (english: string[]) =>
    english.map((line): SummaryLine => {
      const text = translated[line]
      return text ? { text } : { text: line, lang: 'en' }
    })
  return {
    business: lines(__EDITION_SUMMARIES__.business),
    enterprise: lines(__EDITION_SUMMARIES__.enterprise),
  }
}
