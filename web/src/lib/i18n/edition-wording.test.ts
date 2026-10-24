// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE PAID EDITION'S NAME. docs/editions.md calls what the commercial license adds
// Business, in families (Identity & Scale, Compliance Packs, ...). The console used to
// tell users to "rebuild with the enterprise tag" (a build nobody can make from the
// public tree) and to call Business features "enterprise" or "the business build".
// This reads every locale's text, so a translation cannot keep the old words after the
// English is fixed. Keys, error codes and JSON values such as `enforced_by: "enterprise"`
// are published contracts and are not text: only the words a user reads are checked.
// The second block holds the per-locale wording a finished translation keeps.
import { describe, expect, it } from 'vitest'

import { LANGUAGE_CODES } from './index'

const files = import.meta.glob(
  ['@/features/**/i18n/*.json', '@/lib/i18n/locales/**/*.json'],
  { eager: true, import: 'default' },
) as Record<string, unknown>

/** The word for "enterprise" in each locale (the Latin ones keep it as a loanword). */
const ENTERPRISE =
  /enterprise|エンタープライズ|企業|корпоратив|энтерпрайз|企业|entreprise|unternehmen|empresarial/i

/** "business build" in each locale: Business is an edition users license, not a build. */
// prettier-ignore
const businessBuildPattern = '(?:business|ビジネス|商[业業]|бизнес)[ -]?(?:build|version|versión|compilaci[oó]n|ビルド|сборк\\w*|构建)|(?:build|version|versión|compilaci[oó]n|сборк\\w*)[ -]business' // language-data: multilingual edition-wording rejection pattern
const BUSINESS_BUILD = new RegExp(businessBuildPattern, 'i')

/**
 * Community, Business and Enterprise are names, not words: every language keeps them in
 * Latin script. Per language, the translated edition word its catalog used to carry
 * ("community edition", "business edition"). A language joins this list when its
 * catalog is clean; the pattern is the old translated word and nothing else.
 */
const zhEditionNames = '社区版|商业版' // language-data: zh translated edition names, rejected
const TRANSLATED_EDITION_NAMES: Record<string, RegExp> = {
  zh: new RegExp(zhEditionNames),
}

/**
 * Where "enterprise" is the actual word: the tier name in a sample list the operator
 * types, the licensed product tier the edition badge names, and the SCIM/audit-export
 * gate, whose placement is the owner's to settle (docs/editions.md puts inbound SCIM
 * in Community).
 */
const ALLOWED = new Set([
  'settings.edition.badge.enterprise',
  'console.granular.modelGroups.tierSelectorsHint',
  'identity.console.blindSpots.auditExport',
])

type Entry = { locale: string; where: string; text: string }

function leaves(node: unknown, path: string, out: [string, string][]) {
  if (typeof node === 'string') out.push([path, node])
  else if (node && typeof node === 'object')
    for (const [k, v] of Object.entries(node))
      leaves(v, path ? `${path}.${k}` : k, out)
}

function entries(): Entry[] {
  const out: Entry[] = []
  for (const [file, json] of Object.entries(files)) {
    const feature = /features\/(.+)\/i18n\/([^/.]+)\.json$/.exec(file)
    const lib = /locales\/([^/]+)\/([^/.]+)\.json$/.exec(file)
    const [locale, ns] = feature ? [feature[2], feature[1]] : [lib![1], lib![2]]
    const flat: [string, string][] = []
    leaves(json, '', flat)
    for (const [path, text] of flat)
      out.push({ locale, where: `${ns}.${path}`, text })
  }
  return out
}

const all = entries()

describe('edition wording', () => {
  it('reads every locale, from the same files', () => {
    const namespaces = new Map<string, Set<string>>()
    for (const e of all) {
      const ns = e.where.slice(0, e.where.indexOf('.'))
      namespaces.set(e.locale, (namespaces.get(e.locale) ?? new Set()).add(ns))
    }
    expect([...namespaces.keys()].sort()).toEqual([...LANGUAGE_CODES].sort())
    const english = [...namespaces.get('en')!].sort()
    expect(english.length).toBeGreaterThan(40)
    for (const [locale, set] of namespaces)
      expect([...set].sort(), `${locale} namespaces`).toEqual(english)
  })

  it('never names the enterprise build, tag or module in text a user reads', () => {
    const offenders = all
      .filter((e) => ENTERPRISE.test(e.text) && !ALLOWED.has(e.where))
      .map((e) => `${e.locale} ${e.where}: ${e.text.slice(0, 90)}`)
    expect(
      offenders,
      'Name Business and its family (docs/editions.md); keep keys and codes.',
    ).toEqual([])
  })

  it('keeps the edition names in Latin script', () => {
    const offenders = all
      .filter((e) => TRANSLATED_EDITION_NAMES[e.locale]?.test(e.text))
      .map((e) => `${e.locale} ${e.where}: ${e.text.slice(0, 90)}`)
    expect(
      offenders,
      'Write Community and Business as they are (docs/editions.md).',
    ).toEqual([])
  })

  it('never calls Business a build', () => {
    const offenders = all
      .filter((e) => BUSINESS_BUILD.test(e.text))
      .map((e) => `${e.locale} ${e.where}: ${e.text.slice(0, 90)}`)
    expect(offenders).toEqual([])
  })
})

/**
 * Locales whose sentences are fully translated: no sentence may equal its English text.
 * `scripts/check-i18n-parity.mjs` compares keys only, which let English prose ship
 * in a translated file. Add a locale here when its quality issue lands.
 * ponytail: sentences only (`isProse`); short English labels still pass, so a label
 * sweep needs a per-key allow-list of names and units first.
 */
const PROSE_CHECKED = ['it', 'ja']

/** Same text in every language: language names, and a JSON example the operator edits. */
const SAME_AS_ENGLISH_OK = new Set([
  'settings.general.languageHint',
  'catalog.editor.specPlaceholder',
])

const isProse = (text: string) =>
  text.length >= 40 && text.split(/\s+/).length >= 6

/** Wording a locale settled on: the old form must not come back. */
const BANNED: Record<string, [RegExp, string][]> = {
  ja: [
    [
      /コミュニティ|ビジネス/,
      'Community and Business are edition names: keep them English',
    ],
    [/API[\s\u3000-]+キー/, 'write APIキー, without a space'],
    [/ノーオペレーション/, 'write 処理なし'],
  ],
}

/** Wording a locale settled on that has no old form to ban: the value is pinned. */
const REQUIRED: Record<string, Record<string, string>> = {
  ja: { 'nav.items.estate': 'エステート' },
}

describe('locale wording', () => {
  const english = new Map(
    all.filter((e) => e.locale === 'en').map((e) => [e.where, e.text]),
  )

  it.each(PROSE_CHECKED)('%s has no English sentences left', (locale) => {
    const offenders = all
      .filter(
        (e) =>
          e.locale === locale &&
          isProse(e.text) &&
          e.text === english.get(e.where) &&
          !SAME_AS_ENGLISH_OK.has(e.where),
      )
      .map((e) => `${e.where}: ${e.text.slice(0, 90)}`)
    expect(offenders).toEqual([])
  })

  it.each(Object.keys(REQUIRED))('%s keeps its pinned wording', (locale) => {
    const mismatches = Object.entries(REQUIRED[locale]).flatMap(
      ([where, want]) => {
        const got = all.find(
          (e) => e.locale === locale && e.where === where,
        )?.text
        return got === want ? [] : [`${where}: want ${want}, got ${got}`]
      },
    )
    expect(mismatches).toEqual([])
  })

  it.each(Object.keys(BANNED))('%s keeps its settled wording', (locale) => {
    const offenders = all
      .filter((e) => e.locale === locale)
      .flatMap((e) =>
        BANNED[locale]
          .filter(([banned]) => banned.test(e.text))
          .map(([, why]) => `${e.where} (${why}): ${e.text.slice(0, 60)}`),
      )
    expect(offenders).toEqual([])
  })
})
