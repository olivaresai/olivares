// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// PRODUCT WORDS. A person reading Communications sees "you", "channel", "access" and
// "Load more", never the engine's own vocabulary: principal, bits, continuation, cursor.
// The English locale is the contract; every other locale must say the same thing in its
// own product words, so each one carries the same ban in its own terms.
import { describe, expect, it } from 'vitest'
import sandboxEn from '@/features/sandbox/i18n/en.json'
import voiceEn from '@/features/voice/i18n/en.json'
import workEn from '@/features/work/i18n/en.json'
import { de, en, es, fr, it as itLocale, ja, ko, pt, ru, uk, zh } from './index'

/** The engine words, per locale. A locale-specific word that also has an ordinary
 * meaning (Spanish and French "principal", Chinese and Korean "subject", French "debit")
 * is matched only in its engine sense. */
const ES_PREFIX = '\\b(?:este|el|al|del|ese|actual) ' // language-data: locale vocabulary
const ES_SUFFIX = 'principal\\b|\\bbits?\\b|cursor|continuación' // language-data: locale vocabulary
const SPANISH_PRINCIPAL = ES_PREFIX + ES_SUFFIX

const ENGINE_WORDS: Record<string, { locale: object; words: RegExp }> = {
  en: { locale: en, words: /\bprincipals?\b|\bbits?\b|continuation|cursor/i },
  de: { locale: de, words: /principal|\bbits?\b|cursor|fortsetzung/i },
  es: {
    locale: es,
    words: new RegExp(SPANISH_PRINCIPAL, 'i'),
  },
  fr: {
    locale: fr,
    words:
      /\b(?:ce|le|du|au|actuel) principal\b|(?<!\p{L})bits?(?!\p{L})|curseur|continuation/iu,
  },
  it: { locale: itLocale, words: /\bbits?\b|cursore|continuazione/i },
  ja: { locale: ja, words: /プリンシパル|ビット|カーソル|継続トークン/ },
  ko: { locale: ko, words: /(?:이|현재) 주체|비트|커서|연속 지점/ },
  ru: {
    locale: ru,
    words:
      /принципал|(?<!\p{L})бит(?:а|ов|ом|ы|е|у)?(?!\p{L})|курсор|по продолжению/iu,
  },
  zh: { locale: zh, words: /此主体|本地位|写位|读位|个位|的位|游标|续页|续读/ },
}

/** Every string value of a locale, with its dotted key. */
function strings(node: unknown, path = ''): [string, string][] {
  if (typeof node === 'string') return [[path, node]]
  if (node === null || typeof node !== 'object') return []
  return Object.entries(node).flatMap(([k, v]) =>
    strings(v, path ? `${path}.${k}` : k),
  )
}

describe('communications copy', () => {
  for (const [code, { locale, words }] of Object.entries(ENGINE_WORDS)) {
    it(`${code}: no user-visible string speaks the engine's words`, () => {
      const hits = strings(locale)
        .filter(([, value]) => words.test(value))
        .map(([key]) => key)
      expect(hits).toEqual([])
    })
  }
})

/** Every locale but the English contract. */
const OTHER_LOCALES = { de, es, fr, it: itLocale, ja, ko, pt, ru, uk, zh }

/** The word each language's administration line says «never» with. */
const NEVER: Record<keyof typeof OTHER_LOCALES, string> = {
  de: 'nie',
  es: 'nunca',
  fr: 'jamais',
  it: 'mai',
  ja: '決して',
  ko: '절대',
  pt: 'nunca',
  ru: 'никогда',
  uk: 'ніколи',
  zh: '绝不',
}

/** The line under a page's title: what the page is for, in plain words. */
const PAGE_LINES: [string, string][] = [
  ['work', workEn.subtitle],
  ['voice', voiceEn.description],
  ['sandbox', sandboxEn.description],
  ...Object.entries(en.doors).map(([door, words]): [string, string] => [
    `communications ${door}`,
    words.description,
  ]),
]

describe('each page of work and channels says its job in one line', () => {
  it.each(PAGE_LINES)('%s: one short sentence in plain words', (_, line) => {
    expect(line.length).toBeLessThanOrEqual(80)
    expect(line).toMatch(/^[^.!?]+\.$/)
    expect(line).not.toMatch(/\bengine\b|\bkernel\b|selected workspace/i)
  })

  it('administration says that it never opens the messages', () => {
    expect(en.doors.administration.description).toMatch(/never lets you read/)
  })

  it.each(Object.entries(OTHER_LOCALES))(
    '%s: administration says it in its own words',
    (code, locale) => {
      const line = locale.doors.administration.description
      expect(line).toContain(NEVER[code as keyof typeof NEVER])
      expect(line).not.toBe(en.doors.administration.description)
    },
  )
})
