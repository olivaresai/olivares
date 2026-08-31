// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Runtime i18n parity. `scripts/check-i18n-parity.mjs` guards the locale
// FILES in the push gate + CI; this test guards the runtime i18next STORE — i.e.
// that every namespace is actually *registered* for every language and resolves
// with the same keys English does. The two are independent implementations, so a
// bug in one (a wiring mistake, a dropped language in registerTranslations) is
// caught by the other. A missing key would fall back to English silently — these
// assertions make that a failing test instead of a shipped untranslated string.
import { describe, expect, it } from 'vitest'

import i18n, { LANGUAGE_CODES } from './index'

// Importing each feature's i18n entry registers its namespace as a side effect.
// `_intel` now carries the same `i18n/index.ts` every other feature has, so this
// glob picks it up: it used to be re-registered BY HAND here from its JSON, which
// let the test prove the store was complete while saying nothing about whether
// anything in the app registered it (it did not — see _intel/i18n/index.ts).
//
// MEASURED GAP, deliberately left for its own change (2026-08-05): this glob is one
// level deep, and so is `discoverNamespaces` in scripts/check-i18n-parity.mjs — so
// `automations/workflows/i18n` (namespace `automations-workflows`) is checked by
// NEITHER parity implementation. check-i18n-usage.mjs walks `**` and sees 58 English
// namespaces; parity sees 57. Widening this glob to `**` makes it fail immediately:
//   automations-workflows/es editor.validationSummary categories:
//     expected [ 'other' ] to deeply equal [ 'many', 'one', 'other' ]
// The bundle carries a bare `validationSummary` + `_other` in all 7 languages, i.e.
// no CLDR plural forms for es/fr/ru/de. That is a translation fix in someone else's
// strings, not this one — do not widen the glob without doing it.
import.meta.glob('@/features/*/i18n/index.ts', { eager: true })

const PLURAL_SUFFIX = /_(zero|one|two|few|many|other)$/
const pluralCats = (lng: string) =>
  new Set(
    new Intl.PluralRules(lng, { type: 'cardinal' }).resolvedOptions()
      .pluralCategories,
  )

type Flat = { regular: Set<string>; plurals: Map<string, Set<string>> }

/** Flatten a resource tree into non-plural key paths + plural bases→categories. */
function flatten(obj: unknown, prefix = '', acc?: Flat): Flat {
  const out: Flat = acc ?? { regular: new Set(), plurals: new Map() }
  if (!obj || typeof obj !== 'object') return out
  const node = obj as Record<string, unknown>
  const otherBases = new Set<string>()
  for (const k of Object.keys(node)) {
    const m = k.match(PLURAL_SUFFIX)
    if (m && m[1] === 'other') otherBases.add(k.slice(0, m.index))
  }
  for (const k of Object.keys(node)) {
    const v = node[k]
    const m = k.match(PLURAL_SUFFIX)
    if (m && otherBases.has(k.slice(0, m.index))) {
      const base = prefix + k.slice(0, m.index)
      if (!out.plurals.has(base)) out.plurals.set(base, new Set())
      out.plurals.get(base)!.add(m[1])
      continue
    }
    if (v && typeof v === 'object' && !Array.isArray(v))
      flatten(v, prefix + k + '.', out)
    else out.regular.add(prefix + k)
  }
  return out
}

const data = i18n.store.data as Record<string, Record<string, unknown>>
const namespaces = Object.keys(data.en).sort()
const others = LANGUAGE_CODES.filter((l) => l !== 'en')

describe('i18n runtime parity', () => {
  it('registered the expected languages and a non-trivial namespace set', () => {
    expect([...LANGUAGE_CODES].sort()).toEqual([
      'de',
      'en',
      'es',
      'fr',
      'ja',
      'ru',
      'zh',
    ])
    // Foundation (5) + every feature namespace.
    expect(namespaces.length).toBeGreaterThanOrEqual(30)
  })

  it.each(namespaces)(
    'namespace "%s" is registered for every language',
    (ns) => {
      for (const lng of LANGUAGE_CODES)
        expect(data[lng]?.[ns], `${ns} missing for ${lng}`).toBeTruthy()
    },
  )

  it.each(namespaces)(
    'namespace "%s" has key + CLDR-plural parity with English',
    (ns) => {
      const en = flatten(data.en[ns])
      for (const lng of others) {
        const lg = flatten(data[lng][ns])
        // Non-plural keys: identical set.
        expect([...lg.regular].sort(), `${ns}/${lng} regular keys`).toEqual(
          [...en.regular].sort(),
        )
        // Plural bases: identical set of bases…
        expect(
          [...lg.plurals.keys()].sort(),
          `${ns}/${lng} plural bases`,
        ).toEqual([...en.plurals.keys()].sort())
        // …each carrying exactly the language's CLDR categories.
        const cats = [...pluralCats(lng)].sort()
        for (const base of en.plurals.keys())
          expect(
            [...lg.plurals.get(base)!].sort(),
            `${ns}/${lng} ${base} categories`,
          ).toEqual(cats)
      }
    },
  )
})
