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
 * Where "enterprise" is the actual word: the tier name in a sample list the operator
 * types, and the SCIM/audit-export gate, whose placement is the owner's to settle
 * (docs/editions.md puts inbound SCIM in Community).
 */
const ALLOWED = new Set([
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

  it('never calls Business a build', () => {
    const offenders = all
      .filter((e) => BUSINESS_BUILD.test(e.text))
      .map((e) => `${e.locale} ${e.where}: ${e.text.slice(0, 90)}`)
    expect(offenders).toEqual([])
  })
})
