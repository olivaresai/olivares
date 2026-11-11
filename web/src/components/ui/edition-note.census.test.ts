// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// ONE COMPONENT SAYS AN EDITION REFUSAL. The engine refuses a capability above the edition in
// three shapes; `ApiError.isEditionRefusal` is the one reader of their codes (the same decision
// keeps `isForbidden` for the role), `editionRefusal` reads which edition a business_required
// means, and `EditionNote` is the one line that names the edition. A place that matches a
// code itself writes its own sentence beside that line, which is how «requires Business»
// lines were born. This census fails when a console file (tests aside) names one of the
// codes, so the next hand-made line cannot appear. And where a sentence names an edition, it
// names it whole, in every language.
/// <reference types="node" />
import { readFileSync } from 'node:fs'
import { relative, resolve } from 'node:path'
import { describe, expect, it } from 'vitest'
import { LANGUAGE_CODES } from '@/lib/i18n'
import { walk } from '@/test/empty-state-census'

const SRC = resolve(__dirname, '../..')
const CODES = /business_required|edition_required|addon_requires_license/

/** The one reader, the edition line, and the API's own error types. */
const READERS = new Set([
  'lib/api/errors.ts',
  'components/ui/edition-note.tsx',
  'lib/api/types.ts',
  'lib/api/openapi.gen.ts',
])

describe('the three edition refusals are read in one place', () => {
  it('no other console file names their codes', () => {
    const named = walk(SRC, ['.ts', '.tsx'])
      .map((file) => relative(SRC, file))
      .filter((file) => !/\.test\.tsx?$/.test(file) && !READERS.has(file))
      .filter((file) => CODES.test(readFileSync(resolve(SRC, file), 'utf8')))
    expect(named).toEqual([])
  })
})

/** Sentences that name an edition, by catalog file (`{lng}` is the language) and key. */
const NAMING: [string, string][] = [
  ['lib/i18n/locales/{lng}/common.json', 'edition.editingRequiresBusiness'],
  ['lib/i18n/locales/{lng}/settings.json', 'edition.scope'],
  ['lib/i18n/locales/{lng}/settings.json', 'edition.businessAdds'],
  ['lib/i18n/locales/{lng}/settings.json', 'tracing.businessRequired'],
  ['features/console/i18n/{lng}.json', 'activation.unavailable'],
  ['features/console/i18n/{lng}.json', 'secrets.credentialCaption'],
  ['features/eventing/i18n/{lng}.json', 'dialog.businessRequired'],
  ['features/finops/i18n/{lng}.json', 'budgets.storedEmptyHint'],
  ['features/identity/i18n/{lng}.json', 'scim.businessOnly'],
  ['features/observability/i18n/{lng}.json', 'ingestion.status.unavailable'],
]

const WHOLE = /Olivares AI (?:Community|Engineer|Business|Enterprise)/
const BARE = /\b(?:Community|Engineer|Business|Enterprise)\b/

function sentence(file: string, key: string): string {
  let node: unknown = JSON.parse(readFileSync(resolve(SRC, file), 'utf8'))
  for (const part of key.split('.'))
    node = (node as Record<string, unknown> | undefined)?.[part]
  return typeof node === 'string' ? node : ''
}

describe('a sentence that names an edition names it whole', () => {
  it.each(NAMING)('%s %s, in every language', (file, key) => {
    for (const lng of LANGUAGE_CODES) {
      const text = sentence(file.replace('{lng}', lng), key)
      expect(text, `${lng} ${key}`).toMatch(WHOLE)
      const rest = text.replace(new RegExp(WHOLE.source, 'g'), '')
      expect(rest, `${lng} ${key}`).not.toMatch(BARE)
    }
  })
})

/** Every string a catalog holds, with its key path. */
function sentences(node: unknown, path = ''): [string, string][] {
  if (typeof node === 'string') return [[path, node]]
  if (!node || typeof node !== 'object') return []
  return Object.entries(node).flatMap(([key, child]) =>
    sentences(child, path ? `${path}.${key}` : key),
  )
}

// A status code is not a sentence: «…return 501» told a person nothing they could act on.
describe('no catalog sentence says an HTTP status', () => {
  it('no sentence of any catalog, in any language, says «501»', () => {
    const said = walk(SRC, ['.json'])
      .filter((file) => /[/\\]i18n[/\\]/.test(file))
      .flatMap((file) =>
        sentences(JSON.parse(readFileSync(file, 'utf8')))
          .filter(([, text]) => /\b501\b/.test(text))
          .map(([key]) => `${relative(SRC, file)} ${key}`),
      )
    expect(said).toEqual([])
  })
})
