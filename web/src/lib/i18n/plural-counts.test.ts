// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Rendered CLDR plural guard. The parity test checks that every locale carries
// the same plural keys as English; it does not check the wording or that a
// count reaches the right form. This renders each count-based string at 1 and
// 2 and fails on a bare key fallback, a leftover placeholder, a missing number,
// or a singular and plural form that read the same in a language that has both.
import { describe, expect, it } from 'vitest'

import i18n, { i18nReady, LANGUAGE_CODES } from './index'

import.meta.glob('@/features/**/i18n/index.ts', { eager: true })

await i18nReady
await i18n.loadLanguages([...LANGUAGE_CODES])

// Every counted string: each `<key>_other` in the English resources, so a new
// plural key is guarded without anyone listing it. Every placeholder in it
// (count, or a formatted copy such as n) is rendered as the count.
type Counted = [string, string, string[]]
const COUNTED: Counted[] = []
const collect = (ns: string, node: unknown, path: string) => {
  if (typeof node === 'string') {
    if (!path.endsWith('_other')) return
    const names = [...node.matchAll(/\{\{\s*(\w+)/g)].map((m) => m[1])
    COUNTED.push([ns, path.slice(0, -'_other'.length), names])
  } else if (node && typeof node === 'object')
    for (const [k, v] of Object.entries(node))
      collect(ns, v, path ? `${path}.${k}` : k)
}
for (const ns of Object.keys(i18n.store.data.en ?? {}))
  collect(ns, i18n.getResourceBundle('en', ns), '')

// Languages whose counted nouns inflect for number: where English reads 1 and 2
// differently, these must too. de and it are left out: their counted nouns are
// invariant here (Entwickler, Rahmen, team, tenant), so singular and plural read alike.
const DISTINCT_ONE_OTHER = new Set(['en', 'es', 'fr', 'ru'])

// English inflects but the language's noun does not ("fois" in 1 fois, 2 fois).
const INVARIANT = new Set(['fr sessions:activity.compactedTimes'])

const render = (
  ns: string,
  key: string,
  lng: string,
  count: number,
  names: string[],
) =>
  i18n.t(key, {
    ns,
    lng,
    ...Object.fromEntries(names.map((n) => [n, String(count)])),
    count,
  }) as string

// Compare the wording, not the digit: '1' and '2' always differ.
const shape = (text: string, count: number) => text.replace(String(count), '#')

describe('counted i18n strings render the right plural form', () => {
  it.each(COUNTED)(
    '%s:%s renders 1 and 2 in every locale',
    (ns, key, names) => {
      for (const lng of LANGUAGE_CODES) {
        // Checked on the raw resource: a missing form falls back to English
        // (fallbackLng), so the rendered string alone would hide the gap.
        // The sample covers every CLDR category (ru: one, few, many, other).
        for (const count of [0, 1, 2, 5, 11, 21]) {
          const form = new Intl.PluralRules(lng).select(count)
          expect(
            i18n.getResource(lng, ns, `${key}_${form}`),
            `${ns}/${key} ${lng} has no "${form}" form for count ${count}`,
          ).toBeTypeOf('string')
        }
        const enOne = render(ns, key, 'en', 1, names)
        const englishInflects =
          shape(enOne, 1) !== shape(render(ns, key, 'en', 2, names), 2)
        const one = render(ns, key, lng, 1, names)
        const two = render(ns, key, lng, 2, names)

        expect(
          one,
          `${ns}/${key} ${lng} count 1 fell back to the key`,
        ).not.toBe(key)
        expect(
          two,
          `${ns}/${key} ${lng} count 2 fell back to the key`,
        ).not.toBe(key)
        expect(
          one,
          `${ns}/${key} ${lng} count 1 left a placeholder`,
        ).not.toMatch(/\{\{/)
        expect(
          two,
          `${ns}/${key} ${lng} count 2 left a placeholder`,
        ).not.toMatch(/\{\{/)
        if (names.length > 0) {
          // English may drop the digit in the singular ("the session").
          if (enOne.includes('1'))
            expect(
              one,
              `${ns}/${key} ${lng} count 1 lost the number`,
            ).toContain('1')
          expect(two, `${ns}/${key} ${lng} count 2 lost the number`).toContain(
            '2',
          )
        }
        if (
          DISTINCT_ONE_OTHER.has(lng) &&
          englishInflects &&
          !INVARIANT.has(`${lng} ${ns}:${key}`)
        ) {
          expect(
            shape(one, 1),
            `${ns}/${key} ${lng} has no distinct singular form`,
          ).not.toBe(shape(two, 2))
        }
      }
    },
  )
})
